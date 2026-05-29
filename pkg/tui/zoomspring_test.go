package tui

import (
	"testing"
	"time"
)

func TestLerpTime(t *testing.T) {
	t.Parallel()
	a := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	b := a.Add(100 * time.Hour)
	cases := []struct {
		name string
		r    float64
		want time.Time
	}{
		{"r=0 returns a", 0.0, a},
		{"r=1 returns b", 1.0, b},
		{"r=0.5 midpoint", 0.5, a.Add(50 * time.Hour)},
		{"r<0 clamps to a", -0.5, a},
		{"r>1 clamps to b", 1.5, b},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := lerpTime(a, b, tc.r); !got.Equal(tc.want) {
				t.Errorf("lerpTime(a, b, %v) = %v, want %v", tc.r, got, tc.want)
			}
		})
	}
}
