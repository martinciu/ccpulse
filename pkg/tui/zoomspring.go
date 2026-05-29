// Package tui — zoom-squeeze animation (issue #373).
//
// This file holds the single-phase horizontal-squeeze animation for the 'z'
// zoom key in remaining (line) mode. It is a sibling of the two-phase
// unit-toggle machine in springs.go: it reuses the master springActive flag
// and the shared springGen counter (the two animations are mutually exclusive
// — refreshChart aborts any in-flight animation), and is disambiguated by
// Model.springKind.
//
// Bar-mode zoom (cost/tokens) is out of scope and keeps its hard-cut; see the
// spec's deferred-bar follow-up.
package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// zoomAnimSnapshot captures, once at arm time, everything the per-frame tick
// needs so nothing it reads can shift mid-animation: the OLD on-screen visible
// window (oFrom/oTo), the NEW window (nFrom/nTo), the utilization points, and a
// frozen `now` that keeps the right edge stable across frames.
type zoomAnimSnapshot struct {
	oFrom, oTo time.Time
	nFrom, nTo time.Time
	pts5h      []cache.UtilizationPoint
	pts7d      []cache.UtilizationPoint
	now        time.Time
}

// lerpTime returns the linear interpolation between a and b at parameter r,
// clamped to [a, b] for r outside [0, 1]. Used to ease the visible time window
// from the old zoom's window to the new zoom's across the squeeze.
func lerpTime(a, b time.Time, r float64) time.Time {
	if r <= 0 {
		return a
	}
	if r >= 1 {
		return b
	}
	span := b.Sub(a)
	return a.Add(time.Duration(float64(span) * r))
}

