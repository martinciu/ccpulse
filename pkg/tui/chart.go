package tui

import (
	"strings"
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
// in columns (= number of buckets); chartH is the height in rows
// (bars + baseline). The bottom row is always a baseline strip:
// '▒' over data columns, '░' over gap columns.
func buildChart(buckets []cache.TokenBucket, chartW, chartH int) string {
	if chartH < 2 {
		chartH = 2 // need at least one bar row + one baseline row
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

	// Bars take all but the bottom row; the bottom row is the baseline.
	// barGap=0 is required when chartW == numBars: the default gap of 1
	// consumes (numBars-1) cols, leaving (graphSize-gaps)/numBars = 0
	// width per bar — i.e. bars are not drawn at all.
	bc := barchart.New(chartW, chartH-1, barchart.WithBarGap(0))
	bc.PushAll(bars)
	bc.Draw()

	dataStyle := lipgloss.NewStyle().Foreground(Base01)
	gapStyle := lipgloss.NewStyle().Foreground(Base02)
	var sb strings.Builder
	sb.Grow(len(buckets) * 4) // styled rune is several bytes
	for _, b := range buckets {
		if b.Tokens > 0 {
			sb.WriteString(dataStyle.Render("▒"))
		} else {
			sb.WriteString(gapStyle.Render("░"))
		}
	}

	return lipgloss.JoinVertical(lipgloss.Left, bc.View(), sb.String())
}
