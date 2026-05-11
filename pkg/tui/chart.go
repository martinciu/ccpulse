package tui

import (
	"log/slog"
	"strconv"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// ZoomLevel maps a human label to a bucket duration. The chart's
// horizontal extent is no longer per-zoom — it spans from the earliest
// cached message to "now" at every zoom (see issue #53).
type ZoomLevel struct {
	Label    string
	Duration time.Duration
}

// ZoomLevels are the available zoom steps, cycled with the z key.
var ZoomLevels = []ZoomLevel{
	{"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
}

// formatTokenCount renders an int64 token count compactly with a k/M
// suffix, suitable for the Y axis and other in-chart annotations.
// Threshold rules:
//
//	n <= 0       -> "0"
//	n < 1000     -> raw integer
//	n < 1e6      -> k suffix, 1 frac digit; drop frac when v >= 100
//	n >= 1e6     -> M suffix, same precision rule
//
// Max output width: 5 cols. Fits the 6-col Y axis budget with breathing room.
func formatTokenCount(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return formatSuffixed(float64(n)/1000, "k")
	}
	return formatSuffixed(float64(n)/1_000_000, "M")
}

// formatSuffixed prints v with 1 fractional digit when v < 100 and 0
// fractional digits otherwise, then appends suffix. Keeps labels inside
// the 5-col budget for any expected token magnitude.
func formatSuffixed(v float64, suffix string) string {
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64) + suffix
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + suffix
}

// heatColor returns a lipgloss color on a green→yellow→red ramp
// based on ratio (0.0–1.0) of a bucket's tokens relative to the peak bucket.
func heatColor(ratio float64) lipgloss.Color {
	switch {
	case ratio >= 0.66:
		return Red
	case ratio >= 0.33:
		return Yellow
	default:
		return Green
	}
}

// buildChart renders a bar chart from buckets and returns the string
// to be passed to viewport.SetContent. chartW is the total chart width
// in columns (= number of buckets); chartH is the height in rows,
// fully consumed by bars.
func buildChart(buckets []cache.TokenBucket, chartW, chartH int) string {
	start := time.Now()
	if chartH < 1 {
		chartH = 1 // barchart.New panics with a zero height
	}

	var peak int64
	for _, b := range buckets {
		if b.Tokens > peak {
			peak = b.Tokens
		}
	}

	bars := make([]barchart.BarData, len(buckets))
	for i, b := range buckets {
		ratio := float64(0)
		if peak > 0 {
			ratio = float64(b.Tokens) / float64(peak)
		}
		c := heatColor(ratio)
		// ntcharts paints bar cells using both fg + bg of the same color
		// so the cell renders as a solid block (the example sets both).
		// Foreground alone leaves the cell empty.
		bars[i] = barchart.BarData{
			Values: []barchart.BarValue{
				{
					Value: float64(b.Tokens),
					Style: lipgloss.NewStyle().Foreground(c).Background(c),
				},
			},
		}
	}

	// barGap=0 is required when chartW == numBars: the default gap of 1
	// consumes (numBars-1) cols, leaving (graphSize-gaps)/numBars = 0
	// width per bar — i.e. bars are not drawn at all.
	bc := barchart.New(chartW, chartH, barchart.WithBarGap(0))
	bc.PushAll(bars)
	bc.Draw()

	out := bc.View()
	slog.Debug("tui.buildChart",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(buckets),
		"chartW", chartW,
		"chartH", chartH)
	return out
}
