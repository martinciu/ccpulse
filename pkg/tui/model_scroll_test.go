package tui

import (
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestEarliestRemainingSampleAt(t *testing.T) {
	t0 := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(1 * time.Hour)
	t2 := t0.Add(2 * time.Hour)

	tests := []struct {
		name  string
		pts5h []cache.UtilizationPoint
		pts7d []cache.UtilizationPoint
		want  time.Time
	}{
		{
			name:  "both empty returns zero time",
			pts5h: nil,
			pts7d: nil,
			want:  time.Time{},
		},
		{
			name:  "only 5h populated returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t1}, {At: t2}},
			pts7d: nil,
			want:  t1,
		},
		{
			name:  "only 7d populated returns 7d[0]",
			pts5h: nil,
			pts7d: []cache.UtilizationPoint{{At: t2}},
			want:  t2,
		},
		{
			name:  "both populated returns earlier of the two",
			pts5h: []cache.UtilizationPoint{{At: t2}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t1,
		},
		{
			name:  "both populated 5h earlier returns 5h[0]",
			pts5h: []cache.UtilizationPoint{{At: t0}},
			pts7d: []cache.UtilizationPoint{{At: t1}},
			want:  t0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := earliestRemainingSampleAt(tt.pts5h, tt.pts7d)
			if !got.Equal(tt.want) {
				t.Errorf("earliestRemainingSampleAt = %v, want %v", got, tt.want)
			}
		})
	}
}
