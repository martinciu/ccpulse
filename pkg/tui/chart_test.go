package tui

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// sinkString prevents the compiler from eliding the buildChart call
// in BenchmarkBuildChart when its return value is otherwise unused.
var sinkString string

// syntheticChartInput produces n contiguous 5-minute chart inputs with
// deterministic, varied values. Anchored to a 3-hour clock boundary so
// the 15m zoom (used by BenchmarkBuildChart and BenchmarkRenderXLabels)
// hits a label tick every 36th bucket — exercising renderXLabels'
// label-write loop.
// Without this anchor, formatXLabel would return "" for nearly every
// bucket and the label-write path would be under-measured.
func syntheticChartInput(n int) (values []float64, starts []time.Time, peak float64) {
	now := time.Now().UTC().Truncate(3 * time.Hour)
	values = make([]float64, n)
	starts = make([]time.Time, n)
	for i := range values {
		v := float64((i * 137) % 1000)
		values[i] = v
		starts[i] = now.Add(time.Duration(i) * 5 * time.Minute)
		if v > peak {
			peak = v
		}
	}
	return
}

// projectBuckets converts a fixed test slice of cache.TokenBucket into
// the (values, starts, peak) triple buildChart and renderXLabels now
// take. Used in tests where the existing fixture is a curated bucket
// list (not a synthetic generator). Keeps the test data declarations
// readable while the production code moves off bucket types.
func projectBuckets(bs []cache.TokenBucket) (values []float64, starts []time.Time, peak float64) {
	values = make([]float64, len(bs))
	starts = make([]time.Time, len(bs))
	for i, b := range bs {
		values[i] = float64(b.Tokens)
		starts[i] = b.BucketStart
		if values[i] > peak {
			peak = values[i]
		}
	}
	return
}

func BenchmarkBuildChart(b *testing.B) {
	for _, n := range []int{10_000, 25_000, 50_000} {
		values, starts, peak := syntheticChartInput(n)
		b.Run(formatN(n), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			now := time.Now().UTC()
			for b.Loop() {
				sinkString = buildChart(values, starts, peak, n, 20, now, ZoomLevels[1], chartUnitTokens, dateOrderMonthFirst)
			}
		})
	}
}

func formatN(n int) string {
	switch {
	case n >= 1000:
		return itoa3(n/1000) + "k"
	default:
		return itoa3(n)
	}
}

func BenchmarkFormatTokenCount(b *testing.B) {
	// Sub-benchmarks per branch so each "op" is one call, not 8 — exposes
	// per-branch regressions instead of diluting them in a geometric mean.
	cases := []struct {
		name string
		v    int64
	}{
		{"zero", 0},
		{"raw", 999},
		{"k_frac", 45_300},
		{"k_no_frac", 100_000},
		{"M_frac", 1_200_000},
		{"M_no_frac", 100_000_000},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				sinkString = formatTokenCount(tt.v)
			}
		})
	}
}

func BenchmarkRenderXLabels(b *testing.B) {
	now := time.Now().UTC()
	for _, n := range []int{100, 1000, 5000} {
		_, starts, _ := syntheticChartInput(n)
		b.Run(formatN(n), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = renderXLabels(starts, n, ZoomLevels[1], now, dateOrderMonthFirst)
			}
		})
	}
}

// BenchmarkBarChartRender measures the per-frame cost of rebuilding
// the bar chart at the chart widths the animation will hit (the
// harmonica spring rebuilds the chart canvas each tick — see #101).
//
// Sizes 100/1000/5000 cover narrow/normal/wide terminals with 5-min
// spaced data (5000 ≈ 17 days). At 60 FPS the per-frame
// budget is ~16ms; if 5000 exceeds it, the spring tick rate falls
// back to harmonica.FPS(30) per the spec's bench-gate rule.
func BenchmarkBarChartRender(b *testing.B) {
	now := time.Now().UTC()
	for _, n := range []int{100, 1000, 5000} {
		values, starts, peak := syntheticChartInput(n)
		b.Run(formatN(n), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = buildChart(values, starts, peak, n, 20, now, ZoomLevels[1], chartUnitTokens, dateOrderMonthFirst)
			}
		})
	}
}

// itoa3 avoids strconv import noise — keeps the bench's deps minimal.
func itoa3(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestBuildChart_ChartHTooShortDropsXLabels(t *testing.T) {
	now := time.Now().UTC().Truncate(15 * time.Minute)
	bs := []cache.TokenBucket{
		{BucketStart: now.Add(-30 * time.Minute), Tokens: 1000},
		{BucketStart: now.Add(-15 * time.Minute), Tokens: 3000},
		{BucketStart: now, Tokens: 2000},
	}
	// chartH=5 is below the chartH>=6 threshold; the X labels row should
	// be dropped and bars should take all 5 rows.
	values, starts, peak := projectBuckets(bs)
	out := buildChart(values, starts, peak, len(bs), 5, now, ZoomLevels[0], chartUnitTokens, dateOrderMonthFirst)
	rows := strings.Split(out, "\n")
	if len(rows) != 5 {
		t.Errorf("expected 5 rows (chartH), got %d", len(rows))
	}
}

// barHeightAtCol returns how many bar rows (top-to-bottom, excluding the
// trailing x-axis label row) contain a bar block rune at the given visual
// column. ANSI styling is stripped first so the column index is visual.
func barHeightAtCol(body string, col int) int {
	rows := strings.Split(body, "\n")
	if len(rows) > 1 {
		rows = rows[:len(rows)-1] // drop the x-axis labels row
	}
	h := 0
	for _, row := range rows {
		r := []rune(ansi.Strip(row))
		if col < len(r) && strings.ContainsRune("█▇▆▅▄▃▂▁", r[col]) {
			h++
		}
	}
	return h
}

// TestBuildChart_OffscreenTallerBarDoesNotSquashVisible pins the #230
// dynamic-y contract at the render layer: a bar equal to the peak must fill
// (almost) the full chart height even when the data array also contains a
// much taller bucket — an off-screen outlier beyond the visible-slice peak.
//
// ntcharts barchart defaults to AutoMaxValue=true, which silently raises the
// scale to the largest bar in the data (barchart.go SetMax on push),
// overriding WithMaxValue. buildChart passes the FULL bucket array (visible +
// off-screen) with peak = the visible-slice peak, so without
// WithNoAutoMaxValue every on-peak bar collapses to the off-screen outlier's
// scale — the "squashed like before #252" symptom. The earlier #230 tests
// only asserted m.peak (the value), not the rendered height, so they missed
// this.
func TestBuildChart_OffscreenTallerBarDoesNotSquashVisible(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Truncate(15 * time.Minute)
	starts := []time.Time{now.Add(-15 * time.Minute), now}
	const chartH = 12
	// Bar 0 == peak (100); bar 1 is 10× taller (an off-screen outlier the
	// viewport would scroll past). peak = the visible-slice peak (100), so
	// niceCeilingFloat(100) = 100 and bar 0 should fill the whole bar area.
	body := buildChart([]float64{100, 1000}, starts, 100, 2, chartH, now, ZoomLevels[0], chartUnitCost, dateOrderMonthFirst)

	barRows := chartH - 1 // one row reserved for x-axis labels (chartH>=6)
	if got := barHeightAtCol(body, 0); got < barRows-1 {
		t.Errorf("bar 0 (value==peak) rendered %d/%d rows — squashed by the "+
			"off-screen 10× bucket. ntcharts AutoMaxValue overrode WithMaxValue; "+
			"buildChart needs WithNoAutoMaxValue (#230).", got, barRows)
	}
}

func TestRenderXLabels_OverflowingLabelDropped(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC)
	buckets := []cache.TokenBucket{
		{BucketStart: time.Date(2026, 5, 12, 13, 0, 0, 0, time.UTC)}, // would emit "13:00"
		{BucketStart: time.Date(2026, 5, 12, 13, 5, 0, 0, time.UTC)},
		{BucketStart: time.Date(2026, 5, 12, 13, 10, 0, 0, time.UTC)},
	}
	// chartW=3: "13:00" at col 0 needs cols 0-4, overflows. Dropped.
	_, starts, _ := projectBuckets(buckets)
	got := renderXLabels(starts, 3, ZoomLevels[0], now, dateOrderMonthFirst)
	if strings.Contains(got, "13:00") {
		t.Errorf("'13:00' label should have been dropped (would overflow chartW=3), got %q", got)
	}
}

func TestRenderXLabels(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC)
	mkBuckets := func(times ...time.Time) []cache.TokenBucket {
		out := make([]cache.TokenBucket, len(times))
		for i, ts := range times {
			out[i] = cache.TokenBucket{BucketStart: ts}
		}
		return out
	}

	tests := []struct {
		name        string
		buckets     []cache.TokenBucket
		chartW      int
		zoom        ZoomLevel
		wantEmpty   bool
		wantSubstrs []string
	}{
		{
			name:      "empty buckets returns empty string",
			buckets:   nil,
			chartW:    20,
			zoom:      ZoomLevels[0],
			wantEmpty: true,
		},
		{
			name: "15m hour label appears at 3-hour boundary",
			buckets: mkBuckets(
				time.Date(2026, 5, 12, 12, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 12, 15, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 12, 30, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 12, 45, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 13, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 13, 15, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 13, 30, 0, 0, time.UTC),
			),
			chartW:      20,
			zoom:        ZoomLevels[0],
			wantSubstrs: []string{"12:00"},
		},
		{
			name: "24h zoom shows weekday",
			buckets: mkBuckets(
				time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC),
			),
			chartW:      30,
			zoom:        ZoomLevels[2],
			wantSubstrs: []string{"Tue"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, starts, _ := projectBuckets(tt.buckets)
			got := renderXLabels(starts, tt.chartW, tt.zoom, now, dateOrderMonthFirst)
			if tt.wantEmpty {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			if w := lipgloss.Width(got); w != tt.chartW {
				t.Errorf("width = %d, want %d; output = %q", w, tt.chartW, got)
			}
			for _, want := range tt.wantSubstrs {
				if !strings.Contains(got, want) {
					t.Errorf("output missing %q\nfull: %q", want, got)
				}
			}
		})
	}
}

