// Package tui — projects-box slide animation (issue #416).
//
// Sibling of the zoom-transition machine in zoomspring.go: it reuses the master
// springActive flag and the shared springGen counter (the unit/zoom/projects
// animations are mutually exclusive — refreshChart aborts any in-flight one) and
// is disambiguated by Model.springKind == springKindProjects.
//
// The crux (see spec): the CHART rescales (re-rasterized at the interpolated
// height each frame, bars shrink/grow into fewer/more rows) while the BOX slices
// (rendered once at target height at arm, bottom rows revealed each frame with a
// phantom top border). Both read only in-memory snapshot state — no DB, no
// ntcharts rebuild, per frame.
package tui

import (
	"math"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// projectsAnimSnapshot captures, once at arm time, everything the per-frame
// slide tick needs so nothing it reads can shift mid-animation.
type projectsAnimSnapshot struct {
	boxRows []string // box pre-rendered at the steady target height, split into rows (the SLICE source)
	startH  int      // slide start outer height (show: 0, hide: target)
	targetH int      // slide end   outer height (show: target, hide: 0)

	// Chart inputs — frozen so the per-frame re-rasterize/re-build reads no DB.
	values   []float64
	starts   []time.Time
	peak     float64
	pts5h    []cache.UtilizationPoint
	pts7d    []cache.UtilizationPoint
	unit     chartUnit
	isLine   bool
	vpWidth  int
	zoom     ZoomLevel
	viewFrom time.Time // line mode: stable visible window (no horizontal squeeze in this slide)
	viewTo   time.Time
}

// lerpInt linearly interpolates between integer heights a and b at parameter r,
// rounding to the nearest row. r is clamped to [0,1] by the caller's spring.
func lerpInt(a, b int, r float64) int {
	return int(math.Round(float64(a) + (float64(b)-float64(a))*r))
}
