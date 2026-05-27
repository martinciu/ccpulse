package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func openSeededCache(t *testing.T, days int) (*cache.Cache, seedResult) {
	t.Helper()
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	res, err := runSeed(t.Context(), seedOpts{profile: "light", cacheDir: cacheDir, seed: 1, days: days})
	if err != nil {
		t.Fatalf("runSeed: %v", err)
	}
	c, err := cache.Open(t.Context(), filepath.Join(cacheDir, "state.db"))
	if err != nil {
		t.Fatalf("reopen cache: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c, res
}

func TestSeedYear_UsageSamples_Populated(t *testing.T) {
	c, res := openSeededCache(t, 30)
	if res.samplesInserted == 0 {
		t.Fatal("samplesInserted = 0, want > 0")
	}
	if res.samplesTotal != res.samplesInserted {
		t.Errorf("first run: samplesTotal=%d, samplesInserted=%d (want equal)", res.samplesTotal, res.samplesInserted)
	}
	var n int64
	if err := c.DB().QueryRowContext(t.Context(), `SELECT COUNT(*) FROM usage_samples`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != res.samplesTotal {
		t.Errorf("DB count=%d, samplesTotal=%d", n, res.samplesTotal)
	}
}

func TestSeedYear_UsageSamples_SpanAndColumns(t *testing.T) {
	c, _ := openSeededCache(t, 30)

	// usage_samples.ts is epoch seconds; messages.ts is RFC3339 text. Parse
	// the message bounds in Go rather than via SQLite strftime — strftime's
	// ISO-8601 'Z'-suffix parsing depends on the embedded SQLite version.
	const tsFormat = "2006-01-02T15:04:05.000Z07:00"
	var sMin, sMax sql.NullInt64
	if err := c.DB().QueryRowContext(t.Context(), `SELECT MIN(ts), MAX(ts) FROM usage_samples`).Scan(&sMin, &sMax); err != nil {
		t.Fatalf("usage range: %v", err)
	}
	var mMinStr, mMaxStr sql.NullString
	if err := c.DB().QueryRowContext(t.Context(), `SELECT MIN(ts), MAX(ts) FROM messages`).Scan(&mMinStr, &mMaxStr); err != nil {
		t.Fatalf("messages range: %v", err)
	}
	if !sMin.Valid || !sMax.Valid || !mMinStr.Valid || !mMaxStr.Valid {
		t.Fatal("got NULL range bounds")
	}
	mMin, err := time.Parse(tsFormat, mMinStr.String)
	if err != nil {
		t.Fatalf("parse messages MIN %q: %v", mMinStr.String, err)
	}
	mMax, err := time.Parse(tsFormat, mMaxStr.String)
	if err != nil {
		t.Fatalf("parse messages MAX %q: %v", mMaxStr.String, err)
	}
	// First sample at the earliest message; last sample no later than the last.
	if sMin.Int64 != mMin.Unix() {
		t.Errorf("usage MIN ts=%d, messages MIN ts=%d (want equal)", sMin.Int64, mMin.Unix())
	}
	if sMax.Int64 > mMax.Unix() {
		t.Errorf("usage MAX ts=%d after messages MAX ts=%d", sMax.Int64, mMax.Unix())
	}

	// 5h/7d columns populated; an unused bucket column is NULL; resets parse and are > ts.
	var (
		ts          int64
		fivePct     sql.NullFloat64
		sevenPct    sql.NullFloat64
		sonnetPct   sql.NullFloat64
		fiveResets  sql.NullString
		sevenResets sql.NullString
	)
	err = c.DB().QueryRowContext(t.Context(), `
SELECT ts, five_hour_pct, seven_day_pct, seven_day_sonnet_pct, five_hour_resets_at, seven_day_resets_at
FROM usage_samples ORDER BY ts LIMIT 1`).Scan(&ts, &fivePct, &sevenPct, &sonnetPct, &fiveResets, &sevenResets)
	if err != nil {
		t.Fatalf("scan row: %v", err)
	}
	if !fivePct.Valid || !sevenPct.Valid {
		t.Errorf("five/seven pct must be non-null: %+v / %+v", fivePct, sevenPct)
	}
	if sonnetPct.Valid {
		t.Errorf("seven_day_sonnet_pct should be NULL (no renderer), got %v", sonnetPct.Float64)
	}
	for name, rs := range map[string]sql.NullString{"five": fiveResets, "seven": sevenResets} {
		if !rs.Valid {
			t.Errorf("%s resets_at is NULL", name)
			continue
		}
		parsed, perr := time.Parse(time.RFC3339Nano, rs.String)
		if perr != nil {
			t.Errorf("%s resets_at %q does not parse: %v", name, rs.String, perr)
			continue
		}
		if parsed.Unix() <= ts {
			t.Errorf("%s resets_at %d not after sample ts %d", name, parsed.Unix(), ts)
		}
	}
}

func TestSeedYear_UsageSamples_Idempotent(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse-dev")
	opts := seedOpts{profile: "light", cacheDir: cacheDir, seed: 1, days: 30}

	res1, err := runSeed(t.Context(), opts)
	if err != nil {
		t.Fatalf("first runSeed: %v", err)
	}
	if res1.samplesInserted == 0 {
		t.Fatal("first run inserted 0 samples; nothing to test idempotency against")
	}

	res2, err := runSeed(t.Context(), opts)
	if err != nil {
		t.Fatalf("second runSeed: %v", err)
	}
	if res2.samplesInserted != 0 {
		t.Errorf("idempotent: second run samplesInserted=%d, want 0", res2.samplesInserted)
	}
	if res2.msgsInserted != 0 {
		t.Errorf("idempotent: second run msgsInserted=%d, want 0", res2.msgsInserted)
	}
	if res1.samplesTotal != res2.samplesTotal {
		t.Errorf("idempotent: samplesTotal changed %d -> %d", res1.samplesTotal, res2.samplesTotal)
	}
}
