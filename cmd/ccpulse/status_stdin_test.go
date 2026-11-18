package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

const overlayFile = "usage-statusline.json"

// statuslineEnv isolates one status run: temp cache dir, temp HOME (with a
// credential only when withCred), config under that HOME, keychain off so a
// developer machine's real credential can never reach the network.
func statuslineEnv(t *testing.T, withCred bool) (cacheDir string) {
	t.Helper()
	cacheDir = t.TempDir()
	home := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("CCPULSE_DISABLE_KEYCHAIN", "1")
	if withCred {
		writeTempCredential(t, home)
	}
	return cacheDir
}

// touchFreshTranscript creates a transcript file under dir, modified now.
func touchFreshTranscript(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

var payloadSeq atomic.Int64

// fixturePayload loads the captured statusline fixture as a generic map,
// applies mutate, and re-encodes it.
func fixturePayload(t *testing.T, mutate func(p map[string]any)) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "pkg", "anthro", "testdata", "statusline-payload.json"))
	if err != nil {
		t.Fatal(err)
	}
	var p map[string]any
	if err := json.Unmarshal(b, &p); err != nil {
		t.Fatal(err)
	}
	mutate(p)
	out, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// statuslinePayload is the fixture with live rate_limits (5h resets in 2h,
// 7d in 3d; a negative value omits that bucket), a freshly touched
// transcript and a unique session id. Without the fresh transcript the
// reading would come from an unknown session with no activity and never be
// stored.
func statuslinePayload(t *testing.T, five, seven float64) []byte {
	t.Helper()
	now := time.Now()
	return fixturePayload(t, func(p map[string]any) {
		rl := map[string]any{}
		if five >= 0 {
			rl["five_hour"] = map[string]any{"used_percentage": five, "resets_at": now.Add(2 * time.Hour).Unix()}
		}
		if seven >= 0 {
			rl["seven_day"] = map[string]any{"used_percentage": seven, "resets_at": now.Add(72 * time.Hour).Unix()}
		}
		p["rate_limits"] = rl
		p["transcript_path"] = touchFreshTranscript(t, t.TempDir())
		p["session_id"] = fmt.Sprintf("test-session-%d", payloadSeq.Add(1))
	})
}

// execStatus runs a fresh status command. stdin nil leaves InOrStdin unset.
func execStatus(t *testing.T, stdin io.Reader, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	cmd := newStatusCmd()
	cmd.SetArgs(args)
	if stdin != nil {
		cmd.SetIn(stdin)
	}
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	err = cmd.ExecuteContext(t.Context())
	return out.String(), errBuf.String(), err
}

func parseStatusJSON(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("status output is not JSON: %v\n%s", err, s)
	}
	return m
}

func overlayExists(t *testing.T, cacheDir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(cacheDir, overlayFile))
	return err == nil
}

// withUsageServer points anthro.Fetch at handler for the rest of the test.
func withUsageServer(t *testing.T, handler http.HandlerFunc) *atomic.Int64 {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(anthro.SetAPIURLForTest(srv.URL))
	return &hits
}

func TestStatusStdin_IngestsAndReportsStatusline(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	out, _, err := execStatus(t, bytes.NewReader(statuslinePayload(t, 12, 60)), "--stdin", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	m := parseStatusJSON(t, out)
	if m["quota_source"] != "statusline" {
		t.Errorf("quota_source = %v, want statusline", m["quota_source"])
	}
	if m["percent_7d"] != 60.0 {
		t.Errorf("percent_7d = %v, want 60", m["percent_7d"])
	}
	if m["percent"] != 12.0 {
		t.Errorf("percent = %v, want 12", m["percent"])
	}
	ts, _ := m["quota_updated_at"].(string)
	at, perr := time.Parse(time.RFC3339Nano, ts)
	if perr != nil || time.Since(at) > 5*time.Second || time.Since(at) < -time.Second {
		t.Errorf("quota_updated_at = %q, want within 5s of now", ts)
	}
	if _, ok := m["api_updated_at"]; ok {
		t.Errorf("api_updated_at present with no API data: %v", m["api_updated_at"])
	}
	fi, err := os.Stat(filepath.Join(cacheDir, overlayFile))
	if err != nil {
		t.Fatalf("overlay not written: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("overlay mode = %o, want 600", perm)
	}
}

// Non-subscriber payloads leave status --json unchanged.
func TestStatusStdin_NoRateLimitsMatchesPlainStatus(t *testing.T) {
	payloads := map[string][]byte{
		"no rate_limits": fixturePayload(t, func(p map[string]any) { delete(p, "rate_limits") }),
		"only spend_limit": fixturePayload(t, func(p map[string]any) {
			p["rate_limits"] = map[string]any{"spend_limit": map[string]any{"used": 3, "limit": 100}}
		}),
	}
	for name, payload := range payloads {
		t.Run(name, func(t *testing.T) {
			cacheDir := statuslineEnv(t, true)
			writeTempCache(t, cacheDir)
			withUsageServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, sampleAPIBody) })

			plainOut, _, err := execStatus(t, bytes.NewReader(nil), "--json")
			if err != nil {
				t.Fatalf("plain status: %v", err)
			}
			stdinOut, _, err := execStatus(t, bytes.NewReader(payload), "--stdin", "--json")
			if err != nil {
				t.Fatalf("status --stdin: %v", err)
			}
			plain, withStdin := parseStatusJSON(t, plainOut), parseStatusJSON(t, stdinOut)
			for _, k := range []string{"quota_source", "percent", "percent_7d", "api_updated_at", "quota_updated_at"} {
				if fmt.Sprint(plain[k]) != fmt.Sprint(withStdin[k]) {
					t.Errorf("%s: plain %v, --stdin %v", k, plain[k], withStdin[k])
				}
			}
			if (plain["quota"] == nil) != (withStdin["quota"] == nil) {
				t.Errorf("quota presence differs: plain %v, --stdin %v", plain["quota"] != nil, withStdin["quota"] != nil)
			}
			if overlayExists(t, cacheDir) {
				t.Error("overlay file created for a payload without usable rate_limits")
			}
		})
	}
}

