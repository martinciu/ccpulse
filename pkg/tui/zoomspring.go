// Package tui — zoom transition animation (issues #373, #393).
//
// This file holds the single-phase 'z' zoom transition for BOTH chart families,
// disambiguated only at render time by the snapshot's unit:
//   - remaining (line) mode: the horizontal squeeze, lerping the visible time
//     window oWin→nWin and re-slicing the raw utilization points (#373).
//   - cost/tokens (bar) mode: the per-column skyline morph, lerping two
//     snapshotted per-column height arrays oldSky→newSky and drawing them
//     directly with block runes — no ntcharts per frame (#393).
//
// It is a sibling of the two-phase unit-toggle machine in springs.go: it reuses
// the master springActive flag and the shared springGen counter (the two
// animations are mutually exclusive — refreshChart aborts any in-flight
// animation), and is disambiguated by Model.springKind.
package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// zoomAnimSnapshot captures, once at arm time, everything the per-frame tick
// needs so nothing it reads can shift mid-animation: the OLD on-screen visible
// window (oFrom/oTo), the NEW window (nFrom/nTo), the utilization points, and a
// frozen `now` that keeps the right edge stable across frames.
type zoomAnimSnapshot struct {
	oFrom, oTo time.Time
	nFrom, nTo time.Time
	oZoom      ZoomLevel // OLD zoom, captured before zoomIdx advances — the outgoing cadence for the cross-fade.
	pts5h      []cache.UtilizationPoint
	pts7d      []cache.UtilizationPoint
	now        time.Time

	// Bar-mode morph (#393). Line mode leaves these zero; bar mode leaves
	// pts5h/7d nil. oldSky/newSky are per-column float heights captured at arm;
	// oPeak/nPeak feed the y-label cross-fade; unit drives bar color + y-label
	// format (constant across a zoom).
	oldSky, newSky []float64
	oPeak, nPeak   float64
	unit           chartUnit
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

// handleZoomKey advances the zoom level and, with motion enabled and data
// present, arms the zoom transition in BOTH chart families: the horizontal
// squeeze in remaining (line) mode and the per-column skyline morph in
// cost/tokens (bar) mode (#393). All other cases snap exactly as the pre-#373
// handler did: advance zoomIdx, refreshChart, bump nowGen, reschedule the
// live-advance tick.
//
// The animation machinery (m.zoomSpring, springKindZoom, the springGen abort,
// the nowGen re-arm, the synchronous frame-0 paint) is shared between the two
// families; only the snapshot fields captured and the per-frame renderer differ
// by unit. isLine selects the branch.
//
// Snap gates (mirror handleUnitKey / beginUnitAnimation):
//   - reduce_motion — the single animation opt-out (#181)
//   - no data (warming up / empty usage_samples) — nothing to animate
//   - m.w == 0 — pre-init, no viewport geometry yet
func (m *Model) handleZoomKey() tea.Cmd {
	animate := !m.deps.ReduceMotion && m.hasData && m.w != 0
	if !animate {
		return m.snapZoom()
	}

	isLine := chartUnit(m.unitIdx) == chartUnitRemaining

	// Snapshot the OLD on-screen window BEFORE advancing the zoom. On a fresh
	// arm that's the steady-state visible window. But on a rapid second 'z'
	// while a transition is still in flight, visibleWindow() would read the FIRST
	// press's settled target geometry — refreshChart re-pinned lastChart*/offset
	// at the first arm, and the in-flight lerp only repaints viewport content, it
	// never updates those shadows — snapping the new transition's frame 0 to the
	// first target. Start the new transition from where the eye actually is (the
	// live lerp position) so the hand-off is continuous (#373). Read it now,
	// before refreshChart's abort block runs: that block resets springActive/
	// springKind but deliberately leaves zoomSnap/zoomSpringR intact.
	var oFrom, oTo time.Time
	if m.springActive && m.springKind == springKindZoom {
		oFrom = lerpTime(m.zoomSnap.oFrom, m.zoomSnap.nFrom, m.zoomSpringR)
		oTo = lerpTime(m.zoomSnap.oTo, m.zoomSnap.nTo, m.zoomSpringR)
	} else {
		oFrom, oTo = m.visibleWindow()
	}

	// Capture the OLD skyline for bar mode BEFORE refreshChart overwrites
	// lastValues/peak. On a live second 'z', the OLD skyline is the current
	// lerped skyline (mirrors the line oFrom/oTo live read).
	var oldSky []float64
	oPeak := m.peak
	chartH := m.chartHeight()
	barsH := chartH
	if chartH >= 6 {
		barsH = chartH - 1
	}
	if !isLine {
		oldSky = m.captureOldSkyline(barsH)
	}

	oZoom := ZoomLevels[m.zoomIdx] // outgoing cadence — capture before zoomIdx advances.

	m.zoomIdx = (m.zoomIdx + 1) % len(ZoomLevels)
	m.refreshChart() // rebuild at NEW zoom; re-pins the right edge. On a first
	// 'z' the abort block is a no-op (no spring in flight); a second 'z'
	// mid-transition hits it, which correctly tears down the prior transition.

	// If the refresh left us without data (defensive — hasData was true at the
	// gate), fall back to the snap's now-tick re-arm.
	if !m.hasData {
		m.nowGen++
		return m.scheduleNowTick()
	}

	nFrom, nTo := m.visibleWindow()
	// Line mode: the squeeze slices the NEW (post-refresh) points across the
	// lerped window; remaining-mode points are raw samples, so they're
	// zoom-independent. Bar mode: the morph lerps oldSky→newSky column-by-column
	// (pts5h/7d stay nil). The snapshot carries whichever the active family reads.
	m.zoomSnap = zoomAnimSnapshot{
		oFrom: oFrom, oTo: oTo,
		nFrom: nFrom, nTo: nTo,
		oZoom: oZoom,
		pts5h: m.lastPts5h, pts7d: m.lastPts7d,
		now:  m.now(),
		unit: chartUnit(m.unitIdx),
	}
	if !isLine {
		m.zoomSnap.oldSky = oldSky
		m.zoomSnap.oPeak = oPeak
		m.zoomSnap.nPeak = m.peak
		m.zoomSnap.newSky = rasterizeSkyline(m.lastValues, m.lastStarts, m.peak, m.viewport.Width, barsH, ZoomLevels[m.zoomIdx])
	}

	m.zoomSpring = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	m.zoomSpringR = 0
	m.zoomSpringVel = 0
	m.springActive = true
	m.springKind = springKindZoom
	m.springGen++

	// Re-arm the live-advance now-tick on the NEW zoom's cadence and bump nowGen
	// so the old cadence's in-flight tick is dropped. Re-arming HERE (at the arm,
	// not deferred to settle) keeps the chain both correctly-paced and
	// un-orphanable:
	//   - Cadence: scheduleNowTick reads ZoomLevels[m.zoomIdx] (already the new
	//     zoom), so the chain follows the new cadence. Without it a zoom-in (e.g.
	//     the 24h→15m cycle wrap) would leave the chain on the old, coarser
	//     boundary and freeze the live edge on an idle TUI until that boundary
	//     fires — up to ~24h, reintroducing the #311 stall (#373).
	//   - No orphan: handleNowTick's springKindZoom guard advance-suppresses this
	//     re-armed tick if it lands mid-transition (it reschedules without
	//     refreshChart, so the frozen `now` keeps the right edge stable). Because
	//     the chain is re-armed now rather than at settle, every teardown path
	//     (settle OR abort) inherits a live, correctly-scheduled tick. The
	//     earlier deferred-to-settle design is what left aborts orphaned (#311).
	m.nowGen++

	// Render frame 0 (= old window / old skyline) synchronously so the next
	// View() doesn't flash the refreshed full-canvas content before the first
	// tick paints.
	if isLine {
		m.renderZoomFrame(oFrom, oTo)
	} else {
		m.renderZoomBarFrame(0)
	}

	gen := m.springGen
	return tea.Batch(
		tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
			return springTickMsg{gen: gen}
		}),
		m.scheduleNowTick(),
	)
}

