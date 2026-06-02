package status

import (
	"math"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

// projectBucketCase exercises a single (utilization, elapsed) pair.
// resetsAt is reconstructed as windowStart + window, where
// windowStart = now - elapsed.
type projectBucketCase struct {
	name        string
	utilization float64
	elapsed     time.Duration
	window      time.Duration
	lowCutoff   time.Duration
	want        Projection // nil-pointer fields use wantMinutesTo100
	// wantMinutesTo100Set distinguishes "nil" from "0":
	//   false → expect Projection.MinutesTo100Pct == nil
	//   true  → expect *Projection.MinutesTo100Pct == wantMinutesTo100
	wantMinutesTo100Set bool
	wantMinutesTo100    int
}

func TestProjectBucket(t *testing.T) {
	const (
		fiveHour    = 5 * time.Hour
		sevenDay    = 7 * 24 * time.Hour
		fiveHourLow = 30 * time.Minute
		sevenDayLow = 4 * time.Hour
	)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	cases := []projectBucketCase{
		{
			name:        "5h mid-window on-pace",
			utilization: 12,
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      60,
				SlopePctPerHour:     12.00,
				ProjectedPctAtReset: 60,
				WillOverreach:       false,
				Confidence:          "ok",
			},
		},
		{
			name:        "5h early-window low confidence",
			utilization: 5,
			elapsed:     15 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      15,
				SlopePctPerHour:     20.00, // 5 / 0.25 = 20
				ProjectedPctAtReset: 100,   // 5 * 5 / 0.25 = 100
				WillOverreach:       false, // strictly > 100 required
				Confidence:          "low",
			},
		},
		{
			name:        "5h over-pace",
			utilization: 35,
			elapsed:     90 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      90,
				SlopePctPerHour:     23.33, // 35 / 1.5
				ProjectedPctAtReset: 117,   // round(35 * 5 / 1.5)
				WillOverreach:       true,
				Confidence:          "ok",
			},
			wantMinutesTo100Set: true,
			wantMinutesTo100:    167, // round((100-35)/23.333... * 60)
		},
		{
			name:        "5h ridiculously hot, no clamp",
			utilization: 80,
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      60,
				SlopePctPerHour:     80.00,
				ProjectedPctAtReset: 400, // unclamped
				WillOverreach:       true,
				Confidence:          "ok",
			},
			wantMinutesTo100Set: true,
			wantMinutesTo100:    15, // (100-80)/80 * 60
		},
		{
			name:        "7d on-pace",
			utilization: 30,
			elapsed:     72 * time.Hour, // 3 days in
			window:      sevenDay,
			lowCutoff:   sevenDayLow,
			want: Projection{
				ElapsedMinutes:      72 * 60,
				SlopePctPerHour:     0.42, // round2(30/72)
				ProjectedPctAtReset: 70,   // round(30 * 168 / 72)
				WillOverreach:       false,
				Confidence:          "ok",
			},
		},
		{
			name:        "7d early low confidence",
			utilization: 1,
			elapsed:     2 * time.Hour,
			window:      sevenDay,
			lowCutoff:   sevenDayLow,
			want: Projection{
				ElapsedMinutes:      120,
				SlopePctPerHour:     0.50, // round2(1/2)
				ProjectedPctAtReset: 84,   // round(1 * 168 / 2)
				WillOverreach:       false,
				Confidence:          "low",
			},
		},
		{
			name:        "5h confidence boundary at exactly 30 min",
			utilization: 10,
			elapsed:     30 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      30,
				SlopePctPerHour:     20.00, // 10 / 0.5
				ProjectedPctAtReset: 100,
				WillOverreach:       false,
				Confidence:          "ok", // strictly < cutoff is "low"
			},
		},
		{
			name:        "7d confidence boundary at exactly 4 h",
			utilization: 2,
			elapsed:     4 * time.Hour,
			window:      sevenDay,
			lowCutoff:   sevenDayLow,
			want: Projection{
				ElapsedMinutes:      240,
				SlopePctPerHour:     0.50,
				ProjectedPctAtReset: 84,
				WillOverreach:       false,
				Confidence:          "ok",
			},
		},
		{
			name:        "clock skew: now < window_start",
			utilization: 10,
			elapsed:     -5 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      0,
				SlopePctPerHour:     0,
				ProjectedPctAtReset: 0,
				WillOverreach:       false,
				Confidence:          "low",
			},
		},
		{
			name:        "5h zero utilization → no overreach, no ETA",
			utilization: 0,
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      60,
				SlopePctPerHour:     0,
				ProjectedPctAtReset: 0,
				WillOverreach:       false,
				Confidence:          "ok",
			},
		},
		{
			// Already past 100 — "minutes until 100" is nonsense; expect nil.
			// will_overreach is still true so consumers branching on the bool
			// keep working.
			name:        "5h already over 100, no ETA",
			utilization: 105,
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      60,
				SlopePctPerHour:     105.00,
				ProjectedPctAtReset: 525,
				WillOverreach:       true,
				Confidence:          "ok",
			},
			// wantMinutesTo100Set defaults to false → expect nil.
		},
		{
			// Boundary: utilization == 100 exactly. The MinutesTo100Pct guard
			// uses strict `utilization < 100` — at exactly 100 the ETA is nil
			// because we're at the threshold (zero minutes is more confusing
			// than "no ETA needed").
			name:        "5h utilization exactly 100, no ETA",
			utilization: 100,
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      60,
				SlopePctPerHour:     100.00,
				ProjectedPctAtReset: 500,
				WillOverreach:       true,
				Confidence:          "ok",
			},
			// wantMinutesTo100Set defaults to false → expect nil.
		},
		{
			// Defense-in-depth: a corrupt usage.json with NaN utilization must
			// not crash JSON marshalling. projectBucket short-circuits to a
			// zeroed Projection so Window.Projection serializes cleanly.
			name:        "NaN utilization → zeroed low-confidence projection",
			utilization: math.NaN(),
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      0,
				SlopePctPerHour:     0,
				ProjectedPctAtReset: 0,
				WillOverreach:       false,
				Confidence:          "low",
			},
		},
		{
			// Same defense for +Inf — surfaces e.g. when an upstream JSON
			// number overflows float64. Result is finite zeros + low confidence.
			name:        "+Inf utilization → zeroed low-confidence projection",
			utilization: math.Inf(1),
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      0,
				SlopePctPerHour:     0,
				ProjectedPctAtReset: 0,
				WillOverreach:       false,
				Confidence:          "low",
			},
		},
		{
			// Pin the negative-Inf branch too. The guard uses
			// math.IsInf(utilization, 0) which matches either sign; a future
			// refactor to math.IsInf(utilization, 1) would silently regress.
			// This row prevents that.
			name:        "-Inf utilization → zeroed low-confidence projection",
			utilization: math.Inf(-1),
			elapsed:     60 * time.Minute,
			window:      fiveHour,
			lowCutoff:   fiveHourLow,
			want: Projection{
				ElapsedMinutes:      0,
				SlopePctPerHour:     0,
				ProjectedPctAtReset: 0,
				WillOverreach:       false,
				Confidence:          "low",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			windowStart := now.Add(-tc.elapsed)
			resetsAt := windowStart.Add(tc.window)
			got := projectBucket(tc.utilization, &resetsAt, now, tc.window, tc.lowCutoff)

			if got.ElapsedMinutes != tc.want.ElapsedMinutes {
				t.Errorf("ElapsedMinutes = %d, want %d", got.ElapsedMinutes, tc.want.ElapsedMinutes)
			}
			if got.SlopePctPerHour != tc.want.SlopePctPerHour {
				t.Errorf("SlopePctPerHour = %v, want %v", got.SlopePctPerHour, tc.want.SlopePctPerHour)
			}
			if got.ProjectedPctAtReset != tc.want.ProjectedPctAtReset {
				t.Errorf("ProjectedPctAtReset = %d, want %d", got.ProjectedPctAtReset, tc.want.ProjectedPctAtReset)
			}
			if got.WillOverreach != tc.want.WillOverreach {
				t.Errorf("WillOverreach = %v, want %v", got.WillOverreach, tc.want.WillOverreach)
			}
			if got.Confidence != tc.want.Confidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.want.Confidence)
			}

			switch {
			case tc.wantMinutesTo100Set && got.MinutesTo100Pct == nil:
				t.Errorf("MinutesTo100Pct = nil, want %d", tc.wantMinutesTo100)
			case tc.wantMinutesTo100Set && *got.MinutesTo100Pct != tc.wantMinutesTo100:
				t.Errorf("*MinutesTo100Pct = %d, want %d", *got.MinutesTo100Pct, tc.wantMinutesTo100)
			case !tc.wantMinutesTo100Set && got.MinutesTo100Pct != nil:
				t.Errorf("MinutesTo100Pct = %d, want nil", *got.MinutesTo100Pct)
			}
		})
	}
}