func TestStatusStdin_GarbageInputStillReports(t *testing.T) {
	for name, in := range map[string][]byte{
		"not json": []byte("not json"),
		"empty":    {},
		"2 MiB":    bytes.Repeat([]byte("x"), 2<<20),
	} {
		t.Run(name, func(t *testing.T) {
			cacheDir := statuslineEnv(t, false)
			out, stderr, err := execStatus(t, bytes.NewReader(in), "--stdin", "--json")
			if err != nil {
				t.Fatalf("status: %v", err)
			}
			if stderr != "" {
				t.Errorf("stderr = %q; bad statusline input must stay silent", stderr)
			}
			m := parseStatusJSON(t, out)
			if m["ceiling_label"] != "unknown" {
				t.Errorf("ceiling_label = %v, want the normal no-credential report", m["ceiling_label"])
			}
			if overlayExists(t, cacheDir) {
				t.Error("overlay written for garbage input")
			}
		})
	}
}

func TestStatusStdin_TerminalStdinErrors(t *testing.T) {
	statuslineEnv(t, false)
	// A character device that is not /dev/null stands in for a terminal:
	// same file mode, and like a tty it would never deliver a payload.
	f, err := os.Open("/dev/zero")
	if err != nil {
		t.Skipf("no /dev/zero: %v", err)
	}
	defer f.Close()
	start := time.Now()
	_, _, err = execStatus(t, f, "--stdin", "--json")
	if err == nil || !strings.Contains(err.Error(), "stdin is a terminal") {
		t.Fatalf("err = %v, want the terminal-stdin usage error", err)
	}
	if el := time.Since(start); el > 400*time.Millisecond {
		t.Errorf("took %v; a terminal stdin must error immediately, not wait", el)
	}
}

// /dev/null is what tmux #(), cron and launchd hand a command, and what Go
// reopens a closed fd 0 as. It is missing input, not a terminal: report as
// usual, exit 0 — missing input must never change the exit code.
func TestStatusStdin_DevNullIsEmptyInput(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	f, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	out, stderr, err := execStatus(t, f, "--stdin", "--json")
	if err != nil {
		t.Fatalf("status --stdin </dev/null: %v", err)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want silence", stderr)
	}
	if m := parseStatusJSON(t, out); m["ceiling_label"] != "unknown" {
		t.Errorf("ceiling_label = %v, want the normal no-credential report", m["ceiling_label"])
	}
	if overlayExists(t, cacheDir) {
		t.Error("overlay written for empty input")
	}
}

