package tui

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/exp/teatest"
	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// setupTestModel builds a fresh Model with empty Deps and wraps it in a
// teatest.TestModel at a fixed 120x40 terminal. The fixed size keeps
// layout-dependent assertions deterministic across CI runners.
//
// Registers a t.Cleanup that calls tm.Quit() so the bubbletea program
// goroutine is reaped even if a t.Fatal fires before the test reaches
// its own WaitFinished call — otherwise goleak would surface the
// orphaned program goroutine instead of the underlying assertion error.
func setupTestModel(t *testing.T) *teatest.TestModel {
	t.Helper()
	m := New(Deps{})
	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })
	return tm
}

// TestProgram_QuitPropagation verifies that sending 'q' shuts the
// program down cleanly within the timeout. This is the harness smoke
// test: if this fails, every other integration test will fail too.
func TestProgram_QuitPropagation(t *testing.T) {
	tm := setupTestModel(t)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))
}

// TestProgram_HelpOverlayRender verifies that pressing '?' toggles the
// help overlay through a real program loop (not just the unit-level
// Update check in TestHelpToggle). Asserts on m.showHelp from FinalModel
// — the rendered footer text "scroll" appears in every frame regardless
// of the overlay (ShortHelp also lists ScrollLeft/ScrollRight), so the
// model state is the only precise signal.
func TestProgram_HelpOverlayRender(t *testing.T) {
	tm := setupTestModel(t)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))

	final := tm.FinalModel(t, teatest.WithFinalTimeout(1*time.Second))
	m, ok := final.(Model)
	if !ok {
		t.Fatalf("FinalModel: expected tui.Model, got %T", final)
	}
	if !m.showHelp {
		t.Errorf("expected showHelp=true after '?' keypress, got false")
	}
}

// TestProgram_MultiStepInteraction drives the program through a
// sequence: resize -> refresh -> zoom (z) -> scroll right -> quit.
// Verifies state holds across the message sequence end to end.
//
// Zoom note: ZoomLevels = [15m, 1h, 24h], initial zoomIdx = 0 (15m).
// One 'z' press cycles to index 1 (label "1h").
//
// Substitution note: the spec suggests asserting on the "1h" zoom label
// in View output, but ZoomLevels[m.zoomIdx].Label flows only into
// slog.Debug calls — it is never rendered into View(). WaitFor therefore
// uses the footer keybinding "z zoom" (always present once the terminal
// has a valid size) to confirm a frame was rendered after the zoom press.
// The final state assertion uses FinalModel to inspect zoomIdx directly,
// which is the most precise way to verify zoom state propagated end to end.
func TestProgram_MultiStepInteraction(t *testing.T) {
	tm := setupTestModel(t)

	// Resize different from the initial 120x40 to exercise the handler.
	tm.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
	tm.Send(RefreshMsg{})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})

	// Sync gate: wait for a frame after the zoom keypress before
	// scrolling, so the test exercises post-zoom state, not pre-zoom.
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool { return bytes.Contains(bts, []byte("z zoom")) },
		teatest.WithDuration(500*time.Millisecond),
	)

	tm.Send(tea.KeyMsg{Type: tea.KeyRight})
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))

	final := tm.FinalModel(t, teatest.WithFinalTimeout(1*time.Second))
	m, ok := final.(Model)
	if !ok {
		t.Fatalf("FinalModel: expected tui.Model, got %T", final)
	}
	if m.zoomIdx != 1 {
		t.Errorf("expected zoomIdx=1 (1h) after one 'z' press, got %d", m.zoomIdx)
	}
}

// newSeededCache opens a fresh Cache at t.TempDir() and inserts a
// small set of assistant messages so IOTokenBuckets returns non-empty
// data. Returns the Cache for inclusion in Deps; the cleanup hook
// closes the DB at test end.
func newSeededCache(t *testing.T) *cache.Cache {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	c, err := cache.Open(path)
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}

	now := time.Now()
	msgs := []parse.Message{
		{
			SessionID:    "s1",
			ProjectSlug:  "fixture-slug",
			Role:         "assistant",
			Model:        "claude-opus-4-7",
			Timestamp:    now.Add(-30 * time.Minute),
			InputTokens:  1000,
			OutputTokens: 500,
		},
		{
			SessionID:    "s1",
			ProjectSlug:  "fixture-slug",
			Role:         "assistant",
			Model:        "claude-opus-4-7",
			Timestamp:    now.Add(-10 * time.Minute),
			InputTokens:  2000,
			OutputTokens: 800,
		},
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	return c
}

