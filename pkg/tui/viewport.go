package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// earliestRemainingSampleAt returns the earliest UtilizationPoint
// timestamp from pts5h and pts7d. Returns the zero time.Time if both
// slices are empty. Used by setX to clamp the remaining-mode viewport
// to the in-range left edge — the user cannot pan earlier than the
// first usage_samples row, which is what they would see as blank
// canvas otherwise.
func earliestRemainingSampleAt(pts5h, pts7d []cache.UtilizationPoint) time.Time {
	var earliest time.Time
	if len(pts5h) > 0 {
		earliest = pts5h[0].At
	}
	if len(pts7d) > 0 && (earliest.IsZero() || pts7d[0].At.Before(earliest)) {
		earliest = pts7d[0].At
	}
	return earliest
}

// setX is the single point of entry for changing the viewport's horizontal
// scroll position. n is a bucket index (not a column count); setX clamps
// it then multiplies by the per-bar stride (BarWidth+BarGap, defensively
// clamped to ≥1) when delegating to viewport.SetXOffset (column-indexed).
// The shadow viewportXOffset stays in bucket-index space.
//
// Clamp is mode-aware:
//
//   - Bar modes (tokens/cost): clamp against len(lastStarts) -
//     visibleBuckets(). Preserves the existing setX semantics that
//     renderSpringFrame's slack-handling computeSpringSlice was tuned
//     against. The bucket-aligned canvas guarantees lastStarts and
//     visibleBuckets line up.
//
//   - Remaining mode: ceil-divide the column gap by stride. lastStarts
//     in remaining mode is sparse usage_samples (not bucket-aligned),
//     so the bar-mode clamp would collapse to 0 the moment sample
//     count drops below visibleBuckets. Ceil-division of
//     (lastCanvasW - viewport.Width) / stride gives the smallest
//     bucket index whose stride×idx position reaches the canvas
//     right edge — the same value bar mode produces in production
//     (where viewport.Width == chartWidth). Previously this branch
//     floor-divided, which lost up to stride-1 cols of slack and
//     produced maxX one bucket short of bar mode at 24h zoom
//     (BarGap=2). That left the user one stride away from the
//     canvas right edge and turned every u-cycle through remaining
//     mode into a permanent off-by-one shift (#206).
//
//     Remaining mode also enforces a LOWER bound at the earliest
//     usage_samples timestamp (per #200): the user cannot pan earlier
//     than the first sample, since the canvas left of it shows blank
//     line with axis labels but no data. The bound is derived from
//     m.lastPts5h[0].At / m.lastPts7d[0].At via timeToColumn against
//     the active canvas span. When both slices are empty (fresh install
//     with no API fetch yet) the lower bound stays 0 and the existing
//     canvas-clamp behaviour applies — no panic, no spurious snap.
//
//     Mode-switch auto-snap (also #200) falls out for free: when the
//     user presses `u` to enter remaining mode while scrolled out of
//     range, refreshChart's anchor-restore block calls setX with the
//     pre-switch anchor's column; this clamp pulls it up to the
//     earliest in-range bucket before the spring animation samples
//     m.springXOffset.
func (m *Model) setX(n int) {
	stride := ZoomLevels[m.zoomIdx].stride()
	var minX, maxX int
	if chartUnit(m.unitIdx) == chartUnitRemaining {
		// Ceil-divide the column gap so maxX*stride reaches the canvas right
		// edge (the previous floor lost up to stride-1 cols of slack — #206).
		gap := max(0, m.lastCanvasW-m.viewport.Width)
		maxX = (gap + stride - 1) / stride
		if earliest := earliestRemainingSampleAt(m.lastPts5h, m.lastPts7d); !earliest.IsZero() &&
			!m.lastChartFrom.IsZero() && m.lastChartTo.After(m.lastChartFrom) {
			minX = min(timeToColumn(earliest, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)/stride, maxX)
		}
	} else {
		maxX = max(0, len(m.lastStarts)-m.visibleBuckets())
	}
	if m.underfilled {
		// #300: sparse data is locked to the flush-right offset so ←/→ are
		// inert and the right edge stays "now". maxX is the pinned offset in
		// both modes; collapsing minX onto it makes the clamp a single point.
		minX = maxX
	}
	n = min(max(n, minX), maxX)
	m.viewport.SetXOffset(n * stride)
	m.viewportXOffset = n
}

