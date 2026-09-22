package tui

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	// Guarantees IANA tzdata is available regardless of the host OS's own
	// zoneinfo install (notably slim Linux CI images) — this file's
	// property tests exercise zones (America/Sao_Paulo, Australia/Lord_Howe)
	// that a bare-bones image may not ship.
	_ "time/tzdata"
)

// sinkInt prevents the compiler from eliding bucketCountInRange /
// bucketCountInRangeWalk calls in BenchmarkBucketCountInRange_24h when
// their return value is otherwise unused.
var sinkInt int

// mustLoadLocation loads an IANA zone by name, failing the test (not
// skipping) on error: the blank time/tzdata import above means a load
// failure is a real bug, not an environment gap, so it should not
// silently drop coverage.
func mustLoadLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load tz %q: %v", name, err)
	}
	return loc
}

// bucketCountInRangeWalk is the pre-#542 reference implementation of
// bucketCountInRange: it counts 24h buckets by walking one day at a time
// via AddDate(0,0,1) rather than computing the count arithmetically (see
// chart.go's dayBucketCount for the O(1) replacement). Kept here as an
// oracle for the property tests below.
//
// This walk is NOT a perfect ground truth: bucketCountInRangeFresh below
// documents why, and the tests treat divergences between the two as
// expected in specific, reported circumstances rather than as failures.
func bucketCountInRangeWalk(from, to time.Time, dur time.Duration) int {
	if !to.After(from) {
		return 0
	}
	if dur == 24*time.Hour {
		n := 0
		for t := from; t.Before(to); t = t.AddDate(0, 0, 1) {
			n++
		}
		return n
	}
	return int(to.Sub(from) / dur)
}

// bucketCountInRangeFresh is the ground-truth reference for the 24h case:
// the count of k >= 0 with from.AddDate(0, 0, k).Before(to), each k
// computed FRESH from the original `from` — never compounding through a
// previous AddDate result. This is exactly the definition dayBucketCount's
// doc comment (chart.go) promises bucketCountInRange computes; the property
// tests below hold bucketCountInRange to this definition with ZERO
// exceptions, across zones, midnight and non-midnight `from`, DST gaps
// included. O(n) in the day span — test-only, never used on a hot path.
func bucketCountInRangeFresh(from, to time.Time) int {
	n := 0
	for from.AddDate(0, 0, n).Before(to) {
		n++
	}
	return n
}

// dayBucketWallTime names a fixed H:M:S.ns to apply to a swept `from` date.
type dayBucketWallTime struct {
	name        string
	h, m, s, ns int
}

var (
	wtMidnight    = dayBucketWallTime{"midnight", 0, 0, 0, 0}
	wtMorning     = dayBucketWallTime{"morning", 9, 15, 30, 0}
	wtGap0200     = dayBucketWallTime{"gap_0200", 2, 15, 0, 0}     // Warsaw/NY/Lord Howe spring-forward hour
	wtGapMidnight = dayBucketWallTime{"gap_midnight", 0, 30, 0, 0} // Sao Paulo-style midnight transition
)

// dayBucketZoneWallTimes maps each zone under test to the wall times worth
// sweeping for it: every zone gets "midnight" (the common real-caller case,
// via cache.DayStartLocal) and "morning" (an arbitrary time nowhere near any
// zone's gap, as a negative control), plus whichever gap-hour wall time
// actually lands inside THAT zone's DST gap.
var dayBucketZoneWallTimes = map[string][]dayBucketWallTime{
	"UTC":                 {wtMidnight},
	"Europe/Warsaw":       {wtMidnight, wtMorning, wtGap0200},
	"America/New_York":    {wtMidnight, wtMorning, wtGap0200},
	"Australia/Lord_Howe": {wtMidnight, wtMorning, wtGap0200},
	"America/Sao_Paulo":   {wtMidnight, wtMorning, wtGapMidnight},
}

// dayBucketZoneOrder is dayBucketZoneWallTimes' key set in a fixed order,
// for deterministic subtest naming/iteration.
var dayBucketZoneOrder = []string{
	"UTC", "Europe/Warsaw", "America/New_York", "Australia/Lord_Howe", "America/Sao_Paulo",
}

