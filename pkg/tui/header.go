package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/status"
)

// IndexProgress carries indexing state into renderHeader.
type IndexProgress struct {
	Done   int
	Total  int
	Active bool
}

func renderHeader(w status.Window, width int, idx IndexProgress, subtitle string, isDev bool) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Base01).
		Padding(0, 1).
		Width(width - 2)

	label := w.CeilingPretty
	if label == "" {
		label = w.CeilingLabel
	}
	if label == "" {
		label = "Unknown"
	}
	title := fmt.Sprintf("ccpulse  %s", label)
	if isDev {
		title += lipgloss.NewStyle().Foreground(Base01).
			Render(" [DEV]")
	}
	if idx.Active {
		title += lipgloss.NewStyle().Foreground(Base01).
			Render(fmt.Sprintf(" · indexing %d/%d", idx.Done, idx.Total))
	}
	if w.QuotaSource == "cache_stale" {
		mins := int(time.Since(w.QuotaUpdatedAt).Minutes())
		if mins < 1 {
			mins = 1
		}
		title += fmt.Sprintf(" · ⚠ %dm old", mins)
	}

	return box.Render(strings.TrimSpace(title) + "\n" + subtitle)
}

func durString(mins int) string {
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
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
