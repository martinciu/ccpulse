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
	// ScrollStep is the per-keypress scroll distance in BUCKETS for ←/→.
	// 1 at 24h (one day per press); 3 at the finer zooms.
	ScrollStep int
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
// given zoom duration. Matches cache.IOTokenBuckets / cache.CostBuckets
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

// paddedFrom returns `to` walked left by n buckets at the given zoom — the
// bucket-aligned start such that [paddedFrom, to) spans n buckets. refreshChart
// uses it to guarantee the chart window is at least as wide as the viewport so
// sparse data renders flush-right with a full-width x-axis (#300). The 24h zoom
// walks back whole local-tz days (DST-correct via AddDate); sub-day zooms
// subtract n*Duration. Returns `to` unchanged for n<=0.
func paddedFrom(to time.Time, zoom ZoomLevel, n int) time.Time {
	if n <= 0 {
		return to
	}
	if zoom.Duration == 24*time.Hour {
		return to.AddDate(0, 0, -n)
	}
	return to.Add(-time.Duration(n) * zoom.Duration)
}

// ZoomLevels are the available zoom steps, cycled with the z key.
// Order matters: pkg/tui/model.go indexes by position (zoomIdx).
var ZoomLevels = []ZoomLevel{
	{"15m", 15 * time.Minute, 1, 0, horizontalScrollStep},
	{"1h", time.Hour, 1, 0, horizontalScrollStep},
	{"24h", 24 * time.Hour, 10, 2, 1},
}

// computeSpringSlice returns the slice start (bucket-index) and viewport
// xOffset (column-index) for the windowed bar render, keeping the chart's
// right edge flush at EVERY scroll position (#306).
//
// nv whole bars cover nv*stride-gap columns, so when the viewport width is
// not an exact multiple of the stride there are
// slack = (vpWidth+gap) mod stride leftover columns. To keep the right edge
// flush, the slack is pushed onto a partial LEADING bucket: include bucket
// [start-1] and offset into it by stride-slack. The nv fully-visible buckets
// [start, start+nv-1] are unchanged — only the framing shifts left by slack.
//
// Two boundaries fall out without special-casing:
//   - start==0 (oldest edge): no bucket to the left, so the window is
//     left-aligned (xOff==0) and the slack lands on the right — the one
//     place a right gap is expected, where there is no older data to fill it.
//   - slack==0 (vpWidth an exact stride multiple, e.g. every 15m/1h zoom
//     where stride==1): no partial bucket is needed.
//
// Callers must pass a start already clamped to [0, len(values)-nv] (setX
// guarantees this); the helper does not re-clamp against the canvas.
//
// stride is clamped to >=1 and gap to >=0 so a degenerate ZoomLevels literal
// cannot divide by zero or produce a negative offset.
func computeSpringSlice(start, vpWidth, stride, gap int) (sliceStart, springXOff int) {
	if stride < 1 {
		stride = 1
	}
	if gap < 0 {
		gap = 0
	}
	slack := (vpWidth + gap) % stride
	if start >= 1 && slack > 0 {
		return start - 1, stride - slack
	}
	return start, 0
}

// slicePointsInRange returns the sub-slice of pts that falls within
// [from, to], padded by one point on each side if available. The
// padding preserves cross-boundary line continuity — without it,
// timeserieslinechart would have no preceding/following anchor to draw
// the segment that crosses the visible edge.
//
// Empty input returns nil. If no points fall inside [from, to],
// returns the single closest point as a baseline anchor (the first
// point if pts are all after to, the last point if all before from).
//
// pts is assumed to be sorted by At ascending — true for cache.IOTokenBuckets
// and status.Compute output. Linear scan; binary search is unnecessary
// at the n=O(thousands) sizes seen by the line branch.
func slicePointsInRange(pts []cache.UtilizationPoint, from, to time.Time) []cache.UtilizationPoint {
	if len(pts) == 0 {
		return nil
	}
	startIdx := 0
	for startIdx < len(pts) && pts[startIdx].At.Before(from) {
		startIdx++
	}
	endIdx := len(pts)
	for endIdx > 0 && pts[endIdx-1].At.After(to) {
		endIdx--
	}

	// Empty visible range: return the closest single point as a baseline anchor.
	if startIdx >= endIdx {
		if startIdx >= len(pts) {
			return pts[len(pts)-1:]
		}
		return pts[startIdx : startIdx+1]
	}

	// Pad by one on each side to preserve cross-boundary line continuity.
	if startIdx > 0 {
		startIdx--
	}
	if endIdx < len(pts) {
		endIdx++
	}
	return pts[startIdx:endIdx]
}

