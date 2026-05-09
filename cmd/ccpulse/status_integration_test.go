package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
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