// handleZoomKey advances the zoom level and, in remaining (line) mode with
// motion enabled and data present, arms the horizontal squeeze. All other
// cases snap exactly as the pre-#373 handler did: advance zoomIdx, refreshChart,
// bump nowGen, reschedule the live-advance tick.
//
// Snap gates (mirror handleUnitKey / beginUnitAnimation):
//   - bar mode (cost/tokens) — deferred scope, keep the hard-cut
//   - reduce_motion — the single animation opt-out (#181)
//   - no data (warming up / empty usage_samples) — nothing to squeeze
//   - m.w == 0 — pre-init, no viewport geometry yet
func (m *Model) handleZoomKey() tea.Cmd {
	animate := chartUnit(m.unitIdx) == chartUnitRemaining &&
		!m.deps.ReduceMotion &&
		m.hasData &&
		m.w != 0

	if !animate {
		return m.snapZoom()
	}

	// Snapshot the OLD on-screen window BEFORE advancing the zoom — reads the
	// pre-refresh lastChart*/offset geometry at the OLD zoom.
	oFrom, oTo := m.visibleWindow()

	m.zoomIdx = (m.zoomIdx + 1) % len(ZoomLevels)
	m.refreshChart() // rebuild at NEW zoom; re-pins the right edge. On a first
	// 'z' the abort block is a no-op (no spring in flight); a second 'z'
	// mid-squeeze hits it, which correctly tears down the prior squeeze.

	// If the refresh left us without data (defensive — hasData was true at the
	// gate), fall back to the snap's now-tick re-arm.
	if !m.hasData {
		m.nowGen++
		return m.scheduleNowTick()
	}

	nFrom, nTo := m.visibleWindow()
	// The squeeze slices the NEW (post-refresh) points across the lerped
	// window; remaining-mode points are raw samples, so they're
	// zoom-independent and the OLD points are never needed. The snapshot
	// carries the post-refresh pts so a future zoom-dependent point set would
	// still read the correct source.
	m.zoomSnap = zoomAnimSnapshot{
		oFrom: oFrom, oTo: oTo,
		nFrom: nFrom, nTo: nTo,
		pts5h: m.lastPts5h, pts7d: m.lastPts7d,
		now: m.now(),
	}

	m.zoomSpring = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	m.zoomSpringR = 0
	m.zoomSpringVel = 0
	m.springActive = true
	m.springKind = springKindZoom
	m.springGen++

	// The live-advance now-tick chain is left intact on purpose — no nowGen
	// bump. It stays alive throughout the squeeze and is advance-suppressed by
	// handleNowTick's springKindZoom guard, so the frozen `now` still keeps the
	// right edge stable. Never breaking the chain means an abort (RefreshMsg /
	// WindowSizeMsg / 'u') can't orphan the live edge — it resumes the instant
	// the squeeze settles, mirroring how the startup intro coexists with the
	// now-tick. A gen bump here would have to be paired with a re-arm on EVERY
	// teardown path; deferring the re-arm to settle alone left aborts orphaned
	// (#373, #311).

	// Render frame 0 (= old window) synchronously so the next View() doesn't
	// flash the refreshed full-canvas content before the first tick paints.
	m.renderZoomFrame(oFrom, oTo)

	gen := m.springGen
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// snapZoom is the non-animated zoom path: advance the level, refresh, and
// re-arm the live-advance tick on the new cadence. Identical to the pre-#373
// Zoom handler.
func (m *Model) snapZoom() tea.Cmd {
	m.zoomIdx = (m.zoomIdx + 1) % len(ZoomLevels)
	m.refreshChart()
	m.nowGen++
	return m.scheduleNowTick()
}

// renderZoomFrame paints the remaining-mode line for the [viewFrom, viewTo]
// window at viewport width. The raw utilization points are sliced to the
// window (no value interpolation — only the window boundaries move across the
// squeeze) and drawn at the NEW zoom's label cadence. Mirrors
// renderSpringLineFrame minus the Pct envelope. (#373)
func (m *Model) renderZoomFrame(viewFrom, viewTo time.Time) {
	zoom := ZoomLevels[m.zoomIdx]
	chartH := m.chartHeight()
	vpW := m.viewport.Width
	sliced5h := slicePointsInRange(m.zoomSnap.pts5h, viewFrom, viewTo)
	sliced7d := slicePointsInRange(m.zoomSnap.pts7d, viewFrom, viewTo)
	m.viewport.SetContent(buildLineChart(sliced5h, sliced7d, viewFrom, viewTo, vpW, chartH, m.zoomSnap.now, zoom, m.dateOrder, "zoom"))
	m.viewport.SetXOffset(0)
}

// handleZoomSpringTick advances one frame of the zoom squeeze: step the spring
// toward r=1, lerp the visible window oWin→nWin, render windowed, and settle
// when within phaseTransitionThreshold of the target. On settle it restores the
// steady-state full canvas via refreshChart and stops the spring loop; the
// live-advance now-tick chain (never gen-bumped by the squeeze) resumes on its
// own (#311 coexistence). gen is the captured generation; the next tick carries
// it so a superseding 'z'/'u' drops it.
func (m *Model) handleZoomSpringTick(gen int) tea.Cmd {
	r, vel := m.zoomSpring.Update(m.zoomSpringR, m.zoomSpringVel, 1.0)
	m.zoomSpringR = r
	m.zoomSpringVel = vel

	if math.Abs(1.0-r) < phaseTransitionThreshold {
		m.zoomSpringR = 1.0
		m.springActive = false
		m.springKind = springKindNone
		m.refreshChart() // restore the full scrollable canvas at the new zoom.
		// The now-tick chain was never suppressed via a gen bump (see
		// handleZoomKey), so it is still live and resumes advancing on its next
		// fire — no re-arm here. Returning nil stops the spring loop so the idle
		// TUI stays zero-animation-cost (#373).
		return nil
	}

	viewFrom := lerpTime(m.zoomSnap.oFrom, m.zoomSnap.nFrom, r)
	viewTo := lerpTime(m.zoomSnap.oTo, m.zoomSnap.nTo, r)
	m.renderZoomFrame(viewFrom, viewTo)

	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}
