package main

import (
	"net/http"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

func TestPollBackoffNext(t *testing.T) {
	st := func(code int, ra time.Duration) *anthro.StatusError {
		return &anthro.StatusError{Code: code, RetryAfter: ra}
	}
	tests := []struct {
		name string
		seq  []*anthro.StatusError
		want []time.Duration
	}{
		{
			name: "healthy stays at base cadence",
			seq:  []*anthro.StatusError{nil, nil},
			want: []time.Duration{3 * time.Minute, 3 * time.Minute},
		},
		{
			name: "consecutive 429s escalate and cap",
			seq: []*anthro.StatusError{
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				st(http.StatusTooManyRequests, 0),
			},
			want: []time.Duration{
				6 * time.Minute, 12 * time.Minute, 24 * time.Minute,
				30 * time.Minute, 30 * time.Minute,
			},
		},
		{
			name: "success resets escalation",
			seq: []*anthro.StatusError{
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				nil, st(http.StatusTooManyRequests, 0),
			},
			want: []time.Duration{6 * time.Minute, 12 * time.Minute, 3 * time.Minute, 6 * time.Minute},
		},
		{
			name: "non-429 status resets escalation",
			seq:  []*anthro.StatusError{st(http.StatusTooManyRequests, 0), st(http.StatusInternalServerError, 0)},
			want: []time.Duration{6 * time.Minute, 3 * time.Minute},
		},
		{
			name: "retry-after below exponential is floored by exponential",
			seq:  []*anthro.StatusError{st(http.StatusTooManyRequests, 30*time.Second)},
			want: []time.Duration{6 * time.Minute},
		},
		{
			name: "retry-after above cap is honored",
			seq:  []*anthro.StatusError{st(http.StatusTooManyRequests, 45*time.Minute)},
			want: []time.Duration{45 * time.Minute},
		},
		{
			name: "retry-after clamped to one hour",
			seq:  []*anthro.StatusError{st(http.StatusTooManyRequests, 2*time.Hour)},
			want: []time.Duration{time.Hour},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b pollBackoff
			for i, s := range tt.seq {
				if got := b.next(s); got != tt.want[i] {
					t.Errorf("step %d: next() = %v, want %v", i, got, tt.want[i])
				}
			}
		})
	}
}