// scrollLeft / scrollRight shift the bucket-indexed viewport offset and, in
// bar mode, re-render the visible window live (#255 — no debounce; the
// rebuild is now ~viewport width). renderWindow no-ops in remaining mode, so
// line-mode scroll stays a pure offset over the full canvas. The
// !springActive guard ports the old rescaleMsg gate: a scroll mid-animation
// still advances the viewportXOffset shadow (so the post-settle refreshChart
// picks up the new position) but must not recompute m.peak — the spring owns
// it as the bar-height normalization base.
func (m *Model) scrollLeft(n int) {
	m.setX(m.viewportXOffset - n)
	if !m.springActive {
		m.renderWindow()
	}
}

func (m *Model) scrollRight(n int) {
	m.setX(m.viewportXOffset + n)
	if !m.springActive {
		m.renderWindow()
	}
}

// viewportAnchor captures the pre-rebuild horizontal scroll position so
// refreshChart can restore it against the freshly rebuilt canvas width.
type viewportAnchor struct {
	at     time.Time
	had    bool
	pinned bool
}

// snapshotAnchor captures the viewport's scroll anchor BEFORE the rebuild
// overwrites lastCanvasW / lastChartFrom / lastChartTo. had=false on first
// load or after an empty-cache reset; pinned=true when the viewport was at
// the right edge (the restore then re-pins to the new right edge).
func (m *Model) snapshotAnchor() viewportAnchor {
	var a viewportAnchor
	if m.lastCanvasW > 0 && m.lastZoomStride > 0 && !m.lastChartFrom.IsZero() && m.lastChartTo.After(m.lastChartFrom) {
		prevColOffset := m.viewportXOffset * m.lastZoomStride
		prevMaxCol := max(0, m.lastCanvasW-m.viewport.Width)
		a.pinned = prevColOffset >= prevMaxCol
		if !a.pinned {
			a.at = columnToTime(prevColOffset, m.lastCanvasW, m.lastChartFrom, m.lastChartTo)
			a.had = true
		}
	}
	return a
}

// restoreAnchor restores the scroll position captured by snapshotAnchor
// against the rebuilt canvas. !had or pinned re-pins to the right edge;
// otherwise it maps the anchor time back to a column. Routes through setX so
// the viewport offset and the viewportXOffset shadow stay in sync.
func (m *Model) restoreAnchor(a viewportAnchor, zoom ZoomLevel, canvasW int, from, to time.Time) {
	stride := zoom.stride()
	rightEdgeCol := max(0, canvasW-m.viewport.Width)
	switch {
	case !a.had, a.pinned:
		bucketCount := (canvasW + max(zoom.BarGap, 0)) / stride
		m.setX(bucketCount)
	default:
		targetCol := min(timeToColumn(a.at, canvasW, from, to), rightEdgeCol)
		m.setX(targetCol / stride)
	}
}

// chartWidth returns the available width for the viewport. Floors at 10
// so the ntcharts canvas never collapses to a degenerate width.
func (m Model) chartWidth() int {
	w := m.w - 2
	if w < 10 {
		return 10
	}
	return w
}

// visibleBuckets returns how many whole bars fit in the viewport at the
// active zoom's BarWidth+BarGap layout. Bucket-indexed throughout: 1
// unit = 1 bar. Derived from: n bars fit iff n*BarWidth + (n-1)*BarGap
// <= chartWidth(), so n <= (chartWidth + BarGap) / (BarWidth + BarGap).
// Floors at 1 so the chart never collapses to zero visible bars when
// BarWidth > chartWidth() (degenerate terminal width).
//
// BarWidth is clamped to ≥1 and BarGap to ≥0 so stride is always ≥1 —
// guards against a stride-zero divide panic if a future ZoomLevels
// literal sets BarGap = -BarWidth.
func (m Model) visibleBuckets() int {
	z := ZoomLevels[m.zoomIdx]
	stride := z.stride()
	gap := max(z.BarGap, 0)
	v := (m.chartWidth() + gap) / stride
	if v < 1 {
		return 1
	}
	return v
}

