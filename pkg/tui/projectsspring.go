// Package tui — projects-box slide animation (issue #416).
//
// Sibling of the zoom-transition machine in zoomspring.go: it reuses the master
// springActive flag and the shared springGen counter (the unit/zoom/projects
// animations are mutually exclusive — refreshChart aborts any in-flight one) and
// is disambiguated by Model.springKind == springKindProjects.
//
// The crux (round two, see spec): every frame is produced by the STEADY
// rendering pipelines at the animated height — the chart via renderWindow /
// buildLineChart at the lever-derived chartHeight, the box re-flowed by the
// steady View path at projectsHeight(). Endpoint frames are byte-identical to
// the steady views by construction. All per-frame inputs are in-memory — no
// DB per frame.
package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
)

// lerpInt linearly interpolates between integer heights a and b at
// parameter r, rounding to the nearest row. r is clamped to [0,1] here —
// the critically-damped spring approaches 1 asymptotically but nothing
// guarantees it never lands marginally outside the interval.
func lerpInt(a, b int, r float64) int {
	r = min(max(r, 0), 1)
	return int(math.Round(float64(a) + (float64(b)-float64(a))*r))
}

// renderProjectsFrame paints one slide frame entirely through the STEADY
// rendering pipelines at the lever-derived (animated) chart height — the
// property that makes the slide's endpoint frames byte-identical to the
// steady views (#416 round two; round one's parallel skyline/snapshot path
// produced mismatched endpoints, shifted+recolored x-labels and an empty
// box). Bar modes go through renderWindow (visible slice, flush-right
// slack, on-screen peak, in-bar labels); remaining mode uses a WINDOWED
// line build at viewport width (#180 rationale: full-canvas rebuild at
// canvasW=2880 blows the 60fps budget — ~41ms/frame at 30-day history vs.
// the 16.7ms allowance). The windowed render is pixel-identical inside the
// visible region because buildLineChart maps time→col linearly via
// WithTimeRange, so the settle transition to refreshChart's full-canvas
// path does not visibly snap. m.viewportXOffset is NOT changed here (the
// logical scroll position must survive to the settle frame, where
// refreshChart restores the full canvas and re-applies the offset via
// setX). All inputs are in-memory — zero DB per frame.
func (m *Model) renderProjectsFrame() {
	chartH := m.chartHeight()
	m.viewport.Height = chartH
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		zoom := ZoomLevels[m.zoomIdx]
		vpW := m.viewport.Width
		viewFrom, viewTo := m.visibleWindow()
		slicedPts5h := slicePointsInRange(m.lastPts5h, viewFrom, viewTo)
		slicedPts7d := slicePointsInRange(m.lastPts7d, viewFrom, viewTo)
		m.viewport.SetContent(buildLineChart(slicedPts5h, slicedPts7d,
			viewFrom, viewTo, vpW, chartH,
			m.now(), zoom, m.dateOrder, "projects", ""))
		m.viewport.SetXOffset(0)
		return
	}
	m.renderWindow()
}

// handleProjectsSpringTick advances one frame of the box slide: step the spring
// toward r=1, lerp the outer box height startH→targetH, re-render the frame, and
// settle when within phaseTransitionThreshold. On settle it commits the height,
// clears the spring, and restores steady state via refreshChart (which chains
// refreshProjects — the 1 settle query on show; a no-op on hide since
// showProjects was committed to false at arm). Returns nil to stop the loop.
func (m *Model) handleProjectsSpringTick(gen int) tea.Cmd {
	r, vel := m.projectsSpring.Update(m.projectsSpringR, m.projectsSpringVel, 1.0)
	m.projectsSpringR, m.projectsSpringVel = r, vel
	m.projectsAnimH = lerpInt(m.projectsSlideFrom, m.projectsSlideTo, r)

	if math.Abs(1.0-r) < phaseTransitionThreshold {
		m.projectsAnimH = m.projectsSlideTo
		m.springActive = false
		m.springKind = springKindNone
		m.viewport.Height = m.chartHeight()
		m.refreshChart() // steady-state restore (chart + chained refreshProjects)
		return nil       // stop the loop — idle TUI is zero-animation-cost
	}

	m.renderProjectsFrame()
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// beginProjectsAnimation arms the box slide. A re-arm mid-slide reverses
// from the CURRENT animated height (every intermediate height renders
// correctly under re-flow — no snap to an extreme first); an in-flight u/z
// is hard-cut via refreshChart exactly as u and z do to each other.
// showProjects commits at arm (keeps u/z aborts free of projects-specific
// wiring). Show pays THE one arm-time ProjectAggregates query via
// refreshProjects (the box was unloaded while hidden, #414); hide pays
// none. The viewport is deliberately NOT repainted: frame 0 of the slide
// IS the current steady frame (show starts at height 0 = the box-hidden
// layout; hide starts at the current target; re-arm wherever the slide
// was) — that no-touch property is half of endpoint identity.
func (m *Model) beginProjectsAnimation() {
	from := m.projectsHeight() // animH mid-slide, steady extreme otherwise
	if m.springActive && m.springKind != springKindProjects {
		m.refreshChart() // abort in-flight u/z; restores steady chart content
	}

	m.showProjects = !m.showProjects
	to := 0
	if m.showProjects {
		to = m.projectsTargetHeight()
		m.refreshProjects() // THE one arm-time query on the show path
	}
	m.projectsSlideFrom, m.projectsSlideTo = from, to
	m.projectsAnimH = from

	m.projectsSpring = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	m.projectsSpringR, m.projectsSpringVel = 0, 0
	m.springActive = true
	m.springKind = springKindProjects
	m.springGen++
}