// captureOldSkyline returns the OLD on-screen bar skyline for a bar-mode zoom
// arm: the live lerped skyline if a morph is already in flight (a continuous
// hand-off on a rapid second 'z', mirroring the line-mode live oFrom/oTo read),
// else a fresh raster of the current steady state. barsH is the bar-row count
// (chartHeight minus the x-label row). Called before refreshChart overwrites
// lastValues/peak (#393).
func (m *Model) captureOldSkyline(barsH int) []float64 {
	if m.springActive && m.springKind == springKindZoom && len(m.zoomSnap.oldSky) > 0 {
		return lerpSkyline(m.zoomSnap.oldSky, m.zoomSnap.newSky, m.zoomSpringR)
	}
	return rasterizeSkyline(m.lastValues, m.lastStarts, m.peak, m.viewport.Width, barsH, ZoomLevels[m.zoomIdx])
}

// lerpSkyline returns the element-wise linear interpolation of two equal-length
// per-column height arrays at parameter r. Used both for the per-frame morph
// and for the live-position hand-off on a rapid second 'z' (#393). If the two
// arrays differ in length (defensive — both come from the same viewport.Width
// at arm) it interpolates over the shorter length.
func lerpSkyline(a, b []float64, r float64) []float64 {
	n := min(len(a), len(b))
	out := make([]float64, n)
	for i := range out {
		out[i] = a[i] + (b[i]-a[i])*r
	}
	return out
}

