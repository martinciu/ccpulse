package tui

import (
	"fmt"
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
