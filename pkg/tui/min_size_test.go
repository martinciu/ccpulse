package tui

import (
	"strings"
	"testing"
)

// TestViewBelowMinSize is the focused companion to #321's view_fit_test.go
// matrix: it asserts that View() short-circuits to the centered "Terminal
// too small" notice below the MinWidth/MinHeight threshold, and renders
// the full layout at or above it.
//
// The five cells cover the four quadrants of the threshold (w-only,
// h-only, both, above) plus the exactly-on-threshold edge case.
func TestViewBelowMinSize(t *testing.T) {
	t.Parallel()
	c := emptyFitCache(t)
	win := windowFor("safe", true)

	cases := []struct {
		name      string
		w, h      int
		wantSmall bool
	}{
		{"below_width_only", 60, 30, true},
		{"below_height_only", 100, 20, true},
		{"both_below", 60, 20, true},
		{"at_threshold", MinWidth, MinHeight, false},
		{"above_threshold", 120, 40, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := buildFitModel(c, tc.w, tc.h, win, int(chartUnitCost), false)
			v := m.View()

			hasNotice := strings.Contains(v, "Terminal too small")
			// The header box corner is unambiguously present in the full
			// layout and absent from the notice (which uses lipgloss.Place,
			// emitting only spaces around the centered text block).
			hasHeaderBox := strings.Contains(v, "╭")

			if tc.wantSmall {
				if !hasNotice {
					t.Errorf("(%d×%d) want notice, got full layout:\n%s", tc.w, tc.h, v)
				}
				if hasHeaderBox {
					t.Errorf("(%d×%d) want notice only, full-layout marker '╭' leaked:\n%s", tc.w, tc.h, v)
				}
			} else {
				if hasNotice {
					t.Errorf("(%d×%d) want full layout, notice rendered:\n%s", tc.w, tc.h, v)
				}
				if !hasHeaderBox {
					t.Errorf("(%d×%d) want full layout, header box marker '╭' absent:\n%s", tc.w, tc.h, v)
				}
			}
		})
	}
}
