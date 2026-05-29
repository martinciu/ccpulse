// Package tui — zoom-squeeze animation (issue #373).
//
// This file holds the single-phase horizontal-squeeze animation for the 'z'
// zoom key in remaining (line) mode. It is a sibling of the two-phase
// unit-toggle machine in springs.go: it reuses the master springActive flag
// and the shared springGen counter (the two animations are mutually exclusive
// — refreshChart aborts any in-flight animation), and is disambiguated by
// Model.springKind.
//
// Bar-mode zoom (cost/tokens) is out of scope and keeps its hard-cut; see the
// spec's deferred-bar follow-up.
package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// zoomAnimSnapshot captures, once at arm time, everything the per-frame tick
// needs so nothing it reads can shift mid-animation: the OLD on-screen visible
// window (oFrom/oTo), the NEW window (nFrom/nTo), the utilization points, and a
// frozen `now` that keeps the right edge stable across frames.
type zoomAnimSnapshot struct {
	oFrom, oTo time.Time
	nFrom, nTo time.Time
	pts5h      []cache.UtilizationPoint
	pts7d      []cache.UtilizationPoint
	now        time.Time
}

// lerpTime returns the linear interpolation between a and b at parameter r,
// clamped to [a, b] for r outside [0, 1]. Used to ease the visible time window
// from the old zoom's window to the new zoom's across the squeeze.
func lerpTime(a, b time.Time, r float64) time.Time {
	if r <= 0 {
		return a
	}
	if r >= 1 {
		return b
	}
	span := b.Sub(a)
	return a.Add(time.Duration(float64(span) * r))
}

// handleZoomSpringTick advances one frame of the zoom squeeze. Real
// interpolation lands in Task 4; this minimal form settles immediately so the
// dispatch wiring in Task 1 stays green and inert until Task 3 arms it.
func (m *Model) handleZoomSpringTick(gen int) tea.Cmd {
	m.springActive = false
	m.springKind = springKindNone
	return nil
}
