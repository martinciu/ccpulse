package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// zoomByLabel is a lookup so these cases read in terms the UI uses ("15m")
// rather than a positional index into ZoomLevels.
func zoomByLabel(t *testing.T, label string) ZoomLevel {
	t.Helper()
	for _, z := range ZoomLevels {
		if z.Label == label {
			return z
		}
	}
	t.Fatalf("no ZoomLevel labelled %q", label)
	return ZoomLevel{}
}

// TestClampChartFrom is the regression guard for #527: a single cache row with
// a far-past timestamp used to size the chart canvas (and the dense bucket
// slices behind it) into the tens of millions, which allocated >10GB and hung
// the TUI before its first frame.
func TestClampChartFrom(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	// The zero time.Time, exactly as a poisoned cache row hands it back.
	yearOne := time.Time{}

	tests := []struct {
		name      string
		zoomLabel string
		from      time.Time
		want      time.Time
	}{
		{
			name:      "recent range is left alone",
			zoomLabel: "1h",
			from:      to.Add(-72 * time.Hour),
			want:      to.Add(-72 * time.Hour),
		},
		{
			// stride 1 at 1h, so the column budget is the bucket budget.
			name:      "exactly at the ceiling is left alone",
			zoomLabel: "1h",
			from:      to.Add(-maxChartColumns * time.Hour),
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			name:      "one bucket past the ceiling is clamped back to it",
			zoomLabel: "1h",
			from:      to.Add(-(maxChartColumns + 1) * time.Hour),
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			name:      "year one at 15m",
			zoomLabel: "15m",
			from:      yearOne,
			want:      to.Add(-maxChartColumns * 15 * time.Minute),
		},
		{
			name:      "year one at 1h",
			zoomLabel: "1h",
			from:      yearOne,
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			// 24h draws BarWidth 10 + BarGap 2, so its budget is
			// maxChartColumns/12 buckets — an order of magnitude fewer than the
			// 1-column-per-bucket zooms. A bucket-denominated ceiling would
			// have let this zoom build a ~240k-column canvas (#527).
			name:      "year one at 24h is budgeted by stride, not bucket count",
			zoomLabel: "24h",
			from:      yearOne,
			want:      to.Add(-time.Duration(maxChartColumns/12) * 24 * time.Hour),
		},
		{
			name:      "reversed range is left alone",
			zoomLabel: "1h",
			from:      to.Add(time.Hour),
			want:      to.Add(time.Hour),
		},
		{
			name:      "empty range is left alone",
			zoomLabel: "1h",
			from:      to,
			want:      to,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := clampChartFrom(tt.from, to, zoomByLabel(t, tt.zoomLabel))
			if !got.Equal(tt.want) {
				t.Errorf("clampChartFrom(%v, %v, %s) = %v, want %v",
					tt.from, to, tt.zoomLabel, got, tt.want)
			}
		})
	}
}

// TestClampChartFrom_DegenerateZoom pins the defensive branch: a ZoomLevel with
// a non-positive Duration (a typo in the ZoomLevels literal, a future tuning
// slip) must return `from` rather than divide by zero.
func TestClampChartFrom_DegenerateZoom(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	from := time.Time{}
	if got := clampChartFrom(from, to, ZoomLevel{Label: "bad", Duration: 0}); !got.Equal(from) {
		t.Errorf("clampChartFrom with zero Duration = %v, want %v unchanged", got, from)
	}
}

// TestClampChartFrom_BoundsCanvasWidth asserts the property the ceiling exists
// for, rather than a specific instant: whatever the cache claims, the clamped
// range never lays out a canvas wider than maxChartColumns — at ANY zoom. This
// is the assertion that would have caught #527, and it is deliberately stated
// in CanvasWidth terms: an earlier bucket-denominated version of this ceiling
// passed at 1h while still letting 24h build a ~240k-column canvas.
func TestClampChartFrom_BoundsCanvasWidth(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for _, z := range ZoomLevels {
		t.Run(z.Label, func(t *testing.T) {
			t.Parallel()

			// Precondition: the unclamped year-1 canvas must actually exceed the
			// ceiling, or this case proves nothing. Computed arithmetically
			// rather than via bucketCountInRange, which walks 24h day by day —
			// over a 2025-year span that is ~739k laps, exactly the work the
			// clamp exists to avoid. Skipping the check for 24h instead would
			// let this subtest decay into a vacuous pass the moment
			// maxChartColumns is raised (which #528 anticipates).
			if unclamped := z.CanvasWidth(int(to.Sub(time.Time{}) / z.Duration)); unclamped <= maxChartColumns {
				t.Fatalf("precondition: unclamped year-1 canvas at %s is only %d columns; "+
					"this test no longer exercises the ceiling", z.Label, unclamped)
			}

			from := clampChartFrom(time.Time{}, to, z)
			if w := z.CanvasWidth(bucketCountInRange(from, to, z.Duration)); w > maxChartColumns {
				t.Errorf("after clamp, CanvasWidth = %d columns, want <= %d", w, maxChartColumns)
			}
		})
	}
}

