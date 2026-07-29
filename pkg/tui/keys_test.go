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

func TestKeyMap_ModelsBinding(t *testing.T) {
	k := defaultKeyMap()
	if got := k.Models.Help().Key; got != "m" {
		t.Errorf("Models help key = %q, want m", got)
	}
	if got := k.Models.Help().Desc; got != "models" {
		t.Errorf("Models help desc = %q, want models", got)
	}

	var inShort bool
	for _, b := range k.ShortHelp() {
		if b.Help().Key == "m" {
			inShort = true
		}
	}
	if !inShort {
		t.Error("Models missing from ShortHelp")
	}

	var inFull bool
	for _, row := range k.FullHelp() {
		for _, b := range row {
			if b.Help().Key == "m" {
				inFull = true
			}
		}
	}
	if !inFull {
		t.Error("Models missing from FullHelp — the ? overlay is where the binding stays discoverable below ~89 cols")
	}
}
