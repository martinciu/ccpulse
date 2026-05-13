package tui

import (
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// ZoomLevel maps a human label to a bucket duration. The chart's
// horizontal extent is no longer per-zoom — it spans from the earliest
// cached message to "now" at every zoom (see issue #53).
type ZoomLevel struct {
	Label    string
	Duration time.Duration
}

// ZoomLevels are the available zoom steps, cycled with the z key.
var ZoomLevels = []ZoomLevel{
	{"5m", 5 * time.Minute},
	{"15m", 15 * time.Minute},
	{"1h", time.Hour},
}

// overlayYLabel splices `formatUnitValue(niceFloorFloat(peak), unit)` in
// the chosen fade style into the niceFloorFloat(peak) row of an already-rendered
// chart string, replacing the first 5 visible columns of that row.
// Operates ANSI-aware on the post-scroll viewport output so the label
// stays pinned to the viewport's left edge regardless of horizontal
// scroll position (issue #132) — applied in Model.View() after
// m.viewport.View(), not inside buildChart.
//
// fade ∈ [0, 1] selects the label's discrete fade stop via
// labelFadeStyle. fade <= 0 short-circuits and returns body unchanged
// (the empty-moment frame of the two-phase unit-toggle animation,
// issue #136). At steady state Model.View passes fade=1.0.
//
// Other early-returns: peak <= 0, chartH < 6, niceFloorFloat(peak) == 0,
// or body == "" all return body unchanged.
func overlayYLabel(body string, peak float64, unit chartUnit, chartH int, fade float64) string {
	if peak <= 0 || chartH < 6 || body == "" || fade <= 0 {
		return body
	}
	tick := niceFloorFloat(peak)
	if tick <= 0 {
		return body
	}
	barsH := chartH - 1
	row := barsH - int(math.Round(tick/peak*float64(barsH)))
	row = max(row, 0)
	row = min(row, barsH-1)

	label := labelFadeStyle(fade).Render(formatUnitValue(tick, unit))
	labelW := lipgloss.Width(label)

	lines := strings.Split(body, "\n")
	if row >= len(lines) {
		return body
	}
	lines[row] = ansi.TruncateLeft(lines[row], labelW, label)
	return strings.Join(lines, "\n")
}

// renderXLabels returns a 1-row string of width chartW containing
// clock-aligned tick labels placed at matching bucket columns, with
// "▼ now" right-aligned at the rightmost columns (always wins on
// collision). Later labels overwrite earlier ones; labels that would
// overflow chartW on the right are dropped. Empty starts → "".
// colorMuted foreground throughout — Y axis labels are default fg so the eye
// distinguishes the two rows when they sit close together.
func renderXLabels(starts []time.Time, chartW int, zoom ZoomLevel, now time.Time, order dateOrder) string {
	if chartW < 1 || len(starts) == 0 {
		return ""
	}
	row := make([]rune, chartW)
	for i := range row {
		row[i] = ' '
	}

	for i, t := range starts {
		if i >= chartW {
			break
		}
		label := formatXLabel(t, zoom, now, order)
		if label == "" {
			continue
		}
		labelRunes := []rune(label)
		if i+lipgloss.Width(label) > chartW {
			continue
		}
		for j, r := range labelRunes {
			row[i+j] = r
		}
	}

	const nowText = "▼ now"
	nowRunes := []rune(nowText)
	nowW := lipgloss.Width(nowText)
	switch {
	case nowW <= chartW:
		start := chartW - nowW
		for j, r := range nowRunes {
			row[start+j] = r
		}
	case chartW >= 1:
		row[chartW-1] = '▼'
	}

	return dimStyle.Render(string(row))
}

// dateLabel renders the day-boundary stamp shown at midnight slots
// across all three zooms: weekday short ("Mon") for buckets within
// the past 7 days, locale-aware short date ("05/09" or "09/05") for
// older buckets. Both forms fit the 5-col label slot.
//
// Cheap path per issue #145: weekday/month names stay ASCII English;
// only date order is locale-aware. Native-language names depend on
// the wide-rune-aware renderXLabels writer tracked in #130.
//
// t.Format reads t's zone fields. Buckets are persisted as UTC in
// pkg/cache, so the chart renders UTC weekdays/dates — a user in a
// non-UTC zone may see a label off-by-one near local midnight.
func dateLabel(t, now time.Time, order dateOrder) string {
	if t.After(now.AddDate(0, 0, -7)) {
		return t.Format("Mon")
	}
	if order == dateOrderMonthFirst {
		return t.Format("01/02")
	}
	return t.Format("02/01")
}

// formatXLabel returns the X-axis tick label for bucket time t at the
// given zoom; "" if t is not on a label boundary. Cadence is clock-
// aligned (anchored to hour / 3-hour / day marks) so positions are
// stable across refreshes. At midnight, all three zooms route through
// dateLabel(t, now, order) for a unified day-boundary stamp.
func formatXLabel(t time.Time, zoom ZoomLevel, now time.Time, order dateOrder) string {
	switch zoom.Label {
	case "5m":
		if t.Minute() == 0 {
			if t.Hour() == 0 {
				return dateLabel(t, now, order)
			}
			return t.Format("15:04")
		}
	case "15m":
		if t.Hour()%3 == 0 && t.Minute() == 0 {
			if t.Hour() == 0 {
				return dateLabel(t, now, order)
			}
			return t.Format("15:04")
		}
	case "1h":
		if t.Hour() == 0 && t.Minute() == 0 {
			return dateLabel(t, now, order)
		}
	}
	return ""
}

// chartUnit selects what `peak` and bar values represent. Used by
// formatUnitValue to pick the right Y-label format. Spring-animation
// rendering also reads this through Model.unitIdx.
type chartUnit int

const (
	chartUnitTokens chartUnit = iota
	chartUnitCost
)

// niceFloorFloat returns the largest "nice" value <= peak from the
// sequence {1, 2, 3, 5, 7} × 10ᵏ, where k may be negative to support
// sub-1 peaks (cost-mode shows e.g. $0.45 buckets). The integer-only
// mantissa set keeps formatTokenCount labels integer-shaped (no
// "7.5k" / "2.5M"). Returns 0 when peak <= 0 so callers can guard
// the overlay write.
func niceFloorFloat(peak float64) float64 {
	if peak <= 0 {
		return 0
	}
	mag := math.Pow10(int(math.Floor(math.Log10(peak))))
	norm := peak / mag
	var nice float64
	switch {
	case norm >= 7.0:
		nice = 7.0
	case norm >= 5.0:
		nice = 5.0
	case norm >= 3.0:
		nice = 3.0
	case norm >= 2.0:
		nice = 2.0
	default:
		nice = 1.0
	}
	return nice * mag
}

// formatTokenCount renders an int64 token count compactly with a k/M
// suffix, suitable for the Y label and other in-chart annotations.
// Always returns an integer label (no fractional digits). Pair with
// niceFloorFloat so the integer-rounded label exactly matches its row.
//
//	n <= 0     -> "0"
//	n < 1000   -> raw integer
//	n < 1e6    -> rounded thousands with "k" (e.g. "75k", "100k")
//	n >= 1e6   -> rounded millions with "M" (e.g. "50M", "1M")
func formatTokenCount(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return strconv.FormatFloat(float64(n)/1000, 'f', 0, 64) + "k"
	}
	return strconv.FormatFloat(float64(n)/1_000_000, 'f', 0, 64) + "M"
}

