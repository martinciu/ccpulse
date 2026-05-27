package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
)

const sampleAPIBody = `{
  "five_hour":            {"utilization": 5.0,  "resets_at": "2126-01-01T00:00:00.000+00:00"},
  "seven_day":            {"utilization": 89.0, "resets_at": "2126-01-02T00:00:00.000+00:00"},
  "seven_day_oauth_apps": null,
  "seven_day_opus":       null,
  "seven_day_sonnet":     {"utilization": 5.0,  "resets_at": "2126-01-02T00:00:00.000+00:00"},
  "seven_day_cowork":     null,
  "seven_day_omelette":   {"utilization": 21.0, "resets_at": "2126-01-02T00:00:00.000+00:00"},
  "tangelo":              null,
  "iguana_necktie":       null,
  "omelette_promotional": null,
  "extra_usage":          {"is_enabled": true, "monthly_limit": 2000, "used_credits": 0.0, "utilization": null, "currency": "EUR"}
}`

func writeTempCache(t *testing.T, dir string) {
	t.Helper()
	body := `{"v":1,"updated_at":"` + time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339Nano) + `","data":` + sampleAPIBody + `}`
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeTempCredential(t *testing.T, dir string) {
	t.Helper()
	body := `{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"max","rateLimitTier":"default_claude_max_20x","expiresAt":4070908800000}}`
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestStatusJSONWithCachedUsage(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()

	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCache(t, cacheDir)
	writeTempCredential(t, credDir)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"ceiling_label":"max_20x"`) {
		t.Errorf("missing ceiling_label: %s", out)
	}
	if !strings.Contains(out, `"ceiling_pretty":"Max 20x"`) {
		t.Errorf("missing ceiling_pretty: %s", out)
	}
	if !strings.Contains(out, `"quota":`) {
		t.Errorf("missing quota: %s", out)
	}
	if !strings.Contains(out, `"quota_source":"cache_fresh"`) {
		t.Errorf("missing quota_source=cache_fresh: %s", out)
	}
	if !strings.Contains(out, `"five_hour":`) {
		t.Errorf("missing five_hour: %s", out)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if int(parsed["percent"].(float64)) != 5 {
		t.Errorf("percent = %v, want 5", parsed["percent"])
	}
	if !strings.Contains(out, `"projection":`) {
		t.Errorf("missing projection: %s", out)
	}
	// 5h projection is always present in this fixture (FiveHour bucket non-nil).
	proj, ok := parsed["projection"].(map[string]any)
	if !ok {
		t.Fatalf("projection is not an object: %v", parsed["projection"])
	}
	if _, ok := proj["five_hour"]; !ok {
		t.Errorf("projection.five_hour missing: %v", proj)
	}
}

func TestStatusJSONWithoutCredential(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir() // no .credentials.json inside
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	t.Setenv("CCPULSE_DISABLE_KEYCHAIN", "1")

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, `"quota":`) {
		t.Errorf("quota should be omitted when no credential: %s", out)
	}
	if strings.Contains(out, `"quota_updated_at":`) {
		t.Errorf("quota_updated_at should be omitted when no credential: %s", out)
	}
	if !strings.Contains(out, `"ceiling_label":"unknown"`) {
		t.Errorf("expected ceiling_label=unknown without cred: %s", out)
	}
	if strings.Contains(out, `"projection":`) {
		t.Errorf("projection should be omitted when no quota: %s", out)
	}
}

// stubUsageServer returns a server that always answers `sampleAPIBody`
// with a 200. The handler counts hits via `*hits`.
func stubUsageServer(t *testing.T, hits *int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, sampleAPIBody)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// countSamples opens state.db via cache.Open (avoids needing the sqlite
// driver import in this test file) and returns the number of usage_samples rows.
func countSamples(t *testing.T, dbPath string) int {
	t.Helper()
	c, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM usage_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestStatusRecordsUsageSampleOnAPIFetch(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCredential(t, credDir)
	// No usage.json in cacheDir → first call must hit the API.

	var hits int
	srv := stubUsageServer(t, &hits)
	restore := anthro.SetAPIURLForTest(srv.URL)
	t.Cleanup(restore)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if hits != 1 {
		t.Fatalf("API hit count = %d, want 1", hits)
	}
	if got := countSamples(t, filepath.Join(cacheDir, "state.db")); got != 1 {
		t.Fatalf("usage_samples rows = %d, want 1 after first API fetch", got)
	}
}

func TestStatusSkipsRecordOnCacheFresh(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCredential(t, credDir)
	writeTempCache(t, cacheDir) // 30s old, well within cacheTTL

	var hits int
	srv := stubUsageServer(t, &hits)
	restore := anthro.SetAPIURLForTest(srv.URL)
	t.Cleanup(restore)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if hits != 0 {
		t.Fatalf("API hit count = %d, want 0 (cache_fresh path)", hits)
	}
	if got := countSamples(t, filepath.Join(cacheDir, "state.db")); got != 0 {
		t.Fatalf("usage_samples rows = %d, want 0 on cache_fresh", got)
	}
}

// TestStatusPercentDisplayWithOAuthAtZero pins the intent of "auto" display
// mode: when an OAuth credential is present, output uses the percent format
// regardless of current utilization. The previous heuristic gated on
// `q.Usage != nil`; the current `if w.Percent > 0` check incorrectly falls
// into the cost branch for fresh windows. Failing this test signals the
// regression — fix is in cmd/ccpulse/status.go runStatus.
func TestStatusPercentDisplayWithOAuthAtZero(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCredential(t, credDir)

	zeroBody := `{"v":1,"updated_at":"` + time.Now().UTC().Add(-30*time.Second).Format(time.RFC3339Nano) + `","data":{
		"five_hour":            {"utilization": 0.0, "resets_at": "2126-01-01T00:00:00.000+00:00"},
		"seven_day":            {"utilization": 0.0, "resets_at": "2126-01-02T00:00:00.000+00:00"},
		"seven_day_oauth_apps": null,
		"seven_day_opus":       null,
		"seven_day_sonnet":     null,
		"seven_day_cowork":     null,
		"seven_day_omelette":   null,
		"tangelo":              null,
		"iguana_necktie":       null,
		"omelette_promotional": null,
		"extra_usage":          {"is_enabled": false, "monthly_limit": 0, "used_credits": 0.0, "utilization": null, "currency": "USD"}
	}}`
	if err := os.WriteFile(filepath.Join(cacheDir, "usage.json"), []byte(zeroBody), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "0%") {
		t.Errorf("OAuth user at 0%% should see percent format, got: %q", out)
	}
	if strings.Contains(out, "$") {
		t.Errorf("OAuth user at 0%% should not see cost format, got: %q", out)
	}
}

// TestStatusNoOAuthShowsNoQuotaNotice pins the API/no-OAuth branch:
// without a credential, plain `ccpulse status` (no flags) prints a notice
// pointing the user at `--json` and `claude /login` rather than a meaningless
// 0% or $0.00 line.
func TestStatusNoOAuthShowsNoQuotaNotice(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Keychain may have a real credential on dev machines; skipping no-cred test on darwin")
	}
	cacheDir := t.TempDir()
	credDir := t.TempDir() // no .credentials.json inside
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)

	cmd := newStatusCmd()
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no quota data") {
		t.Errorf("missing 'no quota data' notice: %q", out)
	}
	if !strings.Contains(out, "--json") {
		t.Errorf("notice should suggest --json: %q", out)
	}
	if strings.Contains(out, "%") || strings.Contains(out, "$") {
		t.Errorf("notice should not include percent/cost numbers: %q", out)
	}
}

// TestStatusExpiredCredentialWritesToCmdErr asserts that the "OAuth credential
// expired" diagnostic is written to the cobra command's error writer (set via
// cmd.SetErr), not to os.Stderr.
func TestStatusExpiredCredentialWritesToCmdErr(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	t.Setenv("CCPULSE_DISABLE_KEYCHAIN", "1")

	// Write a credential whose expiresAt is epoch+1s (well in the past).
	body := `{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"max","rateLimitTier":"default_claude_max_20x","expiresAt":1000}}`
	claudeDir := filepath.Join(credDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	// Ignore the error return — the command may succeed (quota fetch fails gracefully).
	_ = cmd.Execute()

	if !strings.Contains(errBuf.String(), "OAuth credential expired") {
		t.Errorf("expected 'OAuth credential expired' in stderr buffer, got: %q", errBuf.String())
	}
}

func TestStatusTmuxFlagRemoved(t *testing.T) {
	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--tmux"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for --tmux flag, got nil")
	}
	if !strings.Contains(err.Error(), "unknown flag") && !strings.Contains(buf.String(), "unknown flag") {
		t.Errorf("error should mention 'unknown flag': %v / %s", err, buf.String())
	}
}

// writeOverreachCache writes a usage.json with resets_at anchored to now,
// chosen so that the 5h bucket projects > 100% utilisation at reset.
//
// Math: with utilization=50 and resets_at = now + 4h (elapsed = 1h of a 5h
// window), projected_pct_at_reset = 50 * 5 / 1 = 250.
func writeOverreachCache(t *testing.T, dir string) {
	t.Helper()
	now := time.Now().UTC()
	resets5h := now.Add(4 * time.Hour).Format(time.RFC3339Nano)
	resets7d := now.Add(6 * 24 * time.Hour).Format(time.RFC3339Nano)
	body := `{"v":1,"updated_at":"` + now.Add(-30*time.Second).Format(time.RFC3339Nano) + `","data":{
		"five_hour":            {"utilization": 50.0, "resets_at": "` + resets5h + `"},
		"seven_day":            {"utilization": 10.0, "resets_at": "` + resets7d + `"},
		"seven_day_oauth_apps": null,
		"seven_day_opus":       null,
		"seven_day_sonnet":     null,
		"seven_day_cowork":     null,
		"seven_day_omelette":   null,
		"tangelo":              null,
		"iguana_necktie":       null,
		"omelette_promotional": null,
		"extra_usage":          {"is_enabled": false, "monthly_limit": 0, "used_credits": 0.0, "utilization": null, "currency": "USD"}
	}}`
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusJSONProjectionOverreach(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeOverreachCache(t, cacheDir)
	writeTempCredential(t, credDir)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	out := buf.String()

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	proj, ok := parsed["projection"].(map[string]any)
	if !ok {
		t.Fatalf("projection missing or not an object: %v", parsed["projection"])
	}
	fh, ok := proj["five_hour"].(map[string]any)
	if !ok {
		t.Fatalf("projection.five_hour missing: %v", proj)
	}
	if will, _ := fh["will_overreach"].(bool); !will {
		t.Errorf("five_hour.will_overreach = false, want true (out=%s)", out)
	}
	if conf, _ := fh["confidence"].(string); conf != "ok" {
		t.Errorf("five_hour.confidence = %q, want \"ok\" (out=%s)", conf, out)
	}
	// minutes_to_100_pct must be a positive number when overreaching:
	// negative or zero would mean we've already crossed the threshold, in
	// which case the field should be nil (see "already over 100" unit case).
	mins, isNum := fh["minutes_to_100_pct"].(float64)
	if !isNum {
		t.Errorf("five_hour.minutes_to_100_pct expected number, got %T (out=%s)",
			fh["minutes_to_100_pct"], out)
	} else if mins <= 0 {
		t.Errorf("five_hour.minutes_to_100_pct = %v, want > 0 (out=%s)", mins, out)
	}
	// 7d at 10% with ~1d elapsed projects ~70 — no overreach.
	if sd, ok := proj["seven_day"].(map[string]any); ok {
		if will, _ := sd["will_overreach"].(bool); will {
			t.Errorf("seven_day.will_overreach = true, want false (out=%s)", out)
		}
	}
}

func TestStatusQuietSuppressesStdout(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCache(t, cacheDir)
	writeTempCredential(t, credDir)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--quiet"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --quiet: %v", err)
	}
	if got := out.String(); got != "" {
		t.Errorf("stdout should be empty with --quiet, got: %q", got)
	}
}

func TestStatusQuietStillRecordsSample(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCredential(t, credDir)

	// Spin up a fake Anthropic API so anthro.Fetch returns source=api.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleAPIBody))
	}))
	t.Cleanup(srv.Close)
	restore := anthro.SetAPIURLForTest(srv.URL)
	t.Cleanup(restore)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--quiet"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status --quiet: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("stdout should be empty, got: %q", out.String())
	}

	// Verify the row landed in usage_samples.
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatalf("reopen cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	var count int
	if err := c.DB().QueryRow(`SELECT COUNT(*) FROM usage_samples`).Scan(&count); err != nil {
		t.Fatalf("count usage_samples: %v", err)
	}
	if count == 0 {
		t.Errorf("expected at least one usage_samples row after --quiet fetch, got 0")
	}
}

func TestStatusQuietHardErrorStillExitsNonZero(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)

	// Make state.db a directory so cache.Open fails.
	if err := os.MkdirAll(filepath.Join(cacheDir, "state.db"), 0o700); err != nil {
		t.Fatalf("setup: %v", err)
	}

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--quiet"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected non-nil error from hard cache-open failure with --quiet, got nil")
	}
}

func TestStatusQuietStillEmitsStderrDiagnostics(t *testing.T) {
	// intent: --quiet is stdout-only — stderr diagnostics must still flow.
	// we force the "OAuth credential expired" diagnostic by writing a credential
	// with expiresAt in the past, then assert stderr contains the marker.
	// stdout must remain empty.
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	t.Setenv("CCPULSE_DISABLE_KEYCHAIN", "1")

	// Write a credential whose expiresAt is epoch+1ms (well in the past).
	body := `{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"max","rateLimitTier":"default_claude_max_20x","expiresAt":1}}`
	claudeDir := filepath.Join(credDir, ".claude")
	if err := os.MkdirAll(claudeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--quiet"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	// Expired credential is a soft failure; status still computes via JSONL fallback.
	_ = cmd.Execute()

	if out.Len() != 0 {
		t.Errorf("stdout should be empty with --quiet, got: %q", out.String())
	}
	if !strings.Contains(errBuf.String(), "OAuth credential expired") {
		t.Errorf("expected 'OAuth credential expired' in stderr buffer, got: %q", errBuf.String())
	}
}

func TestStatusJSONQuietMutuallyExclusive(t *testing.T) {
	t.Setenv("CCPULSE_CACHE_DIR", t.TempDir())
	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json", "--quiet"})
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	err := cmd.Execute()
	if err == nil {
		t.Fatalf("expected error for mutually exclusive flags, got nil")
	}
	if !strings.Contains(err.Error(), "json") {
		t.Errorf("error should mention 'json' flag: %v", err)
	}
	if !strings.Contains(err.Error(), "quiet") {
		t.Errorf("error should mention 'quiet' flag: %v", err)
	}
}

// TestRunStatus_MalformedConfig asserts that runStatus returns a non-nil error
// when config.toml exists but contains a TOML syntax error (stray bracket).
// Mirrors the runIndex pattern: os.IsNotExist → fine; any other error → fatal.
func TestRunStatus_MalformedConfig(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("CCPULSE_CACHE_DIR", t.TempDir())

	// channel is "dev" by default in tests; DefaultPath() → $XDG_CONFIG_HOME/ccpulse-dev/config.toml
	ccpulseDir := filepath.Join(cfgDir, "ccpulse-dev")
	if err := os.MkdirAll(ccpulseDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ccpulseDir, "config.toml"), []byte("[[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := newStatusCmd()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("runStatus should return error for malformed config, got nil")
	}
	var perr toml.ParseError
	if !errors.As(err, &perr) {
		t.Errorf("error should unwrap to *toml.ParseError, got: %v", err)
	}
}

// TestRunStatus_AbsentConfigUsesDefaults asserts that runStatus proceeds
// normally (no error on the config step) when config.toml is simply absent.
// The os.IsNotExist guard means defaults kick in — same silent behavior as before.
func TestRunStatus_AbsentConfig_ProducesJSON(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-config-here"))
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	writeTempCache(t, cacheDir)
	writeTempCredential(t, credDir)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("runStatus should succeed with absent config (defaults), got: %v", err)
	}
	if !strings.Contains(buf.String(), `"ceiling_label"`) {
		t.Errorf("expected JSON output on success, got: %s", buf.String())
	}
}

func TestStatusPrunesWhenRetentionConfigured(t *testing.T) {
	cacheDir := t.TempDir()
	credDir := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	// This test writes to the release-channel config path. Pin the channel
	// to release for the duration so DefaultPath resolves there regardless
	// of the test process's default channel state.
	channel.Set("release")
	t.Cleanup(func() { channel.Set("dev") })
	writeTempCredential(t, credDir)

	// Write a config that prunes anything older than 1 day.
	if err := os.MkdirAll(filepath.Join(cfgDir, "ccpulse"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cfgDir, "ccpulse", "config.toml"),
		[]byte("[history]\nretention_days = 1\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	// Pre-seed an old row so the prune step has something to delete.
	// cache.Open applies schema.sql (which includes usage_samples) — no
	// need to CREATE TABLE manually.
	seed, err := cache.Open(t.Context(), filepath.Join(cacheDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	// 2× the configured 1-day retention, so this row is unambiguously inside the cutoff.
	oldTs := time.Now().Add(-48 * time.Hour).Unix()
	_, execErr := seed.DB().Exec(
		`INSERT INTO usage_samples(ts, source) VALUES (?, 'api')`, oldTs,
	)
	seed.Close()
	if execErr != nil {
		t.Fatal(execErr)
	}

	var hits int
	srv := stubUsageServer(t, &hits)
	restore := anthro.SetAPIURLForTest(srv.URL)
	t.Cleanup(restore)

	cmd := newStatusCmd()
	cmd.SetArgs([]string{"--json"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("status: %v", err)
	}
	if hits != 1 {
		t.Fatalf("API hit count = %d, want 1 (prune test must trigger an API fetch)", hits)
	}

	// After the call: 1 fresh row inserted, 1 old row pruned → exactly 1 row total.
	if got := countSamples(t, filepath.Join(cacheDir, "state.db")); got != 1 {
		t.Fatalf("usage_samples rows = %d, want 1 (old row pruned, fresh row inserted)", got)
	}
}
