package cache

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// timePtr returns &t. Test-only helper to satisfy the pointer-typed
// anthro.Bucket.ResetsAt field for sites that build the value inline
// (Go can't take the address of a function-call result directly).
func timePtr(t time.Time) *time.Time { return &t }

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

	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	row := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('messages','files','meta','usage_samples')`)
	var n int
	if err := row.Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("expected 4 tables, got %d", n)
	}
}

func TestInsertMessages(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count = %d, want 1", n)
	}

	var cost float64
	var unknown int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT cost_usd_estimate, pricing_unknown FROM messages`).Scan(&cost, &unknown); err != nil {
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	if err := c.RecordFile(t.Context(), "/tmp/x.jsonl", 1234, 5678, 42); err != nil {
		t.Fatal(err)
	}
	mtime, off, line, found, err := c.GetFile(t.Context(), "/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mtime != 1234 || off != 5678 || line != 42 {
		t.Errorf("got mtime=%d off=%d line=%d found=%v", mtime, off, line, found)
	}

	// Update existing record
	if err := c.RecordFile(t.Context(), "/tmp/x.jsonl", 9999, 8888, 99); err != nil {
		t.Fatal(err)
	}
	mtime, _, _, _, _ = c.GetFile(t.Context(), "/tmp/x.jsonl")
	if mtime != 9999 {
		t.Errorf("after update mtime = %d", mtime)
	}
}

func TestInsertMessagesIdempotent(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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

	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count after duplicate insert = %d, want 1", n)
	}
}

func TestRecordUsageSample_RoundTrip(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	fiveResets := when.Add(2 * time.Hour)
	sevenResets := when.Add(48 * time.Hour)
	util := 42.5
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: &fiveResets},
		SevenDay: &anthro.Bucket{Utilization: 67.0, ResetsAt: &sevenResets},
		ExtraUsage: &anthro.ExtraUsage{
			IsEnabled: true, MonthlyLimit: 100, UsedCredits: 42, Utilization: &util, Currency: "USD",
		},
	}

	if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var ts int64
	var src string
	var fivePct, sevenPct, extraPct sql.NullFloat64
	var fiveResetsGot, sevenResetsGot, extraCurrency sql.NullString
	var extraEnabled sql.NullInt64
	var extraLimit, extraUsed sql.NullFloat64
	err = c.DB().QueryRowContext(t.Context(), `SELECT
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	first := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 10.0, ResetsAt: timePtr(when.Add(time.Hour))}}
	second := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 99.0, ResetsAt: timePtr(when.Add(time.Hour))}}

	if err := c.RecordUsageSample(t.Context(), first, when); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsageSample(t.Context(), second, when); err != nil {
		t.Fatalf("second insert should be a silent no-op, got: %v", err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM usage_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("rows = %d, want 1 (INSERT OR IGNORE should drop the duplicate)", n)
	}

	var fivePct sql.NullFloat64
	if err := c.DB().QueryRowContext(t.Context(), `SELECT five_hour_pct FROM usage_samples`).Scan(&fivePct); err != nil {
		t.Fatal(err)
	}
	if !fivePct.Valid || fivePct.Float64 != 10.0 {
		t.Errorf("expected first row to win (five_hour_pct 10.0), got %+v", fivePct)
	}
}

func TestPruneUsageSamples(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
		u := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: float64(i), ResetsAt: timePtr(when.Add(time.Hour))}}
		if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
			t.Fatal(err)
		}
	}

	cutoff := base.Add(-60 * time.Second) // boundary: drop the -100s row, keep -50s and base
	n, err := c.PruneUsageSamples(t.Context(), cutoff)
	if err != nil {
		t.Fatalf("PruneUsageSamples: %v", err)
	}
	if n != 1 {
		t.Errorf("rows deleted = %d, want 1", n)
	}

	var remaining int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM usage_samples`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 2 {
		t.Errorf("remaining rows = %d, want 2", remaining)
	}

	// Cutoff is strictly less-than: a row exactly at cutoff is kept.
	var earliest int64
	if err := c.DB().QueryRowContext(t.Context(), `SELECT MIN(ts) FROM usage_samples`).Scan(&earliest); err != nil {
		t.Fatal(err)
	}
	if earliest != base.Add(-50*time.Second).Unix() {
		t.Errorf("earliest remaining ts = %d, want %d", earliest, base.Add(-50*time.Second).Unix())
	}
}

func TestRecordUsageSample_NilBucket(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 5, 9, 12, 34, 56, 0, time.UTC)
	u := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.5, ResetsAt: timePtr(when.Add(2 * time.Hour))},
		// SevenDay deliberately nil
	}

	if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var fivePct sql.NullFloat64
	var fiveResets sql.NullString
	var sevenPct sql.NullFloat64
	var sevenResets sql.NullString
	err = c.DB().QueryRowContext(t.Context(), `SELECT five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at FROM usage_samples`).Scan(&fivePct, &fiveResets, &sevenPct, &sevenResets)
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

func TestUtilizationSincePerColumnNullPolicy(t *testing.T) {
	resetNullResetsWarnedForTest()

	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 5h: pct non-null, resets_at NULL → idle, keep.
	// 7d: pct non-null, resets_at NULL → glitch, filter.
	if _, err := c.DB().ExecContext(t.Context(), `
		INSERT INTO usage_samples(ts, source, five_hour_pct, five_hour_resets_at, seven_day_pct, seven_day_resets_at) VALUES
			(1, 'api', 0,  NULL,                   88, '2026-05-10T09:00:00Z'),
			(2, 'api', 5,  '2026-05-09T16:10:00Z', 89, NULL),
			(3, 'api', 10, '2026-05-09T16:10:00Z', 90, '2026-05-10T09:00:00Z')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	fh, err := c.UtilizationSince(t.Context(), "five_hour_pct", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("five_hour: %v", err)
	}
	if len(fh) != 3 {
		t.Errorf("five_hour: expected 3 points (idle rows kept), got %d: %+v", len(fh), fh)
	}

	sd, err := c.UtilizationSince(t.Context(), "seven_day_pct", time.Unix(0, 0))
	if err != nil {
		t.Fatalf("seven_day: %v", err)
	}
	if len(sd) != 2 {
		t.Errorf("seven_day: expected 2 points (glitch row filtered), got %d: %+v", len(sd), sd)
	}
}

func TestSevenDaySamplesSinceSkipsNullResetsAt(t *testing.T) {
	resetNullResetsWarnedForTest()

	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Mix: two valid rows, two glitch rows (pct non-null, resets_at NULL).
	if _, err := c.DB().ExecContext(t.Context(), `
		INSERT INTO usage_samples(ts, source, seven_day_pct, seven_day_resets_at) VALUES
			(1, 'api', 88, '2026-05-10T09:00:00Z'),
			(2, 'api', 89, NULL),
			(3, 'api', 90, '2026-05-10T09:00:00Z'),
			(4, 'api', 91, NULL)
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}

	got, err := c.SevenDaySamplesSince(t.Context(), time.Unix(0, 0))
	if err != nil {
		t.Fatalf("SevenDaySamplesSince: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 valid samples (glitch rows filtered), got %d: %+v", len(got), got)
	}
	for _, s := range got {
		if s.Pct != 88 && s.Pct != 90 {
			t.Errorf("unexpected sample passed through: %+v", s)
		}
	}
}

