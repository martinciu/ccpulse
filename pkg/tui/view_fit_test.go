package tui

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/status"
)

// fitNow is a fixed clock for every matrix cell, so reset timers, chart
// windows, and seeded-cache timestamps line up deterministically.
var fitNow = time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)

// fitWidths spans the narrow→wide regime called out in issue #321. Heights
// stay ≥ 20: below ~12 the chartHeight() floor of 5 makes the >=12-line
// composition unable to fit, which is outside the app's supported range.
var fitWidths = []int{60, 80, 100, 120, 160, 200}

// projFor returns a *status.Projection that lands deterministically on the
// target severityFor branch for the given bucket window. "none" is handled
// by the caller (nil Projections), so it is not produced here.
func projFor(sev string, window time.Duration) *status.Projection {
	switch sev {
	case "warmingUp":
		return &status.Projection{
			ElapsedMinutes: 5, SlopePctPerHour: 10,
			ProjectedPctAtReset: 50, Confidence: "low",
		}
	case "safe":
		return &status.Projection{
			ElapsedMinutes: 90, SlopePctPerHour: 4,
			ProjectedPctAtReset: 60, WillOverreach: false, Confidence: "ok",
		}
	case "watch":
		eta := int(window.Minutes() * 0.5) // > 10% of window → watch
		return &status.Projection{
			ElapsedMinutes: 90, SlopePctPerHour: 12,
			ProjectedPctAtReset: 130, WillOverreach: true,
			MinutesTo100Pct: &eta, Confidence: "ok",
		}
	case "danger":
		eta := int(window.Minutes() * 0.02) // <= 10% of window → danger
		if eta < 1 {
			eta = 1
		}
		return &status.Projection{
			ElapsedMinutes: 90, SlopePctPerHour: 40,
			ProjectedPctAtReset: 220, WillOverreach: true,
			MinutesTo100Pct: &eta, Confidence: "ok",
		}
	default:
		return nil
	}
}

// windowFor builds a status.Window whose burn-rate sides land on the named
// severity. sev=="none" leaves Projection nil so both sides render the
// no-data branch. has7d toggles the 7-day side between real values and the
// "(no data)" placeholder.
func windowFor(sev string, has7d bool) status.Window {
	w := status.Window{
		Percent:        43,
		MinutesToReset: intPtr(137),
		CeilingLabel:   "max_20x",
	}
	if has7d {
		w.Has7d = true
		w.Percent7d = 17
		w.MinutesToReset7d = intPtr(7200)
	}
	if sev != "none" {
		w.Projection = &status.Projections{FiveHour: projFor(sev, 5*time.Hour)}
		if has7d {
			w.Projection.SevenDay = projFor(sev, 7*24*time.Hour)
		}
	}
	return w
}

// buildFitModel constructs a deterministic, non-animating Model at the given
// size and state, driving a WindowSizeMsg through Update so the viewport gets
// real production geometry (not synthetic content). ReduceMotion + no ticks
// keep springActive false, so View() renders a steady frame.
func buildFitModel(c *cache.Cache, w, h int, win status.Window, unitIdx int, showHelp bool) Model {
	m := New(Deps{Cache: c, ReduceMotion: true})
	m.now = func() time.Time { return fitNow }
	m.window = win
	m.unitIdx = unitIdx
	m.showHelp = showHelp
	nm, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return nm.(Model)
}

// emptyFitCache opens a fresh, message-free cache so refreshChart is safe to
// call (avoids any nil-cache path) and the chart body renders the empty-state
// placeholder.
func emptyFitCache(t *testing.T) *cache.Cache {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "empty.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// seedFitCache opens a cache populated with ~200 assistant turns spanning the
// hours before fitNow, so refreshChart produces real bar/line content.
func seedFitCache(t *testing.T, now time.Time) *cache.Cache {
	t.Helper()
	c, err := cache.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { c.Close() })
	hist, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	msgs := make([]parse.Message, 200)
	for i := range msgs {
		msgs[i] = parse.Message{
			SessionID:    "s1",
			ProjectSlug:  "slug-a",
			Model:        "claude-opus-4-7",
			Timestamp:    now.Add(time.Duration(-i*15) * time.Minute),
			InputTokens:  int64(1000 + i*100),
			OutputTokens: int64(500 + i*50),
		}
	}
	if err := c.InsertMessages(msgs, hist); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	return c
}

