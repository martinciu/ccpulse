package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/status"
)

func renderHeader(s Style, w status.Window, expired bool, width int) string {
	bar := renderBar(w.Percent, width-41)
	dur := durString(w.MinutesToReset)
	right := fmt.Sprintf("%d%%   %s to reset", w.Percent, dur)
	line := fmt.Sprintf("Plan window  %s  %s", bar, right)
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
	title := fmt.Sprintf(" ccpulse  %s ", label)
	switch {
	case expired:
		title += "· ⚠ auth expired "
	case w.QuotaSource == "cache_stale":
		mins := int(time.Since(w.QuotaUpdatedAt).Minutes())
		if mins < 1 {
			mins = 1
		}
		title += fmt.Sprintf("· ⚠ %dm old ", mins)
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