func TestWarnOnceNullResets(t *testing.T) {
	// Reset the package-level once-flags to a clean state — necessary
	// because Go tests share package state. Use the unexported reset
	// hook added in the impl step.
	resetNullResetsWarnedForTest()

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	warnOnceNullResets("seven_day_resets_at")
	warnOnceNullResets("seven_day_resets_at") // suppressed
	warnOnceNullResets("seven_day_sonnet_resets_at")

	got := buf.String()
	if count := strings.Count(got, "filtered row with null resets_at"); count != 2 {
		t.Errorf("expected 2 WARN lines (one per column), got %d:\n%s", count, got)
	}
	if !strings.Contains(got, `column=seven_day_resets_at`) {
		t.Errorf("missing seven_day column attribute: %s", got)
	}
	if !strings.Contains(got, `column=seven_day_sonnet_resets_at`) {
		t.Errorf("missing seven_day_sonnet column attribute: %s", got)
	}
}

func TestOpenNormalisesLegacyResetsAtSentinel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	// Seed three rows with the sentinel across multiple *_resets_at
	// columns, simulating the pre-fix corruption pattern.
	if _, err := c.DB().ExecContext(t.Context(), `
		INSERT INTO usage_samples(ts, source,
			five_hour_pct,            five_hour_resets_at,
			seven_day_pct,            seven_day_resets_at,
			seven_day_omelette_pct,   seven_day_omelette_resets_at)
		VALUES
			(1, 'api', 0,    '0001-01-01T00:00:00Z', 89, '2026-05-10T09:00:00Z', 0, '0001-01-01T00:00:00Z'),
			(2, 'api', 10,   '2026-05-09T16:10:00Z', 90, '0001-01-01T00:00:00Z', 0, '0001-01-01T00:00:00Z'),
			(3, 'api', NULL, NULL,                   91, '2026-05-10T09:00:00Z', 0, '0001-01-01T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	c.Close()

	// Re-open: the normalisation should run and convert all sentinel
	// strings to NULL.
	c, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer c.Close()

	var sentinelCount int
	if err := c.DB().QueryRowContext(t.Context(), `
		SELECT count(*) FROM usage_samples
		WHERE five_hour_resets_at          = '0001-01-01T00:00:00Z'
		   OR seven_day_resets_at          = '0001-01-01T00:00:00Z'
		   OR seven_day_omelette_resets_at = '0001-01-01T00:00:00Z'
	`).Scan(&sentinelCount); err != nil {
		t.Fatal(err)
	}
	if sentinelCount != 0 {
		t.Errorf("expected 0 sentinel rows after Open, got %d", sentinelCount)
	}

	// Spot-check: row 1's five_hour_resets_at is now NULL; row 2's
	// seven_day_resets_at is now NULL; row 3's seven_day_omelette is NULL.
	var n int
	if err := c.DB().QueryRowContext(t.Context(), `
		SELECT count(*) FROM usage_samples
		WHERE ts = 1 AND five_hour_resets_at IS NULL AND seven_day_resets_at = '2026-05-10T09:00:00Z'
	`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("row 1 not normalised correctly: matched %d", n)
	}

	// Idempotency: a third Open should be a no-op.
	c.Close()
	c, err = Open(t.Context(), path)
	if err != nil {
		t.Fatalf("third open: %v", err)
	}
	defer c.Close()
}

func TestRecordUsageSampleNullResetsAt(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	u := anthro.Usage{
		FiveHour:         &anthro.Bucket{Utilization: 0.0, ResetsAt: nil},
		SevenDay:         &anthro.Bucket{Utilization: 0.0, ResetsAt: nil},
		SevenDayOmelette: &anthro.Bucket{Utilization: 0.0, ResetsAt: nil},
	}
	if err := c.RecordUsageSample(t.Context(), u, time.Unix(1_700_000_000, 0)); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `
		SELECT count(*) FROM usage_samples
		WHERE five_hour_resets_at IS NULL
		  AND seven_day_resets_at IS NULL
		  AND seven_day_omelette_resets_at IS NULL`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("expected 1 row with all *_resets_at NULL, got %d", n)
	}
}

func TestOpenWipesOnSchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.db")

	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	tab, _ := pricing.Load()
	if err := c.InsertMessages(t.Context(), []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}

	if _, err := c.DB().ExecContext(t.Context(), `UPDATE meta SET value = '0' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}

	c2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	var n int
	if err := c2.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("messages count after wipe = %d, want 0", n)
	}

	var version string
	if err := c2.DB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("schema_version after wipe = %q, want %q", version, SchemaVersion)
	}
}

func TestIOTokenBuckets_ContiguousRange(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
		{
			SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts1, InputTokens: 1000, OutputTokens: 500,
		},
		{
			SessionID: "s2", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts2, InputTokens: 2000, OutputTokens: 800,
		},
		{
			SessionID: "s3", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts3, InputTokens: 500, OutputTokens: 200,
		},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	buckets, err := c.IOTokenBuckets(t.Context(), 5*time.Minute, from, to)
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
	// Aggregate is SUM(input_tokens + output_tokens) — cache_* columns excluded.
	// See issue #232 (revises the output-only choice made in #209).
	for i, b := range buckets {
		switch i {
		case 10:
			if b.Tokens != 1500 {
				t.Errorf("bucket[10].Tokens = %d, want 1500 (1000 input + 500 output)", b.Tokens)
			}
		case 11:
			if b.Tokens != 3500 {
				t.Errorf("bucket[11].Tokens = %d, want 3500 (2000+800 + 500+200)", b.Tokens)
			}
		default:
			if b.Tokens != 0 {
				t.Errorf("bucket[%d].Tokens = %d, want 0 (gap)", i, b.Tokens)
			}
		}
	}
}