func TestFormatXLabel(t *testing.T) {
	t.Parallel()
	// Tuesday 14:30 UTC. Tests use UTC throughout for determinism.
	now := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC)
	d := func(h, m int) time.Time {
		return time.Date(2026, 5, 12, h, m, 0, 0, time.UTC)
	}
	day := func(month, day int) time.Time {
		return time.Date(2026, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	zm15 := ZoomLevels[0] // "15m"
	z1h := ZoomLevels[1]  // "1h"
	z24 := ZoomLevels[2]  // "24h"

	tests := []struct {
		name  string
		t     time.Time
		zoom  ZoomLevel
		order dateOrder
		want  string
	}{
		// 15m: 3-hour cadence
		{"15m on 3-hour MonthFirst", d(12, 0), zm15, dateOrderMonthFirst, "12:00"},
		{"15m on 3-hour DayFirst", d(12, 0), zm15, dateOrderDayFirst, "12:00"},
		{"15m off-3-hour", d(13, 0), zm15, dateOrderMonthFirst, ""},
		{"15m mid-window 09:00", d(9, 0), zm15, dateOrderMonthFirst, "09:00"},
		{"15m midnight today recent → Tue", day(5, 12), zm15, dateOrderMonthFirst, "Tue"},
		{"15m midnight 12 days ago MonthFirst → 04/30", day(4, 30), zm15, dateOrderMonthFirst, "04/30"},
		{"15m midnight 12 days ago DayFirst → 30/04", day(4, 30), zm15, dateOrderDayFirst, "30/04"},

		// 1h: midnight-only labels
		{"1h today midnight Tue", day(5, 12), z1h, dateOrderMonthFirst, "Tue"},
		{"1h yesterday Mon", day(5, 11), z1h, dateOrderMonthFirst, "Mon"},
		{"1h 6 days ago Wed", day(5, 6), z1h, dateOrderMonthFirst, "Wed"},
		{"1h 7 days ago MonthFirst → MM/DD", day(5, 5), z1h, dateOrderMonthFirst, "05/05"},
		{"1h 7 days ago DayFirst → DD/MM", day(5, 5), z1h, dateOrderDayFirst, "05/05"},
		{"1h non-midnight returns empty", d(14, 0), z1h, dateOrderMonthFirst, ""},
		{"1h 12 days ago MonthFirst → 04/30", day(4, 30), z1h, dateOrderMonthFirst, "04/30"},
		{"1h 12 days ago DayFirst → 30/04", day(4, 30), z1h, dateOrderDayFirst, "30/04"},

		// 24h: every bucket labelled (no midnight gate)
		{"24h today recent → Tue", day(5, 12), z24, dateOrderMonthFirst, "Tue"},
		{"24h yesterday recent → Mon", day(5, 11), z24, dateOrderMonthFirst, "Mon"},
		{"24h 12 days ago MonthFirst → 04/30", day(4, 30), z24, dateOrderMonthFirst, "04/30"},
		{"24h 12 days ago DayFirst → 30/04", day(4, 30), z24, dateOrderDayFirst, "30/04"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatXLabel(tt.t, tt.zoom, now, tt.order)
			if got != tt.want {
				t.Errorf("formatXLabel(%v, %s, %v) = %q, want %q",
					tt.t.Format(time.RFC3339), tt.zoom.Label, tt.order, got, tt.want)
			}
		})
	}
}

func TestDateLabel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC) // Tue
	day := func(month, day int) time.Time {
		return time.Date(2026, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}
	tests := []struct {
		name  string
		t     time.Time
		order dateOrder
		want  string
	}{
		// Recent (within past 7 days): weekday short, both orders.
		{"6 days ago MonthFirst → weekday", day(5, 6), dateOrderMonthFirst, "Wed"},
		{"6 days ago DayFirst → weekday", day(5, 6), dateOrderDayFirst, "Wed"},
		{"1 day ago MonthFirst → weekday", day(5, 11), dateOrderMonthFirst, "Mon"},
		{"1 day ago DayFirst → weekday", day(5, 11), dateOrderDayFirst, "Mon"},
		// 7d 14h ago (May 5 00:00, with now = May 12 14:30, the 7-day
		// mark is May 5 14:30 — May 5 00:00 is older than the window).
		{"7d 14h ago MonthFirst → date", day(5, 5), dateOrderMonthFirst, "05/05"},
		{"7d 14h ago DayFirst → date", day(5, 5), dateOrderDayFirst, "05/05"},
		// Strict-After boundary: t.After(now-7d) is `>`, not `>=`, so t
		// at exactly the 7-day mark falls to the date branch; one ns
		// later flips to weekday. Locks the contract for future edits.
		{"exact 7-day boundary → date (After is strict)", now.AddDate(0, 0, -7), dateOrderMonthFirst, "05/05"},
		{"1ns inside 7-day window → weekday", now.AddDate(0, 0, -7).Add(time.Nanosecond), dateOrderMonthFirst, "Tue"},
		// Older: locale-aware date.
		{"12 days ago MonthFirst → MM/DD", day(4, 30), dateOrderMonthFirst, "04/30"},
		{"12 days ago DayFirst → DD/MM", day(4, 30), dateOrderDayFirst, "30/04"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := dateLabel(tt.t, now, tt.order)
			if got != tt.want {
				t.Errorf("dateLabel(%s, _, %v) = %q, want %q",
					tt.t.Format("2006-01-02"), tt.order, got, tt.want)
			}
		})
	}
}

// TestOverlayYLabel_InjectsCeilingAndMidpoint pins the post-#250 splice
// behaviour: max label at row 0 (= niceCeilingFloat(peak)), mid label
// at row barsH/2 (= ceiling/2), both right-aligned in a yLabelSlotW
// (=5) column. Other rows stay untouched.
//
// peak = 87000 → niceCeiling = 100000 → max label "100k" → " 100k" (5 cols)
// mid          =  50000                → mid label  "50k" → "  50k" (5 cols)
// chartH = 6   → barsH = 5             → midRow = 2
func TestOverlayYLabel_InjectsCeilingAndMidpoint(t *testing.T) {
	t.Parallel()
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE\nFFFFFFFFFF"
	out := overlayYLabel(body, 87_000, chartUnitTokens, 6, 1.0)
	rows := strings.Split(out, "\n")
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows, got %d:\n%q", len(rows), out)
	}
	// Row 0 — max label.
	row0 := stripANSIForTest(rows[0])
	if !strings.HasPrefix(row0, " 100k") {
		t.Errorf("expected row 0 to start with \" 100k\", got %q", row0)
	}
	if !strings.HasSuffix(row0, "AAAAA") {
		t.Errorf("expected row 0 to keep 5 trailing 'A's after the 5-col label, got %q", row0)
	}
	// Row 2 — mid label.
	row2 := stripANSIForTest(rows[2])
	if !strings.HasPrefix(row2, "  50k") {
		t.Errorf("expected row 2 to start with \"  50k\", got %q", row2)
	}
	if !strings.HasSuffix(row2, "CCCCC") {
		t.Errorf("expected row 2 to keep 5 trailing 'C's after the 5-col label, got %q", row2)
	}
	// Rows 1, 3, 4, 5 untouched.
	for _, c := range []struct {
		row    int
		expect string
	}{
		{1, "BBBBBBBBBB"},
		{3, "DDDDDDDDDD"},
		{4, "EEEEEEEEEE"},
		{5, "FFFFFFFFFF"},
	} {
		if !strings.Contains(rows[c.row], c.expect) {
			t.Errorf("row %d should still contain %q, got %q", c.row, c.expect, rows[c.row])
		}
	}
}

