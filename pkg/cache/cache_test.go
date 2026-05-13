package cache

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// withTimeLocal swaps time.Local for the duration of the test. Tests
// calling this MUST NOT use t.Parallel() — mutating time.Local races
// with any other tz-aware test in the same package.
//
// Used by 24h-zoom tests to drive SQLite's 'localtime' modifier
// (modernc.org/sqlite reads Go's time.Local). t.Setenv("TZ", ...) is
// insufficient: time.Local is resolved once at program init and does
// not refresh from TZ mid-process.
func withTimeLocal(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load tz %q: %v", name, err)
	}
	prev := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = prev })
}

// TestNoParallelInCacheTests enforces the withTimeLocal contract: no
// test in this file may opt into parallel execution. The helper mutates
// the global time.Local; under -race, any concurrent tz-reading test
// would race. The contract is documented in withTimeLocal's comment but
// not statically checked by the language, so this self-greps the source
// file at test time.
//
// The guard regex matches a leading-whitespace + literal "t.Parallel()"
// pattern. Its source representation here uses backslash-escapes, so
// this test does not match its own body.
func TestNoParallelInCacheTests(t *testing.T) {
	body, err := os.ReadFile("cache_test.go")
	if err != nil {
		t.Fatalf("read cache_test.go: %v", err)
	}
	re := regexp.MustCompile(`(?m)^\s*t\.Parallel\(\)`)
	if locs := re.FindAllIndex(body, -1); len(locs) > 0 {
		t.Fatalf("pkg/cache/cache_test.go contains %d t.Parallel call(s); "+
			"withTimeLocal mutates the global time.Local and cannot race "+
			"with parallel tests in the same package. Remove the call or "+
			"move tz-mutating tests to a sub-package run with -p 1.",
			len(locs))
	}
}

func TestOpenAppliesSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	row := c.DB().QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('messages','files','meta','usage_samples')`)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 tables, got %d", n)
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

	// Phase 1: open, assert default page_size, seed, force a WAL
	// checkpoint, close. The page_size assertion guards against a
	// future driver default change that would otherwise silently
	// corrupt page 1 (schema) instead of the intended B-tree data
	// page. The explicit wal_checkpoint(TRUNCATE) makes the WAL → main
	// flush a hard contract instead of leaning on SQLite's default
	// last-connection-close PASSIVE checkpoint.
	c, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	var pageSize int
	if err := c.DB().QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if pageSize != 4096 {
		t.Fatalf("page_size = %d, want 4096 (test corruption offset assumes default)", pageSize)
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
	if _, err := c.DB().Exec(`PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	// Phase 2: corrupt page 2. Page 1 holds the schema; page 2 is the
	// first B-tree data page. Overwriting 64 bytes at offset 4096 (the
	// page-2 header) reliably trips PRAGMA integrity_check.
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

func TestInsertMessages_PersistsCwdAndGitBranch(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatal(err)
	}

	msgs := []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "-Users-x-proj",
		Timestamp:   time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC),
		Role:        "assistant",
		Model:       "claude-opus-4-7",
		Cwd:         "/Users/x/proj",
		GitBranch:   "main",
	}}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	var cwd, branch string
	if err := c.DB().QueryRow(
		`SELECT cwd, git_branch FROM messages WHERE session_id = 's1'`,
	).Scan(&cwd, &branch); err != nil {
		t.Fatal(err)
	}
	if cwd != "/Users/x/proj" {
		t.Errorf("cwd = %q, want /Users/x/proj", cwd)
	}
	if branch != "main" {
		t.Errorf("git_branch = %q, want main", branch)
	}
}

func TestDayStartLocal(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")

	in := time.Date(2026, 5, 13, 22, 0, 0, 0, time.UTC)
	got := DayStartLocal(in)

	wantInstant := time.Date(2026, 5, 14, 0, 0, 0, 0, time.Local)
	if !got.Equal(wantInstant) {
		t.Errorf("DayStartLocal(%v) = %v, want %v", in, got, wantInstant)
	}
	if got.Location() != time.Local {
		t.Errorf("dayStartLocal Location() = %v, want time.Local", got.Location())
	}
}

