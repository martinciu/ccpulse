package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestLerpTime(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	b := a.Add(100 * time.Hour)
	cases := []struct {
		name string
		r    float64
		want time.Time
	}{
		{"r=0 returns a", 0.0, a},
		{"r=1 returns b", 1.0, b},
		{"r=0.5 midpoint", 0.5, a.Add(50 * time.Hour)},
		{"r<0 clamps to a", -0.5, a},
		{"r>1 clamps to b", 1.5, b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lerpTime(a, b, tc.r); !got.Equal(tc.want) {
				t.Errorf("lerpTime(a, b, %v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

func TestVisibleWindow_RemainingGeometry(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// One sample 12h in so setX clamps the offset to column 48 (the earliest
	// in-range bucket) — matches TestSetX_RemainingMode_ClampsAtInRangeLeftEdge.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h)
	m.setX(0) // clamp to col 48

	vf, vt := m.visibleWindow()

	// fullCanvasW = max(CanvasWidth(96 buckets @ 15m stride=1)=96, vpW=30)=96.
	// chartXOffset = viewportXOffset(48)*stride(1) = 48.
	// viewFrom = columnToTime(48, 96, from, from+24h) = from+12h.
	// viewTo   = columnToTime(48+30=78, 96, ...)      = from + 78/96*24h = +19.5h.
	wantFrom := from.Add(12 * time.Hour)
	wantTo := from.Add(time.Duration(float64(78) / float64(96) * float64(24*time.Hour)))
	if !vf.Equal(wantFrom) {
		t.Errorf("visibleWindow from = %v, want %v", vf, wantFrom)
	}
	if !vt.Equal(wantTo) {
		t.Errorf("visibleWindow to = %v, want %v", vt, wantTo)
	}
}