// TestOverlayYLabel_SmallPeakSkipsMid pins the yLabelMidFloor guard:
// when peak is small enough that the mid would format as "$0.00" /
// "0", the mid splice is skipped entirely and the original row
// content survives intact.
func TestOverlayYLabel_SmallPeakSkipsMid(t *testing.T) {
	t.Parallel()
	// 12-row body (chartH=12 → barsH=11 → midRow=5). Each row is a
	// distinct repeated letter so we can pinpoint untouched rows.
	letters := []byte("ABCDEFGHIJKL")
	lines := make([]string, 12)
	for i, c := range letters {
		lines[i] = strings.Repeat(string(c), 20)
	}
	body := strings.Join(lines, "\n")

	t.Run("cost", func(t *testing.T) {
		// peak = 0.005 → niceCeiling = 0.005 → max label "$0.01"
		// mid = 0.0025 < yLabelMidFloor(cost)=0.005 → skip mid
		out := overlayYLabel(body, 0.005, chartUnitCost, 12, 1.0)
		rows := strings.Split(out, "\n")
		if len(rows) != 12 {
			t.Fatalf("expected 12 rows, got %d", len(rows))
		}
		row0 := stripANSIForTest(rows[0])
		if !strings.HasPrefix(row0, "$0.01") {
			t.Errorf("expected row 0 max label '$0.01', got %q", row0)
		}
		// Mid row (5, letter 'F') untouched — first 5 cols should be
		// 'FFFFF', not "$0.00" or pure spaces from a clobbered splice.
		midSlot := stripANSIForTest(rows[5])[:yLabelSlotW]
		if midSlot != "FFFFF" {
			t.Errorf("expected mid row untouched (5×'F'), got %q (full row %q)", midSlot, rows[5])
		}
	})

	t.Run("tokens", func(t *testing.T) {
		// peak = 1.0 → niceCeiling = 1.0 → max label "1"
		// mid = 0.5 < yLabelMidFloor(tokens)=1.0 → skip mid
		out := overlayYLabel(body, 1.0, chartUnitTokens, 12, 1.0)
		rows := strings.Split(out, "\n")
		if len(rows) != 12 {
			t.Fatalf("expected 12 rows, got %d", len(rows))
		}
		row0 := stripANSIForTest(rows[0])
		if !strings.HasPrefix(row0, "    1") {
			t.Errorf("expected row 0 max label padded '    1', got %q", row0)
		}
		// Mid row (5, letter 'F') untouched.
		midSlot := stripANSIForTest(rows[5])[:yLabelSlotW]
		if midSlot != "FFFFF" {
			t.Errorf("expected mid row untouched (5×'F'), got %q (full row %q)", midSlot, rows[5])
		}
	})
}

func TestOverlayYLabel_BlankWhenEmpty(t *testing.T) {
	t.Parallel()
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE\nFFFFFFFFFF"
	for _, peak := range []float64{0, -5} {
		out := overlayYLabel(body, peak, chartUnitTokens, 6, 1.0)
		if out != body {
			t.Errorf("peak=%v: expected body untouched, got %q", peak, out)
		}
	}
}

func TestOverlayYLabel_HeightTooSmall(t *testing.T) {
	t.Parallel()
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE"
	// chartH < 6 leaves body untouched — same threshold renderXLabels uses.
	if got := overlayYLabel(body, 50_000, chartUnitTokens, 5, 1.0); got != body {
		t.Errorf("expected body untouched at chartH=5, got %q", got)
	}
}

func TestNiceFloorFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		peak float64
		want float64
	}{
		{"zero returns zero", 0, 0},
		{"negative returns zero", -10, 0},
		// Sub-1 (cost mode), exponent k = -1.
		{"0.45 falls to 0.3", 0.45, 0.3},
		{"0.71 falls to 0.7", 0.71, 0.7},
		{"0.12 falls to 0.1", 0.12, 0.1},
		{"0.04 falls to 0.03", 0.04, 0.03},
		// k = 0.
		{"1.0 stays 1", 1.0, 1.0},
		{"1.4 stays 1", 1.4, 1.0},
		{"4.7 falls to 3", 4.7, 3.0},
		{"7.1 falls to 7", 7.1, 7.0},
		// k = 1.
		{"23 falls to 20", 23, 20.0},
		{"45.7 falls to 30", 45.7, 30.0},
		{"66 falls to 50", 66, 50.0},
		{"99 falls to 70", 99, 70.0},
		// k = 2.
		{"123 falls to 100", 123, 100.0},
		{"850 falls to 700", 850, 700.0},
		// k = 3.
		{"1234 falls to 1000", 1234, 1000.0},
		{"4999 falls to 3000", 4999, 3000.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := niceFloorFloat(tt.peak)
			// Sub-1 results carry binary-FP residue from math.Pow10(-1);
			// allow 1e-9 slack rather than asserting exact equality.
			if diff := got - tt.want; diff < -1e-9 || diff > 1e-9 {
				t.Errorf("niceFloorFloat(%v) = %v, want %v", tt.peak, got, tt.want)
			}
		})
	}
}

func TestFormatTokenCount(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0"},
		{"negative", -5, "0"},
		{"one", 1, "1"},
		{"just below k", 999, "999"},
		{"exactly k", 1000, "1k"},
		{"k mid", 45000, "45k"},
		{"k 75k", 75_000, "75k"},
		{"k 100k", 100_000, "100k"},
		{"k high", 999_000, "999k"},
		{"k just below M", 999_999, "1000k"},
		{"exactly M", 1_000_000, "1M"},
		{"M mid", 45_000_000, "45M"},
		{"M 50M", 50_000_000, "50M"},
		{"M 100M", 100_000_000, "100M"},
		{"M high", 999_000_000, "999M"},
		{"M just below G", 999_999_999, "1000M"},
		{"exactly G", 1_000_000_000, "1G"},
		{"G mid", 45_000_000_000, "45G"},
		{"G high", 999_000_000_000, "999G"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatTokenCount(tt.in)
			if got != tt.want {
				t.Errorf("formatTokenCount(%d) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFormatUnitValue(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		v    float64
		unit chartUnit
		want string
	}{
		// Tokens mode — outputs match formatTokenCount(int64(v)).
		{"tokens zero", 0, chartUnitTokens, "0"},
		{"tokens negative", -5, chartUnitTokens, "0"},
		{"tokens 1", 1, chartUnitTokens, "1"},
		{"tokens 999", 999, chartUnitTokens, "999"},
		{"tokens exactly 1k", 1000, chartUnitTokens, "1k"},
		{"tokens 45k", 45000, chartUnitTokens, "45k"},
		{"tokens 100k", 100_000, chartUnitTokens, "100k"},
		{"tokens 1M", 1_000_000, chartUnitTokens, "1M"},
		{"tokens 50M", 50_000_000, chartUnitTokens, "50M"},
		// Cost mode — sub-1 keeps two decimals, then $X / $Xk / $XM.
		{"cost zero", 0, chartUnitCost, "$0"},
		{"cost negative", -0.5, chartUnitCost, "$0"},
		{"cost 0.45", 0.45, chartUnitCost, "$0.45"},
		{"cost 0.10", 0.10, chartUnitCost, "$0.10"},
		{"cost 0.04", 0.04, chartUnitCost, "$0.04"},
		{"cost 0.99", 0.99, chartUnitCost, "$0.99"},
		{"cost 1", 1.0, chartUnitCost, "$1"},
		{"cost 45", 45.0, chartUnitCost, "$45"},
		{"cost 999", 999.0, chartUnitCost, "$999"},
		{"cost exactly 1k", 1000.0, chartUnitCost, "$1k"},
		{"cost 45k", 45000.0, chartUnitCost, "$45k"},
		{"cost 1M", 1_000_000.0, chartUnitCost, "$1M"},
		{"cost 1G", 1_000_000_000.0, chartUnitCost, "$1G"},
		{"cost 45G", 45_000_000_000.0, chartUnitCost, "$45G"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatUnitValue(tt.v, tt.unit)
			if got != tt.want {
				t.Errorf("formatUnitValue(%v, %v) = %q, want %q", tt.v, tt.unit, got, tt.want)
			}
		})
	}
}

func TestFormatBarValue(t *testing.T) {
	tests := []struct {
		name string
		v    float64
		unit chartUnit
		want string
	}{
		// cost — integer dollars, no cents, magnitude carry
		{"cost zero", 0, chartUnitCost, "$0"},
		{"cost negative", -5, chartUnitCost, "$0"},
		{"cost sub-dollar rounds down", 0.4, chartUnitCost, "$0"},
		{"cost sub-dollar rounds up", 0.6, chartUnitCost, "$1"},
		{"cost whole", 45, chartUnitCost, "$45"},
		{"cost 999", 999, chartUnitCost, "$999"},
		{"cost 999.5 carries to k", 999.5, chartUnitCost, "$1k"},
		{"cost 1200 -> 1k", 1200, chartUnitCost, "$1k"},
		{"cost 1600 -> 2k", 1600, chartUnitCost, "$2k"},
		{"cost million", 1_000_000, chartUnitCost, "$1M"},
		// tokens — integer at k, one decimal at M/G, magnitude carry
		{"tok zero", 0, chartUnitTokens, "0"},
		{"tok raw", 42, chartUnitTokens, "42"},
		{"tok 750", 750, chartUnitTokens, "750"},
		{"tok 5k", 5000, chartUnitTokens, "5k"},
		{"tok 750k", 750_000, chartUnitTokens, "750k"},
		{"tok 999500 carries to 1M", 999_500, chartUnitTokens, "1M"},
		{"tok 1.2M", 1_200_000, chartUnitTokens, "1.2M"},
		{"tok exact 1M trims .0", 1_000_000, chartUnitTokens, "1M"},
		{"tok 2.1M", 2_100_000, chartUnitTokens, "2.1M"},
		{"tok 1.2G", 1_200_000_000, chartUnitTokens, "1.2G"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatBarValue(tt.v, tt.unit); got != tt.want {
				t.Errorf("formatBarValue(%v, %v) = %q, want %q", tt.v, tt.unit, got, tt.want)
			}
		})
	}
}

