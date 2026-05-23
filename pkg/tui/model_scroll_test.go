package tui

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func TestEarliestRemainingSampleAt(t *testing.T) {
	t.Parallel()
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	tests := []struct {
		name  string
		pts5h []cache.UtilizationPoint
		pts7d []cache.UtilizationPoint
		want  time.Time
	}{
		{
			name:  "both empty returns zero time",
			pts5h: nil,
			pts7d: nil,
			want:  time.Time{},
		},
		{
			name:  "only 5h populated returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t1}, {At: t2}},
			pts7d: nil,
			want:  t1,
		},
		{
			name:  "only 7d populated returns 7d[0]",
			pts5h: nil,
			pts7d: []cache.UtilizationPoint{{At: t2}},
			want:  t2,
		},
		{
			name:  "both populated returns earlier of the two",
			pts5h: []cache.UtilizationPoint{{At: t2}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
		{
			name:  "both populated 5h earlier returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t0}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t0,
		},
		{
			name:  "both populated equal timestamps returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t1}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
		{
			name:  "empty non-nil 5h plus populated 7d returns 7d[0]",
			pts5h: []cache.UtilizationPoint{},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := earliestRemainingSampleAt(tt.pts5h, tt.pts7d)
			if !got.Equal(tt.want) {
				t.Errorf("earliestRemainingSampleAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// remainingModeModel returns a Model wired for setX remaining-mode tests:
//   - 15m zoom (stride=1, BarWidth=1, BarGap=0)
//   - unitIdx = chartUnitRemaining
//   - viewport.Width = 30, lastCanvasW = 96 → maxX = 66
//   - canvas spanning [from, from+24h)
//
// With these dimensions, timeToColumn(from + N*15min) = N (one col per
// 15m bucket), so the test cases can reason in bucket-index space
// directly — no manual stride or canvas-width arithmetic.
// viewport.Width=30 (not 80) ensures maxX=66 ≥ 60, so tests that check
// in-range position preservation have room to land above minX=48.
func remainingModeModel(pts5h, pts7d []cache.UtilizationPoint) *Model {
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	m := New(Deps{})
	m.zoomIdx = 0 // 15m, stride=1
	m.unitIdx = int(chartUnitRemaining)
	m.w = 120
	m.viewport.Width = 30
	m.lastChartFrom = from
	m.lastChartTo = to
	m.lastCanvasW = 96 // 96 cols of 15m at stride=1 → spans 24h
	m.lastZoomStride = 1
	m.lastPts5h = pts5h
	m.lastPts7d = pts7d
	return &m
}

func TestSetX_RemainingMode_ClampsAtInRangeLeftEdge(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample is 12h into the canvas = column 48.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(0) // try to pan to the far left

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(0) = %d, want %d (earliest in-range bucket)", got, want)
	}
}

func TestSetX_RemainingMode_OutOfRangeSnapsIn(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	m.viewportXOffset = 5 // simulate a stale pre-switch anchor

	// refreshChart-style restore: anchorTime maps to col 5; setX clamps up.
	m.setX(5)

	if got, want := m.viewportXOffset, 48; got != want {
		t.Errorf("viewportXOffset after setX(5) = %d, want %d (snapped to in-range)", got, want)
	}
}

func TestSetX_RemainingMode_InRangePreservesPosition(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(60) // already past the in-range left edge (48)

	if got, want := m.viewportXOffset, 60; got != want {
		t.Errorf("viewportXOffset after setX(60) = %d, want %d (preserved, no clamp)", got, want)
	}
}

func TestSetX_RemainingMode_EmptySamplesLowerBoundZero(t *testing.T) {
	t.Parallel()
	m := remainingModeModel(nil, nil)

	// Must not panic on empty slices; minX stays 0 because earliest IsZero.
	m.setX(0)

	if got := m.viewportXOffset; got != 0 {
		t.Errorf("viewportXOffset after setX(0) with empty samples = %d, want 0", got)
	}
}

func TestSetX_RemainingMode_EmptySamplesClampsToCanvasMaxX(t *testing.T) {
	t.Parallel()
	m := remainingModeModel(nil, nil)

	m.setX(999) // overshoot upper bound
	// Upper bound: max(0, lastCanvasW - viewport.Width) / stride
	//            = max(0, 96 - 30) / 1 = 66
	if got, want := m.viewportXOffset, 66; got != want {
		t.Errorf("viewportXOffset after setX(999) with empty samples = %d, want %d (canvas-clamp upper bound)", got, want)
	}
}

func TestSetX_BarModeAfterRemaining_BarBoundsRestored(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	// Populate bar-mode state: 200 buckets in lastStarts (much wider
	// than the canvas; bar-mode clamp is len(lastStarts)-visibleBuckets).
	m.lastStarts = make([]time.Time, 200)
	for i := range m.lastStarts {
		m.lastStarts[i] = from.Add(time.Duration(i) * 15 * time.Minute)
	}
	// Switch out of remaining mode to tokens.
	m.unitIdx = int(chartUnitTokens)

	// Pan to a position that would be clamped in remaining mode (col 5
	// < earliest in-range col 48); in bar mode this is in bounds.
	m.setX(5)

	if got, want := m.viewportXOffset, 5; got != want {
		t.Errorf("viewportXOffset after setX(5) in bar mode = %d, want %d (no remaining-mode clamp)", got, want)
	}
}

func TestSetX_RemainingMode_MinXAboveMaxX_CollapsesToMaxX(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample at 20h into the 24h canvas → column 80.
	// remainingModeModel has lastCanvasW=96, viewport.Width=30, stride=1
	// → maxX = (96 - 30)/1 = 66. Since 80 > 66, the explicit
	// `if minX > maxX { minX = maxX }` branch in setX must collapse
	// minX to 66. Without that collapse, min(max(n, 80), 66) would
	// produce 80 — wrong direction past maxX.
	pts5h := []cache.UtilizationPoint{{At: from.Add(20 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)

	m.setX(0) // any value; clamp pulls up to the collapsed minX (= maxX).

	if got, want := m.viewportXOffset, 66; got != want {
		t.Errorf("viewportXOffset after setX(0) with minX > maxX = %d, want %d (collapsed to maxX)", got, want)
	}
}

func TestSetX_RemainingMode_ZeroChartFromSkipsLowerBound(t *testing.T) {
	t.Parallel()
	from := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	// Earliest sample at 12h (= col 48) would normally clamp setX(0) up
	// to 48. But when m.lastChartFrom is zero-valued, the guard
	// `!m.lastChartFrom.IsZero() && m.lastChartTo.After(m.lastChartFrom)`
	// in setX must short-circuit the lower-bound clamp, leaving minX=0
	// and the existing canvas-clamp path intact.
	pts5h := []cache.UtilizationPoint{{At: from.Add(12 * time.Hour)}}
	m := remainingModeModel(pts5h, nil)
	m.lastChartFrom = time.Time{}

	m.setX(0)

	if got, want := m.viewportXOffset, 0; got != want {
		t.Errorf("viewportXOffset after setX(0) with zero lastChartFrom = %d, want %d (lower-bound clamp skipped)", got, want)
	}
}

func TestZoomLevels_ScrollStep(t *testing.T) {
	t.Parallel()
	want := map[string]int{"15m": 3, "1h": 3, "24h": 1}
	for _, z := range ZoomLevels {
		if got := z.ScrollStep; got != want[z.Label] {
			t.Errorf("ZoomLevels[%q].ScrollStep = %d, want %d", z.Label, got, want[z.Label])
		}
	}
}

// seedBarModel opens a temp cache, inserts one message per bucket at the
// given spacing with a flat InputTokens value, and returns a tokens-mode
// Model at the requested zoom, refreshed and pinned to the right edge.
func seedBarModel(t *testing.T, zoomIdx, nBuckets int, spacing time.Duration) (Model, *cache.Cache) {
	t.Helper()
	dir := t.TempDir()
	c, err := cache.Open(filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatalf("cache.Open: %v", err)
	}
	tab, err := pricing.Load()
	if err != nil {
		t.Fatalf("pricing.Load: %v", err)
	}
	now := time.Now().UTC().Truncate(spacing)
	var msgs []parse.Message
	for i := range nBuckets {
		msgs = append(msgs, parse.Message{
			SessionID:   "s",
			ProjectSlug: "p",
			Model:       "claude-opus-4-7",
			Timestamp:   now.Add(-time.Duration(i) * spacing),
			InputTokens: 5000,
		})
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		t.Fatalf("InsertMessages: %v", err)
	}
	m := New(Deps{Cache: c})
	m.unitIdx = int(chartUnitTokens)
	m.zoomIdx = zoomIdx
	// w=122 → chartWidth()=120 → slack=(120+2)%12=2 at 24h, so the
	// flush-right leading-bucket path (#306) is actually exercised. A width
	// that is an exact stride multiple (e.g. w=120 → chartWidth=118,
	// slack=0) makes TestScroll24h_NoRightEdgeGap vacuous.
	m.w, m.h = 122, 40
	m.viewport.Width = m.chartWidth()
	m.viewport.Height = m.chartHeight()
	m.refreshChart()
	return m, c
}

func TestScrollStep_OneBucketAt24h(t *testing.T) {
	t.Parallel()
	m, c := seedBarModel(t, 2 /* 24h */, 40, 24*time.Hour)
	defer c.Close()
	before := m.viewportXOffset
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if got := before - m.viewportXOffset; got != 1 {
		t.Errorf("24h: one ScrollLeft moved viewportXOffset by %d, want 1 (one day per press, #306)", got)
	}
}

func TestScrollStep_ThreeBucketsAt15m(t *testing.T) {
	t.Parallel()
	m, c := seedBarModel(t, 0 /* 15m */, 300, 15*time.Minute)
	defer c.Close()
	before := m.viewportXOffset
	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyLeft})
	m = updated.(Model)
	if got := before - m.viewportXOffset; got != 3 {
		t.Errorf("15m: one ScrollLeft moved viewportXOffset by %d, want 3 (unchanged finer-zoom step)", got)
	}
}

// rightmostNonSpaceCol returns the largest column index holding a non-space
// rune across all rows of s (ANSI already stripped). -1 if s is all spaces.
func rightmostNonSpaceCol(s string) int {
	maxCol := -1
	for _, line := range strings.Split(s, "\n") {
		last := -1
		for i, r := range []rune(line) {
			if r != ' ' {
				last = i
			}
		}
		if last > maxCol {
			maxCol = last
		}
	}
	return maxCol
}

// rowHasContentBothEnds reports whether row (ANSI already stripped) has a
// non-space rune in both its left quarter [0, w/4) and right quarter [3w/4, w).
// Used to assert the x-axis label row spans the full chart width (#300).
func rowHasContentBothEnds(row string, w int) bool {
	r := []rune(row)
	var left, right bool
	for i := 0; i < len(r) && i < w; i++ {
		if r[i] == ' ' {
			continue
		}
		if i < w/4 {
			left = true
		}
		if i >= 3*w/4 {
			right = true
		}
	}
	return left && right
}

// TestRefreshChart_Underfill_FlushRight verifies that when indexed data is
// narrower than the chart (2 buckets), refreshChart pads the window leftward so
// data hugs the right edge and the x-axis spans the full width — at every zoom
// and both bar units (#300).
func TestRefreshChart_Underfill_FlushRight(t *testing.T) {
	t.Parallel()
	for _, unit := range []chartUnit{chartUnitTokens, chartUnitCost} {
		for _, zi := range []int{0, 1, 2} { // 15m, 1h, 24h
			unit, zi := unit, zi
			t.Run(unit.String()+"_"+ZoomLevels[zi].Label, func(t *testing.T) {
				t.Parallel()
				m, c := seedBarModel(t, zi, 2, ZoomLevels[zi].Duration)
				defer c.Close()
				if unit != chartUnitTokens {
					m.unitIdx = int(unit)
					m.refreshChart()
				}

				if !m.underfilled {
					t.Fatalf("expected underfilled=true with 2 buckets at %s", ZoomLevels[zi].Label)
				}
				// Window padded to span at least the viewport.
				if got, want := bucketCountInRange(m.lastChartFrom, m.lastChartTo, ZoomLevels[zi].Duration),
					m.visibleBuckets()+1; got < want {
					t.Errorf("window spans %d buckets, want >= %d (padded to fill width)", got, want)
				}
				// Data flush-right: newest bucket (last) holds data, oldest is padding.
				if n := len(m.lastValues); n == 0 || m.lastValues[n-1] == 0 {
					t.Errorf("newest bucket should hold data (flush-right); lastValues=%v", m.lastValues)
				}
				if m.lastValues[0] != 0 {
					t.Errorf("oldest bucket should be zero-fill padding, got %v", m.lastValues[0])
				}
				// Rendered content reaches the right edge.
				view := ansi.Strip(m.viewport.View())
				if got, want := rightmostNonSpaceCol(view), m.viewport.Width-1; got != want {
					t.Errorf("content reaches col %d, want %d (data flush-right)", got, want)
				}
				// X-axis label row spans the full width (left + right populated).
				lines := strings.Split(view, "\n")
				if labelRow := lines[len(lines)-1]; !rowHasContentBothEnds(labelRow, m.viewport.Width) {
					t.Errorf("x-axis labels do not span full width at %s:\n%q", ZoomLevels[zi].Label, labelRow)
				}
			})
		}
	}
}

func TestScroll24h_NoRightEdgeGap(t *testing.T) {
	t.Parallel()
	m, c := seedBarModel(t, 2 /* 24h */, 40, 24*time.Hour)
	defer c.Close()

	// Baseline: pinned-right already fills the viewport.
	if got, want := rightmostNonSpaceCol(ansi.Strip(m.viewport.View())), m.viewport.Width-1; got != want {
		t.Fatalf("baseline (pinned): content reaches col %d, want %d", got, want)
	}

	// Scroll left a few days (24h step = 1 bucket). Flat tall bars mean the
	// rightmost visible bar column must still be a glyph — if the #306 gap
	// regressed, the right `slack` columns would be blank.
	for range 3 {
		m.scrollLeft(ZoomLevels[m.zoomIdx].ScrollStep)
	}
	if m.viewportXOffset < 1 {
		t.Fatalf("test setup: viewportXOffset=%d after 3 left presses; need >=1 to exercise the leading-bucket path", m.viewportXOffset)
	}
	if got, want := rightmostNonSpaceCol(ansi.Strip(m.viewport.View())), m.viewport.Width-1; got != want {
		t.Errorf("after scroll-left at 24h: content reaches col %d, want %d (right-edge gap, #306)", got, want)
	}
}

func TestScroll24h_OldestEdgeClampsToZero(t *testing.T) {
	t.Parallel()
	m, c := seedBarModel(t, 2 /* 24h */, 40, 24*time.Hour)
	defer c.Close()

	// Over-scroll left; setX clamps the bucket index at 0 (oldest bar
	// flush-left, slack on the right — the chosen #306 boundary).
	for range 100 {
		m.scrollLeft(ZoomLevels[m.zoomIdx].ScrollStep)
	}
	if got := m.viewportXOffset; got != 0 {
		t.Errorf("viewportXOffset after over-scroll-left = %d, want 0 (oldest edge)", got)
	}
}

func TestRenderWindow_24hInBarLabels(t *testing.T) {
	t.Parallel()
	// seedBarModel sets unit=tokens, 5000 input tokens/bucket -> "5k" per bar.
	m, c := seedBarModel(t, 2 /* 24h */, 10, 24*time.Hour)
	defer c.Close()
	got := ansi.Strip(m.viewport.View())
	if !strings.Contains(got, "5k") {
		t.Errorf("24h viewport missing in-bar token label %q:\n%s", "5k", got)
	}
}
