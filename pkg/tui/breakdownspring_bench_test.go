package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// seedRemainingModelWith30dMessages builds a remaining-mode model with ~30
// days of 15m-spaced message history (2880 buckets) so that
// EarliestMessageTime widens the canvas to ~2880 cols — the realistic
// worst-case that the line-mode windowing optimisation must stay inside the
// 60fps budget for. Usage samples are also seeded so hasData is true and
// lastPts5h/lastPts7d are populated. Composes seedModelAt (messages) and
// the usage-sample insertion pattern from seedRemainingModelWithSamples.
func seedRemainingModelWith30dMessages(b *testing.B, now time.Time) (Model, *cache.Cache) {
	b.Helper()
	// 2880 = 30 days × 96 buckets/day at 15m zoom — wide enough to force a
	// realistic-history canvas. seedModelAt spaces messages 15m apart.
	m, c := seedModelAt(b, int(chartUnitRemaining), 2880, now)
	// Add 60 usage samples so the line chart has real utilisation data.
	for i := range 60 {
		when := now.Add(-time.Duration(i) * 15 * time.Minute)
		resets := when.Add(2 * time.Hour)
		u := anthro.Usage{
			FiveHour: &anthro.Bucket{Utilization: 20.0 + float64(i)*2.0, ResetsAt: &resets},
		}
		if err := c.RecordUsageSample(b.Context(), u, when); err != nil {
			b.Fatalf("RecordUsageSample: %v", err)
		}
	}
	m.refreshChart()
	return m, c
}

// BenchmarkBreakdownAnimFrame measures one slide-frame render — the work a
// single springTickMsg does besides the O(1) spring step — in both chart
// modes (round-one finding ccpulse-416.3: line mode was unmeasured). The
// per-frame budget is 16.7ms (60fps); renderWindow's own docs put the bar
// path at ~5ms at viewport width. Heights alternate mid-slide values so the
// rebuild cost reflects varying-height frames, not a memoized best case.
//
// renderBreakdownFrame paints the CHART only — the box beneath it
// (renderBreakdownBox) runs on the same tick inside View() (model.go:876)
// and is covered separately by the "box" sub-benchmark below, and the
// leg-1-settle tick of a sequential swap (which pays an aggregate query
// AND a synchronous render in one call) is covered by "swap"
// (ccpulse-475.16).
func BenchmarkBreakdownAnimFrame(b *testing.B) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	b.Run("bar", func(b *testing.B) {
		// breakdownModels, not breakdownProjects: renderBreakdownFrame reads
		// only chart state (height, chart data), never m.breakdown/rows, so
		// it is genuinely kind-independent — this sub-benchmark pins that by
		// exercising the other kind while "line"/"line_30d" stay on
		// breakdownProjects.
		m, c := seedBarModelWithMessages(b, int(chartUnitCost), now)
		defer c.Close()
		m.breakdown = breakdownNone
		m.refreshChart()
		m.beginBreakdownAnimation(breakdownModels) // show: arm + 1 aggs query (outside the loop)
		target := m.breakdownTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.breakdownAnimH = 1 + (i % max(target, 2)) // sweep mid-slide heights
			m.renderBreakdownFrame()
			i++
		}
	})
	b.Run("line", func(b *testing.B) {
		m, c := seedRemainingModelWithSamples(b, 60, now)
		defer c.Close()
		m.breakdown = breakdownNone
		m.refreshChart()
		m.beginBreakdownAnimation(breakdownProjects)
		target := m.breakdownTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.breakdownAnimH = 1 + (i % max(target, 2))
			m.renderBreakdownFrame()
			i++
		}
	})
	b.Run("line_30d", func(b *testing.B) {
		m, c := seedRemainingModelWith30dMessages(b, now)
		defer c.Close()
		// Guard: fixture must have a realistic-history canvas width.
		if m.lastCanvasW < 2000 {
			b.Fatalf("fixture canvas %d, want realistic-history width (>=2000)", m.lastCanvasW)
		}
		m.breakdown = breakdownNone
		m.refreshChart()
		m.beginBreakdownAnimation(breakdownProjects)
		target := m.breakdownTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.breakdownAnimH = 1 + (i % max(target, 2))
			m.renderBreakdownFrame()
			i++
		}
	})
	// box measures renderBreakdownBox — the box render that runs on the SAME
	// tick as renderBreakdownFrame inside View() (model.go:876) but was
	// previously unbenchmarked, undercounting true per-tick cost by ~13%
	// against the 16.7ms/60fps budget (measured 0.475ms at w=120 / 0.643ms
	// at w=200, 102KB and 2064 allocs — ccpulse-475.16). Heights sweep
	// mid-slide exactly like the chart sub-benchmarks above, against real
	// seeded rows so the column-packing/overflow math runs against
	// realistic content rather than an empty-placeholder shortcut.
	b.Run("box", func(b *testing.B) {
		m, c := seedBarModelWithMessages(b, int(chartUnitCost), now)
		defer c.Close()
		m.breakdown = breakdownNone
		m.refreshChart()
		m.beginBreakdownAnimation(breakdownProjects) // arm + 1 aggs query (outside the loop)
		target := m.breakdownTargetHeight()
		title := breakdownTitle(m.breakdownRowsKind)
		rows := m.breakdownRows
		width := m.w
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			h := 1 + (i % max(target, 2)) // sweep mid-slide heights
			sinkString = renderBreakdownBox(title, rows, width, h)
			i++
		}
	})
	// swap measures the leg-1-settle tick of a sequential swap
	// (breakdownspring.go:99-111) — the one tick shape #475 genuinely adds
	// on top of the single-phase show/hide slide: on the frame where leg
	// one's spring crosses the settle threshold, handleBreakdownSpringTick
	// arms leg two (which pays the ONE aggregate query for the incoming
	// kind) and then paints leg two's frame 0 synchronously, all inside a
	// single tick handler call. Every loop iteration replays that exact
	// tick from a snapshot captured once (outside the timed loop) by
	// driving a real show-then-swap sequence up to the frame immediately
	// before the chaining call.
	b.Run("swap", func(b *testing.B) {
		m, c := seedBarModelWithMessages(b, int(chartUnitCost), now)
		defer c.Close()
		m.breakdown = breakdownNone
		m.refreshChart()
		m.beginBreakdownAnimation(breakdownProjects) // show: arm + 1 aggs query
		for i := 0; ; i++ {
			if i >= 600 {
				b.Fatal("initial show did not settle within 600 frames")
			}
			if m.handleBreakdownSpringTick(m.springGen) == nil {
				break
			}
		}
		if m.handleBreakdownKey(breakdownModels) == nil {
			b.Fatal("swap keypress did not arm leg 1")
		}
		var presettle Model
		found := false
		for i := 0; i < 600 && !found; i++ {
			snap := m
			cmd := m.handleBreakdownSpringTick(m.springGen)
			if m.breakdown == breakdownModels {
				// This call chained into leg 2 — snap is the model exactly
				// as it stood the instant BEFORE the settle+chain call.
				presettle = snap
				found = true
				break
			}
			if cmd == nil {
				b.Fatal("leg 1 settled without chaining leg 2")
			}
		}
		if !found {
			b.Fatal("leg 1 did not reach its settle tick within 600 frames")
		}
		b.ReportAllocs()
		for b.Loop() {
			m = presettle
			m.handleBreakdownSpringTick(m.springGen)
		}
	})
}
