package tui

import (
	"fmt"
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

func TestOverlayYLabel_InjectsAtNiceFloorRow(t *testing.T) {
	t.Parallel()
	// peak = 87000 → niceFloorFloat(87000) = 70000 → label "70k".
	// chartH=6 → barsH=5 → row = 5 - round(70000/87000 * 5) = 1.
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE\nFFFFFFFFFF"
	out := overlayYLabel(body, 87_000, chartUnitTokens, 6, 1.0)
	rows := strings.Split(out, "\n")
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows, got %d:\n%q", len(rows), out)
	}
	if !strings.Contains(rows[1], "70k") {
		t.Errorf("expected '70k' on row 1, got %q", rows[1])
	}
	// Other rows untouched.
	for i, r := range []string{"AAAAAAAAAA", "CCCCCCCCCC", "DDDDDDDDDD", "EEEEEEEEEE", "FFFFFFFFFF"} {
		idx := i
		if idx >= 1 {
			idx++ // skip row 1
		}
		if !strings.Contains(rows[idx], r) {
			t.Errorf("row %d should still contain %q, got %q", idx, r, rows[idx])
		}
	}
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
		{"15m", 15 * time.Minute, 1, 0},
		{"1h", time.Hour, 1, 0},
		{"24h", 24 * time.Hour, 10, 2},
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
		prevLongest    int
		vpWidth        int
		stride         int
		wantSlice      int
		wantSpringXOff int
	}{
		{
			name:           "start=0 unclamped",
			start:          0,
			prevLongest:    4318,
			vpWidth:        120,
			stride:         12,
			wantSlice:      0,
			wantSpringXOff: 0,
		},
		{
			name:           "mid-scroll unclamped",
			start:          200,
			prevLongest:    4318,
			vpWidth:        120,
			stride:         12,
			wantSlice:      200,
			wantSpringXOff: 0,
		},
		{
			name:           "pinned-right slack in gap",
			start:          350,
			prevLongest:    4318,
			vpWidth:        120,
			stride:         12,
			wantSlice:      349,
			wantSpringXOff: 10,
		},
		{
			name:           "pinned-right slack in bar (terminal 130)",
			start:          350,
			prevLongest:    4318,
			vpWidth:        128,
			stride:         12,
			wantSlice:      349,
			wantSpringXOff: 2,
		},
		{
			name:           "canvas fits in viewport, start=0",
			start:          0,
			prevLongest:    60,
			vpWidth:        120,
			stride:         12,
			wantSlice:      0,
			wantSpringXOff: 0,
		},
		{
			// Critical: exercises the defensive springXOff < 0 clamp.
			// Without the clamp, springXOff would be:
			//   desiredXOffset = 5*12 = 60
			//   actualXOffset = min(60, max(0, 60-120)) = min(60, 0) = 0
			//   sliceStart = 4, springXOff = 0 - 4*12 = -48
			name:           "canvas fits in viewport, start=5 (negative-pre-clamp path)",
			start:          5,
			prevLongest:    60,
			vpWidth:        120,
			stride:         12,
			wantSlice:      4,
			wantSpringXOff: 0,
		},
		{
			// start=1, tiny canvas: desiredXOffset = 1*12 = 12,
			// actualXOffset = min(12, max(0, 14*12-120)) = min(12, max(0,-6)) = min(12,0) = 0.
			// 0 < 12, so the if-branch fires: sliceStart = 0/12 = 0,
			// springXOff = 0 - 0*12 = 0. The pre-clamp value is already 0,
			// so the defensive springXOff < 0 clamp is NOT exercised here.
			name:           "start=1 sliceStart drops to zero (if-branch arithmetic)",
			start:          1,
			prevLongest:    14,
			vpWidth:        120,
			stride:         12,
			wantSlice:      0,
			wantSpringXOff: 0,
		},
		{
			// stride=1 (matches 15m/1h zoom shape: BarWidth=1, BarGap=0).
			// desiredXOffset = 50*1 = 50,
			// actualXOffset = min(50, max(0, 200*1-80)) = min(50, 120) = 50.
			// 50 == 50, so the if-branch does NOT fire: sliceStart = 50,
			// springXOff = 50 - 50*1 = 0.
			name:           "stride=1 mid-scroll (15m/1h zoom shape)",
			start:          50,
			prevLongest:    200,
			vpWidth:        80,
			stride:         1,
			wantSlice:      50,
			wantSpringXOff: 0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotSlice, gotXOff := computeSpringSlice(tt.start, tt.prevLongest, tt.vpWidth, tt.stride)
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
	body := buildLineChart(pts5h, pts7d, from, to, chartW, chartH, now, ZoomLevels[0], dateOrderMonthFirst)
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
	body := buildLineChart(nil, nil, now.Add(-time.Hour), now, 60, 12, now, ZoomLevels[0], dateOrderMonthFirst)
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
	body := buildLineChart(pts5h, nil, from, to, 80, 14, now, ZoomLevels[1], dateOrderMonthFirst)
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

func TestOverlayYTicks(t *testing.T) {
	chartH := 12
	lines := make([]string, chartH)
	for i := range lines {
		lines[i] = strings.Repeat(" ", 60)
	}
	body := strings.Join(lines, "\n")

	result := overlayYTicks(body, chartH, 1.0)
	if !strings.Contains(result, "100%") {
		t.Error("expected 100% label in output")
	}
	if !strings.Contains(result, "50%") {
		t.Error("expected 50% label in output")
	}
	if !strings.Contains(result, "0%") {
		t.Error("expected 0% label in output")
	}
}

func BenchmarkBuildLineChart(b *testing.B) {
	now := time.Now().UTC()
	from := now.Add(-10 * 24 * time.Hour)
	for _, n := range []int{500, 2000, 5000} {
		pts5h := make([]cache.UtilizationPoint, n)
		pts7d := make([]cache.UtilizationPoint, n)
		for i := range n {
			t := from.Add(time.Duration(i) * 3 * time.Minute)
			pts5h[i] = cache.UtilizationPoint{At: t, Pct: float64(i % 100)}
			pts7d[i] = cache.UtilizationPoint{At: t, Pct: float64((i * 3) % 100)}
		}
		b.Run(fmt.Sprintf("pts=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = buildLineChart(pts5h, pts7d, from, now, 200, 20, now, ZoomLevels[0], dateOrderMonthFirst)
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