func TestOverlayBarLabels_PlacesAtBarTops(t *testing.T) {
	now := time.Now()
	zoom := ZoomLevels[2] // 24h: BarWidth=10, BarGap=2, stride=12
	// peak=100 -> niceCeilingFloat=100; barsH=10 (chartH=11). Round geometry:
	//   100 -> 10 rows, top row 0
	//    50 ->  5 rows, top row 5
	//    20 ->  2 rows, top row 8
	//    10 ->  1 row,  top row 9  (height 1 -> skipped)
	//     0 ->  0 rows            (no fill   -> skipped)
	values := []float64{100, 50, 20, 10, 0}
	starts := make([]time.Time, len(values))
	for i := range starts {
		starts[i] = now.Add(time.Duration(-i) * 24 * time.Hour)
	}
	canvasW := zoom.CanvasWidth(len(values)) // 5*10 + 4*2 = 58
	body := buildChart(values, starts, 100, canvasW, 11, now, zoom, chartUnitTokens, dateOrderMonthFirst)

	style := lipgloss.NewStyle()
	texts := make([]string, len(values))
	for i, v := range values {
		if v > 0 {
			texts[i] = style.Render(formatBarValue(v, chartUnitTokens))
		}
	}
	out := overlayBarLabels(body, texts, 10, canvasW, zoom)
	lines := strings.Split(out, "\n")

	at := func(line, from, to int) string {
		r := []rune(ansi.Strip(lines[line]))
		if to > len(r) {
			t.Fatalf("line %d shorter than %d (len=%d)", line, to, len(r))
		}
		return string(r[from:to])
	}

	// bar0: "100" centered in [0,10) -> start 3, on top row 0
	if got := at(0, 3, 6); got != "100" {
		t.Errorf("bar0 label: got %q, want %q", got, "100")
	}
	// bar1: "50" centered in [12,22) -> start 16, on top row 5
	if got := at(5, 16, 18); got != "50" {
		t.Errorf("bar1 label: got %q, want %q", got, "50")
	}
	// bar2: "20" centered in [24,34) -> start 28, on top row 8
	if got := at(8, 28, 30); got != "20" {
		t.Errorf("bar2 label: got %q, want %q", got, "20")
	}
	// bar3 height-1: no "10" spliced where its label would land (cols 40-41 row 9)
	if got := at(9, 40, 42); got == "10" {
		t.Errorf("bar3 (height 1) should be skipped, got label %q", got)
	}
}

func TestOverlayBarLabels_NarrowZoomPassThrough(t *testing.T) {
	now := time.Now()
	zoom := ZoomLevels[0] // 15m: BarWidth=1 < barLabelMinWidth
	body := buildChart([]float64{5}, []time.Time{now}, 5, 10, 11, now, zoom, chartUnitTokens, dateOrderMonthFirst)
	texts := []string{lipgloss.NewStyle().Render("5")}
	if got := overlayBarLabels(body, texts, 10, 10, zoom); got != body {
		t.Errorf("15m (BarWidth=1) should pass through unchanged")
	}
}

func TestOverlayBarLabels_EmptyInputs(t *testing.T) {
	zoom := ZoomLevels[2]
	if got := overlayBarLabels("", nil, 10, 58, zoom); got != "" {
		t.Errorf("empty body should pass through")
	}
	if got := overlayBarLabels("x", nil, 10, 58, zoom); got != "x" {
		t.Errorf("nil texts should pass through")
	}
}

// stripANSIForTest removes lipgloss/ANSI escape sequences. Tests assert
// against visible content only — coloring is verified elsewhere.
func stripANSIForTest(s string) string {
	return ansi.Strip(s)
}

func TestRenderXLabels_24h_PerBarLabel(t *testing.T) {
	t.Parallel()
	// Make "now" a Tuesday 14:30 UTC; older buckets get date labels.
	now := time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC)
	day := func(month, d int) time.Time {
		return time.Date(2026, time.Month(month), d, 0, 0, 0, 0, time.UTC)
	}
	starts := []time.Time{
		day(4, 30), // 12 days ago → date
		day(5, 1),  // 11 days ago → date
		day(5, 11), // yesterday → "Mon"
		day(5, 12), // today → "Tue"
	}
	zoom := ZoomLevels[2] // 24h
	chartW := zoom.CanvasWidth(len(starts))
	got := renderXLabels(starts, chartW, zoom, now, dateOrderMonthFirst)

	stripped := stripANSIForTest(got)

	// Every bar gets a label, centered in its BarWidth-cols slot at
	// col = i*(BarWidth+BarGap) + (BarWidth-labelW)/2. Bars stride by
	// BarWidth+BarGap; the gap stays blank between them.
	wantLabels := []string{"04/30", "05/01", "Mon", "Tue"}
	stride := zoom.BarWidth + zoom.BarGap
	for slot, want := range wantLabels {
		col := slot*stride + (zoom.BarWidth-len(want))/2
		if col+len(want) > len(stripped) {
			t.Fatalf("slot %d: col %d + %d > len(stripped)=%d; output=%q",
				slot, col, len(want), len(stripped), stripped)
		}
		if got := stripped[col : col+len(want)]; got != want {
			t.Errorf("slot %d: at col %d expected %q, got %q\nfull: %q",
				slot, col, want, got, stripped)
		}
	}
}

func TestZoomLevels_Shape(t *testing.T) {
	t.Parallel()
	if len(ZoomLevels) != 3 {
		t.Fatalf("expected 3 zoom levels, got %d", len(ZoomLevels))
	}
	want := []ZoomLevel{
		{"15m", 15 * time.Minute, 1, 0, horizontalScrollStep},
		{"1h", time.Hour, 1, 0, horizontalScrollStep},
		{"24h", 24 * time.Hour, 10, 2, 1},
	}
	for i, w := range want {
		got := ZoomLevels[i]
		if got != w {
			t.Errorf("ZoomLevels[%d] = %+v, want %+v", i, got, w)
		}
	}
}

func TestZoomLevels_BarWidthPositive(t *testing.T) {
	t.Parallel()
	for i, z := range ZoomLevels {
		if z.BarWidth < 1 {
			t.Errorf("ZoomLevels[%d].BarWidth = %d, want >= 1", i, z.BarWidth)
		}
	}
}

// TestZoomLevel_CanvasWidth_Defensive locks in CanvasWidth's own clamp
// contract: BarWidth is treated as ≥1 and BarGap as ≥0 even when the
// ZoomLevel literal is degenerate. The mirrored per-bar invariant
// (stride ≥1) used by renderXLabels, model.visibleBuckets, and
// model.setX is covered separately by TestZoomLevel_Stride_Defensive.
// This test fails loudly if anyone removes CanvasWidth's max() guards.
func TestZoomLevel_CanvasWidth_Defensive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		zoom ZoomLevel
		n    int
		want int
	}{
		{"zero buckets", ZoomLevel{BarWidth: 10, BarGap: 2}, 0, 0},
		{"negative buckets", ZoomLevel{BarWidth: 10, BarGap: 2}, -1, 0},
		{"one bucket has no gap term", ZoomLevel{BarWidth: 10, BarGap: 2}, 1, 10},
		{"two buckets adds one gap", ZoomLevel{BarWidth: 10, BarGap: 2}, 2, 22},
		{"24h shape: 4 bars", ZoomLevel{BarWidth: 10, BarGap: 2}, 4, 46},
		{"negative BarGap clamped to 0", ZoomLevel{BarWidth: 5, BarGap: -3}, 4, 20},
		{"zero BarWidth clamped to 1", ZoomLevel{BarWidth: 0, BarGap: 2}, 3, 7},
		{"negative BarWidth clamped to 1", ZoomLevel{BarWidth: -5, BarGap: 0}, 3, 3},
		{"both negative still well-defined", ZoomLevel{BarWidth: -5, BarGap: -10}, 3, 3},
		// The exact panic case the commit message calls out: stride
		// would drop to 0 without the clamp at the per-bar sites.
		{"BarGap negates BarWidth", ZoomLevel{BarWidth: 10, BarGap: -10}, 3, 30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.zoom.CanvasWidth(tt.n); got != tt.want {
				t.Errorf("CanvasWidth(%d) on %+v = %d, want %d",
					tt.n, tt.zoom, got, tt.want)
			}
		})
	}
}

// TestZoomLevel_Stride_Defensive locks in stride()'s clamp contract:
// stride is always ≥1 regardless of ZoomLevel inputs. The three per-bar
// sites (renderXLabels, model.visibleBuckets, model.setX) route through
// stride(), so removing this guard re-introduces the integer-divide
// panic class flagged in the original safety review.
func TestZoomLevel_Stride_Defensive(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		zoom ZoomLevel
		want int
	}{
		{"15m shape", ZoomLevel{BarWidth: 1, BarGap: 0}, 1},
		{"24h shape", ZoomLevel{BarWidth: 10, BarGap: 2}, 12},
		{"zero BarWidth clamped to 1", ZoomLevel{BarWidth: 0, BarGap: 0}, 1},
		{"negative BarWidth clamped to 1", ZoomLevel{BarWidth: -5, BarGap: 2}, 3},
		{"negative BarGap clamped to 0", ZoomLevel{BarWidth: 5, BarGap: -3}, 5},
		// The exact panic case: without the BarGap clamp, stride would
		// be 10 + (-10) = 0 and visibleBuckets would divide by zero.
		{"BarGap negates BarWidth (panic-class guard)", ZoomLevel{BarWidth: 10, BarGap: -10}, 10},
		{"both negative", ZoomLevel{BarWidth: -5, BarGap: -10}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.zoom.stride()
			if got != tt.want {
				t.Errorf("stride() on %+v = %d, want %d", tt.zoom, got, tt.want)
			}
			if got < 1 {
				t.Errorf("stride() returned %d, must be ≥ 1 to avoid div-by-zero", got)
			}
		})
	}
}

