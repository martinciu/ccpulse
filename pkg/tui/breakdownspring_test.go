package tui

import (
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
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
		{0, 10, -0.2, 0}, // r clamps to 0 (spring may undershoot marginally)
		{0, 10, 1.3, 10}, // r clamps to 1 (spring may overshoot marginally)
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

	// Steady target is content-aware (#420): the single-project fixture
	// needs border(2)+title(1)+1 body row = 4, well under the 122x40 cap
	// (m.h-7=33 → upper min(16,12)=12). The empty-aggs floor is also 4, so
	// the value holds whether or not refreshBreakdown ran.
	m.breakdown = breakdownProjects
	if got, want := m.breakdownHeight(), m.breakdownTargetHeight(); got != want {
		t.Fatalf("steady breakdownHeight()=%d, want breakdownTargetHeight()=%d", got, want)
	}
	if m.breakdownTargetHeight() != 4 {
		t.Fatalf("breakdownTargetHeight()=%d, want 4 (content-aware, 1 project) at 122x40", m.breakdownTargetHeight())
	}

	// Spring branch: returns breakdownAnimH regardless of m.breakdown.
	m.springActive = true
	m.springKind = springKindBreakdown
	m.breakdownAnimH = 7
	if got := m.breakdownHeight(); got != 7 {
		t.Errorf("in-slide breakdownHeight()=%d, want 7 (animated)", got)
	}
	m.breakdown = breakdownNone
	if got := m.breakdownHeight(); got != 7 {
		t.Errorf("in-slide breakdownHeight() with breakdown=none=%d, want 7", got)
	}
}

