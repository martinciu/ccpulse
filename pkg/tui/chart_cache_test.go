package tui

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// fakeBucket is the element type for the pure-logic resolve tests. It carries
// only a start time so the splice positions can be asserted without a DB.
type fakeBucket struct{ start time.Time }

// fakeQuery returns one fakeBucket per `dur` interval in [from, to), recording
// the ranges it was called with. Mirrors IOTokenBuckets' contiguous-bucket
// contract (len = (to-from)/dur, oldest-first) so resolve's positional splice
// can be checked against a known-good full query.
type fakeQuery struct {
	calls [][2]time.Time // {from, to} per call
}

func (q *fakeQuery) run(_ context.Context, dur time.Duration, from, to time.Time) ([]fakeBucket, error) {
	q.calls = append(q.calls, [2]time.Time{from, to})
	if !to.After(from) {
		return nil, nil
	}
	n := int(to.Sub(from) / dur)
	out := make([]fakeBucket, n)
	for i := range n {
		out[i] = fakeBucket{start: from.Add(time.Duration(i) * dur)}
	}
	return out, nil
}

func starts(bs []fakeBucket) []time.Time {
	out := make([]time.Time, len(bs))
	for i, b := range bs {
		out[i] = b.start
	}
	return out
}

func TestSlotResolve_MissThenStitch(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	earliest := from
	var s slot[fakeBucket]
	q := &fakeQuery{}

	// First call: cold slot → MISS → full query [from, to0).
	to0 := from.Add(4 * dur)
	got0, err := s.resolve(t.Context(), dur, from, to0, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (miss): %v", err)
	}
	if len(q.calls) != 1 || !q.calls[0][0].Equal(from) || !q.calls[0][1].Equal(to0) {
		t.Fatalf("miss query range = %v, want [%v,%v)", q.calls, from, to0)
	}
	if len(got0) != 4 {
		t.Fatalf("miss len = %d, want 4", len(got0))
	}

	// Second call: same key, no boundary crossed (to unchanged) → STITCH
	// re-queries exactly the trailing bucket [to0-dur, to0).
	got1, err := s.resolve(t.Context(), dur, from, to0, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (stitch same to): %v", err)
	}
	last := q.calls[len(q.calls)-1]
	if !last[0].Equal(to0.Add(-dur)) || !last[1].Equal(to0) {
		t.Fatalf("stitch query range = [%v,%v), want [%v,%v)", last[0], last[1], to0.Add(-dur), to0)
	}
	// Byte-identical to a fresh full query over the same window.
	want := mustFull(t, dur, from, to0)
	if !equalStarts(starts(got1), starts(want)) {
		t.Fatalf("stitched starts = %v, want %v", starts(got1), starts(want))
	}

	// Third call: two boundaries crossed → STITCH re-queries [to0-dur, to2).
	to2 := to0.Add(2 * dur)
	got2, err := s.resolve(t.Context(), dur, from, to2, earliest, q.run)
	if err != nil {
		t.Fatalf("resolve (stitch crossed): %v", err)
	}
	last = q.calls[len(q.calls)-1]
	if !last[0].Equal(to0.Add(-dur)) || !last[1].Equal(to2) {
		t.Fatalf("crossed stitch range = [%v,%v), want [%v,%v)", last[0], last[1], to0.Add(-dur), to2)
	}
	want = mustFull(t, dur, from, to2)
	if !equalStarts(starts(got2), starts(want)) {
		t.Fatalf("crossed stitched starts = %v, want %v", starts(got2), starts(want))
	}
}

func TestSlotResolve_KeyChangeForcesMiss(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var s slot[fakeBucket]
	q := &fakeQuery{}

	if _, err := s.resolve(t.Context(), dur, from, to, from, q.run); err != nil {
		t.Fatal(err)
	}
	// earliest changes (backfill widened history) → full re-query.
	newEarliest := from.Add(-dur)
	if _, err := s.resolve(t.Context(), dur, from, to, newEarliest, q.run); err != nil {
		t.Fatal(err)
	}
	last := q.calls[len(q.calls)-1]
	if !last[0].Equal(from) || !last[1].Equal(to) {
		t.Fatalf("earliest-change query = [%v,%v), want full [%v,%v)", last[0], last[1], from, to)
	}
}

func TestSlotResolve_ErrorDoesNotMutate(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var s slot[fakeBucket]
	good := &fakeQuery{}
	if _, err := s.resolve(t.Context(), dur, from, to, from, good.run); err != nil {
		t.Fatal(err)
	}
	prevLen := len(s.buckets)

	boom := func(context.Context, time.Duration, time.Time, time.Time) ([]fakeBucket, error) {
		return nil, errors.New("db down")
	}
	if _, err := s.resolve(t.Context(), dur, from, to.Add(dur), from, boom); err == nil {
		t.Fatal("resolve should propagate query error")
	}
	if len(s.buckets) != prevLen || !s.to.Equal(to) {
		t.Fatalf("slot mutated after error: len %d→%d, to=%v", prevLen, len(s.buckets), s.to)
	}
}

