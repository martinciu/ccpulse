package main

import (
	"errors"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// TestPollDelay covers what is left of the poller's cadence logic after #529
// moved the escalation policy into pkg/anthro (its table now lives there as
// TestNextBackoff). All the poller still decides is how long to sleep given
// the deadline Fetch handed it.
func TestPollDelay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		retryAt time.Time
		want    time.Duration
	}{
		{
			name: "no deadline falls back to the healthy cadence",
			want: anthro.BaseRetryInterval,
		},
		{
			name:    "an open window is slept out in full",
			retryAt: now.Add(24 * time.Minute),
			want:    24 * time.Minute,
		},
		{
			// A deadline can be in the past by the time the poller reads it
			// — a clock jump, or a fetch that outlived its own window. The
			// floor is what keeps that from becoming a spin.
			name:    "an elapsed deadline is floored",
			retryAt: now.Add(-time.Hour),
			want:    minPollDelay,
		},
		{
			name:    "a deadline inside the floor is floored",
			retryAt: now.Add(10 * time.Millisecond),
			want:    minPollDelay,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := pollDelay(tt.retryAt, now); got != tt.want {
				t.Errorf("pollDelay(%v) = %v, want %v", tt.retryAt, got, tt.want)
			}
		})
	}
}

// TestNextPollDelay covers the poller's outcome→cadence step. It exists
// because runQuotaPoller itself is untestable without a tea.Program and a
// cache, and the branch that matters most lives only there: a 429 with no
// cache to fall back on returns an error, and losing the deadline on that
// path spins the timer and floods the log — exactly the behaviour #529 set
// out to kill, reintroduced one layer up.
func TestNextPollDelay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)
	retryErr := func(d time.Duration, n int) error {
		return &anthro.RetryError{RetryAt: now.Add(d), Consecutive429: n, Err: anthro.ErrBackoff}
	}
	tests := []struct {
		name            string
		res             anthro.FetchResult
		err             error
		wantDelay       time.Duration
		wantConsecutive int
	}{
		{
			name:      "success polls at the healthy cadence",
			res:       anthro.FetchResult{Source: "api"},
			wantDelay: anthro.BaseRetryInterval,
		},
		{
			name:      "a fresh cache polls at the healthy cadence",
			res:       anthro.FetchResult{Source: "cache_fresh"},
			wantDelay: anthro.BaseRetryInterval,
		},
		{
			name:            "a stale result is slept out to its deadline",
			res:             anthro.FetchResult{Source: "cache_stale", RetryAt: now.Add(24 * time.Minute), Consecutive429: 3},
			wantDelay:       24 * time.Minute,
			wantConsecutive: 3,
		},
		{
			name:            "an error carrying a deadline is slept out too",
			err:             retryErr(12*time.Minute, 2),
			wantDelay:       12 * time.Minute,
			wantConsecutive: 2,
		},
		{
			name:      "an error carrying no deadline falls back to the cadence",
			err:       errors.New("anthro: empty access token"),
			wantDelay: anthro.BaseRetryInterval,
		},
		{
			name:            "an elapsed deadline is floored, never zero",
			err:             retryErr(-time.Hour, 4),
			wantDelay:       minPollDelay,
			wantConsecutive: 4,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			delay, consecutive429 := nextPollDelay(tt.res, tt.err, now)
			if delay != tt.wantDelay {
				t.Errorf("delay = %v, want %v", delay, tt.wantDelay)
			}
			if consecutive429 != tt.wantConsecutive {
				t.Errorf("consecutive429 = %d, want %d", consecutive429, tt.wantConsecutive)
			}
		})
	}
}
