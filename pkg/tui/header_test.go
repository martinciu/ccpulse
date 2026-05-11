package tui

import (
	"fmt"
	"testing"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

func TestFormatReset7d_Content(t *testing.T) {
	// formatReset7d is a pure variable-width formatter — no padding.
	// Layout (right-align inside a fixed slot) is the renderQuotaSide
	// helper's job. Asserts raw equality so accidental padding regressions
	// fail loudly. Boundary cases: 0, 60 (hour rollover), 1439 (just before
	// day mode), 1440 (just at), 10080 (7 days).
	tests := []struct {
		mins int
		want string
	}{
		{0, "00:00"},
		{30, "00:30"},
		{60, "01:00"},
		{90, "01:30"},
		{1439, "23:59"},
		{1440, "1d"},
		{1500, "1d"}, // truncates, does not round
		{10080, "7d"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%dmins", tt.mins), func(t *testing.T) {
			got := formatReset7d(tt.mins)
			if got != tt.want {
				t.Errorf("formatReset7d(%d) = %q, want %q", tt.mins, got, tt.want)
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