// niceCeilingFloat returns the smallest "nice" value >= peak from the
// sequence {1, 2, 3, 5, 7, 10} × 10^k. The 10 stop is the same value
// as 1 × 10^(k+1) — listing it explicitly keeps the switch flat.
// Companion to niceFloorFloat: callers use niceCeiling for the chart's
// Y-axis top (so labels live on exact rows) and niceFloor when they
// want the largest nice value not exceeding the data.
//
// Returns 0 when peak <= 0 so callers can guard the overlay write.
func niceCeilingFloat(peak float64) float64 {
	if peak <= 0 {
		return 0
	}
	mag := math.Pow10(int(math.Floor(math.Log10(peak))))
	norm := peak / mag
	var nice float64
	switch {
	case norm <= 1.0:
		nice = 1.0
	case norm <= 2.0:
		nice = 2.0
	case norm <= 3.0:
		nice = 3.0
	case norm <= 5.0:
		nice = 5.0
	case norm <= 7.0:
		nice = 7.0
	default:
		nice = 10.0
	}
	return nice * mag
}

// yLabelSlotW is the fixed visible-column width reserved for the
// bar-chart Y-axis label slot (cost / tokens). overlayYLabel splices
// both max and midpoint labels right-aligned in this slot so they
// share a stable left-edge column across refreshes regardless of how
// formatUnitValue's output width shifts (e.g. peak crossing $1k →
// "$700" 4 cols vs "$1k" 3 cols). Matches the line chart's manual
// "100%" / " 50%" pattern (4 cols there; one wider here for the "$"
// prefix and sub-1 "$0.45"-style labels).
const yLabelSlotW = 5

// padYLabel right-aligns s in a yLabelSlotW-wide column with leading
// spaces. Returns s unchanged when its visible width is already >=
// slot width.
func padYLabel(s string) string {
	if w := lipgloss.Width(s); w < yLabelSlotW {
		return strings.Repeat(" ", yLabelSlotW-w) + s
	}
	return s
}

// yLabelMidFloor returns the smallest mid value that formatUnitValue
// will render as a non-zero label for the given unit. Used by
// overlayYLabel to skip the midpoint when FP-rounding inside
// formatUnitValue would otherwise collapse the value to "$0.00" / "0".
//
// niceCeilingFloat returns positive output for positive peak, but
// halving it crosses per-unit format-precision floors:
//
//	chartUnitCost   — 2-decimal FormatFloat; mid in [0, 0.005)
//	                  rounds to "$0.00".
//	chartUnitTokens — int64 cast inside formatTokenCount; mid in
//	                  [0, 1) drops to 0 and renders as "0".
//
// Returns 0 for unknown units (defensive — no skip).
func yLabelMidFloor(unit chartUnit) float64 {
	switch unit {
	case chartUnitCost:
		return 0.005
	case chartUnitTokens:
		return 1
	}
	return 0
}