// TestIOTokenBuckets_InputOutput_CacheExcluded verifies that the
// token-bucket aggregator sums input_tokens + output_tokens per bucket
// and excludes the three cache columns (cache_read, cache_write_5m,
// cache_write_1h). Matches Claude Code /usage semantics — see #232.
func TestIOTokenBuckets_InputOutput_CacheExcluded(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// One message: small input+output, large cache values. If the aggregate
	// were the 5-column sum the bucket would total 1290; under
	// SUM(input_tokens + output_tokens) it must be exactly 15.
	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	msgs := []parse.Message{{
		SessionID:          "s1",
		ProjectSlug:        "p",
		Model:              "claude-sonnet-4-6",
		Timestamp:          ts,
		InputTokens:        10,
		OutputTokens:       5,
		CacheReadTokens:    1000,
		CacheWrite5mTokens: 200,
		CacheWrite1hTokens: 75,
	}}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 45, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 11, 55, 0, 0, time.UTC)
	buckets, err := c.IOTokenBuckets(t.Context(), 5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 {
		t.Fatalf("want 2 buckets, got %d: %+v", len(buckets), buckets)
	}
	// buckets[0] is 11:45-11:49 (empty); buckets[1] is 11:50-11:54 (the row).
	if buckets[0].Tokens != 0 {
		t.Errorf("buckets[0].Tokens = %d, want 0 (empty)", buckets[0].Tokens)
	}
	if buckets[1].Tokens != 15 {
		t.Errorf("buckets[1].Tokens = %d, want 15 (= 10 input + 5 output); cache columns must NOT contribute",
			buckets[1].Tokens)
	}
}

func TestIOTokenBuckets_AllEmpty(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC)
	buckets, err := c.IOTokenBuckets(t.Context(), 15*time.Minute, from, to)
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

func TestIOTokenBuckets_BoundsSnap(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Mid-bucket bounds: 11:03:30 → 12:07:45 with 5m buckets snaps to
	// [11:00, 12:05) → 13 buckets.
	from := time.Date(2026, 5, 9, 11, 3, 30, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 7, 45, 0, time.UTC)
	buckets, err := c.IOTokenBuckets(t.Context(), 5*time.Minute, from, to)
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

func TestIOTokenBuckets_IncludesInFlightBucket(t *testing.T) {
	// Regression: when callers anchor at to = BucketAlign(now) + dur, the
	// in-flight bucket containing now must be included as the rightmost
	// bucket in the [from, to) range — otherwise a freshly-recorded
	// message stays invisible until the bucket boundary ticks over.
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
		{
			SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: now, InputTokens: 1000, OutputTokens: 500,
		},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	dur := 5 * time.Minute
	to := BucketAlign(now, dur).Add(dur)
	from := to.Add(-time.Hour)
	buckets, err := c.IOTokenBuckets(t.Context(), dur, from, to)
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
		t.Errorf("rightmost Tokens = %d, want 1500 (= 1000 input + 500 output of in-flight message)", last.Tokens)
	}
}