// TestBuildChart_24h_CanvasWidth verifies that a 24h-zoom call with
// canvasW = zoom.CanvasWidth(n) produces output rows of exactly that
// width. Exercises WithBarWidth + WithBarGap so ntcharts neither
// auto-expands bars nor swallows the inter-bar gap.
func TestBuildChart_24h_CanvasWidth(t *testing.T) {
	t.Parallel()
	zoom := ZoomLevels[2] // 24h
	now := time.Date(2024, 5, 7, 12, 0, 0, 0, time.UTC)
	n := 4
	values := make([]float64, n)
	starts := make([]time.Time, n)
	for i := range values {
		values[i] = float64((i + 1) * 100)
		starts[i] = now.AddDate(0, 0, i-3)
	}
	peak := values[n-1]
	canvasW := zoom.CanvasWidth(n)
	out := buildChart(values, starts, peak, canvasW, 10, now, zoom, chartUnitTokens, dateOrderMonthFirst)
	rows := strings.Split(out, "\n")
	if len(rows) != 10 {
		t.Fatalf("expected 10 rows, got %d", len(rows))
	}
	for i, row := range rows {
		w := lipgloss.Width(row)
		if w != canvasW {
			t.Errorf("row %d: visual width=%d, want %d\n  %q", i, w, canvasW, row)
		}
	}
}

func TestComputeSpringSlice(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		start          int
		vpWidth        int
		stride         int
		gap            int
		wantSlice      int
		wantSpringXOff int
	}{
		{
			// Oldest edge: no bucket to the left, so left-align and let
			// the slack fall on the right (#306 chosen boundary).
			name:  "oldest edge start=0 left-aligned",
			start: 0, vpWidth: 120, stride: 12, gap: 2,
			wantSlice: 0, wantSpringXOff: 0,
		},
		{
			// THE #306 FIX: mid-scroll now pulls in the leading bucket and
			// offsets by stride-slack so the right edge stays flush.
			// slack = (120+2)%12 = 2 → xOff = 12-2 = 10.
			name:  "mid-scroll flush-right (24h)",
			start: 200, vpWidth: 120, stride: 12, gap: 2,
			wantSlice: 199, wantSpringXOff: 10,
		},
		{
			// Right-pinned: byte-identical to pre-#306 behavior.
			name:  "pinned-right slack in gap",
			start: 350, vpWidth: 120, stride: 12, gap: 2,
			wantSlice: 349, wantSpringXOff: 10,
		},
		{
			// Right-pinned, terminal width 130: slack = (128+2)%12 = 10 →
			// xOff = 12-10 = 2. Byte-identical to pre-#306 behavior.
			name:  "pinned-right slack in bar (terminal 130)",
			start: 350, vpWidth: 128, stride: 12, gap: 2,
			wantSlice: 349, wantSpringXOff: 2,
		},
		{
			// vpWidth an exact stride multiple: slack = (118+2)%12 = 0 →
			// no partial bucket, no offset.
			name:  "exact stride multiple slack=0",
			start: 200, vpWidth: 118, stride: 12, gap: 2,
			wantSlice: 200, wantSpringXOff: 0,
		},
		{
			// start=1: leading bucket is bucket 0. slack=2 → xOff=10.
			name:  "start=1 partial leading bucket (24h)",
			start: 1, vpWidth: 120, stride: 12, gap: 2,
			wantSlice: 0, wantSpringXOff: 10,
		},
		{
			// 15m/1h zoom shape (stride=1, gap=0): slack is always 0, so
			// the gap class never occurs there.
			name:  "stride=1 never has slack (15m/1h)",
			start: 50, vpWidth: 80, stride: 1, gap: 0,
			wantSlice: 50, wantSpringXOff: 0,
		},
		{
			// Defensive: stride<1 clamps to 1 (guards a degenerate ZoomLevels
			// literal) → slack=0, no partial bucket, no panic.
			name:  "defensive stride<1 clamps to 1",
			start: 5, vpWidth: 80, stride: 0, gap: 0,
			wantSlice: 5, wantSpringXOff: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSlice, gotXOff := computeSpringSlice(tt.start, tt.vpWidth, tt.stride, tt.gap)
			if gotSlice != tt.wantSlice {
				t.Errorf("sliceStart = %d, want %d", gotSlice, tt.wantSlice)
			}
			if gotXOff != tt.wantSpringXOff {
				t.Errorf("springXOff = %d, want %d", gotXOff, tt.wantSpringXOff)
			}
		})
	}
}

func TestOverlayYLabel_FadeZeroSkipsRender(t *testing.T) {
	// fade == 0 (and fade < 0 by clamp) must short-circuit and return
	// body unchanged. This is how the empty-moment frame ends up with
	// no Y-label rendered.
	body := strings.Repeat("█  ", 20)
	body = strings.Join([]string{body, body, body, body, body, body, body}, "\n") // 7 rows
	if got := overlayYLabel(body, 100_000, chartUnitTokens, 7, 0); got != body {
		t.Errorf("overlayYLabel(fade=0) modified body; expected pass-through")
	}
	if got := overlayYLabel(body, 100_000, chartUnitTokens, 7, -0.5); got != body {
		t.Errorf("overlayYLabel(fade=-0.5) modified body; expected pass-through")
	}
	// Sanity: fade == 1.0 still renders the label.
	if got := overlayYLabel(body, 100_000, chartUnitTokens, 7, 1.0); got == body {
		t.Errorf("overlayYLabel(fade=1.0) returned body unchanged; expected label to be spliced in")
	}
}

func TestBuildLineChart_NonEmpty(t *testing.T) {
	now := time.Now().UTC()
	from := now.Add(-2 * time.Hour)
	to := now

	pts5h := []cache.UtilizationPoint{
		{At: from, Pct: 10.0},
		{At: from.Add(30 * time.Minute), Pct: 30.0},
		{At: from.Add(time.Hour), Pct: 50.0},
		{At: from.Add(90 * time.Minute), Pct: 20.0},
	}
	pts7d := []cache.UtilizationPoint{
		{At: from, Pct: 5.0},
		{At: from.Add(time.Hour), Pct: 15.0},
	}

	chartW := 60
	chartH := 12
	body := buildLineChart(pts5h, pts7d, from, to, chartW, chartH, now, ZoomLevels[0], dateOrderMonthFirst, "test")
	if body == "" {
		t.Fatal("buildLineChart returned empty string")
	}
	lines := strings.Split(body, "\n")
	if len(lines) < 5 {
		t.Errorf("expected at least 5 lines, got %d", len(lines))
	}
}

func TestBuildLineChart_EmptyPoints(t *testing.T) {
	now := time.Now().UTC()
	body := buildLineChart(nil, nil, now.Add(-time.Hour), now, 60, 12, now, ZoomLevels[0], dateOrderMonthFirst, "test")
	if body == "" {
		t.Fatal("expected non-empty output for empty points (flat 100% line)")
	}
}

// TestBuildLineChart_NoBuiltinXAxis verifies SetXStep(0)/SetYStep(0)
// suppress ntcharts' built-in axis row. The historical bug (issue
// #177) rendered "0% 4 6 8 10 14 …" below the chart body — a row of
// integer column-position labels. After the fix only our renderXLabels
// row (formatted time stamps) remains.
func TestBuildLineChart_NoBuiltinXAxis(t *testing.T) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)
	to := now
	pts5h := []cache.UtilizationPoint{
		{At: from.Add(time.Hour), Pct: 10},
		{At: from.Add(12 * time.Hour), Pct: 50},
		{At: from.Add(23 * time.Hour), Pct: 90},
	}
	body := buildLineChart(pts5h, nil, from, to, 80, 14, now, ZoomLevels[1], dateOrderMonthFirst, "test")
	stripped := ansi.Strip(body)

	// A row consisting almost entirely of small integers separated by
	// spaces is the smell. Look for the canonical "0 4 6 8" prefix that
	// ntcharts emits at xStep=2 over a 0..N range.
	for _, line := range strings.Split(stripped, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "0 4 6 8") || strings.HasPrefix(trimmed, "0  4  6  8") {
			t.Errorf("buildLineChart output still contains built-in x-axis row: %q", trimmed)
		}
	}
}

// TestOverlayYTicks pins the post-#250 line-chart label set: 100% at
// the top and " 50%" at the geometric midpoint. The "  0%" baseline
// tick is dropped — the chart's bottom edge conveys the zero level
// implicitly.
//
// The previous test's positive assertion strings.Contains(result,"0%")
// was a false positive — "100%" contains "0%" as a substring — so the
// negative assertion below uses the exact dropped label "  0%" (two
// leading spaces) to ensure it doesn't match the surviving "100%".
func TestOverlayYTicks(t *testing.T) {
	t.Parallel()
	chartH := 12
	lines := make([]string, chartH)
	for i := range lines {
		lines[i] = strings.Repeat(" ", 60)
	}
	body := strings.Join(lines, "\n")

	result := overlayYTicks(body, chartH, 1.0)
	stripped := stripANSIForTest(result)

	if !strings.Contains(stripped, "100%") {
		t.Error("expected 100% label in output")
	}
	if !strings.Contains(stripped, " 50%") {
		t.Error("expected ' 50%' label in output")
	}
	if strings.Contains(stripped, "  0%") {
		t.Error("did not expect '  0%' label after #250 — issue dropped the baseline tick")
	}
}

