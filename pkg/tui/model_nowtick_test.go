package tui

import (
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// seedModelAt builds a Model backed by a temp-DB cache with nBuckets messages
// spaced 15m apart ending at `now`, with the chart clock pinned to `now`.
// unitIdx selects the chart unit (e.g. int(chartUnitTokens), int(chartUnitRemaining));
// the zoom is fixed at the 15m level (zoomIdx 0).
// Mirrors seedBarModel but injects the #311 clock seam.
func seedModelAt(t *testing.T, unitIdx, nBuckets int, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	if nBuckets > 0 {
		var msgs []parse.Message
		for i := range nBuckets {
			msgs = append(msgs, parse.Message{
				SessionID:   "s",
				ProjectSlug: "p",
				Model:       "claude-opus-4-7",
				Timestamp:   now.Add(-time.Duration(i) * 15 * time.Minute),
				InputTokens: 5000,
			})
		}
		if err := c.InsertMessages(msgs, tab); err != nil {
			t.Fatalf("InsertMessages: %v", err)
		}
	}
	m := New(Deps{Cache: c})
	m.unitIdx = unitIdx
	m.zoomIdx = 0 // 15m
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
	m, c := seedModelAt(t, int(chartUnitTokens), 8, fixed)
	defer c.Close()
	if want := nextBoundary(fixed, ZoomLevels[0]); !m.lastChartTo.Equal(want) {
		t.Errorf("lastChartTo = %v, want %v (driven by injected clock)", m.lastChartTo, want)
	}
}

func TestInit_ArmsNowTick(t *testing.T) {
	t.Parallel()
	m := New(Deps{}) // nil cache is fine — refreshChart no-ops, the tick still arms
	if m.Init() == nil {
		t.Fatal("Init() = nil, want a scheduled now-tick command (#311)")
	}
}

func TestNowTick_StaleGenDropped(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitTokens), 8, base)
	defer c.Close()
	to1 := m.lastChartTo
	updated, cmd := m.Update(nowTickMsg{gen: m.nowGen + 1}) // stale
	m = updated.(Model)
	if cmd != nil {
		t.Error("stale nowTickMsg should not reschedule (got cmd != nil)")
	}
	if !m.lastChartTo.Equal(to1) {
		t.Errorf("stale nowTickMsg advanced the window: %v → %v", to1, m.lastChartTo)
	}
}

func TestNowTick_AdvancesWindowWhenPinned(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	// Few buckets → underfilled → locked flush-right (always pinned, scroll inert).
	m, c := seedModelAt(t, int(chartUnitTokens), 4, base)
	defer c.Close()
	if !m.underfilled {
		t.Fatalf("precondition: expected underfilled (locked-pinned) model")
	}
	to1 := m.lastChartTo
	xoff1 := m.viewportXOffset
	// Cross the next 15m boundary.
	next := nextBoundary(base, ZoomLevels[0]) // 14:15
	m.now = func() time.Time { return next.Add(time.Minute) }
	updated, cmd := m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)
	if cmd == nil {
		t.Error("live nowTickMsg should reschedule (got cmd == nil)")
	}
	if want := nextBoundary(next.Add(time.Minute), ZoomLevels[0]); !m.lastChartTo.Equal(want) {
		t.Errorf("window did not advance: lastChartTo = %v, want %v", m.lastChartTo, want)
	}
	if got := m.lastChartTo.Sub(to1); got != 15*time.Minute {
		t.Errorf("window advanced by %v, want one 15m bucket", got)
	}
	if m.viewportXOffset != xoff1 {
		t.Errorf("pinned offset moved: %d → %d (should stay locked flush-right)", xoff1, m.viewportXOffset)
	}
}

func TestNowTick_FreezesWhenScrolled(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	// Wide dataset → scrollable (not underfilled).
	m, c := seedModelAt(t, int(chartUnitTokens), 300, base)
	defer c.Close()
	if m.underfilled {
		t.Fatalf("precondition: expected a wide (scrollable) model, got underfilled")
	}
	to1 := m.lastChartTo
	// Scroll left, off the right edge.
	for range 5 {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
		m = updated.(Model)
	}
	maxBefore := max(0, len(m.lastStarts)-m.visibleBuckets())
	if m.viewportXOffset >= maxBefore {
		t.Fatalf("precondition: expected scrolled off the right edge, off=%d max=%d", m.viewportXOffset, maxBefore)
	}
	// Cross a boundary.
	next := nextBoundary(base, ZoomLevels[0])
	m.now = func() time.Time { return next.Add(time.Minute) }
	updated, _ := m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)
	if !m.lastChartTo.After(to1) {
		t.Errorf("window did not advance while scrolled: to1=%v to2=%v", to1, m.lastChartTo)
	}
	maxAfter := max(0, len(m.lastStarts)-m.visibleBuckets())
	if m.viewportXOffset >= maxAfter {
		t.Errorf("scrolled user yanked to the right edge: off=%d max=%d (should stay in the past)", m.viewportXOffset, maxAfter)
	}
}

func TestNowTick_FreezesDuringIntro(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	// 8 buckets → underfilled, spring intro active.
	m, c := seedModelAt(t, int(chartUnitTokens), 8, base)
	defer c.Close()
	// Simulate the startup intro animation being in-flight.
	m.springIntro = true
	to1 := m.lastChartTo
	// Cross the next 15m boundary.
	next := nextBoundary(base, ZoomLevels[0])
	m.now = func() time.Time { return next.Add(time.Minute) }
	updated, cmd := m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)
	// refreshChart must be skipped — the window must not advance during intro.
	if !m.lastChartTo.Equal(to1) {
		t.Errorf("intro nowTickMsg advanced the window: %v → %v (should be frozen)", to1, m.lastChartTo)
	}
	// The tick chain must still reschedule so the next boundary fires after intro.
	if cmd == nil {
		t.Error("intro nowTickMsg should still reschedule (got cmd == nil)")
	}
}

func TestNowTick_AdvancesRemainingUnit(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitRemaining), 0 /*no messages*/, base)
	defer c.Close()
	to1 := m.lastChartTo
	next := nextBoundary(base, ZoomLevels[0])
	m.now = func() time.Time { return next.Add(time.Minute) }
	updated, cmd := m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)
	if cmd == nil {
		t.Error("remaining-unit nowTickMsg should reschedule (got cmd == nil)")
	}
	if !m.lastChartTo.After(to1) {
		t.Errorf("remaining window did not advance: to1=%v to2=%v", to1, m.lastChartTo)
	}
}

func TestZoom_RearmsNowTick(t *testing.T) {
	t.Parallel()
	base := time.Date(2026, 5, 23, 14, 7, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitTokens), 8, base)
	defer c.Close()
	gen0 := m.nowGen
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("z")})
	m = updated.(Model)
	if m.nowGen != gen0+1 {
		t.Errorf("zoom did not bump nowGen: %d → %d, want +1", gen0, m.nowGen)
	}
	if cmd == nil {
		t.Error("zoom should re-arm the now-tick (got cmd == nil)")
	}
	// The pre-zoom tick is now stale and must be dropped.
	updated, cmd2 := m.Update(nowTickMsg{gen: gen0})
	m = updated.(Model)
	if cmd2 != nil {
		t.Error("pre-zoom (stale-gen) nowTickMsg should be dropped after re-arm")
	}
}
