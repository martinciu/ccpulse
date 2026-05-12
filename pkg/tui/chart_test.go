package tui

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// sinkString prevents the compiler from eliding the buildChart call
// in BenchmarkBuildChart when its return value is otherwise unused.
var sinkString string

// syntheticChartInput produces n contiguous 5-minute chart inputs with
// deterministic, varied values so heatColor exercises all three colour
// bands. Anchored to a 3-hour clock boundary so the 15m zoom (used by
// BenchmarkBuildChart and BenchmarkRenderXLabels) hits a label tick
// every 36th bucket — exercising renderXLabels' label-write loop.
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
				sinkString = buildChart(values, starts, peak, n, 20, now, ZoomLevels[1], chartUnitTokens)
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
				sinkString = renderXLabels(starts, n, ZoomLevels[1], now)
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

func TestBuildChart_ContainsXLabelsAndNowMarker(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	bs := []cache.TokenBucket{
		{BucketStart: now.Add(-30 * time.Minute), Tokens: 1000},
		{BucketStart: now.Add(-25 * time.Minute), Tokens: 2000},
		{BucketStart: now.Add(-20 * time.Minute), Tokens: 1500},
		{BucketStart: now.Add(-15 * time.Minute), Tokens: 3000},
		{BucketStart: now.Add(-10 * time.Minute), Tokens: 2500},
		{BucketStart: now.Add(-5 * time.Minute), Tokens: 4500},
		{BucketStart: now, Tokens: 3500},
	}
	values, starts, peak := projectBuckets(bs)
	out := buildChart(values, starts, peak, len(bs), 10, now, ZoomLevels[0], chartUnitTokens)
	if !strings.Contains(out, "▼ now") {
		t.Errorf("expected '▼ now' marker in chart output:\n%s", out)
	}
	rows := strings.Split(out, "\n")
	if len(rows) != 10 {
		t.Errorf("expected 10 rows (chartH), got %d", len(rows))
	}
	if !strings.Contains(rows[len(rows)-1], "▼ now") {
		t.Errorf("▼ now should be on the last row:\nlast row: %q\nfull:\n%s",
			rows[len(rows)-1], out)
	}
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
	out := buildChart(values, starts, peak, len(bs), 5, now, ZoomLevels[0], chartUnitTokens)
	if strings.Contains(out, "▼ now") {
		t.Errorf("expected no '▼ now' marker when chartH=5; X labels should be dropped:\n%s", out)
	}
	rows := strings.Split(out, "\n")
	if len(rows) != 5 {
		t.Errorf("expected 5 rows (chartH), got %d", len(rows))
	}
}

func TestRenderXLabels_NowTruncatesAtTinyChartW(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	buckets := []cache.TokenBucket{{BucketStart: now}}
	// chartW=1 can't fit "▼ now" (5 cols); only ▼ should appear.
	_, starts, _ := projectBuckets(buckets)
	got := renderXLabels(starts, 1, ZoomLevels[0], now)
	if !strings.Contains(got, "▼") {
		t.Errorf("expected ▼ at chartW=1, got %q", got)
	}
	if strings.Contains(got, "▼ now") {
		t.Errorf("expected truncated ▼ at chartW=1, not full '▼ now', got %q", got)
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
	got := renderXLabels(starts, 3, ZoomLevels[0], now)
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
			name: "5m hour label appears and now marker at right edge",
			buckets: mkBuckets(
				time.Date(2026, 5, 12, 14, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 5, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 10, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 15, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 20, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 25, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 14, 30, 0, 0, time.UTC),
			),
			chartW:      20,
			zoom:        ZoomLevels[0],
			wantSubstrs: []string{"14:00", "▼ now"},
		},
		{
			name: "1h zoom shows weekday",
			buckets: mkBuckets(
				time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
				time.Date(2026, 5, 12, 1, 0, 0, 0, time.UTC),
			),
			chartW:      30,
			zoom:        ZoomLevels[2],
			wantSubstrs: []string{"Tue", "▼ now"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, starts, _ := projectBuckets(tt.buckets)
			got := renderXLabels(starts, tt.chartW, tt.zoom, now)
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
	tests := []struct {
		name string
		t    time.Time
		zoom ZoomLevel
		want string
	}{
		{"5m on hour", d(13, 0), ZoomLevels[0], "13:00"},
		{"5m mid-hour 15min", d(13, 15), ZoomLevels[0], ""},
		{"5m mid-hour 5min", d(13, 5), ZoomLevels[0], ""},
		{"15m on 3-hour", d(12, 0), ZoomLevels[1], "12:00"},
		{"15m off-3-hour", d(13, 0), ZoomLevels[1], ""},
		{"15m mid-window 09:00", d(9, 0), ZoomLevels[1], "09:00"},
		{"1h today midnight Tue", day(5, 12), ZoomLevels[2], "Tue"},
		{"1h yesterday Mon", day(5, 11), ZoomLevels[2], "Mon"},
		{"1h 6 days ago Wed", day(5, 6), ZoomLevels[2], "Wed"},
		{"1h 7 days ago falls to MM-DD", day(5, 5), ZoomLevels[2], "05-05"},
		{"1h non-midnight returns empty", d(14, 0), ZoomLevels[2], ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := formatXLabel(tt.t, tt.zoom, now)
			if got != tt.want {
				t.Errorf("formatXLabel(%v, %s) = %q, want %q",
					tt.t.Format(time.RFC3339), tt.zoom.Label, got, tt.want)
			}
		})
	}
}

func TestOverlayYLabel_InjectsAtNiceFloorRow(t *testing.T) {
	t.Parallel()
	// peak = 87000 → niceFloorFloat(87000) = 70000 → label "70k".
	// chartH=6 → barsH=5 → row = 5 - round(70000/87000 * 5) = 1.
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE\nFFFFFFFFFF"
	out := overlayYLabel(body, 87_000, chartUnitTokens, 6)
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
		out := overlayYLabel(body, peak, chartUnitTokens, 6)
		if out != body {
			t.Errorf("peak=%v: expected body untouched, got %q", peak, out)
		}
	}
}

func TestOverlayYLabel_HeightTooSmall(t *testing.T) {
	t.Parallel()
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE"
	// chartH < 6 leaves body untouched — same threshold renderXLabels uses.
	if got := overlayYLabel(body, 50_000, chartUnitTokens, 5); got != body {
		t.Errorf("expected body untouched at chartH=5, got %q", got)
	}
}

func TestNiceFloor(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		peak int64
		want int64
	}{
		{"zero returns zero", 0, 0},
		{"negative returns zero", -10, 0},
		{"one", 1, 1},
		{"99 falls to 70", 99, 70},
		{"exactly 100", 100, 100},
		{"exactly 1000", 1000, 1000},
		{"4999 falls to 3k", 4_999, 3_000},
		{"exactly 7k", 7_000, 7_000},
		{"9k falls to 7k", 9_000, 7_000},
		{"23k falls to 20k", 23_000, 20_000},
		{"49k falls to 30k", 49_000, 30_000},
		{"87k falls to 70k", 87_000, 70_000},
		{"exactly 100k", 100_000, 100_000},
		{"123456 falls to 100k", 123_456, 100_000},
		{"999999 falls to 700k", 999_999, 700_000},
		{"exactly 1M", 1_000_000, 1_000_000},
		{"50M stays 50M", 50_000_000, 50_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := niceFloor(tt.peak)
			if got != tt.want {
				t.Errorf("niceFloor(%d) = %d, want %d", tt.peak, got, tt.want)
			}
		})
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
		{"45.7 falls to 30", 45.7, 30.0},
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