func TestChartCacheInvalidate(t *testing.T) {
	t.Parallel()
	dur := 15 * time.Minute
	from := time.Date(2026, 5, 23, 0, 0, 0, 0, time.UTC)
	to := from.Add(4 * dur)
	var cc chartCache
	tokenQuery := func(_ context.Context, d time.Duration, f, tt time.Time) ([]cache.TokenBucket, error) {
		return tokenBucketsLike(d, f, tt), nil
	}
	// Warm the tokens slot, invalidate, then assert the next resolve is a MISS
	// (it re-queries instead of stitching). A second resolve with the SAME key
	// after the warm would otherwise stitch (1 trailing-bucket query); the
	// invalidate must force a full query regardless.
	if _, err := cc.tokens.resolve(t.Context(), dur, from, to, from, tokenQuery); err != nil {
		t.Fatal(err)
	}
	cc.invalidate()
	if cc.tokens.key.valid {
		t.Fatal("invalidate did not clear tokens.key.valid")
	}

	calls := 0
	counting := func(ctx context.Context, d time.Duration, f, tt time.Time) ([]cache.TokenBucket, error) {
		calls++
		// A MISS queries the full [from, to); a STITCH would query
		// [to-dur, to). Assert the full range to prove it was a MISS.
		if !f.Equal(from) || !tt.Equal(to) {
			t.Errorf("post-invalidate query range = [%v,%v), want full [%v,%v) (MISS)", f, tt, from, to)
		}
		return tokenBucketsLike(d, f, tt), nil
	}
	if _, err := cc.tokens.resolve(t.Context(), dur, from, to, from, counting); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("after invalidate, query calls = %d, want 1", calls)
	}
}

// tokenBucketsLike builds a contiguous []cache.TokenBucket for the
// invalidate test (real element type, zero tokens).
func tokenBucketsLike(dur time.Duration, from, to time.Time) []cache.TokenBucket {
	if !to.After(from) {
		return nil
	}
	n := int(to.Sub(from) / dur)
	out := make([]cache.TokenBucket, n)
	for i := range n {
		out[i] = cache.TokenBucket{BucketStart: from.Add(time.Duration(i) * dur)}
	}
	return out
}

func mustFull(t *testing.T, dur time.Duration, from, to time.Time) []fakeBucket {
	t.Helper()
	fresh := &fakeQuery{}
	out, err := fresh.run(t.Context(), dur, from, to)
	if err != nil {
		t.Fatalf("mustFull: %v", err)
	}
	return out
}

func equalStarts(a, b []time.Time) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].Equal(b[i]) {
			return false
		}
	}
	return true
}

// freshSeriesValues reads the chart's resolved series directly from a
// FRESH (un-memoized) cache query over [m.lastChartFrom, m.lastChartTo), to
// compare against the memoized refresh result.
func freshSeriesValues(t *testing.T, c *cache.Cache, unit chartUnit, dur time.Duration, from, to time.Time) []float64 {
	t.Helper()
	switch unit {
	case chartUnitCost:
		bs, err := c.CostBuckets(t.Context(), dur, from, to)
		if err != nil {
			t.Fatalf("CostBuckets: %v", err)
		}
		out := make([]float64, len(bs))
		for i, b := range bs {
			out[i] = b.Cost
		}
		return out
	default:
		bs, err := c.IOTokenBuckets(t.Context(), dur, from, to)
		if err != nil {
			t.Fatalf("IOTokenBuckets: %v", err)
		}
		out := make([]float64, len(bs))
		for i, b := range bs {
			out[i] = float64(b.Tokens)
		}
		return out
	}
}

