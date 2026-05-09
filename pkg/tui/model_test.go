package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/state"
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
		{ProjectCanonical: "/foo/dotfiles", Model: "claude-opus-4-7", CostUSD: 1.84,
			LastTS: time.Now().Add(-time.Hour)},
	}
	v := m.View()
	if !strings.Contains(v, "dotfiles") {
		t.Errorf("expected project in view:\n%s", v)
	}
	if !strings.Contains(v, "$1.84") {
		t.Errorf("expected $1.84 in view:\n%s", v)
	}
}

func TestLiveMarkers(t *testing.T) {
	now := time.Now()
	rows := []cache.LiveSession{
		{SessionID: "s1", ProjectCanonical: "/x/dotfiles", Model: "opus", CostUSD: 1.0,
			LastTS: now.Add(-1 * time.Minute)},
		{SessionID: "s2", ProjectCanonical: "/x/foo", Model: "sonnet", CostUSD: 2.0,
			LastTS: now.Add(-1 * time.Hour)},
	}
	got := renderLive(rows, map[string]bool{"s1": true}, now)
	if !strings.Contains(got, "⚡◆") {
		t.Errorf("expected ⚡◆ on s1: %s", got)
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

func TestScopeTogglePersists(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_STATE_HOME", tmp)
	m := New(Deps{})
	m.tab = TabLive
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'g'}})
	if updated.(Model).liveScope != "this_tmux" {
		t.Errorf("scope not flipped, got %q", updated.(Model).liveScope)
	}
	saved := state.Load()
	if saved.LiveScope != "this_tmux" {
		t.Errorf("not persisted, got %q", saved.LiveScope)
	}
}

func TestUpdate_IndexProgressMsgUpdatesModelState(t *testing.T) {
	m := Model{}
	updated, _ := m.Update(IndexProgressMsg{Done: 3, Total: 7, Active: true})
	got := updated.(Model)

	if got.indexDone != 3 {
		t.Errorf("indexDone = %d, want 3", got.indexDone)
	}
	if got.indexTotal != 7 {
		t.Errorf("indexTotal = %d, want 7", got.indexTotal)
	}
	if !got.indexActive {
		t.Errorf("indexActive = false, want true")
	}
}

func TestUpdate_IndexProgressMsg_FinalClearsActive(t *testing.T) {
	m := Model{indexDone: 3, indexTotal: 7, indexActive: true}
	updated, _ := m.Update(IndexProgressMsg{Done: 7, Total: 7, Active: false})
	got := updated.(Model)

	if got.indexActive {
		t.Errorf("indexActive = true, want false (final progress)")
	}
	if got.indexTotal != 7 {
		t.Errorf("indexTotal = %d, want 7", got.indexTotal)
	}
}

func TestRenderHeader_NoIndicatorWhenInactive(t *testing.T) {
	got := renderHeader(Style{}, status.Window{CeilingLabel: "Max 20×"}, false, 80,
		IndexProgress{Active: false})
	if strings.Contains(got, "indexing") {
		t.Errorf("header contains 'indexing' when Active=false:\n%s", got)
	}
}

func TestRenderHeader_ShowsIndexingWhenActive(t *testing.T) {
	got := renderHeader(Style{}, status.Window{CeilingLabel: "Max 20×"}, false, 80,
		IndexProgress{Active: true, Done: 12, Total: 47})
	if !strings.Contains(got, "indexing 12/47") {
		t.Errorf("header missing 'indexing 12/47':\n%s", got)
	}
}

func TestHeatColor(t *testing.T) {
	tests := []struct {
		ratio float64
		want  lipgloss.Color
	}{
		{0.0, Green},
		{0.32, Green},
		{0.33, Yellow},
		{0.65, Yellow},
		{0.66, Red},
		{1.0, Red},
	}
	for _, tt := range tests {
		got := heatColor(tt.ratio)
		if got != tt.want {
			t.Errorf("heatColor(%v) = %v, want %v", tt.ratio, got, tt.want)
		}
	}
}

func TestQuotaMsgUpdatesWindow(t *testing.T) {
	m := New(Deps{Cache: nil})
	msg := QuotaMsg{
		Usage:     &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: time.Now().Add(time.Hour)}},
		Source:    "api",
		UpdatedAt: time.Now(),
	}
	next, _ := m.Update(msg)
	nm := next.(Model)
	if nm.quota == nil || nm.quotaSource != "api" {
		t.Errorf("QuotaMsg not applied: %+v", nm)
	}
}

func TestRenderHeader_BothBars(t *testing.T) {
	w := status.Window{
		Percent:          14,
		MinutesToReset:   2*60 + 3,
		Has7d:            true,
		Percent7d:        89,
		MinutesToReset7d: 17*60 + 33,
		CeilingLabel:     "max_20x",
	}
	got := renderHeader(Style{}, w, false, 80, IndexProgress{})
	for _, want := range []string{"5h", "7d", "14%", "89%", "2h 3m", "17h 33m"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Usage window") {
		t.Errorf("did not expect single-bar caption when Has7d=true:\n%s", got)
	}
	if strings.Contains(got, "to reset") {
		t.Errorf("did not expect 'to reset' suffix in side-by-side layout:\n%s", got)
	}
}

func TestRenderHeader_SingleBarWhenNo7d(t *testing.T) {
	w := status.Window{
		Percent:        14,
		MinutesToReset: 2*60 + 3,
		CeilingLabel:   "max_20x",
	}
	got := renderHeader(Style{}, w, false, 80, IndexProgress{})
	for _, want := range []string{"Usage window", "14%", "2h 3m", "to reset"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in single-bar fallback:\n%s", want, got)
		}
	}
	if strings.Contains(got, "7d") {
		t.Errorf("unexpected 7d label in single-bar fallback:\n%s", got)
	}
}

func TestRenderHeader_NoPanicOnZeroWindow(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("renderHeader panicked on zero Window: %v", r)
		}
	}()
	got := renderHeader(Style{}, status.Window{}, false, 80, IndexProgress{})
	if strings.Contains(got, "7d") {
		t.Errorf("zero Window should not include 7d label:\n%s", got)
	}
}