func TestEarliestMessageTime_Empty(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ts, ok, err := c.EarliestMessageTime(t.Context())
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	want := time.Date(2026, 4, 1, 12, 30, 0, 0, time.UTC)
	if err := c.InsertMessages(t.Context(), []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   want,
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}

	ts, ok, err := c.EarliestMessageTime(t.Context())
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	if err := c.InsertMessages(t.Context(), []parse.Message{
		mk("s2", mid),
		mk("s3", latest),
		mk("s1", earliest),
	}, tab); err != nil {
		t.Fatal(err)
	}

	ts, ok, err := c.EarliestMessageTime(t.Context())
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// busy_timeout, synchronous, and temp_store are per-connection. Holding
	// one Conn open forces sql.DB to hand us a second, distinct connection —
	// proving the DSN pragmas hit every conn, not just the one db.Exec
	// happened to grab. Without the DSN, conn2 would report busy_timeout=0.
	ctx := t.Context()
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
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
			if err := c.InsertMessages(t.Context(), []parse.Message{msg}, tab); err != nil {
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
			if _, err := c.IOTokenBuckets(t.Context(), 5*time.Minute, from, to); err != nil {
				errs <- fmt.Errorf("IOTokenBuckets: %w", err)
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
		if err := os.WriteFile(base+suffix, []byte("planted"), 0o644); err != nil {
			t.Fatalf("plant %s: %v", base+suffix, err)
		}
	}

	if err := removeWithSiblings(base); err != nil {
		t.Fatalf("removeWithSiblings: %v", err)
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

	if err := removeWithSiblings(base); err != nil {
		t.Fatalf("removeWithSiblings on missing tree: %v", err)
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
// IOTokenBuckets compares its query bounds in UTC, regardless of the
// time.Time zone the caller hands in. Without normalization at both
// boundaries the WHERE ts >= ? AND ts < ? lex comparison silently
// misbehaves when a caller passes non-UTC values.
func TestInsertMessages_NormalizesNonUTCTimestamp(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
		{
			SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts, InputTokens: 1000, OutputTokens: 500,
		},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, loc)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, loc)
	buckets, err := c.IOTokenBuckets(t.Context(), 5*time.Minute, from, to)
	if err != nil {
		t.Fatal(err)
	}

	// 1 hour / 5 min = 12 buckets. The message at 11:50 (loc) lands in
	// the bucket starting 09:50 UTC == 11:50 in loc, i.e. index 10.
	if len(buckets) != 12 {
		t.Fatalf("want 12 buckets, got %d: %+v", len(buckets), buckets)
	}
	if buckets[10].Tokens != 1500 {
		t.Errorf("bucket[10].Tokens = %d, want 1500 (= 1000 input + 500 output of the non-UTC insert)",
			buckets[10].Tokens)
	}
}

func TestIntegrityOK_Healthy(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ok, err := c.IntegrityOK(t.Context())
	if err != nil {
		t.Fatalf("IntegrityOK: unexpected err: %v", err)
	}
	if !ok {
		t.Fatalf("IntegrityOK on healthy cache returned false")
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
	c, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	var pageSize int
	if err := c.DB().QueryRowContext(t.Context(), `PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if pageSize != 4096 {
		t.Fatalf("page_size = %d, want 4096 (test corruption offset assumes default)", pageSize)
	}
	tab, _ := pricing.Load()
	if err := c.InsertMessages(t.Context(), []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "p",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}, tab); err != nil {
		t.Fatal(err)
	}
	if _, err := c.DB().ExecContext(t.Context(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
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
	c2, err := Open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	ok, err := c2.IntegrityOK(t.Context())
	if err != nil {
		t.Fatalf("IntegrityOK: unexpected err: %v", err)
	}
	if ok {
		t.Fatalf("IntegrityOK on corrupt cache returned true")
	}
}

func TestIntegrityOK_CtxCancelled(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err = c.IntegrityOK(ctx)
	if err == nil {
		t.Fatalf("IntegrityOK with cancelled ctx: expected error, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("IntegrityOK with cancelled ctx: got %v, want context.Canceled", err)
	}
}

func TestInsertMessages_PersistsCwdAndGitBranch(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), filepath.Join(dir, "state.db"))
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
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var cwd, branch string
	if err := c.DB().QueryRowContext(t.Context(),
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
	tsStr := ts.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err := c.DB().ExecContext(t.Context(), `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch)
VALUES('s',?,'p',?,'assistant','m',0,?,0,0,0,0,'v1',0,0,'','','')`,
		"synthetic:"+tsStr, tsStr, tokens)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestIOTokenBuckets_24h_LocalAlignment(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
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

func TestIOTokenBuckets_24h_EmptyDays(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessage(t, c, time.Date(2026, 5, 13, 12, 0, 0, 0, time.UTC), 100)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 16, 0, 0, 0, 0, time.Local))
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
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

func TestIOTokenBuckets_24h_UTCFallback(t *testing.T) {
	withTimeLocal(t, "UTC")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessage(t, c, time.Date(2026, 5, 13, 23, 59, 0, 0, time.UTC), 100)
	insertMessage(t, c, time.Date(2026, 5, 14, 0, 1, 0, 0, time.UTC), 200)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || buckets[0].Tokens != 100 || buckets[1].Tokens != 200 {
		t.Errorf("UTC-local bucketing failed: %+v", buckets)
	}
}

func TestIOTokenBuckets_24h_DST_SpringForward(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
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

func TestIOTokenBuckets_24h_DST_FallBack(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// 2026-10-25 Europe/Berlin: clocks fall 03:00 CEST -> 02:00 CET.
	// Local day is 25h, from 2026-10-24T22:00:00Z to 2026-10-25T23:00:00Z.
	for _, ts := range []time.Time{
		time.Date(2026, 10, 24, 22, 0, 0, 0, time.UTC),  // 00:00 CEST
		time.Date(2026, 10, 25, 0, 30, 0, 0, time.UTC),  // 02:30 CEST (first 02:xx)
		time.Date(2026, 10, 25, 1, 30, 0, 0, time.UTC),  // 02:30 CET  (second 02:xx after fall-back)
		time.Date(2026, 10, 25, 22, 59, 0, 0, time.UTC), // 23:59 CET
	} {
		insertMessage(t, c, ts, 100)
	}
	// Next day:
	insertMessage(t, c, time.Date(2026, 10, 25, 23, 0, 0, 0, time.UTC), 999)

	from := DayStartLocal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local))
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
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

func TestIOTokenBuckets_24h_HalfHourOffsetTz(t *testing.T) {
	withTimeLocal(t, "Asia/Kolkata") // UTC+5:30, no DST
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	buckets, err := c.IOTokenBuckets(t.Context(), 24*time.Hour, from, to)
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
	tsStr := ts.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	_, err := c.DB().ExecContext(t.Context(), `
INSERT INTO messages
(session_id, message_id, project_slug, ts, role, model,
 input_tokens, output_tokens, cache_read_tokens,
 cache_write_5m_tokens, cache_write_1h_tokens,
 cost_usd_estimate, pricing_version, pricing_unknown,
 is_subagent, parent_session_id, cwd, git_branch)
VALUES('s',?,'p',?,'assistant','m',0,0,0,0,0,?,'v1',0,0,'','','')`,
		"synthetic:"+tsStr, tsStr, cost)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func TestCostBuckets_24h_LocalAlignment(t *testing.T) {
	withTimeLocal(t, "Europe/Berlin")
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	insertMessageCost(t, c, time.Date(2026, 5, 13, 21, 59, 0, 0, time.UTC), 1.00)
	insertMessageCost(t, c, time.Date(2026, 5, 13, 22, 0, 0, 0, time.UTC), 2.50)
	insertMessageCost(t, c, time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC), 0.50)

	from := DayStartLocal(time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local))
	to := DayStartLocal(time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local))
	buckets, err := c.CostBuckets(t.Context(), 24*time.Hour, from, to)
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
	buckets, err := c.CostBuckets(t.Context(), 24*time.Hour, from, to)
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	entry := pricing.Table{
		Version:  "test-version-2026-05-11",
		Currency: "USD",
		Models: map[string]pricing.ModelRate{
			"claude-opus-4-7": {InputPerMtok: 5.00},
		},
	}
	hist, err := pricing.HistoryForTest([]pricing.Table{entry})
	if err != nil {
		t.Fatalf("HistoryForTest: %v", err)
	}
	msg := parse.Message{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 1000,
	}
	if err := c.InsertMessages(t.Context(), []parse.Message{msg}, hist); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	var got string
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT pricing_version FROM messages WHERE session_id = ?`,
		"s1",
	).Scan(&got); err != nil {
		t.Fatalf("query pricing_version: %v", err)
	}
	if got != entry.Version {
		t.Errorf("pricing_version = %q, want %q", got, entry.Version)
	}
}

func TestCostBuckets_ContiguousRange(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// Same shape as TestIOTokenBuckets_ContiguousRange so a future Metric
	// refactor (#93) can grep for the parallel structure.
	ts1 := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	ts2 := time.Date(2026, 5, 9, 11, 55, 0, 0, time.UTC)
	ts3 := time.Date(2026, 5, 9, 11, 57, 0, 0, time.UTC)

	msgs := []parse.Message{
		{
			SessionID: "s1", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts1, InputTokens: 1000, OutputTokens: 500,
		},
		{
			SessionID: "s2", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts2, InputTokens: 2000, OutputTokens: 800,
		},
		{
			SessionID: "s3", ProjectSlug: "p", Model: "claude-sonnet-4-6",
			Timestamp: ts3, InputTokens: 500, OutputTokens: 200,
		},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	buckets, err := c.CostBuckets(t.Context(), 5*time.Minute, from, to)
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
	cost1, _, _ := tab.CostFor(msgs[0])
	cost2, _, _ := tab.CostFor(msgs[1])
	cost3, _, _ := tab.CostFor(msgs[2])
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	from := time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC)
	buckets, err := c.CostBuckets(t.Context(), 15*time.Minute, from, to)
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Date(2026, 5, 9, 11, 50, 0, 0, time.UTC)
	msgs := []parse.Message{
		{
			SessionID: "s1", ProjectSlug: "p", Model: "model-not-in-pricing-json",
			Timestamp: ts, InputTokens: 1000, OutputTokens: 500,
		},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	from := time.Date(2026, 5, 9, 11, 0, 0, 0, time.UTC)
	to := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	buckets, err := c.CostBuckets(t.Context(), 5*time.Minute, from, to)
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	offsets, err := c.AllFileOffsets(t.Context())
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
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
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
		if err := c.RecordFile(t.Context(), path, 1, off, 0); err != nil {
			t.Fatal(err)
		}
	}

	got, err := c.AllFileOffsets(t.Context())
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

func TestSevenDaySamplesSince_OrderingAndFilter(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), dir+"/state.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	resetsA := time.Date(2026, 5, 18, 11, 0, 0, 0, time.UTC) // bucket A reset
	mkUsage := func(pct float64, resetsAt time.Time) anthro.Usage {
		return anthro.Usage{
			SevenDay: &anthro.Bucket{Utilization: pct, ResetsAt: &resetsAt},
		}
	}

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	// Three samples: 26h ago, 12h ago, now. Insert out-of-order to verify ORDER BY.
	if err := c.RecordUsageSample(t.Context(), mkUsage(40.0, resetsA), now); err != nil {
		t.Fatalf("Record now: %v", err)
	}
	if err := c.RecordUsageSample(t.Context(), mkUsage(20.0, resetsA), now.Add(-26*time.Hour)); err != nil {
		t.Fatalf("Record -26h: %v", err)
	}
	if err := c.RecordUsageSample(t.Context(), mkUsage(30.0, resetsA), now.Add(-12*time.Hour)); err != nil {
		t.Fatalf("Record -12h: %v", err)
	}

	since := now.Add(-24 * time.Hour)
	got, err := c.SevenDaySamplesSince(t.Context(), since)
	if err != nil {
		t.Fatalf("SevenDaySamplesSince: %v", err)
	}

	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (the -26h sample is older than since)", len(got))
	}
	if got[0].Pct != 30.0 || got[1].Pct != 40.0 {
		t.Errorf("pct order = %v / %v, want 30.0 then 40.0", got[0].Pct, got[1].Pct)
	}
	if !got[0].At.Before(got[1].At) {
		t.Errorf("not ordered oldest-first: %v then %v", got[0].At, got[1].At)
	}
	if got[0].ResetsAt == "" {
		t.Errorf("ResetsAt empty; want raw stored value")
	}
}

func TestSevenDaySamplesSince_NullPctExcluded(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), dir+"/state.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)

	// One sample with SevenDay populated; one with SevenDay nil (NULL pct).
	if err := c.RecordUsageSample(t.Context(), anthro.Usage{
		SevenDay: &anthro.Bucket{Utilization: 25.0, ResetsAt: timePtr(now.Add(96 * time.Hour))},
	}, now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("Record populated: %v", err)
	}
	if err := c.RecordUsageSample(t.Context(), anthro.Usage{}, now.Add(-1*time.Hour)); err != nil {
		t.Fatalf("Record nil bucket: %v", err)
	}

	got, err := c.SevenDaySamplesSince(t.Context(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("SevenDaySamplesSince: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (NULL pct row excluded)", len(got))
	}
	if got[0].Pct != 25.0 {
		t.Errorf("pct = %v, want 25.0", got[0].Pct)
	}
}

func TestSevenDaySamplesSince_ResetsAtNormalized(t *testing.T) {
	// The Anthropic API returns slightly different nanosecond timestamps for the
	// same logical reset boundary on each call. Without normalization the equality
	// filter in projectSevenDay treats every sample as its own bucket, leaving
	// <2 filtered samples and silently falling back to the linear projection.
	// This test pins the fix: sub-second jitter must be collapsed to second precision.
	dir := t.TempDir()
	c, err := Open(t.Context(), dir+"/state.db")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	// Same logical reset (2026-05-17T09:00:00) but nanoseconds differ per sample.
	resets1 := time.Date(2026, 5, 17, 9, 0, 0, 745677000, time.UTC)
	resets2 := time.Date(2026, 5, 17, 9, 0, 0, 719504000, time.UTC)

	if err := c.RecordUsageSample(t.Context(), anthro.Usage{
		SevenDay: &anthro.Bucket{Utilization: 70.0, ResetsAt: &resets1},
	}, now.Add(-12*time.Hour)); err != nil {
		t.Fatalf("Record 1: %v", err)
	}
	if err := c.RecordUsageSample(t.Context(), anthro.Usage{
		SevenDay: &anthro.Bucket{Utilization: 80.0, ResetsAt: &resets2},
	}, now); err != nil {
		t.Fatalf("Record 2: %v", err)
	}

	got, err := c.SevenDaySamplesSince(t.Context(), now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("SevenDaySamplesSince: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].ResetsAt != got[1].ResetsAt {
		t.Errorf("ResetsAt not normalized: %q vs %q (nanosecond jitter survived)", got[0].ResetsAt, got[1].ResetsAt)
	}
}

func TestUtilizationSince(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	now := time.Now().UTC().Truncate(time.Second)

	samples := []struct {
		ts       time.Time
		fiveHour *anthro.Bucket
		sevenDay *anthro.Bucket
	}{
		{now.Add(-6 * time.Minute), &anthro.Bucket{Utilization: 10.0, ResetsAt: timePtr(now.Add(time.Hour))}, &anthro.Bucket{Utilization: 5.0, ResetsAt: timePtr(now.Add(24 * time.Hour))}},
		{now.Add(-3 * time.Minute), &anthro.Bucket{Utilization: 25.0, ResetsAt: timePtr(now.Add(time.Hour))}, nil},
		{now, &anthro.Bucket{Utilization: 50.0, ResetsAt: timePtr(now.Add(time.Hour))}, &anthro.Bucket{Utilization: 15.0, ResetsAt: timePtr(now.Add(24 * time.Hour))}},
	}
	for _, s := range samples {
		u := anthro.Usage{FiveHour: s.fiveHour, SevenDay: s.sevenDay}
		if err := c.RecordUsageSample(t.Context(), u, s.ts); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}

	tests := []struct {
		name    string
		column  string
		since   time.Time
		wantLen int
		wantPct []float64
	}{
		{"five_hour_all", "five_hour_pct", now.Add(-10 * time.Minute), 3, []float64{10.0, 25.0, 50.0}},
		{"seven_day_all", "seven_day_pct", now.Add(-10 * time.Minute), 2, []float64{5.0, 15.0}},
		{"five_hour_partial", "five_hour_pct", now.Add(-4 * time.Minute), 2, []float64{25.0, 50.0}},
		{"no_rows", "five_hour_pct", now.Add(time.Hour), 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pts, err := c.UtilizationSince(t.Context(), tt.column, tt.since)
			if err != nil {
				t.Fatalf("UtilizationSince: %v", err)
			}
			if len(pts) != tt.wantLen {
				t.Fatalf("got %d points, want %d", len(pts), tt.wantLen)
			}
			for i, want := range tt.wantPct {
				if pts[i].Pct != want {
					t.Errorf("pts[%d].Pct = %f, want %f", i, pts[i].Pct, want)
				}
			}
		})
	}
}

func TestUtilizationSince_InvalidColumn(t *testing.T) {
	dir := t.TempDir()
	c, err := Open(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer c.Close()

	_, err = c.UtilizationSince(t.Context(), "DROP TABLE messages", time.Now())
	if err == nil {
		t.Fatal("expected error for invalid column, got nil")
	}
}

func TestInsertMessages_DedupOnMessageID(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	base := time.Date(2026, 5, 28, 15, 53, 58, 0, time.UTC)
	// Use a non-monotonic ramp so that min/first/last each yield a value
	// other than 64000, while only max yields exactly 64000.
	// Pattern: ramp linearly from 1 up to a peak of 64000 at the midpoint
	// (i==87), then back down to 1. This places the maximum at neither the
	// first nor the last element.
	outputRamp := func(i int) int64 {
		half := 175 / 2 // 87
		if i <= half {
			return int64(1 + (64000-1)*i/half)
		}
		return int64(1 + (64000-1)*(175-1-i)/half)
	}
	var msgs []parse.Message
	for i := range 175 {
		msgs = append(msgs, parse.Message{
			SessionID:    "sess-1",
			MessageID:    "msg_01EaHHYYfAp2yyszT7wAq64w",
			ProjectSlug:  "slug-a",
			Model:        "claude-opus-4-8",
			Timestamp:    base.Add(time.Duration(i) * time.Second),
			InputTokens:  200,
			OutputTokens: outputRamp(i),
		})
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	var out int64
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT count(*), COALESCE(MAX(output_tokens),0) FROM messages`).Scan(&n, &out); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1 (175 content-block lines, one message)", n)
	}
	if out != 64000 {
		t.Errorf("output_tokens = %d, want 64000 (MAX, not min/first/last)", out)
	}
}

func TestInsertMessages_CrossBatchUpsertMAX(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Date(2026, 5, 28, 15, 53, 58, 0, time.UTC)
	// Batch A: an early streaming partial line (lower cumulative usage across
	// all token columns). Strictly lower than batchB in every maxed column.
	batchA := []parse.Message{{
		SessionID:          "sess-1",
		MessageID:          "msg_x",
		ProjectSlug:        "slug-a",
		Model:              "claude-opus-4-8",
		Timestamp:          ts,
		InputTokens:        100,
		OutputTokens:       5000,
		CacheReadTokens:    50,
		CacheWrite5mTokens: 10,
		CacheWrite1hTokens: 5,
	}}
	// Batch B: the final line of the same message (full cumulative usage).
	// Strictly higher than batchA in every maxed column so that only MAX
	// semantics (not min/first/last) keeps batchB's values.
	batchB := []parse.Message{{
		SessionID:          "sess-1",
		MessageID:          "msg_x",
		ProjectSlug:        "slug-a",
		Model:              "claude-opus-4-8",
		Timestamp:          ts.Add(time.Minute),
		InputTokens:        200,
		OutputTokens:       64000,
		CacheReadTokens:    300,
		CacheWrite5mTokens: 150,
		CacheWrite1hTokens: 75,
	}}
	if err := c.InsertMessages(t.Context(), batchA, tab); err != nil {
		t.Fatal(err)
	}
	if err := c.InsertMessages(t.Context(), batchB, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	var gotInput, gotOutput, gotCacheRead, gotCacheWrite5m, gotCacheWrite1h int64
	var gotCost float64
	if err := c.DB().QueryRowContext(t.Context(), `
		SELECT count(*),
		       input_tokens, output_tokens,
		       cache_read_tokens, cache_write_5m_tokens, cache_write_1h_tokens,
		       cost_usd_estimate
		FROM messages`).Scan(
		&n,
		&gotInput, &gotOutput,
		&gotCacheRead, &gotCacheWrite5m, &gotCacheWrite1h,
		&gotCost,
	); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("row count = %d, want 1 (same message across two batches)", n)
	}
	if gotOutput != 64000 {
		t.Errorf("output_tokens = %d, want 64000 (MAX across batches)", gotOutput)
	}
	if gotInput != 200 {
		t.Errorf("input_tokens = %d, want 200 (MAX across batches)", gotInput)
	}
	if gotCacheRead != 300 {
		t.Errorf("cache_read_tokens = %d, want 300 (MAX across batches)", gotCacheRead)
	}
	if gotCacheWrite5m != 150 {
		t.Errorf("cache_write_5m_tokens = %d, want 150 (MAX across batches)", gotCacheWrite5m)
	}
	if gotCacheWrite1h != 75 {
		t.Errorf("cache_write_1h_tokens = %d, want 75 (MAX across batches)", gotCacheWrite1h)
	}
	// cost_usd_estimate must be batchB's cost (batchB has strictly higher token
	// counts so its cost exceeds batchA's cost; the UPSERT keeps the max).
	wantCost, _, _ := tab.CostFor(batchB[0])
	if !approxEqual(gotCost, wantCost, 1e-9) {
		t.Errorf("cost_usd_estimate = %v, want %v (MAX cost matches batchB)", gotCost, wantCost)
	}
}

func TestInsertMessages_TSCollapseMIN(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	first := time.Date(2026, 5, 28, 15, 53, 58, 0, time.UTC)
	msgs := []parse.Message{
		{SessionID: "sess-1", MessageID: "msg_x", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: first.Add(2 * time.Minute), OutputTokens: 64000},
		{SessionID: "sess-1", MessageID: "msg_x", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: first, OutputTokens: 64000},
		{SessionID: "sess-1", MessageID: "msg_x", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: first.Add(5 * time.Minute), OutputTokens: 64000},
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var ts string
	if err := c.DB().QueryRowContext(t.Context(), `SELECT ts FROM messages`).Scan(&ts); err != nil {
		t.Fatal(err)
	}
	want := first.Format(tsFormat)
	if ts != want {
		t.Errorf("ts = %q, want %q (earliest content-block line)", ts, want)
	}
}

func TestInsertMessages_FallbackSyntheticID(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ts := time.Date(2026, 5, 28, 15, 53, 58, 0, time.UTC)
	// Two distinct-ts messages with NO MessageID → two synthetic IDs → two rows.
	distinct := []parse.Message{
		{SessionID: "sess-1", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: ts, OutputTokens: 10},
		{SessionID: "sess-1", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: ts.Add(time.Minute), OutputTokens: 20},
	}
	if err := c.InsertMessages(t.Context(), distinct, tab); err != nil {
		t.Fatal(err)
	}
	// Same-ts no-MessageID message → collides with the first synthetic ID → still 2 rows.
	same := []parse.Message{
		{SessionID: "sess-1", ProjectSlug: "slug-a", Model: "claude-opus-4-8", Timestamp: ts, OutputTokens: 99},
	}
	if err := c.InsertMessages(t.Context(), same, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("row count = %d, want 2 (synthetic fallback keeps (session,ts) semantics)", n)
	}
	// The collided synthetic row (same session + ts) must have kept the MAX of
	// the two competing output_tokens values (99 over the original 10).
	tsStr := ts.UTC().Format(tsFormat)
	var gotOut int64
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT output_tokens FROM messages WHERE ts = ?`, tsStr).Scan(&gotOut); err != nil {
		t.Fatal(err)
	}
	if gotOut != 99 {
		t.Errorf("collided row output_tokens = %d, want 99 (MAX of 10 and 99)", gotOut)
	}
}

func TestInsertMessages_RegressionMultilineMessage(t *testing.T) {
	f, err := os.Open("testdata/multiline_message.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	msgs, err := parse.Parse(f, "slug-a")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(msgs) != 4 {
		t.Fatalf("parsed %d messages, want 4", len(msgs))
	}

	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	tab, _ := pricing.Load()
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatal(err)
	}

	var n int
	var totalOut int64
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT count(*), COALESCE(SUM(output_tokens),0) FROM messages`).Scan(&n, &totalOut); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("row count = %d, want 2 (msg_a collapsed from 3 lines, msg_b distinct)", n)
	}
	// msg_a contributes 64000 once (not 3x192000), msg_b contributes 1000.
	if totalOut != 65000 {
		t.Errorf("SUM(output_tokens) = %d, want 65000 (64000 + 1000, deduped)", totalOut)
	}
}

// seedHistory inserts n messages spread one per minute ending at `end`, each
// with a distinct message_id so none collapse via the UNIQUE(session_id,
// message_id) upsert. Shared by the bucket-query regression benchmarks (#378).
func seedHistory(b *testing.B, c *Cache, tab pricing.History, n int, end time.Time) {
	b.Helper()
	msgs := make([]parse.Message, n)
	for i := range n {
		msgs[i] = parse.Message{
			SessionID:   "s1",
			MessageID:   "m" + strconv.Itoa(i),
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   end.Add(-time.Duration(i) * time.Minute),
			InputTokens: int64(1000 + i),
		}
	}
	if err := c.InsertMessages(b.Context(), msgs, tab); err != nil {
		b.Fatalf("InsertMessages: %v", err)
	}
}

func benchBuckets(b *testing.B, cost bool) {
	tab, err := pricing.Load()
	if err != nil {
		b.Fatalf("pricing.Load: %v", err)
	}
	end := time.Now().UTC().Truncate(15 * time.Minute)
	for _, n := range []int{5_000, 30_000, 100_000} {
		b.Run(strconv.Itoa(n), func(b *testing.B) {
			c, err := Open(b.Context(), filepath.Join(b.TempDir(), "s.db"))
			if err != nil {
				b.Fatalf("cache.Open: %v", err)
			}
			defer c.Close()
			seedHistory(b, c, tab, n, end)
			from := end.Add(-time.Duration(n) * time.Minute)
			to := end.Add(15 * time.Minute)
			dur := 15 * time.Minute

			b.ReportAllocs()
			for b.Loop() {
				if cost {
					if _, err := c.CostBuckets(b.Context(), dur, from, to); err != nil {
						b.Fatal(err)
					}
				} else {
					if _, err := c.IOTokenBuckets(b.Context(), dur, from, to); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

// BenchmarkIOTokenBuckets / BenchmarkCostBuckets guard the full-history query
// cost (#378). Linear in total rows — the numbers the issue measured
// (~3.5/23/76 ms at 5k/30k/100k on M1 Max).
func BenchmarkIOTokenBuckets(b *testing.B) { benchBuckets(b, false) }
func BenchmarkCostBuckets(b *testing.B)    { benchBuckets(b, true) }

func TestSevenDaySamplesSince_BucketJitterMerged(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	base := time.Date(2026, 6, 2, 12, 0, 0, 0, time.UTC)
	// One logical 7d reset at 09:00:00 next-day, but the API returns it with
	// sub-second jitter straddling the whole-second boundary on three fetches.
	resetBoundary := time.Date(2026, 6, 3, 9, 0, 0, 0, time.UTC)
	jitter := []time.Duration{-100 * time.Millisecond, 100 * time.Millisecond, 1300 * time.Millisecond}
	for i, j := range jitter {
		reset := resetBoundary.Add(j)
		when := base.Add(time.Duration(i) * time.Minute) // distinct ts (unix seconds)
		if err := c.RecordUsageSample(t.Context(), anthro.Usage{
			SevenDay: &anthro.Bucket{Utilization: float64(10 + i), ResetsAt: &reset},
		}, when); err != nil {
			t.Fatalf("RecordUsageSample[%d]: %v", i, err)
		}
	}

	samples, err := c.SevenDaySamplesSince(t.Context(), base.Add(-time.Hour))
	if err != nil {
		t.Fatalf("SevenDaySamplesSince: %v", err)
	}
	if len(samples) != 3 {
		t.Fatalf("got %d samples, want 3", len(samples))
	}
	keys := map[string]struct{}{}
	for _, s := range samples {
		keys[s.ResetsAt] = struct{}{}
	}
	if len(keys) != 1 {
		t.Errorf("got %d distinct ResetsAt bucket keys, want 1 (jitter must merge): %v", len(keys), keys)
	}
}

func TestInsertMessagesAndRecordFile_Atomic(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	msgs := []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}

	if err := c.InsertMessagesAndRecordFile(t.Context(), msgs, tab, "/tmp/x.jsonl", 7, 200, 9); err != nil {
		t.Fatalf("InsertMessagesAndRecordFile: %v", err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("messages count = %d, want 1", n)
	}

	mtime, off, line, found, err := c.GetFile(t.Context(), "/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !found || mtime != 7 || off != 200 || line != 9 {
		t.Errorf("cursor: mtime=%d off=%d line=%d found=%v; want 7/200/9/true", mtime, off, line, found)
	}
}

func TestInsertMessagesAndRecordFile_EmptyMsgs(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()

	// Zero messages: the cursor must still advance, transactionally.
	if err := c.InsertMessagesAndRecordFile(t.Context(), nil, tab, "/tmp/x.jsonl", 3, 50, 2); err != nil {
		t.Fatalf("InsertMessagesAndRecordFile: %v", err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("messages count = %d, want 0", n)
	}

	_, off, line, found, err := c.GetFile(t.Context(), "/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if !found || off != 50 || line != 2 {
		t.Errorf("cursor: off=%d line=%d found=%v; want 50/2/true", off, line, found)
	}
}

func TestInsertMessagesAndRecordFile_RollbackOnFailure(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	msgs := []parse.Message{{
		SessionID:   "s1",
		ProjectSlug: "slug-a",
		Model:       "claude-opus-4-7",
		Timestamp:   time.Now(),
		InputTokens: 10,
	}}

	// Force the cursor write to fail mid-transaction: drop the files table so
	// the message insert succeeds but recordFileTx errors ("no such table:
	// files"). A correct atomic method rolls the whole tx back, discarding the
	// already-inserted message rows.
	if _, err := c.DB().ExecContext(t.Context(), `DROP TABLE files`); err != nil {
		t.Fatalf("drop files table: %v", err)
	}

	err = c.InsertMessagesAndRecordFile(t.Context(), msgs, tab, "/tmp/x.jsonl", 1, 100, 5)
	if err == nil {
		t.Fatal("expected error when files table is missing, got nil")
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(), `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("messages count = %d, want 0 (insert must roll back with the failed cursor write)", n)
	}
}

