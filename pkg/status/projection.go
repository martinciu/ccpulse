package status

import (
	"math"
	"time"
)

const (
	fiveHourWindow = 5 * time.Hour
	sevenDayWindow = 7 * 24 * time.Hour

	fiveHourLowConfidenceCutoff = 30 * time.Minute
	sevenDayLowConfidenceCutoff = 4 * time.Hour
)

// projectBucket extrapolates the bucket's current utilisation to end-of-window.
//
// Inputs:
//   - utilization: raw float from anthro.Bucket.Utilization (NOT the clamped Window.Percent).
//   - resetsAt:    bucket reset time.
//   - now:         caller-supplied current time (testable).
//   - window:      configured window duration (5h or 7d).
//   - lowCutoff:   elapsed threshold below which Confidence is "low".
//
// When elapsed <= 0 (clock skew) the function returns a zeroed projection
// with Confidence "low" rather than dividing by zero.
func projectBucket(utilization float64, resetsAt, now time.Time, window, lowCutoff time.Duration) Projection {
	windowStart := resetsAt.Add(-window)
	elapsed := now.Sub(windowStart)
	if elapsed <= 0 {
		return Projection{Confidence: "low"}
	}

	elapsedHours := elapsed.Hours()
	slopePerHour := utilization / elapsedHours
	projectedAtReset := utilization * window.Hours() / elapsedHours

	proj := Projection{
		ElapsedMinutes:      int(elapsed.Minutes()),
		SlopePctPerHour:     round2(slopePerHour),
		ProjectedPctAtReset: int(math.Round(projectedAtReset)),
		WillOverreach:       projectedAtReset > 100,
		Confidence:          confidenceFor(elapsed, lowCutoff),
	}
	// utilization >= 100 means we've already crossed the threshold —
	// "minutes until 100 %" is nonsensical (would be negative), so leave nil.
	if proj.WillOverreach && slopePerHour > 0 && utilization < 100 {
		m := int(math.Round((100 - utilization) / slopePerHour * 60))
		proj.MinutesTo100Pct = &m
	}
	return proj
}

func confidenceFor(elapsed, lowCutoff time.Duration) string {
	if elapsed < lowCutoff {
		return "low"
	}
	return "ok"
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
