package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/martinciu/ccpulse/pkg/cache"
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

func TestLiveTabRendersSessions(t *testing.T) {
	m := New(Deps{})
	m.tab = TabLive
	m.live = []cache.LiveSession{
		{ProjectCanonical: "/foo/dotfiles", Model: "claude-opus-4-7", CostUSD: 1.84},
	}
	v := m.View()
	if !strings.Contains(v, "dotfiles") {
		t.Errorf("expected project in view:\n%s", v)
	}
	if !strings.Contains(v, "$1.84") {
		t.Errorf("expected $1.84 in view:\n%s", v)
	}
}

func TestTodayTabRendersModels(t *testing.T) {
	m := New(Deps{})
	m.tab = TabToday
	m.today = []cache.ModelTotals{
		{Model: "claude-opus-4-7", Messages: 12, CostUSD: 7.10},
	}
	v := m.View()
	if !strings.Contains(v, "opus-4-7") || !strings.Contains(v, "$7.10") {
		t.Errorf("today rendering missing data: %s", v)
	}
}

func TestEnterDrillsHistory(t *testing.T) {
	m := New(Deps{})
	m.tab = TabHistory
	m.history = []cache.DayTotals{{Date: "2026-05-08", Sessions: 1, CostUSD: 1.0}}
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !updated.(Model).drilled {
		t.Errorf("expected drilled=true")
	}
	upBack, _ := updated.(Model).Update(tea.KeyMsg{Type: tea.KeyEsc})
	if upBack.(Model).drilled {
		t.Errorf("expected drilled=false after esc")
	}
}