// overlayYLabel splices two right-aligned labels into the bar-chart
// canvas: the max label (= niceCeilingFloat(peak)) at row 0 and the
// midpoint label (= ceiling/2) at row barsH/2. Both occupy a fixed
// yLabelSlotW-wide column at the viewport's left edge, replacing the
// underlying bar content via ansi.TruncateLeft. Operates ANSI-aware
// on the post-scroll viewport output so the labels stay pinned to the
// viewport's left edge regardless of horizontal scroll position
// (issue #132) — applied in Model.View() after m.viewport.View(), not
// inside buildChart.
//
// fade ∈ [0, 1] selects the labels' discrete fade stop via
// labelFadeStyle. fade <= 0 short-circuits and returns body unchanged
// (the empty-moment frame of the two-phase unit-toggle animation,
// issue #136). At steady state Model.View passes fade=1.0.
//
// Skip semantics:
//
//	peak <= 0 || chartH < 6 || body == "" || fade <= 0:
//	  return body unchanged (top-of-function early return).
//	niceCeilingFloat(peak) <= 0: defensive — return body unchanged.
//	mid < yLabelMidFloor(unit): skip the mid splice (formatUnitValue
//	  would render the value as "$0.00" / "0"); render max only.
//	midRow >= len(lines): defensive — skip mid only.
//
// max and mid share labelFadeStyle(fade) — one style instance, two
// renders, same allocation pattern as the prior single-label path.
func overlayYLabel(body string, peak float64, unit chartUnit, chartH int, fade float64) string {
	if peak <= 0 || chartH < 6 || body == "" || fade <= 0 {
		return body
	}
	top := niceCeilingFloat(peak)
	if top <= 0 {
		return body
	}

	barsH := chartH - 1
	midRow := barsH / 2

	lines := strings.Split(body, "\n")
	if len(lines) < 1 {
		return body
	}

	style := labelFadeStyle(fade)
	maxLabel := style.Render(padYLabel(formatUnitValue(top, unit)))
	lines[0] = ansi.TruncateLeft(lines[0], yLabelSlotW, maxLabel)

	mid := top / 2
	if mid >= yLabelMidFloor(unit) && midRow < len(lines) {
		midLabel := style.Render(padYLabel(formatUnitValue(mid, unit)))
		lines[midRow] = ansi.TruncateLeft(lines[midRow], yLabelSlotW, midLabel)
	}

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

const (
	// barLabelMinWidth gates in-bar numbers to wide-enough zooms (#308). 24h
	// has BarWidth=10; 15m/1h have BarWidth=1 and never qualify. Using the
	// width (not a "24h" string match) auto-extends to any future wide zoom.
	barLabelMinWidth = 3
	// barLabelMinRows is the minimum bar height (in rows) to host a number;
	// shorter bars stay clean colored stubs (#308).
	barLabelMinRows = 2
)

// barLabelStyle returns the knockout style for in-bar numbers over the active
// unit's bar color (#308): dark/light adaptive foreground on the bar's own
// background, so the digits read as cut into the bar.
func barLabelStyle(unit chartUnit) lipgloss.Style {
	var bg lipgloss.TerminalColor = colorChartTokens
	if unit == chartUnitCost {
		bg = colorChartCost
	}
	return lipgloss.NewStyle().Foreground(colorBarLabel).Background(bg)
}

// spliceLabel overwrites the cells [start, start+width) of line with the
// already-styled label, preserving the cells on either side. ANSI-aware via
// x/ansi: Truncate keeps the left cells, TruncateLeft keeps the trailing ones.
func spliceLabel(line string, start, width int, label string) string {
	left := ansi.Truncate(line, start, "")
	right := ansi.TruncateLeft(line, start+width, "")
	return left + label + right
}

// overlayBarLabels splices each bucket's compact number onto the top fill row
// of its bar, for the steady-state 24h bar chart (#308). texts is 1:1 with the
// bars in the rendered slice; texts[i]=="" skips bar i. The labels must already
// be styled (see barLabelStyle).
//
// Placement is read directly off the rendered body: for each bar it scans the
// bar's center column from the top to find the first fill row, so the label
// always lands on the bar's visual top and can never drift from ntcharts' own
// height rounding. This also means the same function works on a future
// animating body (bar tops mid-grow) — the seam for the deferred animation card.
//
// Returns body unchanged when the zoom is too narrow (BarWidth <
// barLabelMinWidth — i.e. 15m/1h), body is empty, or texts is empty. Per-bar
// skips: empty text, a bar shorter than barLabelMinRows, a label wider than
// BarWidth, or a label that would overflow chartW on the right.
//
// barsH is the count of bar rows (the X-label row, if any, sits below and is
// never written). Called from renderWindow only — renderSpringFrame does not
// call it, so numbers are absent during animation.
func overlayBarLabels(body string, texts []string, barsH, chartW int, zoom ZoomLevel) string {
	if zoom.BarWidth < barLabelMinWidth || body == "" || len(texts) == 0 {
		return body
	}
	bw := max(zoom.BarWidth, 1)
	stride := zoom.stride()

	lines := strings.Split(body, "\n")
	if barsH > len(lines) {
		barsH = len(lines)
	}
	if barsH < 1 {
		return body
	}
	// Strip the bar rows once for column scanning (ANSI-aware). Chart cells
	// are width-1 (block runes / spaces), so rune index == visual column.
	stripped := make([][]rune, barsH)
	for r := 0; r < barsH; r++ {
		stripped[r] = []rune(ansi.Strip(lines[r]))
	}

	for i, text := range texts {
		if text == "" {
			continue
		}
		col := i * stride
		if col >= chartW {
			break
		}
		cc := col + bw/2 // bar center column, used to probe height
		top := -1
		for r := 0; r < barsH; r++ {
			if cc < len(stripped[r]) && stripped[r][cc] != ' ' {
				top = r
				break
			}
		}
		if top < 0 {
			continue // zero-height bar — nothing to label
		}
		if barsH-top < barLabelMinRows {
			continue // too short (#308)
		}
		labelW := lipgloss.Width(text)
		if labelW > bw {
			continue // would exceed the bar
		}
		start := col + (bw-labelW)/2
		if start < 0 {
			start = col
		}
		if start+labelW > chartW {
			continue // would overflow the right edge
		}
		lines[top] = spliceLabel(lines[top], start, labelW, text)
	}
	return strings.Join(lines, "\n")
}

// dateLabel renders the day-boundary stamp shown at midnight slots
// across all three zooms: weekday short ("Mon") for buckets within the
// past 7 days, locale-aware short date ("05/09" or "09/05") for older
// buckets. Both forms fit the 5-col label slot.
//
// Cheap path per issue #145: weekday/month names stay ASCII English;
// only date order is locale-aware. Native-language names depend on the
// wide-rune-aware renderXLabels writer tracked in #130.
//
// The bucket time is rendered in now.Location() — time.Local in
// production, since every caller passes time.Now() — so weekday/date
// stamps match the user's wall clock on all three zooms (issue #253).
// dateLabel is reached only via formatXLabel, which already passes a
// localized time; re-applying .In here is a no-op for that path and
// keeps dateLabel correct when called directly.
func dateLabel(t, now time.Time, order dateOrder) string {
	t = t.In(now.Location())
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
// dateLabel for a unified day-boundary stamp. At 24h, every bucket is a
// midnight so dateLabel runs unconditionally.
//
// t is converted to now.Location() (= time.Local in production) before
// the cadence tests and formatting, so all three zooms agree on the
// user's timezone (issue #253). 15m/1h buckets stay UTC-aligned in
// pkg/cache (Approach A, display-only): for whole-hour-offset zones the
// UTC boundaries coincide with local hour marks, so bars sit correctly
// and labels read local. Sub-hour-offset zones (e.g. India +5:30) keep
// UTC-aligned bars, so 1h-zoom hour ticks may not land on a local hour
// mark — a documented limitation; local-aligned bucketing (Approach B)
// would be needed to fix it.
func formatXLabel(t time.Time, zoom ZoomLevel, now time.Time, order dateOrder) string {
	tl := t.In(now.Location())
	switch zoom.Label {
	case "15m":
		if tl.Hour()%3 == 0 && tl.Minute() == 0 {
			if tl.Hour() == 0 {
				return dateLabel(tl, now, order)
			}
			return tl.Format("15:04")
		}
	case "1h":
		if tl.Hour() == 0 && tl.Minute() == 0 {
			return dateLabel(tl, now, order)
		}
	case "24h":
		return dateLabel(tl, now, order)
	}
	return ""
}

// chartUnit selects what `peak` and bar values represent. Used by
// formatUnitValue to pick the right Y-label format. Spring-animation
// rendering also reads this through Model.unitIdx.
//
// Cost is declared first so the zero-value default of Model.unitIdx (0)
// renders the cost histogram on launch; the `u` cycle then advances
// cost → tokens → remaining → cost via (unitIdx+1) % chartUnitCount.
// Resets to cost on every launch — no persistence (see issue #209).
type chartUnit int

const (
	chartUnitCost      chartUnit = iota
	chartUnitTokens
	chartUnitRemaining
	chartUnitCount // sentinel — cycle modulus
)

// String returns the unit's short name, used for table-test subtest names.
func (u chartUnit) String() string {
	switch u {
	case chartUnitCost:
		return "cost"
	case chartUnitTokens:
		return "tokens"
	case chartUnitRemaining:
		return "remaining"
	default:
		return "unknown"
	}
}

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
// niceCeilingFloat so the integer-rounded label exactly matches its row.
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
		// Reuse formatTokenCount's exact behaviour by casting to int64.
		return formatTokenCount(int64(v))
	}
}

