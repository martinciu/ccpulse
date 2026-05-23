package tui

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
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

// seedTokenModelAt builds a tokens-unit Model backed by a temp-DB cache with
// nBuckets messages spaced `spacing` apart ending at `now`, with the chart
// clock pinned to `now`. Mirrors seedBarModel but injects the #311 clock seam.
func seedTokenModelAt(t *testing.T, zoomIdx, nBuckets int, spacing time.Duration, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	var msgs []parse.Message
	for i := range nBuckets {
		msgs = append(msgs, parse.Message{
			SessionID:   "s",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(-time.Duration(i) * spacing),
			InputTokens: 5000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.zoomIdx = zoomIdx
	m.w, m.h = 122, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.now = func() time.Time { return now }
	m.refreshChart()
	return m, c
}

func TestRefreshChart_UsesInjectedClock(t *testing.T) {
	t.Parallel()
	// A clock far from real wall-time: if the seam is unwired, refreshChart
	// uses time.Now() and lastChartTo lands ~2026, failing this assertion.
	fixed := time.Date(2020, 1, 15, 9, 23, 0, 0, time.UTC)
	m, c := seedTokenModelAt(t, 0 /* 15m */, 8, 15*time.Minute, fixed)
	defer c.Close()
	if want := nextBoundary(fixed, ZoomLevels[0]); !m.lastChartTo.Equal(want) {
		t.Errorf("lastChartTo = %v, want %v (driven by injected clock)", m.lastChartTo, want)
	}
}
