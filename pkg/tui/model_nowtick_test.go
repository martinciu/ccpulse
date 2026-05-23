package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestNextBoundary_SubDay(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 30, 0, time.UTC) // 14:07:30
	if got, want := nextBoundary(base, ZoomLevels[0]), time.Date(2026, 5, 23, 14, 15, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("15m nextBoundary(%v) = %v, want %v", base, got, want)
	}
	if got, want := nextBoundary(base, ZoomLevels[1]), time.Date(2026, 5, 23, 15, 0, 0, 0, time.UTC); !got.Equal(want) {
		t.Errorf("1h nextBoundary(%v) = %v, want %v", base, got, want)
	}
}

func TestNextBoundary_ExactlyOnBoundary(t *testing.T) {
	t.Parallel()
	// On a 15m boundary, the END of the current bucket is one full interval
	// later — never `now` itself (so a tick scheduled here never has a 0/neg duration).
	base := time.Date(2026, 5, 23, 14, 15, 0, 0, time.UTC)
	if got, want := nextBoundary(base, ZoomLevels[0]), base.Add(15*time.Minute); !got.Equal(want) {
		t.Errorf("on-boundary nextBoundary(%v) = %v, want %v", base, got, want)
	}
}

func TestNextBoundary_24hLocalMidnight(t *testing.T) {
	t.Parallel()
	// 24h returns the NEXT local midnight, strictly after now, zero wall time.
	// DST calendar-correctness is inherited from cache.DayStartLocal + stdlib
	// AddDate (covered by cache tests); we avoid mutating the global time.Local
	// here so the test stays parallel-safe.
	base := time.Now()
	got := nextBoundary(base, ZoomLevels[2]) // 24h
	if !got.After(base) {
		t.Errorf("24h nextBoundary(%v) = %v, want strictly after now", base, got)
	}
	if local := got.In(time.Local); local.Hour() != 0 || local.Minute() != 0 || local.Second() != 0 {
		t.Errorf("24h nextBoundary = %v, want local midnight (00:00:00)", local)
	}
	if !got.After(cache.DayStartLocal(base)) {
		t.Errorf("24h nextBoundary should be after today's local midnight")
	}
}
