package tui

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
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

// renderProjectsFrame must keep viewport.Height in lockstep with the
// lever-derived chartHeight every frame (round-one finding ccpulse-416.1).
func TestRenderProjectsFrame_SetsViewportHeightToChartHeight(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	for range 4 { // advance a few frames so projectsAnimH is mid-flight
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("slide settled in 4 ticks; cannot probe mid-flight")
	}
	if m.viewport.Height != m.chartHeight() {
		t.Errorf("viewport.Height=%d, want chartHeight()=%d", m.viewport.Height, m.chartHeight())
	}
}

// TestView_DuringSlide_HeightConservedRealBorder probes a mid-flight frame:
// total height is conserved (chartHeight() + projectsHeight() == m.h - 7 and
// the rendered frame is exactly m.h rows), and the box band carries the REAL
// renderProjectsBox top border + title — re-flowed at the animated height,
// not a phantom-topped bottom slice (#416 round two).
func TestView_DuringSlide_HeightConservedRealBorder(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = true
	m.refreshProjects()
	m.projectsSnap = projectsAnimSnapshot{
		startH: 0, targetH: 12,
	}
	m.springActive = true
	m.springKind = springKindProjects
	m.projectsAnimH = 5
	m.renderProjectsFrame()

	if got, want := m.chartHeight()+m.projectsHeight(), m.h-7; got != want {
		t.Errorf("chartHeight+projectsHeight=%d, want %d (height lever conserved)", got, want)
	}
	frame := m.View()
	if got := lipgloss.Height(frame); got != m.h {
		t.Errorf("View height=%d, want %d (conserved every frame)", got, m.h)
	}
	// animH=5 ≥ 4: the box band must be the real re-flowed box — top border
	// with rounded corners and the title row right beneath it. The header is
	// also a rounded-bordered block, so the box's top border is the LAST ╭
	// row in the frame.
	lines := strings.Split(frame, "\n")
	topIdx := -1
	for i, line := range lines {
		if strings.Contains(line, "╭") {
			topIdx = i
		}
	}
	if topIdx == -1 {
		t.Fatal("mid-slide frame missing the box top border")
	}
	if topIdx+1 >= len(lines) || !strings.Contains(lines[topIdx+1], projectsTitle) {
		t.Errorf("row beneath the top border lacks the title %q (box not re-flowed)", projectsTitle)
	}
}

// armProjectsShowForTest hand-builds a fully-armed SHOW slide (no key handler,
// so this task is testable before Task 4). Mirrors what beginProjectsAnimation
// will set up.
func armProjectsShowForTest(t testing.TB, m *Model) {
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

func TestProjectsKey_ShowFromIdle_ArmsAndQueriesOnce(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false // hidden by default (#414) → first 'p' is a show
	m.refreshChart()       // ensure steady chart inputs present; projectAggs stays nil (hidden)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if !m.springActive || m.springKind != springKindProjects {
		t.Fatalf("show 'p': springActive=%v springKind=%d, want true/projects", m.springActive, m.springKind)
	}
	if !m.showProjects {
		t.Error("show 'p': showProjects=false, want true (committed at arm)")
	}
	if m.projectsSnap.startH != 0 || m.projectsSnap.targetH != m.projectsTargetHeight() {
		t.Errorf("show snap heights = (%d,%d), want (0,%d)", m.projectsSnap.startH, m.projectsSnap.targetH, m.projectsTargetHeight())
	}
	if len(m.projectsSnap.boxRows) == 0 {
		t.Error("show 'p': boxRows not snapshotted (arm requery missing)")
	}
	if cmd == nil {
		t.Error("show 'p': cmd=nil, want first tick scheduled")
	}

	// Zero-DB-per-frame contract: the arm query repopulated projectAggs once;
	// driving mid-flight ticks must NOT reissue ProjectAggregates (the slice's
	// backing array is untouched), and settle reissues exactly once (new array).
	armPtr := projectAggsBackingPtr(m.projectAggs)
	for range 3 { // safely mid-flight (critically-damped spring needs ~15+ ticks)
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("3 ticks settled the slide unexpectedly; can't probe mid-flight")
	}
	if projectAggsBackingPtr(m.projectAggs) != armPtr {
		t.Error("projectAggs reassigned mid-slide → a per-tick refreshProjects ran (want zero DB per frame)")
	}
}

