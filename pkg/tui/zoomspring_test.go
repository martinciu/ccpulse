package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestLerpTime(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	b := a.Add(100 * time.Hour)
	cases := []struct {
		name string
		r    float64
		want time.Time
	}{
		{"r=0 returns a", 0.0, a},
		{"r=1 returns b", 1.0, b},
		{"r=0.5 midpoint", 0.5, a.Add(50 * time.Hour)},
		{"r<0 clamps to a", -0.5, a},
		{"r>1 clamps to b", 1.5, b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lerpTime(a, b, tc.r); !got.Equal(tc.want) {
				t.Errorf("lerpTime(a, b, %v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

func TestVisibleWindow_RemainingGeometry(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// One sample 12h in so setX clamps the offset to column 48 (the earliest
	// in-range bucket) — matches TestSetX_RemainingMode_ClampsAtInRangeLeftEdge.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h)
	m.setX(0) // clamp to col 48

	vf, vt := m.visibleWindow()

	// fullCanvasW = max(CanvasWidth(96 buckets @ 15m stride=1)=96, vpW=30)=96.
	// chartXOffset = viewportXOffset(48)*stride(1) = 48.
	// viewFrom = columnToTime(48, 96, from, from+24h) = from+12h.
	// viewTo   = columnToTime(48+30=78, 96, ...)      = from + 78/96*24h = +19.5h.
	wantFrom := from.Add(12 * time.Hour)
	wantTo := from.Add(time.Duration(float64(78) / float64(96) * float64(24*time.Hour)))
	if !vf.Equal(wantFrom) {
		t.Errorf("visibleWindow from = %v, want %v", vf, wantFrom)
	}
	if !vt.Equal(wantTo) {
		t.Errorf("visibleWindow to = %v, want %v", vt, wantTo)
	}
}

// seedRemainingModelWithSamples builds a remaining-mode model at the 15m zoom
// with `n` usage samples spaced 15m apart ending at `now`, then refreshes so
// lastPts5h / lastChart* / hasData reflect the seeded data.
func seedRemainingModelWithSamples(t *testing.T, n int, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	m, c := seedModelAt(t, int(chartUnitRemaining), 0, now)
	for i := range n {
		when := now.Add(-time.Duration(i) * 15 * time.Minute)
		resets := when.Add(2 * time.Hour)
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: 20.0 + float64(i)*2.0, ResetsAt: &resets},
		}
		if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m.refreshChart()
	return m, c
}

func TestZoomKey_Remaining_Squeezes_Arms(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 40, now)
	defer c.Close()
	if !m.hasData {
		t.Fatalf("seed sanity: hasData=false, want true (samples seeded)")
	}
	startZoom := m.zoomIdx
	startNowGen := m.nowGen

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if !m.springActive {
		t.Errorf("after 'z': springActive=false, want true (squeeze armed)")
	}
	if m.springKind != springKindZoom {
		t.Errorf("after 'z': springKind=%d, want springKindZoom(%d)", m.springKind, springKindZoom)
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("after 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	// The arm re-arms the live-advance now-tick on the NEW cadence (bumps nowGen)
	// so a zoom-in doesn't leave the live edge frozen on the old, coarser
	// boundary (#373).
	if m.nowGen != startNowGen+1 {
		t.Errorf("after 'z': nowGen=%d, want %d (now-tick re-armed on new cadence)", m.nowGen, startNowGen+1)
	}
	if cmd == nil {
		t.Fatalf("after 'z': cmd=nil, want a tea.Batch(springTick, now-tick re-arm)")
	}
	// The arm batches the spring tick with the now-tick re-arm. Invoking the
	// Batch Cmd yields a tea.BatchMsg (the child Cmds) WITHOUT running them, so
	// this is safe — we must NOT invoke the now-tick child (scheduleNowTick blocks
	// until the next bucket boundary). The spring tick's delivery is exercised by
	// the drive-loop tests via constructed springTickMsg.
	batch, ok := cmd().(tea.BatchMsg)
	if !ok {
		t.Fatalf("after 'z': cmd produced %T, want tea.BatchMsg", cmd())
	}
	if len(batch) != 2 {
		t.Errorf("after 'z': batch has %d cmds, want 2 (spring tick + now-tick re-arm)", len(batch))
	}
}

func TestZoomKey_Remaining_ReduceMotion_Snaps(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 40, now)
	defer c.Close()
	m.deps.ReduceMotion = true
	startZoom := m.zoomIdx

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("ReduceMotion 'z': springActive=true, want false (snap)")
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("ReduceMotion 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	// Snap path re-arms the live-advance now-tick, so cmd is non-nil. We must
	// NOT invoke cmd() to inspect its type: scheduleNowTick returns a tea.Tick
	// fired at the next bucket boundary (up to 1h/24h away), and invoking the
	// Cmd blocks for that real duration. springActive=false above already
	// proves no spring follow-up was armed (the only spring-tick scheduler is
	// the arm path, which sets springActive=true).
	if cmd == nil {
		t.Errorf("ReduceMotion 'z': cmd=nil, want now-tick re-arm")
	}
}

func TestZoomKey_BarMode_HardCut(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitTokens), 40, now)
	defer c.Close()
	startZoom := m.zoomIdx

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("bar-mode 'z': springActive=true, want false (hard-cut, deferred scope)")
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("bar-mode 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	// See ReduceMotion test: don't invoke the now-tick cmd (it blocks until the
	// next bucket boundary). springActive=false above proves the hard-cut.
	if cmd == nil {
		t.Errorf("bar-mode 'z': cmd=nil, want now-tick re-arm")
	}
}

func TestZoomKey_NoData_Snaps(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitRemaining), 0, now) // no usage samples
	defer c.Close()
	if m.hasData {
		t.Fatalf("seed sanity: hasData=true with no samples, want false")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("no-data 'z': springActive=true, want false (snap)")
	}
	// See ReduceMotion test: don't invoke the now-tick cmd (it blocks until the
	// next bucket boundary). springActive=false above proves the snap.
	if cmd == nil {
		t.Errorf("no-data 'z': cmd=nil, want now-tick re-arm")
	}
}

func TestZoomSpring_SettlesAndRestoresSteadyState(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 60, now)
	defer c.Close()

	nowGenBeforeArm := m.nowGen
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("arm sanity: springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	// The animated arm re-arms the now-tick on the NEW cadence (bumps nowGen
	// once) so a zoom-in doesn't strand the live edge on the old coarser
	// boundary. The springKindZoom guard in handleNowTick advance-suppresses any
	// mid-squeeze fire, and re-arming at arm (not settle) keeps every abort path
	// from orphaning the chain (#373, #311).
	nowGenAfterArm := m.nowGen
	if nowGenAfterArm != nowGenBeforeArm+1 {
		t.Errorf("arm: nowGen=%d, want %d (now-tick re-armed once on new cadence)", nowGenAfterArm, nowGenBeforeArm+1)
	}

	// Drive constructed springTickMsgs (the codebase pattern — never invoke the
	// tick Cmd, which would real-sleep 16.7ms/frame). 600 ticks (10s @ 60fps)
	// is generous; the critically-damped spring to r=1 settles well within that.
	const maxTicks = 600
	var lastCmd tea.Cmd
	for i := 0; i < maxTicks && m.springActive; i++ {
		updated, lastCmd = m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.springActive {
		t.Fatalf("zoom squeeze did not settle within %d ticks", maxTicks)
	}
	if m.springKind != springKindNone {
		t.Errorf("after settle: springKind=%d, want springKindNone(%d)", m.springKind, springKindNone)
	}
	// Settle stops the spring loop with no follow-up tick — idle TUI is
	// zero-animation-cost. The now-tick chain was already re-armed at arm, so
	// settle neither re-arms nor bumps nowGen again.
	if lastCmd != nil {
		t.Errorf("settle tick: cmd=%v, want nil (no follow-up; now-tick chain already re-armed at arm)", lastCmd)
	}
	if m.nowGen != nowGenAfterArm {
		t.Errorf("settle: nowGen=%d, want %d unchanged since arm (re-arm happens at arm, not settle)", m.nowGen, nowGenAfterArm)
	}
	// refreshChart ran at settle → steady-state full-canvas restored.
	if m.lastCanvasW == 0 {
		t.Errorf("after settle: lastCanvasW=0, want steady-state canvas restored")
	}
}

func TestZoomSpring_WindowLerpsTowardNew(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 60, now)
	defer c.Close()
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	snap := m.zoomSnap
	// One tick: r moves off 0, so the lerped viewFrom must sit between the old
	// and new window starts (when the window narrows; for a widening window the
	// direction inverts, so the bounds check is gated on oFrom.Before(nFrom)).
	updated, _ = m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)

	if m.zoomSpringR <= 0 {
		t.Errorf("after one tick: zoomSpringR=%v, want > 0", m.zoomSpringR)
	}
	gotFrom := lerpTime(snap.oFrom, snap.nFrom, m.zoomSpringR)
	if snap.oFrom.Before(snap.nFrom) {
		if gotFrom.Before(snap.oFrom) || gotFrom.After(snap.nFrom) {
			t.Errorf("lerped from %v outside [%v, %v]", gotFrom, snap.oFrom, snap.nFrom)
		}
	}
}

// armZoom drives a freshly-seeded remaining model (60 samples) through one 'z'
// press and returns the model mid-squeeze. Drive subsequent frames with
// m.Update(springTickMsg{gen: m.springGen}) — never invoke the tick Cmd.
func armZoom(t *testing.T, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	m, c := seedRemainingModelWithSamples(t, 60, now)
	// Model the settled-intro steady state. The open-path slide-in fires once
	// at startup (first WindowSizeMsg/RefreshMsg with data) and is long
	// settled by the time a user toggles to remaining mode and presses 'z'.
	// The seed helper calls refreshChart directly (bypassing maybeArmIntro),
	// so introPending lingers true; clearing it prevents a RefreshMsg/
	// WindowSizeMsg abort from re-arming the intro and masking the teardown.
	m.introPending = false
	m.quotaIntroPending = false
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("armZoom sanity: springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	return m, c
}

func TestZoomSpring_AbortedBySecondZoom(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()
	gen1 := m.springGen
	z1 := m.zoomIdx

	// Deliver one frame, then press 'z' again mid-squeeze.
	updated, _ := m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springGen == gen1 {
		t.Errorf("second 'z': springGen unchanged (%d) — stale tick not superseded", gen1)
	}
	if m.zoomIdx != (z1+1)%len(ZoomLevels) {
		t.Errorf("second 'z': zoomIdx=%d, want %d", m.zoomIdx, (z1+1)%len(ZoomLevels))
	}
	if !m.springActive || m.springKind != springKindZoom {
		t.Errorf("second 'z': springActive=%v springKind=%d, want active zoom", m.springActive, m.springKind)
	}
	// The first generation's still-pending tick must be dropped.
	_, staleCmd := m.Update(springTickMsg{gen: gen1})
	if staleCmd != nil {
		t.Errorf("stale gen-%d tick: cmd=%v, want nil (dropped)", gen1, staleCmd)
	}
}

func TestZoomSpring_AbortedByUnitKey(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)

	// 'u' from remaining wraps to cost (a bar mode). beginUnitAnimation →
	// refreshChart aborts the zoom; the unit toggle then takes over (or snaps
	// if its own guards bail). Either way springKind must no longer be zoom.
	if m.springKind == springKindZoom {
		t.Errorf("after 'u' mid-zoom: springKind still zoom, want torn down")
	}
}

func TestZoomSpring_AbortedByRefreshMsg(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()

	updated, _ := m.Update(RefreshMsg{})
	m = updated.(Model)

	if m.springActive || m.springKind != springKindNone {
		t.Errorf("after RefreshMsg mid-zoom: springActive=%v springKind=%d, want hard-cut", m.springActive, m.springKind)
	}
}

func TestZoomSpring_AbortedByWindowSize(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = updated.(Model)

	if m.springActive || m.springKind != springKindNone {
		t.Errorf("after WindowSizeMsg mid-zoom: springActive=%v springKind=%d, want hard-cut", m.springActive, m.springKind)
	}
}

func TestZoomSpring_NowTickSuppressedMidSqueeze(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()

	// A live-advance now-tick that fires mid-squeeze must NOT hard-cut the
	// squeeze (refreshChart would trip the abort block). It is advance-
	// suppressed but still reschedules, so the chain keeps rolling and resumes
	// advancing once the squeeze settles (#373).
	updated, cmd := m.Update(nowTickMsg{gen: m.nowGen})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("now-tick mid-squeeze tore down the squeeze: springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	if cmd == nil {
		t.Errorf("now-tick mid-squeeze: cmd=nil, want a reschedule keeping the chain alive")
	}
}

func TestZoomSpring_NowTickSurvivesAbort(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		abort tea.Msg
	}{
		{"RefreshMsg", RefreshMsg{}},
		{"WindowSizeMsg", tea.WindowSizeMsg{Width: 100, Height: 30}},
		{"UnitKey", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m, c := armZoom(t, now)
			defer c.Close()
			// armZoom already pressed 'z'. The arm re-armed the now-tick on the
			// new cadence and bumped nowGen, so a real now-tick carrying THIS gen
			// is in flight.
			armNowGen := m.nowGen

			updated, _ := m.Update(tc.abort)
			m = updated.(Model)

			// The squeeze must be torn down…
			if m.springKind == springKindZoom {
				t.Errorf("after %s mid-squeeze: springKind still zoom, want torn down", tc.name)
			}
			// …but the now-tick chain must NOT be orphaned. The arm re-armed it at
			// arm time (not deferred to settle), so an abort that skips settle
			// still leaves a live, correctly-scheduled tick — nowGen is unchanged
			// across the abort. Orphaning the chain here was the #373/#311
			// regression.
			if m.nowGen != armNowGen {
				t.Errorf("after %s: nowGen=%d, want %d (chain must stay at its scheduled gen)", tc.name, m.nowGen, armNowGen)
			}
			// The in-flight now-tick (gen == nowGen) still matches and drives the
			// live edge — the chain is alive, not dead.
			_, after := m.Update(nowTickMsg{gen: m.nowGen})
			if after == nil {
				t.Errorf("after %s: now-tick at gen %d dropped, want a live reschedule", tc.name, m.nowGen)
			}
		})
	}
}