func TestStatusStdin_FetchFailureStillShowsOverlay(t *testing.T) {
	statuslineEnv(t, true) // no usage.json: today this renders no quota at all
	hits := withUsageServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	out, _, err := execStatus(t, bytes.NewReader(statuslinePayload(t, -1, 58)), "--stdin", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	m := parseStatusJSON(t, out)
	if m["quota_source"] != "statusline" || m["percent_7d"] != 58.0 {
		t.Errorf("quota_source=%v percent_7d=%v, want statusline / 58", m["quota_source"], m["percent_7d"])
	}
	if hits.Load() != 1 {
		t.Errorf("API hits = %d, want 1 (the failed attempt)", hits.Load())
	}
	if _, ok := m["api_updated_at"]; ok {
		t.Error("api_updated_at present though no API data exists")
	}
}

func TestStatusStdin_MergesAPILimits(t *testing.T) {
	cacheDir := statuslineEnv(t, true)
	writeTempCache(t, cacheDir) // 30 s old, sampleAPIBody: 7d=89, Fable scoped limit
	hits := withUsageServer(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, sampleAPIBody) })

	out, _, err := execStatus(t, bytes.NewReader(statuslinePayload(t, -1, 61)), "--stdin", "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	m := parseStatusJSON(t, out)
	if m["quota_source"] != "statusline" || m["percent_7d"] != 61.0 {
		t.Errorf("quota_source=%v percent_7d=%v, want statusline / 61 (overlay over the API's 89)", m["quota_source"], m["percent_7d"])
	}
	scoped, _ := m["scoped_limits"].([]any)
	if len(scoped) == 0 {
		t.Fatalf("scoped_limits missing: %s", out)
	}
	if first, _ := scoped[0].(map[string]any); first["model"] != "Fable" {
		t.Errorf("scoped_limits[0].model = %v, want Fable", first["model"])
	}
	if _, ok := m["api_updated_at"].(string); !ok {
		t.Errorf("api_updated_at missing: %s", out)
	}
	if hits.Load() != 0 {
		t.Errorf("API hits = %d, want 0 (cache fresh)", hits.Load())
	}
}

