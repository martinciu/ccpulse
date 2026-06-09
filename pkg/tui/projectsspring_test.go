package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"
)

func TestLerpInt(t *testing.T) {
	cases := []struct {
		a, b int
		r    float64
		want int
	}{
		{0, 12, 0, 0},
		{0, 12, 1, 12},
		{0, 12, 0.5, 6},
		{12, 0, 0.5, 6},
		{12, 0, 1, 0},
		{0, 10, 0.24, 2}, // 2.4 rounds to 2
		{0, 10, 0.25, 3}, // 2.5 rounds to 3 (math.Round)
	}
	for _, c := range cases {
		if got := lerpInt(c.a, c.b, c.r); got != c.want {
			t.Errorf("lerpInt(%d,%d,%g)=%d, want %d", c.a, c.b, c.r, got, c.want)
		}
	}
}

func TestProjectsHeight_SpringBranchOverridesTarget(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()

	// Steady target (122x40 → m.h-7=33 → min(16,12)=12).
	m.showProjects = true
	if got, want := m.projectsHeight(), m.projectsTargetHeight(); got != want {
		t.Fatalf("steady projectsHeight()=%d, want projectsTargetHeight()=%d", got, want)
	}
	if m.projectsTargetHeight() != 12 {
		t.Fatalf("projectsTargetHeight()=%d, want 12 at 122x40", m.projectsTargetHeight())
	}

	// Spring branch: returns projectsAnimH regardless of showProjects.
	m.springActive = true
	m.springKind = springKindProjects
	m.projectsAnimH = 7
	if got := m.projectsHeight(); got != 7 {
		t.Errorf("in-slide projectsHeight()=%d, want 7 (animated)", got)
	}
	m.showProjects = false
	if got := m.projectsHeight(); got != 7 {
		t.Errorf("in-slide projectsHeight() with showProjects=false=%d, want 7", got)
	}
}

func TestProjectsTopBorder_WidthAndCorners(t *testing.T) {
	b := projectsTopBorder(80)
	if w := lipgloss.Width(b); w != 80 {
		t.Errorf("projectsTopBorder width=%d, want 80", w)
	}
	// Rounded border corners (strip styling via lipgloss.Width-agnostic contains).
	if !strings.Contains(b, "╭") || !strings.Contains(b, "╮") {
		t.Errorf("projectsTopBorder missing rounded corners: %q", b)
	}
}

func TestProjectsBandRows_RevealsBottomWithPhantomTop(t *testing.T) {
	rows := []string{"TOPBORDER", "title", "r1", "r2", "BOTBORDER"} // 5-row box

	// animH=0 → no band.
	if got := projectsBandRows(rows, 40, 0); got != nil {
		t.Errorf("animH=0 band=%v, want nil", got)
	}
	// animH=1 → just the phantom top border.
	band := projectsBandRows(rows, 40, 1)
	if len(band) != 1 || !strings.Contains(band[0], "╭") {
		t.Errorf("animH=1 band=%v, want [phantom-top]", band)
	}
	// animH=3 → phantom top + bottom 2 rows (r2, BOTBORDER).
	band = projectsBandRows(rows, 40, 3)
	if len(band) != 3 {
		t.Fatalf("animH=3 len(band)=%d, want 3", len(band))
	}
	if !strings.Contains(band[0], "╭") || band[1] != "r2" || band[2] != "BOTBORDER" {
		t.Errorf("animH=3 band=%v, want [phantom, r2, BOTBORDER]", band)
	}
	// animH>=len → full box verbatim (settle frame).
	band = projectsBandRows(rows, 40, 5)
	if len(band) != 5 || band[0] != "TOPBORDER" {
		t.Errorf("animH=5 band=%v, want full rows", band)
	}
}

func TestRenderProjectsAnimFrame_SetsViewportHeightToChartHeight(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()

	// Hand-arm a mid-slide state (show, animH=5 of target 12).
	m.showProjects = true
	m.refreshProjects()
	m.projectsSnap = projectsAnimSnapshot{
		boxRows: strings.Split(renderProjectsBox(m.projectAggs, m.w, 12), "\n"),
		startH:  0, targetH: 12,
		values: m.lastValues, starts: m.lastStarts, peak: m.peak,
		unit: chartUnitCost, isLine: false, vpWidth: m.viewport.Width,
		zoom: ZoomLevels[m.zoomIdx], viewFrom: m.lastChartFrom, viewTo: m.lastChartTo,
	}
	m.springActive = true
	m.springKind = springKindProjects
	m.projectsAnimH = 5

	m.renderProjectsAnimFrame()

	wantChartH := m.h - 7 - 5 // 33-5 = 28
	if m.viewport.Height != wantChartH {
		t.Errorf("viewport.Height=%d, want chartHeight=%d during slide", m.viewport.Height, wantChartH)
	}
	if m.chartHeight() != wantChartH {
		t.Errorf("chartHeight()=%d, want %d", m.chartHeight(), wantChartH)
	}
	if m.viewport.View() == "" {
		t.Error("viewport content empty after renderProjectsAnimFrame")
	}
}

