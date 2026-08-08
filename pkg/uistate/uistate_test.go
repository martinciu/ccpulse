package uistate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveLoad_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := State{Zoom: "24h", View: "usage"}
	if err := Save(dir, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if got := Load(dir); got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestLoad_MissingFile_ReturnsZero(t *testing.T) {
	if got := Load(t.TempDir()); got != (State{}) {
		t.Errorf("Load = %+v, want zero State", got)
	}
}

func TestLoad_CorruptFile_ReturnsZero(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("not = [valid"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if got := Load(dir); got != (State{}) {
		t.Errorf("Load = %+v, want zero State", got)
	}
}

func TestLoad_UnknownKeys_Tolerated(t *testing.T) {
	// Forward compat: a newer ccpulse may add fields (e.g. breakdown);
	// this binary must still decode the fields it knows.
	dir := t.TempDir()
	body := "zoom = \"1h\"\nview = \"tokens\"\nbreakdown = \"models\"\n"
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	want := State{Zoom: "1h", View: "tokens"}
	if got := Load(dir); got != want {
		t.Errorf("Load = %+v, want %+v", got, want)
	}
}

func TestSave_FileMode0600(t *testing.T) {
	dir := t.TempDir()
	if err := Save(dir, State{Zoom: "15m", View: "cost"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := fi.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestSave_MissingDir_ReturnsError(t *testing.T) {
	// Save never creates the cache dir — bootstrap owns it. A missing dir
	// must surface as an error for the caller to log at debug.
	if err := Save(filepath.Join(t.TempDir(), "nope"), State{}); err == nil {
		t.Error("Save into missing dir: err = nil, want error")
	}
}
