package tui

import (
	"fmt"
	"strings"
	"time"

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
// renderQuotaSide right-aligns the time inside a fixed slot so short
// values like "5d" sit flush against the divider.
func formatReset7d(mins int) string {
	if mins >= 1440 {
		return fmt.Sprintf("%dd", mins/1440)
	}
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

// statusBlockMaxW is the worst-case visible width of the time-to-reset
// status block, used by renderQuotaSide. Worst case is "4h 59m"
// (5h side; 6 cols). The 7d side's worst case is "23:59" (5 cols), so
// it gets 1 col of leading pad — keeping both sides' status slots
// symmetric is what lets the │ divider centre exactly. The current %
// is conveyed by the bar fill itself and is no longer text-rendered.
const statusBlockMaxW = 6

// barTimeGap is the explicit 1-col margin between the bar and the
// right-aligned time slot, so the worst-case time ("4h 59m") doesn't
// abut the bar's trailing cells. At shorter times the slot's
// right-align adds further padding on top of this guaranteed minimum.
const barTimeGap = " "

// burnPad is the leading blank that replaces the "5h "/"7d " label on
// the burn-rate row, so the projection text starts at the same column
// as the progress bar above it. Three spaces match lipgloss.Width("5h ").
const burnPad = "   "

// renderQuotaSide composes one side of the quota bars row:
//
//	[dim label] [bar] [1-col gap] [right-aligned time slot]
//
// The reset time ("5d", "23:59", "4h 59m", etc.) is right-aligned
// within a fixed statusBlockMaxW slot so short values sit flush
// against the divider. A 1-col barTimeGap between the bar and the
// slot guarantees a visible margin even when the time string fills
// the slot entirely.
//
// Rendered width is always lipgloss.Width(label) + bar.Width +
// 1 + statusBlockMaxW — composed via JoinHorizontal.
//
// label is rendered in Base01 (Solarized comment-grey) to match the
// divider's dim style. reset is variable-width output from durString
// or formatReset7d.
func renderQuotaSide(label string, bar progress.Model, fillRatio float64, reset string) string {
	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		dimStyle.Render(label),
		bar.ViewAs(fillRatio),
		barTimeGap,
		timeSlotStyle.Render(reset),
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
	burnSeverityWatch                         // WillOverreach && eta > 10% of window
	burnSeverityDanger                        // WillOverreach && eta <= 10% of window (or eta nil)
)

// Package-level lipgloss styles for the bars/burn-rate header rows.
// Hoisted out of renderQuotaSide / renderBurnRateSide so a fresh Style
// isn't allocated on every View() frame (this code runs in the per-frame
// hot path).
var (
	dimStyle        = lipgloss.NewStyle().Foreground(Base01)
	burnSafeStyle   = lipgloss.NewStyle().Foreground(Green)
	burnWatchStyle  = lipgloss.NewStyle().Foreground(Yellow)
	burnDangerStyle = lipgloss.NewStyle().Foreground(Red)
	timeSlotStyle   = lipgloss.NewStyle().Width(statusBlockMaxW).Align(lipgloss.Right)
)

// burnImminentRatio is the fraction of a bucket's window below which an
// overreaching projection escalates from "watch" (yellow) to "danger"
// (red). 10% means: 5h bucket → 30-min red zone; 7d bucket → ~17-hour
// red zone. Scaling the threshold to the window keeps the semantic
// meaning ("imminent") consistent across buckets — a fixed 30-min
// threshold would never fire watch on the 7d side. TUI-local constant.
const burnImminentRatio = 0.1

// severityFor classifies a projection into a visual state. Dispatch order:
// nil → Confidence=low → !WillOverreach → eta>threshold → eta<=threshold.
// A nil MinutesTo100Pct under WillOverreach=true means "already at limit"
// and counts as imminent (danger). The window argument is the bucket's
// total duration (5h or 7d) — it scales the imminent threshold.
func severityFor(p *status.Projection, window time.Duration) burnSeverity {
	if p == nil {
		return burnSeverityNoData
	}
	if p.Confidence == "low" {
		return burnSeverityWarmingUp
	}
	if !p.WillOverreach {
		return burnSeveritySafe
	}
	thresholdMinutes := int(window.Minutes() * burnImminentRatio)
	if p.MinutesTo100Pct == nil || *p.MinutesTo100Pct <= thresholdMinutes {
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
//
// window is the bucket's full duration (5h or 7d), forwarded to
// severityFor so the imminent threshold scales per bucket.
func renderBurnRateSide(label string, p *status.Projection, slotW int, window time.Duration) string {
	labelW := lipgloss.Width(label)
	textSlot := max(slotW-labelW, 1)
	render := func(text string, style lipgloss.Style) string {
		return dimStyle.Render(label) + style.Width(textSlot).Render(text)
	}
	switch severityFor(p, window) {
	case burnSeverityNoData:
		return render("(no data)", dimStyle)
	case burnSeverityWarmingUp:
		return render("warming up", dimStyle)
	case burnSeveritySafe:
		text := fmt.Sprintf("%s • projecting %d%%", formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset)
		return render(text, burnSafeStyle)
	case burnSeverityWatch:
		text := fmt.Sprintf("%s • projecting %d%% • limit in %s",
			formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
		return render(text, burnWatchStyle)
	case burnSeverityDanger:
		var text string
		if p.MinutesTo100Pct == nil {
			text = fmt.Sprintf("%s • already at limit", formatBurnRate(p.SlopePctPerHour))
		} else {
			text = fmt.Sprintf("%s • projecting %d%% • limit in %s",
				formatBurnRate(p.SlopePctPerHour), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
		}
		return render(text, burnDangerStyle)
	default:
		// Unreachable today — severityFor returns one of the five constants
		// above, all handled. Fail loudly if a new burnSeverity is added
		// without updating this switch, rather than rendering a silent
		// empty string into the header.
		panic(fmt.Sprintf("renderBurnRateSide: unhandled burnSeverity %d", severityFor(p, window)))
	}
}
