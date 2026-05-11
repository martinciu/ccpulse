package tui

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
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
// Zoom note: ZoomLevels = [5m, 15m, 1h], initial zoomIdx = 1 (15m).
// One 'z' press cycles to index 2 (label "1h").
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
	if m.zoomIdx != 2 {
		t.Errorf("expected zoomIdx=2 (1h) after one 'z' press, got %d", m.zoomIdx)
	}
}

// newSeededCache opens a fresh Cache at t.TempDir() and inserts a
// small set of assistant messages so TokenBuckets returns non-empty
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

// TestProgram_EmptyToFirstChart verifies the transition from the
// "no Claude sessions yet" placeholder to a rendered chart after a
// RefreshMsg with seeded cache data. Mirrors the post-backfill UX
// from #94.
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