// compactSuffix indexes magnitude suffixes for in-bar numbers (#308).
var compactSuffix = [...]string{"", "k", "M", "G"}

// scaleCompact reduces v (>= 0) to a mantissa and a compactSuffix index by
// repeatedly dividing by 1000. The mantissa is in [0, 1000) before the
// caller's rounding.
func scaleCompact(v float64) (float64, int) {
	exp := 0
	for v >= 1000 && exp < len(compactSuffix)-1 {
		v /= 1000
		exp++
	}
	return v, exp
}

// roundCompact rounds mantissa to dec decimals at the given exp, carrying to
// the next magnitude when rounding reaches 1000 (e.g. 999.6 with dec=0 -> "1"
// at exp+1). Returns the formatted mantissa (trailing ".0" trimmed) and the
// final exp.
func roundCompact(mantissa float64, exp, dec int) (string, int) {
	for {
		p := math.Pow10(dec)
		r := math.Round(mantissa*p) / p
		if r >= 1000 && exp < len(compactSuffix)-1 {
			mantissa, exp = r/1000, exp+1
			continue
		}
		s := strconv.FormatFloat(r, 'f', dec, 64)
		if dec > 0 {
			s = strings.TrimSuffix(strings.TrimRight(s, "0"), ".")
		}
		return s, exp
	}
}

