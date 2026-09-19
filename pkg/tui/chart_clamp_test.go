package tui

import (
	"testing"
	"time"
)

// zoomByLabel is a lookup so these cases read in terms the UI uses ("15m")
// rather than a positional index into ZoomLevels.
func zoomByLabel(t *testing.T, label string) ZoomLevel {
	t.Helper()
	for _, z := range ZoomLevels {
		if z.Label == label {
			return z
		}
	}
	t.Fatalf("no ZoomLevel labelled %q", label)
	return ZoomLevel{}
}

// TestClampChartFrom is the regression guard for #527: a single cache row with
// a far-past timestamp used to size the chart canvas (and the dense bucket
// slices behind it) into the tens of millions, which allocated >10GB and hung
// the TUI before its first frame.
func TestClampChartFrom(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	// The zero time.Time, exactly as a poisoned cache row hands it back.
	yearOne := time.Time{}

	tests := []struct {
		name      string
		zoomLabel string
		from      time.Time
		want      time.Time
	}{
		{
			name:      "recent range is left alone",
			zoomLabel: "1h",
			from:      to.Add(-72 * time.Hour),
			want:      to.Add(-72 * time.Hour),
		},
		{
			// stride 1 at 1h, so the column budget is the bucket budget.
			name:      "exactly at the ceiling is left alone",
			zoomLabel: "1h",
			from:      to.Add(-maxChartColumns * time.Hour),
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			name:      "one bucket past the ceiling is clamped back to it",
			zoomLabel: "1h",
			from:      to.Add(-(maxChartColumns + 1) * time.Hour),
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			name:      "year one at 15m",
			zoomLabel: "15m",
			from:      yearOne,
			want:      to.Add(-maxChartColumns * 15 * time.Minute),
		},
		{
			name:      "year one at 1h",
			zoomLabel: "1h",
			from:      yearOne,
			want:      to.Add(-maxChartColumns * time.Hour),
		},
		{
			// 24h draws BarWidth 10 + BarGap 2, so its budget is
			// maxChartColumns/12 buckets — an order of magnitude fewer than the
			// 1-column-per-bucket zooms. A bucket-denominated ceiling would
			// have let this zoom build a ~240k-column canvas (#527).
			name:      "year one at 24h is budgeted by stride, not bucket count",
			zoomLabel: "24h",
			from:      yearOne,
			want:      to.Add(-time.Duration(maxChartColumns/12) * 24 * time.Hour),
		},
		{
			name:      "reversed range is left alone",
			zoomLabel: "1h",
			from:      to.Add(time.Hour),
			want:      to.Add(time.Hour),
		},
		{
			name:      "empty range is left alone",
			zoomLabel: "1h",
			from:      to,
			want:      to,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := clampChartFrom(tt.from, to, zoomByLabel(t, tt.zoomLabel))
			if !got.Equal(tt.want) {
				t.Errorf("clampChartFrom(%v, %v, %s) = %v, want %v",
					tt.from, to, tt.zoomLabel, got, tt.want)
			}
		})
	}
}

// TestClampChartFrom_DegenerateZoom pins the defensive branch: a ZoomLevel with
// a non-positive Duration (a typo in the ZoomLevels literal, a future tuning
// slip) must return `from` rather than divide by zero.
func TestClampChartFrom_DegenerateZoom(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	from := time.Time{}
	if got := clampChartFrom(from, to, ZoomLevel{Label: "bad", Duration: 0}); !got.Equal(from) {
		t.Errorf("clampChartFrom with zero Duration = %v, want %v unchanged", got, from)
	}
}

// TestClampChartFrom_BoundsCanvasWidth asserts the property the ceiling exists
// for, rather than a specific instant: whatever the cache claims, the clamped
// range never lays out a canvas wider than maxChartColumns — at ANY zoom. This
// is the assertion that would have caught #527, and it is deliberately stated
// in CanvasWidth terms: an earlier bucket-denominated version of this ceiling
// passed at 1h while still letting 24h build a ~240k-column canvas.
func TestClampChartFrom_BoundsCanvasWidth(t *testing.T) {
	t.Parallel()

	to := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)

	for _, z := range ZoomLevels {
		t.Run(z.Label, func(t *testing.T) {
			t.Parallel()

			// Precondition: the unclamped year-1 range must actually exceed the
			// ceiling, or this case proves nothing. Counted in columns, and
			// only at sub-day zooms — bucketCountInRange walks 24h day by day,
			// which over a 2025-year span is exactly the work the clamp exists
			// to avoid.
			if z.Duration != 24*time.Hour {
				unclamped := z.CanvasWidth(bucketCountInRange(time.Time{}, to, z.Duration))
				if unclamped <= maxChartColumns {
					t.Fatalf("precondition: unclamped year-1 canvas at %s is only %d columns; "+
						"this test no longer exercises the ceiling", z.Label, unclamped)
				}
			}

			from := clampChartFrom(time.Time{}, to, z)
			if w := z.CanvasWidth(bucketCountInRange(from, to, z.Duration)); w > maxChartColumns {
				t.Errorf("after clamp, CanvasWidth = %d columns, want <= %d", w, maxChartColumns)
			}
		})
	}
}
