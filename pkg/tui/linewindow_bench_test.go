package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// seedWideRemainingModel builds a remaining-mode (usage line chart) model whose
// LOGICAL canvas is much wider than the viewport.
//
// The chart's left edge comes from EarliestMessageTime, not from usage samples,
// so seedRemainingModelWithSamples (samples only, no messages) yields a canvas
// barely wider than the viewport and cannot exercise windowing. Here nBuckets
// messages 15m apart set the history span — at the 15m zoom the logical canvas
// is ~nBuckets columns — and nSamples usage samples spread evenly across that
// span give the line something to draw. The model is 122x40, as in seedModelAt.
func seedWideRemainingModel(tb testing.TB, nBuckets, nSamples int, now time.Time) (Model, *cache.Cache) {
	tb.Helper()
	m, c := seedModelAt(tb, int(chartUnitRemaining), nBuckets, now)
	span := time.Duration(nBuckets) * 15 * time.Minute
	for i := range nSamples {
		when := now.Add(-span * time.Duration(i) / time.Duration(nSamples))
		resets := when.Add(2 * time.Hour)
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: 20 + float64(i%60), ResetsAt: &resets},
		}
		if err := c.RecordUsageSample(tb.Context(), u, when); err != nil {
			tb.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m.refreshChart()
	return m, c
}

// BenchmarkRefreshChartRemaining measures a full steady-state refresh of the
// usage line chart against history length. Before #528 B/op grew ~linearly —
// ntcharts allocated canvasW x rows cells of 560 B, ~37 KB per logical column.
//
// After it the cost is sub-linear, NOT flat: ~34 B and ~55 ns per column, so
// 1,000 -> 20,000 columns is about +12% B/op and +26% ns/op — small, but well
// outside run-to-run noise. All of that residual is the full-canvas x-label row
// refreshChart rebuilds once per refresh (renderXLabels + synthLabelStarts);
// with that row reused instead, B/op moves only +0.4% from 20,000 to 50,000.
// Windowing it is the remaining work, and what would let maxChartColumns rise.
//
// Note the fixture dilutes the render: refreshChart also issues three SQLite
// queries per call (EarliestMessageTime plus two UtilizationSince), a
// width-independent floor of roughly 3.6 ms on this machine. The render work is
// therefore flatter than these totals suggest.
func BenchmarkRefreshChartRemaining(b *testing.B) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	for _, n := range []int{1_000, 5_000, 20_000, 50_000} {
		b.Run(fmt.Sprintf("cols=%d", n), func(b *testing.B) {
			m, c := seedWideRemainingModel(b, n, 500, now)
			defer c.Close()
			if m.lastCanvasW < n/2 {
				b.Fatalf("fixture: lastCanvasW = %d, want ~%d", m.lastCanvasW, n)
			}
			b.ReportAllocs()
			for b.Loop() {
				m.refreshChart()
			}
		})
	}
}

// BenchmarkScrollRemaining measures one scroll keypress pair in the usage
// line chart. Before #528 this is a pure offset (nanoseconds, zero allocs);
// after it each keypress re-renders the visible window, like the bars have
// since #255.
//
// Read the trend across `cols` with care: the 500 samples are spread over the
// whole span, so a ~120-column window holds ~61 of them at cols=1000 but ~3 at
// cols=20000. That thinning very nearly cancels the growth of the O(canvas)
// ansi.Cut of the label row, so an apparently flat line here is two opposing
// effects, not an absence of scaling. cols=50_000 is the shipped
// maxChartColumns and is the case that matters.
func BenchmarkScrollRemaining(b *testing.B) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	for _, n := range []int{1_000, 20_000, 50_000} {
		b.Run(fmt.Sprintf("cols=%d", n), func(b *testing.B) {
			m, c := seedWideRemainingModel(b, n, 500, now)
			defer c.Close()
			m.scrollLeft(50) // off the pinned-right edge
			b.ReportAllocs()
			for b.Loop() {
				m.scrollLeft(1)
				m.scrollRight(1)
			}
		})
	}
}
