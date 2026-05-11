package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/status"
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

// formatBurnRate renders a percent-per-hour slope for the burn-rate row.
// Uses %.1f then strips a trailing ".0" so integer rates display as "12%/h"
// while sub-1 fractional rates keep their digit ("0.4%/h"). This keeps the
// header line compact without losing information on slow-burn windows.
func formatBurnRate(slope float64) string {
	s := fmt.Sprintf("%.1f", slope)
	s = strings.TrimSuffix(s, ".0")
	return s + "%/h"
}

// burnSeverity is the rendering classification for a status.Projection.
// Five mutually-exclusive states driven by a first-match dispatch.
type burnSeverity int

const (
	burnSeverityNoData    burnSeverity = iota // p == nil
	burnSeverityWarmingUp                     // Confidence == "low"
	burnSeveritySafe                          // !WillOverreach
	burnSeverityWatch                         // WillOverreach && eta > 30m
	burnSeverityDanger                        // WillOverreach && eta <= 30m (or eta nil)
)

// burnImminentThreshold is the ETA boundary below which an overreaching
// projection escalates from "watch" (yellow) to "danger" (red). TUI-local
// constant — if status --json ever exposes a severity field, this moves to
// pkg/status so both consumers share it.
const burnImminentThreshold = 30 // minutes

// severityFor classifies a projection into a visual state. Dispatch order:
// nil → Confidence=low → !WillOverreach → eta>threshold → eta<=threshold.
// A nil MinutesTo100Pct under WillOverreach=true means "already at limit"
// and counts as imminent (danger).
func severityFor(p *status.Projection) burnSeverity {
	if p == nil {
		return burnSeverityNoData
	}
	if p.Confidence == "low" {
		return burnSeverityWarmingUp
	}
	if !p.WillOverreach {
		return burnSeveritySafe
	}
	if p.MinutesTo100Pct == nil || *p.MinutesTo100Pct <= burnImminentThreshold {
		return burnSeverityDanger
	}
	return burnSeverityWatch
}

// renderBurnRateSide builds one half of the burn-rate row inside the
// header box, mirroring the layout contract of renderQuotaSide:
//
//	[dim label][padded burn-rate text within slotW]
//
// The slotW cap ensures lipgloss truncates rather than overflows at
// narrow terminals; layout above (model.quotaBars) sizes slotW to match
// the bars row above it for visual symmetry.
//
// State dispatch is delegated to severityFor; this function owns only
// the copy strings and the style mapping. A nil projection or
// low-confidence projection renders dim; the three projection-driven
// states share the same "X%/h • projecting Y%[ • limit in Zm]" template,
// with the trailing limit-in clause appearing only when overreaching.
func renderBurnRateSide(label string, p *status.Projection, slotW int) string {
	dim := lipgloss.NewStyle().Foreground(Base01)
	labelW := lipgloss.Width(label)
	textSlot := max(slotW-labelW, 1)
	render := func(text string, style lipgloss.Style) string {
		return dim.Render(label) + style.Width(textSlot).Render(text)
	}
	switch severityFor(p) {
	case burnSeverityNoData:
		return render("(no data)", dim)
	case burnSeverityWarmingUp:
		return render("🌀 warming up", dim)
	case burnSeveritySafe:
		text := fmt.Sprintf("🟢 %s • projecting %d%%", formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset)
		return render(text, lipgloss.NewStyle().Foreground(Green))
	case burnSeverityWatch:
		text := fmt.Sprintf("🟡 %s • projecting %d%% • limit in %s",
			formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
		return render(text, lipgloss.NewStyle().Foreground(Yellow))
	case burnSeverityDanger:
		var text string
		if p.MinutesTo100Pct == nil {
			text = fmt.Sprintf("🔴 %s • already at limit", formatBurnRate(p.SlopePctPerHour))
		} else {
			text = fmt.Sprintf("🔴 %s • projecting %d%% • limit in %s",
				formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
		}
		return render(text, lipgloss.NewStyle().Foreground(Red))
	}
	return ""
}
