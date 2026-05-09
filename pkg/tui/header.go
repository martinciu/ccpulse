package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/status"
)

// IndexProgress is a small render-time view of the model's three
// indexing fields, passed into renderHeader.
type IndexProgress struct {
	Done   int
	Total  int
	Active bool
}

func renderHeader(s Style, w status.Window, width int, idx IndexProgress) string {
	bar := renderBar(w.Percent, width-41)
	dur := durString(w.MinutesToReset)
	right := fmt.Sprintf("%d%%   %s to reset", w.Percent, dur)
	line := fmt.Sprintf("Plan window  %s  %s", bar, right)
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(Base01).
		Padding(0, 1).
		Width(width - 2)

	title := fmt.Sprintf(" ccpulse  %s ", w.CeilingLabel)
	if idx.Active {
		suffix := lipgloss.NewStyle().
			Foreground(Base01).
			Render(fmt.Sprintf(" · indexing %d/%d", idx.Done, idx.Total))
		title = strings.TrimRight(title, " ") + suffix + " "
	}

	return box.Render(strings.TrimSpace(title) + "\n" + line)
}

func renderBar(percent, w int) string {
	if w < 4 {
		w = 4
	}
	filled := percent * w / 100
	if filled > w {
		filled = w
	}
	color := Violet
	switch {
	case percent >= 90:
		color = Red
	case percent >= 70:
		color = Yellow
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", w-filled)
	return lipgloss.NewStyle().Foreground(color).Render(bar)
}

func durString(mins int) string {
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}
