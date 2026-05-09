package status

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(`CREATE TABLE messages (
		ts TEXT, input_tokens INTEGER, output_tokens INTEGER,
		cache_read_tokens INTEGER, cache_write_5m_tokens INTEGER,
		cache_write_1h_tokens INTEGER, cost_usd_estimate REAL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestComputeWithoutQuota(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	_, _ = db.Exec(`INSERT INTO messages VALUES (?, 100, 50, 0, 0, 0, 0.01)`,
		now.Add(-1*time.Hour).Format("2006-01-02T15:04:05.000Z07:00"))
	w, err := Compute(db, now, QuotaInput{TierSlug: "unknown", TierPretty: "Unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Percent != 0 {
		t.Errorf("Percent = %d, want 0 without quota", w.Percent)
	}
	if w.MinutesToReset < 230 || w.MinutesToReset > 250 {
		t.Errorf("MinutesToReset = %d, want ~240", w.MinutesToReset)
	}
	if w.CeilingLabel != "unknown" {
		t.Errorf("CeilingLabel = %q", w.CeilingLabel)
	}
}

func TestComputeWithQuota(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	resetsAt := now.Add(70 * time.Minute)
	usage := &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.7, ResetsAt: resetsAt}}
	w, err := Compute(db, now, QuotaInput{
		Usage: usage, Source: "api", UpdatedAt: now,
		TierSlug: "max_20x", TierPretty: "Max 20x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Percent != 13 {
		t.Errorf("Percent = %d, want 13 (rounded)", w.Percent)
	}
	if w.MinutesToReset != 70 {
		t.Errorf("MinutesToReset = %d, want 70", w.MinutesToReset)
	}
	if w.CeilingLabel != "max_20x" || w.CeilingPretty != "Max 20x" {
		t.Errorf("Ceiling labels: %q / %q", w.CeilingLabel, w.CeilingPretty)
	}
	if w.Quota == nil {
		t.Errorf("Quota should be set")
	}
}

func TestJSONOutputIncludesQuota(t *testing.T) {
	w := Window{
		Percent: 13, MinutesToReset: 70,
		CeilingLabel: "max_20x", CeilingPretty: "Max 20x",
		Quota:          &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.7}},
		QuotaSource:    "api",
		QuotaUpdatedAt: time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC),
	}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	for _, want := range []string{`"quota":`, `"quota_source":"api"`, `"five_hour":`, `"ceiling_pretty":"Max 20x"`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s in %s", want, out)
		}
	}
}

func TestJSONOutputOmitsQuotaWhenAbsent(t *testing.T) {
	w := Window{Percent: 0, CeilingLabel: "unknown"}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "quota") {
		t.Errorf("JSON should omit quota fields when nil: %s", out)
	}
}

func TestTmuxLinePercent(t *testing.T) {
	w := Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := TmuxLine(w, DisplayPercent, DisplayBudget{})
	if !strings.Contains(got, "61%") {
		t.Errorf("missing percent in %q", got)
	}
	if !strings.Contains(got, "1h47m") {
		t.Errorf("missing duration in %q", got)
	}
}

func TestTmuxLineCost(t *testing.T) {
	w := Window{Cost5hUSD: 4.21, MinutesToReset: 107, CeilingLabel: "api"}
	got := TmuxLine(w, DisplayCost, DisplayBudget{WarnUSD: 5, HotUSD: 10})
	if !strings.Contains(got, "$4.21") {
		t.Errorf("missing dollars in %q", got)
	}
}

func TestTmuxLineHot(t *testing.T) {
	w := Window{Percent: 95, MinutesToReset: 10, CeilingLabel: "max_20x"}
	got := TmuxLine(w, DisplayPercent, DisplayBudget{})
	if !strings.Contains(got, "#dc322f") {
		t.Errorf("expected red fg in %q", got)
	}
}

func TestCompute_PopulatesSevenDay(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	resets5h := now.Add(2*time.Hour + 3*time.Minute)
	resets7d := now.Add(17*time.Hour + 33*time.Minute)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 14.0, ResetsAt: resets5h},
		SevenDay: &anthro.Bucket{Utilization: 89.0, ResetsAt: resets7d},
	}
	w, err := Compute(db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if !w.Has7d {
		t.Errorf("Has7d = false, want true")
	}
	if w.Percent7d != 89 {
		t.Errorf("Percent7d = %d, want 89", w.Percent7d)
	}
	if w.MinutesToReset7d != 17*60+33 {
		t.Errorf("MinutesToReset7d = %d, want %d", w.MinutesToReset7d, 17*60+33)
	}
}

func TestCompute_OmitsSevenDayWhenSevenDayNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 14.0, ResetsAt: now.Add(2 * time.Hour)},
	}
	w, err := Compute(db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Has7d {
		t.Errorf("Has7d = true, want false")
	}
	if w.Percent7d != 0 || w.MinutesToReset7d != 0 {
		t.Errorf("7d fields nonzero: percent=%d minutes=%d", w.Percent7d, w.MinutesToReset7d)
	}
}

func TestCompute_OmitsSevenDayWhenUsageNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	w, err := Compute(db, now, QuotaInput{Usage: nil, Source: "cache_stale", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Has7d {
		t.Errorf("Has7d = true, want false")
	}
}
