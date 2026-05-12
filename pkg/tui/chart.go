package tui

import (
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/NimbleMarkets/ntcharts/barchart"
	"github.com/NimbleMarkets/ntcharts/canvas"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
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

// renderXLabels returns a 1-row string of width chartW containing
// clock-aligned tick labels placed at matching bucket columns, with
// "▼ now" right-aligned at the rightmost columns (always wins on
// collision). Later labels overwrite earlier ones; labels that would
// overflow chartW on the right are dropped. Empty bucket set → "".
// Dim foreground throughout — Y axis labels are default fg so the eye
// distinguishes the two rows when they sit close together.
func renderXLabels(buckets []cache.TokenBucket, chartW int, zoom ZoomLevel, now time.Time) string {
	if chartW < 1 || len(buckets) == 0 {
		return ""
	}
	row := make([]rune, chartW)
	for i := range row {
		row[i] = ' '
	}

	// First pass: write clock-aligned labels at matching bucket columns.
	// Each bucket maps 1:1 to a column index — same as the bars above.
	for i, b := range buckets {
		if i >= chartW {
			break
		}
		label := formatXLabel(b.BucketStart, zoom, now)
		if label == "" {
			continue
		}
		labelRunes := []rune(label)
		if i+len(labelRunes) > chartW {
			continue // would overflow the right edge; skip
		}
		for j, r := range labelRunes {
			row[i+j] = r
		}
	}

	// Second pass: overlay "▼ now" at the right edge. Always wins.
	const nowText = "▼ now"
	nowRunes := []rune(nowText)
	switch {
	case len(nowRunes) <= chartW:
		start := chartW - len(nowRunes)
		for j, r := range nowRunes {
			row[start+j] = r
		}
	case chartW >= 1:
		row[chartW-1] = '▼'
	}

	return dimStyle.Render(string(row))
}

// yAxisWidth is the column budget for the fixed-left Y axis. 5 cols are
// enough for the widest expected label ("99.9k", "1.5M"); the 6th col is
// breathing room between the axis and the leftmost bar.
const yAxisWidth = 6

// renderYAxis returns a yAxisWidth-col × height string with the ceiling
// label at row 0 and "0" at row height-2 (the row above the X labels row
// in the viewport). Other rows are 6 spaces. ceiling <= 0 returns a 6×H
// blank column (no spurious "1" over empty bars). height < 6 returns ""
// (the caller should also have already decided not to show the Y axis
// via Model.shouldShowYAxis()).
func renderYAxis(ceiling int64, height int) string {
	if height < 6 {
		return ""
	}
	blank := strings.Repeat(" ", yAxisWidth)
	rows := make([]string, height)
	for i := range rows {
		rows[i] = blank
	}
	if ceiling > 0 {
		rows[0] = fmt.Sprintf("%*s", yAxisWidth, formatTokenCount(ceiling))
		rows[height-2] = fmt.Sprintf("%*s", yAxisWidth, "0")
	}
	return strings.Join(rows, "\n")
}

// formatXLabel returns the X-axis tick label for bucket time t at the
// given zoom; "" if t is not on a label boundary. Cadence is clock-aligned
// (anchored to hour / 3-hour / day marks) so positions are stable across
// refreshes. 1h zoom uses hybrid weekday/MM-DD: weekday short ("Mon", ...)
// for buckets within the past 7 days; "MM-DD" for older.
func formatXLabel(t time.Time, zoom ZoomLevel, now time.Time) string {
	switch zoom.Label {
	case "5m":
		if t.Minute() == 0 {
			return t.Format("15:04")
		}
	case "15m":
		if t.Hour()%3 == 0 && t.Minute() == 0 {
			return t.Format("15:04")
		}
	case "1h":
		if t.Hour() == 0 && t.Minute() == 0 {
			if t.After(now.AddDate(0, 0, -7)) {
				return t.Format("Mon")
			}
			return t.Format("01-02")
		}
	}
	return ""
}

// niceCeiling returns the smallest "nice" value >= peak from the sequence
// {1, 1.5, 2, 2.5, 5} × 10^k. Used to pick a clean Y axis top label and
// to override the barchart's auto-scaling (via WithMaxValue) so labels
// stay truthful about where the tallest bar actually lands.
func niceCeiling(peak int64) int64 {
	if peak <= 0 {
		return 1
	}
	mag := math.Pow10(int(math.Floor(math.Log10(float64(peak)))))
	norm := float64(peak) / mag
	var nice float64
	switch {
	case norm <= 1.0:
		nice = 1.0
	case norm <= 1.5:
		nice = 1.5
	case norm <= 2.0:
		nice = 2.0
	case norm <= 2.5:
		nice = 2.5
	case norm <= 5.0:
		nice = 5.0
	default:
		nice = 10.0
	}
	return int64(math.Round(nice * mag))
}

// niceFloor returns the largest "nice" value <= peak from the sequence
// {1, 2, 2.5, 5, 7.5} × 10^k. Used to pick the in-canvas Y tick label
// for the bar chart (issue #132). Denser than niceCeiling's set so the
// tick lands near the peak rather than half-way down the chart.
// Returns 0 when peak <= 0 so the caller can guard the overlay write.
func niceFloor(peak int64) int64 {
	if peak <= 0 {
		return 0
	}
	mag := math.Pow10(int(math.Floor(math.Log10(float64(peak)))))
	norm := float64(peak) / mag
	var nice float64
	switch {
	case norm >= 7.5:
		nice = 7.5
	case norm >= 5.0:
		nice = 5.0
	case norm >= 2.5:
		nice = 2.5
	case norm >= 2.0:
		nice = 2.0
	default:
		nice = 1.0
	}
	return int64(math.Round(nice * mag))
}

// formatTokenCount renders an int64 token count compactly with a k/M
// suffix, suitable for the Y axis and other in-chart annotations.
// Threshold rules:
//
//	n <= 0       -> "0"
//	n < 1000     -> raw integer
//	n < 1e6      -> k suffix, 1 frac digit; drop frac when v >= 100
//	n >= 1e6     -> M suffix, same precision rule
//
// Max output width: 5 cols. Fits the 6-col Y axis budget with breathing room.
func formatTokenCount(n int64) string {
	if n <= 0 {
		return "0"
	}
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	if n < 1_000_000 {
		return formatSuffixed(float64(n)/1000, "k")
	}
	return formatSuffixed(float64(n)/1_000_000, "M")
}

// formatSuffixed prints v with 1 fractional digit when v < 100 and 0
// fractional digits otherwise, then appends suffix. Keeps labels inside
// the 5-col budget for any expected token magnitude.
func formatSuffixed(v float64, suffix string) string {
	if v >= 100 {
		return strconv.FormatFloat(v, 'f', 0, 64) + suffix
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + suffix
}

// heatColor returns a lipgloss color on a green→yellow→red ramp
// based on ratio (0.0–1.0) of a bucket's tokens relative to the peak bucket.
func heatColor(ratio float64) lipgloss.Color {
	switch {
	case ratio >= 0.66:
		return Red
	case ratio >= 0.33:
		return Yellow
	default:
		return Green
	}
}

// buildChart renders the viewport content from buckets at the given zoom:
// bars in the top chartH-1 rows (or all chartH if chartH < 6, the same
// threshold renderXLabels uses), plus an X-axis tick label row at the
// bottom. Bars are scaled to peak (not niceFloor) so the tallest bar fills
// the full canvas; barchart.WithNoAxis() reclaims the 2 rows ntcharts
// otherwise reserves for its own (empty) axis. A single dim Y tick label
// at niceFloor(peak) is overlaid inside the canvas at the row matching
// that value (issue #132).
func buildChart(buckets []cache.TokenBucket, chartW, chartH int, now time.Time, zoom ZoomLevel) string {
	start := time.Now()
	if chartH < 1 {
		chartH = 1 // barchart.New panics with a zero height
	}

	var peak int64
	for _, b := range buckets {
		if b.Tokens > peak {
			peak = b.Tokens
		}
	}

	barsH := chartH
	showXLabels := chartH >= 6
	if showXLabels {
		barsH = chartH - 1
	}

	bars := make([]barchart.BarData, len(buckets))
	for i, b := range buckets {
		ratio := float64(0)
		if peak > 0 {
			ratio = float64(b.Tokens) / float64(peak)
		}
		c := heatColor(ratio)
		// ntcharts paints bar cells using both fg + bg of the same color
		// so the cell renders as a solid block (the example sets both).
		// Foreground alone leaves the cell empty.
		bars[i] = barchart.BarData{
			Values: []barchart.BarValue{
				{
					Value: float64(b.Tokens),
					Style: lipgloss.NewStyle().Foreground(c).Background(c),
				},
			},
		}
	}

	// barGap=0 — bars must touch when chartW == numBars (see #102).
	// WithNoAxis disables ntcharts' internal axis (we never set BarData.Label,
	// so its 2 reserved rows are pure waste). WithMaxValue(peak) makes the
	// tallest bar fill the full canvas; the Y tick label is a sub-peak
	// reference overlaid separately below.
	maxValue := float64(peak)
	if maxValue == 0 {
		maxValue = 1 // ntcharts requires a non-zero max; bars will all be empty anyway
	}
	bc := barchart.New(chartW, barsH,
		barchart.WithBarGap(0),
		barchart.WithNoAxis(),
		barchart.WithMaxValue(maxValue),
	)
	bc.PushAll(bars)
	bc.Draw()

	// Overlay the Y tick label at the canvas row matching niceFloor(peak).
	// Skipped when peak == 0 (empty data) or niceFloor returns 0.
	if peak > 0 {
		if tick := niceFloor(peak); tick > 0 {
			row := barsH - int(math.Round(float64(tick)/float64(peak)*float64(barsH)))
			if row < 0 {
				row = 0
			}
			if row >= barsH {
				row = barsH - 1
			}
			bc.Canvas.SetStringWithStyle(canvas.Point{X: 0, Y: row}, formatTokenCount(tick), dimStyle)
		}
	}

	body := bc.View()

	if showXLabels {
		body = lipgloss.JoinVertical(lipgloss.Left, body, renderXLabels(buckets, chartW, zoom, now))
	}

	slog.Debug("tui.buildChart",
		"dur_ms", time.Since(start).Milliseconds(),
		"buckets", len(buckets),
		"chartW", chartW,
		"chartH", chartH,
		"peak", peak)
	return body
}
