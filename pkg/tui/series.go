package tui

import (
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// chartSeries carries the per-unit data loaded for one refreshChart pass:
// the bar/line values, their bucket-start times, the y-axis peak (set only
// for remaining mode; bar modes recompute it in renderWindow), and which
// unit produced them.
type chartSeries struct {
	values []float64
	starts []time.Time
	peak   float64
	unit   chartUnit
}

// loadSeries loads the active unit's series for the [from, to] window.
// Returns ok=false when the unit's query fails or yields no data; the caller
// then resets via clearChart. loadRemainingSeries owns the lastPts5h/7d and
// hasData side effects (its existing behavior).
func (m *Model) loadSeries(zoom ZoomLevel, from, to, earliest time.Time) (chartSeries, bool) {
	switch chartUnit(m.unitIdx) {
	case chartUnitCost:
		return m.loadCostSeries(zoom, from, to, earliest)
	case chartUnitRemaining:
		return m.loadRemainingSeries(from)
	default:
		return m.loadTokenSeries(zoom, from, to, earliest)
	}
}

func (m *Model) loadCostSeries(zoom ZoomLevel, from, to, earliest time.Time) (chartSeries, bool) {
	buckets, err := m.chartCache.cost.resolve(m.ctx, zoom.Duration, from, to, earliest, m.deps.Cache.CostBuckets)
	if err != nil || len(buckets) == 0 {
		return chartSeries{}, false
	}
	values := make([]float64, len(buckets))
	starts := make([]time.Time, len(buckets))
	for i, b := range buckets {
		values[i] = b.Cost
		starts[i] = b.BucketStart
	}
	return chartSeries{values: values, starts: starts, unit: chartUnitCost}, true
}

func (m *Model) loadRemainingSeries(from time.Time) (chartSeries, bool) {
	pts5h, err5h := m.deps.Cache.UtilizationSince(m.ctx, "five_hour_pct", from)
	pts7d, err7d := m.deps.Cache.UtilizationSince(m.ctx, "seven_day_pct", from)
	if err5h != nil && err7d != nil {
		// Both queries failed: nil the cached points (the reset extra this
		// path owns), then signal failure — the caller runs clearChart.
		m.lastPts5h = nil
		m.lastPts7d = nil
		return chartSeries{}, false
	}
	if err5h != nil {
		pts5h = nil
	}
	if err7d != nil {
		pts7d = nil
	}
	m.lastPts5h = pts5h
	m.lastPts7d = pts7d
	// #300: remaining mode has real data only when usage samples exist; the
	// padded axis (no samples) is a warming-up state, not data.
	m.hasData = len(pts5h) > 0 || len(pts7d) > 0
	anchor := pts5h
	if len(anchor) == 0 {
		anchor = pts7d
	}
	values := make([]float64, len(anchor))
	starts := make([]time.Time, len(anchor))
	for i, p := range anchor {
		values[i] = max(0, 1.0-p.Pct/100.0)
		starts[i] = p.At
	}
	return chartSeries{values: values, starts: starts, peak: 1.0, unit: chartUnitRemaining}, true
}

func (m *Model) loadTokenSeries(zoom ZoomLevel, from, to, earliest time.Time) (chartSeries, bool) {
	buckets, err := m.chartCache.tokens.resolve(m.ctx, zoom.Duration, from, to, earliest, m.deps.Cache.IOTokenBuckets)
	if err != nil || len(buckets) == 0 {
		return chartSeries{}, false
	}
	values := make([]float64, len(buckets))
	starts := make([]time.Time, len(buckets))
	for i, b := range buckets {
		values[i] = float64(b.Tokens)
		starts[i] = b.BucketStart
	}
	return chartSeries{values: values, starts: starts, unit: chartUnitTokens}, true
}

// clearChart resets the chart to the empty-cache placeholder and zeroes the
// cached canvas state. It resets only the fields every reset path shares;
// call-site-specific extras (underfilled at the EarliestMessageTime branch,
// lastPts5h/7d in loadRemainingSeries) stay at their sites so behavior is
// preserved exactly.
func (m *Model) clearChart() {
	m.viewport.SetContent(emptyPlaceholder(m.chartWidth(), m.chartHeight()))
	m.lastValues = nil
	m.lastStarts = nil
	m.peak = 0
	m.lastCanvasW = 0
	m.lastZoomStride = 0
	m.lastChartFrom = time.Time{}
	m.lastChartTo = time.Time{}
	m.hasData = false
	m.chartCache = chartCache{}
	m.projectAggs = nil
	m.setX(0)
}

// refreshChart queries the cache and updates the viewport content.
// Safe to call when deps.Cache is nil (no-op). Loads the full history
// present in the cache, from the earliest message up to "now". On a
// DB error renders a placeholder; an empty cache renders the padded
// warming-up axis (#300).
func (m *Model) refreshChart() {
	if m.deps.Cache == nil {
		return
	}
	// If a unit-toggle spring is still in flight, hard-cut it: refresh
	// paths (watcher RefreshMsg, WindowSizeMsg, Zoom) bypass the
	// animation per the spec — only the initial 'u' press animates.
	// No need to snap springRatios to targets here: the rest of
	// refreshChart overwrites lastValues/lastStarts/peak and rebuilds
	// the viewport from cache; nothing reads springRatios while
	// springActive is false.
	if m.springActive {
		m.springActive = false
		m.springIntro = false
		m.springPhase = springIdle
		m.springKind = springKindNone
		// springProjectiles, springFinalTargets, oldPeak, oldUnitIdx
		// remain populated but unread — guarded by springActive=false.
		// Next beginUnitAnimation re-makes the slices. Zoom scalars
		// (zoomSpringR/Vel/zoomSnap) likewise stay set but unread (#373).
		// The projects slide (#416) rides this same abort: springActive=false +
		// springKind=springKindNone drops it; projectsAnimH/projectsSnap stay set
		// but unread, and showProjects was already committed at arm so the chart
		// rebuild below reads the correct chartHeight.
	}

	// Snapshot the wall-clock scroll anchor BEFORE the rebuild overwrites
	// lastCanvasW / lastChartFrom / lastChartTo. snapshotAnchor handles the
	// first-load and pinned-to-right-edge cases.
	anchor := m.snapshotAnchor()

	zoom := ZoomLevels[m.zoomIdx]
	// Right edge = the END of the bucket containing now (#311: same instant
	// the live-advance tick is scheduled to fire at).
	to := nextBoundary(m.now(), zoom)

	earliest, ok, err := m.deps.Cache.EarliestMessageTime(m.ctx)
	if err != nil {
		// Genuine DB read failure: keep the placeholder. (An empty cache —
		// ok == false, err == nil — falls through to the padded-window path
		// below, so the warming-up state shows a full-width axis instead, #300.)
		// underfilled is set explicitly here (clearChart leaves it untouched):
		// this branch runs before the dataBuckets-based assignment below.
		m.clearChart()
		m.underfilled = false
		return
	}

	// #300: the window must always span at least the viewport so sparse data
	// renders flush-right with a full-width x-axis. minFrom is `to` walked back
	// visibleBuckets()+1 buckets; the extra bucket guarantees canvasW >
	// viewport.Width at every zoom, so the pin-right path lands flush via
	// computeSpringSlice's leading-partial bucket (24h would otherwise leave a
	// stride-1 right gap at start==0).
	minFrom := paddedFrom(to, zoom, m.visibleBuckets()+1)

	var from time.Time
	var dataBuckets int
	if !ok {
		// Empty cache: synthesize the full window so the chart shows the empty
		// axis filling in from the right instead of the placeholder (#300).
		from = minFrom
	} else {
		if zoom.Duration == 24*time.Hour {
			from = cache.DayStartLocal(earliest)
		} else {
			from = cache.BucketAlign(earliest, zoom.Duration)
		}
		dataBuckets = bucketCountInRange(from, to, zoom.Duration)
		if minFrom.Before(from) {
			from = minFrom
		}
	}
	// Underfilled iff the real data does not reach across the viewport. At
	// exactly visibleBuckets() the canvas is still <= chartWidth(), so we pad
	// (and lock, Task 3) for a flush right edge at 24h; at visibleBuckets()+1
	// and above the canvas exceeds the viewport and normal scroll applies.
	m.underfilled = dataBuckets <= m.visibleBuckets()

	// #300: real data exists for the bar units iff the cache has messages (ok);
	// the zero-fill that paints the padded axis must not read as data. The
	// remaining-mode branch overrides this with whether usage samples exist.
	m.hasData = ok

	series, loaded := m.loadSeries(zoom, from, to, earliest)
	if !loaded {
		m.clearChart()
		return
	}
	m.lastValues = series.values
	m.lastStarts = series.starts

	chartH := m.chartHeight()
	var canvasW int
	if series.unit == chartUnitRemaining {
		// Mirror bar mode's canvas-width formula so 'z' zoom and 'u'
		// unit-toggle preserve the same time-range under the viewport's
		// left edge in both modes. Floor at chartWidth() so a short
		// usage_samples history still spans the visible area instead
		// of rendering in a narrow slice on the left.
		canvasW = max(zoom.CanvasWidth(bucketCountInRange(from, to, zoom.Duration)), m.chartWidth())
	} else {
		canvasW = zoom.CanvasWidth(len(series.values))
	}
	m.lastChartFrom = from
	m.lastChartTo = to
	m.lastCanvasW = canvasW
	m.lastZoomStride = zoom.stride()

	// Restore the scroll anchor against the rebuilt canvas BEFORE peak is
	// computed, so the post-anchor viewportXOffset drives the visible-slice
	// peak (#230). restoreAnchor routes through setX so the viewport offset and
	// the viewportXOffset shadow stay in sync — the invariant that all scroll
	// mutations go through setX / scrollLeft / scrollRight.
	m.restoreAnchor(anchor, zoom, canvasW, from, to)

	// Paint (#255). Bar modes window the render to the visible slice via
	// renderWindow, which computes the visible-slice peak and sets the
	// viewport content + slack offset itself — so no separate peak calc and
	// no re-apply setX are needed (renderWindow's SetXOffset against its own
	// windowed canvas is what restores the right edge; the old full-canvas
	// re-apply dance is gone). Remaining mode keeps the full-canvas line
	// chart plus the re-apply setX to restore the offset against the new
	// wide canvas after a bar→line spring left narrow content behind.
	if series.unit == chartUnitRemaining {
		m.peak = series.peak // 1.0, set in loadRemainingSeries
		m.viewport.SetContent(buildLineChart(m.lastPts5h, m.lastPts7d, from, to, canvasW, chartH, m.now(), zoom, m.dateOrder, "refresh", ""))
		m.setX(m.viewportXOffset)
	} else {
		m.renderWindow()
	}

	// Recompute the per-project rollup for the freshly-set visible window
	// (lastStarts/viewportXOffset/lastChartTo are all current here).
	m.refreshProjects()
}

// renderWindow renders the bar-chart viewport from the visible window of
// m.lastValues / m.lastStarts (#255). It is the steady-state twin of
// renderSpringFrame's bar branch: slice to the visible buckets plus one
// leading-slack bucket via computeSpringSlice, compute the dynamic-y peak
// from the on-screen window (peakOfVisibleSlice), buildChart at ~viewport
// width, and apply the small slack SetXOffset. Where rebuildAtVisiblePeak
// rebuilt the full canvas (m.lastCanvasW ≈ 3090 cols), this builds ~viewport
// width — dropping the per-scroll rebuild from ~80-130ms to ~5ms.
//
// No-op when lastValues is empty, lastCanvasW is 0 (pre-init), or the active
// unit is chartUnitRemaining (the line chart keeps a fixed peak=1.0 and a
// full-canvas pure-offset scroll — bar-only per #255 scope).
//
// Runs live per scroll keypress now that #255 dropped the #252 scroll-stop
// debounce — each call allocates ~2.4 MB / ~10k allocs (mostly inside
// ntcharts), a deliberate GC-pressure-for-responsiveness trade that the
// windowing keeps well under the per-frame budget.
func (m *Model) renderWindow() {
	if len(m.lastValues) == 0 || m.lastCanvasW == 0 {
		return
	}
	unit := chartUnit(m.unitIdx)
	if unit == chartUnitRemaining {
		return
	}
	zoom := ZoomLevels[m.zoomIdx]
	nv := m.visibleBuckets()

	start := max(m.viewportXOffset, 0)
	end := min(start+nv, len(m.lastValues), len(m.lastStarts))
	if start >= end {
		return
	}

	stride := zoom.stride()
	// Flush-right (#306): include a partial leading bucket and offset by
	// stride-slack so the right edge stays flush at every scroll position.
	sliceStart, xOff := computeSpringSlice(start, m.viewport.Width, stride, max(zoom.BarGap, 0))

	// Peak is the on-screen window only; the leading-slack bucket is excluded
	// and clips to full height via buildChart's WithNoAutoMaxValue — exactly
	// as the full-canvas path did.
	peak := peakOfVisibleSlice(m.lastValues, start, nv)
	m.peak = peak

	// sliceStart is start or start-1, so sliceStart <= start < end; the end
	// clamps above therefore keep [sliceStart:end] in bounds.
	chartH := m.chartHeight()
	barsH := chartH
	if chartH >= 6 {
		barsH = chartH - 1
	}
	canvasW := zoom.CanvasWidth(end - sliceStart)
	vals := m.lastValues[sliceStart:end]
	body := buildChart(vals, m.lastStarts[sliceStart:end],
		peak, canvasW, chartH, time.Now(), zoom, unit, m.dateOrder)

	// In-bar numbers, 24h steady state only (#308). renderSpringFrame does not
	// call this, so the labels are absent during animation.
	style := barLabelStyle(unit)
	texts := make([]string, len(vals))
	for i, v := range vals {
		if v > 0 {
			texts[i] = style.Render(formatBarValue(v, unit))
		}
	}
	body = overlayBarLabels(body, texts, barsH, canvasW, zoom)

	m.viewport.SetContent(body)
	m.viewport.SetXOffset(xOff)
}

// emptyPlaceholder returns a w×h block with "no Claude sessions yet"
// centered in colorMuted — the empty-cache state of the chart viewport.
func emptyPlaceholder(w, h int) string {
	if h < 1 {
		h = 1
	}
	if w < 1 {
		w = 1
	}
	msg := lipgloss.NewStyle().Foreground(colorMuted).Render("no Claude sessions yet")
	return lipgloss.Place(w, h, lipgloss.Center, lipgloss.Center, msg)
}

// peakOfVisibleSlice returns the maximum of values[xOff : xOff+visibleN],
// clamped to slice bounds. Returns 0 for an empty slice or non-positive
// width — niceCeilingFloat handles peak=0 by returning 0 and buildChart
// guards WithMaxValue against zero (chart.go:613-614).
//
// Used by both refreshChart's post-anchor peak computation and
// renderWindow's windowed steady-state scroll render to compute the
// dynamic y-axis peak from the currently-visible bucket window (#230, #255).
func peakOfVisibleSlice(values []float64, xOff, visibleN int) float64 {
	if len(values) == 0 || visibleN <= 0 {
		return 0
	}
	lo := max(0, xOff)
	hi := min(len(values), xOff+visibleN)
	var peak float64
	for i := lo; i < hi; i++ {
		if values[i] > peak {
			peak = values[i]
		}
	}
	return peak
}
