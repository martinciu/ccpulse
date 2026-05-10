package status

import (
	"math"
	"testing"
	"time"
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
			got := projectBucket(tc.utilization, resetsAt, now, tc.window, tc.lowCutoff)

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
