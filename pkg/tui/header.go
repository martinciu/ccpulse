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
	if mins >= 1440 {
		return fmt.Sprintf("%dd %dh", mins/1440, (mins%1440)/60)
	}
	if mins >= 60 {
		return fmt.Sprintf("%dh %dm", mins/60, mins%60)
	}
	return fmt.Sprintf("%dm", mins)
}

// formatReset7d renders the 7d quota reset time in variable-width form.
// For >= 24h remaining, it returns whole days ("1d", "7d") — the
// rounding loss is harmless for a multi-day horizon. For < 24h it
// switches to zero-padded HH:MM ("23:59", "00:30") so the eventual
// reset reads at a glance. Alignment is the caller's responsibility:
// renderQuotaSide right-aligns the whole percent+time status block as
// a unit so short values like "5d" sit flush against the divider.
func formatReset7d(mins int) string {
	if mins >= 1440 {
		return fmt.Sprintf("%dd", mins/1440)
	}
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

// statusBlockMaxW is the worst-case visible width of the percent+time
// status block, used by renderQuotaSide. Worst case is "100% 4h 59m"
// (5h side; 11 cols). The 7d side's worst case is "100% 23:59"
// (10 cols), so it gets 1 col of leading pad — keeping both sides'
// status slots symmetric is what lets the │ divider centre exactly.
const statusBlockMaxW = 11

// renderQuotaSide composes one side of the quota bars row:
//
//	[dim label] [bar] [right-aligned status block: "percent% reset"]
//
// The status block ("33% 5d", "100% 4h 59m", etc.) is treated as a
// single unit and right-aligned within a fixed statusBlockMaxW slot.
// That keeps the percent and reset value visually adjacent (one space
// between them) and pushes the unused slot space to the gap between
// the bar and the status — where it visually merges with the bar's
// unfilled cells rather than leaving an awkward fixed gap.
//
// Rendered width is always lipgloss.Width(label) + bar.Width +
// statusBlockMaxW — the three components composed via JoinHorizontal.
//
// label is rendered in Base01 (Solarized comment-grey) to match the
// divider's dim style. reset is variable-width output from durString
// or formatReset7d.
func renderQuotaSide(label string, bar progress.Model, fillRatio float64, percent int, reset string) string {
	dim := lipgloss.NewStyle().Foreground(Base01)
	status := fmt.Sprintf("%d%% %s", percent, reset)
	statusSlot := lipgloss.NewStyle().Width(statusBlockMaxW).Align(lipgloss.Right).Render(status)
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		dim.Render(label),
		bar.ViewAs(fillRatio),
		statusSlot,
	)
}
