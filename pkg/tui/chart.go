package tui

import (
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/NimbleMarkets/ntcharts/canvas/runes"
	"github.com/NimbleMarkets/ntcharts/linechart/timeserieslinechart"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// ZoomLevel maps a human label to a bucket duration and visual layout
// parameters. The chart's horizontal extent is no longer per-zoom — it
// spans from the earliest cached message to "now" at every zoom (see
// issue #53).
//
// BarWidth is the column count each bar occupies. BarGap is the empty
// column count between adjacent bars (no trailing gap after the last
// bar). The X-axis label slot is BarWidth cols, positioned over the
// bar itself (gaps stay blank). Tuning either is a single edit in
// ZoomLevels.
type ZoomLevel struct {
	Label    string
	Duration time.Duration
	BarWidth int
	BarGap   int
}

// CanvasWidth returns the total column count to render n bars at this
// zoom: n*BarWidth + (n-1)*BarGap. Returns 0 for n<=0. Shared by
// buildChart's caller (model.refreshChart) and the spring-frame path.
//
// BarWidth is clamped to ≥1 and BarGap to ≥0 so degenerate ZoomLevels
// values (typos, future tuning slips) can't produce a negative or
// stride-zero layout downstream — see stride() for the matching
// per-bar invariant used by renderXLabels, model.visibleBuckets,
// and model.setX.
func (z ZoomLevel) CanvasWidth(n int) int {
	if n <= 0 {
		return 0
	}
	return n*max(z.BarWidth, 1) + (n-1)*max(z.BarGap, 0)
}

// stride returns the per-bar column distance: BarWidth + BarGap with
// both terms defensively clamped (BarWidth≥1, BarGap≥0) so callers
// can divide by stride without a panic. Bar i starts at column
// i*stride; the bar itself occupies cols [i*stride, i*stride+BarWidth).
//
// Single source of truth for the per-bar invariant: any in-package
// site that converts bucket-index → column count routes through this.
// Renaming/removing this helper without updating all callers risks
// re-introducing the integer-divide panic class.
func (z ZoomLevel) stride() int {
	return max(z.BarWidth, 1) + max(z.BarGap, 0)
}

// columnToTime maps a viewport column to the wall-clock time at that
// column within the canvas's [from, to) range. Used pre-rebuild to
// snapshot the anchor; the (truncating) inverse is timeToColumn.
//
// Uses integer-second arithmetic with round-to-nearest so the
// round-trip columnToTime(col) -> timeToColumn(t) returns col exactly
// at canvasW values where the truncated form would drop by 1 (e.g.,
// 312/490*span over a 41-day range).
//
// Defensive: canvasW<=0 or to<=from collapse to the canvas origin so
// callers can use the result without nil checks. col is clamped to
// [0, canvasW] so out-of-range scroll state never escapes the helper.
func columnToTime(col, canvasW int, from, to time.Time) time.Time {
	if canvasW <= 0 || !to.After(from) {
		return from
	}
	if col <= 0 {
		return from
	}
	if col >= canvasW {
		return to
	}
	spanSec := int64(to.Sub(from) / time.Second)
	if spanSec <= 0 {
		return from
	}
	// Round-to-nearest: (a + b/2) / b. The +canvasW/2 bias makes this
	// the inverse of timeToColumn's truncating divide for exact
	// bucket-boundary inputs that would otherwise drift by 1 col.
	cw := int64(canvasW)
	offSec := (spanSec*int64(col) + cw/2) / cw
	return from.Add(time.Duration(offSec) * time.Second)
}

// timeToColumn maps a wall-clock time back to a viewport column in
// the canvas's [from, to) range. Used post-rebuild to restore the
// anchor; the inverse of columnToTime.
//
// Truncates (floor) rather than rounding so a mid-bucket wall-clock
// returns the column at the start of the bucket containing it. This
// matches the existing bar-mode semantics where BucketAlign(t, dur)
// snaps an anchor DOWN to the bucket it lives in (e.g., 09:45 at 1h
// zoom resolves to the 09:00 bucket, not the 10:00 one).
//
// Defensive: same clamping contract as columnToTime — out-of-range t
// snaps to the canvas edges; degenerate canvas returns 0.
func timeToColumn(t time.Time, canvasW int, from, to time.Time) int {
	if canvasW <= 0 || !to.After(from) {
		return 0
	}
	if !t.After(from) {
		return 0
	}
	if !t.Before(to) {
		return canvasW
	}
	spanSec := int64(to.Sub(from) / time.Second)
	if spanSec <= 0 {
		return 0
	}
	elapsedSec := int64(t.Sub(from) / time.Second)
	col := int(elapsedSec * int64(canvasW) / spanSec)
	if col < 0 {
		return 0
	}
	if col > canvasW {
		return canvasW
	}
	return col
}

// bucketCountInRange counts the bucket slots covering [from, to) at the
// given zoom duration. Matches cache.TokenBuckets / cache.CostBuckets
// return-length semantics:
//   - For sub-day durations, the count is int(to.Sub(from) / dur).
//   - For 24h, the count is the number of local-tz calendar days in
//     the range (DST-correct via AddDate(0,0,1)).
//
// Returns 0 for empty or reversed ranges.
func bucketCountInRange(from, to time.Time, dur time.Duration) int {
	if !to.After(from) {
		return 0
	}
	if dur == 24*time.Hour {
		n := 0
		for t := from; t.Before(to); t = t.AddDate(0, 0, 1) {
			n++
		}
		return n
	}
	return int(to.Sub(from) / dur)
}

// ZoomLevels are the available zoom steps, cycled with the z key.
// Order matters: pkg/tui/model.go indexes by position (zoomIdx).
var ZoomLevels = []ZoomLevel{
	{"15m", 15 * time.Minute, 1, 0},
	{"1h", time.Hour, 1, 0},
	{"24h", 24 * time.Hour, 10, 2},
}

// computeSpringSlice returns the slice start (bucket-index) and viewport
// xOffset (column-index) used by renderSpringFrame to reproduce the
// pre-spring viewport position. Pre-spring's viewport.SetXOffset(K*stride)
// is clamped to longestLineWidth-vpWidth at the right edge; when that
// clamp doesn't align to a stride boundary, the spring canvas must
// include bucket [start-1] as a leading bar and offset into it by the
// slack so the partial-bar / gap content matches.
//
// Defensive: springXOff is clamped to ≥0 so a future degenerate state
// (e.g. prevLongest < vpWidth mid-animation due to data shrinking)
// cannot produce a negative offset that downstream callers would have
// to handle.
func computeSpringSlice(start, prevLongest, vpWidth, stride int) (sliceStart, springXOff int) {
	desiredXOffset := start * stride
	actualXOffset := min(desiredXOffset, max(0, prevLongest-vpWidth))
	sliceStart = start
	if start >= 1 && actualXOffset < desiredXOffset {
		sliceStart = start - 1
		springXOff = actualXOffset - sliceStart*stride
	}
	if springXOff < 0 {
		springXOff = 0
	}
	return sliceStart, springXOff
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
// clock-aligned tick labels placed over each bucket's bar. Bar i starts
// at column i*(BarWidth+BarGap); the label is centered inside the bar
// itself (gaps stay blank). For BarWidth=1 / BarGap=0 the centering
// math falls back to col (labelW ≥ 3 > 1, so (1-labelW)/2 < 0). Labels
// that would overflow chartW on the right are dropped. Empty starts → "".
// colorMuted foreground throughout — Y axis labels are default fg so
// the eye distinguishes the two rows when they sit close.
func renderXLabels(starts []time.Time, chartW int, zoom ZoomLevel, now time.Time, order dateOrder) string {
	if chartW < 1 || len(starts) == 0 {
		return ""
	}
	bw := max(zoom.BarWidth, 1)
	stride := zoom.stride()
	row := make([]rune, chartW)
	for i := range row {
		row[i] = ' '
	}

	for i, t := range starts {
		col := i * stride
		if col >= chartW {
			break
		}
		label := formatXLabel(t, zoom, now, order)
		if label == "" {
			continue
		}
		labelW := lipgloss.Width(label)
		start := col + (bw-labelW)/2
		if start < 0 {
			start = col
		}
		if start+labelW > chartW {
			continue
		}
		for j, r := range []rune(label) {
			row[start+j] = r
		}
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
// stable across refreshes. At midnight, the 15m/1h zooms route through
// dateLabel(t, now, order) for a unified day-boundary stamp. At 24h,
// every bucket is a midnight so dateLabel runs unconditionally.
func formatXLabel(t time.Time, zoom ZoomLevel, now time.Time, order dateOrder) string {
	switch zoom.Label {
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
	case "24h":
		return dateLabel(t, now, order)
	}
	return ""
}

// chartUnit selects what `peak` and bar values represent. Used by
// formatUnitValue to pick the right Y-label format. Spring-animation
// rendering also reads this through Model.unitIdx.
type chartUnit int

const (
	chartUnitTokens    chartUnit = iota
	chartUnitCost
	chartUnitRemaining
	chartUnitCount // sentinel — cycle modulus
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

// formatTokenCount renders an int64 token count compactly with a k/M/G
// suffix, suitable for the Y label and other in-chart annotations.
// Always returns an integer label (no fractional digits). Pair with
// niceFloorFloat so the integer-rounded label exactly matches its row.
//
//	n <= 0       -> "0"
//	n < 1000     -> raw integer
//	n < 1e6      -> rounded thousands with "k" (e.g. "75k", "100k")
//	n < 1e9      -> rounded millions with "M" (e.g. "50M", "1M")
//	n >= 1e9     -> rounded billions with "G" (e.g. "1G", "9G")
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
	if n < 1_000_000_000 {
		return strconv.FormatFloat(float64(n)/1_000_000, 'f', 0, 64) + "M"
	}
	return strconv.FormatFloat(float64(n)/1_000_000_000, 'f', 0, 64) + "G"
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
		if v < 1_000_000_000 {
			return "$" + strconv.FormatFloat(v/1_000_000, 'f', 0, 64) + "M"
		}
		return "$" + strconv.FormatFloat(v/1_000_000_000, 'f', 0, 64) + "G"
	default: // chartUnitTokens
		// Reuse formatTokenCount's exact behaviour by casting to int64
		// after the niceFloorFloat path has already snapped to an integer.
		return formatTokenCount(int64(v))
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
// unit selects the bar color (Blue for chartUnitTokens, Amber for
// chartUnitCost). It is also read by the Y-label overlay path in
// Model.View() — passed through here for that reason too.
func buildChart(values []float64, starts []time.Time, peak float64,
	chartW, chartH int, now time.Time, zoom ZoomLevel, unit chartUnit, order dateOrder) string {
	start := time.Now()
	if chartH < 1 {
		chartH = 1
	}

	barsH := chartH
	showXLabels := chartH >= 6
	if showXLabels {
		barsH = chartH - 1
	}

	// unit-keyed bar color — see colorChartTokens / colorChartCost in style.go.
	// Bucket height is encoded by the bar's height; color encodes which axis
	// is plotted (tokens vs cost), independent of the visible peak. issue #162.
	var barColor lipgloss.TerminalColor = colorChartTokens
	if unit == chartUnitCost {
		barColor = colorChartCost
	}

	bars := make([]barchart.BarData, len(values))
	for i, v := range values {
		bars[i] = barchart.BarData{
			Values: []barchart.BarValue{
				{
					Value: v,
					Style: lipgloss.NewStyle().Foreground(barColor).Background(barColor),
				},
			},
		}
	}

	maxValue := peak
	if maxValue == 0 {
		maxValue = 1 // ntcharts requires non-zero max; bars will all be empty anyway
	}
	bc := barchart.New(chartW, barsH,
		barchart.WithBarGap(zoom.BarGap),
		barchart.WithNoAxis(),
		barchart.WithMaxValue(maxValue),
		barchart.WithNoAutoBarWidth(),
		barchart.WithBarWidth(zoom.BarWidth),
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

// isLineMode returns true when the given chartUnit renders as a line chart
// (remaining mode) rather than a bar chart (tokens/cost).
func isLineMode(u chartUnit) bool {
	return u == chartUnitRemaining
}

// buildLineChart renders two remaining-quota series (5h green, 7d
// purple) as a dotted braille trail on a timeserieslinechart canvas.
// Time values are mapped natively by ntcharts via SetViewTimeRange.
// Y values are remaining fractions: 1 - Pct/100, in [0, 1].
//
// Empty input renders a flat baseline at 1.0 (100% headroom — no-quota
// fallback) per dataset.
//
// X-axis labels are rendered by ccpulse's renderXLabels and joined
// below the chart body; ntcharts' built-in axes are suppressed via
// SetXStep(0)/SetYStep(0). The left-edge Y labels (100%/50%/0% +
// 5h/7d legend) are spliced in by overlayYTicks in the caller.
//
// Implementation note: ntcharts' line-chart Style API splits "rune set"
// (SetLineStyle / SetDataSetLineStyle, taking runes.LineStyle) from
// "lipgloss color" (SetStyle / SetDataSetStyle, taking lipgloss.Style).
// They are SEPARATE setters — passing both via a hypothetical
// SetStyles(line, color) would fail to compile against ntcharts v0.5.1.
func buildLineChart(pts5h, pts7d []cache.UtilizationPoint,
	from, to time.Time, chartW, chartH int,
	now time.Time, zoom ZoomLevel, order dateOrder) string {

	logStart := time.Now()
	if chartH < 1 {
		chartH = 1
	}

	barsH := chartH
	showXLabels := chartH >= 6
	if showXLabels {
		barsH = chartH - 1
	}

	tslc := timeserieslinechart.New(chartW, barsH,
		timeserieslinechart.WithYRange(0, 1.0),
		timeserieslinechart.WithTimeRange(from, to),
	)
	tslc.SetXStep(0)
	tslc.SetYStep(0)
	// must come after Set{X,Y}Step — SetViewTimeRange triggers rescaleData
	// on the default dataset, anchoring it to the post-step geometry so it
	// shares scale with later PushDataSet datasets (issue #194).
	tslc.SetViewTimeRange(from, to)

	// 5h dataset (default).
	tslc.SetLineStyle(runes.ThinLineStyle)
	tslc.SetStyle(lipgloss.NewStyle().Foreground(colorChartRemaining5h))
	if len(pts5h) == 0 {
		tslc.Push(timeserieslinechart.TimePoint{Time: from, Value: 1.0})
		tslc.Push(timeserieslinechart.TimePoint{Time: to, Value: 1.0})
	} else {
		for _, p := range pts5h {
			tslc.Push(timeserieslinechart.TimePoint{
				Time:  p.At,
				Value: math.Max(0, 1.0-p.Pct/100.0),
			})
		}
	}

	// 7d dataset.
	const ds7d = "7d"
	tslc.SetDataSetLineStyle(ds7d, runes.ThinLineStyle)
	tslc.SetDataSetStyle(ds7d, lipgloss.NewStyle().Foreground(colorChartRemaining7d))
	if len(pts7d) == 0 {
		tslc.PushDataSet(ds7d, timeserieslinechart.TimePoint{Time: from, Value: 1.0})
		tslc.PushDataSet(ds7d, timeserieslinechart.TimePoint{Time: to, Value: 1.0})
	} else {
		for _, p := range pts7d {
			tslc.PushDataSet(ds7d, timeserieslinechart.TimePoint{
				Time:  p.At,
				Value: math.Max(0, 1.0-p.Pct/100.0),
			})
		}
	}

	tslc.DrawBrailleAll()
	body := tslc.View()

	if showXLabels {
		// Synthesise bucket starts for x-axis labels. The line chart spans
		// [from, to) continuously but the label cadence comes from the zoom
		// duration so it matches bar-chart mode.
		dur := zoom.Duration
		n := max(int(to.Sub(from)/dur)+1, 1)
		labelStarts := make([]time.Time, 0, n)
		for t := from; t.Before(to); t = t.Add(dur) {
			labelStarts = append(labelStarts, t)
		}
		body = lipgloss.JoinVertical(lipgloss.Left, body, renderXLabels(labelStarts, chartW, zoom, now, order))
	}

	slog.Debug("tui.buildLineChart",
		"dur_ms", time.Since(logStart).Milliseconds(),
		"pts5h", len(pts5h),
		"pts7d", len(pts7d),
		"chartW", chartW,
		"chartH", chartH)
	return body
}

// overlayYTicks splices fixed "100%", "50%", "0%" labels and a colored
// legend ("5h", "7d") into the left edge of an already-rendered line
// chart string. Operates ANSI-aware on the post-scroll viewport output
// so labels stay pinned to the viewport's left edge regardless of
// horizontal scroll position.
//
// fade ∈ [0, 1] controls label visibility via labelFadeStyle. fade <= 0
// returns body unchanged.
func overlayYTicks(body string, chartH int, fade float64) string {
	if chartH < 5 || body == "" || fade <= 0 {
		return body
	}
	barsH := chartH
	if chartH >= 6 {
		barsH = chartH - 1
	}

	lines := strings.Split(body, "\n")
	style := labelFadeStyle(fade)

	type tick struct {
		row   int
		label string
	}
	ticks := []tick{
		{0, "100%"},
		{barsH / 2, " 50%"},
		{barsH - 1, "  0%"},
	}
	for _, tk := range ticks {
		if tk.row >= len(lines) {
			continue
		}
		rendered := style.Render(tk.label)
		labelW := lipgloss.Width(rendered)
		lines[tk.row] = ansi.TruncateLeft(lines[tk.row], labelW, rendered)
	}

	// Legend: colored "5h" and "7d" labels on rows 1 and 2 (below 100% tick).
	legendItems := []struct {
		row   int
		label string
		color lipgloss.TerminalColor
	}{
		{1, " 5h", colorChartRemaining5h},
		{2, " 7d", colorChartRemaining7d},
	}
	for _, li := range legendItems {
		if li.row >= len(lines) {
			continue
		}
		rendered := labelFadeStyle(fade).Foreground(li.color).Render(li.label)
		labelW := lipgloss.Width(rendered)
		lines[li.row] = ansi.TruncateLeft(lines[li.row], labelW, rendered)
	}

	return strings.Join(lines, "\n")
}