func TestProjectSevenDay_ColdStartAndFallback(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	resetsAt := now.Add(72 * time.Hour) // 3 days until reset
	bucketResetID := resetsAt.UTC().Format(time.RFC3339Nano)

	t.Run("zero samples falls back to linear", func(t *testing.T) {
		linear := projectBucket(20.0, &resetsAt, now, sevenDayWindow, sevenDayLowConfidenceCutoff)
		got := projectSevenDay(nil, 20.0, &resetsAt, now)
		if got != linear {
			t.Errorf("got %+v, want linear fallback %+v", got, linear)
		}
	})

	t.Run("single sample falls back to linear", func(t *testing.T) {
		samples := []cache.SevenDaySample{
			{At: now.Add(-2 * time.Hour), Pct: 18.0, ResetsAt: bucketResetID},
		}
		linear := projectBucket(20.0, &resetsAt, now, sevenDayWindow, sevenDayLowConfidenceCutoff)
		got := projectSevenDay(samples, 20.0, &resetsAt, now)
		if got != linear {
			t.Errorf("got %+v, want linear fallback %+v", got, linear)
		}
	})

	t.Run("post-reset span under 4h falls back to linear", func(t *testing.T) {
		samples := []cache.SevenDaySample{
			{At: now.Add(-2 * time.Hour), Pct: 18.0, ResetsAt: bucketResetID},
			{At: now.Add(-30 * time.Minute), Pct: 19.5, ResetsAt: bucketResetID},
		}
		linear := projectBucket(20.0, &resetsAt, now, sevenDayWindow, sevenDayLowConfidenceCutoff)
		got := projectSevenDay(samples, 20.0, &resetsAt, now)
		if got != linear {
			t.Errorf("got %+v, want linear fallback %+v", got, linear)
		}
	})
}

