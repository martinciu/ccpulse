package tui

import (
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

type ZoomLevel struct {
	Label    string
	Duration time.Duration
}

var ZoomLevels = []ZoomLevel{
	{"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
}

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

func buildChart(buckets []cache.TokenBucket, chartW, chartH int) string {
	if chartH < 1 {
		chartH = 1
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
		bars[i] = barchart.BarData{
			Values: []barchart.BarValue{
				{
					Value: float64(b.Tokens),
					Style: lipgloss.NewStyle().Foreground(heatColor(ratio)),
				},
			},
		}
	}

	bc := barchart.New(chartW, chartH)
	bc.PushAll(bars)
	bc.Draw()
	return bc.View()
}