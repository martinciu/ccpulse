package tui

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestFormatReset5h_PadsToFixedWidth(t *testing.T) {
	// Every output of formatReset5h must be exactly 6 cols wide
	// (lipgloss.Width, not len) so the surrounding chrome stays anchored.
	// Covers the 5h-window range: minutes-to-reset is in [0, 300).
	for _, mins := range []int{0, 1, 59, 60, 120, 299} {
		t.Run(fmt.Sprintf("%dmins", mins), func(t *testing.T) {
			got := formatReset5h(mins)
			if w := lipgloss.Width(got); w != 6 {
				t.Errorf("formatReset5h(%d) = %q (width %d); want width 6", mins, got, w)
			}
		})
	}
}

func TestFormatReset7d_PadsToFixedWidth(t *testing.T) {
	// Every output of formatReset7d must be exactly 6 cols wide so the
	// 7d-side chrome matches the 5h-side chrome — symmetric chrome is
	// what lets the │ divider sit at the true midpoint of the bars row.
	// Covers the 7d-window range: minutes-to-reset is in [0, 10080].
	for _, mins := range []int{0, 30, 90, 1439, 1440, 1500, 10080} {
		t.Run(fmt.Sprintf("%dmins", mins), func(t *testing.T) {
			got := formatReset7d(mins)
			if w := lipgloss.Width(got); w != 6 {
				t.Errorf("formatReset7d(%d) = %q (width %d); want width 6", mins, got, w)
			}
		})
	}
}

func TestFormatReset7d_Content(t *testing.T) {
	// Asserts the visible (non-padding) prefix of formatReset7d. Trailing
	// whitespace is handled by TestFormatReset7d_PadsToFixedWidth — here
	// we only care that the visible content matches expected formatting.
	tests := []struct {
		mins int
		want string // visible content; trailing padding stripped
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
			got := strings.TrimRight(formatReset7d(tt.mins), " ")
			if got != tt.want {
				t.Errorf("formatReset7d(%d) = %q (trimmed), want %q", tt.mins, got, tt.want)
			}
		})
	}
}
