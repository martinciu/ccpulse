package anthro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/martinciu/ccpulse/pkg/secfile"
)

// Bucket is one quota dimension (e.g. five_hour, seven_day_sonnet).
// utilization is a 0-100 percent reported by Anthropic; resets_at is the
// server-side reset boundary in RFC3339Nano.
type Bucket struct {
	Utilization float64   `json:"utilization"`
	ResetsAt    time.Time `json:"resets_at"`
}

// ExtraUsage describes the pay-as-you-go credit pool. Fields can be zero/null
// when the feature is disabled; Utilization is *float64 because the API
// returns null when MonthlyLimit is 0.
type ExtraUsage struct {
	IsEnabled    bool     `json:"is_enabled"`
	MonthlyLimit float64  `json:"monthly_limit"`
	UsedCredits  float64  `json:"used_credits"`
	Utilization  *float64 `json:"utilization"`
	Currency     string   `json:"currency"`
}

// Usage mirrors the /api/oauth/usage response 1:1. Null buckets are
// represented as nil pointers so they round-trip faithfully.
type Usage struct {
	FiveHour            *Bucket     `json:"five_hour"`
	SevenDay            *Bucket     `json:"seven_day"`
	SevenDayOauthApps   *Bucket     `json:"seven_day_oauth_apps"`
	SevenDayOpus        *Bucket     `json:"seven_day_opus"`
	SevenDaySonnet      *Bucket     `json:"seven_day_sonnet"`
	SevenDayCowork      *Bucket     `json:"seven_day_cowork"`
	SevenDayOmelette    *Bucket     `json:"seven_day_omelette"`
	Tangelo             *Bucket     `json:"tangelo"`
	IguanaNecktie       *Bucket     `json:"iguana_necktie"`
	OmelettePromotional *Bucket     `json:"omelette_promotional"`
	ExtraUsage          *ExtraUsage `json:"extra_usage"`
}

// cacheVersion bumps when the cache envelope shape changes. Old caches are
// treated as missing and overwritten on next fetch.
const cacheVersion = 1

type cacheEnvelope struct {
	V         int             `json:"v"`
	UpdatedAt time.Time       `json:"updated_at"`
	Data      json.RawMessage `json:"data"`
}

type cachedUsage struct {
	Usage     Usage
	UpdatedAt time.Time
}

func readCache(path string) (cachedUsage, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cachedUsage{}, err
	}
	var env cacheEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return cachedUsage{}, fmt.Errorf("parse cache: %w", err)
	}
	if env.V != cacheVersion {
		return cachedUsage{}, fmt.Errorf("cache version %d, want %d", env.V, cacheVersion)
	}
	var u Usage
	if err := json.Unmarshal(env.Data, &u); err != nil {
		return cachedUsage{}, fmt.Errorf("parse cache data: %w", err)
	}
	return cachedUsage{Usage: u, UpdatedAt: env.UpdatedAt}, nil
}

func writeCache(path string, u Usage, when time.Time) error {
	data, err := json.Marshal(u)
	if err != nil {
		return err
	}
	env := cacheEnvelope{V: cacheVersion, UpdatedAt: when.UTC(), Data: data}
	out, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return err
	}
	if err := secfile.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	return secfile.WriteFileAtomic(path, out)
}

// Tunable knobs. Vars (not consts) so tests can override apiURL.
var (
	apiURL      = "https://api.anthropic.com/api/oauth/usage"
	cacheTTL    = 3 * time.Minute
	httpTimeout = 5 * time.Second
)

// maxBodySnippet bounds the body bytes that fetchAPI surfaces in the
// non-2xx WARN log. Anthropic error bodies are tiny (~80 bytes) and
// CloudFlare 429 HTML is well under 512.
//
// The snippet is wrapped in strconv.Quote at log sites to keep raw ANSI
// escape sequences / CR / NUL bytes from a malicious or MitM'd response
// from executing in the user's terminal when they tail the debug log.
const maxBodySnippet = 512

// maxBodyRead caps the bytes fetchAPI will pull from the response body.
// Defensive: a misbehaving server shouldn't be able to blow memory. The
// real usage endpoint returns ~1 KB; 64 KiB leaves plenty of headroom.
const maxBodyRead = 64 * 1024

// SetAPIURLForTest overrides the usage endpoint URL and returns a restore
// function for the previous value. Intended for test packages outside
// pkg/anthro that need to redirect Fetch at an httptest.Server. Calling
// the returned function in t.Cleanup is the expected pattern.
func SetAPIURLForTest(url string) (restore func()) {
	prev := apiURL
	apiURL = url
	return func() { apiURL = prev }
}

// FetchResult is what Fetch returns to the caller.
type FetchResult struct {
	Usage     Usage
	Source    string    // "api" | "cache_fresh" | "cache_stale"
	UpdatedAt time.Time // when the data was actually pulled from Anthropic
}

