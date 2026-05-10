package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

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
	if err := os.WriteFile(filepath.Join(dir, "usage.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

func writeTempCredential(t *testing.T, dir string) {
	t.Helper()
	body := `{"claudeAiOauth":{"accessToken":"tok","subscriptionType":"max","rateLimitTier":"default_claude_max_20x","expiresAt":4070908800000}}`
	claudeDir := filepath.Join(dir, ".claude")
	if err := os.MkdirAll(claudeDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, ".credentials.json"), []byte(body), 0600); err != nil {
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
}

func TestStatusJSONWithoutCredential(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Keychain may have a real credential on dev machines; skipping no-cred test on darwin")
	}
	cacheDir := t.TempDir()
	credDir := t.TempDir() // no .credentials.json inside
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("HOME", credDir)

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
	if !strings.Contains(out, `"ceiling_label":"unknown"`) {
		t.Errorf("expected ceiling_label=unknown without cred: %s", out)
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
	c, err := cache.Open(dbPath)
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
	if err := os.MkdirAll(filepath.Join(cfgDir, "ccpulse"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(cfgDir, "ccpulse", "config.toml"),
		[]byte("[history]\nretention_days = 1\n"),
		0600,
	); err != nil {
		t.Fatal(err)
	}

	// Pre-seed an old row so the prune step has something to delete.
	// cache.Open applies schema.sql (which includes usage_samples) — no
	// need to CREATE TABLE manually.
	seed, err := cache.Open(filepath.Join(cacheDir, "state.db"))
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