func TestProjectsKey_HideFromIdle_NoArmQuery(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = true
	m.refreshChart() // box shown → projectAggs populated
	if len(m.projectAggs) == 0 {
		t.Fatal("seed: projectAggs empty, want populated before hide")
	}
	beforePtr := projectAggsBackingPtr(m.projectAggs)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if !m.springActive || m.springKind != springKindProjects {
		t.Fatalf("hide 'p': springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	if m.showProjects {
		t.Error("hide 'p': showProjects=true, want false (committed at arm)")
	}
	if m.projectsSnap.startH != m.projectsTargetHeight() || m.projectsSnap.targetH != 0 {
		t.Errorf("hide snap heights=(%d,%d), want (%d,0)", m.projectsSnap.startH, m.projectsSnap.targetH, m.projectsTargetHeight())
	}
	// No arm requery on hide: the snapshot reused the already-populated aggs.
	if projectAggsBackingPtr(m.projectAggs) != beforePtr {
		t.Error("hide 'p' reissued ProjectAggregates at arm, want 0 queries (reuse in-memory aggs)")
	}
}

func TestProjectsKey_ReduceMotion_Snaps(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.deps.ReduceMotion = true
	m.showProjects = false

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if m.springActive {
		t.Error("reduce_motion 'p': springActive=true, want snap")
	}
	if !m.showProjects {
		t.Error("reduce_motion 'p': showProjects=false, want toggled on")
	}
	if cmd != nil {
		t.Errorf("reduce_motion 'p': cmd=%v, want nil (synchronous cut)", cmd)
	}
	if m.viewport.Height != m.chartHeight() {
		t.Errorf("reduce_motion 'p': viewport.Height=%d, want chartHeight=%d", m.viewport.Height, m.chartHeight())
	}
}

func TestProjectsKey_TooShort_Snaps(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.h = 12 // m.h-7=5 < 9 → projectsTargetHeight()==0
	m.viewport.Height = m.chartHeight()
	m.showProjects = false

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if m.springActive {
		t.Error("too-short 'p': springActive=true, want snap")
	}
	if cmd != nil {
		t.Errorf("too-short 'p': cmd=%v, want nil", cmd)
	}
}

func TestProjectsKey_AbortsInflightZoom(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false

	// Arm a zoom, then press 'p' mid-flight.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if m.springKind != springKindZoom {
		t.Fatalf("setup: springKind=%d, want zoom", m.springKind)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if m.springKind != springKindProjects || !m.springActive {
		t.Errorf("'p' during zoom: springKind=%d active=%v, want projects/true (zoom aborted, slide armed)", m.springKind, m.springActive)
	}
}

func TestZoomKey_AbortsInflightProjectsSlide(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}) // arm show slide
	m = updated.(Model)
	if m.springKind != springKindProjects {
		t.Fatalf("setup: springKind=%d, want projects", m.springKind)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springKind != springKindZoom || !m.springActive {
		t.Errorf("'z' during slide: springKind=%d active=%v, want zoom/true", m.springKind, m.springActive)
	}
	if !m.showProjects {
		t.Error("'z' during show-slide: showProjects=false, want true (slide's committed terminal state)")
	}
}

// TestProjectsSlide_EndpointIdentity_BarMode is the headline #416-round-two
// property: the slide's frame 0 is byte-identical to the steady pre-slide
// View, and the settle frame is byte-identical to the steady post-slide View
// — both directions, under forced color so styling drift fails the test.
func TestProjectsSlide_EndpointIdentity_BarMode(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false // hidden by default (#414) → first 'p' is a show
	m.refreshChart()

	m = assertSlideEndpoints(t, m, "show")
	m = assertSlideEndpoints(t, m, "hide")
	_ = m
}

// assertSlideEndpoints presses 'p', asserts frame-0 identity, drives the
// spring to settle via constructed springTickMsg (never the real tea.Tick
// Cmd), and asserts settle identity against a fresh steady re-render.
func assertSlideEndpoints(t *testing.T, m Model, dir string) Model {
	t.Helper()
	pre := m.View()

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatalf("%s: 'p' returned nil cmd, want first tick scheduled", dir)
	}
	if !m.springActive || m.springKind != springKindProjects {
		t.Fatalf("%s: springActive=%v kind=%d, want true/projects", dir, m.springActive, m.springKind)
	}
	if got := m.View(); got != pre {
		t.Errorf("%s: frame 0 differs from steady pre-slide view\nframe0:\n%s\nsteady:\n%s", dir, got, pre)
	}

	for i := 0; m.springActive; i++ {
		if i > 600 {
			t.Fatalf("%s: slide did not settle within 600 ticks", dir)
		}
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	settled := m.View()
	m.refreshChart() // independent steady re-render of the post-slide state
	if post := m.View(); settled != post {
		t.Errorf("%s: settle frame differs from steady post-slide view\nsettle:\n%s\nsteady:\n%s", dir, settled, post)
	}
	return m
}

