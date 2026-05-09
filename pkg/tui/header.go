package tui

import (
	"fmt"
	"strings"
	"time"

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

func renderHeader(s Style, w status.Window, expired bool, width int, idx IndexProgress) string {
	var line string
	if w.Has7d {
		line = renderTwoBars(w, width)
	} else {
		bar := renderBar(w.Percent, width-41)
		dur := durString(w.MinutesToReset)
		right := fmt.Sprintf("%d%%   %s to reset", w.Percent, dur)
		line = fmt.Sprintf("Usage window  %s  %s", bar, right)
	}
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
	if idx.Active {
		suffix := lipgloss.NewStyle().
			Foreground(Base01).
			Render(fmt.Sprintf(" · indexing %d/%d", idx.Done, idx.Total))
		title = strings.TrimRight(title, " ") + suffix + " "
	}
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

// renderTwoBars draws "5h <bar>  PPP%  Hh MMm  │  7d <bar>  PPP%  Hh MMm"
// onto a single header line. The two bars get equal width; any leftover
// char from an odd content split goes to the right bar.
func renderTwoBars(w status.Window, width int) string {
	content := width - 4
	const sideOverhead = 18
	const dividerWidth = 3
	bars := content - dividerWidth - 2*sideOverhead
	if bars < 4 {
		bars = 4
	}
	leftBar := bars / 2
	rightBar := bars - leftBar
	bar5h := renderBar(w.Percent, leftBar)
	bar7d := renderBar(w.Percent7d, rightBar)
	left := fmt.Sprintf("5h %s  %s", bar5h, rightLabel(w.Percent, w.MinutesToReset))
	right := fmt.Sprintf("7d %s  %s", bar7d, rightLabel(w.Percent7d, w.MinutesToReset7d))
	divider := lipgloss.NewStyle().Foreground(Base01).Render("│")
	return left + " " + divider + " " + right
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

// padDur returns durString(mins) left-padded to 7 chars so durations
// like "59m" and "17h 33m" line up under each other.
func padDur(mins int) string {
	return fmt.Sprintf("%7s", durString(mins))
}

// rightLabel formats the percent + duration trailing text for one
// half of the side-by-side header line.
func rightLabel(percent, mins int) string {
	return fmt.Sprintf("%3d%%  %s", percent, padDur(mins))
}
