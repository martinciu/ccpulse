package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/status"
)

type IndexProgress struct {
	Done   int
	Total  int
	Active bool
}

func renderHeader(w status.Window, width int, idx IndexProgress) string {
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

	dur := durString(w.MinutesToReset)
	subtitle := fmt.Sprintf("%d%%   %s to reset", w.Percent, dur)
	return box.Render(strings.TrimSpace(title) + "\n" + subtitle)
}

func durString(mins int) string {
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}