// TestProgram_BurnRateOverreach pumps a QuotaMsg with a 5h utilization
// that overreaches (95% with ~45m to reset, so ~4h 15m elapsed). The
// projection's MinutesTo100Pct lands in single-digit minutes — well
// inside the burnImminentThreshold — so the rendered header should
// show the compact ·eta limit clause in the red style.
//
// Why this test: the unit tests cover renderBurnRateSide in isolation,
// but only this scenario verifies the full wiring (QuotaMsg →
// recomputeWindow → Compute → Projection → quotaBars two-row layout).
// A wiring regression (e.g. the burn-rate row not being joined into
// quotaBars output) would fail this test even though all unit tests
// still pass.
func TestProgram_BurnRateOverreach(t *testing.T) {
	withForcedColor(t)
	withForcedDarkBackground(t, true)
	c := newSeededCache(t)
	m := New(Deps{Cache: c})
	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })

	now := time.Now()
	tm.Send(QuotaMsg{
		Usage: &anthro.Usage{
			FiveHour: &anthro.Bucket{
				Utilization: 95.0,
				ResetsAt:    timePtr(now.Add(45 * time.Minute)),
			},
		},
		Source:    "api",
		UpdatedAt: now,
	})

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	out, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil {
		t.Fatalf("FinalOutput read: %v", err)
	}
	final := string(out)
	// Compact danger copy is "{rate} →{proj}% ·{eta}" (issue #320 abbreviated
	// the former "projecting …/limit in …" words to the → and · symbols).
	// Assert both the projected (→) and eta (·) markers so this still verifies
	// the full overreach template wired through, not merely any projection.
	for _, want := range []string{"→", "·"} {
		if !strings.Contains(final, want) {
			t.Errorf("expected compact danger marker %q after overreach QuotaMsg; got:\n%s", want, final)
		}
	}
	// Danger style marker — produce the same envelope the renderer would use
	// and check it appears somewhere in the frame. Avoids hard-coding
	// escape bytes; survives lipgloss version bumps.
	redMarker := lipgloss.NewStyle().Foreground(colorDanger).Render(probeMarker)
	openSeq, _, ok := splitANSIEnvelope(redMarker)
	if !ok {
		t.Fatalf("could not split red marker envelope from %q", redMarker)
	}
	if !strings.Contains(final, openSeq) {
		t.Errorf("expected red style envelope %q in output; not found in:\n%s", openSeq, final)
	}
}

// TestProgram_IndexFadeAppearsAndClears feeds an IndexProgressMsg
// falling edge into a live teatest program and verifies the
// ✓ indexed N text appears in a rendered frame and then disappears
// after the full fade window elapses. Asserts on rendered output via
// the bytes channel (not FinalModel) because the model goes back to
// indexFadeStop=0 after the final tick, so the only visible signal
// of the fade is in the intermediate frames.
func TestProgram_IndexFadeAppearsAndClears(t *testing.T) {
	tm := setupTestModel(t)

	// Drive a backfill-shaped sequence: Active=true, then Active=false.
	tm.Send(IndexProgressMsg{Done: 0, Total: 3, Active: true})
	tm.Send(IndexProgressMsg{Done: 3, Total: 3, Active: false})

	// During the fade window (up to 1.2 s), at least one rendered
	// frame should contain "✓ indexed 3".
	teatest.WaitFor(t, tm.Output(),
		func(out []byte) bool {
			return bytes.Contains(out, []byte("✓ indexed 3"))
		},
		teatest.WithCheckInterval(50*time.Millisecond),
		teatest.WithDuration(2*time.Second),
	)

	// Let the full fade cycle finish before quitting and reading
	// FinalModel. WaitFor's output buffer is cumulative, so we cannot
	// poll for disappearance there. Sleep one step past the actual fade
	// (indexFadeStopCount+1 = 4 steps × indexFadeStepDuration) so a
	// late tick from scheduler jitter still lands before the quit.
	time.Sleep((indexFadeStopCount + 1) * indexFadeStepDuration)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(2*time.Second))

	final := tm.FinalModel(t, teatest.WithFinalTimeout(1*time.Second))
	mm, ok := final.(Model)
	if !ok {
		t.Fatalf("FinalModel: expected tui.Model, got %T", final)
	}
	// After the full fade window, indexFadeStop should be back to 0.
	if mm.indexFadeStop != 0 {
		t.Errorf("post-fade indexFadeStop: got %d, want 0", mm.indexFadeStop)
	}
}

