package main

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

func TestWatcherStartupError(t *testing.T) {
	const root = "/home/u/.claude/projects"

	tests := []struct {
		name        string
		in          error
		wantSame    bool     // expect the input error returned unchanged
		wantContain []string // substrings the returned message must contain
	}{
		{
			name:        "missing root gets tailored hint",
			in:          fmt.Errorf("watch root %s: %w", root, fs.ErrNotExist),
			wantContain: []string{root, "no such file or directory", "run Claude Code", "CCPULSE_PROJECTS_ROOT"},
		},
		{
			name:     "non-ENOENT error passes through unchanged",
			in:       errors.New("permission denied"),
			wantSame: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := watcherStartupError(root, tt.in)
			if tt.wantSame {
				if !errors.Is(got, tt.in) {
					t.Fatalf("watcherStartupError returned a new error %v; want the input unchanged", got)
				}
				return
			}
			for _, sub := range tt.wantContain {
				if !strings.Contains(got.Error(), sub) {
					t.Errorf("message %q missing substring %q", got, sub)
				}
			}
		})
	}
}