func BenchmarkBuildLineChart(b *testing.B) {
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// Realistic sample cadence: ~5 minute spacing.
	mkPoints := func(span time.Duration) []cache.UtilizationPoint {
		n := int(span / (5 * time.Minute))
		out := make([]cache.UtilizationPoint, n)
		for i := range n {
			out[i] = cache.UtilizationPoint{
				At:  now.Add(-span + time.Duration(i)*5*time.Minute),
				Pct: 50 + float64(i%50),
			}
		}
		return out
	}

	cases := []struct {
		name    string
		canvasW int
		span    time.Duration
		zoom    ZoomLevel
	}{
		{"w100_24h", 100, 24 * time.Hour, ZoomLevels[1]},
		{"w1000_7d", 1000, 7 * 24 * time.Hour, ZoomLevels[1]},
		{"w2880_30d_15m", 2880, 30 * 24 * time.Hour, ZoomLevels[0]},
		{"w5000_30d_15m", 5000, 30 * 24 * time.Hour, ZoomLevels[0]},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			pts5h := mkPoints(tc.span)
			pts7d := mkPoints(tc.span)
			from := now.Add(-tc.span)
			to := now

			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = buildLineChart(pts5h, pts7d, from, to, tc.canvasW, 20, now, tc.zoom, dateOrderMonthFirst, "test")
			}
		})
	}
}

func TestColumnToTime_RoundTrip(t *testing.T) {
	from := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)

	tests := []struct {
		name    string
		canvasW int
		col     int
	}{
		{"narrow_canvas", 24, 0},
		{"narrow_canvas_mid", 24, 12},
		{"narrow_canvas_end", 24, 23},
		{"wide_canvas_start", 5000, 0},
		{"wide_canvas_mid", 5000, 2500},
		{"wide_canvas_end", 5000, 4999},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotT := columnToTime(tt.col, tt.canvasW, from, to)
			gotCol := timeToColumn(gotT, tt.canvasW, from, to)
			if absInt(gotCol-tt.col) > 1 {
				t.Errorf("round-trip col=%d -> t=%v -> col=%d (canvasW=%d); want within +-1",
					tt.col, gotT, gotCol, tt.canvasW)
			}
		})
	}
}

func TestColumnToTime_Clamps(t *testing.T) {
	from := time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)

	if got := columnToTime(-5, 100, from, to); !got.Equal(from) {
		t.Errorf("columnToTime(-5) = %v; want from = %v", got, from)
	}
	if got := columnToTime(150, 100, from, to); !got.Equal(to) {
		t.Errorf("columnToTime(150, canvasW=100) = %v; want to = %v", got, to)
	}
	if got := columnToTime(50, 0, from, to); !got.Equal(from) {
		t.Errorf("columnToTime(50, canvasW=0) = %v; want from = %v", got, from)
	}
	if got := columnToTime(50, -1, from, to); !got.Equal(from) {
		t.Errorf("columnToTime(50, canvasW=-1) = %v; want from = %v", got, from)
	}

	if got := timeToColumn(from.Add(-time.Hour), 100, from, to); got != 0 {
		t.Errorf("timeToColumn(before from) = %d; want 0", got)
	}
	if got := timeToColumn(to.Add(time.Hour), 100, from, to); got != 100 {
		t.Errorf("timeToColumn(after to) = %d; want 100 (canvasW)", got)
	}
	if got := timeToColumn(from.Add(30*time.Minute), 0, from, to); got != 0 {
		t.Errorf("timeToColumn(canvasW=0) = %d; want 0", got)
	}
}

func TestBucketCountInRange(t *testing.T) {
	tests := []struct {
		name string
		from time.Time
		to   time.Time
		dur  time.Duration
		want int
	}{
		{
			"15m_one_hour",
			time.Date(2026, 5, 15, 9, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 15, 10, 0, 0, 0, time.UTC),
			15 * time.Minute,
			4,
		},
		{
			"1h_one_day",
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 16, 0, 0, 0, 0, time.UTC),
			time.Hour,
			24,
		},
		{
			"24h_seven_days_local",
			time.Date(2026, 5, 8, 0, 0, 0, 0, time.Local),
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.Local),
			24 * time.Hour,
			7,
		},
		{
			"empty_range",
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
			time.Hour,
			0,
		},
		{
			"to_before_from",
			time.Date(2026, 5, 15, 1, 0, 0, 0, time.UTC),
			time.Date(2026, 5, 15, 0, 0, 0, 0, time.UTC),
			time.Hour,
			0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := bucketCountInRange(tt.from, tt.to, tt.dur); got != tt.want {
				t.Errorf("bucketCountInRange(%v, %v, %v) = %d; want %d",
					tt.from, tt.to, tt.dur, got, tt.want)
			}
		})
	}
}

// absInt is a small int absolute-value helper used by helper round-trip tests.
func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// TestBuildLineChart_5hAnd7dShareScale guards against issue #194 —
// ntcharts' timeserieslinechart caches each dataset's buffer scale
// at creation time. The default 5h dataset is created during
// timeserieslinechart.New(...), before SetXStep(0)/SetYStep(0) grow
// the graph area; the 7d dataset is created later via PushDataSet,
// after the steps. Without an explicit rescale, the two datasets
// carry different scale factors and render at different rows and
// columns despite identical data. SetViewTimeRange triggers
// rescaleData() — placing it after the step calls anchors the
// default dataset to the post-step geometry.
//
// chartW=200, chartH=18 reproduces the issue body's instrumented
// geometry (barsH=17) so a pre-fix run gives the canonical row 2 vs
// row 1, col ~198 vs col ~200 split.
//
// Overdraw constraint: 5h ("default") deliberately wins shared
// braille cells via the explicit draw order in buildLineChart
// (issue #196). The column_shared_scale and row_shared_scale
// subtests below place the two series in non-overlapping cells
// so the per-series rightmost/topmost assertions are not eaten
// by overdraw; the adjacent overlap_5h_wins subtest exercises
// the overdraw invariant directly with identical-data series.
func TestBuildLineChart_5hAnd7dShareScale(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, true)

	const chartW = 200
	const chartH = 18

	now := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	from := now.Add(-24 * time.Hour)
	to := now

	// Derive per-series SGR opening sequences from the same
	// colorChartRemaining* constants the production code uses.
	// Rendering a known sentinel through each style lets us slice
	// out the leading SGR-open and the trailing SGR-close that
	// lipgloss emits. The test then scans the styled chart body
	// for those exact byte sequences. If the palette ever changes,
	// this derivation tracks it automatically.
	const sentinel = "X"
	sgrOpen5h, sgrClose5h := splitSGR(lipgloss.NewStyle().Foreground(colorChartRemaining5h).Render(sentinel), sentinel)
	sgrOpen7d, sgrClose7d := splitSGR(lipgloss.NewStyle().Foreground(colorChartRemaining7d).Render(sentinel), sentinel)

	if sgrOpen5h == "" || sgrOpen7d == "" {
		t.Fatalf("could not derive SGR sequences (5h=%q, 7d=%q) — lipgloss color profile may not be forced", sgrOpen5h, sgrOpen7d)
	}
	if sgrOpen5h == sgrOpen7d {
		t.Fatalf("5h and 7d SGR sequences are identical (%q) — palette would make the test ambiguous", sgrOpen5h)
	}

	t.Run("column_shared_scale", func(t *testing.T) {
		// Both series span the full 24h timeline but at different
		// Y values so they live on different rows. No overdraw.
		// Post-fix invariant: both lines end at the same rightmost
		// canvas column because both datasets share the same X
		// scale factor.
		pts5h := makeUniformPoints(from, to, 24, 4.0)  // Y = 0.96
		pts7d := makeUniformPoints(from, to, 24, 20.0) // Y = 0.80

		body := buildLineChart(pts5h, pts7d, from, to, chartW, chartH, now, ZoomLevels[1], dateOrderMonthFirst, "test")

		scan := scanSeries(body, sgrOpen5h, sgrClose5h, sgrOpen7d, sgrClose7d)

		if scan.count5h == 0 {
			t.Fatalf("no 5h cells found — test setup broken (palette mismatch or chart empty)")
		}
		if scan.count7d == 0 {
			t.Fatalf("no 7d cells found — test setup broken (palette mismatch or chart empty)")
		}
		if scan.rightmost5h != scan.rightmost7d {
			t.Errorf("issue #194: 5h rightmost col=%d != 7d rightmost col=%d (datasets do not share X scale)",
				scan.rightmost5h, scan.rightmost7d)
		}
	})

	t.Run("row_shared_scale", func(t *testing.T) {
		// Both series at the same Y, but time-shifted into
		// non-overlapping halves of the timeline. No overdraw.
		// Post-fix invariant: both horizontal segments sit on the
		// same canvas row because both datasets share the same Y
		// scale factor.
		mid := from.Add(12 * time.Hour)
		pts5h := makeUniformPoints(from, mid, 12, 4.0)
		pts7d := makeUniformPoints(mid, to, 12, 4.0)

		body := buildLineChart(pts5h, pts7d, from, to, chartW, chartH, now, ZoomLevels[1], dateOrderMonthFirst, "test")

		scan := scanSeries(body, sgrOpen5h, sgrClose5h, sgrOpen7d, sgrClose7d)

		if scan.count5h == 0 {
			t.Fatalf("no 5h cells found — test setup broken (palette mismatch or chart empty)")
		}
		if scan.count7d == 0 {
			t.Fatalf("no 7d cells found — test setup broken (palette mismatch or chart empty)")
		}
		if scan.topmost5h != scan.topmost7d {
			t.Errorf("issue #194: 5h topmost row=%d != 7d topmost row=%d (datasets do not share Y scale)",
				scan.topmost5h, scan.topmost7d)
		}
	})

	t.Run("overlap_5h_wins", func(t *testing.T) {
		// Both series have IDENTICAL data: same Y, same time
		// range, same point count. Every braille cell that
		// holds a 5h dot also holds a 7d dot — maximum
		// overdraw. Post-fix invariant: 5h ("default") wins
		// every shared cell, so the rendered body must contain
		// 5h-styled cells but ZERO 7d-styled cells.
		//
		// This test guards against a future regression where
		// someone switches back to DrawBrailleAll (whose
		// internal sort.Strings would reshuffle the winner the
		// moment a third dataset's name sorts after "default")
		// or rearranges the explicit name list in buildLineChart.
		pts5h := makeUniformPoints(from, to, 24, 12.0) // Y = 0.88
		pts7d := makeUniformPoints(from, to, 24, 12.0) // identical

		body := buildLineChart(pts5h, pts7d, from, to, chartW, chartH, now, ZoomLevels[1], dateOrderMonthFirst, "test")

		scan := scanSeries(body, sgrOpen5h, sgrClose5h, sgrOpen7d, sgrClose7d)

		if scan.count5h == 0 {
			t.Fatalf("no 5h cells found — test setup broken (palette mismatch or chart empty)")
		}
		if scan.count7d != 0 {
			t.Errorf("issue #196: 5h must consistently win shared cells; got %d visible 7d cells (expected 0)", scan.count7d)
		}
	})
}