func countSamplesBySource(t *testing.T, dbPath, source string) int {
	t.Helper()
	c, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM usage_samples WHERE source = ?`, source).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStatusStdin_RecordsSettledSampleOnce(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	now := time.Now()
	resets := now.Add(72 * time.Hour).UTC()
	// A reading accepted 40 s ago: held long enough to be settled.
	if _, err := anthro.IngestStatusline(cacheDir, anthro.StatuslineReading{
		SevenDay: &anthro.Bucket{Utilization: 60, ResetsAt: &resets},
	}, now.Add(-40*time.Second)); err != nil {
		t.Fatal(err)
	}
	// `{}` ingests nothing, so the seeded reading stays the stored winner.
	for range 2 {
		if _, _, err := execStatus(t, strings.NewReader("{}"), "--stdin", "--quiet"); err != nil {
			t.Fatalf("status: %v", err)
		}
	}
	if n := countSamplesBySource(t, filepath.Join(cacheDir, "state.db"), cache.SampleSourceStatusline); n != 1 {
		t.Errorf("statusline samples = %d, want exactly 1", n)
	}
	// The row count alone can't tell a marked sample from an INSERT OR
	// IGNORE collision; the overlay must also say it was recorded.
	if _, _, ok := anthro.PendingStatuslineSample(cacheDir, time.Now()); ok {
		t.Error("sample still pending after status recorded it (MarkStatuslineRecorded not called)")
	}
}

func TestStatusStdin_IngestBeforeDBOpen(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	if err := os.MkdirAll(filepath.Join(cacheDir, "state.db"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, _, err := execStatus(t, bytes.NewReader(statuslinePayload(t, -1, 60)), "--stdin", "--quiet")
	if err == nil {
		t.Fatal("status err = nil, want the cache-open error")
	}
	if !overlayExists(t, cacheDir) {
		t.Error("overlay not written although ingestion runs before the DB is opened")
	}
}

func TestStatusStdin_NoSentinelsInLogs(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	prevLevel, prevLogger := resolvedLogLevel, slog.Default()
	t.Cleanup(func() { resolvedLogLevel = prevLevel; slog.SetDefault(prevLogger) })

	const sentinel = "PRIVACY-SENTINEL-568"
	transcript := touchFreshTranscript(t, filepath.Join(t.TempDir(), sentinel+"-dir"))
	payload := fixturePayload(t, func(p map[string]any) {
		p["cwd"] = "/" + sentinel + "-cwd"
		p["session_id"] = sentinel + "-session"
		p["transcript_path"] = transcript
		p["workspace"] = map[string]any{"current_dir": "/" + sentinel + "-ws", "project_dir": "/" + sentinel + "-proj"}
		p["rate_limits"] = map[string]any{"seven_day": map[string]any{
			"used_percentage": 60, "resets_at": time.Now().Add(72 * time.Hour).Unix(),
		}}
	})

	root := newRootCmd()
	root.SetArgs([]string{"status", "--stdin", "--quiet", "--log-level", "debug"})
	root.SetIn(bytes.NewReader(payload))
	root.SetOut(io.Discard)
	root.SetErr(io.Discard)
	if err := root.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("status: %v", err)
	}

	if !overlayExists(t, cacheDir) {
		t.Fatal("overlay not written: ingestion did not run, so the log proves nothing")
	}
	logBytes, err := os.ReadFile(filepath.Join(cacheDir, "debug.log"))
	if err != nil {
		t.Fatalf("debug.log: %v", err)
	}
	if !bytes.Contains(logBytes, []byte("ccpulse.statuslineIngest")) {
		t.Fatalf("debug.log has no ingest line; the sentinel check would pass vacuously:\n%s", logBytes)
	}
	if bytes.Contains(logBytes, []byte(sentinel)) {
		t.Errorf("debug.log leaks payload content:\n%s", logBytes)
	}
}

func TestStatusWithoutStdin_IgnoresStdin(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	if _, _, err := execStatus(t, bytes.NewReader(statuslinePayload(t, 12, 60)), "--json"); err != nil {
		t.Fatalf("status: %v", err)
	}
	if overlayExists(t, cacheDir) {
		t.Error("overlay written without --stdin")
	}
}

// The Stop hook's `status --quiet` and any prompt integration benefit from
// the overlay without passing --stdin themselves.
func TestStatusPlain_UsesExistingOverlay(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	resets := time.Now().Add(72 * time.Hour).UTC()
	if _, err := anthro.IngestStatusline(cacheDir, anthro.StatuslineReading{
		SevenDay: &anthro.Bucket{Utilization: 60, ResetsAt: &resets},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	out, _, err := execStatus(t, bytes.NewReader(nil), "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	m := parseStatusJSON(t, out)
	if m["quota_source"] != "statusline" || m["percent_7d"] != 60.0 {
		t.Errorf("quota_source=%v percent_7d=%v, want statusline / 60", m["quota_source"], m["percent_7d"])
	}
}

func TestStatusStdin_SettledSampleShiftsPastAPIRowSameSecond(t *testing.T) {
	cacheDir := statuslineEnv(t, false)
	at := time.Now().Add(-40 * time.Second)
	resets := at.Add(72 * time.Hour).UTC()
	if _, err := anthro.IngestStatusline(cacheDir, anthro.StatuslineReading{
		SevenDay: &anthro.Bucket{Utilization: 60, ResetsAt: &resets},
	}, at); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsageSample(t.Context(), anthro.Usage{
		SevenDay: &anthro.Bucket{Utilization: 50, ResetsAt: &resets},
	}, at); err != nil {
		t.Fatal(err)
	}
	c.Close()

	if _, _, err := execStatus(t, strings.NewReader("{}"), "--stdin", "--quiet"); err != nil {
		t.Fatalf("status: %v", err)
	}
	c, err = cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var src string
	if err := c.DB().QueryRowContext(t.Context(), `SELECT source FROM usage_samples WHERE ts = ?`, at.UTC().Unix()+1).Scan(&src); err != nil {
		t.Fatalf("no row at second+1: %v", err)
	}
	if src != cache.SampleSourceStatusline {
		t.Errorf("row at second+1 source = %q, want statusline", src)
	}
}

// A statusline reading accepted after the API fetch wins the merge, yet the
// 'api' usage_samples row must keep the API's raw numbers: recording the merged
// quota would file the statusline's value under the wrong source.
func TestStatus_RecordsRawAPIWhenOverlayWins(t *testing.T) {
	cacheDir := statuslineEnv(t, true)
	resets := time.Now().Add(72 * time.Hour).UTC()
	var ingestErr error
	withUsageServer(t, func(w http.ResponseWriter, _ *http.Request) {
		// Lands while the fetch is in flight, stamped after the API result's
		// UpdatedAt so the overlay wins the freshness merge.
		_, ingestErr = anthro.IngestStatusline(cacheDir, anthro.StatuslineReading{
			SevenDay: &anthro.Bucket{Utilization: 60, ResetsAt: &resets},
		}, time.Now().Add(time.Minute))
		_, _ = fmt.Fprint(w, sampleAPIBody) // raw 7d = 89
	})

	out, _, err := execStatus(t, bytes.NewReader(nil), "--json")
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if ingestErr != nil {
		t.Fatalf("ingest during fetch: %v", ingestErr)
	}
	if got := parseStatusJSON(t, out)["quota_source"]; got != "statusline" {
		t.Fatalf("quota_source = %v, want statusline (overlay must win the merge)", got)
	}

	dbPath := filepath.Join(cacheDir, "state.db")
	if n := countSamplesBySource(t, dbPath, "api"); n != 1 {
		t.Fatalf("api samples = %d, want 1", n)
	}
	c, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var pct float64
	if err := c.DB().QueryRowContext(t.Context(), `SELECT seven_day_pct FROM usage_samples WHERE source = 'api'`).Scan(&pct); err != nil {
		t.Fatal(err)
	}
	if pct != 89 {
		t.Errorf("api row seven_day_pct = %v, want the raw API value 89 (not the overlay's 60)", pct)
	}
}