// formatUnitValue renders v in the active unit's compact Y-label form.
// Tokens use the existing k/M suffix shape (mirroring formatTokenCount).
// Cost prefixes "$" and keeps two decimals only for sub-dollar values
// (e.g. "$0.45"); otherwise integer dollars with a k/M suffix above
// 1000. The 5-col Y-label slot is respected — "$0.45" is exactly 5 cols.
func formatUnitValue(v float64, unit chartUnit) string {
	switch unit {
	case chartUnitCost:
		if v <= 0 {
			return "$0"
		}
		if v < 1 {
			return "$" + strconv.FormatFloat(v, 'f', 2, 64)
		}
		if v < 1000 {
			return "$" + strconv.FormatFloat(v, 'f', 0, 64)
		}
		if v < 1_000_000 {
			return "$" + strconv.FormatFloat(v/1000, 'f', 0, 64) + "k"
		}
		return "$" + strconv.FormatFloat(v/1_000_000, 'f', 0, 64) + "M"
	default: // chartUnitTokens
		// Reuse formatTokenCount's exact behaviour by casting to int64
		// after the niceFloorFloat path has already snapped to an integer.
		return formatTokenCount(int64(v))
	}
}

// heatColor returns the adaptive-palette token on a safe → watch → danger
// ramp based on ratio (0.0–1.0) of a bucket's tokens relative to the peak
// bucket. Return type is lipgloss.TerminalColor (the interface satisfied
// by lipgloss.AdaptiveColor) so callers can assign without a type assertion.
func heatColor(ratio float64) lipgloss.TerminalColor {
	switch {
	case ratio >= 0.66:
		return colorDanger
	case ratio >= 0.33:
		return colorWatch
	default:
		return colorSafe
	}
}

