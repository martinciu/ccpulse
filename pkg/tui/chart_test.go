package tui

import (
	"runtime"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// sinkString prevents the compiler from eliding the buildChart call
// in BenchmarkBuildChart when its return value is otherwise unused.
var sinkString string

// syntheticBuckets returns n contiguous 5-minute TokenBucket entries
// with deterministic, varied Tokens values so heatColor exercises all
// three colour bands.
func syntheticBuckets(n int) []cache.TokenBucket {
	now := time.Now().UTC()
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
			for b.Loop() {
				sinkString = buildChart(buckets, n, 20)
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
