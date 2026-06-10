// Package tui — projects-box slide animation (issue #416).
//
// Sibling of the zoom-transition machine in zoomspring.go: it reuses the master
// springActive flag and the shared springGen counter (the unit/zoom/projects
// animations are mutually exclusive — refreshChart aborts any in-flight one) and
// is disambiguated by Model.springKind == springKindProjects.
//
// The crux (see spec): the CHART rescales (re-rasterized at the interpolated
// height each frame, bars shrink/grow into fewer/more rows) while the BOX slices
// (rendered once at target height at arm, bottom rows revealed each frame with a
// phantom top border). Both read only in-memory snapshot state — no DB, no
// ntcharts rebuild, per frame.
package tui

import (
	"math"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// projectsAnimSnapshot captures, once at arm time, everything the per-frame
// slide tick needs so nothing it reads can shift mid-animation.
type projectsAnimSnapshot struct {
	boxRows []string // box pre-rendered at the steady target height, split into rows (the SLICE source)
	startH  int      // slide start outer height (show: 0, hide: target)
	targetH int      // slide end   outer height (show: target, hide: 0)

	// Chart inputs — frozen so the per-frame re-rasterize/re-build reads no DB.
	values   []float64
	starts   []time.Time
	peak     float64
	pts5h    []cache.UtilizationPoint
	pts7d    []cache.UtilizationPoint
	unit     chartUnit
	isLine   bool
	vpWidth  int
	zoom     ZoomLevel
	viewFrom time.Time // line mode: stable visible window (no horizontal squeeze in this slide)
	viewTo   time.Time
}

// lerpInt linearly interpolates between integer heights a and b at parameter r,
// rounding to the nearest row. r is clamped to [0,1] by the caller's spring.
func lerpInt(a, b int, r float64) int {
	return int(math.Round(float64(a) + (float64(b)-float64(a))*r))
}

// projectsTopBorder renders a phantom rounded top-border line `width` cols wide,
// matching renderProjectsBox's RoundedBorder + colorMuted, so the sliced box
// reads as a complete bordered box at the cut throughout the slide.
func projectsTopBorder(width int) string {
	b := lipgloss.RoundedBorder()
	inner := max(width-2, 0)
	line := b.TopLeft + strings.Repeat(b.Top, inner) + b.TopRight
	return lipgloss.NewStyle().Foreground(colorMuted).Render(line)
}

// projectsBandRows returns the bottom `animH` rows of the once-rendered box
// `rows`, with the topmost visible row replaced by a phantom top border. animH<=0
// → nil (nothing to show); animH>=len → the full box verbatim (settle frame).
func projectsBandRows(rows []string, width, animH int) []string {
	if animH <= 0 || len(rows) == 0 {
		return nil
	}
	if animH >= len(rows) {
		return rows
	}
	band := make([]string, 0, animH)
	band = append(band, projectsTopBorder(width))
	band = append(band, rows[len(rows)-(animH-1):]...) // bottom (animH-1) rows incl. real bottom border
	return band
}

// renderProjectsFrame paints one slide frame entirely through the STEADY
// rendering pipelines at the lever-derived (animated) chart height — the
// property that makes the slide's endpoint frames byte-identical to the
// steady views (#416 round two; round one's parallel skyline/snapshot path
// produced mismatched endpoints, shifted+recolored x-labels and an empty
// box). Bar modes go through renderWindow (visible slice, flush-right
// slack, on-screen peak, in-bar labels); remaining mode re-issues the
// steady full-canvas line build + offset re-apply. All inputs are
// in-memory — zero DB per frame.
func (m *Model) renderProjectsFrame() {
	chartH := m.chartHeight()
	m.viewport.Height = chartH
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		zoom := ZoomLevels[m.zoomIdx]
		m.viewport.SetContent(buildLineChart(m.lastPts5h, m.lastPts7d,
			m.lastChartFrom, m.lastChartTo, m.lastCanvasW, chartH,
			m.now(), zoom, m.dateOrder, "projects", ""))
		m.setX(m.viewportXOffset)
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
	m.projectsAnimH = lerpInt(m.projectsSnap.startH, m.projectsSnap.targetH, r)

	if math.Abs(1.0-r) < phaseTransitionThreshold {
		m.projectsAnimH = m.projectsSnap.targetH
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

// beginProjectsAnimation arms the box slide. Aborts any in-flight u/z FIRST (only
// when one is running — calling refreshChart unconditionally would fire a wasted
// refreshProjects query on the hide path). Commits showProjects to its terminal
// value at arm so a later u/z abort reads the correct chartHeight with no extra
// wiring (see series.go abort block). Snapshots the box (one requery on show; the
// in-memory aggs on hide) and the chart inputs, seeds the spring, paints frame 0.
func (m *Model) beginProjectsAnimation() {
	if m.springActive {
		m.refreshChart() // abort in-flight u/z; restore steady chart inputs to snapshot
	}

	show := !m.showProjects
	m.showProjects = show // committed terminal state (the invariant that makes abort free)

	target := m.projectsTargetHeight()
	if show {
		m.refreshProjects() // THE one arm-time query: box was hidden (#414) → repopulate
		m.projectsSnap.startH, m.projectsSnap.targetH = 0, target
	} else {
		// projectAggs already populated (box was showing) — no query.
		m.projectsSnap.startH, m.projectsSnap.targetH = target, 0
	}

	m.projectsSnap.boxRows = strings.Split(renderProjectsBox(m.projectAggs, m.w, target), "\n")
	m.projectsSnap.values = m.lastValues
	m.projectsSnap.starts = m.lastStarts
	m.projectsSnap.peak = m.peak
	m.projectsSnap.pts5h = m.lastPts5h
	m.projectsSnap.pts7d = m.lastPts7d
	m.projectsSnap.unit = chartUnit(m.unitIdx)
	m.projectsSnap.isLine = isLineMode(chartUnit(m.unitIdx))
	m.projectsSnap.vpWidth = m.viewport.Width
	m.projectsSnap.zoom = ZoomLevels[m.zoomIdx]
	m.projectsSnap.viewFrom, m.projectsSnap.viewTo = m.visibleWindow()

	m.projectsSpring = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	m.projectsSpringR, m.projectsSpringVel = 0, 0
	m.projectsAnimH = m.projectsSnap.startH
	m.springActive = true
	m.springKind = springKindProjects
	m.springGen++

	// The viewport is deliberately NOT repainted here: frame 0 of the slide IS
	// the current steady frame (show starts at height 0 = the box-hidden
	// layout; hide starts at the current target). That no-touch property is
	// half of the endpoint-identity guarantee (#416 round two).
}
