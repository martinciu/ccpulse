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
	// peak = 87000 → niceFloor(87000) = 75000 → label "75.0k".
	// chartH=6 → barsH=5 → row = 5 - round(75000/87000 * 5) = 1.
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE\nFFFFFFFFFF"
	out := overlayYLabel(body, 87_000, 6)
	rows := strings.Split(out, "\n")
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows, got %d:\n%q", len(rows), out)
	}
	if !strings.Contains(rows[1], "75.0k") {
		t.Errorf("expected '75.0k' on row 1, got %q", rows[1])
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
	for _, peak := range []int64{0, -5} {
		out := overlayYLabel(body, peak, 6)
		if out != body {
			t.Errorf("peak=%d: expected body untouched, got %q", peak, out)
		}
	}
}

func TestOverlayYLabel_HeightTooSmall(t *testing.T) {
	t.Parallel()
	body := "AAAAAAAAAA\nBBBBBBBBBB\nCCCCCCCCCC\nDDDDDDDDDD\nEEEEEEEEEE"
	// chartH < 6 leaves body untouched — same threshold renderXLabels uses.
	if got := overlayYLabel(body, 50_000, 5); got != body {
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