// groupThousands renders n in base 10 with comma thousands separators
// (e.g. 12340 -> "12,340"). Used by formatBarValue for the full-number
// 24h bar labels below the M threshold (#324). Hand-rolled to avoid
// promoting golang.org/x/text from an indirect to a direct dependency.
func groupThousands(n int64) string {
	neg := n < 0
	s := strconv.FormatInt(n, 10)
	if neg {
		s = s[1:] // strip leading '-'; re-added below
	}
	if len(s) > 3 {
		var b strings.Builder
		pre := len(s) % 3
		if pre > 0 {
			b.WriteString(s[:pre])
			b.WriteByte(',')
		}
		for i := pre; i < len(s); i += 3 {
			b.WriteString(s[i : i+3])
			if i+3 < len(s) {
				b.WriteByte(',')
			}
		}
		s = b.String()
	}
	if neg {
		return "-" + s
	}
	return s
}

// compactSig3 renders v (>= 1e6) as a 3-significant-figure mantissa plus an
// M/G suffix: 2 decimals in [1,10) ("1.23M"), 1 in [10,100) ("12.3M"), 0 in
// [100,1000) ("123M"), with trailing zeros trimmed and magnitude carry
// ("999.6M" -> "1G"). Used only for the 24h bar's M/G safety band (#324).
func compactSig3(v float64) string {
	mant, exp := scaleCompact(v)
	dec := 0
	switch {
	case mant < 10:
		dec = 2
	case mant < 100:
		dec = 1
	}
	s, exp := roundCompact(mant, exp, dec)
	return s + compactSuffix[exp]
}

