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

// sevenDayHalfLife sets how fast older samples are de-weighted in the 7d
// slope regression: a sample Δt old contributes exp(-Δt/halfLife) weight.
// 4h means a sample 4h back counts ~half, 12h back ~1/8, 24h back ~1.6%.
const sevenDayHalfLife = 4 * time.Hour

// weightedRegressionSlope returns the least-squares slope in pct-per-hour of
// the (At, Pct) samples, weighting each sample w_i = exp(-(tLast-t_i)/halfLife)
// so recent samples dominate. samples must be sorted oldest-first; tLast is the
// last sample's At. The slope is signed — callers clamp negatives if they want
// a non-decreasing projection. Returns 0 for fewer than 2 samples or when the
// weighted x-variance is 0 (all samples at one instant, or degenerate weights):
// a 0 slope produces a flat, conservative projection, the correct answer when
// there is no usable signal.
func weightedRegressionSlope(samples []cache.SevenDaySample, halfLife time.Duration) float64 {
	if len(samples) < 2 {
		return 0
	}
	tLast := samples[len(samples)-1].At
	halfLifeHours := halfLife.Hours()

	var sumW, sumWX, sumWY float64
	for _, s := range samples {
		dtHours := tLast.Sub(s.At).Hours() // >= 0, samples are oldest-first
		w := math.Exp(-dtHours / halfLifeHours)
		x := -dtHours // hours relative to tLast (<= 0)
		sumW += w
		sumWX += w * x
		sumWY += w * s.Pct
	}
	if sumW == 0 {
		return 0
	}
	xBar := sumWX / sumW
	yBar := sumWY / sumW

	var num, den float64
	for _, s := range samples {
		dtHours := tLast.Sub(s.At).Hours()
		w := math.Exp(-dtHours / halfLifeHours)
		dx := -dtHours - xBar
		num += w * dx * (s.Pct - yBar)
		den += w * dx * dx
	}
	if den == 0 {
		return 0
	}
	return num / den
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

	slopePerHour := weightedRegressionSlope(filtered, sevenDayHalfLife)
	if slopePerHour < 0 {
		slopePerHour = 0 // users care about "will I overrun", not "trending down"
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