func TestProjectSevenDay_SlopeCases(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	resetsAt := now.Add(72 * time.Hour) // 3 days (72h) until reset
	bucketA := resetsAt.UTC().Format(time.RFC3339Nano)
	bucketB := now.Add(72*time.Hour + 7*24*time.Hour).UTC().Format(time.RFC3339Nano) // a later bucket

	mkSamples := func(pcts []float64, startBack, step time.Duration, bucketID string) []cache.SevenDaySample {
		out := make([]cache.SevenDaySample, len(pcts))
		for i, p := range pcts {
			out[i] = cache.SevenDaySample{
				At:       now.Add(-startBack + time.Duration(i)*step),
				Pct:      p,
				ResetsAt: bucketID,
			}
		}
		return out
	}

	const epsilon = 0.01
	cases := []struct {
		name             string
		samples          []cache.SevenDaySample
		currentPct       float64
		wantSlopeApprox  float64
		wantOverreach    bool
		wantConfidence   string
		wantMinutesTo100 bool
	}{
		{
			name:            "front-loaded: flat 24h trailing → slope ≈ 0",
			samples:         mkSamples([]float64{50.0, 50.0, 50.0, 50.0, 50.0}, 24*time.Hour, 6*time.Hour, bucketA),
			currentPct:      50.0,
			wantSlopeApprox: 0.0,
			wantOverreach:   false,
			wantConfidence:  "ok",
		},
		{
			name:             "back-loaded: 0→50 over 24h → slope ≈ 2.083%/h",
			samples:          mkSamples([]float64{0.0, 12.5, 25.0, 37.5, 50.0}, 24*time.Hour, 6*time.Hour, bucketA),
			currentPct:       50.0,
			wantSlopeApprox:  50.0 / 24.0,
			wantOverreach:    true,
			wantConfidence:   "ok",
			wantMinutesTo100: true,
		},
		{
			name:            "flat steady ramp: 0→1 over 24h → slope ≈ 0.0417%/h",
			samples:         mkSamples([]float64{0.0, 0.25, 0.5, 0.75, 1.0}, 24*time.Hour, 6*time.Hour, bucketA),
			currentPct:      25.0,
			wantSlopeApprox: 1.0 / 24.0,
			wantOverreach:   false,
			wantConfidence:  "ok",
		},
		{
			name: "recent spike: flat at 30% for 20h, +20% in last 4h",
			samples: []cache.SevenDaySample{
				{At: now.Add(-24 * time.Hour), Pct: 30.0, ResetsAt: bucketA},
				{At: now.Add(-18 * time.Hour), Pct: 30.0, ResetsAt: bucketA},
				{At: now.Add(-12 * time.Hour), Pct: 30.0, ResetsAt: bucketA},
				{At: now.Add(-6 * time.Hour), Pct: 30.0, ResetsAt: bucketA},
				{At: now, Pct: 50.0, ResetsAt: bucketA},
			},
			currentPct:       50.0,
			wantSlopeApprox:  2.07, // recency-weighted: late +20% jump dominates (was 20/24 endpoint)
			wantOverreach:    true,
			wantConfidence:   "ok",
			wantMinutesTo100: true,
		},
		{
			name:            "old spike: flat at 30% for 24h → slope ≈ 0",
			samples:         mkSamples([]float64{30.0, 30.0, 30.0, 30.0, 30.0}, 24*time.Hour, 6*time.Hour, bucketA),
			currentPct:      30.0,
			wantSlopeApprox: 0.0,
			wantOverreach:   false,
			wantConfidence:  "ok",
		},
		{
			name: "dip-recover: 9→11→0→4.5→9 (issue #395) → positive recent slope",
			samples: []cache.SevenDaySample{
				{At: now.Add(-24 * time.Hour), Pct: 9.0, ResetsAt: bucketA},
				{At: now.Add(-18 * time.Hour), Pct: 11.0, ResetsAt: bucketA},
				{At: now.Add(-12 * time.Hour), Pct: 0.0, ResetsAt: bucketA},
				{At: now.Add(-6 * time.Hour), Pct: 4.5, ResetsAt: bucketA},
				{At: now, Pct: 9.0, ResetsAt: bucketA},
			},
			currentPct:      9.0,
			wantSlopeApprox: 0.52,  // endpoint-diff would give 0 (9→9); weighted ≈ 0.52
			wantOverreach:   false, // 9 + 0.52*72 ≈ 46
			wantConfidence:  "ok",
		},
		{
			name: "sparse window: two samples 24h apart",
			samples: []cache.SevenDaySample{
				{At: now.Add(-24 * time.Hour), Pct: 10.0, ResetsAt: bucketA},
				{At: now, Pct: 30.0, ResetsAt: bucketA},
			},
			currentPct:      30.0,
			wantSlopeApprox: 20.0 / 24.0,
			wantOverreach:   false, // 30 + 0.833*72 ≈ 90
			wantConfidence:  "ok",
		},
		{
			name: "reset inside window: pre-reset samples filtered out",
			samples: []cache.SevenDaySample{
				{At: now.Add(-20 * time.Hour), Pct: 80.0, ResetsAt: bucketA},
				{At: now.Add(-10 * time.Hour), Pct: 90.0, ResetsAt: bucketA},
				{At: now.Add(-4 * time.Hour), Pct: 0.0, ResetsAt: bucketB},
				{At: now, Pct: 5.0, ResetsAt: bucketB},
			},
			currentPct:      5.0,
			wantSlopeApprox: 5.0 / 4.0, // 1.25%/h
			wantOverreach:   false,     // 5 + 1.25*72 = 95
			wantConfidence:  "ok",
		},
		{
			name: "negative-Δ noise: clamped to 0",
			samples: []cache.SevenDaySample{
				{At: now.Add(-24 * time.Hour), Pct: 30.0, ResetsAt: bucketA},
				{At: now, Pct: 29.7, ResetsAt: bucketA},
			},
			currentPct:      29.7,
			wantSlopeApprox: 0.0,
			wantOverreach:   false,
			wantConfidence:  "ok",
		},
		{
			name: "NaN sample is filtered, surrounding samples drive slope",
			samples: []cache.SevenDaySample{
				{At: now.Add(-24 * time.Hour), Pct: 10.0, ResetsAt: bucketA},
				{At: now.Add(-12 * time.Hour), Pct: math.NaN(), ResetsAt: bucketA},
				{At: now, Pct: 30.0, ResetsAt: bucketA},
			},
			currentPct:      30.0,
			wantSlopeApprox: 20.0 / 24.0,
			wantOverreach:   false,
			wantConfidence:  "ok",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := projectSevenDay(tc.samples, tc.currentPct, &resetsAt, now)
			if math.Abs(got.SlopePctPerHour-tc.wantSlopeApprox) > epsilon {
				t.Errorf("SlopePctPerHour = %v, want ≈ %v (±%v)", got.SlopePctPerHour, tc.wantSlopeApprox, epsilon)
			}
			if got.WillOverreach != tc.wantOverreach {
				t.Errorf("WillOverreach = %v, want %v", got.WillOverreach, tc.wantOverreach)
			}
			if got.Confidence != tc.wantConfidence {
				t.Errorf("Confidence = %q, want %q", got.Confidence, tc.wantConfidence)
			}
			if tc.wantMinutesTo100 && got.MinutesTo100Pct == nil {
				t.Errorf("MinutesTo100Pct = nil, want non-nil")
			}
			if !tc.wantMinutesTo100 && got.MinutesTo100Pct != nil {
				t.Errorf("MinutesTo100Pct = %d, want nil", *got.MinutesTo100Pct)
			}
		})
	}
}

