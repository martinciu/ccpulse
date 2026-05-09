package cache

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func TestOpenAppliesSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	row := c.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('messages','files','slug_canonical','meta','usage_samples')`)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("expected 5 tables, got %d", n)
	}
}

func TestInsertMessages(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	msgs := []parse.Message{
		{
			SessionID:   "s1",
			ProjectSlug: "slug-a",
			Model:       "claude-opus-4-7",
			Timestamp:   time.Now(),
			InputTokens: 10,
		},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count = %d, want 1", n)
	}

	var cost float64
	var unknown int
	if err := c.DB().QueryRow(`SELECT cost_usd_estimate, pricing_unknown FROM messages`).Scan(&cost, &unknown); err != nil {
		t.Fatal(err)
	}
	if unknown != 0 {
		t.Errorf("pricing_unknown = %d, want 0", unknown)
	}
	if cost <= 0 {
		t.Errorf("cost = %v, want > 0", cost)
	}
}

func TestFileTracking(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.RecordFile("/tmp/x.jsonl", 1234, 5678, 42); err != nil {
		t.Fatal(err)
	}
	mtime, off, line, found, err := c.GetFile("/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mtime != 1234 || off != 5678 || line != 42 {
		t.Errorf("got mtime=%d off=%d line=%d found=%v", mtime, off, line, found)
	}

	// Update existing record
	if err := c.RecordFile("/tmp/x.jsonl", 9999, 8888, 99); err != nil {
		t.Fatal(err)
	}
	mtime, _, _, _, _ = c.GetFile("/tmp/x.jsonl")
	if mtime != 9999 {
		t.Errorf("after update mtime = %d", mtime)
	}
}

func TestInsertMessagesIdempotent(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Now()
	msgs := []parse.Message{
		{
			SessionID:   "s1",
			ProjectSlug: "slug-a",
			Model:       "claude-opus-4-7",
			Timestamp:   ts,
			InputTokens: 10,
		},
	}

	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count after duplicate insert = %d, want 1", n)
	}
}

func TestRecordUsageSample_RoundTrip(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	fiveResets := when.Add(2 * time.Hour)
	sevenResets := when.Add(48 * time.Hour)
	util := 42.5
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: fiveResets},
		SevenDay: &anthro.Bucket{Utilization: 67.0, ResetsAt: sevenResets},
		ExtraUsage: &anthro.ExtraUsage{
			IsEnabled: true, MonthlyLimit: 100, UsedCredits: 42, Utilization: &util, Currency: "USD",
		},
	}

	if err := c.RecordUsageSample(u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var ts int64
	var src string
	var fivePct, sevenPct, extraPct sql.NullFloat64
	var fiveResetsGot, sevenResetsGot, extraCurrency sql.NullString
	var extraEnabled sql.NullInt64
	var extraLimit, extraUsed sql.NullFloat64
	err = c.DB().QueryRow(`SELECT
		ts, source,
		five_hour_pct, five_hour_resets_at,
		seven_day_pct, seven_day_resets_at,
		extra_usage_enabled, extra_usage_limit, extra_usage_used, extra_usage_pct, extra_usage_currency
		FROM usage_samples`).Scan(
		&ts, &src,
		&fivePct, &fiveResetsGot,
		&sevenPct, &sevenResetsGot,
		&extraEnabled, &extraLimit, &extraUsed, &extraPct, &extraCurrency,
	)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if ts != when.Unix() {
		t.Errorf("ts = %d, want %d", ts, when.Unix())
	}
	if src != "api" {
		t.Errorf("source = %q, want api", src)
	}
	if !fivePct.Valid || fivePct.Float64 != 12.0 {
		t.Errorf("five_hour_pct = %+v, want 12.0", fivePct)
	}
	if !fiveResetsGot.Valid || fiveResetsGot.String != fiveResets.Format(time.RFC3339Nano) {
		t.Errorf("five_hour_resets_at = %+v, want %s", fiveResetsGot, fiveResets.Format(time.RFC3339Nano))
	}
	if !sevenPct.Valid || sevenPct.Float64 != 67.0 {
		t.Errorf("seven_day_pct = %+v, want 67.0", sevenPct)
	}
	if !sevenResetsGot.Valid || sevenResetsGot.String != sevenResets.Format(time.RFC3339Nano) {
		t.Errorf("seven_day_resets_at = %+v, want %s", sevenResetsGot, sevenResets.Format(time.RFC3339Nano))
	}
	if !extraEnabled.Valid || extraEnabled.Int64 != 1 {
		t.Errorf("extra_usage_enabled = %+v, want 1", extraEnabled)
	}
	if !extraLimit.Valid || extraLimit.Float64 != 100 {
		t.Errorf("extra_usage_limit = %+v, want 100", extraLimit)
	}
	if !extraUsed.Valid || extraUsed.Float64 != 42 {
		t.Errorf("extra_usage_used = %+v, want 42", extraUsed)
	}
	if !extraPct.Valid || extraPct.Float64 != 42.5 {
		t.Errorf("extra_usage_pct = %+v, want 42.5", extraPct)
	}
	if !extraCurrency.Valid || extraCurrency.String != "USD" {
		t.Errorf("extra_usage_currency = %+v, want USD", extraCurrency)
	}
}

func TestRecordUsageSample_DuplicateTs(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	first := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 10.0, ResetsAt: when.Add(time.Hour)}}
	second := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 99.0, ResetsAt: when.Add(time.Hour)}}

	if err := c.RecordUsageSample(first, when); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsageSample(second, when); err != nil {
		t.Fatalf("second insert should be a silent no-op, got: %v", err)
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM usage_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1 (INSERT OR IGNORE should drop the duplicate)", n)
	}

	var fivePct sql.NullFloat64
	if err := c.DB().QueryRow(`SELECT five_hour_pct FROM usage_samples`).Scan(&fivePct); err != nil {
		t.Fatal(err)
	}
	if !fivePct.Valid || fivePct.Float64 != 10.0 {
		t.Errorf("expected first row to win (five_hour_pct 10.0), got %+v", fivePct)
	}
}

func TestPruneUsageSamples(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	base := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	samples := []time.Time{
		base.Add(-100 * time.Second),
		base.Add(-50 * time.Second),
		base,
	}
	for i, when := range samples {
		u := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: float64(i), ResetsAt: when.Add(time.Hour)}}
		if err := c.RecordUsageSample(u, when); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := base.Add(-60 * time.Second) // boundary: drop the -100s row, keep -50s and base
	n, err := c.PruneUsageSamples(cutoff)
	if err != nil {
		t.Fatalf("PruneUsageSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("rows deleted = %d, want 1", n)
	}

	var remaining int
	if err := c.DB().QueryRow(`SELECT count(*) FROM usage_samples`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Errorf("remaining rows = %d, want 2", remaining)
	}

	// Cutoff is strictly less-than: a row exactly at cutoff is kept.
	var earliest int64
	if err := c.DB().QueryRow(`SELECT MIN(ts) FROM usage_samples`).Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if earliest != base.Add(-50*time.Second).Unix() {
		t.Errorf("earliest remaining ts = %d, want %d", earliest, base.Add(-50*time.Second).Unix())
	}
}

func TestRecordUsageSample_NilBucket(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.5, ResetsAt: when.Add(2 * time.Hour)},
		// SevenDay deliberately nil
	}

	if err := c.RecordUsageSample(u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var fivePct sql.NullFloat64
	var fiveResets sql.NullString
	var sevenPct sql.NullFloat64
	var sevenResets sql.NullString
	err = c.DB().QueryRow(`SELECT five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at FROM usage_samples`).Scan(&fivePct, &fiveResets, &sevenPct, &sevenResets)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if !fivePct.Valid || fivePct.Float64 != 12.5 {
		t.Errorf("five_hour_pct = %+v, want 12.5", fivePct)
	}
	if !fiveResets.Valid || fiveResets.String != when.Add(2*time.Hour).Format(time.RFC3339Nano) {
		t.Errorf("five_hour_resets_at = %+v, want %s", fiveResets, when.Add(2*time.Hour).Format(time.RFC3339Nano))
	}
	if sevenPct.Valid {
		t.Errorf("seven_day_pct = %v, want NULL", sevenPct.Float64)
	}
	if sevenResets.Valid {
		t.Errorf("seven_day_resets_at = %q, want NULL", sevenResets.String)
	}
}

func TestOpenWipesOnSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tab, _ := pricing.Load()
	if err := c.InsertMessages([]parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}

	if _, err := c.DB().Exec(`UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	var n int
	if err := c2.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("messages count after wipe = %d, want 0", n)
	}

	var version string
	if err := c2.DB().QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema_version after wipe = %q, want %q", version, SchemaVersion)
	}
}
