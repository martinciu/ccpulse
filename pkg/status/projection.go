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

// linearRegressionSlope returns the exponentially-weighted least-squares
// slope (units per hour) for a set of (time, value) pairs.  Recent samples
// are weighted more heavily (half-life ≈ 6 h) so that dip-recover patterns
// and recent spikes are reflected in the slope, not washed out by flat
// earlier history.
func linearRegressionSlope(samples []cache.SevenDaySample) float64 {
	n := float64(len(samples))
	if n < 2 {
		return 0
	}
	// Use the earliest sample's timestamp as the origin to keep float64
	// precision reasonable (hours-since-epoch loses sub-second precision).
	t0 := samples[0].At
	const halfLifeHours = 6.0
	const decay = math.Ln2 / halfLifeHours // ≈ 0.1155
	var sumW, sumWX, sumWY, sumWXY, sumWX2 float64
	for _, s := range samples {
		ageHours := s.At.Sub(t0).Hours()
		w := math.Exp(decay * (ageHours - samples[len(samples)-1].At.Sub(t0).Hours()))
		x := ageHours
		y := s.Pct
		sumW += w
		sumWX += w * x
		sumWY += w * y
		sumWXY += w * x * y
		sumWX2 += w * x * x
	}
	denom := sumW*sumWX2 - sumWX*sumWX
	if denom == 0 {
		return 0
	}
	return (sumW*sumWXY - sumWX*sumWY) / denom
}

const (
	sevenDayTrailingWindow = 24 * time.Hour
	minSamplesForSlope     = 2
	minSpanForSlope        = 4 * time.Hour // must match sevenDayLowConfidenceCutoff
)

// filterCurrentBucket keeps only the samples in the most recent reset bucket
// (identified by the last sample's ResetsAt), dropping NaN/Inf utilisation
// values. Returns nil for empty input.
func filterCurrentBucket(samples []cache.SevenDaySample) []cache.SevenDaySample {
	if len(samples) == 0 {
		return nil
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
	return filtered
}

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

	filtered := filterCurrentBucket(samples)

	if len(filtered) < minSamplesForSlope {
		return linear
	}

	span := filtered[len(filtered)-1].At.Sub(filtered[0].At)
	if span < minSpanForSlope {
		return linear
	}

	// Use least-squares linear regression over all filtered samples instead
	// of a two-point endpoint difference.  The endpoint approach is blind to
	// non-monotonic series (dip-recover patterns where start == end).
	slopePerHour := linearRegressionSlope(filtered)
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
