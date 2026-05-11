package tui

import (
	"fmt"

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

// formatReset5h wraps durString and right-pads its output to a fixed
// 6 cols so the surrounding chrome stays anchored as minutes-to-reset
// changes (e.g. "52m" → "4h 59m" doesn't shift the percent column).
// 6 is the worst-case visual width of durString inside a 5h window —
// "4h 59m" is 6 chars; values like "0m" or "52m" get trailing pad.
func formatReset5h(mins int) string {
	return fmt.Sprintf("%-6s", durString(mins))
}

// formatReset7d renders the 7d quota reset time. For >= 24h remaining,
// it returns whole days ("1d", "7d") — the rounding loss is harmless for
// a multi-day horizon. For < 24h, it switches to zero-padded HH:MM
// duration ("23:59", "00:30") so the eventual reset reads at a glance.
func formatReset7d(mins int) string {
	if mins >= 1440 {
		return fmt.Sprintf("%dd", mins/1440)
	}
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}