func TestRecordUsageSample_WritesLimits(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	reset := when.Add(3 * time.Hour)
	fable := "Fable"
	u := anthro.Usage{
		Limits: []anthro.Limit{
			{Kind: "session", Group: "session", Percent: 8, Severity: "normal", ResetsAt: &reset},
			{Kind: "weekly_scoped", Group: "weekly", Percent: 35, Severity: "normal", IsActive: true,
				Scope: &anthro.LimitScope{Model: &anthro.ScopeModel{DisplayName: &fable}}},
		},
	}
	if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_limits WHERE ts=?`, when.Unix()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("usage_limits rows = %d, want 2", n)
	}

	var limGroup, severity, scopeModel, scopeSurface string
	var percent float64
	var resetsAt sql.NullString
	var isActive int
	err = c.DB().QueryRowContext(t.Context(),
		`SELECT lim_group, percent, severity, resets_at, scope_model, scope_surface, is_active
		 FROM usage_limits WHERE ts=? AND kind='weekly_scoped'`, when.Unix()).
		Scan(&limGroup, &percent, &severity, &resetsAt, &scopeModel, &scopeSurface, &isActive)
	if err != nil {
		t.Fatalf("query weekly_scoped row: %v", err)
	}
	if limGroup != "weekly" || percent != 35 || severity != "normal" {
		t.Errorf("weekly_scoped row: group=%q percent=%v severity=%q", limGroup, percent, severity)
	}
	if resetsAt.Valid {
		t.Errorf("weekly_scoped resets_at should be NULL, got %q", resetsAt.String)
	}
	if scopeModel != "Fable" {
		t.Errorf("scope_model = %q, want Fable", scopeModel)
	}
	if scopeSurface != "" {
		t.Errorf("scope_surface = %q, want empty", scopeSurface)
	}
	if isActive != 1 {
		t.Errorf("is_active = %d, want 1", isActive)
	}

	var sessResets sql.NullString
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT resets_at FROM usage_limits WHERE ts=? AND kind='session'`, when.Unix()).
		Scan(&sessResets); err != nil {
		t.Fatal(err)
	}
	if !sessResets.Valid || sessResets.String != reset.UTC().Format(time.RFC3339Nano) {
		t.Errorf("session resets_at = %+v, want %s", sessResets, reset.UTC().Format(time.RFC3339Nano))
	}
}

