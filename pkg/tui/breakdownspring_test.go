package tui

import (
	"database/sql"
	"database/sql/driver"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	modernsqlite "modernc.org/sqlite"
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

// TestProjectsSpringTick_SettlesOnIntegerArrival (#477): the settle branch
// must fire on exactly the tick where lerpInt's integer output first reaches
// breakdownSlideTo — not ~30 ticks later when the continuous spring parameter
// crosses phaseTransitionThreshold. Invariant: the loop is never live with
// the height already at target.
func TestProjectsSpringTick_SettlesOnIntegerArrival(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	armProjectsShowForTest(t, &m)
	target := m.breakdownSlideTo
	if target == 0 {
		t.Fatal("fixture arm produced target 0; want a non-degenerate show slide")
	}

	const maxTicks = 600
	ticks := 0
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, _ := m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
		ticks++
		if m.springActive && m.breakdownAnimH == target {
			t.Fatalf("tick %d: height reached target %d but the loop is still live (settle lagging integer arrival)", ticks, target)
		}
	}
	if m.springActive {
		t.Fatalf("slide did not settle within %d ticks", maxTicks)
	}
	if m.breakdownAnimH != target {
		t.Errorf("settled at breakdownAnimH=%d, want %d", m.breakdownAnimH, target)
	}
	// The 0→4 fixture slide reached height 4 at ~tick 36 under the old
	// threshold and settled at 67; integer-arrival settle must land well
	// under that.
	if ticks > 45 {
		t.Errorf("settled in %d ticks, want ≤ 45 (integer arrival ≈ tick 36 for the 0→4 fixture)", ticks)
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

// assertSwapCompletesHardCut is the shared postcondition for the
// TestWindowSize_MidSwapAbort_CompletesSwap / TestRefreshMsg_MidSwapAbort_CompletesSwap /
// TestNowTick_MidSwapAbort_CompletesSwap / TestKeypress_MidSwapAbort_CompletesSwap
// quartet below (ccpulse-485). An abort mid leg-1 of a sequential swap must
// land the user on the panel they actually asked for — a hard cut straight to
// breakdownModels, exactly like every other refresh path hard-cuts an
// in-flight animation — not merely leave the model in a state recoverable by
// a further keypress. That weaker postcondition is what the pre-#485 tests
// checked, and it stayed true even when the swap was silently abandoned (the
// original #485 bug): the panel never showed up, but pendingBreakdown was
// still clear and a fresh press still armed a new slide, so the tests passed
// while the user's keypress vanished.
//
// Round two (ccpulse-475.40) found this helper's own postcondition was STILL
// too weak: breakdownHeight()!=0 is not an independent check once breakdown==
// breakdownModels is already asserted — breakdownTargetHeight() floors at 4
// even with zero rows (pkg/tui/viewport.go), so it follows mechanically from
// the kind assertion above and a tall-enough terminal. Worse, the box's
// TITLE is drawn from m.breakdownRowsKind (pkg/tui/model.go:887), not
// m.breakdown — so a mutant that hard-cuts m.breakdown to breakdownModels but
// leaves breakdownRowsKind/breakdownRows pointing at the stale projects data
// (or nils them out) passed every assertion here while the user saw the
// WRONG title, or no box at all. So this now asserts the panel the user sees,
// not just the field that gates whether a box is drawn at all: breakdownRowsKind,
// a nonempty breakdownRows, AND the painted frame carries the models title —
// on top of the original hard-cut and anti-stranding checks (pendingBreakdown
// == breakdownNone matters on its own, since a stranded pendingBreakdown
// would make every subsequent p/m press a no-op).
func assertSwapCompletesHardCut(t *testing.T, m *Model) {
	t.Helper()
	if m.pendingBreakdown != breakdownNone {
		t.Fatalf("pendingBreakdown=%d after abort, want breakdownNone (stranded — every subsequent p/m press would no-op)", m.pendingBreakdown)
	}
	if m.breakdown != breakdownModels {
		t.Fatalf("breakdown=%d after abort, want breakdownModels (swap must complete as a hard cut, not be abandoned — #485)", m.breakdown)
	}
	// Not "breakdownHeight()==0" — that follows mechanically from the check
	// above (breakdownTargetHeight() floors at 4 with zero rows) and would
	// pass even if the box were showing the OUTGOING kind's stale content.
	if m.breakdownRowsKind != breakdownModels {
		t.Fatalf("breakdownRowsKind=%d after abort, want breakdownModels (the box's title is drawn from this field, not m.breakdown — pkg/tui/model.go:887)", m.breakdownRowsKind)
	}
	if len(m.breakdownRows) == 0 {
		t.Fatal("breakdownRows empty after abort, want populated (models panel must actually have content, not just a committed kind)")
	}
	if !strings.Contains(m.View(), breakdownModelsTitle) {
		t.Error("View() after abort lacks the models title — models panel must actually be showing on screen, not just committed in state")
	}
}

// TestWindowSize_MidSwapAbort_CompletesSwap is the ccpulse-485 regression
// test (strengthened from the original ccpulse-475.1 pendingBreakdown-clear
// test — see assertSwapCompletesHardCut's doc comment for why the original
// assertions were too weak to catch #485). refreshChart's spring-abort block
// (pkg/tui/series.go) clears springActive/springIntro/springPhase/springKind
// and pendingBreakdown; before the #485 fix it dropped the queued leg 2
// destination along with the stale spring state. A sequential swap (#475)
// queues leg 2's destination in pendingBreakdown while leg 1 (hiding the
// current panel) is in flight; handleWindowSize calls refreshChart on every
// resize, so a resize mid-swap silently abandoned the swap — the outgoing
// panel slid away and the incoming one never appeared, even though
// pendingBreakdown ended up clear and the model looked outwardly recovered.
// The fix commits m.breakdown = m.pendingBreakdown before clearing it, so the
// abort hard-cuts straight to the queued destination — matching what a plain
// show already does when interrupted, since a plain show has already
// committed m.breakdown at arm time.
//
// Uses seedBarModelWithVariedModels (not seedBarModelWithMessages, ccpulse-
// 475.40): with the single-model fixture both the projects and models panels
// render at exactly 4 rows (the #420 empty-aggs floor), so a regression that
// hard-cuts m.breakdown to breakdownModels while leaving breakdownRowsKind /
// breakdownRows pointing at the stale projects data would still satisfy a
// bare height check. The varied fixture's models panel needs 6 rows,
// discriminating "the right kind's content is actually showing" from "some
// box of the right height is showing".
func TestWindowSize_MidSwapAbort_CompletesSwap(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithVariedModels(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()

	// Arm a swap: 'm' while projects is showing hides projects (leg 1) and
	// queues models as leg 2 in pendingBreakdown.
	if cmd := m.handleBreakdownKey(breakdownModels); cmd == nil {
		t.Fatal("setup: arming the swap returned a nil cmd")
	}
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/breakdown", m.springActive, m.springKind)
	}
	if m.pendingBreakdown != breakdownModels {
		t.Fatalf("setup: pendingBreakdown=%d, want breakdownModels (leg 2 queued)", m.pendingBreakdown)
	}

	// Advance into leg 1 (never invoke the real tea.Tick Cmd — it real-sleeps;
	// drive via direct handleBreakdownSpringTick calls).
	for range 3 {
		m.handleBreakdownSpringTick(m.springGen)
	}
	if !m.springActive {
		t.Fatal("leg 1 settled in 3 ticks; cannot probe mid-swap behaviour")
	}

	// Fire a resize mid-swap. handleWindowSize's refreshChart call must abort
	// leg 1 AND commit the queued leg 2 as a hard cut (#485).
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	mm := updated.(Model)
	assertSwapCompletesHardCut(t, &mm)
}

// TestRefreshMsg_MidSwapAbort_CompletesSwap is the RefreshMsg sibling of
// TestWindowSize_MidSwapAbort_CompletesSwap — same abandoned-swap bug
// (ccpulse-485), reached via the watcher-driven refresh path (handleRefresh
// fires on every debounced .jsonl write) instead of a resize. This is the
// path #485's real-binary capture actually exercised: a watcher RefreshMsg
// landing ~0.4s into leg 1 of a `p` then `m` swap left no breakdown box at
// all, while the plain-show control at the same timing rendered correctly.
//
// Uses seedBarModelWithVariedModels (not seedBarModelWithMessages, ccpulse-
// 475.40) — see TestWindowSize_MidSwapAbort_CompletesSwap's doc comment for
// why the single-model fixture cannot discriminate the two panels.
func TestRefreshMsg_MidSwapAbort_CompletesSwap(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithVariedModels(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()

	if cmd := m.handleBreakdownKey(breakdownModels); cmd == nil {
		t.Fatal("setup: arming the swap returned a nil cmd")
	}
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/breakdown", m.springActive, m.springKind)
	}
	if m.pendingBreakdown != breakdownModels {
		t.Fatalf("setup: pendingBreakdown=%d, want breakdownModels (leg 2 queued)", m.pendingBreakdown)
	}

	for range 3 {
		m.handleBreakdownSpringTick(m.springGen)
	}
	if !m.springActive {
		t.Fatal("leg 1 settled in 3 ticks; cannot probe mid-swap behaviour")
	}

	// Fire a RefreshMsg mid-swap. refreshChart's abort block must commit the
	// queued leg 2 (m.breakdown = m.pendingBreakdown) as a hard cut BEFORE
	// clearing pendingBreakdown (#485) — so the models panel actually shows,
	// instead of only ending up in a state a further keypress could recover.
	updated, _ := m.Update(RefreshMsg{})
	mm := updated.(Model)
	assertSwapCompletesHardCut(t, &mm)
}

// TestNowTick_MidSwapAbort_CompletesSwap is the nowTickMsg sibling of
// TestWindowSize_MidSwapAbort_CompletesSwap — same abandoned-swap bug
// (ccpulse-485), reached via the live-advance path (handleNowTick's
// animatingViewport guard excludes springKindBreakdown, so nowTickMsg still
// calls refreshChart during a projects/models slide). Note: handleNowTick
// returns a non-nil Cmd to reschedule the next tick — never invoke it (it
// real-sleeps up to 1h); ignore.
//
// Uses seedBarModelWithVariedModels (not seedBarModelWithMessages, ccpulse-
// 475.40) — see TestWindowSize_MidSwapAbort_CompletesSwap's doc comment for
// why the single-model fixture cannot discriminate the two panels.
func TestNowTick_MidSwapAbort_CompletesSwap(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithVariedModels(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()

	if cmd := m.handleBreakdownKey(breakdownModels); cmd == nil {
		t.Fatal("setup: arming the swap returned a nil cmd")
	}
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Fatalf("setup: springActive=%v springKind=%d, want true/breakdown", m.springActive, m.springKind)
	}
	if m.pendingBreakdown != breakdownModels {
		t.Fatalf("setup: pendingBreakdown=%d, want breakdownModels (leg 2 queued)", m.pendingBreakdown)
	}

	for range 3 {
		m.handleBreakdownSpringTick(m.springGen)
	}
	if !m.springActive {
		t.Fatal("leg 1 settled in 3 ticks; cannot probe mid-swap behaviour")
	}

	// Fire nowTickMsg mid-swap. Ignore the returned Cmd — it reschedules a
	// real tea.Tick that would real-sleep.
	updated, _ := m.Update(nowTickMsg{gen: m.nowGen})
	mm := updated.(Model)
	assertSwapCompletesHardCut(t, &mm)
}

// TestKeypress_MidSwapAbort_CompletesSwap is the keypress sibling of the
// resize/refresh/now-tick trio above (ccpulse-475.44). refreshChart's
// spring-abort block (pkg/tui/series.go) is reachable from five call sites;
// the trio above covers the three non-keypress ones (handleWindowSize,
// handleRefresh, handleNowTick). The remaining two are user keypresses that
// reach the SAME abort block through a different route:
//   - 'u' -> handleUnitKey -> beginUnitAnimation -> refreshChart (pkg/tui/springs.go:349)
//   - 'z' -> handleZoomKey -> refreshChart directly (pkg/tui/zoomspring.go:122)
//
// Both had zero coverage of the mid-swap-abort interaction despite #485
// changing user-visible behaviour on both paths: TestProjectsKey_AbortsInflightZoom
// and TestZoomKey_AbortsInflightProjectsSlide (above) both start from
// m.breakdown = breakdownNone, so neither ever has a queued leg 2 to abandon.
// Table-driven over the two trigger keys, reusing the same swap-arming setup
// and the strengthened assertSwapCompletesHardCut.
func TestKeypress_MidSwapAbort_CompletesSwap(t *testing.T) {
	for _, key := range []string{"u", "z"} {
		t.Run(key, func(t *testing.T) {
			now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
			m, c := seedBarModelWithVariedModels(t, now)
			defer c.Close()
			m.breakdown = breakdownProjects
			m.refreshBreakdown()

			// Arm a swap: 'm' while projects is showing hides projects (leg 1)
			// and queues models as leg 2 in pendingBreakdown.
			if cmd := m.handleBreakdownKey(breakdownModels); cmd == nil {
				t.Fatal("setup: arming the swap returned a nil cmd")
			}
			if !m.springActive || m.springKind != springKindBreakdown {
				t.Fatalf("setup: springActive=%v springKind=%d, want true/breakdown", m.springActive, m.springKind)
			}
			if m.pendingBreakdown != breakdownModels {
				t.Fatalf("setup: pendingBreakdown=%d, want breakdownModels (leg 2 queued)", m.pendingBreakdown)
			}

			// Advance into leg 1 (never invoke the real tea.Tick Cmd — it
			// real-sleeps; drive via direct handleBreakdownSpringTick calls).
			for range 3 {
				m.handleBreakdownSpringTick(m.springGen)
			}
			if !m.springActive {
				t.Fatal("leg 1 settled in 3 ticks; cannot probe mid-swap behaviour")
			}

			// Fire the keypress mid-swap. handleUnitKey (via beginUnitAnimation)
			// or handleZoomKey's own refreshChart call must abort leg 1 AND
			// commit the queued leg 2 as a hard cut (#485) — exactly like the
			// resize/refresh/now-tick paths above, even though this abort is
			// reached on the way to arming the key's OWN (unit/zoom) animation
			// rather than as the sole effect of the keypress.
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			mm := updated.(Model)
			assertSwapCompletesHardCut(t, &mm)
		})
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

// driveToSettle advances the breakdown spring with constructed ticks until the
// handler returns nil (settled) or the cap trips, and reports how many frames
// that took. Never invokes a returned Cmd — the now-tick Cmd real-sleeps to the
// next bucket boundary and would hang the test.
func driveToSettle(t *testing.T, m *Model) (frames int) {
	t.Helper()
	for range 600 { // 10s at 60fps — far beyond any real settle
		frames++
		if m.handleBreakdownSpringTick(m.springGen) == nil {
			return frames
		}
	}
	t.Fatal("breakdown spring did not settle within 600 frames")
	return frames
}

// TestBreakdownSwap_ChainsSecondLeg is the core of #475's sequential swap: leg
// one must settle into leg two rather than stopping.
//
// Uses seedBarModelWithVariedModels (not seedBarModelWithMessages) so the
// projects and models panels need DIFFERENT content-aware heights (#475.13):
// with a single-model fixture, leg 2's target is only ever asserted as ">0",
// which a bug that armed leg 2 against the OUTGOING kind's height (projects,
// also 4 rows) would pass just as easily as the correct implementation.
func TestBreakdownSwap_ChainsSecondLeg(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithVariedModels(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()
	projectsTarget := m.breakdownTargetHeight()
	if projectsTarget != 4 {
		t.Fatalf("setup: projects target = %d, want 4 (fixture's single project, #420 floor)", projectsTarget)
	}

	if cmd := m.handleBreakdownKey(breakdownModels); cmd == nil {
		t.Fatal("swap keypress returned nil Cmd, want an armed slide")
	}
	if m.pendingBreakdown != breakdownModels {
		t.Fatalf("pendingBreakdown = %v, want breakdownModels", m.pendingBreakdown)
	}
	if m.breakdownSlideTo != 0 {
		t.Errorf("leg 1 target = %d, want 0 (outgoing panel slides fully down)", m.breakdownSlideTo)
	}

	genBefore := m.springGen
	// Drive leg 1. The settle must NOT return nil — it chains leg 2.
	var chained tea.Cmd
	for range 600 {
		cmd := m.handleBreakdownSpringTick(m.springGen)
		if m.breakdown == breakdownModels {
			chained = cmd
			break
		}
		if cmd == nil {
			t.Fatal("leg 1 settled to nil without chaining leg 2")
		}
	}
	if chained == nil {
		t.Fatal("leg 1 settle returned nil Cmd, want leg 2's tick")
	}
	if m.pendingBreakdown != breakdownNone {
		t.Errorf("pendingBreakdown = %v after chaining, want breakdownNone (consumed)", m.pendingBreakdown)
	}
	if m.springGen == genBefore {
		t.Error("springGen not bumped by leg 2's arm — leg 2's own ticks would be dropped by the generation guard")
	}
	if !m.springActive || m.springKind != springKindBreakdown {
		t.Error("spring not active on breakdown kind after chaining")
	}
	if m.breakdownSlideFrom != 0 {
		t.Errorf("leg 2 start = %d, want 0", m.breakdownSlideFrom)
	}
	// leg 2's target must be the INCOMING kind's (models) content-aware
	// height: the fixture's 6 model rows at breakdownCellCols(122)=2 cols
	// need border(2)+title(1)+⌈6/2⌉=6 outer rows — strictly more than the
	// projects panel's 4, so a leg 2 armed against the OUTGOING kind's
	// height (a stale-height bug) fails this exact-value check instead of
	// slipping through a bare ">0".
	const wantModelsTarget = 6
	if m.breakdownSlideTo != wantModelsTarget {
		t.Errorf("leg 2 target = %d, want %d (models' content-aware height)", m.breakdownSlideTo, wantModelsTarget)
	}
	if m.breakdownSlideTo == projectsTarget {
		t.Error("leg 2 target equals the OUTGOING (projects) height — must be the incoming (models) height")
	}
	if m.viewport.Height != m.chartHeight() {
		t.Errorf("viewport.Height = %d, want chartHeight() = %d — leg-2 frame 0 must be painted synchronously",
			m.viewport.Height, m.chartHeight())
	}

	// Leg 2 settles normally.
	if frames := driveToSettle(t, &m); frames == 0 {
		t.Error("leg 2 produced no frames")
	}
	if m.breakdown != breakdownModels {
		t.Errorf("final kind = %v, want breakdownModels", m.breakdown)
	}
	if m.springActive {
		t.Error("spring still active after leg 2 settled — idle TUI must be zero-animation-cost")
	}
}

// TestBreakdownSwap_ArmedDuringZoomSpring is the regression test for
// ccpulse-475.25. handleBreakdownKey used to write m.pendingBreakdown =
// target and ONLY THEN call beginBreakdownAnimation. When a unit/zoom spring
// was in flight, beginBreakdownAnimation aborts it via m.refreshChart()
// (breakdownspring.go), and refreshChart clears pendingBreakdown (series.go)
// to drop a STRANDED pending left by one of the three Update-driven abort
// paths. (Since #485 it commits the destination into m.breakdown before
// clearing, but that does not rescue this path: beginBreakdownAnimation
// overwrites m.breakdown with its own `to` one statement later.) That
// cleared the just-written queue one
// statement after it was set: leg 1 (hiding the current panel) would settle
// with nothing to chain into, so pressing `m` (or `p`) while a `u`/`z` spring
// was still in flight (~1.1s window) made the visible panel slide away and
// never return. Covers both trigger keys.
func TestBreakdownSwap_ArmedDuringZoomSpring(t *testing.T) {
	for _, key := range []string{"u", "z"} {
		t.Run(key, func(t *testing.T) {
			now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
			m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
			defer c.Close()
			m.breakdown = breakdownProjects
			m.refreshBreakdown()
			m.viewport.Height = m.chartHeight()

			// Arm the unit/zoom spring: the projects panel is steady and
			// visible, only the chart's unit/zoom axis is animating.
			updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
			m = updated.(Model)
			if !m.springActive || m.springKind == springKindBreakdown {
				t.Fatalf("setup: springActive=%v springKind=%d, want a non-breakdown spring in flight", m.springActive, m.springKind)
			}

			// Press 'm' mid-flight: swap toward models. Under the bug this
			// silently drops the queued leg 2.
			updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
			m = updated.(Model)

			if frames := driveToSettle(t, &m); frames == 0 {
				t.Fatal("breakdown spring produced no frames")
			}
			if m.breakdown != breakdownModels {
				t.Fatalf("breakdown = %v after settle, want breakdownModels (panel must arrive, not vanish)", m.breakdown)
			}
			if m.breakdownHeight() == 0 {
				t.Error("breakdownHeight() = 0 after settle, want > 0 (models panel must be visible)")
			}
		})
	}
}

// TestBreakdownSwap_ReduceMotionSnaps: no legs, no ticks, no visible zero state.
func TestBreakdownSwap_ReduceMotionSnaps(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
	defer c.Close()
	m.deps.ReduceMotion = true
	m.breakdown = breakdownProjects
	m.refreshBreakdown()

	cmd := m.handleBreakdownKey(breakdownModels)
	if cmd != nil {
		t.Error("reduce_motion swap returned a Cmd, want nil (no ticks)")
	}
	if m.breakdown != breakdownModels {
		t.Errorf("kind = %v, want breakdownModels immediately", m.breakdown)
	}
	if m.pendingBreakdown != breakdownNone {
		t.Errorf("pendingBreakdown = %v, want breakdownNone (no queued leg)", m.pendingBreakdown)
	}
	if m.springActive {
		t.Error("spring active under reduce_motion")
	}
}

// TestBreakdownKey_MidAnimation covers the effectiveKind() resolution table:
// during leg 1 a keypress only rewrites the destination and must NOT re-arm;
// during leg 2 (pendingBreakdown already consumed) a keypress DOES re-arm,
// behaving as an ordinary reverse-from-current-height re-arm — the same
// contract TestProjectsKey_RearmMidSlide_ReversesFromCurrentHeight pins
// outside a swap.
func TestBreakdownKey_MidAnimation(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)

	t.Run("leg 1: pressing the pending panel's key cancels the swap", func(t *testing.T) {
		m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
		defer c.Close()
		m.breakdown = breakdownProjects
		m.refreshBreakdown()
		m.handleBreakdownKey(breakdownModels) // swap armed, pending = models
		m.handleBreakdownSpringTick(m.springGen)

		genBefore, breakdownBefore := m.springGen, m.breakdown
		m.handleBreakdownKey(breakdownModels) // effectiveKind == models → hide

		if m.pendingBreakdown != breakdownNone {
			t.Errorf("pendingBreakdown = %v, want breakdownNone (swap cancelled)", m.pendingBreakdown)
		}
		// breakdownSlideTo is deliberately NOT asserted here: leg 1's target
		// is always 0, whether this press is correctly guarded (no-op) or a
		// bug re-armed a "hide" (also targets 0) — it cannot distinguish the
		// two. springGen is the load-bearing check: beginBreakdownAnimation
		// always bumps it, guarded or not.
		if m.springGen != genBefore {
			t.Error("re-armed during leg 1; a mid-leg-1 press must only rewrite the destination")
		}
		if m.breakdown != breakdownBefore {
			t.Errorf("breakdown changed mid-leg-1: %v → %v, want unchanged (committed only at arm)", breakdownBefore, m.breakdown)
		}
	})

	t.Run("leg 1: pressing the other key rewrites the destination", func(t *testing.T) {
		m, c := seedBarModelWithMessages(t, int(chartUnitCost), now)
		defer c.Close()
		m.breakdown = breakdownProjects
		m.refreshBreakdown()
		m.handleBreakdownKey(breakdownModels)
		m.handleBreakdownSpringTick(m.springGen)

		genBefore, breakdownBefore := m.springGen, m.breakdown
		m.handleBreakdownKey(breakdownProjects) // effectiveKind == models → swap to projects

		if m.pendingBreakdown != breakdownProjects {
			t.Errorf("pendingBreakdown = %v, want breakdownProjects", m.pendingBreakdown)
		}
		if m.springGen != genBefore {
			t.Error("re-armed during leg 1; only the destination should change")
		}
		if m.breakdown != breakdownBefore {
			t.Errorf("breakdown changed mid-leg-1: %v → %v, want unchanged (committed only at arm)", breakdownBefore, m.breakdown)
		}
	})

	t.Run("leg 2: keypress re-arms as a normal reverse-from-current-height", func(t *testing.T) {
		// seedBarModelWithVariedModels (not seedBarModelWithMessages, #475.28):
		// with the single-model fixture, projects target == models target ==
		// 4, so leg 2's target can't be distinguished from the outgoing
		// panel's height. The exact-value check below closes that gap.
		m, c := seedBarModelWithVariedModels(t, now)
		defer c.Close()
		m.breakdown = breakdownProjects
		m.refreshBreakdown()
		if projectsTarget := m.breakdownTargetHeight(); projectsTarget != 4 {
			t.Fatalf("setup: projects target = %d, want 4 (fixture's single project, #420 floor)", projectsTarget)
		}
		m.handleBreakdownKey(breakdownModels) // arm the swap: hide projects, queue models

		// Drive leg 1 to settle and chain into leg 2 (mirrors
		// TestBreakdownSwap_ChainsSecondLeg's drive loop).
		for i := 0; ; i++ {
			if i > 600 {
				t.Fatal("leg 1 did not chain into leg 2 within 600 ticks")
			}
			cmd := m.handleBreakdownSpringTick(m.springGen)
			if m.breakdown == breakdownModels {
				break
			}
			if cmd == nil {
				t.Fatal("leg 1 settled without chaining leg 2")
			}
		}
		if m.pendingBreakdown != breakdownNone {
			t.Fatalf("setup: pendingBreakdown=%v after chaining, want breakdownNone (consumed)", m.pendingBreakdown)
		}
		// leg 2's ARMED slide target (breakdownSlideTo — what the spring
		// actually animates toward) must be the INCOMING kind's (models)
		// content-aware height, not the OUTGOING (projects) panel's 4 rows —
		// the same exact-value guard TestBreakdownSwap_ChainsSecondLeg pins.
		// breakdownTargetHeight() is deliberately NOT asserted here: it is a
		// pure recomputation from current content and would still report the
		// correct value even if a bug corrupted breakdownSlideTo itself.
		const wantModelsTarget = 6
		if got := m.breakdownSlideTo; got != wantModelsTarget {
			t.Fatalf("leg 2 armed target (breakdownSlideTo) = %d, want %d (models' content-aware height)", got, wantModelsTarget)
		}

		// Advance further until leg 2 is strictly mid-flight (mirrors
		// TestProjectsKey_RearmMidSlide_ReversesFromCurrentHeight).
		for i := 0; m.breakdownAnimH <= 0 || m.breakdownAnimH >= m.breakdownTargetHeight(); i++ {
			if i > 600 || !m.springActive {
				t.Fatalf("no strictly-mid-flight leg-2 frame observed (tick %d, animH=%d, target=%d, active=%v)",
					i, m.breakdownAnimH, m.breakdownTargetHeight(), m.springActive)
			}
			m.handleBreakdownSpringTick(m.springGen)
		}
		mid := m.breakdownAnimH
		genBefore := m.springGen

		// Pending is already consumed, so this press must NOT be swallowed by
		// handleBreakdownKey's pendingBreakdown-!=-none guard — it must
		// re-arm, reversing from the current leg-2 height exactly as a plain
		// (non-swap) mid-slide re-arm does.
		cmd := m.handleBreakdownKey(breakdownModels) // effectiveKind == models (pending consumed) → hide

		if cmd == nil {
			t.Fatal("leg-2 re-arm: cmd=nil, want a new tick loop")
		}
		if m.breakdown != breakdownNone {
			t.Errorf("leg-2 re-arm: breakdown=%v, want breakdownNone (reversed to hide)", m.breakdown)
		}
		if m.pendingBreakdown != breakdownNone {
			t.Errorf("leg-2 re-arm: pendingBreakdown=%v, want breakdownNone (no further swap queued)", m.pendingBreakdown)
		}
		if m.springGen == genBefore {
			t.Error("leg-2 re-arm: springGen not bumped — stale leg-2 ticks would still apply")
		}
		if m.breakdownSlideFrom != mid || m.breakdownSlideTo != 0 {
			t.Errorf("leg-2 re-arm from/to = (%d,%d), want (%d,0) — must reverse from the current leg-2 height",
				m.breakdownSlideFrom, m.breakdownSlideTo, mid)
		}
		if m.breakdownAnimH != mid {
			t.Errorf("leg-2 re-arm animH=%d, want %d (frame 0 of the reversal = current frame)", m.breakdownAnimH, mid)
		}
	})
}

// TestBreakdownSwap_HeightConservedAcrossBothLegs extends the #416 invariant
// over the swap: View must emit exactly m.h rows at every animated height of
// BOTH legs, including the handoff frame.
//
// Uses seedBarModelWithVariedModels (not seedBarModelWithMessages, #475.28)
// so the projects and models panels need DIFFERENT content-aware heights:
// with the single-model fixture, projects target == models target == 4, and
// a bug that armed leg 2 against the OUTGOING kind's height instead of the
// incoming one would still satisfy a bare per-frame height-conservation
// check. The final-height assertion below pins the exact incoming value.
func TestBreakdownSwap_HeightConservedAcrossBothLegs(t *testing.T) {
	withForcedColor(t)
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c := seedBarModelWithVariedModels(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown()
	if projectsTarget := m.breakdownTargetHeight(); projectsTarget != 4 {
		t.Fatalf("setup: projects target = %d, want 4 (fixture's single project, #420 floor)", projectsTarget)
	}
	// Production's re-sync order (same as handleWindowSize / view_fit_test):
	// kind and rows first, viewport.Height = chartHeight() last. Without it the
	// pre-swap frame is already over-tall and every assertion below is bogus.
	m.viewport.Height = m.chartHeight()
	m.renderWindow()

	if got := strings.Count(m.View(), "\n") + 1; got != m.h {
		t.Fatalf("setup: steady projects-up View() rows = %d, want %d", got, m.h)
	}
	m.handleBreakdownKey(breakdownModels)

	for i := range 600 {
		got := strings.Count(m.View(), "\n") + 1
		if got != m.h {
			t.Fatalf("frame %d: View() rows = %d, want %d (kind=%v animH=%d)",
				i, got, m.h, m.breakdown, m.breakdownAnimH)
		}
		if m.handleBreakdownSpringTick(m.springGen) == nil {
			break
		}
	}

	if m.springActive {
		t.Fatal("swap did not settle within 600 frames")
	}
	if m.breakdown != breakdownModels {
		t.Fatalf("final kind = %v, want breakdownModels", m.breakdown)
	}
	// leg 2's settled height must be the INCOMING kind's (models)
	// content-aware height — strictly more than the outgoing (projects)
	// panel's 4 rows — so a leg 2 armed against the outgoing kind's height
	// fails this exact-value check instead of slipping through the bare
	// per-frame conservation loop above.
	const wantModelsTarget = 6
	if m.breakdownAnimH != wantModelsTarget {
		t.Errorf("final breakdownAnimH = %d, want %d (models' content-aware height)", m.breakdownAnimH, wantModelsTarget)
	}
}

// breakdownQueryCounter wraps modernc.org/sqlite's driver so
// TestBreakdownSwap_QueryCount can count exactly how many times a query
// matching `match` reached the database — a real counter, not the
// backing-pointer-changed proxy breakdownRowsBackingPtr provides elsewhere in
// this file. That proxy only samples BETWEEN whole handleBreakdownSpringTick
// calls (so two refreshBreakdown calls landing inside a single tick would
// register as one) and the uintptr it compares does not keep the slice's
// backing array reachable, so the allocator could in principle hand a later,
// genuinely distinct allocation the same address (#475.24).
type breakdownQueryCounter struct {
	inner driver.Driver
	n     atomic.Int64
	match func(query string) bool
}

func (d *breakdownQueryCounter) Open(name string) (driver.Conn, error) {
	c, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return &breakdownCountingConn{Conn: c, counter: d}, nil
}

// breakdownCountingConn wraps the real connection by embedding the
// driver.Conn INTERFACE (not the concrete modernc.org/sqlite conn struct) so
// Prepare/Close/Begin delegate to it unchanged while only Query is
// intercepted. Measured directly against modernc.org/sqlite's conn (opened
// via modernsqlite.Driver{}.Open): it implements the full modern optional-
// interface set — QueryerContext, Queryer, ExecerContext, Execer,
// ConnBeginTx, ConnPrepareContext, Pinger, SessionResetter, and Validator.
// Embedding the bare driver.Conn interface type hides ALL of those from
// database/sql's runtime type assertions (a struct embedding only reveals
// the interface's declared method set, not the concrete type's), so
// database/sql falls back to the deprecated driver.Queryer path for every
// query. That forced fallback — not any limitation of the underlying driver
// — is the actual choke point this type intercepts.
//
// Consequences of forcing that fallback, worth knowing before reusing this
// pattern elsewhere:
//   - ctx is NOT propagated into the driver for queries on this fixture's
//     DB: driver.Queryer.Query takes no context.Context, and QueryerContext
//     is never reached because it's hidden.
//   - ConnBeginTx is hidden too, so BeginTx degrades to Begin() and would
//     error on any non-default sql.TxOptions.
//   - Every Exec on this connection also goes through the legacy
//     Prepare/Exec/Close path rather than ExecerContext/Execer.
//   - The counted path is therefore NOT the path production code takes
//     (*sql.DB.QueryContext → driver.QueryerContext) — it's a legacy
//     fallback this wrapper forces on purpose to get a single, simple
//     interception point for the test.
type breakdownCountingConn struct {
	driver.Conn
	counter *breakdownQueryCounter
}

func (c *breakdownCountingConn) Query(query string, args []driver.Value) (driver.Rows, error) {
	if c.counter.match(query) {
		c.counter.n.Add(1)
	}
	//nolint:staticcheck // SA1019: driver.Queryer is deprecated, but it's the
	// exact interface database/sql falls back to once embedding driver.Conn
	// hides the modern Queryer/QueryerContext set (see the type doc above).
	// This call reaches the fallback deliberately, not accidentally.
	return c.Conn.(driver.Queryer).Query(query, args)
}

// breakdownQueryCounterDriverSeq generates deterministic, collision-free
// names for sql.Register calls made by newBreakdownSwapFixtureWithQueryCounter
// (#475.33). sql.Register PANICS the whole test binary on a duplicate name;
// a wall-clock-derived name (time.Now().UnixNano()) is not actually
// guaranteed unique — a backwards NTP step, or any future t.Parallel()/
// -count change landing two calls in the same nanosecond, would panic
// instead of failing one test. A package-level atomic counter is
// deterministic and costs nothing.
var breakdownQueryCounterDriverSeq atomic.Int64

// breakdownQueryCounterFixturePragmas mirrors cachePragmas (pkg/cache/cache.go)
// verbatim (#475.34). cachePragmas itself is unexported and unreachable from
// pkg/tui, so it can't be imported directly; this fixture restates its exact
// value rather than a subset, per cachePragmas' own comment warning that
// connections started with driver defaults (un-tuned busy_timeout=0,
// synchronous=FULL, no WAL, no temp_store tuning) behave differently from
// production connections.
const breakdownQueryCounterFixturePragmas = "_pragma=busy_timeout(5000)" +
	"&_pragma=journal_mode(wal)" +
	"&_pragma=synchronous(normal)" +
	"&_pragma=temp_store(memory)"

// newBreakdownSwapFixtureWithQueryCounter builds the same single-model swap
// fixture as seedBarModelWithMessages, backed by a *cache.Cache whose
// underlying *sql.DB is wrapped in a breakdownQueryCounter matching
// ModelAggregates' query text: "GROUP BY model" is unique to it (neither
// ProjectAggregates' "GROUP BY repo_root" nor the chart bucket queries'
// "GROUP BY bucket_epoch"/"GROUP BY day" share that clause), so the counter
// tracks exactly the queries refreshBreakdown issues on the models kind.
// Pre-warms the schema through a throwaway cache.Open (the real "sqlite"
// driver, unwrapped) so the counting driver only ever sees the queries this
// test cares about.
func newBreakdownSwapFixtureWithQueryCounter(t *testing.T, now time.Time) (Model, *cache.Cache, *breakdownQueryCounter) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	warm, err := cache.Open(t.Context(), path)
	if err != nil {
		t.Fatalf("prewarm cache.Open: %v", err)
	}
	if err := warm.Close(); err != nil {
		t.Fatalf("prewarm close: %v", err)
	}

	drvName := fmt.Sprintf("sqlite-querycount-%d", breakdownQueryCounterDriverSeq.Add(1))
	counter := &breakdownQueryCounter{
		inner: &modernsqlite.Driver{},
		match: func(q string) bool { return strings.Contains(q, "GROUP BY model") },
	}
	sql.Register(drvName, counter)

	db, err := sql.Open(drvName, path+"?"+breakdownQueryCounterFixturePragmas)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	c := cache.NewFromDB(db)

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	var msgs []parse.Message
	for i := range 60 {
		msgs = append(msgs, parse.Message{
			SessionID:   "s",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(-time.Duration(i) * 15 * time.Minute),
			InputTokens: 5000,
		})
	}
	if err := c.InsertMessages(t.Context(), msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}

	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitCost)
	m.zoomIdx = 0 // 15m
	m.w, m.h = 122, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.now = func() time.Time { return now }
	m.introPending = false
	m.quotaIntroPending = false
	m.refreshChart()
	return m, c, counter
}

// TestBreakdownSwap_QueryCount pins the per-frame DB contract across a whole
// swap: two queries total (leg 2's arm and leg 2's settle). Leg 1 targets
// breakdownNone so it queries nothing, and its chained settle skips
// refreshChart. Counts real ModelAggregates invocations via
// breakdownQueryCounter rather than sampling the breakdownRows backing
// pointer between ticks (#475.24 — see that type's doc comment for why the
// pointer proxy cannot make "want 2" exact).
func TestBreakdownSwap_QueryCount(t *testing.T) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	m, c, counter := newBreakdownSwapFixtureWithQueryCounter(t, now)
	defer c.Close()
	m.breakdown = breakdownProjects
	m.refreshBreakdown() // ProjectAggregates — "GROUP BY repo_root" must not match

	if n := counter.n.Load(); n != 0 {
		t.Fatalf("setup: query counter = %d after the projects setup query, want 0", n)
	}

	m.handleBreakdownKey(breakdownModels)
	for range 1200 {
		if m.handleBreakdownSpringTick(m.springGen) == nil {
			break
		}
	}
	if got := counter.n.Load(); got != 2 {
		t.Errorf("ModelAggregates queries across the swap = %d, want 2 (leg-2 arm + leg-2 settle)", got)
	}
}
