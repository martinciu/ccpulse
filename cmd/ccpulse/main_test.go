package main

import "testing"

func TestVersionString(t *testing.T) {
	got := versionString()
	want := "ccpulse v0.0.0"
	if got != want {
		t.Fatalf("versionString() = %q, want %q", got, want)
	}
}
