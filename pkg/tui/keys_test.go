package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestProjectsKeyInHelp(t *testing.T) {
	// The 'p projects' binding must appear in both ShortHelp (footer) and
	// FullHelp (overlay opened with '?'). Asserts on rendered help strings so
	// a misnamed help text surfaces in the failure output.
	m := New(Deps{})
	m.w, m.h = 120, 40

	footer := m.help.View(m.keys)
	if !strings.Contains(footer, "p projects") {
		t.Errorf("footer help missing 'p projects' binding:\n%s", footer)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	overlay := updated.(Model).View()
	if !strings.Contains(overlay, "projects") {
		t.Errorf("help overlay missing 'projects' binding:\n%s", overlay)
	}
}
