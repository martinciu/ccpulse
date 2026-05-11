package tui

import (
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
