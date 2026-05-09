package anthro

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"
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
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0644)
}

// Tunable knobs. Vars (not consts) so tests can override apiURL.
var (
	apiURL      = "https://api.anthropic.com/api/oauth/usage"
	cacheTTL    = 3 * time.Minute
	httpTimeout = 5 * time.Second
)

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
func Fetch(ctx context.Context, cred Credential, cacheDir string) (FetchResult, error) {
	if cred.AccessToken == "" {
		return FetchResult{}, errors.New("anthro: empty access token")
	}
	cachePath := filepath.Join(cacheDir, "usage.json")
	now := time.Now()

	cached, cacheErr := readCache(cachePath)
	if cacheErr == nil && now.Sub(cached.UpdatedAt) < cacheTTL {
		return FetchResult{Usage: cached.Usage, Source: "cache_fresh", UpdatedAt: cached.UpdatedAt}, nil
	}

	u, err := fetchAPI(ctx, cred.AccessToken)
	if err != nil {
		if cacheErr == nil {
			return FetchResult{Usage: cached.Usage, Source: "cache_stale", UpdatedAt: cached.UpdatedAt}, nil
		}
		return FetchResult{}, fmt.Errorf("anthro fetch: %w", err)
	}
	_ = writeCache(cachePath, u, now)
	return FetchResult{Usage: u, Source: "api", UpdatedAt: now.UTC()}, nil
}

func fetchAPI(ctx context.Context, token string) (Usage, error) {
	ctx, cancel := context.WithTimeout(ctx, httpTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return Usage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Usage{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Usage{}, fmt.Errorf("api status %d", resp.StatusCode)
	}
	var u Usage
	if err := json.NewDecoder(resp.Body).Decode(&u); err != nil {
		return Usage{}, fmt.Errorf("decode response: %w", err)
	}
	return u, nil
}
