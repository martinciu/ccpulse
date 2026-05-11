package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"
)

// setupTestModel builds a fresh Model with empty Deps and wraps it in a
// teatest.TestModel at a fixed 120x40 terminal. The fixed size keeps
// layout-dependent assertions deterministic across CI runners.
func setupTestModel(t *testing.T) *teatest.TestModel {
	t.Helper()
	m := New(Deps{})
	return teatest.NewTestModel(t, m,
		teatest.WithInitialTermSize(120, 40))
}

// TestProgram_QuitPropagation verifies that sending 'q' shuts the
// program down cleanly within the timeout. This is the harness smoke
// test: if this fails, every other integration test will fail too.
func TestProgram_QuitPropagation(t *testing.T) {
	tm := setupTestModel(t)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})

	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))
}

// TestProgram_HelpOverlayRender verifies that pressing '?' renders the
// help overlay through a real program loop (not just the unit-level
// Update check in TestHelpToggle). Asserts on the rendered key-binding
// description text — full layout-position assertions are deliberately
// out of scope; goldens are the future tool for that (see spec).
func TestProgram_HelpOverlayRender(t *testing.T) {
	tm := setupTestModel(t)

	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})

	// Trigger quit so we can capture FinalOutput cleanly.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))

	out, err := io.ReadAll(tm.FinalOutput(t))
	if err != nil {
		t.Fatalf("FinalOutput read: %v", err)
	}
	final := string(out)
	if !strings.Contains(final, "scroll") {
		t.Errorf("expected help overlay text 'scroll' in final output, got:\n%s", final)
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

	// Resize to a different size than the initial 120x40 to exercise
	// the WindowSizeMsg handler.
	tm.Send(tea.WindowSizeMsg{Width: 100, Height: 30})

	// Trigger a refresh — empty cache, so the placeholder renders.
	tm.Send(RefreshMsg{})

	// Zoom: 15m -> 1h.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})

	// Wait for a frame to be rendered after the zoom press. "z zoom"
	// is always in the short-help footer once the terminal has a valid
	// size, confirming the program is alive and processing messages.
	teatest.WaitFor(t, tm.Output(),
		func(bts []byte) bool { return bytes.Contains(bts, []byte("z zoom")) },
		teatest.WithDuration(500*time.Millisecond),
	)

	// Scroll right.
	tm.Send(tea.KeyMsg{Type: tea.KeyRight})

	// Quit.
	tm.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	tm.WaitFinished(t, teatest.WithFinalTimeout(1*time.Second))

	// FinalModel lets us inspect model state directly. The zoom label
	// "1h" is not in View() output, so we assert on zoomIdx instead.
	final := tm.FinalModel(t, teatest.WithFinalTimeout(1*time.Second))
	m, ok := final.(Model)
	if !ok {
		t.Fatalf("FinalModel: expected tui.Model, got %T", final)
	}
	if m.zoomIdx != 2 {
		t.Errorf("expected zoomIdx=2 (1h) after one 'z' press, got %d", m.zoomIdx)
	}
}
