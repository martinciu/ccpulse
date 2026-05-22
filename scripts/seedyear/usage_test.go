package main

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/parse"
)

func TestWindowFloor(t *testing.T) {
	epoch := time.Unix(0, 0).UTC()
	tests := []struct {
		name   string
		ts     time.Time
		window time.Duration
		want   time.Time
	}{
		{"exactly on a 5h boundary", epoch.Add(10 * time.Hour), 5 * time.Hour, epoch.Add(10 * time.Hour)},
		{"mid 5h window", epoch.Add(12 * time.Hour), 5 * time.Hour, epoch.Add(10 * time.Hour)},
		{"mid 7d window", epoch.Add(10 * 24 * time.Hour), 7 * 24 * time.Hour, epoch.Add(7 * 24 * time.Hour)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := windowFloor(tt.ts, tt.window)
			if !got.Equal(tt.want) {
				t.Errorf("windowFloor(%s, %s) = %s, want %s", tt.ts, tt.window, got, tt.want)
			}
		})
	}
}

func TestDensityIndex_Sum(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	msgs := []parse.Message{
		{Timestamp: base.Add(1 * time.Hour), InputTokens: 10, OutputTokens: 5},  // 15
		{Timestamp: base.Add(2 * time.Hour), InputTokens: 20, OutputTokens: 0},  // 20
		{Timestamp: base.Add(3 * time.Hour), InputTokens: 0, OutputTokens: 100}, // 100
	}
	idx := newDensityIndex(msgs)

	if got := idx.sum(base, base.Add(4*time.Hour)); got != 135 {
		t.Errorf("sum(all) = %d, want 135", got)
	}
	if got := idx.sum(base.Add(2*time.Hour), base.Add(3*time.Hour)); got != 120 {
		t.Errorf("sum(2h..3h inclusive) = %d, want 120", got)
	}
	if got := idx.sum(base.Add(5*time.Hour), base.Add(6*time.Hour)); got != 0 {
		t.Errorf("sum(empty range) = %d, want 0", got)
	}
}

func TestNormalizePeak(t *testing.T) {
	t.Run("busiest reads ~85", func(t *testing.T) {
		out := normalizePeak([]float64{0, 50, 100})
		if out[0] != 0 {
			t.Errorf("min: got %v, want 0", out[0])
		}
		if out[2] < 84.9 || out[2] > 85.1 {
			t.Errorf("peak: got %v, want ~85", out[2])
		}
		if !(out[1] > out[0] && out[1] < out[2]) {
			t.Errorf("monotonic: got %v", out)
		}
	})
	t.Run("all zero stays zero", func(t *testing.T) {
		out := normalizePeak([]float64{0, 0, 0})
		for i, v := range out {
			if v != 0 {
				t.Errorf("out[%d] = %v, want 0", i, v)
			}
		}
	})
}

func TestBuildUsageSamples_SpanAndCorrelation(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var msgs []parse.Message
	// Busy burst on day 0 (lots of tokens in a tight window)...
	for i := 0; i < 60; i++ {
		msgs = append(msgs, parse.Message{
			Timestamp:   base.Add(time.Duration(i) * time.Minute),
			InputTokens: 1000, OutputTokens: 5000,
		})
	}
	// ...then a single low-token message ~6 days later (long idle gap between).
	msgs = append(msgs, parse.Message{
		Timestamp:   base.Add(6 * 24 * time.Hour),
		InputTokens: 1, OutputTokens: 1,
	})

	samples := buildUsageSamples(msgs)
	if len(samples) == 0 {
		t.Fatal("buildUsageSamples returned no samples")
	}

	// Span: first sample at the earliest msg, last at/just before the latest.
	if !samples[0].at.Equal(base) {
		t.Errorf("first sample at %s, want %s", samples[0].at, base)
	}
	last := samples[len(samples)-1].at
	latestMsg := base.Add(6 * 24 * time.Hour)
	if last.After(latestMsg) {
		t.Errorf("last sample %s is after latest msg %s", last, latestMsg)
	}

	// Correlation: 5h utilization during the burst is high; deep in the idle
	// gap (e.g. day 3) it is ~0.
	var burst, idle float64
	for _, s := range samples {
		if s.at.Equal(base.Add(30 * time.Minute)) {
			burst = s.fiveHour
		}
		if s.at.After(base.Add(3*24*time.Hour)) && s.at.Before(base.Add(4*24*time.Hour)) {
			idle = s.fiveHour // last match in the window wins; any is fine
		}
	}
	if burst < 40 {
		t.Errorf("burst 5h util = %v, want elevated (>=40)", burst)
	}
	if idle > 1 {
		t.Errorf("idle 5h util = %v, want ~0", idle)
	}

	// resets_at is always strictly after the sample timestamp.
	for _, s := range samples {
		if !s.fiveReset.After(s.at) {
			t.Errorf("fiveReset %s not after at %s", s.fiveReset, s.at)
		}
		if !s.sevenReset.After(s.at) {
			t.Errorf("sevenReset %s not after at %s", s.sevenReset, s.at)
		}
	}
}