// TestClampChartFrom_IsLossy documents WHY refreshChart must not assign the
// clamped value back over `earliest`.
//
// Clamping is deliberately lossy: every earliest beyond the horizon collapses
// onto the same instant. chartCache keys its memoized prefix on `earliest`
// specifically to notice backfill widening history leftward (see slotKey), so
// feeding it a clamped value would make the key stop changing exactly when
// older data arrives — and slot.resolve would then stitch a fresh tail onto a
// stale prefix, silently dropping in-window backfilled rows.
//
// This test pins the collapse. The guarantee it protects is asserted end-to-end
// in TestRefreshChart_BackfillStillInvalidatesChartCache.
func TestClampChartFrom_IsLossy(t *testing.T) {
	t.Parallel()

	zoom := zoomByLabel(t, "1h")
	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	// Two distinct earliest values, both past the 1h horizon (~2.3 years).
	e1 := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	e2 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	if e1.Equal(e2) {
		t.Fatal("precondition: the two earliest values must differ")
	}

	c1 := clampChartFrom(e1, to, zoom)
	c2 := clampChartFrom(e2, to, zoom)
	if !c1.Equal(c2) {
		t.Fatalf("expected the clamp to collapse both onto one instant, got %v and %v — "+
			"if this ever stops being true, re-read why refreshChart keeps `earliest` raw", c1, c2)
	}
}

// TestRefreshChart_KeepsRawEarliestForChartCache is the real guard for the
// clamp's interaction with chartCache.
//
// chartCache keys its memoized prefix on EarliestMessageTime to notice backfill
// widening history leftward. Clamping is lossy — every earliest beyond the
// horizon collapses onto one instant (TestClampChartFrom_IsLossy) — so if
// refreshChart assigned the clamped value back over `earliest`, the key would
// stop changing exactly when older data arrives, and slot.resolve would stitch
// a fresh tail onto a stale prefix, silently dropping in-window backfilled rows.
//
// Asserting on the stored slot key is what makes this a real guard: it fails if
// refreshChart ever passes the clamped value, which a test driving slot.resolve
// with hand-picked arguments cannot detect.
func TestRefreshChart_KeepsRawEarliestForChartCache(t *testing.T) {
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

	// One row far enough back that the 1h clamp is definitely active, plus
	// ordinary recent history.
	ancient := base.AddDate(-40, 0, 0)
	insertAt(t, c, tab, "ancient", ancient, 1)
	for i := 1; i <= 6; i++ {
		insertAt(t, c, tab, "seed-"+itoa(i), base.Add(-time.Duration(i)*time.Hour), int64(1000+i))
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.zoomIdx = 1 // 1h
	m.w, m.h = 122, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.now = func() time.Time { return base }
	m.refreshChart()

	rawEarliest, ok, err := c.EarliestMessageTime(t.Context())
	if err != nil || !ok {
		t.Fatalf("EarliestMessageTime: ok=%v err=%v", ok, err)
	}

	// Precondition: the clamp must actually be engaged, or this proves nothing.
	clamped := clampChartFrom(rawEarliest, m.lastChartTo, ZoomLevels[m.zoomIdx])
	if clamped.Equal(rawEarliest) {
		t.Fatalf("precondition: clamp not engaged for earliest=%v", rawEarliest)
	}

	got := m.chartCache.tokens.key.earliest
	if !got.Equal(rawEarliest) {
		t.Errorf("chartCache slot key earliest = %v, want the RAW %v.\n"+
			"It looks like refreshChart passed the CLAMPED value (%v): that collapses "+
			"every far-past earliest onto one instant, so backfill can no longer "+
			"invalidate the memoized prefix.", got, rawEarliest, clamped)
	}
}

// TestRefreshChart_FarPastRowBoundsCanvas is the guard that layer 2 is actually
// WIRED IN, not merely present.
//
// Every other test here exercises clampChartFrom as a pure function, so all of
// them survive deleting the CALL in refreshChart — only deleting the function
// breaks them, and that is a compile error nobody needs a test for. This one
// drives the real Model and asserts on the canvas it produced. Without the
// clamp call, lastCanvasW measures in the millions.
//
// Seeded with 1970, not time.Time{}: layer 1 now refuses Year() <= 1 at the
// parser, so a year-1 row can only reach the cache through a direct
// InsertMessages. Epoch-0 is a row layer 1 deliberately admits — it is exactly
// the far-past-but-valid shape layer 2 exists to catch — so this test cannot
// rot into an unreachable-by-design no-op if layer 1 is widened later.
func TestRefreshChart_FarPastRowBoundsCanvas(t *testing.T) {
	t.Parallel()

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	base := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for zi, z := range ZoomLevels {
		t.Run(z.Label, func(t *testing.T) {
			t.Parallel()

			c, err := cache.Open(t.Context(), filepath.Join(t.TempDir(), "s.db"))
			if err != nil {
				t.Fatalf("cache.Open: %v", err)
			}
			defer c.Close()

			insertAt(t, c, tab, "poison", time.Unix(0, 0).UTC(), 1000)
			insertAt(t, c, tab, "good", base.Add(-time.Hour), 2000)

			m := New(Deps{Cache: c})
			m.zoomIdx = zi
			m.w, m.h = 122, 40
			m.viewport.Width, m.viewport.Height = m.chartWidth(), m.chartHeight()
			m.now = func() time.Time { return base }
			m.refreshChart()

			// The bound refreshChart actually satisfies is ceiling+stride, not
			// ceiling: cache.DayStartLocal / BucketAlign walk `from` back to a
			// bucket boundary AFTER the clamp, which can add one more bar.
			if lim := maxChartColumns + z.stride(); m.lastCanvasW > lim {
				t.Errorf("lastCanvasW = %d, want <= %d — is refreshChart still calling "+
					"clampChartFrom? (unclamped this reaches the millions)", m.lastCanvasW, lim)
			}
		})
	}
}
