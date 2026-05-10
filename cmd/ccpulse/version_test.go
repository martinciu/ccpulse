package main

import (
	"strings"
	"testing"

	"github.com/martinciu/ccpulse/pkg/channel"
)

// resetChannel forces the channel global back to its dev default. Each test
// that calls channel.Set must register this via t.Cleanup so sibling tests
// (including TestVersionString in main_test.go) start from a known state.
func resetChannel() { channel.Set("dev") }

func TestVersionStringIncludesChannel_Dev(t *testing.T) {
	resetChannel()
	t.Cleanup(resetChannel)
	channel.Set("dev")
	got := versionString()
	if !strings.Contains(got, "channel dev") {
		t.Errorf("versionString() = %q, want substring %q", got, "channel dev")
	}
}

func TestVersionStringIncludesChannel_Release(t *testing.T) {
	resetChannel()
	t.Cleanup(resetChannel)
	channel.Set("release")
	got := versionString()
	if !strings.Contains(got, "channel release") {
		t.Errorf("versionString() = %q, want substring %q", got, "channel release")
	}
}