// makeUniformPoints produces n uniformly-spaced cache.UtilizationPoint
// across [start, end), all at the given pct. Helper for
// TestBuildLineChart_5hAnd7dShareScale.
func makeUniformPoints(start, end time.Time, n int, pct float64) []cache.UtilizationPoint {
	if n < 1 {
		return nil
	}
	out := make([]cache.UtilizationPoint, 0, n)
	step := end.Sub(start) / time.Duration(n)
	for i := 0; i < n; i++ {
		out = append(out, cache.UtilizationPoint{
			At:  start.Add(time.Duration(i) * step),
			Pct: pct,
		})
	}
	return out
}

// splitSGR takes a string of the form "<SGR-open><sentinel><SGR-close>"
// (what lipgloss emits when rendering a styled sentinel) and returns
// the opening SGR sequence and the closing SGR sequence. Returns
// empty strings if the sentinel is not found.
func splitSGR(rendered, sentinel string) (open, close string) {
	idx := strings.Index(rendered, sentinel)
	if idx < 0 {
		return "", ""
	}
	return rendered[:idx], rendered[idx+len(sentinel):]
}

// seriesScan holds the per-series probe results from scanSeries.
type seriesScan struct {
	count5h, count7d         int
	topmost5h, topmost7d     int
	rightmost5h, rightmost7d int
}

// scanSeries walks the styled body line by line, tracks the currently
// active foreground color via the supplied SGR open/close sequences,
// and records each non-space cell's (row, visual-col) tagged by series.
// topmost is the smallest row index seen for the series; rightmost is
// the largest visual column index. Both default to -1 if the series
// has no cells.
//
// Each line is scanned with fresh ANSI state. The walk uses
// ansi.DecodeSequence to step through the line in ANSI-aware chunks:
// zero-width chunks are control sequences (SGR changes update the
// active tag), positive-width chunks are printable runes that advance
// the column counter.
func scanSeries(body, sgrOpen5h, sgrClose5h, sgrOpen7d, sgrClose7d string) seriesScan {
	out := seriesScan{
		topmost5h: -1, topmost7d: -1,
		rightmost5h: -1, rightmost7d: -1,
	}

	for row, line := range strings.Split(body, "\n") {
		parser := ansi.NewParser()
		state := byte(0)
		col := 0
		active := "" // "5h", "7d", or ""

		remaining := line
		for len(remaining) > 0 {
			seq, width, n, newState := ansi.DecodeSequence(remaining, state, parser)
			state = newState
			if width > 0 {
				if seq != " " {
					switch active {
					case "5h":
						if out.topmost5h < 0 {
							out.topmost5h = row
						}
						if col > out.rightmost5h {
							out.rightmost5h = col
						}
						out.count5h++
					case "7d":
						if out.topmost7d < 0 {
							out.topmost7d = row
						}
						if col > out.rightmost7d {
							out.rightmost7d = col
						}
						out.count7d++
					}
				}
				col += width
			} else {
				// Escape sequence. Update active tag on SGR open/close.
				switch {
				case seq == sgrOpen5h:
					active = "5h"
				case seq == sgrOpen7d:
					active = "7d"
				case seq == sgrClose5h || seq == sgrClose7d:
					active = ""
				}
			}
			remaining = remaining[n:]
		}
	}
	return out
}

func TestSlicePointsInRange(t *testing.T) {
	base := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC)
	mk := func(offsets ...time.Duration) []cache.UtilizationPoint {
		out := make([]cache.UtilizationPoint, len(offsets))
		for i, off := range offsets {
			out[i] = cache.UtilizationPoint{At: base.Add(off), Pct: float64(i)}
		}
		return out
	}

	tests := []struct {
		name string
		pts  []cache.UtilizationPoint
		from time.Time
		to   time.Time
		want []time.Duration // offsets that should survive
	}{
		{
			name: "empty input",
			pts:  nil,
			from: base, to: base.Add(time.Hour),
			want: nil,
		},
		{
			name: "all in range",
			pts:  mk(0, 10*time.Minute, 20*time.Minute, 30*time.Minute),
			from: base.Add(-time.Hour), to: base.Add(time.Hour),
			want: []time.Duration{0, 10 * time.Minute, 20 * time.Minute, 30 * time.Minute},
		},
		{
			name: "middle slice with padding",
			pts:  mk(0, 10*time.Minute, 20*time.Minute, 30*time.Minute, 40*time.Minute),
			from: base.Add(15 * time.Minute), to: base.Add(25 * time.Minute),
			// Pad by one on each side: idx 1 (10m) + idx 2 (20m) + idx 3 (30m).
			want: []time.Duration{10 * time.Minute, 20 * time.Minute, 30 * time.Minute},
		},
		{
			name: "range before all points",
			pts:  mk(time.Hour, 2*time.Hour),
			from: base, to: base.Add(30 * time.Minute),
			// Empty visible range: return the closest single point (idx 0 = 1h).
			want: []time.Duration{time.Hour},
		},
		{
			name: "range after all points",
			pts:  mk(0, 10*time.Minute),
			from: base.Add(time.Hour), to: base.Add(2 * time.Hour),
			// Empty visible range: return the closest single point (idx 1 = 10m, last).
			want: []time.Duration{10 * time.Minute},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := slicePointsInRange(tt.pts, tt.from, tt.to)
			if len(got) != len(tt.want) {
				t.Fatalf("len=%d, want %d (got=%v)", len(got), len(tt.want), got)
			}
			for i, off := range tt.want {
				wantAt := base.Add(off)
				if !got[i].At.Equal(wantAt) {
					t.Errorf("[%d].At = %v, want %v", i, got[i].At, wantAt)
				}
			}
		})
	}
}

func TestNiceCeilingFloat(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		peak float64
		want float64
	}{
		{"zero returns zero", 0, 0},
		{"negative returns zero", -10, 0},
		// Sub-1 (cost mode), exponent k = -2.
		{"0.04 rises to 0.05", 0.04, 0.05},
		{"0.05 stays 0.05", 0.05, 0.05},
		{"0.06 rises to 0.07", 0.06, 0.07},
		// k = -1.
		{"0.12 rises to 0.2", 0.12, 0.2},
		{"0.71 rises to 1.0", 0.71, 1.0},
		// k = 0.
		{"1.0 stays 1", 1.0, 1.0},
		{"1.4 rises to 2", 1.4, 2.0},
		{"2.0 stays 2", 2.0, 2.0},
		{"4.7 rises to 5", 4.7, 5.0},
		{"7.0 stays 7", 7.0, 7.0},
		{"7.1 rises to 10", 7.1, 10.0},
		// k = 1.
		{"23 rises to 30", 23, 30.0},
		// k = 4.
		{"70001 rises to 100000", 70001, 100_000.0},
		{"87000 rises to 100000", 87000, 100_000.0},
		{"70000 stays 70000", 70000, 70_000.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := niceCeilingFloat(tt.peak)
			// Sub-1 results carry binary-FP residue from math.Pow10(-1/-2);
			// allow 1e-9 slack rather than asserting exact equality. Same
			// pattern as TestNiceFloorFloat.
			if diff := got - tt.want; diff < -1e-9 || diff > 1e-9 {
				t.Errorf("niceCeilingFloat(%v) = %v, want %v", tt.peak, got, tt.want)
			}
		})
	}
}