// buildChart renders the viewport content from a parallel
// (values, starts) pair at the given zoom: bars in the top chartH-1
// rows (or all chartH if chartH < 6), plus an X-axis tick label row
// at the bottom. Bars are scaled to peak so the tallest bar fills
// the canvas; barchart.WithNoAxis() reclaims the row ntcharts
// otherwise reserves for its (empty) internal axis.
//
// values, starts, and the resulting bar count are 1:1 — values[i]
// is plotted at column i, with starts[i] feeding the X-axis label.
// peak is the normalisation reference; pass max(values) for steady
// state, or 1.0 during ratio-space animation (when values are
// already normalised to [0, 1]).
//
// unit is read by the Y-label overlay path in Model.View() — not
// used directly here; passed through for symmetry with overlayYLabel.
func buildChart(values []float64, starts []time.Time, peak float64,
	chartW, chartH int, now time.Time, zoom ZoomLevel, unit chartUnit, order dateOrder) string {
	_ = unit // unit is consumed by overlayYLabel in Model.View(), not by buildChart itself
	start := time.Now()
	if chartH < 1 {
		chartH = 1
	}

	barsH := chartH
	showXLabels := chartH >= 6
	if showXLabels {
		barsH = chartH - 1
	}

	bars := make([]barchart.BarData, len(values))
	for i, v := range values {
		ratio := float64(0)
		if peak > 0 {
			ratio = v / peak
		}
		c := heatColor(ratio)
		bars[i] = barchart.BarData{
			Values: []barchart.BarValue{
				{
					Value: v,
					Style: lipgloss.NewStyle().Foreground(c).Background(c),
				},
			},
		}
	}

	maxValue := peak
	if maxValue == 0 {
		maxValue = 1 // ntcharts requires non-zero max; bars will all be empty anyway
	}
	bc := barchart.New(chartW, barsH,
		barchart.WithBarGap(0),
		barchart.WithNoAxis(),
		barchart.WithMaxValue(maxValue),
	)
	bc.PushAll(bars)
	bc.Draw()
	body := bc.View()

	if showXLabels {
		body = lipgloss.JoinVertical(lipgloss.Left, body, renderXLabels(starts, chartW, zoom, now, order))
	}

	slog.Debug("tui.buildChart",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(values),
		"chartW", chartW,
		"chartH", chartH,
		"peak", peak)
	return body
}