// projectsMaxRows caps the projects box outer height so it never crowds the
// chart on tall terminals.
const projectsMaxRows = 12

// projectsHeight returns the OUTER height (incl. border) reserved for the
// projects box. While a slide is in flight it returns the animated value so the
// chart cedes/reclaims rows in lockstep (#416); otherwise 0 when hidden, else
// the steady target (content-aware since #420 — see projectsTargetHeight).
func (m Model) projectsHeight() int {
	if m.springActive && m.springKind == springKindProjects {
		return m.projectsAnimH
	}
	if !m.showProjects {
		return 0
	}
	return m.projectsTargetHeight()
}

// projectsTargetHeight is the steady-state outer box height: the rows its
// content actually needs — border (2) + title (1) + ceil(len(projectAggs)/
// cols) body rows at the current column packing — capped at half the
// post-header area and projectsMaxRows, and 0 when the terminal is too short
// to host both the chart's 5-row floor and a usable box. Content-aware since
// #420: the box shrinks to its aggregates and chartHeight reclaims the rows.
// Heights depend on aggs but never the reverse (refreshProjects derives its
// window from width/scroll state), so there is no cycle; every projectAggs
// mutation re-syncs the viewport in one pass (refreshChart inline before its
// paint, the debounced settle via applyProjectsResize, clearChart
// self-contained). The #416 slide arms against this target, so the arm must
// populate projectAggs BEFORE reading it (beginProjectsAnimation).
func (m Model) projectsTargetHeight() int {
	avail := m.h - 7 // shared by chart + projects box (same overhead as chartHeight)
	// Need the chart's 5-row floor + a minimum 4-row box (border+title+1 row).
	if avail < 5+4 {
		return 0
	}
	upper := min(avail/2, projectsMaxRows)
	needed := 4 // empty aggs: the centered placeholder keeps the 4-row floor
	if n := len(m.projectAggs); n > 0 {
		cols := projectCellCols(m.w)
		needed = 3 + (n+cols-1)/cols // border(2) + title(1) + body rows
	}
	return min(needed, upper)
}

// chartHeight returns the available rows for the chart, leaving room for
// the bordered header box (4 rows: top border, bars row, burn-rate row,
// bottom border), two separators (2 rows), the help footer (1 row), and the
// projects box (projectsHeight, 0 when the terminal is too short). Total
// non-body overhead = 7 rows + the projects box.
func (m Model) chartHeight() int {
	h := m.h - 7 - m.projectsHeight()
	if h < 5 {
		return 5
	}
	return h
}

// progressWidth returns the rendered width of each of the two quota bars,
// which sit side-by-side inside the header box. Per-side fixed chrome
// (matched across both sides for symmetry — the prerequisite for centring
// the │ divider exactly):
//   - 3 cols dim label prefix ("5h " or "7d ")
//   - barTimeGap cols bar→time margin (1)
//   - statusBlockMaxW cols right-aligned time slot ("23h 59m" 7d worst
//     case; the 5h "4h 59m" fits in 6 and gets 1 col of leading pad to
//     stay symmetric)
//
// The chrome budget is derived from statusBlockMaxW and barTimeGap rather
// than baked into a literal so it stays in lockstep with renderQuotaSide:
// #316 widened the slot 6→7 but a hardcoded constant here did not, so each
// bar was sized 1 col too wide and the header row overflowed the box and
// wrapped. The header box reserves 4 cols (border + padding) and a 3-col
// " │ " divider sits between the two halves.
//
// At odd parities the integer division leaves a 1-col residual that
// lipgloss absorbs as a trailing pad inside the box. Doesn't affect
// divider centring because the divider is positioned relative to the
// symmetric chrome, not derived from total width.
func (m Model) progressWidth() int {
	const labelW = 3 // lipgloss.Width("5h ") == lipgloss.Width("7d ")
	perSide := labelW + lipgloss.Width(barTimeGap) + statusBlockMaxW
	chrome := 4 + 3 + 2*perSide // 4 = border+padding, 3 = " │ " divider
	w := (m.w - chrome) / 2
	if w < minBarWidth {
		return minBarWidth
	}
	return w
}
