package cache

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
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

func TestTokenBuckets_ContiguousRange(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// Two messages: one at 11:50, two more at 11:55 + 11:57.
	// Empty buckets at 11:00, 11:05, ..., 11:45 separate the active stretch
	// from the requested window start.
	ts1 := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	ts2 := time.Date(2026, 5, 9, 11, 55, 0, 0, time.UTC)
	ts3 := time.Date(2026, 5, 9, 11, 57, 0, 0, time.UTC)

	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts1, InputTokens: 1000, OutputTokens: 500},
		{SessionID: "s2", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts2, InputTokens: 2000, OutputTokens: 800},
		{SessionID: "s3", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts3, InputTokens: 500, OutputTokens: 200},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	buckets, err := c.TokenBuckets(5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}

	// 1 hour / 5 min = 12 buckets, contiguous, oldest first.
	if len(buckets) != 12 {
		t.Fatalf("want 12 buckets, got %d: %+v", len(buckets), buckets)
	}
	for i, b := range buckets {
		want := from.Add(time.Duration(i) * 5 * time.Minute)
		if !b.BucketStart.Equal(want) {
			t.Errorf("bucket[%d].BucketStart = %v, want %v", i, b.BucketStart, want)
		}
	}
	// Indices 10 (11:50), 11 (11:55) carry data; everything else is zero.
	for i, b := range buckets {
		switch i {
		case 10:
			if b.Tokens != 1500 {
				t.Errorf("bucket[10].Tokens = %d, want 1500", b.Tokens)
			}
		case 11:
			if b.Tokens != 3500 {
				t.Errorf("bucket[11].Tokens = %d, want 3500", b.Tokens)
			}
		default:
			if b.Tokens != 0 {
				t.Errorf("bucket[%d].Tokens = %d, want 0 (gap)", i, b.Tokens)
			}
		}
	}
}

func TestTokenBuckets_AllEmpty(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC)
	buckets, err := c.TokenBuckets(15*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}
	// 6h / 15m = 24 buckets, all zero, monotonic.
	if len(buckets) != 24 {
		t.Fatalf("want 24 buckets, got %d", len(buckets))
	}
	for i, b := range buckets {
		if b.Tokens != 0 {
			t.Errorf("bucket[%d].Tokens = %d, want 0", i, b.Tokens)
		}
		if i > 0 && !b.BucketStart.After(buckets[i-1].BucketStart) {
			t.Errorf("bucket starts not monotonic at index %d", i)
		}
	}
}

func TestTokenBuckets_BoundsSnap(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Mid-bucket bounds: 11:03:30 → 12:07:45 with 5m buckets snaps to
	// [11:00, 12:05) → 13 buckets.
	from := time.Date(2026, 5, 9, 11, 3, 30, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 7, 45, 0, time.UTC)
	buckets, err := c.TokenBuckets(5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 13 {
		t.Fatalf("want 13 buckets after snap, got %d", len(buckets))
	}
	if !buckets[0].BucketStart.Equal(time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("first bucket = %v, want 11:00", buckets[0].BucketStart)
	}
	last := buckets[len(buckets)-1].BucketStart
	if !last.Equal(time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("last bucket = %v, want 12:00", last)
	}
	for _, b := range buckets {
		if b.BucketStart.Unix()%int64((5*time.Minute).Seconds()) != 0 {
			t.Errorf("bucket start not on 5m boundary: %v", b.BucketStart)
		}
	}
}

func TestTokenBuckets_IncludesInFlightBucket(t *testing.T) {
	// Regression: when callers anchor at to = BucketAlign(now) + dur, the
	// in-flight bucket containing now must be included as the rightmost
	// bucket in the [from, to) range — otherwise a freshly-recorded
	// message stays invisible until the bucket boundary ticks over.
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// Production messages come from JSONL parsing — always UTC. The
	// cache layer normalizes both insert and query bounds to UTC, so
	// non-UTC inputs would also work; UTC here just matches reality.
	now := time.Now().UTC()
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: now, InputTokens: 1000, OutputTokens: 500},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	dur := 5 * time.Minute
	to := BucketAlign(now, dur).Add(dur)
	from := to.Add(-time.Hour)
	buckets, err := c.TokenBuckets(dur, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) == 0 {
		t.Fatalf("got 0 buckets")
	}
	last := buckets[len(buckets)-1]
	if !last.BucketStart.Equal(BucketAlign(now, dur)) {
		t.Errorf("rightmost BucketStart = %v, want %v (bucket containing now)",
			last.BucketStart, BucketAlign(now, dur))
	}
	if last.Tokens != 1500 {
		t.Errorf("rightmost Tokens = %d, want 1500 (in-flight message)", last.Tokens)
	}
}

func TestEarliestMessageTime_Empty(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ts, ok, err := c.EarliestMessageTime()
	if err != nil {
		t.Fatalf("EarliestMessageTime: %v", err)
	}
	if ok {
		t.Errorf("ok = true on empty cache, want false")
	}
	if !ts.IsZero() {
		t.Errorf("ts = %v, want zero time", ts)
	}
}

