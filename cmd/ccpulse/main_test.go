package main

import "testing"

func TestVersionString(t *testing.T) {
	got := versionString()
	want := "ccpulse v0.0.0"
	if got != want {
		t.Fatalf("versionString() = %q, want %q", got, want)
	}
}

func TestRootCommandRegistersSubcommands(t *testing.T) {
	root := newRootCmd()
	want := []string{"status", "index", "config", "doctor", "version"}
	for _, name := range want {
		cmd, _, err := root.Find([]string{name})
		if err != nil || cmd == nil || cmd == root {
			t.Errorf("subcommand %q not registered", name)
		}
	}
}