// renderZoomBarFrame paints one frame of the bar-mode zoom morph: lerp the
// snapshotted oldSky→newSky per-column heights by the spring ratio r, draw the
// skyline directly (no ntcharts), and append the cross-faded x-axis label row.
// Mirrors renderZoomFrame (line mode) but for the bar body. The y-axis peak
// label cross-fade is applied separately by renderChartBody (#393).
func (m *Model) renderZoomBarFrame(r float64) {
	zoom := ZoomLevels[m.zoomIdx]
	chartH := m.chartHeight()
	vpW := m.viewport.Width
	barsH := chartH
	if chartH >= 6 {
		barsH = chartH - 1
	}

	cur := lerpSkyline(m.zoomSnap.oldSky, m.zoomSnap.newSky, r)
	body := drawSkyline(cur, barsH, m.zoomSnap.unit)

	if chartH >= 6 {
		labelRow := crossfadeLabelRow(m.zoomSnap, zoom, vpW, m.zoomSpringR, m.dateOrder)
		body = lipgloss.JoinVertical(lipgloss.Left, body, labelRow)
	}

	m.viewport.SetContent(body)
	m.viewport.SetXOffset(0)
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
	// The label row is decoupled from the squeeze: it fades the OLD cadence out
	// in place and the NEW cadence in at its final position, driven only by the
	// spring ratio — viewFrom/viewTo (the lerped window) drive the chart BODY
	// below, not the labels (#382 follow-up).
	labelRow := crossfadeLabelRow(m.zoomSnap, zoom, vpW, m.zoomSpringR, m.dateOrder)
	m.viewport.SetContent(buildLineChart(sliced5h, sliced7d, viewFrom, viewTo, vpW, chartH, m.zoomSnap.now, zoom, m.dateOrder, "zoom", labelRow))
	m.viewport.SetXOffset(0)
}

// handleZoomSpringTick advances one frame of the zoom squeeze: step the spring
// toward r=1, lerp the visible window oWin→nWin, render windowed, and settle
// when within phaseTransitionThreshold of the target. On settle it restores the
// steady-state full canvas via refreshChart and stops the spring loop; the
// live-advance now-tick chain (re-armed on the new cadence at arm time, see
// handleZoomKey) is already live, so settle leaves it untouched (#311
// coexistence). gen is the captured generation; the next tick carries it so a
// superseding 'z'/'u' drops it.
func (m *Model) handleZoomSpringTick(gen int) tea.Cmd {
	r, vel := m.zoomSpring.Update(m.zoomSpringR, m.zoomSpringVel, 1.0)
	m.zoomSpringR = r
	m.zoomSpringVel = vel

	if math.Abs(1.0-r) < phaseTransitionThreshold {
		m.zoomSpringR = 1.0
		m.springActive = false
		m.springKind = springKindNone
		m.refreshChart() // restore the full scrollable canvas at the new zoom.
		// The now-tick chain was already re-armed on the new cadence at arm time
		// (see handleZoomKey) and is still live, so settle neither bumps nor
		// reschedules it — returning nil stops the spring loop, keeping the idle
		// TUI zero-animation-cost (#373).
		return nil
	}

	viewFrom := lerpTime(m.zoomSnap.oFrom, m.zoomSnap.nFrom, r)
	viewTo := lerpTime(m.zoomSnap.oTo, m.zoomSnap.nTo, r)
	if m.zoomSnap.unit == chartUnitRemaining {
		m.renderZoomFrame(viewFrom, viewTo)
	} else {
		m.renderZoomBarFrame(r)
	}

	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}
