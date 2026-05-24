package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/martinciu/ccpulse/pkg/status"
)

// IndexProgress carries indexing state from the model into the footer
// indicator block. Built by View(), passed to renderIndicators. FadeStop
// is non-zero only during the post-backfill fade window (1, 2, or 3 —
// see indexFadeStopCount); a zero value means "not fading".
type IndexProgress struct {
	Done     int
	Total    int
	Active   bool
	FadeStop int
}

// renderHeader returns the bordered box containing the supplied bar row.
// Status indicators ([DEV], indexing, stale-quota warning) used to live
// here on a separate title row; they now compose into the footer via
// renderIndicators.
func renderHeader(width int, bars string) string {
	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(colorMuted).
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

// formatReset7d renders the 7d quota reset time in variable-width form,
// delegating to durString so the 7d side reads the same as the 5h side:
// "5d 12h" while >= 24h remain, then "18h 34m" / "34m" below that.
// Alignment is the caller's responsibility: renderQuotaSide right-aligns
// the time inside a fixed slot so short values like "34m" sit flush
// against the divider.
func formatReset7d(mins int) string {
	return durString(mins)
}

// statusBlockMaxW is the worst-case visible width of the time-to-reset
// status block, used by renderQuotaSide. The 7d side's sub-24h worst
// case is "23h 59m" (7 cols); the 5h side's worst case "4h 59m" is
// 6 cols, so the 5h side gets 1 col of leading pad — keeping both
// sides' status slots symmetric is what lets the │ divider centre
// exactly. The current % is conveyed by the bar fill itself and is no
// longer text-rendered.
const statusBlockMaxW = 7

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
// The reset time ("5d 12h", "18h 34m", "4h 59m", etc.) is right-aligned
// within a fixed statusBlockMaxW slot so short values sit flush
// against the divider. A 1-col barTimeGap between the bar and the
// slot guarantees a visible margin even when the time string fills
// the slot entirely.
//
// Rendered width is always lipgloss.Width(label) + bar.Width +
// 1 + statusBlockMaxW — composed via JoinHorizontal.
//
// label is rendered in colorMuted to match the
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

// burnRateUnit selects the time unit used by formatBurnRate to render a
// projection slope. The 5h side uses burnRateUnitPerHour (slope is already
// %/h); the 7d side uses burnRateUnitPerDay, which scales the input by 24
// before formatting so the number reads as "burn per day" against the 7d
// ceiling. The unit only changes the displayed rate token — projection,
// severity, and layout are identical across both.
type burnRateUnit int

const (
	burnRateUnitPerHour burnRateUnit = iota
	burnRateUnitPerDay
)

// formatBurnRate renders a projection slope for the burn-rate row. Uses
// %.1f then strips a trailing ".0" so integer rates display as "12%/h" or
// "12%/day" while sub-1 fractional rates keep their digit ("0.4%/h").
// For burnRateUnitPerDay the slope is multiplied by 24 before formatting,
// converting the underlying %/h figure into a %/day reading against the
// same denominator (the 7d ceiling).
func formatBurnRate(slope float64, unit burnRateUnit) string {
	value := slope
	suffix := "%/h"
	if unit == burnRateUnitPerDay {
		value = slope * 24
		suffix = "%/day"
	}
	s := fmt.Sprintf("%.1f", value)
	s = strings.TrimSuffix(s, ".0")
	return s + suffix
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
	dimStyle        = lipgloss.NewStyle().Foreground(colorMuted)
	burnSafeStyle   = lipgloss.NewStyle().Foreground(colorSafe)
	burnWatchStyle  = lipgloss.NewStyle().Foreground(colorWatch)
	burnDangerStyle = lipgloss.NewStyle().Foreground(colorDanger)
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
//	[dim label][burn-rate text, truncated + padded to the per-side slot]
//
// The burn text is ansi.Truncate'd to the slot BEFORE styling so a long
// projection string can never wrap onto a second line and break the
// header box (#320) — lipgloss .Width() word-wraps overflow rather than
// truncating, so the slot cap alone is not enough. .Width then pads short
// text back out so the two sides stay symmetric. quotaBars sizes slotW to
// match the bars row above for visual symmetry.
//
// The copy is kept compact ("{rate} →{proj}%[ ·{eta}]") so the danger
// info survives at ~80-col terminals well before truncation ever kicks in;
// the symbols stand in for the former "projecting"/"limit in" words.
//
// State dispatch is delegated to severityFor; this function owns only the
// copy strings and the style mapping. A nil or low-confidence projection
// renders dim; the projection-driven states share the
// "{rate} →{proj}%[ ·{eta}]" template, with the ·eta clause appearing only
// when overreaching (or "·at limit" when already over with no eta).
//
// window is the bucket's full duration (5h or 7d), forwarded to
// severityFor so the imminent threshold scales per bucket.
func renderBurnRateSide(label string, p *status.Projection, slotW int, window time.Duration, unit burnRateUnit) string {
	labelW := lipgloss.Width(label)
	textSlot := max(slotW-labelW, 1)
	render := func(text string, style lipgloss.Style) string {
		return dimStyle.Render(label) + style.Width(textSlot).Render(ansi.Truncate(text, textSlot, ""))
	}
	switch severityFor(p, window) {
	case burnSeverityNoData:
		return render("(no data)", dimStyle)
	case burnSeverityWarmingUp:
		return render("warming up", dimStyle)
	case burnSeveritySafe:
		text := fmt.Sprintf("%s →%d%%", formatBurnRate(p.SlopePctPerHour, unit), p.ProjectedPctAtReset)
		return render(text, burnSafeStyle)
	case burnSeverityWatch:
		text := fmt.Sprintf("%s →%d%% ·%s",
			formatBurnRate(p.SlopePctPerHour, unit), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
		return render(text, burnWatchStyle)
	case burnSeverityDanger:
		var text string
		if p.MinutesTo100Pct == nil {
			text = formatBurnRate(p.SlopePctPerHour, unit) + " ·at limit"
		} else {
			text = fmt.Sprintf("%s →%d%% ·%s",
				formatBurnRate(p.SlopePctPerHour, unit), p.ProjectedPctAtReset, durString(*p.MinutesTo100Pct))
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
