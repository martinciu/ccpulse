package tui

import (
	"strconv"
	"testing"
	"time"
)

// benchModelForProjects builds a bar model armed mid-slide at the given viewport
// width, so BenchmarkProjectsAnimFrame measures only the in-memory per-frame
// render (rasterize + drawSkyline + label row). The DB-touching setup
// (seedBarModelWithMessages → refreshChart, armProjectsShowForTest →
// refreshProjects) runs once here, outside b.Loop().
func benchModelForProjects(b *testing.B, vpWidth int, now time.Time) Model {
	b.Helper()
	m, c := seedBarModelWithMessages(b, int(chartUnitCost), now)
	b.Cleanup(func() { _ = c.Close() })
	// Override geometry to the bench width and rebuild the chart inputs against
	// it before arming, so the snapshot's vpWidth matches.
	m.w = vpWidth + 2
	m.viewport.Width = vpWidth
	m.refreshChart()
	armProjectsShowForTest(b, &m)
	m.projectsAnimH = m.projectsSnap.targetH / 2 // mid-slide
	return m
}

// BenchmarkProjectsAnimFrame times one full per-frame slide render (rasterize +
// drawSkyline + label row + band slice) at representative chart widths and a
// mid-slide height, confirming the redraw stays within the 60fps (16.7ms)
// budget. Mirrors the BenchmarkBarChartRender width sweep from CLAUDE.md.
func BenchmarkProjectsAnimFrame(b *testing.B) {
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	for _, w := range []int{100, 1000, 5000} {
		b.Run(strconv.Itoa(w), func(b *testing.B) {
			m := benchModelForProjects(b, w, now)
			for b.Loop() {
				m.renderProjectsAnimFrame()
				_ = projectsBandRows(m.projectsSnap.boxRows, m.w, m.projectsAnimH)
			}
		})
	}
}
