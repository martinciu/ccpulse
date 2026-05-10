package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSeedYear_RejectsReleaseCacheDir(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "ccpulse") // no -dev suffix
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		t.Fatal(err)
	}

	_, _, err := runSeed(seedOpts{
		profile:  "light",
		cacheDir: cacheDir,
		seed:     1,
		days:     1,
	})
	if err == nil {
		t.Fatal("runSeed: expected error for non-dev cache dir, got nil")
	}
	if !strings.Contains(err.Error(), "-dev") {
		t.Errorf("runSeed: error %q does not mention '-dev'", err)
	}

	if _, err := os.Stat(filepath.Join(cacheDir, "state.db")); err == nil {
		t.Error("state.db was created despite path-guard rejection")
	}
}