// TestViewFitsTerminal_HeaderStates stresses the fixed-geometry header box
// (where #318/#320 broke) across width × severity × 7d × showHelp. The header
// is rendered in both help and non-help modes, so showHelp is on this axis.
func TestViewFitsTerminal_HeaderStates(t *testing.T) {
	t.Parallel()
	// Subtests run serially (no t.Parallel inside t.Run): they share a
	// read-only cache, so they'd be safe to parallelize, but serial
	// execution keeps the matrix's failure output ordered and avoids
	// interleaved View() dumps. Same applies to _ChartBody/_HelpOverlay.
	cEmpty := emptyFitCache(t)
	severities := []string{"none", "warmingUp", "safe", "watch", "danger"}
	for _, w := range fitWidths {
		for _, h := range []int{20, 40} {
			for _, sev := range severities {
				for _, has7d := range []bool{true, false} {
					for _, showHelp := range []bool{false, true} {
						name := fitName(w, h, sev, has7d, showHelp)
						t.Run(name, func(t *testing.T) {
							m := buildFitModel(cEmpty, w, h, windowFor(sev, has7d), int(chartUnitCost), showHelp)
							assertFits(t, m)
						})
					}
				}
			}
		}
	}
}

// fitName builds a stable, greppable subtest name.
func fitName(w, h int, sev string, has7d, showHelp bool) string {
	d7 := "no7d"
	if has7d {
		d7 = "7d"
	}
	n := fmt.Sprintf("w%d_h%d_%s_%s", w, h, sev, d7)
	if showHelp {
		n += "_help"
	}
	return n
}

// assertFits checks the coarse whole-View height invariant and dumps the
// rendered view on failure so the over-growing region is eyeballable.
func assertFits(t *testing.T, m Model) {
	t.Helper()
	v := m.View()
	if got := lipgloss.Height(v); got > m.h {
		t.Errorf("View() rendered %d lines, exceeds terminal height %d (w=%d)\n%s", got, m.h, m.w, v)
	}
}

// TestViewFitsTerminal_ChartBody exercises the viewport-clipped chart body
// across width × height × cache × unit. The body is clipped to chartHeight()
// by construction, so this is a sanity backstop confirming no render path
// (bar for cost/tokens, line for remaining; empty vs populated) adds height.
func TestViewFitsTerminal_ChartBody(t *testing.T) {
	t.Parallel()
	cEmpty := emptyFitCache(t)
	cFull := seedFitCache(t, fitNow)
	win := windowFor("safe", true)
	units := []struct {
		name string
		idx  int
	}{
		{"cost", int(chartUnitCost)},
		{"tokens", int(chartUnitTokens)},
		{"remaining", int(chartUnitRemaining)},
	}
	for _, w := range fitWidths {
		for _, h := range []int{20, 40, 60} {
			for _, cacheState := range []struct {
				name string
				c    *cache.Cache
			}{{"empty", cEmpty}, {"populated", cFull}} {
				for _, u := range units {
					name := fmt.Sprintf("w%d_h%d_%s_%s", w, h, cacheState.name, u.name)
					t.Run(name, func(t *testing.T) {
						m := buildFitModel(cacheState.c, w, h, win, u.idx, false)
						assertFits(t, m)
					})
				}
			}
		}
	}
}

// TestViewFitsTerminal_HelpOverlay confirms the full-help overlay body (which
// is NOT viewport-clipped) fits the terminal across width × height at the
// smallest supported heights.
func TestViewFitsTerminal_HelpOverlay(t *testing.T) {
	t.Parallel()
	cEmpty := emptyFitCache(t)
	win := windowFor("safe", true)
	for _, w := range fitWidths {
		for _, h := range []int{20, 40, 60} {
			name := fmt.Sprintf("w%d_h%d", w, h)
			t.Run(name, func(t *testing.T) {
				m := buildFitModel(cEmpty, w, h, win, int(chartUnitCost), true)
				assertFits(t, m)
			})
		}
	}
}
