package state_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/martinciu/ccpulse/pkg/state"
)

func TestSave_FreshModes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := state.Save(state.State{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	leaf := filepath.Join(dir, "ccpulse")
	if got, _ := os.Stat(leaf); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(filepath.Join(leaf, "state.json")); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}

func TestSave_TightensExisting(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	leaf := filepath.Join(dir, "ccpulse")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(leaf, "state.json"), []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := state.Save(state.State{}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got, _ := os.Stat(leaf); got.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode: got %o want %o", got.Mode().Perm(), 0o700)
	}
	if got, _ := os.Stat(filepath.Join(leaf, "state.json")); got.Mode().Perm() != 0o600 {
		t.Fatalf("file mode: got %o want %o", got.Mode().Perm(), 0o600)
	}
}
