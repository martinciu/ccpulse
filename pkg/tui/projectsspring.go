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

// renderProjectsAnimFrame rebuilds the viewport chart body at the frame's
// (animated) height from in-memory snapshot state — bar mode re-rasterizes the
// skyline so bars rescale into the new row count; line mode re-builds the
// windowed line chart. No DB, no ntcharts rebuild beyond the windowed line
// canvas. The box band is composed separately by View.
func (m *Model) renderProjectsAnimFrame() {
	chartH := m.chartHeight() // derives from projectsAnimH via the projectsHeight lever
	m.viewport.Height = chartH
	snap := m.projectsSnap

	if snap.isLine {
		body := buildLineChart(snap.pts5h, snap.pts7d, snap.viewFrom, snap.viewTo,
			snap.vpWidth, chartH, m.now(), snap.zoom, m.dateOrder, "projects", "")
		m.viewport.SetContent(body)
		m.viewport.SetXOffset(0)
		return
	}

	barsH := chartH
	if chartH >= 6 {
		barsH = chartH - 1
	}
	sky := rasterizeSkyline(snap.values, snap.starts, snap.peak, snap.vpWidth, barsH, snap.zoom)
	body := drawSkyline(sky, barsH, snap.unit)
	if chartH >= 6 {
		labelRow := buildXLabelsRow(synthLabelStarts(snap.viewFrom, snap.viewTo, snap.zoom),
			snap.vpWidth, snap.zoom, m.now(), m.dateOrder)
		body = lipgloss.JoinVertical(lipgloss.Left, body, labelRow)
	}
	m.viewport.SetContent(body)
	m.viewport.SetXOffset(0)
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

	m.renderProjectsAnimFrame()
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}