// insertMessage is a thin test helper that writes one row into messages
// with the given UTC timestamp and token count. Avoids exercising the
// full InsertMessages path (parse.Message + pricing) for tests that only
// care about token sums per ts.
func insertMessage(t *testing.T, c *Cache, ts time.Time, tokens int64) {
	t.Helper()
	_, err := c.DB().Exec(`
INSERT INTO messages
(session_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch)
VALUES('s','p',?,'assistant','m',?,0,0,0,0,0,'v1',0,0,'','','')`,
		ts.UTC().Format("2006-01-02T15:04:05.000Z07:00"), tokens)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestTokenBuckets_24h_LocalAlignment(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// In CEST (UTC+2), local midnight 2026-05-14 corresponds to
	// 2026-05-13T22:00:00Z. So 21:59Z belongs to 2026-05-13 local;
	// 22:00Z belongs to 2026-05-14 local.
	insertMessage(t, c, time.Date(2026, 5, 13, 21, 59, 0, 0, time.UTC), 100)
	insertMessage(t, c, time.Date(2026, 5, 13, 22, 0, 0, 0, time.UTC), 200)
	insertMessage(t, c, time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), 300)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2: %+v", len(buckets), buckets)
	}
	if buckets[0].Tokens != 100 {
		t.Errorf("2026-05-13 bucket tokens = %d, want 100", buckets[0].Tokens)
	}
	if buckets[1].Tokens != 500 {
		t.Errorf("2026-05-14 bucket tokens = %d, want 500", buckets[1].Tokens)
	}
	if buckets[0].BucketStart.Location() != time.Local {
		t.Errorf("BucketStart.Location() = %v, want time.Local", buckets[0].BucketStart.Location())
	}
}

func TestTokenBuckets_24h_EmptyDays(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessage(t, c, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC), 100)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 16, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 3 {
		t.Fatalf("got %d buckets, want 3", len(buckets))
	}
	if buckets[0].Tokens != 100 || buckets[1].Tokens != 0 || buckets[2].Tokens != 0 {
		t.Errorf("token totals = [%d %d %d], want [100 0 0]",
			buckets[0].Tokens, buckets[1].Tokens, buckets[2].Tokens)
	}
}

func TestTokenBuckets_24h_UTCFallback(t *testing.T) {
	withTimeLocal(t, "UTC")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessage(t, c, time.Date(2026, 5, 13, 23, 59, 0, 0, time.UTC), 100)
	insertMessage(t, c, time.Date(2026, 5, 14, 0, 1, 0, 0, time.UTC), 200)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[0].Tokens != 100 || buckets[1].Tokens != 200 {
		t.Errorf("UTC-local bucketing failed: %+v", buckets)
	}
}

func TestTokenBuckets_24h_DST_SpringForward(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 2026-03-29 Europe/Berlin: clocks jump 02:00 -> 03:00. Local day is
	// 23h long, from 2026-03-28T23:00:00Z to 2026-03-29T22:00:00Z.
	// All five timestamps below fall inside 2026-03-29 local.
	for _, ts := range []time.Time{
		time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC),  // 00:00 CET
		time.Date(2026, 3, 29, 0, 59, 0, 0, time.UTC),  // 01:59 CET (right before jump)
		time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC),   // 03:00 CEST (right after jump)
		time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC),  // 14:00 CEST
		time.Date(2026, 3, 29, 21, 59, 0, 0, time.UTC), // 23:59 CEST
	} {
		insertMessage(t, c, ts, 100)
	}
	// This one belongs to 2026-03-30 local (00:00 CEST).
	insertMessage(t, c, time.Date(2026, 3, 29, 22, 0, 0, 0, time.UTC), 999)

	from := DayStartLocal(time.Date(2026, 3, 29, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 3, 31, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Tokens != 500 {
		t.Errorf("2026-03-29 (23h day) tokens = %d, want 500", buckets[0].Tokens)
	}
	if buckets[1].Tokens != 999 {
		t.Errorf("2026-03-30 tokens = %d, want 999", buckets[1].Tokens)
	}
}

func TestTokenBuckets_24h_DST_FallBack(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 2026-10-25 Europe/Berlin: clocks fall 03:00 CEST -> 02:00 CET.
	// Local day is 25h, from 2026-10-24T22:00:00Z to 2026-10-25T23:00:00Z.
	for _, ts := range []time.Time{
		time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC), // 00:00 CEST
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC), // 02:30 CEST (first 02:xx)
		time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC), // 02:30 CET  (second 02:xx after fall-back)
		time.Date(2026, 10, 25, 22, 59, 0, 0, time.UTC), // 23:59 CET
	} {
		insertMessage(t, c, ts, 100)
	}
	// Next day:
	insertMessage(t, c, time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC), 999)

	from := DayStartLocal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Tokens != 400 {
		t.Errorf("2026-10-25 (25h day) tokens = %d, want 400", buckets[0].Tokens)
	}
	if buckets[1].Tokens != 999 {
		t.Errorf("2026-10-26 tokens = %d, want 999", buckets[1].Tokens)
	}
}