// Fetch resolves the usage data per the spec's resolution tree:
//  1. cache exists & fresh           → cache_fresh
//  2. cache exists & stale + API ok  → api (cache rewritten)
//  3. cache exists & stale + API fail → cache_stale
//  4. no cache + API ok              → api (cache written)
//  5. no cache + API fail            → error
//
// On error, callers should fall back to the JSONL heuristic.
//
// Concurrent callers that observe a stale cache serialize behind an advisory
// flock on a sibling lock file: the first caller refreshes the cache, the
// rest re-read under the lock and find a fresh entry — eliminating the
// duplicate-API-hit race that survived the atomic-write fix in #75.
func Fetch(ctx context.Context, cred Credential, cacheDir string) (res FetchResult, err error) {
	if cred.AccessToken == "" {
		return FetchResult{}, errors.New("anthro: empty access token")
	}
	cachePath := filepath.Join(cacheDir, "usage.json")
	now := time.Now()
	var lockAcquired bool

	cached, cacheErr := readCache(cachePath)
	defer func() {
		src := res.Source
		if src == "" && err != nil {
			src = "error"
		}
		attrs := []any{"source", src}
		if cacheErr == nil {
			attrs = append(attrs, "cache_age_s", int(now.Sub(cached.UpdatedAt).Seconds()))
		}
		attrs = append(attrs, "lock_acquired", lockAcquired)
		if err != nil {
			attrs = append(attrs, "err", err)
		}
		slog.Debug("anthro.Fetch", attrs...)
	}()

	if cacheErr == nil && now.Sub(cached.UpdatedAt) < cacheTTL {
		return FetchResult{Usage: cached.Usage, Source: "cache_fresh", UpdatedAt: cached.UpdatedAt}, nil
	}

	if release, lockErr := acquireFetchLock(cacheDir); lockErr == nil {
		lockAcquired = true
		defer release()
		now = time.Now()
		cached, cacheErr = readCache(cachePath)
		if cacheErr == nil && now.Sub(cached.UpdatedAt) < cacheTTL {
			return FetchResult{Usage: cached.Usage, Source: "cache_fresh", UpdatedAt: cached.UpdatedAt}, nil
		}
	}

	u, apiErr := fetchAPI(ctx, cred.AccessToken)
	if apiErr != nil {
		if cacheErr == nil {
			return FetchResult{Usage: cached.Usage, Source: "cache_stale", UpdatedAt: cached.UpdatedAt}, nil
		}
		return FetchResult{}, fmt.Errorf("anthro fetch: %w", apiErr)
	}
	if werr := writeCache(cachePath, u, now); werr != nil {
		slog.Warn("anthro.writeCache",
			"path", cachePath,
			"err", werr)
	}
	return FetchResult{Usage: u, Source: "api", UpdatedAt: now.UTC()}, nil
}

// acquireFetchLock takes an exclusive advisory lock on cacheDir/usage.json.lock
// via flock(2). The returned closure releases the lock and closes the file;
// callers must invoke it (typically via defer). The lockfile is a sibling of
// usage.json so atomic-rename writes to the cache don't disturb the lock fd.
//
// On a system where flock fails (e.g. ENOLCK on a quirky filesystem), Fetch
// degrades to its pre-#76 behaviour rather than refusing to fetch.
func acquireFetchLock(cacheDir string) (release func(), err error) {
	if err := secfile.MkdirAll(cacheDir); err != nil {
		return nil, err
	}
	f, err := secfile.OpenFile(filepath.Join(cacheDir, "usage.json.lock"), os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, nil
}

// fetchAPI emits exactly one slog record per call. The four message keys
// (anthro.fetchAPI, anthro.fetchAPI non-2xx, anthro.fetchAPI transport error,
// anthro.fetchAPI decode) are distinct because each variant carries a
// different attribute shape (status+body_snippet vs err vs status+body+err).
// Compare with runQuotaPoller in cmd/ccpulse/main.go, which uses one
// message ("ccpulse.quotaPoller") plus an "outcome" attribute because its
// branches share the same attribute shape. Rule of thumb: one msg per
// distinct attribute shape; vary outcome by attribute value when shapes match.
func fetchAPI(ctx context.Context, token string) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")

	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		slog.Warn("anthro.fetchAPI transport error",
			"dur_ms", time.Since(start).Milliseconds(),
			"err", err)
		return Usage{}, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBodyRead))
	snippet := body
	if len(snippet) > maxBodySnippet {
		snippet = snippet[:maxBodySnippet]
	}
	durMS := time.Since(start).Milliseconds()

	if resp.StatusCode != http.StatusOK {
		// fetchAPI is the only layer with HTTP detail; log here AND return
		// a sentinel error for caller branching. Not a duplicate-handling
		// violation — upstream layers log different content at different
		// severity.
		// strconv.Quote escapes ANSI/CR/control bytes in body_snippet so a
		// malicious or MitM'd response can't inject terminal-escape
		// sequences into the log file (which is plain bytes; `tail`/`cat`
		// would interpret). The "err" attribute is intentionally not
		// Quote'd: error strings here surface only Go-internal text
		// (status code, decode-offset, url.Error with host but no body
		// bytes), not attacker-controlled response payload.
		slog.Warn("anthro.fetchAPI non-2xx",
			"status", resp.StatusCode,
			"dur_ms", durMS,
			"body_snippet", strconv.Quote(string(snippet)))
		return Usage{}, fmt.Errorf("api status %d", resp.StatusCode)
	}

	var u Usage
	if err := json.Unmarshal(body, &u); err != nil {
		slog.Warn("anthro.fetchAPI decode",
			"status", resp.StatusCode,
			"dur_ms", durMS,
			"body_snippet", strconv.Quote(string(snippet)),
			"err", err)
		return Usage{}, fmt.Errorf("decode response: %w", err)
	}
	slog.Debug("anthro.fetchAPI",
		"status", resp.StatusCode,
		"dur_ms", durMS)
	return u, nil
}
