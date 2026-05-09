package anthro

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleAPIBody = `{
  "five_hour":            {"utilization": 5.0,  "resets_at": "2026-05-09T16:10:00.151311+00:00"},
  "seven_day":            {"utilization": 89.0, "resets_at": "2026-05-10T09:00:00.151331+00:00"},
  "seven_day_oauth_apps": null,
  "seven_day_opus":       null,
  "seven_day_sonnet":     {"utilization": 5.0,  "resets_at": "2026-05-10T09:00:00.151340+00:00"},
  "seven_day_cowork":     null,
  "seven_day_omelette":   {"utilization": 21.0, "resets_at": "2026-05-10T09:00:01.151348+00:00"},
  "tangelo":              null,
  "iguana_necktie":       null,
  "omelette_promotional": null,
  "extra_usage":          {"is_enabled": true, "monthly_limit": 2000, "used_credits": 0.0, "utilization": null, "currency": "EUR"}
}`

func TestUsageUnmarshalFull(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.FiveHour == nil || u.FiveHour.Utilization != 5.0 {
		t.Errorf("five_hour utilization: %+v", u.FiveHour)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-05-09T16:10:00.151311+00:00")
	if !u.FiveHour.ResetsAt.Equal(want) {
		t.Errorf("five_hour.ResetsAt = %v, want %v", u.FiveHour.ResetsAt, want)
	}
	if u.SevenDay == nil || u.SevenDay.Utilization != 89.0 {
		t.Errorf("seven_day: %+v", u.SevenDay)
	}
	if u.SevenDayOpus != nil {
		t.Errorf("seven_day_opus should be nil, got %+v", u.SevenDayOpus)
	}
	if u.Tangelo != nil {
		t.Errorf("tangelo should be nil, got %+v", u.Tangelo)
	}
	if u.ExtraUsage == nil || !u.ExtraUsage.IsEnabled {
		t.Errorf("extra_usage: %+v", u.ExtraUsage)
	}
	if u.ExtraUsage.MonthlyLimit != 2000 || u.ExtraUsage.Currency != "EUR" {
		t.Errorf("extra_usage fields: %+v", u.ExtraUsage)
	}
	if u.ExtraUsage.Utilization != nil {
		t.Errorf("extra_usage.utilization should be nil pointer, got %v", *u.ExtraUsage.Utilization)
	}
}

func TestUsageUnmarshalAllNull(t *testing.T) {
	body := `{
	  "five_hour": null, "seven_day": null,
	  "seven_day_oauth_apps": null, "seven_day_opus": null,
	  "seven_day_sonnet": null, "seven_day_cowork": null,
	  "seven_day_omelette": null, "tangelo": null,
	  "iguana_necktie": null, "omelette_promotional": null,
	  "extra_usage": null
	}`
	var u Usage
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.FiveHour != nil || u.ExtraUsage != nil {
		t.Errorf("expected all nil, got %+v", u)
	}
}

func TestUsageRoundTrip(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"five_hour"`, `"seven_day_sonnet"`, `"extra_usage"`, `"tangelo":null`, `"is_enabled":true`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("round-trip missing %s in %s", want, out)
		}
	}
}

func TestReadCacheFresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	wrote := writeFixtureCache(t, dir, now.Add(-1*time.Minute))
	got, err := readCache(filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatalf("readCache: %v", err)
	}
	if got.UpdatedAt.Sub(wrote).Abs() > time.Second {
		t.Errorf("UpdatedAt drift: got %v, want %v", got.UpdatedAt, wrote)
	}
	if got.Usage.FiveHour == nil {
		t.Errorf("usage.FiveHour nil")
	}
}

func TestReadCacheMissing(t *testing.T) {
	_, err := readCache(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Errorf("expected error on missing cache")
	}
}

func TestReadCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(p, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readCache(p)
	if err == nil {
		t.Errorf("expected error on corrupt cache")
	}
}

func TestReadCacheWrongVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	body := `{"v":99,"updated_at":"2026-05-09T15:00:00Z","data":{}}`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readCache(p)
	if err == nil {
		t.Errorf("expected error on wrong cache version")
	}
}

func TestWriteCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	if err := writeCache(p, u, when); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, err := readCache(p)
	if err != nil {
		t.Fatalf("readCache: %v", err)
	}
	if !got.UpdatedAt.Equal(when) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, when)
	}
	if got.Usage.SevenDay == nil || got.Usage.SevenDay.Utilization != 89.0 {
		t.Errorf("seven_day round-trip lost: %+v", got.Usage.SevenDay)
	}
}

// writeFixtureCache writes sampleAPIBody as a v:1 cache file with the given
// updated_at and returns the timestamp actually written.
func writeFixtureCache(t *testing.T, dir string, when time.Time) time.Time {
	t.Helper()
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	if err := writeCache(filepath.Join(dir, "usage.json"), u, when); err != nil {
		t.Fatal(err)
	}
	return when
}