func insertAt(t *testing.T, c *cache.Cache, tab pricing.History, id string, ts time.Time, in int64) {
	t.Helper()
	if err := c.InsertMessages(t.Context(), []parse.Message{{
		SessionID: "s", MessageID: id, ProjectSlug: "p",
		Model: "claude-opus-4-7", Timestamp: ts, InputTokens: in,
	}}, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
}

// TestRefreshChart_MemoizedEqualsFullQuery is the core correctness contract:
// after a sequence of incremental refreshes (the watcher/now-tick path), the
// memoized m.lastValues must equal a fresh full query over the same window.
func TestRefreshChart_MemoizedEqualsFullQuery(t *testing.T) {
	t.Parallel()
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	base := time.Date(2026, 5, 23, 12, 7, 0, 0, time.UTC) // mid-bucket [12:00,12:15)

	cases := []struct {
		name    string
		unit    chartUnit
		zoomIdx int
		// mutate appends rows and/or advances the clock between refreshes;
		// it returns the clock to use for the SECOND refresh.
		mutate func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time
	}{
		{
			name: "append within current open bucket",
			unit: chartUnitTokens, zoomIdx: 0,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				insertAt(t, c, tab, "m-late", base.Add(time.Minute), 9999) // same [12:00,12:15) bucket
				return base.Add(time.Minute)
			},
		},
		{
			name: "append crossing one boundary",
			unit: chartUnitTokens, zoomIdx: 0,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				next := base.Add(15 * time.Minute) // 12:22 → bucket [12:15,12:30)
				insertAt(t, c, tab, "m-next", next, 4242)
				return next
			},
		},
		{
			name: "append crossing several boundaries (idle gap)",
			unit: chartUnitTokens, zoomIdx: 0,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				later := base.Add(70 * time.Minute) // ~4 buckets later
				insertAt(t, c, tab, "m-later", later, 777)
				return later
			},
		},
		{
			name: "re-tail bumps an already-counted turn (max upsert)",
			unit: chartUnitTokens, zoomIdx: 0,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				// Same (session, message_id) as a seeded row, higher tokens →
				// upsert max() bumps the existing bucket. Must be recomputed.
				insertAt(t, c, tab, "seed-0", base.Add(-time.Minute), 50000)
				return base.Add(time.Minute)
			},
		},
		{
			name: "cost unit, append within bucket",
			unit: chartUnitCost, zoomIdx: 0,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				insertAt(t, c, tab, "m-cost", base.Add(time.Minute), 8888)
				return base.Add(time.Minute)
			},
		},
		{
			name: "24h same-day append",
			unit: chartUnitTokens, zoomIdx: 2,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				insertAt(t, c, tab, "m-today", base.Add(time.Hour), 6000) // same local day
				return base.Add(time.Hour)
			},
		},
		{
			name: "24h append crossing local midnight",
			unit: chartUnitTokens, zoomIdx: 2,
			mutate: func(t *testing.T, c *cache.Cache, tab pricing.History, base time.Time) time.Time {
				tomorrow := cache.DayStartLocal(base).AddDate(0, 0, 1).Add(9 * time.Hour)
				insertAt(t, c, tab, "m-tomorrow", tomorrow, 6000)
				return tomorrow
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
			if err != nil {
				t.Fatalf("cache.Open: %v", err)
			}
			defer c.Close()

			// Seed ~12 buckets of history before `base`.
			for i := 1; i <= 12; i++ {
				insertAt(t, c, tab, "seed-"+itoa(i), base.Add(-time.Duration(i)*15*time.Minute), int64(1000+i))
			}
			insertAt(t, c, tab, "seed-0", base.Add(-time.Minute), 2000)

			m := New(Deps{Cache: c})
			m.unitIdx = int(tt.unit)
			m.zoomIdx = tt.zoomIdx
			m.w, m.h = 122, 40
			m.viewport.Width = m.chartWidth()
			m.viewport.Height = m.chartHeight()
			m.now = func() time.Time { return base }

			// Refresh #1 — warms the slot.
			m.refreshChart()

			// Mutate + advance clock, refresh #2 — exercises the stitch path.
			clk := tt.mutate(t, c, tab, base)
			m.now = func() time.Time { return clk }
			m.refreshChart()

			// The memoized series MUST equal a fresh full query over the
			// window refreshChart just used.
			want := freshSeriesValues(t, c, tt.unit, ZoomLevels[tt.zoomIdx].Duration, m.lastChartFrom, m.lastChartTo)
			if len(m.lastValues) != len(want) {
				t.Fatalf("len(lastValues) = %d, want %d", len(m.lastValues), len(want))
			}
			for i := range want {
				if m.lastValues[i] != want[i] {
					t.Fatalf("bucket %d: memoized %v, full-query %v\nstart=%v", i, m.lastValues[i], want[i], m.lastStarts[i])
				}
			}
		})
	}
}

// TestRefreshChart_ZoomChangeFullReload asserts a zoom change is a MISS (the
// cached 15m buckets must not be reused for the 24h view).
func TestRefreshChart_ZoomChangeFullReload(t *testing.T) {
	t.Parallel()
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	base := time.Date(2026, 5, 23, 12, 7, 0, 0, time.UTC)
	c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	defer c.Close()
	for i := 1; i <= 12; i++ {
		insertAt(t, c, tab, "seed-"+itoa(i), base.Add(-time.Duration(i)*15*time.Minute), int64(1000+i))
	}
	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.zoomIdx = 0
	m.w, m.h = 122, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.now = func() time.Time { return base }
	m.refreshChart()

	m.zoomIdx = 2 // 24h
	m.refreshChart()

	want := freshSeriesValues(t, c, chartUnitTokens, 24*time.Hour, m.lastChartFrom, m.lastChartTo)
	if len(m.lastValues) != len(want) {
		t.Fatalf("after zoom change len = %d, want %d (full reload)", len(m.lastValues), len(want))
	}
	for i := range want {
		if m.lastValues[i] != want[i] {
			t.Fatalf("zoom-change bucket %d: %v, want %v", i, m.lastValues[i], want[i])
		}
	}
}

// itoa avoids strconv import churn in the test (small positive ints only).
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
