package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestEarliestRemainingSampleAt(t *testing.T) {
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
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
func remainingModeModel(t *testing.T, pts5h, pts7d []cache.UtilizationPoint) *Model {
	t.Helper()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	m := New(Deps{})
	m.zoomIdx = 0                    // 15m, stride=1
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
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample is 12h into the canvas = column 48.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(t, pts5h, nil)

	m.setX(0) // try to pan to the far left

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(0) = %d, want %d (earliest in-range bucket)", got, want)
	}
}

func TestSetX_RemainingMode_OutOfRangeSnapsIn(t *testing.T) {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(t, pts5h, nil)
	m.viewportXOffset = 5 // simulate a stale pre-switch anchor

	// refreshChart-style restore: anchorTime maps to col 5; setX clamps up.
	m.setX(5)

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(5) = %d, want %d (snapped to in-range)", got, want)
	}
}

func TestSetX_RemainingMode_InRangePreservesPosition(t *testing.T) {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(t, pts5h, nil)

	m.setX(60) // already past the in-range left edge (48)

	if got, want := m.viewportXOffset, 60; got != want {
		t.Errorf("viewportXOffset after setX(60) = %d, want %d (preserved, no clamp)", got, want)
	}
}

func TestSetX_RemainingMode_EmptySamplesNoPanic(t *testing.T) {
	m := remainingModeModel(t, nil, nil)

	// Must not panic on empty slices.
	m.setX(0)
	if got := m.viewportXOffset; got != 0 {
		t.Errorf("viewportXOffset after setX(0) with empty samples = %d, want 0", got)
	}

	m.setX(999) // overshoot upper bound
	// Upper bound: max(0, lastCanvasW - viewport.Width) / stride
	//            = max(0, 96 - 30) / 1 = 66
	if got, want := m.viewportXOffset, 66; got != want {
		t.Errorf("viewportXOffset after setX(999) with empty samples = %d, want %d (canvas-clamp upper bound)", got, want)
	}
}

func TestSetX_BarModeAfterRemaining_BarBoundsRestored(t *testing.T) {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(t, pts5h, nil)
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
