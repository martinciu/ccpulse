package status

import (
	"math"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
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
//   - resetsAt:    bucket reset time, or nil when the API reported `resets_at: null`.
//   - now:         caller-supplied current time (testable).
//   - window:      configured window duration (5h or 7d).
//   - lowCutoff:   elapsed threshold below which Confidence is "low".
//
// When resetsAt is nil (idle window or upstream glitch) or elapsed <= 0
// (clock skew) the function returns a zeroed projection with Confidence
// "low" rather than dividing by zero.
func projectBucket(utilization float64, resetsAt *time.Time, now time.Time, window, lowCutoff time.Duration) Projection {
	if resetsAt == nil {
		return Projection{Confidence: "low"}
	}
	// Defend the JSON output: a corrupt usage.json cache or future API
	// change could surface NaN / ±Inf in Utilization, which would propagate
	// into SlopePctPerHour and make encoding/json reject the whole Window
	// ("json: unsupported value: NaN"). Surface as low-confidence instead.
	if math.IsNaN(utilization) || math.IsInf(utilization, 0) {
		return Projection{Confidence: "low"}
	}
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

const (
	sevenDayTrailingWindow = 24 * time.Hour
	minSamplesForSlope     = 2
	minSpanForSlope        = 4 * time.Hour // must match sevenDayLowConfidenceCutoff
)

func projectSevenDay(
	samples []cache.SevenDaySample,
	currentPct float64,
	resetsAt *time.Time,
	now time.Time,
) Projection {
	if resetsAt == nil {
		return Projection{Confidence: "low"}
	}
	linear := projectBucket(currentPct, resetsAt, now, sevenDayWindow, sevenDayLowConfidenceCutoff)

	if len(samples) < minSamplesForSlope {
		return linear
	}

	currentBucketID := samples[len(samples)-1].ResetsAt
	var filtered []cache.SevenDaySample
	for _, s := range samples {
		if s.ResetsAt != currentBucketID {
			continue
		}
		if math.IsNaN(s.Pct) || math.IsInf(s.Pct, 0) {
			continue
		}
		filtered = append(filtered, s)
	}

	if len(filtered) < minSamplesForSlope {
		return linear
	}

	span := filtered[len(filtered)-1].At.Sub(filtered[0].At)
	if span < minSpanForSlope {
		return linear
	}

	deltaPct := filtered[len(filtered)-1].Pct - filtered[0].Pct
	slopePerHour := deltaPct / span.Hours()
	if slopePerHour < 0 {
		slopePerHour = 0
	}

	hoursToReset := resetsAt.Sub(now).Hours()
	if hoursToReset < 0 {
		hoursToReset = 0
	}
	projectedAtReset := currentPct + slopePerHour*hoursToReset

	windowStart := resetsAt.Add(-sevenDayWindow)
	elapsed := max(now.Sub(windowStart), 0)

	proj := Projection{
		ElapsedMinutes:      int(elapsed.Minutes()),
		SlopePctPerHour:     round2(slopePerHour),
		ProjectedPctAtReset: int(math.Round(projectedAtReset)),
		WillOverreach:       projectedAtReset > 100,
		Confidence:          "ok",
	}
	if proj.WillOverreach && slopePerHour > 0 && currentPct < 100 {
		m := int(math.Round((100 - currentPct) / slopePerHour * 60))
		proj.MinutesTo100Pct = &m
	}
	return proj
}
