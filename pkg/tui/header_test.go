package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

func TestFormatReset7d_Content(t *testing.T) {
	// Asserts the visible (non-padding) content of formatReset7d. Padding
	// alignment (leading vs trailing) is a layout choice handled separately;
	// here we only care that the visible content matches expected formatting.
	tests := []struct {
		mins int
		want string // visible content; padding stripped
	}{
		{30, "00:30"},
		{90, "01:30"},
		{1439, "23:59"},
		{1440, "1d"},
		{1500, "1d"}, // truncates, does not round
		{10080, "7d"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dmins", tt.mins), func(t *testing.T) {
			got := strings.TrimSpace(formatReset7d(tt.mins))
			if got != tt.want {
				t.Errorf("formatReset7d(%d) = %q (trimmed), want %q", tt.mins, got, tt.want)
			}
		})
	}
}

func TestRenderQuotaSide_ProducesExactSlotWidth(t *testing.T) {
	// renderQuotaSide's output width is determined entirely by its inputs:
	// lipgloss.Width(label) + bar.Width + statusBlockMaxW. Property under
	// test: the function returns exactly that width regardless of percent
	// or time-string width — short statuses get right-align pad inside
	// the fixed statusBlockMaxW slot, so the total stays constant.
	const labelW = 3
	const barW = 10
	bar := progress.New(
		progress.WithWidth(barW),
		progress.WithoutPercentage(),
		progress.WithGradient(QuotaGradientStart, QuotaGradientEnd),
	)
	const expectedW = labelW + barW + statusBlockMaxW
	cases := []struct {
		name    string
		percent int
		time    string
	}{
		{"min", 0, "0m"},
		{"low_short_time", 5, "52m"},
		{"mid_short_time", 33, "5d"},
		{"mid_hhmm", 50, "12:34"},
		{"high_long_time", 95, "4h 59m"},
		{"max", 100, "4h 59m"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			got := renderQuotaSide("5h ", bar, float64(tt.percent)/100.0, tt.percent, tt.time)
			if w := lipgloss.Width(got); w != expectedW {
				t.Errorf("renderQuotaSide percent=%d time=%q: width %d, want %d", tt.percent, tt.time, w, expectedW)
			}
		})
	}
}
