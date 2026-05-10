package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

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
			for b.Loop() {
				_ = buildChart(buckets, n, 20)
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