// TestProgram_EmptyToFirstChart verifies the transition from the
// "no Claude sessions yet" placeholder to a rendered chart after a
// RefreshMsg with seeded cache data. Mirrors the post-backfill UX
// from #94.
// TestUToggle_PreservesTimeAnchor verifies that toggling u between
// bar (tokens/cost) and remaining preserves the wall-clock moment
// under the viewport's left edge. Locks in the time-range parity
// claim of the design — relies on Task 4's canvas-width mirror and
// Task 3's time-based anchor.
func TestUToggle_PreservesTimeAnchor(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 700)
	defer cleanup()
	m.zoomIdx = 1 // 1h zoom — wide enough canvas to scroll
	m.unitIdx = int(chartUnitTokens)
	m.refreshChart()

	if m.lastCanvasW <= m.viewport.Width+10 {
		t.Skipf("seeded canvas (%d) too narrow for a meaningful scroll on a %d-wide viewport",
			m.lastCanvasW, m.viewport.Width)
	}

	// Scroll to a known mid-history column and capture its time.
	canvasW := m.lastCanvasW
	stride := ZoomLevels[m.zoomIdx].stride()
	wantBucketIdx := (canvasW / 3) / stride
	m.setX(wantBucketIdx)
	wantCol := wantBucketIdx * stride
	wantTime := columnToTime(wantCol, canvasW, m.lastChartFrom, m.lastChartTo)

	// Bar (tokens) → remaining.
	m.unitIdx = int(chartUnitRemaining)
	m.refreshChart()
	gotCol := m.viewportXOffset * ZoomLevels[m.zoomIdx].stride()
	gotTime := columnToTime(gotCol, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
	tolerance := timeColumnTolerance(m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
	if absDuration(gotTime.Sub(wantTime)) > tolerance {
		t.Errorf("u toggle to remaining shifted anchor: want=%v got=%v (tol=%v)",
			wantTime, gotTime, tolerance)
	}

	// Remaining → cost.
	m.unitIdx = int(chartUnitCost)
	m.refreshChart()
	gotCol = m.viewportXOffset * ZoomLevels[m.zoomIdx].stride()
	gotTime = columnToTime(gotCol, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
	tolerance = timeColumnTolerance(m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
	if absDuration(gotTime.Sub(wantTime)) > tolerance {
		t.Errorf("u toggle remaining→cost shifted anchor: want=%v got=%v (tol=%v)",
			wantTime, gotTime, tolerance)
	}
}

// TestZoomCycle_PreservesTimeAnchorInRemaining verifies z cycling
// inside remaining mode keeps the wall-clock under the viewport edge
// when scrolling history.
func TestZoomCycle_PreservesTimeAnchorInRemaining(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 700)
	defer cleanup()
	m.zoomIdx = 0 // 15m — widest canvas
	m.unitIdx = int(chartUnitRemaining)

	// Insert an early usage sample so the remaining-mode lower-bound clamp
	// (#200) doesn't prevent backward scrolling. The sample is placed exactly
	// at m.lastChartFrom (the canvas left edge populated by seedScrollTestModel's
	// initial refreshChart call). timeToColumn clamps any t <= from to column 0,
	// so minX resolves to 0 and never blocks startBucket. This no longer depends
	// on the seed's row count or a hard-coded hour offset.
	earlyUsage := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 5.0, ResetsAt: timePtr(time.Now().UTC())},
	}
	if err := m.deps.Cache.RecordUsageSample(earlyUsage, m.lastChartFrom); err != nil {
		t.Fatalf("RecordUsageSample early: %v", err)
	}
	m.refreshChart()

	if m.lastCanvasW <= m.viewport.Width+10 {
		t.Skipf("seeded canvas (%d) too narrow for a meaningful scroll", m.lastCanvasW)
	}

	stride := ZoomLevels[m.zoomIdx].stride()
	startBucket := (m.lastCanvasW / 4) / stride
	m.setX(startBucket)
	wantCol := startBucket * stride
	wantTime := columnToTime(wantCol, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)

	for i := 1; i < len(ZoomLevels); i++ {
		m.zoomIdx = i
		m.refreshChart()
		if m.underfilled {
			// #300: at this zoom the data is narrower than the viewport, so
			// refreshChart pads the window and setX locks it flush-right (←/→
			// inert). Anchor preservation is a fill-regime concept — when the
			// chart is pinned right there is no scroll position to preserve, so
			// the invariant is vacuous here. (e.g. ~7 days of seed data spans
			// only ~8 day-buckets, which underfills the 24h viewport.)
			continue
		}
		if m.lastCanvasW <= m.viewport.Width {
			// No scroll room at this zoom — the entire canvas fits in
			// the viewport, so the "wall-clock under the left edge"
			// invariant is vacuous (the left edge is always
			// lastChartFrom regardless of pre-zoom scroll position).
			continue
		}
		gotCol := m.viewportXOffset * ZoomLevels[m.zoomIdx].stride()
		gotTime := columnToTime(gotCol, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
		tolerance := timeColumnTolerance(m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
		// Add one zoom-bucket of tolerance: zoom changes can shift the
		// canvas's [from, to) window by up to one bucket-duration
		// (BucketAlign vs DayStartLocal), and the bucket-quantised
		// viewportXOffset shadow drops sub-bucket precision.
		tolerance += ZoomLevels[m.zoomIdx].Duration
		if absDuration(gotTime.Sub(wantTime)) > tolerance {
			t.Errorf("zoom %s shifted anchor: want=%v got=%v (tol=%v)",
				ZoomLevels[m.zoomIdx].Label, wantTime, gotTime, tolerance)
		}
	}
}

// TestRemainingClamp_DoesNotBlockZoomAnchorRestore verifies the precondition
// that the remaining-mode lower-bound clamp (pkg/tui/model.go, the minX guard
// inside setX when unitIdx == chartUnitRemaining) does NOT prevent a backward
// scroll once an early usage sample is anchored at m.lastChartFrom. If that
// clamp regresses, TestZoomCycle_PreservesTimeAnchorInRemaining fails with a
// confusing "zoom shifted anchor" message. This test asserts the precondition
// directly so a clamp regression is caught here with an unambiguous message.
func TestRemainingClamp_DoesNotBlockZoomAnchorRestore(t *testing.T) {
	m, cleanup := seedScrollTestModel(t, 700)
	defer cleanup()
	m.zoomIdx = 0 // 15m — widest canvas
	m.unitIdx = int(chartUnitRemaining)

	// Place an early usage sample at m.lastChartFrom (the canvas left edge).
	// timeToColumn clamps any t <= from to column 0, so minX resolves to 0
	// and the clamp never blocks startBucket. This mirrors the setup in
	// TestZoomCycle_PreservesTimeAnchorInRemaining.
	earlyUsage := anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 5.0, ResetsAt: timePtr(time.Now().UTC())},
	}
	if err := m.deps.Cache.RecordUsageSample(earlyUsage, m.lastChartFrom); err != nil {
		t.Fatalf("RecordUsageSample early: %v", err)
	}
	m.refreshChart()

	if m.lastCanvasW <= m.viewport.Width+10 {
		t.Skipf("seeded canvas (%d) too narrow for a meaningful scroll", m.lastCanvasW)
	}

	stride := ZoomLevels[m.zoomIdx].stride()
	startBucket := (m.lastCanvasW / 4) / stride
	m.setX(startBucket)

	if m.viewportXOffset != startBucket {
		t.Fatalf("setX(%d) clamped to %d — remaining-mode lower-bound (minX) regressed; canvasW=%d viewport.Width=%d",
			startBucket, m.viewportXOffset, m.lastCanvasW, m.viewport.Width)
	}
}

