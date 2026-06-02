package tui

import (
	"math"
	"testing"
	"time"
)

func TestRasterizeSkyline_ColumnHeightsMatchNtchartsFormula(t *testing.T) {
	t.Parallel()
	// 15m zoom: BarWidth=1, BarGap=0, stride=1. Three buckets, peak=10.
	// niceCeilingFloat(10)=10, barsH=20 → heights = value/10*20 = value*2.
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	values := []float64{2, 5, 10}
	starts := []time.Time{now.Add(-30 * time.Minute), now.Add(-15 * time.Minute), now}
	const vpWidth, barsH = 3, 20

	sky := rasterizeSkyline(values, starts, 10, vpWidth, barsH, ZoomLevels[0])

	if len(sky) != vpWidth {
		t.Fatalf("len(sky)=%d, want %d", len(sky), vpWidth)
	}
	// Flush-right, stride=1, slack=0 → cols map 1:1 to the last vpWidth buckets.
	want := []float64{4, 10, 20} // value*2
	for i, w := range want {
		if math.Abs(sky[i]-w) > 1e-9 {
			t.Errorf("sky[%d]=%v, want %v", i, sky[i], w)
		}
	}
}

func TestRasterizeSkyline_ZeroPeakAllZero(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	sky := rasterizeSkyline([]float64{0, 0}, []time.Time{now.Add(-15 * time.Minute), now}, 0, 2, 20, ZoomLevels[0])
	for i, h := range sky {
		if h != 0 {
			t.Errorf("sky[%d]=%v, want 0 (peak=0)", i, h)
		}
	}
}

func TestDrawSkyline_MatchesBuildChartHeights(t *testing.T) {
	t.Parallel()
	// drawSkyline of a rasterized window must reproduce buildChart's per-column
	// integer bar heights (the #393 continuity guarantee). 15m zoom, BarWidth=1.
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	values := []float64{2, 5, 10}
	starts := []time.Time{now.Add(-30 * time.Minute), now.Add(-15 * time.Minute), now}
	const chartH = 12 // barsH = 11 (chartH>=6 reserves 1 row for x-labels)
	barsH := chartH - 1

	want := buildChart(values, starts, 10, len(values), chartH, now, ZoomLevels[0], chartUnitCost, dateOrderMonthFirst)
	sky := rasterizeSkyline(values, starts, 10, len(values), barsH, ZoomLevels[0])
	got := drawSkyline(sky, barsH, chartUnitCost)

	for col := range values {
		if g, w := barHeightAtCol(got+"\n", col), barHeightAtCol(want, col); g != w {
			t.Errorf("col %d: drawSkyline height=%d, buildChart height=%d", col, g, w)
		}
	}
}
