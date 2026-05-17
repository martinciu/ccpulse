package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestEarliestRemainingSampleAt(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	tests := []struct {
		name  string
		pts5h []cache.UtilizationPoint
		pts7d []cache.UtilizationPoint
		want  time.Time
	}{
		{
			name:  "both empty returns zero time",
			pts5h: nil,
			pts7d: nil,
			want:  time.Time{},
		},
		{
			name:  "only 5h populated returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t1}, {At: t2}},
			pts7d: nil,
			want:  t1,
		},
		{
			name:  "only 7d populated returns 7d[0]",
			pts5h: nil,
			pts7d: []cache.UtilizationPoint{{At: t2}},
			want:  t2,
		},
		{
			name:  "both populated returns earlier of the two",
			pts5h: []cache.UtilizationPoint{{At: t2}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
		{
			name:  "both populated 5h earlier returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t0}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t0,
		},
		{
			name:  "both populated equal timestamps returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t1}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
		{
			name:  "empty non-nil 5h plus populated 7d returns 7d[0]",
			pts5h: []cache.UtilizationPoint{},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := earliestRemainingSampleAt(tt.pts5h, tt.pts7d)
			if !got.Equal(tt.want) {
				t.Errorf("earliestRemainingSampleAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// remainingModeModel returns a Model wired for setX remaining-mode tests:
//   - 15m zoom (stride=1, BarWidth=1, BarGap=0)
//   - unitIdx = chartUnitRemaining
//   - viewport.Width = 30, lastCanvasW = 96 → maxX = 66
//   - canvas spanning [from, from+24h)
//
// With these dimensions, timeToColumn(from + N*15min) = N (one col per
// 15m bucket), so the test cases can reason in bucket-index space
// directly — no manual stride or canvas-width arithmetic.
// viewport.Width=30 (not 80) ensures maxX=66 ≥ 60, so tests that check
// in-range position preservation have room to land above minX=48.
func remainingModeModel(pts5h, pts7d []cache.UtilizationPoint) *Model {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	m := New(Deps{})
	m.zoomIdx = 0 // 15m, stride=1
	m.unitIdx = int(chartUnitRemaining)
	m.w = 120
	m.viewport.Width = 30
	m.lastChartFrom = from
	m.lastChartTo = to
	m.lastCanvasW = 96 // 96 cols of 15m at stride=1 → spans 24h
	m.lastZoomStride = 1
	m.lastPts5h = pts5h
	m.lastPts7d = pts7d
	return &m
}

func TestSetX_RemainingMode_ClampsAtInRangeLeftEdge(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample is 12h into the canvas = column 48.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(0) // try to pan to the far left

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(0) = %d, want %d (earliest in-range bucket)", got, want)
	}
}

func TestSetX_RemainingMode_OutOfRangeSnapsIn(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	m.viewportXOffset = 5 // simulate a stale pre-switch anchor

	// refreshChart-style restore: anchorTime maps to col 5; setX clamps up.
	m.setX(5)

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(5) = %d, want %d (snapped to in-range)", got, want)
	}
}

func TestSetX_RemainingMode_InRangePreservesPosition(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(60) // already past the in-range left edge (48)

	if got, want := m.viewportXOffset, 60; got != want {
		t.Errorf("viewportXOffset after setX(60) = %d, want %d (preserved, no clamp)", got, want)
	}
}

func TestSetX_RemainingMode_EmptySamplesLowerBoundZero(t *testing.T) {
	t.Parallel()
	m := remainingModeModel(nil, nil)

	// Must not panic on empty slices; minX stays 0 because earliest IsZero.
	m.setX(0)

	if got := m.viewportXOffset; got != 0 {
		t.Errorf("viewportXOffset after setX(0) with empty samples = %d, want 0", got)
	}
}

func TestSetX_RemainingMode_EmptySamplesClampsToCanvasMaxX(t *testing.T) {
	t.Parallel()
	m := remainingModeModel(nil, nil)

	m.setX(999) // overshoot upper bound
	// Upper bound: max(0, lastCanvasW - viewport.Width) / stride
	//            = max(0, 96 - 30) / 1 = 66
	if got, want := m.viewportXOffset, 66; got != want {
		t.Errorf("viewportXOffset after setX(999) with empty samples = %d, want %d (canvas-clamp upper bound)", got, want)
	}
}

func TestSetX_BarModeAfterRemaining_BarBoundsRestored(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	// Populate bar-mode state: 200 buckets in lastStarts (much wider
	// than the canvas; bar-mode clamp is len(lastStarts)-visibleBuckets).
	m.lastStarts = make([]time.Time, 200)
	for i := range m.lastStarts {
		m.lastStarts[i] = from.Add(time.Duration(i) * 15 * time.Minute)
	}
	// Switch out of remaining mode to tokens.
	m.unitIdx = int(chartUnitTokens)

	// Pan to a position that would be clamped in remaining mode (col 5
	// < earliest in-range col 48); in bar mode this is in bounds.
	m.setX(5)

	if got, want := m.viewportXOffset, 5; got != want {
		t.Errorf("viewportXOffset after setX(5) in bar mode = %d, want %d (no remaining-mode clamp)", got, want)
	}
}

func TestSetX_RemainingMode_MinXAboveMaxX_CollapsesToMaxX(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample at 20h into the 24h canvas → column 80.
	// remainingModeModel has lastCanvasW=96, viewport.Width=30, stride=1
	// → maxX = (96 - 30)/1 = 66. Since 80 > 66, the explicit
	// `if minX > maxX { minX = maxX }` branch in setX must collapse
	// minX to 66. Without that collapse, min(max(n, 80), 66) would
	// produce 80 — wrong direction past maxX.
	pts5h := []cache.UtilizationPoint{{At: from.Add(20 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(0) // any value; clamp pulls up to the collapsed minX (= maxX).

	if got, want := m.viewportXOffset, 66; got != want {
		t.Errorf("viewportXOffset after setX(0) with minX > maxX = %d, want %d (collapsed to maxX)", got, want)
	}
}

func TestSetX_RemainingMode_ZeroChartFromSkipsLowerBound(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample at 12h (= col 48) would normally clamp setX(0) up
	// to 48. But when m.lastChartFrom is zero-valued, the guard
	// `!m.lastChartFrom.IsZero() && m.lastChartTo.After(m.lastChartFrom)`
	// in setX must short-circuit the lower-bound clamp, leaving minX=0
	// and the existing canvas-clamp path intact.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	m.lastChartFrom = time.Time{}

	m.setX(0)

	if got, want := m.viewportXOffset, 0; got != want {
		t.Errorf("viewportXOffset after setX(0) with zero lastChartFrom = %d, want %d (lower-bound clamp skipped)", got, want)
	}
}

// seedRescaleModel builds a deps-free Model ready for rescaleMsg handler
// tests. No cache needed — the handler only reads in-memory state.
// values populates m.lastValues; lastStarts is filled with synthetic
// minute-spaced timestamps so buildChart's X-label path doesn't trip on
// zero times.
func seedRescaleModel(t *testing.T, values []float64) Model {
	t.Helper()
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.lastValues = append([]float64(nil), values...)
	m.lastStarts = make([]time.Time, len(values))
	now := time.Now().UTC().Truncate(time.Minute)
	for i := range m.lastStarts {
		m.lastStarts[i] = now.Add(time.Duration(-i) * time.Minute)
	}
	m.lastCanvasW = m.chartWidth()
	m.lastZoomStride = ZoomLevels[m.zoomIdx].stride()
	return m
}

// TestScroll_StaleRescaleDropped reproduces the bug class #218 fixed for
// spring ticks, now for rescale ticks. Two scroll bumps stack two
// pending rescaleMsg generations; only the latest must update m.peak.
func TestScroll_StaleRescaleDropped(t *testing.T) {
	values := []float64{100, 1, 1, 1, 1, 2, 2, 2, 2, 2, 2}
	m := seedRescaleModel(t, values)
	m.unitIdx = int(chartUnitCost)
	m.peak = 100 // pre-rescale value (full-range max)

	// Force xOffset over the quiet region. setX clamps to whatever
	// the canvas allows; the absolute value matters less than that
	// xOffset > 0 so peakOf can skip the outlier.
	m.setX(5)
	xOffsetBefore := m.viewportXOffset

	// Bump scrollGen twice — simulates two rapid scroll keypresses.
	m.scrollGen++ // gen=1
	m.scrollGen++ // gen=2
	if m.scrollGen != 2 {
		t.Fatalf("scrollGen = %d after two bumps, want 2", m.scrollGen)
	}

	peakBeforeStale := m.peak

	// Deliver stale tick (gen=1). Handler must drop it; m.peak unchanged.
	updated, staleCmd := m.Update(rescaleMsg{gen: 1})
	m = updated.(Model)
	if staleCmd != nil {
		t.Errorf("stale rescaleMsg returned non-nil Cmd; must not perpetuate a stale rebuild")
	}
	if m.peak != peakBeforeStale {
		t.Errorf("stale rescaleMsg mutated m.peak: before=%v after=%v", peakBeforeStale, m.peak)
	}
	if m.viewportXOffset != xOffsetBefore {
		t.Errorf("stale rescaleMsg mutated xOffset: before=%d after=%d", xOffsetBefore, m.viewportXOffset)
	}

	// Deliver fresh tick (gen=2). Handler must update m.peak to the
	// visible-slice max.
	updated, _ = m.Update(rescaleMsg{gen: 2})
	m = updated.(Model)
	visN := m.visibleBuckets()
	want := peakOf(m.lastValues, m.viewportXOffset, m.viewportXOffset+visN)
	if m.peak != want {
		t.Errorf("fresh rescaleMsg m.peak = %v, want %v (visible slice max)", m.peak, want)
	}
}

// TestRescale_WindowPeakMatchesVisibleSlice asserts the handler reads
// m.viewportXOffset and computes peak over the visible slice — both
// cost and tokens modes participate. Seeds enough buckets that the
// visible window cannot cover the outlier from a non-zero xOffset.
func TestRescale_WindowPeakMatchesVisibleSlice(t *testing.T) {
	// Build a wide slice so chartWidth (120-2 = 118) doesn't swallow
	// the outlier into the visible window. With 15m zoom (stride=1,
	// BarWidth=1, BarGap=0), visibleBuckets ≈ 118; a slice of 300
	// buckets with the outlier at 0 leaves quiet middle/right regions
	// the viewport can land on after setX clamping.
	values := make([]float64, 300)
	values[0] = 500   // tall outlier far left
	values[290] = 400 // tall outlier far right
	for i := 1; i < 290; i++ {
		values[i] = 5
	}

	for _, unit := range []chartUnit{chartUnitCost, chartUnitTokens} {
		t.Run(fmt.Sprintf("unit=%d", unit), func(t *testing.T) {
			m := seedRescaleModel(t, values)
			m.unitIdx = int(unit)
			m.peak = 500 // pre-rescale full-range max

			// Position over the quiet middle region.
			m.setX(150)
			if m.viewportXOffset == 0 {
				t.Fatalf("viewportXOffset still 0 after setX(150); test setup can't isolate the outlier — adjust seed shape")
			}
			m.scrollGen = 7 // arbitrary >0

			updated, _ := m.Update(rescaleMsg{gen: 7})
			m = updated.(Model)

			visN := m.visibleBuckets()
			want := peakOf(m.lastValues, m.viewportXOffset, m.viewportXOffset+visN)
			if m.peak != want {
				t.Errorf("unit=%v: m.peak = %v, want %v", unit, m.peak, want)
			}
			if m.peak >= 500 {
				t.Errorf("unit=%v: m.peak = %v still pinned by full-range outlier", unit, m.peak)
			}
		})
	}
}