// timeColumnTolerance returns the wall-clock span of one column in
// the current canvas — the minimum quantum the anchor restoration
// can hit. Tests assert anchor preservation within this tolerance.
func timeColumnTolerance(canvasW int, from, to time.Time) time.Duration {
	if canvasW <= 0 || !to.After(from) {
		return time.Second
	}
	return to.Sub(from) / time.Duration(canvasW)
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}

func TestProgram_EmptyToFirstChart(t *testing.T) {
	c := newSeededCache(t)
	m := New(Deps{Cache: c})
	tm := teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40))
	t.Cleanup(func() { _ = tm.Quit() })

	tm.Send(RefreshMsg{})

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))

	out, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil {
		t.Fatalf("FinalOutput read: %v", err)
	}
	final := string(out)
	if strings.Contains(final, "no Claude sessions yet") {
		t.Errorf("final frame still shows empty placeholder, expected chart:\n%s", final)
	}
	// Positive companion: confirm the program rendered SOMETHING — guards
	// against a degenerate empty-buffer pass of the negative assertion.
	// "z zoom" is the short-help footer text, present in every rendered
	// frame once the terminal has a valid size.
	if !strings.Contains(final, "z zoom") {
		t.Errorf("final frame missing footer text 'z zoom' — program may not have rendered:\n%s", final)
	}
}