func TestTokenBuckets_24h_HalfHourOffsetTz(t *testing.T) {
	withTimeLocal(t, "Asia/Kolkata") // UTC+5:30, no DST
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 2026-05-13T18:29:00Z = 2026-05-13T23:59:00 IST (still local day -13).
	// 2026-05-13T18:30:00Z = 2026-05-14T00:00:00 IST (local day -14).
	insertMessage(t, c, time.Date(2026, 5, 13, 18, 29, 0, 0, time.UTC), 100)
	insertMessage(t, c, time.Date(2026, 5, 13, 18, 30, 0, 0, time.UTC), 200)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.TokenBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[0].Tokens != 100 || buckets[1].Tokens != 200 {
		t.Errorf("IST 30-min offset: %+v, want [100 200]", buckets)
	}
}

// insertMessageCost is a cost-only variant of insertMessage.
func insertMessageCost(t *testing.T, c *Cache, ts time.Time, cost float64) {
	t.Helper()
	_, err := c.DB().Exec(`
INSERT INTO messages
(session_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch)
VALUES('s','p',?,'assistant','m',0,0,0,0,0,?,'v1',0,0,'','','')`,
		ts.UTC().Format("2006-01-02T15:04:05.000Z07:00"), cost)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestCostBuckets_24h_LocalAlignment(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessageCost(t, c, time.Date(2026, 5, 13, 21, 59, 0, 0, time.UTC), 1.00)
	insertMessageCost(t, c, time.Date(2026, 5, 13, 22, 0, 0, 0, time.UTC), 2.50)
	insertMessageCost(t, c, time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), 0.50)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.CostBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Cost != 1.00 {
		t.Errorf("2026-05-13 cost = %v, want 1.00", buckets[0].Cost)
	}
	if buckets[1].Cost != 3.00 {
		t.Errorf("2026-05-14 cost = %v, want 3.00", buckets[1].Cost)
	}
}