func TestView_DuringSlide_HeightConservedAndPhantomBorder(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = true
	m.refreshProjects()
	m.projectsSnap = projectsAnimSnapshot{
		boxRows: strings.Split(renderProjectsBox(m.projectAggs, m.w, 12), "\n"),
		startH:  0, targetH: 12,
		values: m.lastValues, starts: m.lastStarts, peak: m.peak,
		unit: chartUnitCost, isLine: false, vpWidth: m.viewport.Width,
		zoom: ZoomLevels[m.zoomIdx], viewFrom: m.lastChartFrom, viewTo: m.lastChartTo,
	}
	m.springActive = true
	m.springKind = springKindProjects
	m.projectsAnimH = 5
	m.renderProjectsAnimFrame()

	frame := m.View()
	if got := lipgloss.Height(frame); got != m.h {
		t.Errorf("View height=%d, want %d (conserved every frame)", got, m.h)
	}
	if !strings.Contains(frame, "╭") {
		t.Error("mid-slide frame missing phantom top border")
	}
}

// armProjectsShowForTest hand-builds a fully-armed SHOW slide (no key handler,
// so this task is testable before Task 4). Mirrors what beginProjectsAnimation
// will set up.
func armProjectsShowForTest(t *testing.T, m *Model) {
	t.Helper()
	m.showProjects = true
	m.refreshProjects()
	target := m.projectsTargetHeight()
	m.projectsSnap = projectsAnimSnapshot{
		boxRows: strings.Split(renderProjectsBox(m.projectAggs, m.w, target), "\n"),
		startH:  0, targetH: target,
		values: m.lastValues, starts: m.lastStarts, peak: m.peak,
		unit: chartUnitCost, isLine: false, vpWidth: m.viewport.Width,
		zoom: ZoomLevels[m.zoomIdx], viewFrom: m.lastChartFrom, viewTo: m.lastChartTo,
	}
	m.projectsSpring = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	m.projectsSpringR, m.projectsSpringVel = 0, 0
	m.projectsAnimH = 0
	m.springActive = true
	m.springKind = springKindProjects
	m.springGen++
}

func TestProjectsSpringTick_AdvancesThenSettles(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	armProjectsShowForTest(t, &m)
	target := m.projectsSnap.targetH

	// One tick: ratio moves off 0, animH advances toward target.
	updated, cmd := m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)
	if m.projectsSpringR <= 0 {
		t.Errorf("after one tick: projectsSpringR=%g, want >0", m.projectsSpringR)
	}
	if cmd == nil {
		t.Error("mid-slide tick returned nil cmd, want next tick scheduled")
	}
	if m.projectsAnimH < 0 || m.projectsAnimH > target {
		t.Errorf("projectsAnimH=%d out of [0,%d]", m.projectsAnimH, target)
	}

	// Drive to settle (never invoke the tick Cmd — it real-sleeps; construct msgs).
	const maxTicks = 600
	var lastCmd tea.Cmd
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, lastCmd = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("projects slide did not settle within %d ticks", maxTicks)
	}
	if m.springKind != springKindNone {
		t.Errorf("after settle: springKind=%d, want springKindNone", m.springKind)
	}
	if lastCmd != nil {
		t.Errorf("settle: cmd=%v, want nil (loop stops — idle TUI zero-cost)", lastCmd)
	}
	if m.projectsAnimH != target {
		t.Errorf("after settle: projectsAnimH=%d, want target %d", m.projectsAnimH, target)
	}
	if !m.showProjects {
		t.Error("after show settle: showProjects=false, want true (committed)")
	}
	if m.viewport.Height != m.chartHeight() {
		t.Errorf("after settle: viewport.Height=%d, want chartHeight=%d", m.viewport.Height, m.chartHeight())
	}
}

func TestProjectsSpringTick_StaleGenDropped(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	armProjectsShowForTest(t, &m)

	updated, cmd := m.Update(springTickMsg{gen: m.springGen - 1}) // superseded
	m = updated.(Model)
	if cmd != nil {
		t.Errorf("stale-gen tick: cmd=%v, want nil (dropped)", cmd)
	}
	if !m.springActive {
		t.Error("stale-gen tick must not settle the live animation")
	}
}