// formatBarValue renders v as the compact in-bar label for the active unit
// (#308, precision reworked in #324). Below 1,000,000 the full value is shown
// with thousands separators — cost as whole dollars ("$1,234"), with cents for
// sub-dollar amounts ("$0.45"), tokens as a full integer ("12,340"). At or
// above 1,000,000 a 3-significant-figure M/G suffix is used ("1.23M", "1.2G");
// for cost this is a practically-unreachable safety net. Every label fits the
// 24h BarWidth (10 cols). Distinct from formatUnitValue (the 5-col Y-axis
// label), which keeps the k/M compact form because its slot can't fit a full
// number.
func formatBarValue(v float64, unit chartUnit) string {
	if unit == chartUnitCost {
		switch {
		case v <= 0:
			return "$0"
		case v < 1:
			return "$" + strconv.FormatFloat(v, 'f', 2, 64)
		}
		d := math.Round(v)
		if d < 1_000_000 {
			return "$" + groupThousands(int64(d))
		}
		return "$" + compactSig3(d)
	}
	// tokens
	if v <= 0 {
		return "0"
	}
	n := math.Round(v)
	if n < 1_000_000 {
		return groupThousands(int64(n))
	}
	return compactSig3(n)
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
// already normalised to [0, 1]). The chart's Y range tops out at
// niceCeilingFloat(peak) so overlayYLabel's max/mid labels land on
// exact rows (row 0 and row barsH/2 respectively) — see #250.
//
// unit selects the bar color (Blue for chartUnitTokens — tokens
// (input+output, see issue #232), Amber for chartUnitCost). It is also read by the
// Y-label overlay path in Model.View() — passed through here for that
// reason too.
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

	maxValue := niceCeilingFloat(peak)
	if maxValue == 0 {
		maxValue = 1 // ntcharts requires non-zero max; bars will all be empty anyway
	}
	// WithNoAutoMaxValue is load-bearing for the #230 dynamic-y axis: ntcharts
	// defaults to AutoMaxValue=true, which raises the scale to the tallest bar
	// in the data the moment one exceeds maxValue. We pass the FULL bucket
	// array with peak = the visible-slice peak, so an off-screen bucket taller
	// than the visible peak would otherwise hijack the scale and squash every
	// on-screen bar to the global maximum (defeating dynamic-y entirely).
	// Disabling it pins the ceiling at niceCeilingFloat(peak); ntcharts clips
	// taller (off-screen) bars to full height via math.Min in drawBars.
	bc := barchart.New(chartW, barsH,
		barchart.WithBarGap(zoom.BarGap),
		barchart.WithNoAxis(),
		barchart.WithMaxValue(maxValue),
		barchart.WithNoAutoMaxValue(),
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
	now time.Time, zoom ZoomLevel, order dateOrder, source string) string {

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

	// Explicit draw order: 7d first, 5h ("default") last. ntcharts'
	// DrawBraille* OR-merges rune dot bits via CombineBraillePatterns
	// but overwrites the cell STYLE per draw call (graph.go's
	// DrawBrailleRune calls canvas.NewCellWithStyle with the new
	// dataset's style each time), so the last name in the slice wins
	// coloring in any shared braille cell. 5h is the actionable
	// rate-limit indicator and must remain visible in shared y-bands;
	// 7d is the slower context line and accepts being overdrawn.
	//
	// Do NOT switch back to DrawBrailleAll: its internal sort.Strings
	// silently reshuffles this invariant the moment a third dataset
	// is added whose name sorts after "default" (e.g. "future"). The
	// overlap_5h_wins regression subtest in chart_test.go guards this.
	//
	// "default" matches timeserieslinechart.DefaultDataSetName — used
	// inline because buildLineChart already targets the default
	// dataset implicitly via tslc.Push / tslc.SetStyle above.
	tslc.DrawBrailleDataSets([]string{ds7d, "default"})
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
		"source", source,
		"dur_ms", time.Since(logStart).Milliseconds(),
		"pts5h", len(pts5h),
		"pts7d", len(pts7d),
		"chartW", chartW,
		"chartH", chartH)
	return body
}

// overlayYTicks splices fixed "100%" and " 50%" Y-axis labels and a
// colored legend ("5h", "7d") into the left edge of an already-rendered
// line chart string. The 0% baseline label was dropped in #250 — the
// chart's bottom edge conveys the zero level visually, so the explicit
// label was redundant chrome. Operates ANSI-aware on the post-scroll
// viewport output so labels stay pinned to the viewport's left edge
// regardless of horizontal scroll position.
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