func TestPadYLabel(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want string
	}{
		{"", "     "},
		{"1", "    1"},
		{"99", "   99"},
		{"$99", "  $99"},
		{"100k", " 100k"},
		{"$0.45", "$0.45"},   // already at slot width — unchanged
		{"$1000k", "$1000k"}, // wider than slot — unchanged
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()
			if got := padYLabel(tt.in); got != tt.want {
				t.Errorf("padYLabel(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestYLabelMidFloor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		unit chartUnit
		want float64
	}{
		{"cost", chartUnitCost, 0.005},
		{"tokens", chartUnitTokens, 1.0},
		{"unknown unit returns 0", chartUnit(999), 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := yLabelMidFloor(tt.unit); got != tt.want {
				t.Errorf("yLabelMidFloor(%v) = %v, want %v", tt.unit, got, tt.want)
			}
		})
	}
}

// TestBuildChart_NiceCeilingYRange confirms buildChart bumps its Y
// range up to niceCeilingFloat(peak) so the tallest bar sits BELOW the
// canvas top whenever peak < ceiling. The peak column should therefore
// have at least one fully-blank top row.
//
// Before #250: maxValue = peak directly → tallest bar fills 100%, no
// top headroom. After: maxValue = niceCeilingFloat(peak) → headroom
// proportional to peak/ceiling.
func TestBuildChart_NiceCeilingYRange(t *testing.T) {
	t.Parallel()
	// peak = 87000 → niceCeiling = 100000 → fill ratio 0.87.
	// chartH = 12 → barsH = 11 → tallest bar ≈ round(0.87 × 11) = 10 rows
	// → row 0 of the bars area should be blank in the peak column.
	now := time.Now().UTC().Truncate(15 * time.Minute)
	values := []float64{87_000}
	starts := []time.Time{now}
	zoom := ZoomLevels[0] // BarWidth=1, BarGap=0
	chartW := zoom.CanvasWidth(len(values)) // = 1
	chartH := 12
	out := buildChart(values, starts, 87_000, chartW, chartH, now, zoom, chartUnitTokens, dateOrderMonthFirst)
	rows := strings.Split(out, "\n")
	// The trailing row is the X-axis label row (chartH >= 6 turns it
	// on); the first chartH-1 rows are the bars area.
	barsH := chartH - 1
	if len(rows) < barsH {
		t.Fatalf("expected at least %d bar rows, got %d", barsH, len(rows))
	}
	// Strip ANSI on row 0 of the bars area. With niceCeiling=100k and
	// peak=87k, the bar leaves the top row empty in the peak column.
	top := stripANSIForTest(rows[0])
	if strings.TrimSpace(top) != "" {
		t.Errorf("expected row 0 of bars area to be blank above peak (niceCeiling headroom); got %q", top)
	}
}

// TestFormatXLabel_LocalZone pins issue #253: 15m/1h zoom labels render in
// now.Location(), not UTC. Buckets are UTC-aligned (Approach A); the labels
// convert to local. now is in America/New_York (whole-hour offset, EDT =
// UTC-4 on this July date), passed in via the now arg so no global
// time.Local mutation is needed and the test stays parallel-safe.
func TestFormatXLabel_LocalZone(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	now := time.Date(2026, 7, 14, 14, 30, 0, 0, ny) // Tue 2026-07-14 14:30 EDT
	// utc builds a UTC-aligned bucket instant (what pkg/cache produces on
	// 15m/1h) at the given UTC wall-clock on 2026-07-14.
	utc := func(h, m int) time.Time {
		return time.Date(2026, 7, 14, h, m, 0, 0, time.UTC)
	}
	zm15 := ZoomLevels[0] // "15m"
	z1h := ZoomLevels[1]  // "1h"
	z24 := ZoomLevels[2]  // "24h"

	tests := []struct {
		name  string
		t     time.Time
		zoom  ZoomLevel
		order dateOrder
		want  string
	}{
		// 15m: hour ticks on local 3-hour cadence. local 09:00 EDT = 13:00 UTC.
		{"15m local 09:00 (=13:00 UTC)", utc(13, 0), zm15, dateOrderMonthFirst, "09:00"},
		// local 12:00 EDT = 16:00 UTC.
		{"15m local 12:00 (=16:00 UTC)", utc(16, 0), zm15, dateOrderMonthFirst, "12:00"},
		// local midnight 00:00 EDT = 04:00 UTC → day stamp (Tue, within 7d).
		{"15m local midnight (=04:00 UTC) → Tue", utc(4, 0), zm15, dateOrderMonthFirst, "Tue"},
		// local 13:00 EDT = 17:00 UTC, off the 3-hour cadence → "".
		{"15m local 13:00 off-cadence (=17:00 UTC)", utc(17, 0), zm15, dateOrderMonthFirst, ""},

		// 1h: midnight-only day stamp. local midnight = 04:00 UTC → Tue.
		{"1h local midnight (=04:00 UTC) → Tue", utc(4, 0), z1h, dateOrderMonthFirst, "Tue"},
		// UTC midnight = local 20:00 prev day → NOT a local midnight → "".
		{"1h UTC midnight is local 20:00 → empty", utc(0, 0), z1h, dateOrderMonthFirst, ""},
		// non-midnight, non-zero minute → "".
		{"1h non-midnight → empty", utc(13, 30), z1h, dateOrderMonthFirst, ""},

		// 24h: bucket is already time.Local; conversion is a no-op. Confirm unaffected.
		{"24h local-zone bucket → Tue", time.Date(2026, 7, 14, 0, 0, 0, 0, ny), z24, dateOrderMonthFirst, "Tue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatXLabel(tt.t, tt.zoom, now, tt.order)
			if got != tt.want {
				t.Errorf("formatXLabel(%s, %s, %v) = %q, want %q",
					tt.t.Format(time.RFC3339), tt.zoom.Label, tt.order, got, tt.want)
			}
		})
	}
}

// TestDateLabel_LocalZone pins that dateLabel renders the bucket time in
// now.Location(). The instants below are UTC; their NY-local calendar day
// is what the label must reflect.
func TestDateLabel_LocalZone(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	now := time.Date(2026, 7, 14, 14, 30, 0, 0, ny) // Tue EDT
	// 2026-07-13 00:00 EDT = 04:00 UTC (Mon, within past 7 days).
	monUTC := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	if got := dateLabel(monUTC, now, dateOrderMonthFirst); got != "Mon" {
		t.Errorf("dateLabel recent = %q, want Mon", got)
	}
	// 2026-07-01 00:00 EDT = 04:00 UTC (older than 7 days → date).
	oldUTC := time.Date(2026, 7, 1, 4, 0, 0, 0, time.UTC)
	if got := dateLabel(oldUTC, now, dateOrderMonthFirst); got != "07/01" {
		t.Errorf("dateLabel old MonthFirst = %q, want 07/01", got)
	}
	if got := dateLabel(oldUTC, now, dateOrderDayFirst); got != "01/07" {
		t.Errorf("dateLabel old DayFirst = %q, want 01/07", got)
	}
}

// TestChartXLabel_CrossZoomDayStampAgreement pins the issue #253 contract:
// for the same instant at local midnight, the 1h-zoom day stamp (emitted on
// the UTC instant of local midnight) equals the 24h-zoom day boundary stamp
// (emitted on the time.Local bucket). Pre-fix the 1h zoom stamped UTC
// midnight instead, so the two disagreed.
func TestChartXLabel_CrossZoomDayStampAgreement(t *testing.T) {
	t.Parallel()
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("load tz: %v", err)
	}
	now := time.Date(2026, 7, 14, 14, 30, 0, 0, ny) // Tue EDT
	z1h := ZoomLevels[1]
	z24 := ZoomLevels[2]

	// Local midnight of Fri 2026-07-10 (within the past 7 days → weekday).
	localMidnight := time.Date(2026, 7, 10, 0, 0, 0, 0, ny)

	hourStamp := formatXLabel(localMidnight.UTC(), z1h, now, dateOrderMonthFirst)
	dayStamp := formatXLabel(localMidnight, z24, now, dateOrderMonthFirst)
	if hourStamp != dayStamp {
		t.Errorf("cross-zoom day stamp mismatch: 1h=%q 24h=%q", hourStamp, dayStamp)
	}
	if hourStamp != "Fri" {
		t.Errorf("day stamp = %q, want Fri", hourStamp)
	}
}

func TestPaddedFrom(t *testing.T) {
	t.Parallel()
	// 1h zoom (ZoomLevels[1]): walk back 5 buckets == 5h, spanning 5 buckets.
	to := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	if got, want := paddedFrom(to, ZoomLevels[1], 5), to.Add(-5*time.Hour); !got.Equal(want) {
		t.Errorf("paddedFrom 1h = %v, want %v", got, want)
	}
	if got := bucketCountInRange(paddedFrom(to, ZoomLevels[1], 5), to, time.Hour); got != 5 {
		t.Errorf("1h span = %d buckets, want 5", got)
	}
	// 24h zoom (ZoomLevels[2]): walk back whole local-tz days (DST-correct).
	toDay := cache.DayStartLocal(time.Now()).AddDate(0, 0, 1)
	if got := bucketCountInRange(paddedFrom(toDay, ZoomLevels[2], 3), toDay, 24*time.Hour); got != 3 {
		t.Errorf("24h span = %d days, want 3", got)
	}
	// n <= 0 returns `to` unchanged.
	if got := paddedFrom(to, ZoomLevels[1], 0); !got.Equal(to) {
		t.Errorf("paddedFrom n=0 = %v, want %v (unchanged)", got, to)
	}
}
