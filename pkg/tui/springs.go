package tui

import (
	"math"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/harmonica"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// springPhase tracks which leg of the two-phase unit-toggle animation
// is currently running. Idle is the steady state; springActive=false
// implies springPhase=springIdle. See issue #136.
type springPhase int

const (
	springIdle      springPhase = iota
	springShrinking             // Phase 1: bars fall to zero (Projectile, ease-in)
	springHolding               // Hold: bars rest at zero so the eye registers the beat (#163)
	springGrowing               // Phase 2: bars grow from zero to target (Spring with Vi, ease-out)
)

// handleSpringTick dispatches one unit-toggle / intro animation tick to the
// active phase, dropping stale or superseded-generation ticks (#218).
func (m *Model) handleSpringTick(msg springTickMsg) tea.Cmd {
	if !m.springActive || msg.gen != m.springGen {
		// springActive=false → no animation in flight at all.
		// gen mismatch → tick belongs to a superseded animation
		// generation (rapid 'u' press during animation). Either way,
		// drop without rescheduling so the loop doesn't perpetuate.
		return nil
	}
	if m.springKind == springKindZoom {
		return m.handleZoomSpringTick(m.springGen)
	}
	gen := m.springGen
	switch m.springPhase {
	case springShrinking:
		return m.advanceSpringShrinking(gen)
	case springHolding:
		return m.advanceSpringHolding(gen)
	case springGrowing:
		return m.advanceSpringGrowing(gen)
	}
	return nil
}

// advanceSpringShrinking runs one Phase-1 (Projectile fall) tick. gen is the
// animation generation captured by handleSpringTick.
func (m *Model) advanceSpringShrinking(gen int) tea.Cmd {
	var maxR float64
	for i := range m.springRatios {
		pos := m.springProjectiles[i].Update()
		// Defensive clamp — early-exit beats us to it under
		// well-tuned per-bar gravity, but Projectile keeps
		// accelerating past zero if we let it.
		pos.X = max(pos.X, 0)
		m.springRatios[i] = pos.X
		maxR = max(maxR, pos.X)
	}
	if maxR < phaseTransitionThreshold {
		// Phase 1 → Hold handoff: snap ratios to zero, render the
		// all-zero frame once, schedule a one-shot tick at
		// phaseHoldDuration. Phase 2 state (springTargetRatios,
		// springVelocities) is seeded in the springHolding case
		// when the hold tick arrives — not here (#163).
		for i := range m.springRatios {
			m.springRatios[i] = 0
		}
		m.springPhase = springHolding
		m.renderSpringFrame()
		return tea.Tick(phaseHoldDuration, func(time.Time) tea.Msg {
			return springTickMsg{gen: gen}
		})
	}
	m.renderSpringFrame()
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// advanceSpringHolding seeds Phase 2 targets/velocities on the hold tick and
// switches to springGrowing (#163, quota seeding #192).
func (m *Model) advanceSpringHolding(gen int) tea.Cmd {
	// Hold tick arrived: seed Phase 2 targets and initial
	// velocities, switch to springGrowing, resume FPS ticking.
	// Ratios remain at zero (already snapped in the Phase 1
	// threshold-cross). The first springGrowing tick will move
	// them off zero (#163).
	for i := range m.springRatios {
		m.springTargetRatios[i] = m.springFinalTargets[i]
		m.springVelocities[i] = phase2InitialVelocityV0 * m.springFinalTargets[i]
	}
	// Quota-bar Phase 2 seeding (#192). quotaTarget5h/7d were
	// snapshotted at arm in beginIntroAnimation; mirror the
	// bucket V_i = V0 * target_i contract.
	m.quotaVel5h = phase2InitialVelocityV0 * m.quotaTarget5h
	m.quotaVel7d = phase2InitialVelocityV0 * m.quotaTarget7d
	m.springPhase = springGrowing
	m.renderSpringFrame()
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// advanceSpringGrowing runs one Phase-2 (Spring grow) tick across bucket and
// quota springs; settles when every surface is within threshold (#192).
func (m *Model) advanceSpringGrowing(gen int) tea.Cmd {
	var maxGap float64
	for i := range m.springRatios {
		r, v := m.springs[i].Update(m.springRatios[i],
			m.springVelocities[i], m.springTargetRatios[i])
		m.springRatios[i] = r
		m.springVelocities[i] = v
		gap := math.Abs(m.springTargetRatios[i] - r)
		maxGap = max(maxGap, gap)
	}
	// Quota-bar Phase 2 advance (#192). Two scalar Update calls
	// per tick; their gaps fold into the same maxGap so the
	// existing settle check fires when ALL three surfaces are
	// within threshold of their targets.
	r5, v5 := m.quotaSpring5h.Update(
		m.quotaRatio5h, m.quotaVel5h, m.quotaTarget5h,
	)
	m.quotaRatio5h, m.quotaVel5h = r5, v5
	maxGap = max(maxGap, math.Abs(m.quotaTarget5h-r5))

	r7, v7 := m.quotaSpring7d.Update(
		m.quotaRatio7d, m.quotaVel7d, m.quotaTarget7d,
	)
	m.quotaRatio7d, m.quotaVel7d = r7, v7
	maxGap = max(maxGap, math.Abs(m.quotaTarget7d-r7))

	if maxGap < phaseTransitionThreshold {
		copy(m.springRatios, m.springTargetRatios)
		m.springActive = false
		m.springIntro = false
		m.springPhase = springIdle
		// Reset springKind too: this settle sets springActive=false BEFORE
		// refreshChart, so refreshChart's abort reset (guarded by springActive)
		// is skipped here. Without this the tag would linger as springKindUnit
		// after the animation ends, violating the "springActive ⇒ springKind
		// reliable" invariant the zoom dispatch relies on (#373).
		m.springKind = springKindNone
		m.refreshChart()
		return nil
	}
	m.renderSpringFrame()
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// kickLateArrivalQuotaIntro starts a quota-only slide-in when the Anthropic
// poller delivered usage after the chart intro already settled (#192). Returns
// nil when both targets are zero (nothing to animate).
func (m *Model) kickLateArrivalQuotaIntro() tea.Cmd {
	target5h := float64(m.window.Percent) / 100.0
	var target7d float64
	if m.window.Has7d {
		target7d = float64(m.window.Percent7d) / 100.0
	}
	if target5h <= 0 && target7d <= 0 {
		// Targets both zero — nothing to animate. Clear the flag
		// so we don't keep checking on every QuotaMsg.
		m.quotaIntroPending = false
		return nil
	}
	m.quotaTarget5h = target5h
	m.quotaTarget7d = target7d
	m.quotaRatio5h = 0
	m.quotaRatio7d = 0
	m.quotaVel5h = phase2InitialVelocityV0 * target5h
	m.quotaVel7d = phase2InitialVelocityV0 * target7d
	m.quotaSpring5h = harmonica.NewSpring(
		harmonica.FPS(springFPS),
		phase2Frequency, phase2Damping,
	)
	m.quotaSpring7d = harmonica.NewSpring(
		harmonica.FPS(springFPS),
		phase2Frequency, phase2Damping,
	)
	m.quotaIntroPending = false
	// Zero residual chart velocities from the prior settle
	// so reusing the springGrowing arm for the quota-only
	// late-arrival intro doesn't wobble the chart bars.
	// Position is already at-target (copy in the settle
	// block), velocity was never reset.
	clear(m.springVelocities)
	m.springActive = true
	m.springIntro = true
	m.springPhase = springGrowing
	// Same as beginIntroAnimation: keep springKind reliable while springActive
	// (this late-arrival quota intro reuses the two-phase grow machinery) (#373).
	m.springKind = springKindUnit
	m.springGen++
	gen := m.springGen
	return tea.Tick(time.Second/time.Duration(springFPS), func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// quotaIntroRatio returns the fill ratio a quota bar should render
// this frame:
//   - target during steady state (springIntro == false): pass-through
//     so quotaBars() reads m.window.Percent / 100.0 unchanged.
//   - 0 during the hold beat (springPhase == springHolding): the bar
//     rests at zero so the eye registers the beat before the grow.
//   - the per-side spring ratio during the grow (springPhase ==
//     springGrowing): the bar interpolates from 0 to its target via
//     the same harmonica config as the chart intro.
//
// Callers route 5h through quotaSide5h and 7d through quotaSide7d so
// the helper picks the right (ratio, velocity, target) triple.
func (m Model) quotaIntroRatio(side quotaSide, target float64) float64 {
	if !m.springIntro {
		return target
	}
	switch m.springPhase {
	case springHolding:
		return 0
	case springGrowing:
		switch side {
		case quotaSide5h:
			return m.quotaRatio5h
		case quotaSide7d:
			return m.quotaRatio7d
		}
	}
	return target
}

// springFPS is the harmonica tick rate for the unit-toggle animation.
// 60 leaves a 16.7ms per-frame budget. Task 10 (the bench-gate commit)
// validates the choice against BenchmarkBarChartRender.
const springFPS = 60

// springFrequency, springDamping are the harmonica parameters for the
// per-bar spring. Frequency 6 Hz controls the speed of approach;
// damping 1.0 is critically damped — the spring reaches the target
// as fast as possible WITHOUT overshooting. No bounce, no oscillation,
// just a tight monotonic ease into the new ratios.
const (
	springFrequency = 6.0
	springDamping   = 1.0
)

// Phase durations and tuning for the two-phase animation (#136).
//
// phase1Duration is the wall-clock target for Phase 1 (Projectile fall).
// Per-bar gravity is tuned at beginUnitAnimation so each bar reaches
// zero at exactly t = phase1Duration regardless of starting height.
//
// phase2Frequency, phase2Damping mirror the existing springFrequency
// and springDamping for Phase 2's spring. Critical damping (1.0) +
// initial velocity below omega guarantees monotonic ease-out without
// overshoot.
//
// phase2InitialVelocityV0 is V₀ in Vi[i] = V₀ · springFinalTargets[i].
// Picked below omega=6 so the spring approaches the target monotonically.
//
// phaseTransitionThreshold is the symmetric early-exit cutoff: Phase 1
// hands off when max(springRatios) < threshold; Phase 2 settles when
// max(springTargetRatios − springRatios) < threshold. 0.01 is below
// single-cell visual quantisation.
const (
	phase1Duration           = 350 * time.Millisecond
	phase2Frequency          = springFrequency // 6.0
	phase2Damping            = springDamping   // 1.0
	phase2InitialVelocityV0  = 5.0
	phaseTransitionThreshold = 0.01
	// phaseHoldDuration is the all-zero pause between Phase 1 (fall) and
	// Phase 2 (grow) in the 'u' unit-toggle animation. Long enough to
	// read the unit change, short enough to feel snappy (#163).
	phaseHoldDuration = 150 * time.Millisecond
)

// beginUnitAnimation primes the two-phase unit-toggle animation. It
// snapshots the OLD state (oldPeak, oldUnitIdx, oldValues from
// m.lastValues), runs refreshChart so the viewport content reflects
// the NEW unit, then builds:
//   - springRatios[i] from the OLD ratios (current visible heights).
//   - springFinalTargets[i] from the NEW ratios (Phase 2 destination).
//   - springProjectiles[i] with per-bar tuned gravity so bar i lands
//     at zero at t = phase1Duration regardless of its starting ratio.
//   - springs[i] (Phase 2 spring) configured; springVelocities seeded
//     at the phase transition, not here.
//   - springPhase = springShrinking, springActive = true.
//
// Caller must have already incremented m.unitIdx before calling.
// oldUnitIdx is derived by inverting the toggle since this is a
// 3-cycle (cost → tokens → remaining → cost; see chartUnit iota).
//
// Snapshots happen BEFORE refreshChart so the OLD m.peak / m.lastValues
// survive the refresh that overwrites them.

// seedPhase2Springs sizes the spring state arrays to len(targets), zeros
// springRatios / springVelocities / springTargetRatios, copies targets
// into springFinalTargets (the Phase 2 destination), and builds
// springs[i] with phase2Frequency / phase2Damping.
//
// Used by both beginIntroAnimation (intro = Phase 2 only, preceded by
// hold) and beginUnitAnimation (Phase 1 fall + Phase 2 grow); the
// latter overwrites springRatios with the OLD heights and seeds
// springProjectiles after this call. Sharing this helper enforces the
// "same Phase 2 spring config" requirement from #188 by construction.
//
// springTargetRatios is left zeroed; the springHolding tick is what
// seeds it from springFinalTargets at the Phase 2 entry — same
// contract as the unit-toggle path.
func (m *Model) seedPhase2Springs(targets []float64) {
	n := len(targets)
	m.springs = make([]harmonica.Spring, n)
	m.springProjectiles = make([]harmonica.Projectile, n)
	m.springRatios = make([]float64, n)
	m.springVelocities = make([]float64, n)
	m.springTargetRatios = make([]float64, n)
	m.springFinalTargets = make([]float64, n)
	for i, t := range targets {
		m.springFinalTargets[i] = t
		m.springs[i] = harmonica.NewSpring(harmonica.FPS(springFPS), phase2Frequency, phase2Damping)
	}
}

func (m *Model) beginUnitAnimation() {
	if m.deps.Cache == nil {
		return
	}

	m.oldPeak = m.peak
	m.oldUnitIdx = (m.unitIdx + int(chartUnitCount) - 1) % int(chartUnitCount)
	m.oldIsLine = isLineMode(chartUnit(m.oldUnitIdx))
	m.newIsLine = isLineMode(chartUnit(m.unitIdx))
	// Snapshot OLD-unit data before refreshChart overwrites lastValues/
	// lastStarts/lastPts5h/lastPts7d. The bar branch of renderSpringFrame
	// needs oldValues/oldStarts during springShrinking when oldIsLine=false;
	// the line branch needs oldPts5h/7d during springShrinking when
	// oldIsLine=true.
	m.oldValues = m.lastValues
	m.oldStarts = m.lastStarts
	m.oldPts5h = m.lastPts5h
	m.oldPts7d = m.lastPts7d

	m.refreshChart()
	newValues := m.lastValues
	newPeak := m.peak

	// No animation while warming up: since #300 an empty cache yields a
	// zero-padded axis (newValues non-empty), so gate on hasData, not length.
	if len(newValues) == 0 || !m.hasData {
		m.springActive = false
		m.springPhase = springIdle
		return
	}

	// Size spring arrays to max(old, new) so cross-mode transitions
	// (bar↔line) don't bail out in renderSpringFrame's bar branch when
	// the user's scroll position is past the smaller side's length.
	n := max(len(m.oldValues), len(newValues))

	// Phase 2 targets sized to n; entries past len(newValues) stay 0
	// (invisible bars on the long side of a bar↔line cross-transition).
	targets := make([]float64, n)
	for i := range n {
		if m.newIsLine {
			targets[i] = 1.0
		} else if newPeak > 0 && i < len(newValues) {
			// Clamp to 1.0 so an off-screen bucket taller than the visible
			// peak can't inflate the target past 1.0 and drag out the spring
			// settle window — see beginIntroAnimation for the full rationale.
			targets[i] = min(1.0, newValues[i]/niceCeilingFloat(newPeak))
		}
	}
	m.seedPhase2Springs(targets)

	// Layer Phase 1 setup on top of the Phase-2-seeded arrays:
	//   - Overwrite springRatios[i] with the OLD heights (Phase 1 start).
	//   - Seed springProjectiles[i] with per-bar tuned gravity so bar i
	//     lands at zero at t = phase1Duration regardless of its starting
	//     ratio. h = 0.5·g·t² ⇒ g = 2h/t².
	t1 := phase1Duration.Seconds()
	for i := range n {
		if m.oldIsLine {
			m.springRatios[i] = 1.0
		} else if m.oldPeak > 0 && i < len(m.oldValues) {
			m.springRatios[i] = m.oldValues[i] / niceCeilingFloat(m.oldPeak)
		}

		g := 2 * m.springRatios[i] / (t1 * t1)
		// Stored by value; Phase 1 tick MUST index (m.springProjectiles[i].Update()),
		// never range-copy. Projectile.Update has a pointer receiver and mutates
		// state; a range-copy loop would freeze Phase 1 silently.
		m.springProjectiles[i] = *harmonica.NewProjectile(
			harmonica.FPS(springFPS),
			harmonica.Point{X: m.springRatios[i]},
			harmonica.Vector{},      // v0 = 0 (at rest)
			harmonica.Vector{X: -g}, // accel toward zero
		)
	}
	m.springActive = true
	m.springPhase = springShrinking
	m.springKind = springKindUnit
	m.springGen++
}

// beginIntroAnimation primes the open-path slide-in. Caller must have
// already called refreshChart so m.lastValues / m.peak reflect the
// current cache contents. The animation re-uses Phase 2 of the unit-
// toggle state machine: springs are seeded with target ratios but
// springRatios stay at zero until the springHolding tick fires after
// phaseHoldDuration. See #188.
//
// No-op if lastValues is empty or peak is non-positive (defensive —
// maybeArmIntro should have gated those cases already).
//
// Snapshots m.newIsLine for the View()/renderSpringFrame branches that
// check it in the springHolding/springGrowing arms. m.oldIsLine /
// m.oldValues / m.oldPeak are left at their zero values; the intro
// never enters springShrinking, so the OLD-state fields are unread.
func (m *Model) beginIntroAnimation() {
	if len(m.lastValues) == 0 || m.peak <= 0 {
		return
	}

	// Clamp to 1.0: m.peak is the VISIBLE-slice peak (#230), so a bucket
	// taller than the visible peak — i.e. an off-screen outlier — would
	// otherwise yield a target far above 1.0. Such a bar renders identically
	// either way (the spring frame's fixed max is 1.0, so ntcharts clips it),
	// but an un-clamped target still gates the maxGap settle check: the
	// animation can't end until the inflated off-screen spring converges to
	// within 0.01 of e.g. 9.99, so the settle window scales with the outlier
	// magnitude (measured ~2s for a 3× outlier, worse for larger ones).
	// Clamping bounds it to the normal full-height settle regardless of how
	// tall the off-screen bars are.
	targets := make([]float64, len(m.lastValues))
	for i, v := range m.lastValues {
		targets[i] = min(1.0, v/niceCeilingFloat(m.peak))
	}

	m.seedPhase2Springs(targets)

	// Quota-bar spring seeding (#192). Targets are snapshotted from
	// the current window so a mid-intro recomputeWindow can't shift
	// the visual destination. When Has7d=false the 7d target stays at
	// 0 (defensively zeroed) — quotaBars() short-circuits to the
	// (no data) placeholder for that side regardless of intro phase,
	// so the spring ratio is unread; this just keeps state consistent.
	m.quotaTarget5h = float64(m.window.Percent) / 100.0
	m.quotaRatio5h = 0
	m.quotaVel5h = 0
	m.quotaSpring5h = harmonica.NewSpring(
		harmonica.FPS(springFPS),
		phase2Frequency, phase2Damping,
	)
	if m.window.Has7d {
		m.quotaTarget7d = float64(m.window.Percent7d) / 100.0
	} else {
		m.quotaTarget7d = 0
	}
	m.quotaRatio7d = 0
	m.quotaVel7d = 0
	m.quotaSpring7d = harmonica.NewSpring(
		harmonica.FPS(springFPS),
		phase2Frequency, phase2Damping,
	)
	// If quota is already loaded, the targets snapshotted above are
	// real values and the springs will animate to them. If quota is
	// still nil (the common startup race — async Anthropic poller
	// hasn't finished), targets are 0; the QuotaMsg handler will
	// either re-snapshot in flight or kick a quota-only late-arrival
	// intro after settle.
	if m.quota != nil {
		m.quotaIntroPending = false
	}

	// Spring window tracks current viewport position so the animated
	// slice matches what the user is about to look at. On open the
	// shadow offset is at the right edge (pinned by refreshChart's
	// post-rebuild restore); preserve it.
	m.springXOffset = m.viewportXOffset

	// renderSpringFrame's default arm reads m.newIsLine; pin it for the
	// intro (always bar mode at open since default unit is tokens).
	m.newIsLine = isLineMode(chartUnit(m.unitIdx))

	m.springActive = true
	m.springIntro = true
	m.springPhase = springHolding
	// Tag the in-flight machine so springKind is never springKindNone while
	// springActive: the intro rides the two-phase (unit) spring machinery, so
	// it routes through the springPhase switch, not the zoom dispatch (#373).
	m.springKind = springKindUnit
	m.springGen++

	// Render the zero-bars hold frame synchronously so the next View()
	// call doesn't briefly show refreshChart's fully-formed bars before
	// the first tick paints over the viewport with the empty hold frame.
	m.renderSpringFrame()
}

// maybeArmIntro fires the open-path slide-in when introPending is true
// and the most recent refreshChart produced non-empty data. Called
// from WindowSizeMsg and RefreshMsg handlers right after refreshChart.
// Returns tea.Tick(phaseHoldDuration, ...) when the intro arms, nil
// otherwise.
//
// Always clears introPending on the first non-empty refresh, whether
// the intro actually arms (motion path) or is a no-op (reduce_motion
// is already gated upstream via introPending init in New()). This
// ensures the intro is strictly one-shot.
//
// When the cache starts empty: hasData stays false through the early
// refreshes (since #300 the empty cache renders a zero-padded axis, so
// lastValues is non-empty — hasData is the warming-up signal); introPending
// stays true; the first refresh with real data is what arms the intro.
// See #188 spec / acceptance criteria.
func (m *Model) maybeArmIntro() tea.Cmd {
	if !m.introPending {
		return nil
	}
	// Wait for the first WindowSizeMsg before arming: in production the
	// startup-time p.Send(RefreshMsg{}) from cmd/ccpulse/main.go:329 can
	// race ahead of bubbletea's initial WindowSizeMsg. Arming with m.w=0
	// produces a zero-size spring frame that the subsequent WindowSizeMsg
	// would tear down via refreshChart's spring-abort block. Defer the
	// arm until we have a real viewport. introPending stays true.
	if m.w == 0 {
		return nil
	}
	if !m.hasData {
		return nil
	}
	m.introPending = false
	m.beginIntroAnimation()
	if !m.springActive {
		return nil
	}
	gen := m.springGen
	return tea.Tick(phaseHoldDuration, func(time.Time) tea.Msg {
		return springTickMsg{gen: gen}
	})
}

// renderSpringFrame rebuilds the viewport content from the visible
// window of spring ratios. Pass 1.0 as the chart's max value because
// the ratios already live in [0, 1]; ntcharts then renders each bar
// at the right proportional height.
//
// PERF: full-canvas rebuilds at chartW > 1000 buckets exceed the 60fps
// per-frame budget (see BenchmarkBarChartRender). The spring runs over
// ALL ratios so every bar settles correctly, but only the chartWidth()
// window starting at m.springXOffset is rendered each tick. Off-screen
// bars are invisible anyway; their final positions are committed on
// settle via the steady-state refreshChart call.
//
// Decides line-vs-bar based on m.springPhase and delegates to
// renderSpringLineFrame or renderSpringBarFrame; each helper paints
// the viewport and owns its own X-offset handling.
func (m *Model) renderSpringFrame() {
	if len(m.springRatios) == 0 {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	chartH := m.chartHeight()

	// Determine whether the current frame renders as a line chart (remaining
	// mode). Exit phase shows the OLD chart type; enter phase shows the NEW.
	renderAsLine := false
	switch m.springPhase {
	case springShrinking:
		renderAsLine = m.oldIsLine
	default:
		renderAsLine = m.newIsLine
	}

	if renderAsLine {
		m.renderSpringLineFrame(zoom, chartH)
		return
	}
	m.renderSpringBarFrame(zoom, chartH)
}

// visibleWindow returns the [from, to] wall-clock window currently mapped to
// the viewport at the active zoom and scroll offset. It is the single source
// of truth for "what time range is on screen", shared by renderSpringLineFrame
// (the u-toggle line frame), the zoom squeeze's arm-time snapshot (#373), and
// refreshProjects' remaining-mode window (#430).
//
// Reads m.lastChartFrom/To, m.lastCanvasW (via the recomputed fullCanvasW),
// m.viewportXOffset, and m.viewport.Width — all consistent with ZoomLevels[
// m.zoomIdx] at any call site that hasn't mutated zoomIdx without a refresh.
func (m *Model) visibleWindow() (from, to time.Time) {
	zoom := ZoomLevels[m.zoomIdx]
	fullFrom, fullTo := m.lastChartFrom, m.lastChartTo
	if fullFrom.IsZero() {
		fullFrom = m.now().Add(-5 * time.Hour)
	}
	if fullTo.IsZero() {
		fullTo = m.now()
	}
	fullCanvasW := max(zoom.CanvasWidth(bucketCountInRange(fullFrom, fullTo, zoom.Duration)), m.viewport.Width)
	vpW := m.viewport.Width
	chartXOffset := m.viewportXOffset * zoom.stride()
	if maxOff := fullCanvasW - vpW; chartXOffset > maxOff {
		chartXOffset = maxOff
	}
	from = columnToTime(chartXOffset, fullCanvasW, fullFrom, fullTo)
	to = columnToTime(chartXOffset+vpW, fullCanvasW, fullFrom, fullTo)
	return from, to
}

// renderSpringLineFrame renders one frame of the line-chart (remaining-mode)
// spring transition: it interpolates the windowed utilization points toward
// the flat 100%-headroom line via the springRatios envelope and paints the
// viewport. Split out of renderSpringFrame (#336). See renderSpringFrame for
// the shape-fraction convention and the #180 viewport-windowing rationale.
func (m *Model) renderSpringLineFrame(zoom ZoomLevel, chartH int) {
	// Shape-fraction convention: springRatios[i] is a uniform scalar in
	// [0,1] where 1.0 = full real shape and 0 = flat line at 100% headroom.
	// Both exit and enter use the SAME ratio direction (springShrinking:
	// 1→0, springGrowing: 0→1). The visual direction is produced by
	// interpPt below, which maps the scalar onto the displayed value via
	// displayed = 1.0 + (target-1.0)*maxR — so maxR=0 renders flat 100%
	// and maxR=1 renders the real shape. This is why exit (line
	// collapses upward to 100%) and enter (line drops from 100% to real
	// shape) both use ratios approaching 1.0 → 0 and 0 → 1.0 respectively
	// without needing separate direction flags.
	// maxR is the global envelope; all ratios move together.
	var maxR float64
	for _, r := range m.springRatios {
		maxR = max(maxR, r)
	}

	// Select which pts to interpolate: old data during exit, new during enter.
	pts5h, pts7d := m.lastPts5h, m.lastPts7d
	if m.springPhase == springShrinking && m.oldIsLine {
		pts5h, pts7d = m.oldPts5h, m.oldPts7d
	}

	// PERF (#180): window the line chart to the visible viewport via the
	// shared visibleWindow() helper (#373). Full-canvas rebuild at
	// canvasW=2880 blows the 60fps frame budget (~93ms per real-binary
	// probe). The windowed render at canvasW=viewport.Width is pixel-
	// identical inside the visible region because timeserieslinechart's
	// WithTimeRange maps time→col linearly — so the settle transition to
	// refreshChart's full canvas doesn't visibly snap.
	vpW := m.viewport.Width
	viewFrom, viewTo := m.visibleWindow()

	slicedPts5h := slicePointsInRange(pts5h, viewFrom, viewTo)
	slicedPts7d := slicePointsInRange(pts7d, viewFrom, viewTo)

	interpPt := func(p cache.UtilizationPoint) cache.UtilizationPoint {
		target := max(0, 1.0-p.Pct/100.0)
		// displayed ∈ [1.0, target]: 1.0 (flat) when maxR=0, target (real) when maxR=1.
		displayed := 1.0 + (target-1.0)*maxR
		return cache.UtilizationPoint{At: p.At, Pct: (1.0 - displayed) * 100.0}
	}

	interp5h := make([]cache.UtilizationPoint, len(slicedPts5h))
	for i, p := range slicedPts5h {
		interp5h[i] = interpPt(p)
	}
	interp7d := make([]cache.UtilizationPoint, len(slicedPts7d))
	for i, p := range slicedPts7d {
		interp7d[i] = interpPt(p)
	}

	m.viewport.SetContent(buildLineChart(interp5h, interp7d, viewFrom, viewTo, vpW, chartH, time.Now(), zoom, m.dateOrder, "spring", ""))
	m.viewport.SetXOffset(0)
}

// renderSpringBarFrame renders one frame of the bar-chart spring transition:
// it slices the visible springRatios window (flush-right via
// computeSpringSlice), colors it by the active phase's unit, and paints the
// viewport. Split out of renderSpringFrame (#336). Steady-state twin:
// renderWindow.
func (m *Model) renderSpringBarFrame(zoom ZoomLevel, chartH int) {
	nv := m.visibleBuckets()

	// Pick the starts that align with the springRatios for the current
	// phase. Phase 1 of a bar→line transition needs the OLD bar starts
	// (lastStarts has been overwritten with sparse line points by
	// refreshChart); every other case uses the post-refresh lastStarts.
	var rangeStarts []time.Time
	if m.springPhase == springShrinking && !m.oldIsLine {
		rangeStarts = m.oldStarts
	} else {
		rangeStarts = m.lastStarts
	}

	// Clamp the window to the smaller of springRatios and rangeStarts so
	// the slice indices stay valid for both arrays. With springs sized to
	// max(old, new) and rangeStarts chosen to match the active phase, the
	// two slices line up 1:1 in normal flow; the clamp is a safety net.
	start := max(m.springXOffset, 0)
	end := min(start+nv, len(m.springRatios), len(rangeStarts))
	if start >= end {
		return
	}

	// Flush-right (#306): the steady-state render (renderWindow) uses the
	// same helper with the same (start, vpWidth, stride, gap), so the
	// spring frame reproduces the identical viewport framing — no jump on
	// the steady-state ↔ spring transition. Include a partial leading
	// bucket and offset by stride-slack so the right edge stays flush.
	stride := zoom.stride()
	sliceStart, springXOff := computeSpringSlice(start, m.viewport.Width, stride, max(zoom.BarGap, 0))

	visibleRatios := m.springRatios[sliceStart:end]
	visibleStarts := rangeStarts[sliceStart:end]
	// During the shrinking phase the bars are still showing OLD-unit data
	// falling toward zero, so render them in the OLD unit's color. Only
	// after the handoff (springGrowing onward) does the color switch to
	// the new unit. Mirrors the labelUnit logic in Model.View().
	frameUnit := chartUnit(m.unitIdx)
	if m.springPhase == springShrinking {
		frameUnit = chartUnit(m.oldUnitIdx)
	}
	m.viewport.SetContent(buildChart(visibleRatios, visibleStarts, 1.0,
		zoom.CanvasWidth(len(visibleRatios)), chartH, time.Now(), zoom, frameUnit, m.dateOrder))
	m.viewport.SetXOffset(springXOff)
}
