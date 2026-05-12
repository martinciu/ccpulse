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

// syntheticBuckets returns n contiguous 5-minute TokenBucket entries
// with deterministic, varied Tokens values so heatColor exercises all
// three colour bands. Anchored to a 3-hour clock boundary so the 15m
// zoom (used by BenchmarkBuildChart and BenchmarkRenderXLabels) hits a
// label tick every 36th bucket — exercising renderXLabels' label-write
// loop. Without this anchor, formatXLabel would return "" for nearly
// every bucket and the label-write path would be under-measured.
func syntheticBuckets(n int) []cache.TokenBucket {
	now := time.Now().UTC().Truncate(3 * time.Hour)
	out := make([]cache.TokenBucket, n)
	for i := range out {
		out[i] = cache.TokenBucket{
			BucketStart: now.Add(time.Duration(i) * 5 * time.Minute),
			// Sweep across the heat range; a few zero buckets for gaps.
			Tokens: int64((i * 137) % 1000),
		}
	}
	return out
}

func BenchmarkBuildChart(b *testing.B) {
	for _, n := range []int{10_000, 25_000, 50_000} {
		buckets := syntheticBuckets(n)
		b.Run(formatN(n), func(b *testing.B) {
			b.ReportAllocs()
			// Drain GC pressure from the syntheticBuckets allocation so it
			// doesn't bleed into the first iteration's measurement.
			runtime.GC()
			b.ResetTimer()
			now := time.Now().UTC()
			for b.Loop() {
				sinkString = buildChart(buckets, n, 20, now, ZoomLevels[1])
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

func BenchmarkNiceCeiling(b *testing.B) {
	cases := []struct {
		name string
		v    int64
	}{
		{"zero", 0}, // exercises the early-return branch
		{"small", 12},
		{"k", 45_300},
		{"M_low", 1_200_000},
		{"M_high", 999_999},
	}
	for _, tt := range cases {
		b.Run(tt.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = niceCeiling(tt.v)
			}
		})
	}
}

func BenchmarkRenderYAxis(b *testing.B) {
	for _, h := range []int{10, 50, 100} {
		b.Run(formatN(h), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = renderYAxis(50_000, h)
			}
		})
	}
}

func BenchmarkRenderXLabels(b *testing.B) {
	now := time.Now().UTC()
	for _, n := range []int{100, 1000, 5000} {
		buckets := syntheticBuckets(n)
		b.Run(formatN(n), func(b *testing.B) {
			b.ReportAllocs()
			runtime.GC()
			b.ResetTimer()
			for b.Loop() {
				sinkString = renderXLabels(buckets, n, ZoomLevels[1], now)
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
	out := buildChart(bs, len(bs), 10, now, ZoomLevels[0])
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
	out := buildChart(bs, len(bs), 5, now, ZoomLevels[0])
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
	got := renderXLabels(buckets, 1, ZoomLevels[0], now)
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
	got := renderXLabels(buckets, 3, ZoomLevels[0], now)
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
			got := renderXLabels(tt.buckets, tt.chartW, tt.zoom, now)
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

func TestRenderYAxis(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		ceiling  int64
		height   int
		wantRows []string // nil means expect empty output
	}{
		{
			name:    "ceiling 50k height 6",
			ceiling: 50_000,
			height:  6,
			wantRows: []string{
				" 50.0k",
				"      ",
				"      ",
				"      ",
				"     0",
				"      ",
			},
		},
		{
			name:    "ceiling 1.5M height 8",
			ceiling: 1_500_000,
			height:  8,
			wantRows: []string{
				"  1.5M",
				"      ",
				"      ",
				"      ",
				"      ",
				"      ",
				"     0",
				"      ",
			},
		},
		{
			name:     "ceiling 0 returns blank column",
			ceiling:  0,
			height:   6,
			wantRows: []string{"      ", "      ", "      ", "      ", "      ", "      "},
		},
		{
			name:     "height too small returns empty",
			ceiling:  50_000,
			height:   5,
			wantRows: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := renderYAxis(tt.ceiling, tt.height)
			if tt.wantRows == nil {
				if got != "" {
					t.Errorf("expected empty, got %q", got)
				}
				return
			}
			rows := strings.Split(got, "\n")
			if len(rows) != len(tt.wantRows) {
				t.Fatalf("got %d rows, want %d:\n%q", len(rows), len(tt.wantRows), got)
			}
			for i, want := range tt.wantRows {
				if rows[i] != want {
					t.Errorf("row %d: got %q, want %q", i, rows[i], want)
				}
			}
			if w := lipgloss.Width(rows[0]); w != yAxisWidth {
				t.Errorf("row 0 width = %d, want %d", w, yAxisWidth)
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

func TestNiceCeiling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		peak int64
		want int64
	}{
		{"zero returns 1", 0, 1},
		{"negative returns 1", -10, 1},
		{"one", 1, 1},
		{"two", 2, 2},
		{"three rounds to 5", 3, 5},
		{"five", 5, 5},
		{"six rounds to 10", 6, 10},
		{"twelve rounds to 15", 12, 15},
		{"1200 rounds to 1500", 1200, 1500},
		{"12000 rounds to 15000", 12_000, 15_000},
		{"45300 rounds to 50000", 45_300, 50_000},
		{"1.2M rounds to 1.5M", 1_200_000, 1_500_000},
		{"1.6M rounds to 2M", 1_600_000, 2_000_000},
		{"2.3M rounds to 2.5M", 2_300_000, 2_500_000},
		{"999999 rounds to 1M", 999_999, 1_000_000},
		{"exactly 1M", 1_000_000, 1_000_000},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := niceCeiling(tt.peak)
			if got != tt.want {
				t.Errorf("niceCeiling(%d) = %d, want %d", tt.peak, got, tt.want)
			}
		})
	}
}

func TestBuildChart_TickLabelPosition(t *testing.T) {
	// peak = 87000 → niceFloor(87000) = 75000 → label "75.0k".
	// barsH = chartH - 1 = 5 (chartH=6, X labels show).
	// row = 5 - round(75000/87000 * 5) = 5 - 4 = 1.
	now := time.Now().UTC().Truncate(time.Hour)
	buckets := []cache.TokenBucket{
		{BucketStart: now, Tokens: 87_000},
	}
	out := buildChart(buckets, 10, 6, now, ZoomLevels[1])
	rows := strings.Split(out, "\n")
	if len(rows) < 6 {
		t.Fatalf("expected at least 6 rows (5 canvas + 1 X label), got %d:\n%s", len(rows), out)
	}
	if !strings.Contains(rows[1], "75.0k") {
		t.Errorf("expected '75.0k' overlay at canvas row 1, got row 1 = %q\nfull:\n%s", rows[1], out)
	}
	// Negative: no overlay on other canvas rows.
	for i, r := range rows[:5] {
		if i == 1 {
			continue
		}
		if strings.Contains(r, "75.0k") {
			t.Errorf("'75.0k' overlay leaked onto canvas row %d: %q", i, r)
		}
	}
}

func TestBuildChart_NoTickWhenEmpty(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Hour)
	// All-zero buckets — peak == 0 → niceFloor returns 0 → overlay skipped.
	buckets := make([]cache.TokenBucket, 5)
	for i := range buckets {
		buckets[i] = cache.TokenBucket{BucketStart: now.Add(time.Duration(i*5) * time.Minute)}
	}
	out := buildChart(buckets, 10, 6, now, ZoomLevels[0])
	// formatTokenCount for any non-zero value ends in "k" or "M" (for n >= 1000)
	// or is a bare integer < 1000. With peak=0 the overlay is skipped entirely,
	// so the canvas portion (first 5 rows) must contain no "k" or "M" — neither
	// ntcharts bar cells nor empty cells emit those characters. Limiting the
	// scope to canvas rows avoids false-positives from X labels ("Mon" → "M").
	canvasOnly := strings.Join(strings.Split(out, "\n")[:5], "\n")
	if strings.ContainsAny(canvasOnly, "kM") {
		t.Errorf("canvas contains 'k' or 'M' suggesting a token label leaked for empty data:\n%s", canvasOnly)
	}
}

func TestBuildChart_NoYAxisGutter(t *testing.T) {
	// Regression for issue #132: the 6-col Y axis gutter is gone. The bar
	// at column 0 reaches the bottom canvas row, so that row must NOT start
	// with whitespace where the old yAxisWidth=6 gutter used to live.
	now := time.Now().UTC().Truncate(time.Hour)
	buckets := []cache.TokenBucket{
		{BucketStart: now, Tokens: 10_000},
	}
	out := buildChart(buckets, 10, 6, now, ZoomLevels[1])
	rows := strings.Split(out, "\n")
	// Bottom canvas row is rows[4] (canvas occupies 0..4 at chartH=6).
	canvasBottom := rows[4]
	if strings.HasPrefix(canvasBottom, "      ") {
		t.Errorf("canvas bottom row starts with 6-col whitespace gutter; Y axis column should be gone:\n%q", canvasBottom)
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
		{"99 falls to 75", 99, 75},
		{"exactly 100", 100, 100},
		{"exactly 1000", 1000, 1000},
		{"7499 falls to 5k", 7_499, 5_000},
		{"exactly 7.5k", 7_500, 7_500},
		{"9k falls to 7.5k", 9_000, 7_500},
		{"23k falls to 20k", 23_000, 20_000},
		{"87k falls to 75k", 87_000, 75_000},
		{"exactly 100k", 100_000, 100_000},
		{"123456 falls to 100k", 123_456, 100_000},
		{"999999 falls to 750k", 999_999, 750_000},
		{"exactly 1M", 1_000_000, 1_000_000},
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
		{"exactly k", 1000, "1.0k"},
		{"k small frac", 1234, "1.2k"},
		{"k mid", 45300, "45.3k"},
		{"k rounds half-up", 99499, "99.5k"},
		{"k drop frac at 100", 100000, "100k"},
		{"k high", 999000, "999k"},
		{"k just below M", 999999, "1000k"},
		{"exactly M", 1_000_000, "1.0M"},
		{"M small frac", 1_200_000, "1.2M"},
		{"M mid", 45_300_000, "45.3M"},
		{"M drop frac at 100", 100_000_000, "100M"},
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
