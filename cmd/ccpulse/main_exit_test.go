package main

import (
	"errors"
	"fmt"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
)

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"nil err is irrelevant — main never calls with nil", errors.New("plain"), 1},
		{"ErrLockHeld → 75", cache.ErrLockHeld, 75},
		{"wrapped ErrLockHeld → 75", fmt.Errorf("status: %w", cache.ErrLockHeld), 75},
		{"unrelated error → 1", errors.New("oops"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.err); got != tc.want {
				t.Fatalf("exitCodeFor(%v) = %d, want %d", tc.err, got, tc.want)
			}
		})
	}
}
