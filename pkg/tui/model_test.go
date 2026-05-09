package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/martinciu/ccpulse/pkg/status"
)

func TestInitialView(t *testing.T) {
	m := New(Deps{})
	v := m.View()
	if !strings.Contains(v, "ccpulse") {
		t.Errorf("expected 'ccpulse' in view, got:\n%s", v)
	}
	if !strings.Contains(v, "Live") {
		t.Errorf("expected Live tab in view")
	}
}

func TestHeaderShowsPercent(t *testing.T) {
	m := New(Deps{})
	m.window = status.Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "61%") {
		t.Errorf("expected 61%% in:\n%s", got)
	}
	if !strings.Contains(got, "1h 47m") {
		t.Errorf("expected 1h 47m in:\n%s", got)
	}
}

func TestHelpOverlay(t *testing.T) {
	m := New(Deps{})
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	v := updated.(Model).View()
	if !strings.Contains(v, "Keys") {
		t.Errorf("help overlay missing")
	}
}