func TestRecordUsageSample_NoLimitsWritesNoChildRows(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	if err := c.RecordUsageSample(t.Context(), anthro.Usage{}, when); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}
	var parents, children int
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_samples`).Scan(&parents); err != nil {
		t.Fatal(err)
	}
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_limits`).Scan(&children); err != nil {
		t.Fatal(err)
	}
	if parents != 1 || children != 0 {
		t.Fatalf("parents=%d children=%d, want 1 and 0", parents, children)
	}
}

func TestRecordUsageSample_DuplicateTsKeepsFirstLimits(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	when := time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)
	first := anthro.Usage{Limits: []anthro.Limit{
		{Kind: "weekly_all", Group: "weekly", Percent: 10},
	}}
	second := anthro.Usage{Limits: []anthro.Limit{
		{Kind: "weekly_all", Group: "weekly", Percent: 99},
		{Kind: "session", Group: "session", Percent: 50},
	}}
	if err := c.RecordUsageSample(t.Context(), first, when); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsageSample(t.Context(), second, when); err != nil {
		t.Fatal(err)
	}

	var n int
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_limits WHERE ts=?`, when.Unix()).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("usage_limits rows = %d, want 1 (second sample's children must not land)", n)
	}
	var pct float64
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT percent FROM usage_limits WHERE ts=? AND kind='weekly_all'`, when.Unix()).Scan(&pct); err != nil {
		t.Fatal(err)
	}
	if pct != 10 {
		t.Fatalf("percent = %v, want 10 (first sample wins)", pct)
	}
}