func TestFilterCurrentBucket(t *testing.T) {
	t0 := time.Date(2026, 5, 25, 0, 0, 0, 0, time.UTC)
	reset1 := "2026-06-01T00:00:00.000Z"
	reset2 := "2026-06-02T00:00:00.000Z"
	tests := []struct {
		name string
		in   []cache.SevenDaySample
		want int
	}{
		{"empty", nil, 0},
		{"single bucket kept", []cache.SevenDaySample{
			{At: t0, Pct: 10, ResetsAt: reset2},
			{At: t0.Add(time.Hour), Pct: 20, ResetsAt: reset2},
		}, 2},
		{"older bucket dropped", []cache.SevenDaySample{
			{At: t0, Pct: 10, ResetsAt: reset1},
			{At: t0.Add(time.Hour), Pct: 20, ResetsAt: reset2},
		}, 1},
		{"NaN and Inf dropped", []cache.SevenDaySample{
			{At: t0, Pct: math.NaN(), ResetsAt: reset2},
			{At: t0.Add(time.Hour), Pct: math.Inf(1), ResetsAt: reset2},
			{At: t0.Add(2 * time.Hour), Pct: 30, ResetsAt: reset2},
		}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(filterCurrentBucket(tt.in)); got != tt.want {
				t.Errorf("filterCurrentBucket() len = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestWeightedRegressionSlope(t *testing.T) {
	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	// samples at -24,-18,-12,-6,0 hours, oldest-first.
	mk := func(pcts ...float64) []cache.SevenDaySample {
		out := make([]cache.SevenDaySample, len(pcts))
		startBack := time.Duration(len(pcts)-1) * 6 * time.Hour
		for i, p := range pcts {
			out[i] = cache.SevenDaySample{
				At:  now.Add(-startBack + time.Duration(i)*6*time.Hour),
				Pct: p,
			}
		}
		return out
	}

	t.Run("nil returns 0", func(t *testing.T) {
		if got := weightedRegressionSlope(nil); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("single sample returns 0", func(t *testing.T) {
		if got := weightedRegressionSlope(mk(42)); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("zero span (all At equal) returns 0", func(t *testing.T) {
		s := []cache.SevenDaySample{
			{At: now, Pct: 10}, {At: now, Pct: 20},
		}
		if got := weightedRegressionSlope(s); got != 0 {
			t.Errorf("got %v, want 0", got)
		}
	})
	t.Run("flat series returns 0", func(t *testing.T) {
		if got := weightedRegressionSlope(mk(50, 50, 50, 50, 50)); math.Abs(got) > 1e-9 {
			t.Errorf("got %v, want ~0", got)
		}
	})
	t.Run("perfectly linear ramp returns exact slope", func(t *testing.T) {
		// 0..50 over 24h => 50/24 = 2.08333 %/h (exact for collinear points).
		got := weightedRegressionSlope(mk(0, 12.5, 25, 37.5, 50))
		if math.Abs(got-50.0/24.0) > 1e-9 {
			t.Errorf("got %v, want %v", got, 50.0/24.0)
		}
	})
	t.Run("two collinear points return endpoint slope", func(t *testing.T) {
		s := []cache.SevenDaySample{
			{At: now.Add(-24 * time.Hour), Pct: 10},
			{At: now, Pct: 30},
		}
		got := weightedRegressionSlope(s)
		if math.Abs(got-20.0/24.0) > 1e-9 {
			t.Errorf("got %v, want %v", got, 20.0/24.0)
		}
	})
	t.Run("negative trend is NOT clamped (caller clamps)", func(t *testing.T) {
		// descending 50..0 => -2.08333 %/h; helper returns the signed value.
		got := weightedRegressionSlope(mk(50, 37.5, 25, 12.5, 0))
		if math.Abs(got-(-50.0/24.0)) > 1e-9 {
			t.Errorf("got %v, want %v", got, -50.0/24.0)
		}
	})
	t.Run("dip-recover yields positive slope (issue #395)", func(t *testing.T) {
		// 9,11,0,4.5,9 — endpoints equal (9->9) so endpoint-diff = 0,
		// but the recent climb dominates under recency weighting.
		got := weightedRegressionSlope(mk(9, 11, 0, 4.5, 9))
		if got <= 0.4 || got >= 0.7 {
			t.Errorf("got %v, want in (0.4, 0.7) — non-zero positive recent climb", got)
		}
	})
}

func TestRound2(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{0.05, 0.05},
		{0.333333, 0.33},
		{0.005, 0.01}, // Go's math.Round rounds half away from zero, not banker's
		{21.005, 21.01},
		{21.004, 21.00},
	}
	for _, tc := range cases {
		if got := round2(tc.in); got != tc.want {
			t.Errorf("round2(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
