package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/uistate"
)

func TestNew_RestoresUIState(t *testing.T) {
	tests := []struct {
		name     string
		state    uistate.State
		wantZoom int
		wantUnit int
	}{
		{"zero state keeps defaults", uistate.State{}, 0, int(chartUnitCost)},
		{"24h and usage", uistate.State{Zoom: "24h", View: "usage"}, 2, int(chartUnitRemaining)},
		{"1h and tokens", uistate.State{Zoom: "1h", View: "tokens"}, 1, int(chartUnitTokens)},
		{"unknown zoom falls back to 15m", uistate.State{Zoom: "2h", View: "cost"}, 0, int(chartUnitCost)},
		{"unknown view falls back to cost", uistate.State{Zoom: "15m", View: "sparkles"}, 0, int(chartUnitCost)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := New(Deps{UIState: tt.state})
			if m.zoomIdx != tt.wantZoom {
				t.Errorf("zoomIdx = %d, want %d", m.zoomIdx, tt.wantZoom)
			}
			if m.unitIdx != tt.wantUnit {
				t.Errorf("unitIdx = %d, want %d", m.unitIdx, tt.wantUnit)
			}
			// Restore must not arm any animation — only key handlers do.
			if m.springActive {
				t.Error("springActive = true after New; restore must not fire springs")
			}
		})
	}
}

func TestViewName_RoundTripsViewIdx(t *testing.T) {
	// Every persistable unit must survive name→idx→name unchanged, so a
	// value written by persistUIState is always restorable.
	for i := range int(chartUnitCount) {
		u := chartUnit(i)
		idx, ok := viewIdx(viewName(u))
		if !ok || idx != i {
			t.Errorf("viewIdx(viewName(%v)) = (%d, %v), want (%d, true)", u, idx, ok, i)
		}
	}
}

func TestZoomIdxForLabel_CoversAllZoomLevels(t *testing.T) {
	for i, z := range ZoomLevels {
		idx, ok := zoomIdxForLabel(z.Label)
		if !ok || idx != i {
			t.Errorf("zoomIdxForLabel(%q) = (%d, %v), want (%d, true)", z.Label, idx, ok, i)
		}
	}
	if _, ok := zoomIdxForLabel(""); ok {
		t.Error(`zoomIdxForLabel("") ok = true, want false`)
	}
}

func TestIntro_RestoredUsageView_ArmsLineMode(t *testing.T) {
	// A restored "usage" view must take the intro's existing line-mode
	// path: restoring is not a unit-toggle keypress, so no unit spring
	// fires — beginIntroAnimation just reads the restored unitIdx (#490).
	seed := seedIntroModel(t, false)
	u := anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 42.0, ResetsAt: timePtr(time.Now().UTC())}}
	// -5m keeps the sample inside the chart window regardless of zoom:
	// the window starts at the earliest seeded message (-30m), so an
	// older sample could fall outside it and never set hasData.
	if err := seed.deps.Cache.RecordUsageSample(t.Context(), u, time.Now().UTC().Add(-5*time.Minute)); err != nil {
		t.Fatalf("RecordUsageSample: %v", err)
	}
	m := New(Deps{Cache: seed.deps.Cache, UIState: uistate.State{Zoom: "24h", View: "usage"}})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)

	if !m.springActive || !m.springIntro {
		t.Fatalf("springActive=%v springIntro=%v after WindowSizeMsg; want intro armed", m.springActive, m.springIntro)
	}
	if !m.newIsLine {
		t.Error("newIsLine = false; want true (restored usage view renders the line-mode intro)")
	}
	if m.zoomIdx != 2 {
		t.Errorf("zoomIdx = %d, want 2 (24h)", m.zoomIdx)
	}
}