func TestPruneUsageSamples_CascadesLimits(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	oldWhen := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	newWhen := time.Date(2026, 7, 20, 0, 0, 0, 0, time.UTC)
	u := anthro.Usage{Limits: []anthro.Limit{{Kind: "weekly_all", Group: "weekly", Percent: 22}}}
	if err := c.RecordUsageSample(t.Context(), u, oldWhen); err != nil {
		t.Fatal(err)
	}
	if err := c.RecordUsageSample(t.Context(), u, newWhen); err != nil {
		t.Fatal(err)
	}

	deleted, err := c.PruneUsageSamples(t.Context(), time.Date(2026, 7, 10, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PruneUsageSamples: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (parent rows only)", deleted)
	}

	var oldChildren, newChildren int
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_limits WHERE ts=?`, oldWhen.Unix()).Scan(&oldChildren); err != nil {
		t.Fatal(err)
	}
	if err := c.DB().QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM usage_limits WHERE ts=?`, newWhen.Unix()).Scan(&newChildren); err != nil {
		t.Fatal(err)
	}
	if oldChildren != 0 {
		t.Errorf("old usage_limits rows = %d, want 0 (pruned)", oldChildren)
	}
	if newChildren != 1 {
		t.Errorf("new usage_limits rows = %d, want 1 (kept)", newChildren)
	}
}