func TestZoomSpring_ReArmsNowTickOnNewCadence(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 60, now)
	defer c.Close()
	gen0 := m.nowGen

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("arm sanity: springActive=%v springKind=%d", m.springActive, m.springKind)
	}
	// The animated arm re-arms the live-advance now-tick on the NEW zoom's
	// cadence (scheduleNowTick reads the already-advanced zoomIdx), bumping
	// nowGen. Mirrors the snap path's TestZoom_RearmsNowTick. Without the re-arm,
	// a zoom-in (e.g. the 24h→15m cycle wrap) leaves the chain on the old,
	// coarser boundary and freezes the live edge on an idle TUI (#373).
	if m.nowGen != gen0+1 {
		t.Errorf("animated zoom did not bump nowGen: %d → %d, want +1", gen0, m.nowGen)
	}
	// The pre-arm tick is now stale and must be dropped so chains can't stack.
	_, staleCmd := m.Update(nowTickMsg{gen: gen0})
	if staleCmd != nil {
		t.Error("pre-arm (stale-gen) nowTickMsg should be dropped after re-arm")
	}
}

func TestZoomSpring_SecondZoomStartsFromLivePosition(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()

	// Advance a few frames so the first squeeze is mid-lerp (r strictly in (0,1)).
	for range 5 {
		updated, _ := m.Update(springTickMsg{gen: m.springGen})
		m = updated.(Model)
	}
	if m.zoomSpringR <= 0 || m.zoomSpringR >= 1 {
		t.Fatalf("need mid-lerp r in (0,1), got %v", m.zoomSpringR)
	}
	snap1 := m.zoomSnap
	livePos := lerpTime(snap1.oFrom, snap1.nFrom, m.zoomSpringR)

	// Second 'z' mid-squeeze.
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("second 'z' sanity: springActive=%v springKind=%d", m.springActive, m.springKind)
	}

	// The new squeeze must START from the live lerp position of the aborted first
	// squeeze — not snap to the first squeeze's settled target (snap1.nFrom),
	// which is what visibleWindow() would return from the re-pinned lastChart*
	// geometry. A continuous hand-off, per the review-focus note's edge case
	// (#373).
	if !m.zoomSnap.oFrom.Equal(livePos) {
		t.Errorf("second 'z' oFrom = %v, want live lerp position %v", m.zoomSnap.oFrom, livePos)
	}
	if m.zoomSnap.oFrom.Equal(snap1.nFrom) {
		t.Errorf("second 'z' oFrom snapped to first squeeze's settled target %v (the bug)", snap1.nFrom)
	}
}

func TestZoomSpring_ViewRendersLineMidSqueeze(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := armZoom(t, now)
	defer c.Close()
	// Deliver one frame, then render.
	updated, _ := m.Update(springTickMsg{gen: m.springGen})
	m = updated.(Model)

	out := m.View()
	if out == "" {
		t.Fatalf("View() empty mid-squeeze")
	}
	// The y-tick overlay (overlayYTicks) renders the 100%/50%/0% ladder; its
	// presence confirms the springKindZoom View branch (full-fade y-ticks) ran
	// rather than the unit-toggle fade path (which would read empty springRatios
	// and fade to nothing).
	body := chartBodyLines(out)
	if len(body) == 0 {
		t.Fatalf("chart body empty mid-squeeze")
	}
}
