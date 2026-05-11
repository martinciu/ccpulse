package tui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
)

// IndexProgress carries indexing state from the model into the footer
// indicator block. Built by View(), passed to renderIndicators.
type IndexProgress struct {
	Done   int
	Total  int
	Active bool
}

// renderHeader returns the bordered box containing the supplied bar row.
// Status indicators ([DEV], indexing, stale-quota warning) used to live
// here on a separate title row; they now compose into the footer via
// renderIndicators.
func renderHeader(width int, bars string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Base01).
		Padding(0, 1).
		Width(width - 2)
	return box.Render(bars)
}

func durString(mins int) string {
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}

// formatReset5h wraps durString and right-aligns its output inside a
// fixed 6-col slot so the time always sits flush against the divider
// (or right edge). Padding falls between the percent block and the
// time digits — the natural separator zone — rather than between the
// time and the divider, which would leave a visible gap. 6 is the
// worst-case visual width of durString inside a 5h window: "4h 59m"
// is 6 chars; values like "0m" or "52m" get leading pad.
func formatReset5h(mins int) string {
	return fmt.Sprintf("%6s", durString(mins))
}

// formatReset7d renders the 7d quota reset time, right-aligned inside
// a fixed 6-col slot so the value sits flush against the divider and
// the 7d-side chrome matches the 5h-side chrome — this symmetry is
// what lets the │ divider sit at the true midpoint of the bars row.
// For >= 24h remaining, it returns whole days ("1d", "7d") — the
// rounding loss is harmless for a multi-day horizon. For < 24h it
// switches to zero-padded HH:MM ("23:59", "00:30") so the eventual
// reset reads at a glance. Both forms are right-aligned to 6 cols.
func formatReset7d(mins int) string {
	if mins >= 1440 {
		return fmt.Sprintf("%6s", fmt.Sprintf("%dd", mins/1440))
	}
	return fmt.Sprintf("%6s", fmt.Sprintf("%02d:%02d", mins/60, mins%60))
}

// renderQuotaSide composes one side of the quota bars row:
//   [dim label] [bar] [percent block] [padded time]
//
// Wrapped in lipgloss.NewStyle().Width(slotW) as a safety net so the
// rendered output is exactly slotW cols wide regardless of subtle
// terminal/font width disagreements about the bar's styled output.
//
// label is rendered in Base01 (Solarized comment-grey) to match the
// divider's dim style. paddedTime is expected to be a fixed-width
// string (6 cols) produced by formatReset5h or formatReset7d.
func renderQuotaSide(label string, bar progress.Model, fillRatio float64, percent int, paddedTime string, slotW int) string {
	dim := lipgloss.NewStyle().Foreground(Base01)
	parts := lipgloss.JoinHorizontal(
		lipgloss.Top,
		dim.Render(label),
		bar.ViewAs(fillRatio),
		fmt.Sprintf(" %3d%%", percent),
		"  ",
		paddedTime,
	)
	return lipgloss.NewStyle().Width(slotW).Render(parts)
}