// TestProjectsSlide_BoxContentPresentEarly guards the round-one "box rose
// empty" defect: as soon as the box band is a few rows tall it must carry
// the real top border, the title, and (one row later) the top spender —
// renderProjectsBox re-flowed at the animated height, not a blank-padded
// pre-render sliced bottom-first.
func TestProjectsSlide_BoxContentPresentEarly(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if len(m.projectAggs) == 0 {
		t.Fatal("arm did not populate projectAggs (show-path requery missing)")
	}
	topLabel := m.projectAggs[0].Label

	sawTitle := false
	for i := 0; m.springActive && i < 600; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		frame := m.View()
		if lipgloss.Height(frame) != m.h {
			t.Fatalf("tick %d: frame height %d != terminal height %d", i, lipgloss.Height(frame), m.h)
		}
		if m.springActive && m.projectsAnimH >= 4 {
			if !strings.Contains(frame, projectsTitle) {
				t.Fatalf("animH=%d: frame lacks box title %q — box rendering empty", m.projectsAnimH, projectsTitle)
			}
			sawTitle = true
		}
		if m.springActive && m.projectsAnimH >= 5 && !strings.Contains(frame, topLabel) {
			t.Fatalf("animH=%d: frame lacks top spender %q — content not re-flowed", m.projectsAnimH, topLabel)
		}
	}
	if !sawTitle {
		t.Fatal("slide settled without ever sampling a frame at animH >= 4")
	}
}

// TestProjectsSlide_XLabelRowStable guards round-one defects 3+4: during the
// slide the x-axis label row must keep its exact content, column position
// and ANSI styling — the steady renderer's own label row, not a synthetic
// re-creation. Asserted by requiring the steady view's label line to appear
// verbatim (bytes, incl. color) in every sampled mid-slide frame.
func TestProjectsSlide_XLabelRowStable(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false
	m.refreshChart()

	labelRow := findXLabelRow(t, m.View())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	for i := 0; m.springActive && i < 600; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		if m.springActive && m.chartHeight() >= 6 && !strings.Contains(m.View(), labelRow) {
			t.Fatalf("tick %d (chartH=%d): steady x-label row missing/altered mid-slide\nwant line: %q", i, m.chartHeight(), labelRow)
		}
	}
}

// findXLabelRow returns the first View line carrying >= 2 HH:MM time labels
// — the bar chart's x-axis row.
func findXLabelRow(t *testing.T, view string) string {
	t.Helper()
	re := regexp.MustCompile(`\d{1,2}:\d{2}`)
	for line := range strings.SplitSeq(view, "\n") {
		if len(re.FindAllString(line, -1)) >= 2 {
			return line
		}
	}
	t.Fatal("no x-label row found in steady view")
	return ""
}

// projectAggsBackingPtr returns the backing-array address of a ProjectAggregate
// slice, or 0 if empty. refreshProjects reassigns m.projectAggs to a fresh slice
// from ProjectAggregates, so a changed pointer ⇒ a query ran. Used to prove the
// zero-DB-per-frame contract without a cache interface seam.
func projectAggsBackingPtr(a []cache.ProjectAggregate) uintptr {
	if len(a) == 0 {
		return 0
	}
	return reflect.ValueOf(a).Pointer()
}

// TestProjectsSlide_RealFrame_BoundaryMovesMonotonically drives a real show
// slide tick-by-tick and asserts on the actual painted frame (withForcedColor →
// real ANSI): the box band grows monotonically, the phantom top border is
// present every mid-slide frame, the true top border lands only at settle, and
// total height is conserved. Per the project's real-binary-verification rule,
// the assertion is on View() output (the painted frame), not internal counters.
func TestProjectsSlide_RealFrame_BoundaryMovesMonotonically(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.showProjects = false
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	roundedTop := lipgloss.RoundedBorder().TopLeft // "╭"
	prevBand := -1
	const maxTicks = 600
	for i := range maxTicks {
		frame := m.View()
		if h := lipgloss.Height(frame); h != m.h {
			t.Fatalf("tick %d: frame height=%d, want %d (conserved)", i, h, m.h)
		}
		band := m.projectsAnimH
		if band > 0 && band < m.projectsSnap.targetH { // mid-slide
			if !strings.Contains(frame, roundedTop) {
				t.Errorf("tick %d (band=%d): phantom top border absent", i, band)
			}
			if band < prevBand {
				t.Errorf("tick %d: band=%d < prev=%d (non-monotonic)", i, band, prevBand)
			}
		}
		prevBand = band
		if !m.springActive {
			break
		}
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("slide did not settle within %d ticks", maxTicks)
	}
	// Settle frame: full box present (title visible), at the steady target height.
	final := m.View()
	if !strings.Contains(final, projectsTitle) {
		t.Error("settle frame missing the projects box title (full box not restored)")
	}
	if m.projectsAnimH != m.projectsSnap.targetH {
		t.Errorf("settle animH=%d, want target %d", m.projectsAnimH, m.projectsSnap.targetH)
	}
}