// renderBreakdownFrame must keep viewport.Height in lockstep with the
// lever-derived chartHeight every frame (round-one finding ccpulse-416.1).
func TestRenderProjectsFrame_SetsViewportHeightToChartHeight(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	for range 4 { // advance a few frames so breakdownAnimH is mid-flight
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
// total height is conserved (chartHeight() + breakdownHeight() == m.h - 7 and
// the rendered frame is exactly m.h rows), and the box band carries the REAL
// renderBreakdownBox top border + title — re-flowed at the animated height,
// not a phantom-topped bottom slice (#416 round two).
func TestView_DuringSlide_HeightConservedRealBorder(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()
	m.breakdownSlideFrom, m.breakdownSlideTo = 0, 12
	m.springActive = true
	m.springKind = springKindBreakdown
	m.breakdownAnimH = 5
	m.renderBreakdownFrame()

	if got, want := m.chartHeight()+m.breakdownHeight(), m.h-7; got != want {
		t.Errorf("chartHeight+breakdownHeight=%d, want %d (height lever conserved)", got, want)
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
	if topIdx+1 >= len(lines) || !strings.Contains(lines[topIdx+1], breakdownProjectsTitle) {
		t.Errorf("row beneath the top border lacks the title %q (box not re-flowed)", breakdownProjectsTitle)
	}
}

// armProjectsShowForTest arms a SHOW slide through the production arm path
// (the box starts hidden; beginBreakdownAnimation commits the target, pays the
// arm-time aggs query, and seeds the spring without repainting frame 0).
func armProjectsShowForTest(t testing.TB, m *Model) {
	t.Helper()
	m.breakdown = breakdownNone
	m.beginBreakdownAnimation(breakdownProjects)
}

func TestProjectsSpringTick_AdvancesThenSettles(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	armProjectsShowForTest(t, &m)
	target := m.breakdownSlideTo

	// One tick: ratio moves off 0, animH advances toward target.
	updated, cmd := m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)
	if m.breakdownSpringR <= 0 {
		t.Errorf("after one tick: breakdownSpringR=%g, want >0", m.breakdownSpringR)
	}
	if cmd == nil {
		t.Error("mid-slide tick returned nil cmd, want next tick scheduled")
	}
	if m.breakdownAnimH < 0 || m.breakdownAnimH > target {
		t.Errorf("breakdownAnimH=%d out of [0,%d]", m.breakdownAnimH, target)
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
	if m.breakdownAnimH != target {
		t.Errorf("after settle: breakdownAnimH=%d, want target %d", m.breakdownAnimH, target)
	}
	if m.breakdown == breakdownNone {
		t.Error("after show settle: breakdown=none, want projects (committed)")
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
	m.breakdown = breakdownNone // hidden by default (#414) → first 'p' is a show
	m.refreshChart()            // ensure steady chart inputs present; breakdownRows stays nil (hidden)

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("show 'p': springActive=%v springKind=%d, want true/projects", m.springActive, m.springKind)
	}
	if m.breakdown == breakdownNone {
		t.Error("show 'p': breakdown=none, want projects (committed at arm)")
	}
	if m.breakdownSlideFrom != 0 || m.breakdownSlideTo != m.breakdownTargetHeight() {
		t.Errorf("show slide from/to = (%d,%d), want (0,%d)", m.breakdownSlideFrom, m.breakdownSlideTo, m.breakdownTargetHeight())
	}
	if len(m.breakdownRows) == 0 {
		t.Error("show 'p': breakdownRows empty after arm (requery missing)")
	}
	if cmd == nil {
		t.Error("show 'p': cmd=nil, want first tick scheduled")
	}

	// Zero-DB-per-frame contract: the arm query repopulated breakdownRows once;
	// driving mid-flight ticks must NOT reissue ProjectAggregates (the slice's
	// backing array is untouched), and settle reissues exactly once (new array).
	armPtr := breakdownRowsBackingPtr(m.breakdownRows)
	for range 3 { // safely mid-flight (critically-damped spring needs ~15+ ticks)
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("3 ticks settled the slide unexpectedly; can't probe mid-flight")
	}
	if breakdownRowsBackingPtr(m.breakdownRows) != armPtr {
		t.Error("breakdownRows reassigned mid-slide → a per-tick refreshBreakdown ran (want zero DB per frame)")
	}
}

func TestProjectsKey_HideFromIdle_NoArmQuery(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshChart() // box shown → breakdownRows populated
	if len(m.breakdownRows) == 0 {
		t.Fatal("seed: breakdownRows empty, want populated before hide")
	}
	beforePtr := breakdownRowsBackingPtr(m.breakdownRows)

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("hide 'p': springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	if m.breakdown != breakdownNone {
		t.Error("hide 'p': breakdown=projects, want none (committed at arm)")
	}
	if m.breakdownSlideFrom != m.breakdownTargetHeight() || m.breakdownSlideTo != 0 {
		t.Errorf("hide slide from/to=(%d,%d), want (%d,0)", m.breakdownSlideFrom, m.breakdownSlideTo, m.breakdownTargetHeight())
	}
	// No arm requery on hide: the snapshot reused the already-populated aggs.
	if breakdownRowsBackingPtr(m.breakdownRows) != beforePtr {
		t.Error("hide 'p' reissued ProjectAggregates at arm, want 0 queries (reuse in-memory aggs)")
	}
}

func TestProjectsKey_ReduceMotion_Snaps(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.deps.ReduceMotion = true
	m.breakdown = breakdownNone

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if m.springActive {
		t.Error("reduce_motion 'p': springActive=true, want snap")
	}
	if m.breakdown == breakdownNone {
		t.Error("reduce_motion 'p': breakdown=none, want toggled on")
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
	m.h = 12 // m.h-7=5 < 9 → breakdownTargetHeight()==0
	m.viewport.Height = m.chartHeight()
	m.breakdown = breakdownNone

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
	m.breakdown = breakdownNone

	// Arm a zoom, then press 'p' mid-flight.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if m.springKind != springKindZoom {
		t.Fatalf("setup: springKind=%d, want zoom", m.springKind)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	if m.springKind != springKindBreakdown || !m.springActive {
		t.Errorf("'p' during zoom: springKind=%d active=%v, want projects/true (zoom aborted, slide armed)", m.springKind, m.springActive)
	}
}

func TestZoomKey_AbortsInflightProjectsSlide(t *testing.T) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}}) // arm show slide
	m = updated.(Model)
	if m.springKind != springKindBreakdown {
		t.Fatalf("setup: springKind=%d, want projects", m.springKind)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springKind != springKindZoom || !m.springActive {
		t.Errorf("'z' during slide: springKind=%d active=%v, want zoom/true", m.springKind, m.springActive)
	}
	if m.breakdown == breakdownNone {
		t.Error("'z' during show-slide: breakdown=none, want projects (slide's committed terminal state)")
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
	m.breakdown = breakdownNone // hidden by default (#414) → first 'p' is a show
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
	if !m.springActive || m.springKind != springKindBreakdown {
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

// TestProjectsSlide_EndpointIdentity_LineMode mirrors the bar-mode headline
// test in remaining (line) mode: the per-frame build is the steady
// full-canvas buildLineChart at the lever height + the steady offset
// re-apply — closing round-one finding ccpulse-416.2 (the old frame skipped
// the steady path's windowing/offset semantics entirely).
func TestProjectsSlide_EndpointIdentity_LineMode(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 60, now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	m = assertSlideEndpoints(t, m, "show-line")
	m = assertSlideEndpoints(t, m, "hide-line")
	_ = m
}

// TestProjectsSlide_BoxContentPresentEarly guards the round-one "box rose
// empty" defect: as soon as the box band is a few rows tall it must carry
// the real top border, the title (height 3: shell around the title row, per
// the degenerate-heights contract), and one row later the top spender —
// renderBreakdownBox re-flowed at the animated height, not a blank-padded
// pre-render sliced bottom-first. Thresholds are 3/4 rather than 4/5 so the
// assertions stay non-vacuous with the content-aware target (#420): the
// single-project fixture's target is only 4 rows.
func TestProjectsSlide_BoxContentPresentEarly(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if len(m.breakdownRows) == 0 {
		t.Fatal("arm did not populate breakdownRows (show-path requery missing)")
	}
	topLabel := m.breakdownRows[0].Label

	sawTitle := false
	for i := 0; m.springActive && i < 600; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		frame := m.View()
		if lipgloss.Height(frame) != m.h {
			t.Fatalf("tick %d: frame height %d != terminal height %d", i, lipgloss.Height(frame), m.h)
		}
		if m.springActive && m.breakdownAnimH >= 3 {
			if !strings.Contains(frame, breakdownProjectsTitle) {
				t.Fatalf("animH=%d: frame lacks box title %q — box rendering empty", m.breakdownAnimH, breakdownProjectsTitle)
			}
			sawTitle = true
		}
		if m.springActive && m.breakdownAnimH >= 4 && !strings.Contains(frame, topLabel) {
			t.Fatalf("animH=%d: frame lacks top spender %q — content not re-flowed", m.breakdownAnimH, topLabel)
		}
	}
	if !sawTitle {
		t.Fatal("slide settled without ever sampling a frame at animH >= 3")
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
	m.breakdown = breakdownNone
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

// TestProjectsSlide_XLabelRowStable_LineMode is the remaining-mode (line chart)
// sibling of TestProjectsSlide_XLabelRowStable. It pins the windowed per-frame
// buildLineChart fidelity: every mid-flight frame rendered by the WINDOWED
// renderBreakdownFrame remaining branch (slicePointsInRange + viewport.Width +
// SetXOffset(0)) must keep the steady label row verbatim and must preserve the
// terminal frame height. The endpoint-identity test (TestProjectsSlide_EndpointIdentity_LineMode)
// sees only frame-0 and the settle frame — this test covers the frames in between.
//
// Threshold: buildLineChart emits the x-label row only when chartH >= 6
// (identical to bar mode); frames below that height have no label row to check.
func TestProjectsSlide_XLabelRowStable_LineMode(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 60, now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	labelRow := findXLabelRow(t, m.View())

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	for i := 0; m.springActive && i < 600; i++ {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		frame := m.View()
		if lipgloss.Height(frame) != m.h {
			t.Fatalf("tick %d: frame height %d != terminal height %d", i, lipgloss.Height(frame), m.h)
		}
		// buildLineChart emits the x-label row only when chartH >= 6; skip the
		// assertion for shorter frames where no label row is rendered.
		if m.springActive && m.chartHeight() >= 6 && !strings.Contains(frame, labelRow) {
			t.Fatalf("tick %d (chartH=%d): steady x-label row missing/altered mid-slide (windowed line build)\nwant line: %q", i, m.chartHeight(), labelRow)
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

// TestProjectsKey_RearmMidSlide_ReversesFromCurrentHeight: a second 'p'
// mid-slide reverses the motion from wherever the box currently is — no
// snap to an extreme first (re-flow rendering makes every intermediate
// height a valid start), no u/z-style hard-cut abort.
func TestProjectsKey_RearmMidSlide_ReversesFromCurrentHeight(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	genShow := m.springGen
	// Advance until animH is STRICTLY between the endpoints. The content-aware
	// target (#420) is only 4 rows for the single-project fixture, so a fixed
	// tick count is fragile: too few ticks round the lerp to 0, too many land
	// on the target. Driving on the observed height is robust to both the
	// spring constants and the fixture's target size.
	for i := 0; m.breakdownAnimH <= 0 || m.breakdownAnimH >= m.breakdownTargetHeight(); i++ {
		if i > 600 || !m.springActive {
			t.Fatalf("no strictly-mid-flight frame observed (tick %d, animH=%d, target=%d, active=%v)",
				i, m.breakdownAnimH, m.breakdownTargetHeight(), m.springActive)
		}
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	mid := m.breakdownAnimH

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("re-arm 'p': cmd=nil, want new tick loop")
	}
	if m.breakdown != breakdownNone {
		t.Error("re-arm 'p': breakdown=projects, want none (reversed to hide)")
	}
	if m.springGen == genShow {
		t.Error("re-arm 'p': springGen not bumped — stale show ticks would still apply")
	}
	if m.breakdownSlideFrom != mid || m.breakdownSlideTo != 0 {
		t.Errorf("re-arm from/to = (%d,%d), want (%d,0) — must reverse from current height",
			m.breakdownSlideFrom, m.breakdownSlideTo, mid)
	}
	if m.breakdownAnimH != mid {
		t.Errorf("re-arm animH=%d, want %d (frame 0 of the reversal = current frame)", m.breakdownAnimH, mid)
	}
}

// TestWindowSize_MidSlide_ViewportHeightSynced is the regression test for the
// handleWindowSize desync found in ccpulse-416.17: before the fix, viewport.Height
// was assigned from chartHeight() BEFORE refreshChart() aborted the in-flight
// projects spring. refreshChart sets springActive=false and springKind=None, which
// changes breakdownHeight() (and therefore chartHeight()), so the pre-abort
// assignment baked the mid-slide animated value into the viewport. Every frame
// until the next resize or 'p' press over- or under-filled the terminal by up to
// breakdownMaxRows rows. The fix moves the Height assignment to after refreshChart.
func TestWindowSize_MidSlide_ViewportHeightSynced(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	// Arm the show slide.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/projects", m.springActive, m.springKind)
	}

	// Advance 3-4 ticks so breakdownAnimH is mid-flight (never invoke the real
	// tea.Tick Cmd — it real-sleeps; drive via constructed springTickMsg).
	for range 4 {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("slide settled in 4 ticks; cannot probe mid-flight behaviour")
	}

	// Fire a resize mid-slide. handleWindowSize must abort the spring and then
	// re-assign viewport.Height from the post-abort chartHeight().
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	if got, want := m.viewport.Height, m.chartHeight(); got != want {
		t.Errorf("after mid-slide resize: viewport.Height=%d, want chartHeight()=%d (desynced)", got, want)
	}
	if got := lipgloss.Height(m.View()); got != m.h {
		t.Errorf("after mid-slide resize: View height=%d, want terminal height %d", got, m.h)
	}
}

// TestRefreshMsg_MidSlide_ViewportHeightSynced is the RefreshMsg sibling of
// TestWindowSize_MidSlide_ViewportHeightSynced. Before the fix, refreshChart's
// spring-abort block cleared springActive/springKind, which changed
// breakdownHeight() (and therefore chartHeight()), but nothing re-assigned
// m.viewport.Height — it kept the per-frame value renderBreakdownFrame had
// last written. Every subsequent View() would paint more or fewer rows than
// m.h until the next resize or 'p'. The fix adds m.viewport.Height =
// m.chartHeight() inside the abort block (same desync class, watcher-refresh
// abort path).
func TestRefreshMsg_MidSlide_ViewportHeightSynced(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	// Arm the show slide.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/projects", m.springActive, m.springKind)
	}

	// Advance 4 ticks so breakdownAnimH is mid-flight (never invoke the real
	// tea.Tick Cmd — it real-sleeps; drive via constructed springTickMsg).
	for range 4 {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("slide settled in 4 ticks; cannot probe mid-flight behaviour")
	}

	// Fire a RefreshMsg mid-slide. refreshChart must abort the spring and
	// re-assign viewport.Height from the post-abort chartHeight().
	updated, _ = m.Update(RefreshMsg{})
	m = updated.(Model)

	if m.springActive {
		t.Error("after RefreshMsg mid-slide: springActive=true, want false (slide aborted)")
	}
	if got, want := m.viewport.Height, m.chartHeight(); got != want {
		t.Errorf("after mid-slide RefreshMsg: viewport.Height=%d, want chartHeight()=%d (desynced)", got, want)
	}
	if got := lipgloss.Height(m.View()); got != m.h {
		t.Errorf("after mid-slide RefreshMsg: View height=%d, want terminal height %d", got, m.h)
	}
}

// TestNowTick_MidSlide_ViewportHeightSynced is the nowTickMsg sibling of
// TestWindowSize_MidSlide_ViewportHeightSynced. Before the fix, handleNowTick's
// animatingViewport guard excluded springKindBreakdown, so nowTickMsg called
// refreshChart during a projects slide; the abort block cleared springActive/Kind
// but left viewport.Height at the mid-slide per-frame value. The fix adds
// m.viewport.Height = m.chartHeight() inside the abort block (same desync class,
// live-advance abort path). Note: handleNowTick returns a non-nil Cmd to
// reschedule the next tick — never invoke it (it real-sleeps up to 1h); ignore.
func TestNowTick_MidSlide_ViewportHeightSynced(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	// Arm the show slide.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/projects", m.springActive, m.springKind)
	}

	// Advance 4 ticks so breakdownAnimH is mid-flight (never invoke the real
	// tea.Tick Cmd — it real-sleeps; drive via constructed springTickMsg).
	for range 4 {
		updated, _ = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if !m.springActive {
		t.Fatal("slide settled in 4 ticks; cannot probe mid-flight behaviour")
	}

	// Fire nowTickMsg mid-slide. handleNowTick calls refreshChart (not guarded
	// for springKindBreakdown), which must abort the spring and re-assign
	// viewport.Height from the post-abort chartHeight(). Ignore the returned
	// Cmd — it reschedules a real tea.Tick that would real-sleep.
	updated, _ = m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)

	if m.springActive {
		t.Error("after nowTickMsg mid-slide: springActive=true, want false (slide aborted)")
	}
	if got, want := m.viewport.Height, m.chartHeight(); got != want {
		t.Errorf("after mid-slide nowTickMsg: viewport.Height=%d, want chartHeight()=%d (desynced)", got, want)
	}
	if got := lipgloss.Height(m.View()); got != m.h {
		t.Errorf("after mid-slide nowTickMsg: View height=%d, want terminal height %d", got, m.h)
	}
}

// breakdownRowsBackingPtr returns the backing-array address of a breakdownRow
// slice, or 0 if empty. refreshBreakdown reassigns m.breakdownRows to a fresh slice
// from ProjectAggregates, so a changed pointer ⇒ a query ran. Used to prove the
// zero-DB-per-frame contract without a cache interface seam.
func breakdownRowsBackingPtr(a []breakdownRow) uintptr {
	if len(a) == 0 {
		return 0
	}
	return reflect.ValueOf(a).Pointer()
}

// TestProjectsSlide_RealFrame_BoundaryMovesMonotonically drives a real show
// slide tick-by-tick and asserts on the PAINTED BOUNDARY in View() output
// (withForcedColor → real ANSI), not on internal counters.
//
// Property: as the box grows (mid-flight, animH in (0, breakdownSlideTo)), the
// row index of the box's top border — the LAST "╭" row in the frame — must
// move UP monotonically (decreasing or equal row index). The header is also a
// rounded-bordered block, so the box's top border is always the last ╭ row;
// this is the same detection used by TestView_DuringSlide_HeightConservedRealBorder.
//
// When animH == 0 there is no box band, and the last ╭ row is the header's —
// a much smaller (higher) index. Monotonic tracking is gated on animH > 0 to
// avoid a spurious first sample against the header-only frame.
//
// Cheap riders: per-tick height conservation (Fatalf), and settle assertions
// (title present, animH == breakdownSlideTo).
func TestProjectsSlide_RealFrame_BoundaryMovesMonotonically(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.breakdown = breakdownNone
	m.refreshChart()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'p'}})
	m = updated.(Model)

	// lastRoundedTopRow returns the row index of the last "╭" line in frame, or
	// -1 if none. The box's top border is the last such row (the header's ╭
	// rows sit above it).
	lastRoundedTopRow := func(frame string) int {
		lines := strings.Split(frame, "\n")
		idx := -1
		for i, line := range lines {
			if strings.Contains(line, "╭") {
				idx = i
			}
		}
		return idx
	}

	prevBoxTopRow := -1 // -1 = not yet tracking (animH still 0)
	const maxTicks = 600
	for i := range maxTicks {
		frame := m.View()
		if h := lipgloss.Height(frame); h != m.h {
			t.Fatalf("tick %d: frame height=%d, want %d (conserved)", i, h, m.h)
		}
		band := m.breakdownAnimH
		if band > 0 && band < m.breakdownSlideTo { // mid-slide, box is present
			boxTopRow := lastRoundedTopRow(frame)
			if boxTopRow == -1 {
				t.Errorf("tick %d (animH=%d): no ╭ row found in frame (box top border missing)", i, band)
			} else if prevBoxTopRow != -1 && boxTopRow > prevBoxTopRow {
				// Box top should move UP (lower row index) as the slide grows.
				t.Errorf("tick %d: box top row moved DOWN: %d → %d (non-monotonic; box should rise)", i, prevBoxTopRow, boxTopRow)
			}
			prevBoxTopRow = boxTopRow
		}
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
	if !strings.Contains(final, breakdownProjectsTitle) {
		t.Error("settle frame missing the projects box title (full box not restored)")
	}
	if m.breakdownAnimH != m.breakdownSlideTo {
		t.Errorf("settle animH=%d, want target %d", m.breakdownAnimH, m.breakdownSlideTo)
	}
}

// TestBreakdownKind_ZeroValueIsHidden pins the enum ordering: the zero value
// must be "hidden" so a freshly constructed Model renders no box.
func TestBreakdownKind_ZeroValueIsHidden(t *testing.T) {
	var k breakdownKind
	if k != breakdownNone {
		t.Fatalf("zero breakdownKind = %v, want breakdownNone", k)
	}
	if breakdownProjects == breakdownModels {
		t.Fatal("breakdownProjects and breakdownModels must be distinct")
	}
}

func TestEffectiveKind(t *testing.T) {
	tests := []struct {
		name    string
		cur     breakdownKind
		pending breakdownKind
		want    breakdownKind
	}{
		{"no pending returns committed", breakdownProjects, breakdownNone, breakdownProjects},
		{"pending wins", breakdownNone, breakdownModels, breakdownModels},
		{"hidden and idle", breakdownNone, breakdownNone, breakdownNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := Model{breakdown: tt.cur, pendingBreakdown: tt.pending}
			if got := m.effectiveKind(); got != tt.want {
				t.Errorf("effectiveKind() = %v, want %v", got, tt.want)
			}
		})
	}
}
