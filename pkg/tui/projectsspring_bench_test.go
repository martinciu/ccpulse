package tui

import (
	"testing"
	"time"
)

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
}
