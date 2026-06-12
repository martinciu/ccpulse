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

// BenchmarkProjectsAnimFrame measures one slide-frame render — the work a
// single springTickMsg does besides the O(1) spring step — in both chart
// modes (round-one finding ccpulse-416.3: line mode was unmeasured). The
// per-frame budget is 16.7ms (60fps); renderWindow's own docs put the bar
// path at ~5ms at viewport width. Heights alternate mid-slide values so the
// rebuild cost reflects varying-height frames, not a memoized best case.
func BenchmarkProjectsAnimFrame(b *testing.B) {
	now := time.Date(2026, 6, 10, 12, 0, 0, 0, time.UTC)
	b.Run("bar", func(b *testing.B) {
		m, c := seedBarModelWithMessages(b, int(chartUnitCost), now)
		defer c.Close()
		m.showProjects = false
		m.refreshChart()
		m.beginProjectsAnimation() // show: arm + 1 aggs query (outside the loop)
		target := m.projectsTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.projectsAnimH = 1 + (i % max(target, 2)) // sweep mid-slide heights
			m.renderProjectsFrame()
			i++
		}
	})
	b.Run("line", func(b *testing.B) {
		m, c := seedRemainingModelWithSamples(b, 60, now)
		defer c.Close()
		m.showProjects = false
		m.refreshChart()
		m.beginProjectsAnimation()
		target := m.projectsTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.projectsAnimH = 1 + (i % max(target, 2))
			m.renderProjectsFrame()
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
		m.showProjects = false
		m.refreshChart()
		m.beginProjectsAnimation()
		target := m.projectsTargetHeight()
		b.ReportAllocs()
		i := 0
		for b.Loop() {
			m.projectsAnimH = 1 + (i % max(target, 2))
			m.renderProjectsFrame()
			i++
		}
	})
}
