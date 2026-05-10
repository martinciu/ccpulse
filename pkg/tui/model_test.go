package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/status"
)

func TestInitialView_ContainsCcpulse(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	v := m.View()
	if !strings.Contains(v, "ccpulse") {
		t.Errorf("expected 'ccpulse' in view, got:\n%s", v)
	}
}

func TestHeaderShowsPercent(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 61, MinutesToReset: 107, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "61%") {
		t.Errorf("expected 61%% in:\n%s", got)
	}
	if !strings.Contains(got, "1h 47m") {
		t.Errorf("expected 1h 47m in:\n%s", got)
	}
}

func TestHelpToggle(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	v := updated.(Model).View()
	// help overlay renders key binding descriptions
	if !strings.Contains(v, "scroll") {
		t.Errorf("help overlay missing key descriptions: %s", v)
	}
}

func TestZoomCycles(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	initial := m.zoomIdx
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	next := updated.(Model).zoomIdx
	if next == initial {
		t.Errorf("zoom did not change: still %d", next)
	}
	// Three z presses should cycle back to start
	m2 := updated.(Model)
	r2, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m2 = r2.(Model)
	r3, _ := m2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	if r3.(Model).zoomIdx != initial {
		t.Errorf("zoom did not cycle: want %d, got %d", initial, r3.(Model).zoomIdx)
	}
}

func TestQuitKey(t *testing.T) {
	m := New(Deps{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected quit cmd, got nil")
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

func TestIndexProgressMsg(t *testing.T) {
	m := New(Deps{})
	updated, _ := m.Update(IndexProgressMsg{Done: 3, Total: 7, Active: true})
	got := updated.(Model)
	if got.indexDone != 3 || got.indexTotal != 7 || !got.indexActive {
		t.Errorf("IndexProgressMsg not applied: %+v", got)
	}
}

func TestViewFitsTerminal(t *testing.T) {
	for _, h := range []int{20, 40, 60} {
		m := New(Deps{})
		m.w, m.h = 120, h
		m.viewport.Width = m.chartWidth()
		m.viewport.Height = m.chartHeight()
		m.viewport.SetContent(strings.Repeat("X\n", m.chartHeight()))
		v := m.View()
		got := strings.Count(v, "\n") + 1
		if got > h {
			t.Errorf("h=%d: rendered %d lines, exceeds terminal height", h, got)
		}
	}
}

func TestQuotaMsgApplied(t *testing.T) {
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