func TestEarliestMessageTime_SingleRow(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	want := time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)
	if err := c.InsertMessages([]parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   want,
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}

	ts, ok, err := c.EarliestMessageTime()
	if err != nil {
		t.Fatalf("EarliestMessageTime: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if !ts.Equal(want) {
		t.Errorf("ts = %v, want %v", ts, want)
	}
	if ts.Location() != time.UTC {
		t.Errorf("ts location = %v, want UTC", ts.Location())
	}
}

func TestEarliestMessageTime_MultipleRows(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	earliest := time.Date(2026, 1, 5, 8, 0, 0, 0, time.UTC)
	mid := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)
	latest := time.Date(2026, 5, 1, 22, 0, 0, 0, time.UTC)

	mk := func(id string, when time.Time) parse.Message {
		return parse.Message{
			SessionID:   id,
			ProjectSlug: "slug-a",
			Model:       "claude-opus-4-7",
			Timestamp:   when,
			InputTokens: 10,
		}
	}
	// Insert in non-sorted order to make sure we return MIN, not the
	// first-inserted row.
	if err := c.InsertMessages([]parse.Message{
		mk("s2", mid),
		mk("s3", latest),
		mk("s1", earliest),
	}, tab); err != nil {
		t.Fatal(err)
	}

	ts, ok, err := c.EarliestMessageTime()
	if err != nil {
		t.Fatalf("EarliestMessageTime: %v", err)
	}
	if !ok {
		t.Fatalf("ok = false, want true")
	}
	if !ts.Equal(earliest) {
		t.Errorf("ts = %v, want %v (earliest)", ts, earliest)
	}
}

func TestOpenSetsWALAndBusyTimeout(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// busy_timeout, synchronous, and temp_store are per-connection. Holding
	// one Conn open forces sql.DB to hand us a second, distinct connection —
	// proving the DSN pragmas hit every conn, not just the one db.Exec
	// happened to grab. Without the DSN, conn2 would report busy_timeout=0.
	ctx := context.Background()
	conn1, err := c.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	conn2, err := c.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()

	for i, conn := range []*sql.Conn{conn1, conn2} {
		var mode string
		if err := conn.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode); err != nil {
			t.Fatalf("conn[%d] journal_mode: %v", i, err)
		}
		if mode != "wal" {
			t.Errorf("conn[%d] journal_mode = %q, want wal", i, mode)
		}

		var timeout int
		if err := conn.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&timeout); err != nil {
			t.Fatalf("conn[%d] busy_timeout: %v", i, err)
		}
		if timeout != 5000 {
			t.Errorf("conn[%d] busy_timeout = %d, want 5000", i, timeout)
		}
	}
}

func TestConcurrentReadWriteNoBusy(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	base := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	stop := make(chan struct{})
	errs := make(chan error, 2)

	go func() {
		i := 0
		for {
			select {
			case <-stop:
				errs <- nil
				return
			default:
			}
			msg := parse.Message{
				SessionID:   fmt.Sprintf("s%d", i),
				ProjectSlug: "p",
				Model:       "claude-sonnet-4-6",
				Timestamp:   base.Add(time.Duration(i) * time.Millisecond),
				InputTokens: 100,
			}
			if err := c.InsertMessages([]parse.Message{msg}, tab); err != nil {
				errs <- fmt.Errorf("InsertMessages: %w", err)
				return
			}
			i++
		}
	}()

	go func() {
		from := base
		to := base.Add(time.Hour)
		for {
			select {
			case <-stop:
				errs <- nil
				return
			default:
			}
			if _, err := c.TokenBuckets(5*time.Minute, from, to); err != nil {
				errs <- fmt.Errorf("TokenBuckets: %w", err)
				return
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)
	close(stop)

	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent op: %v", err)
		}
	}
}

func TestRemoveWithSiblings(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "state.db")

	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.WriteFile(base+suffix, []byte("planted"), 0644); err != nil {
			t.Fatalf("plant %s: %v", base+suffix, err)
		}
	}

	if err := RemoveWithSiblings(base); err != nil {
		t.Fatalf("RemoveWithSiblings: %v", err)
	}

	for _, suffix := range []string{"", "-wal", "-shm", "-journal"} {
		if _, err := os.Stat(base + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("expected %s%s to be absent, stat err = %v", base, suffix, err)
		}
	}
}

func TestRemoveWithSiblings_AllMissing(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, "absent.db")

	if err := RemoveWithSiblings(base); err != nil {
		t.Fatalf("RemoveWithSiblings on missing tree: %v", err)
	}
}

