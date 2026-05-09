package cache

import (
	"encoding/json"
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

func TestRecordUsageSample_RoundTrip(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	util := 42.5
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: when.Add(2 * time.Hour)},
		SevenDay: &anthro.Bucket{Utilization: 67.0, ResetsAt: when.Add(48 * time.Hour)},
		ExtraUsage: &anthro.ExtraUsage{
			IsEnabled: true, MonthlyLimit: 100, UsedCredits: 42, Utilization: &util, Currency: "USD",
		},
	}

	if err := c.RecordUsageSample(u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var ts int64
	var payload string
	var src string
	err = c.DB().QueryRow(`SELECT ts, payload, source FROM usage_samples`).Scan(&ts, &payload, &src)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if ts != when.Unix() {
		t.Errorf("ts = %d, want %d", ts, when.Unix())
	}
	if src != "api" {
		t.Errorf("source = %q, want api", src)
	}
	var got anthro.Usage
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got.FiveHour == nil || got.FiveHour.Utilization != 12.0 {
		t.Errorf("FiveHour round-trip failed: %+v", got.FiveHour)
	}
	if got.SevenDay == nil || got.SevenDay.Utilization != 67.0 {
		t.Errorf("SevenDay round-trip failed: %+v", got.SevenDay)
	}
	if got.ExtraUsage == nil || got.ExtraUsage.Utilization == nil || *got.ExtraUsage.Utilization != 42.5 {
		t.Errorf("ExtraUsage round-trip failed: %+v", got.ExtraUsage)
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

	var payload string
	if err := c.DB().QueryRow(`SELECT payload FROM usage_samples`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var got anthro.Usage
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatal(err)
	}
	if got.FiveHour == nil || got.FiveHour.Utilization != 10.0 {
		t.Errorf("expected first row to win (utilization 10.0), got %+v", got.FiveHour)
	}
}