func TestCostBuckets_24h_DST_SpringForward(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	for _, ts := range []time.Time{
		time.Date(2026, 3, 28, 23, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 29, 1, 0, 0, 0, time.UTC),
		time.Date(2026, 3, 29, 21, 59, 0, 0, time.UTC),
	} {
		insertMessageCost(t, c, ts, 1.0)
	}

	from := DayStartLocal(time.Date(2026, 3, 29, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 3, 30, 0, 0, 0, 0, time.Local))
	buckets, err := c.CostBuckets(24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Cost != 3.0 {
		t.Errorf("DST spring-forward: %+v, want one bucket of 3.0", buckets)
	}
}

func TestZoomLabel_DefaultFallback(t *testing.T) {
	if got := zoomLabel(30 * time.Minute); got != "30m0s" {
		t.Errorf("zoomLabel(30m) = %q, want %q (default fallback)", got, "30m0s")
	}
}

func TestInsertMessages_StampsPricingVersion(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab := pricing.Table{
		Version:  "test-version-2026-05-11",
		Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-7": {InputPerMtok: 5.00},
		},
	}
	msg := parse.Message{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 1000,
	}
	if err := c.InsertMessages([]parse.Message{msg}, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	var got string
	if err := c.DB().QueryRow(
		`SELECT pricing_version FROM messages WHERE session_id = ?`,
		"s1",
	).Scan(&got); err != nil {
		t.Fatalf("query pricing_version: %v", err)
	}
	if got != tab.Version {
		t.Errorf("pricing_version = %q, want %q", got, tab.Version)
	}
}

func TestCostBuckets_ContiguousRange(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// Same shape as TestTokenBuckets_ContiguousRange so a future Metric
	// refactor (#93) can grep for the parallel structure.
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
	buckets, err := c.CostBuckets(5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}

	if len(buckets) != 12 {
		t.Fatalf("want 12 buckets, got %d: %+v", len(buckets), buckets)
	}
	for i, b := range buckets {
		want := from.Add(time.Duration(i) * 5 * time.Minute)
		if !b.BucketStart.Equal(want) {
			t.Errorf("bucket[%d].BucketStart = %v, want %v", i, b.BucketStart, want)
		}
	}
	// Indices 10 (11:50, 1 msg) and 11 (11:55 + 11:57, 2 msgs) carry cost.
	// Compute the expected per-bucket cost from the same pricing.Table the
	// production code uses, so a pricing.json change doesn't make this test
	// drift; we just want to assert the SUM aggregator matches CostFor.
	cost1, _ := tab.CostFor(msgs[0])
	cost2, _ := tab.CostFor(msgs[1])
	cost3, _ := tab.CostFor(msgs[2])
	wantBucket10 := cost1
	wantBucket11 := cost2 + cost3
	for i, b := range buckets {
		switch i {
		case 10:
			if !approxEqual(b.Cost, wantBucket10, 1e-9) {
				t.Errorf("bucket[10].Cost = %v, want %v", b.Cost, wantBucket10)
			}
		case 11:
			if !approxEqual(b.Cost, wantBucket11, 1e-9) {
				t.Errorf("bucket[11].Cost = %v, want %v", b.Cost, wantBucket11)
			}
		default:
			if b.Cost != 0 {
				t.Errorf("bucket[%d].Cost = %v, want 0 (gap)", i, b.Cost)
			}
		}
	}
}

// approxEqual is the float comparison helper for cost-aggregation tests.
// Cost is computed in Go via pricing.CostFor and SUMmed in SQLite as REAL,
// so a strict == would be brittle to FP rounding across the round-trip.
func approxEqual(a, b, eps float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= eps
}

func TestCostBuckets_AllEmpty(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC)
	buckets, err := c.CostBuckets(15*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 24 {
		t.Fatalf("want 24 buckets, got %d", len(buckets))
	}
	for i, b := range buckets {
		if b.Cost != 0 {
			t.Errorf("bucket[%d].Cost = %v, want 0", i, b.Cost)
		}
		if i > 0 && !b.BucketStart.After(buckets[i-1].BucketStart) {
			t.Errorf("bucket starts not monotonic at index %d", i)
		}
	}
}

func TestCostBuckets_PricingUnknownContributesZero(t *testing.T) {
	// pricing_unknown=1 messages store cost_usd_estimate=0 at ingest, so
	// SUM(cost_usd_estimate) returns 0 for buckets containing only unpriced
	// models. Documented as a silent under-report in the spec; this test
	// pins the behaviour so a future "exclude unpriced" change is a
	// deliberate decision, not an accident.
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	msgs := []parse.Message{
		{SessionID: "s1", ProjectSlug: "p", Model: "model-not-in-pricing-json",
			Timestamp: ts, InputTokens: 1000, OutputTokens: 500},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	buckets, err := c.CostBuckets(5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}
	for i, b := range buckets {
		if b.Cost != 0 {
			t.Errorf("bucket[%d].Cost = %v, want 0 (unpriced model)", i, b.Cost)
		}
	}
}

func TestAllFileOffsets_Empty(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	offsets, err := c.AllFileOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if offsets == nil {
		t.Fatal("AllFileOffsets returned nil map on empty cache; want empty non-nil map")
	}
	if len(offsets) != 0 {
		t.Errorf("len = %d, want 0", len(offsets))
	}
}

func TestAllFileOffsets_ReturnsAllRows(t *testing.T) {
	c, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	want := map[string]int64{
		"/tmp/a.jsonl": 100,
		"/tmp/b.jsonl": 200,
		"/tmp/c.jsonl": 300,
	}
	for path, off := range want {
		if err := c.RecordFile(path, 1, off, 0); err != nil {
			t.Fatal(err)
		}
	}

	got, err := c.AllFileOffsets()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("len = %d, want %d", len(got), len(want))
	}
	for path, wantOff := range want {
		if got[path] != wantOff {
			t.Errorf("offset[%s] = %d, want %d", path, got[path], wantOff)
		}
	}
}
