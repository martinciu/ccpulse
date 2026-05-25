package main

import (
	"bytes"
	"testing"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/status"
)

func TestPrintStatus(t *testing.T) {
	mins := 42
	tests := []struct {
		name string
		w    status.Window
		want string
	}{
		{
			name: "quota with reset",
			w:    status.Window{Quota: &anthro.Usage{}, Percent: 30, MinutesToReset: &mins},
			want: "5h window: 30% used, resets in 42m\n",
		},
		{
			name: "quota idle",
			w:    status.Window{Quota: &anthro.Usage{}, Percent: 30},
			want: "5h window: 30% used, idle\n",
		},
		{
			name: "no quota",
			w:    status.Window{},
			want: "5h window: no quota data — run 'claude /login' for percent display, or use --json for tokens/cost.\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			printStatus(&buf, tt.w, false)
			if got := buf.String(); got != tt.want {
				t.Errorf("printStatus() = %q, want %q", got, tt.want)
			}
		})
	}
}
