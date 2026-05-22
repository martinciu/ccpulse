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