// TestDayBucketCount_PropertySweep is the primary correctness property test
// for bucketCountInRange's 24h case (issue #542). For every (zone, wall
// time) combination it sweeps `from` across every calendar day in
// [2017-01-01, 2019-01-01) — a dense, two-year window chosen to contain a
// real DST transition for every zone under test, including Sao Paulo's
// (Brazil last observed DST in the 2018/2019 season) — crossed against
// several span lengths and several `to` offsets (day-aligned, and a mix of
// fine- and coarse-grained non-aligned offsets — see the toOffsets comment
// below for why the fine-grained ones are load-bearing).
//
// Two things are checked per case:
//
//  1. bucketCountInRange MUST equal bucketCountInRangeFresh — the ground
//     truth — with ZERO exceptions. This is the actual correctness bar:
//     bucketCountInRange's whole job is to compute this definition in O(1)
//     instead of by walking.
//  2. bucketCountInRangeFresh vs bucketCountInRangeWalk (the OLD, pre-#542
//     implementation) is tallied, not asserted per-case: the walk is a
//     compounding AddDate chain, and once its running wall clock crosses a
//     DST gap it keeps the shifted time for every subsequent day (see
//     dayBucketCount's doc comment in chart.go) — so the walk itself can
//     disagree with the correct definition. After the sweep, the tallies
//     are checked against what's actually expected:
//     - "morning" (nowhere near any tested zone's gap) must never diverge,
//     in any zone — this is the sanity check that walk/fresh agree absent
//     a gap.
//     - "midnight" in Europe/Warsaw, America/New_York and Australia/Lord_Howe
//     must never diverge either: local midnight always exists in those
//     zones (their gap sits at ~02:00), so a midnight-aligned walk never
//     enters it.
//     - the zone's own gap-hour wall time ("gap_0200" or "gap_midnight")
//     MUST diverge at least once — proving the sweep actually exercised
//     the documented subtlety rather than silently missing it.
func TestDayBucketCount_PropertySweep(t *testing.T) {
	spans := []int{0, 1, 2, 7, 30}
	// Fine-grained short offsets (15/30/45m) are deliberate, not arbitrary:
	// a compounding walk that crosses a DST gap drifts by roughly the gap's
	// own size (up to ~1h here), so only a `to` landing inside that narrow
	// window exposes the divergence — 6h17m and 23h59m alone would miss it
	// entirely and the "positive control" assertions below would never
	// trigger.
	toOffsets := []time.Duration{
		0, 15 * time.Minute, 30 * time.Minute, 45 * time.Minute,
		6*time.Hour + 17*time.Minute, 23*time.Hour + 59*time.Minute,
	}
	start := time.Date(2017, time.January, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2019, time.January, 1, 0, 0, 0, 0, time.UTC)

	type tally struct{ total, walkDiverged int }
	results := make(map[string]map[string]*tally) // zone -> walltime -> tally

	for _, zname := range dayBucketZoneOrder {
		loc := mustLoadLocation(t, zname)
		results[zname] = make(map[string]*tally)
		for _, wt := range dayBucketZoneWallTimes[zname] {
			tl := &tally{}
			results[zname][wt.name] = tl
			t.Run(zname+"/"+wt.name, func(t *testing.T) {
				for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
					y, m, day := d.Date()
					from := time.Date(y, m, day, wt.h, wt.m, wt.s, wt.ns, loc)
					for _, days := range spans {
						for _, off := range toOffsets {
							to := from.AddDate(0, 0, days).Add(off)
							if !to.After(from) {
								continue
							}
							fresh := bucketCountInRangeFresh(from, to)
							if got := bucketCountInRange(from, to, 24*time.Hour); got != fresh {
								t.Fatalf("bucketCountInRange(%v, %v, 24h) = %d, want (fresh) %d",
									from, to, got, fresh)
							}
							tl.total++
							if bucketCountInRangeWalk(from, to, 24*time.Hour) != fresh {
								tl.walkDiverged++
							}
						}
					}
				}
			})
		}
	}

	// Negative controls: away from any gap, walk and fresh must always agree.
	for _, zname := range dayBucketZoneOrder {
		if tl := results[zname]["morning"]; tl != nil && tl.walkDiverged != 0 {
			t.Errorf("%s/morning: expected 0 walk/fresh divergences (no gap at this wall time), got %d/%d",
				zname, tl.walkDiverged, tl.total)
		}
	}
	for _, zname := range []string{"Europe/Warsaw", "America/New_York", "Australia/Lord_Howe"} {
		if tl := results[zname]["midnight"]; tl != nil && tl.walkDiverged != 0 {
			t.Errorf("%s/midnight: expected 0 walk/fresh divergences (this zone's gap sits at ~02:00, never at midnight), got %d/%d",
				zname, tl.walkDiverged, tl.total)
		}
	}

	// Positive controls: the documented subtlety must actually be exercised.
	for zname, wtName := range map[string]string{
		"Europe/Warsaw":       "gap_0200",
		"America/New_York":    "gap_0200",
		"Australia/Lord_Howe": "gap_0200",
		"America/Sao_Paulo":   "gap_midnight",
	} {
		tl := results[zname][wtName]
		if tl == nil {
			t.Fatalf("missing tally for %s/%s", zname, wtName)
		}
		if tl.walkDiverged == 0 {
			t.Errorf("%s/%s: expected at least one walk/fresh divergence (this wall time sits in the zone's DST gap) — "+
				"the swept date range may no longer cross a real transition", zname, wtName)
		} else {
			t.Logf("%s/%s: %d/%d cases diverge between bucketCountInRangeWalk and the fresh definition — "+
				"documented, expected (see dayBucketCount's doc comment); bucketCountInRange matches the fresh "+
				"definition in every one of them", zname, wtName, tl.walkDiverged, tl.total)
		}
	}

	// Also document Sao Paulo at plain midnight: its DST gap IS at midnight,
	// so unlike the three 02:00-gap zones above, its midnight walk CAN
	// diverge from fresh too.
	if tl := results["America/Sao_Paulo"]["midnight"]; tl != nil {
		t.Logf("America/Sao_Paulo/midnight: %d/%d cases diverge between bucketCountInRangeWalk and the fresh "+
			"definition (expected — Sao Paulo's DST gap sits at local midnight); bucketCountInRange matches the "+
			"fresh definition in every one of them", tl.walkDiverged, tl.total)
	}
}

