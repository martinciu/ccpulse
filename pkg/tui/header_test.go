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
	// renderQuotaSide must produce output exactly slotW cols wide for any
	// well-formed inputs, regardless of bar width, percent, or time-string
	// width. This is the safety-net property of the
	// lipgloss.NewStyle().Width(slotW) wrapper combined with the
	// right-aligned statusBlockMaxW status slot.
	bar := progress.New(
		progress.WithWidth(10),
		progress.WithoutPercentage(),
		progress.WithGradient(QuotaGradientStart, QuotaGradientEnd),
	)
	const slotW = 3 + 10 + statusBlockMaxW // label + bar + status block = 24
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
			got := renderQuotaSide("5h ", bar, float64(tt.percent)/100.0, tt.percent, tt.time, slotW)
			if w := lipgloss.Width(got); w != slotW {
				t.Errorf("renderQuotaSide percent=%d time=%q: width %d, want %d", tt.percent, tt.time, w, slotW)
			}
		})
	}
}
