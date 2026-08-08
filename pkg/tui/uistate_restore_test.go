package tui

import (
	"os"
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

func TestZoomAndUnitKeys_PersistUIState(t *testing.T) {
	// Snap paths only (ReduceMotion) — no tick Cmds to leak. Cache is nil;
	// refreshChart no-ops on nil cache, and persistence must not depend on
	// a refresh succeeding.
	dir := t.TempDir()
	m := New(Deps{CacheDir: dir, ReduceMotion: true})
	m.w, m.h = 120, 40

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if got, want := uistate.Load(dir), (uistate.State{Zoom: "1h", View: "cost"}); got != want {
		t.Errorf("state after z = %+v, want %+v", got, want)
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if got, want := uistate.Load(dir), (uistate.State{Zoom: "1h", View: "tokens"}); got != want {
		t.Errorf("state after u = %+v, want %+v", got, want)
	}

	// Full u-cycle: tokens → usage → cost; the file must track each press.
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if got := uistate.Load(dir).View; got != "usage" {
		t.Errorf("view after second u = %q, want usage", got)
	}
	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'u'}})
	m = updated.(Model)
	if got := uistate.Load(dir).View; got != "cost" {
		t.Errorf("view after third u = %q, want cost", got)
	}
}

func TestZoomKey_AnimatedPath_PersistsNewValue(t *testing.T) {
	// The snap-path test above cannot catch an ordering bug on the
	// ANIMATED branch, where handleZoomKey advances zoomIdx amid the
	// spring setup rather than as its first statement. Press 'z' with
	// motion on and assert the file already holds the NEW zoom — the
	// invariant persistUIState depends on and that the plan only stated
	// in prose (#490).
	//
	// The returned Cmd is deliberately discarded, never invoked: it is a
	// spring tick, and invoking tick Cmds in tests leaks goroutines past
	// the package's goleak guard.
	dir := t.TempDir()
	seed := seedIntroModel(t, false)
	m := New(Deps{Cache: seed.deps.Cache, CacheDir: dir})

	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if !m.hasData {
		t.Fatal("hasData = false after WindowSizeMsg; animated path would not be exercised")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if !m.springActive || m.springKind != springKindZoom {
		t.Fatalf("springActive=%v springKind=%v after z; want the animated zoom path", m.springActive, m.springKind)
	}
	if got, want := uistate.Load(dir), (uistate.State{Zoom: "1h", View: "cost"}); got != want {
		t.Errorf("state after animated z = %+v, want %+v (stale value persisted?)", got, want)
	}
}

func TestPersistUIState_EmptyCacheDir_NoWrite(t *testing.T) {
	// Bare Deps{} (the fixture most tui tests use) must not panic, and
	// pressing 'z' must not write anything anywhere — Chdir into an empty
	// temp dir so a stray write (including the atomic-write ".tmp-*" file)
	// is directly observable via ReadDir below.
	t.Chdir(t.TempDir())

	m := New(Deps{ReduceMotion: true})
	m.w, m.h = 120, 40
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)
	if m.zoomIdx != 1 {
		t.Errorf("zoomIdx = %d, want 1 (z still cycles zoom even with no cache dir)", m.zoomIdx)
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	for _, e := range entries {
		t.Errorf("unexpected entry in CWD after z with no CacheDir: %q", e.Name())
	}
}