// TestDayBucketCount_RandomizedSweep fuzzes bucketCountInRange against the
// fresh ground-truth definition (see bucketCountInRangeFresh) across a much
// wider, randomized date space than the dense grid above — multi-decade
// spans, arbitrary (non-midnight, including in-gap) wall times, and
// randomized `to` offsets — using a fixed seed for reproducibility. Unlike
// the property sweep above, this makes NO exception: bucketCountInRange
// must equal the fresh definition in every case, since that IS its
// contract (chart.go's dayBucketCount doc comment).
func TestDayBucketCount_RandomizedSweep(t *testing.T) {
	const seed = 542
	const iterations = 600
	const maxSpanDays = 1500 // ~4 years

	for _, zname := range dayBucketZoneOrder {
		loc := mustLoadLocation(t, zname)
		t.Run(zname, func(t *testing.T) {
			rng := rand.New(rand.NewSource(seed))
			for i := range iterations {
				year := 1990 + rng.Intn(50) // 1990..2039
				month := time.Month(1 + rng.Intn(12))
				day := 1 + rng.Intn(28)
				h, m, s := rng.Intn(24), rng.Intn(60), rng.Intn(60)
				from := time.Date(year, month, day, h, m, s, 0, loc)

				days := rng.Intn(maxSpanDays)
				offset := time.Duration(rng.Int63n(int64(24 * time.Hour)))
				to := from.AddDate(0, 0, days).Add(offset)
				if !to.After(from) {
					continue
				}

				fresh := bucketCountInRangeFresh(from, to)
				if got := bucketCountInRange(from, to, 24*time.Hour); got != fresh {
					t.Fatalf("seed=%d i=%d zone=%s: bucketCountInRange(%v, %v, 24h) = %d, want (fresh) %d",
						seed, i, zname, from, to, got, fresh)
				}
			}
		})
	}
}

// TestDayBucketCount_EdgeCases covers the trivial boundary shapes
// explicitly: from==to, from>to, and a `to` that is not day-aligned (a
// stand-in for callers passing time.Now(), as model.go's refreshChart
// does) — supplementing the existing table-driven TestBucketCountInRange
// and the property sweeps above.
func TestDayBucketCount_EdgeCases(t *testing.T) {
	loc := mustLoadLocation(t, "Europe/Warsaw")

	from := time.Date(2026, 5, 15, 0, 0, 0, 0, loc)
	if got := bucketCountInRange(from, from, 24*time.Hour); got != 0 {
		t.Errorf("from==to: got %d, want 0", got)
	}
	if got := bucketCountInRange(from.AddDate(0, 0, 1), from, 24*time.Hour); got != 0 {
		t.Errorf("from>to: got %d, want 0", got)
	}

	// to not day-aligned: 7 full days plus 14h30m — from.AddDate(0,0,7) is
	// still Before `to` (same day, earlier wall clock), so k=0..7 all
	// qualify (8 buckets); k=8 lands on day 8 at 00:00, which is NOT before
	// `to`'s day-7 14:30.
	to := from.AddDate(0, 0, 7).Add(14*time.Hour + 30*time.Minute)
	if got, want := bucketCountInRange(from, to, 24*time.Hour), 8; got != want {
		t.Errorf("non-day-aligned to: bucketCountInRange(%v, %v, 24h) = %d, want %d", from, to, got, want)
	}
}