func TestBucketAlign(t *testing.T) {
	// 14:23:45 UTC, snapped down at 5m → 14:20:00 UTC
	in := time.Date(2026, 5, 10, 14, 23, 45, 0, time.UTC)
	got := BucketAlign(in, 5*time.Minute)
	want := time.Date(2026, 5, 10, 14, 20, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("BucketAlign(%v, 5m) = %v, want %v", in, got, want)
	}

	// Already-aligned input is idempotent
	if BucketAlign(want, 5*time.Minute) != want {
		t.Errorf("BucketAlign not idempotent on aligned input")
	}

	// 1h zoom snaps down to the hour
	got2 := BucketAlign(in, time.Hour)
	want2 := time.Date(2026, 5, 10, 14, 0, 0, 0, time.UTC)
	if !got2.Equal(want2) {
		t.Errorf("BucketAlign(%v, 1h) = %v, want %v", in, got2, want2)
	}

	// Output is in UTC even when input is non-UTC
	loc, _ := time.LoadLocation("America/New_York")
	in3 := time.Date(2026, 5, 10, 14, 23, 45, 0, loc)
	got3 := BucketAlign(in3, 15*time.Minute)
	if got3.Location() != time.UTC {
		t.Errorf("BucketAlign output location = %v, want UTC", got3.Location())
	}
}

// TestInsertMessages_NormalizesNonUTCTimestamp locks in the invariant
// that messages.ts is always stored as a Z-suffixed UTC string and that
// TokenBuckets compares its query bounds in UTC, regardless of the
// time.Time zone the caller hands in. Without normalization at both
// boundaries the WHERE ts >= ? AND ts < ? lex comparison silently
// misbehaves when a caller passes non-UTC values.
func TestInsertMessages_NormalizesNonUTCTimestamp(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	loc := time.FixedZone("test+02", 2*60*60)
	// 11:50 in +02:00 == 09:50 UTC. Both insert and query use the
	// non-UTC zone; the cache layer must normalize on both sides.
	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, loc)

	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts, InputTokens: 1000, OutputTokens: 500},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, loc)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, loc)
	buckets, err := c.TokenBuckets(5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}

	// 1 hour / 5 min = 12 buckets. The message at 11:50 (loc) lands in
	// the bucket starting 09:50 UTC == 11:50 in loc, i.e. index 10.
	if len(buckets) != 12 {
		t.Fatalf("want 12 buckets, got %d: %+v", len(buckets), buckets)
	}
	if buckets[10].Tokens != 1500 {
		t.Errorf("bucket[10].Tokens = %d, want 1500 (input+output of the non-UTC insert)",
			buckets[10].Tokens)
	}
}

func TestIntegrityOK_Healthy(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if !c.IntegrityOK() {
		t.Fatal("fresh cache should report healthy")
	}
}

func TestIntegrityOK_Corrupt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "s.db")

	// Phase 1: open, seed, close. Close checkpoints the WAL into the
	// main file so subsequent byte edits actually land in the database.
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	tab, _ := pricing.Load()
	if err := c.InsertMessages([]parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "p",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: corrupt page 2. Default SQLite page_size = 4096; page 1
	// holds the schema, page 2 is the first B-tree data page. Overwriting
	// 64 bytes at offset 4096 (the page-2 header) reliably trips
	// PRAGMA integrity_check.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	junk := bytes.Repeat([]byte{0xFF}, 64)
	if _, err := f.WriteAt(junk, 4096); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 3: reopen + verify detection. Re-Open succeeds because the
	// schema on page 1 is untouched; IntegrityOK returns false via the
	// integrity_check != "ok" branch.
	c2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	if c2.IntegrityOK() {
		t.Fatal("page-2-corrupted cache should report unhealthy")
	}
}

func TestSlugCanonical_RoundTrip(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	want := SlugCanonical{
		Slug:          "-Users-x-foo-bar",
		CanonicalPath: "/Users/x/foo/bar",
		Branch:        "feature/x",
		Resolved:      true,
	}
	if err := c.PutSlugCanonical(want); err != nil {
		t.Fatal(err)
	}

	got, ok, err := c.GetSlugCanonical(want.Slug)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("want ok=true, got ok=false")
	}
	if got.Slug != want.Slug || got.CanonicalPath != want.CanonicalPath ||
		got.Branch != want.Branch || got.Resolved != want.Resolved {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestSlugCanonical_Miss(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	_, ok, err := c.GetSlugCanonical("never-stored")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("expected miss, got ok=true")
	}
}

func TestSlugCanonical_Upsert(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.PutSlugCanonical(SlugCanonical{
		Slug: "k", CanonicalPath: "/old", Resolved: false,
	}); err != nil {
		t.Fatal(err)
	}
	if err := c.PutSlugCanonical(SlugCanonical{
		Slug: "k", CanonicalPath: "/new", Branch: "wt", Resolved: true,
	}); err != nil {
		t.Fatal(err)
	}

	got, ok, err := c.GetSlugCanonical("k")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.CanonicalPath != "/new" || got.Branch != "wt" || !got.Resolved {
		t.Errorf("upsert did not overwrite: %+v", got)
	}
}

func TestZoomLabel_DefaultFallback(t *testing.T) {
	if got := zoomLabel(30 * time.Minute); got != "30m0s" {
		t.Errorf("zoomLabel(30m) = %q, want %q (default fallback)", got, "30m0s")
	}
}
