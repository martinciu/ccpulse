package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
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

func TestHelpModeSuppressesScroll(t *testing.T) {
	// While the help overlay is up, scroll keys must not affect the
	// underlying chart's offset — otherwise dismissing help (?) reveals
	// a different position than the one the user left.
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	// Seed the viewport with content wider than its width so scroll has
	// room to actually move.
	m.viewport.SetContent(strings.Repeat("X", 500))
	m.viewport.SetXOffset(50)
	startPct := m.viewport.HorizontalScrollPercent()

	// Toggle help on.
	r1, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	m = r1.(Model)
	if !m.showHelp {
		t.Fatalf("expected showHelp=true after '?'")
	}

	// Mash scroll keys.
	for _, k := range []rune{'l', 'l', 'h', 'l'} {
		r, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{k}})
		m = r.(Model)
	}
	if m.viewport.HorizontalScrollPercent() != startPct {
		t.Errorf("scroll keys leaked through help overlay: scroll%% went %.3f → %.3f",
			startPct, m.viewport.HorizontalScrollPercent())
	}

	// Zoom must also not change.
	startZoom := m.zoomIdx
	rz, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = rz.(Model)
	if m.zoomIdx != startZoom {
		t.Errorf("zoom key leaked through help overlay: %d → %d",
			startZoom, m.zoomIdx)
	}
}

func TestQuitKey(t *testing.T) {
	m := New(Deps{})
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if cmd == nil {
		t.Error("expected quit cmd, got nil")
	}
}

func TestRefreshChartClearsOnEmptyData(t *testing.T) {
	c, err := cache.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	m := New(Deps{Cache: c})
	m.w, m.h = 120, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()

	// Seed the viewport with a non-placeholder chart string to simulate
	// stale content from a prior refresh.
	m.viewport.SetContent("STALE CHART CONTENT")

	m.refreshChart()

	got := m.viewport.View()
	if strings.Contains(got, "STALE CHART CONTENT") {
		t.Errorf("refreshChart left stale content in viewport when cache empty:\n%s", got)
	}
	if !strings.Contains(got, "no usage data") {
		t.Errorf("refreshChart did not render empty-state placeholder:\n%s", got)
	}
}

func TestBuildChartEmitsBars(t *testing.T) {
	// Regression: when chartW == len(buckets), the default ntcharts BarGap
	// of 1 forces bar width to (graphSize - gaps) / numBars = 0, so no bars
	// render. buildChart must pass WithBarGap(0) and a Style that has both
	// Foreground and Background set, otherwise the chart is just blank
	// canvas + axis line.
	now := time.Now()
	buckets := make([]cache.TokenBucket, 30)
	for i := range buckets {
		buckets[i] = cache.TokenBucket{
			BucketStart: now.Add(time.Duration(i) * 15 * time.Minute),
			Tokens:      int64((i*7 + 1000) * (1 + i%3)),
		}
	}
	out := buildChart(buckets, 30, 10)
	if !strings.ContainsAny(out, "█▇▆▅▄▃▂▁") {
		t.Errorf("buildChart produced no bar block characters; got:\n%s", out)
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

func TestSevenDayBarRendered(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 1, Has7d: true, Percent7d: 12}
	m.progress = newProgressBar(0.01, m.progressWidth())
	m.progress7d = newProgressBar(0.12, m.progressWidth())
	v := m.View()
	if !strings.Contains(v, "5h") {
		t.Errorf("expected '5h' label in:\n%s", v)
	}
	if !strings.Contains(v, "7d") {
		t.Errorf("expected '7d' label in:\n%s", v)
	}
	if !strings.Contains(v, " 12%") {
		t.Errorf("expected '12%%' for 7d in:\n%s", v)
	}

	// Both labels must appear on the same line — they sit side-by-side
	// inside the header box rather than stacked.
	for _, line := range strings.Split(v, "\n") {
		if strings.Contains(line, "5h") && strings.Contains(line, "7d") {
			return
		}
	}
	t.Errorf("expected '5h' and '7d' on the same line; got:\n%s", v)
}

func TestSevenDayBarPlaceholderWhenAbsent(t *testing.T) {
	m := New(Deps{})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 1, Has7d: false}
	m.progress = newProgressBar(0.01, m.progressWidth())
	v := m.View()
	if !strings.Contains(v, "7d") {
		t.Errorf("expected '7d' label even when absent: %s", v)
	}
	if !strings.Contains(v, "no data") {
		t.Errorf("expected 'no data' placeholder in:\n%s", v)
	}
}

func TestQuotaBarRendered(t *testing.T) {
	// progress.ViewAs at a non-zero percent emits the solid-fill ANSI
	// background sequence for at least one block. View() at 0% emits only
	// the unfilled track; at 80% it must emit filled blocks. Compare.
	low := New(Deps{})
	low.w, low.h = 120, 40
	low.window = status.Window{Percent: 0}
	low.progress = newProgressBar(0, low.progressWidth())

	hi := New(Deps{})
	hi.w, hi.h = 120, 40
	hi.window = status.Window{Percent: 80}
	hi.progress = newProgressBar(0.8, hi.progressWidth())

	if low.View() == hi.View() {
		t.Errorf("quota bar must render differently at 0%% vs 80%%; got identical View output")
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

func TestHeaderShowsDevChip(t *testing.T) {
	m := New(Deps{IsDev: true})
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 5, MinutesToReset: 60, CeilingLabel: "max_20x"}
	got := m.View()
	if !strings.Contains(got, "[DEV]") {
		t.Errorf("expected [DEV] chip in dev header, got:\n%s", got)
	}
}

func TestHeaderHidesDevChipInRelease(t *testing.T) {
	m := New(Deps{}) // IsDev defaults to false
	m.w, m.h = 120, 40
	m.window = status.Window{Percent: 5, MinutesToReset: 60, CeilingLabel: "max_20x"}
	got := m.View()
	if strings.Contains(got, "[DEV]") {
		t.Errorf("release header should not contain [DEV] chip:\n%s", got)
	}
}
