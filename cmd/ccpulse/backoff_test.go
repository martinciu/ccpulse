package main

import (
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
