package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestLerpTime(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	b := a.Add(100 * time.Hour)
	cases := []struct {
		name string
		r    float64
		want time.Time
	}{
		{"r=0 returns a", 0.0, a},
		{"r=1 returns b", 1.0, b},
		{"r=0.5 midpoint", 0.5, a.Add(50 * time.Hour)},
		{"r<0 clamps to a", -0.5, a},
		{"r>1 clamps to b", 1.5, b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lerpTime(a, b, tc.r); !got.Equal(tc.want) {
				t.Errorf("lerpTime(a, b, %v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}

func TestVisibleWindow_RemainingGeometry(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// One sample 12h in so setX clamps the offset to column 48 (the earliest
	// in-range bucket) — matches TestSetX_RemainingMode_ClampsAtInRangeLeftEdge.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h)
	m.setX(0) // clamp to col 48

	vf, vt := m.visibleWindow()

	// fullCanvasW = max(CanvasWidth(96 buckets @ 15m stride=1)=96, vpW=30)=96.
	// chartXOffset = viewportXOffset(48)*stride(1) = 48.
	// viewFrom = columnToTime(48, 96, from, from+24h) = from+12h.
	// viewTo   = columnToTime(48+30=78, 96, ...)      = from + 78/96*24h = +19.5h.
	wantFrom := from.Add(12 * time.Hour)
	wantTo := from.Add(time.Duration(float64(78) / float64(96) * float64(24*time.Hour)))
	if !vf.Equal(wantFrom) {
		t.Errorf("visibleWindow from = %v, want %v", vf, wantFrom)
	}
	if !vt.Equal(wantTo) {
		t.Errorf("visibleWindow to = %v, want %v", vt, wantTo)
	}
}

// seedRemainingModelWithSamples builds a remaining-mode model at the 15m zoom
// with `n` usage samples spaced 15m apart ending at `now`, then refreshes so
// lastPts5h / lastChart* / hasData reflect the seeded data.
func seedRemainingModelWithSamples(t *testing.T, n int, now time.Time) (Model, *cache.Cache) {
	t.Helper()
	m, c := seedModelAt(t, int(chartUnitRemaining), 0, now)
	for i := range n {
		when := now.Add(-time.Duration(i) * 15 * time.Minute)
		resets := when.Add(2 * time.Hour)
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: 20.0 + float64(i)*2.0, ResetsAt: &resets},
		}
		if err := c.RecordUsageSample(t.Context(), u, when); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m.refreshChart()
	return m, c
}

func TestZoomKey_Remaining_Squeezes_Arms(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 40, now)
	defer c.Close()
	if !m.hasData {
		t.Fatalf("seed sanity: hasData=false, want true (samples seeded)")
	}
	startZoom := m.zoomIdx

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if !m.springActive {
		t.Errorf("after 'z': springActive=false, want true (squeeze armed)")
	}
	if m.springKind != springKindZoom {
		t.Errorf("after 'z': springKind=%d, want springKindZoom(%d)", m.springKind, springKindZoom)
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("after 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	if cmd == nil {
		t.Fatalf("after 'z': cmd=nil, want a springTickMsg tea.Tick")
	}
	if _, ok := cmd().(springTickMsg); !ok {
		t.Errorf("after 'z': cmd produced %T, want springTickMsg", cmd())
	}
}

func TestZoomKey_Remaining_ReduceMotion_Snaps(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedRemainingModelWithSamples(t, 40, now)
	defer c.Close()
	m.deps.ReduceMotion = true
	startZoom := m.zoomIdx

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("ReduceMotion 'z': springActive=true, want false (snap)")
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("ReduceMotion 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	// Snap path re-arms the live-advance now-tick, so cmd is non-nil. We must
	// NOT invoke cmd() to inspect its type: scheduleNowTick returns a tea.Tick
	// fired at the next bucket boundary (up to 1h/24h away), and invoking the
	// Cmd blocks for that real duration. springActive=false above already
	// proves no spring follow-up was armed (the only spring-tick scheduler is
	// the arm path, which sets springActive=true).
	if cmd == nil {
		t.Errorf("ReduceMotion 'z': cmd=nil, want now-tick re-arm")
	}
}

func TestZoomKey_BarMode_HardCut(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitTokens), 40, now)
	defer c.Close()
	startZoom := m.zoomIdx

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("bar-mode 'z': springActive=true, want false (hard-cut, deferred scope)")
	}
	if m.zoomIdx != (startZoom+1)%len(ZoomLevels) {
		t.Errorf("bar-mode 'z': zoomIdx=%d, want %d", m.zoomIdx, (startZoom+1)%len(ZoomLevels))
	}
	// See ReduceMotion test: don't invoke the now-tick cmd (it blocks until the
	// next bucket boundary). springActive=false above proves the hard-cut.
	if cmd == nil {
		t.Errorf("bar-mode 'z': cmd=nil, want now-tick re-arm")
	}
}

func TestZoomKey_NoData_Snaps(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	m, c := seedModelAt(t, int(chartUnitRemaining), 0, now) // no usage samples
	defer c.Close()
	if m.hasData {
		t.Fatalf("seed sanity: hasData=true with no samples, want false")
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'z'}})
	m = updated.(Model)

	if m.springActive {
		t.Errorf("no-data 'z': springActive=true, want false (snap)")
	}
	// See ReduceMotion test: don't invoke the now-tick cmd (it blocks until the
	// next bucket boundary). springActive=false above proves the snap.
	if cmd == nil {
		t.Errorf("no-data 'z': cmd=nil, want now-tick re-arm")
	}
}