// TestPaddedFrom_BucketCountPairingInvariant asserts the invariant
// bucketCountInRange(paddedFrom(to, zoom24h, n), to, 24h) == n across a
// range of n and zones — the property refreshChart relies on to guarantee
// the chart window is at least as wide as the viewport (#300).
//
// The invariant is guaranteed whenever the round trip
// to.AddDate(0,0,-n).AddDate(0,0,n) == to holds: paddedFrom computes
// `from` as exactly that backward AddDate, so if the forward AddDate
// returns to the same instant, `from` and `to` are related by precisely n
// fresh AddDate steps — exactly what bucketCountInRange (and
// bucketCountInRangeFresh) count. When the round trip does NOT hold — `to`
// or an intermediate date landed in a DST gap along the way — neither
// implementation can be expected to satisfy the invariant exactly, so
// those (to, n) pairs are skipped and tallied rather than asserted.
//
// For a plain midnight `to` (cache.DayStartLocal's output — the only value
// real callers pass, via refreshChart) in Europe/Warsaw, America/New_York
// and Australia/Lord_Howe, the round trip always holds (midnight is never
// in those zones' ~02:00 gap) — so those three zones are held to zero
// skips for the "midnight_now" and "midnight_dst_era" cases.
func TestPaddedFrom_BucketCountPairingInvariant(t *testing.T) {
	zoomIdx, ok := zoomIdxForLabel("24h")
	if !ok {
		t.Fatal("24h zoom not found in ZoomLevels")
	}
	zoom24h := ZoomLevels[zoomIdx]

	type toCase struct {
		name string
		to   func(loc *time.Location) time.Time
	}
	cases := []toCase{
		{"midnight_now", func(loc *time.Location) time.Time {
			return time.Date(2026, 5, 15, 0, 0, 0, 0, loc)
		}},
		// An era when every zone under test (Sao Paulo included) still
		// observed regular DST transitions, so walking back n<=400 days
		// from this `to` crosses at least one real transition everywhere.
		{"midnight_dst_era", func(loc *time.Location) time.Time {
			return time.Date(2018, 5, 15, 0, 0, 0, 0, loc)
		}},
		{"arbitrary_wall_time", func(loc *time.Location) time.Time {
			return time.Date(2026, 5, 15, 14, 37, 0, 0, loc)
		}},
	}

	const maxN = 400

	for _, zname := range dayBucketZoneOrder {
		loc := mustLoadLocation(t, zname)
		t.Run(zname, func(t *testing.T) {
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					to := tc.to(loc)
					skipped := 0
					for n := 0; n <= maxN; n++ {
						from := paddedFrom(to, zoom24h, n)
						got := bucketCountInRange(from, to, 24*time.Hour)
						if n == 0 {
							if got != 0 {
								t.Errorf("n=0: bucketCountInRange = %d, want 0", got)
							}
							continue
						}
						if !to.AddDate(0, 0, -n).AddDate(0, 0, n).Equal(to) {
							skipped++
							continue
						}
						if got != n {
							t.Errorf("n=%d: bucketCountInRange(paddedFrom(to,24h,%d), to, 24h) = %d, want %d",
								n, n, got, n)
						}
					}
					if skipped > 0 {
						t.Logf("%d/%d pairs skipped: to.AddDate(0,0,-n).AddDate(0,0,n) != to "+
							"(DST round-trip information loss, not a bug)", skipped, maxN)
					}
					switch zname {
					case "Europe/Warsaw", "America/New_York", "Australia/Lord_Howe":
						if skipped != 0 {
							t.Errorf("%s/%s: expected 0 round-trip skips (midnight is always valid in this "+
								"zone), got %d", zname, tc.name, skipped)
						}
					case "America/Sao_Paulo":
						if tc.name == "midnight_dst_era" && skipped == 0 {
							t.Errorf("%s/%s: expected at least one round-trip skip (Sao Paulo's DST gap sits "+
								"at local midnight, and this `to` spans a real transition within n<=%d) — the "+
								"test may no longer be exercising it", zname, tc.name, maxN)
						}
					}
				})
			}
		})
	}
}

// BenchmarkBucketCountInRange_24h measures the O(1) implementation against
// bucketCountInRangeWalk (the pre-#542 day-by-day walk this replaces) at
// the day counts cited in issue #542 (1,666 and 4,166 days — the 24h-zoom
// widths that were measured at 53-81µs and 133-244µs respectively on the
// walk). Uses UTC (no DST) so the walk's per-lap cost isn't itself
// perturbed by zone lookups, matching how the original numbers were framed
// purely in terms of day count.
func BenchmarkBucketCountInRange_24h(b *testing.B) {
	now := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	for _, days := range []int{1_666, 4_166} {
		from := now.AddDate(0, 0, -days)
		b.Run(fmt.Sprintf("days=%d", days), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkInt = bucketCountInRange(from, now, 24*time.Hour)
			}
		})
		b.Run(fmt.Sprintf("days=%d_walk_oracle_before", days), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkInt = bucketCountInRangeWalk(from, now, 24*time.Hour)
			}
		})
	}
}
