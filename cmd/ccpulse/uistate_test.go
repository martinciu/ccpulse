package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/martinciu/ccpulse/pkg/uistate"
)

// oneByteReader yields a single byte per Read so each rune arrives as its
// own KeyMsg. A strings.Reader hands the whole string over in one Read,
// which bubbletea coalesces into ONE multi-rune KeyMsg ("zuq") whose
// String() matches no binding — the keys are then silently dropped and
// the program never even sees the 'q'.
type oneByteReader struct {
	s []byte
	i int
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.s[r.i]
	r.i++
	return 1, nil
}

// injectKeys points newTeaProgram at a headless program whose stdin is
// the given key sequence — the established way to drive runTUI's full
// lifecycle without a TTY (mirrors TestRunTUI_AbsentConfigUsesDefaults).
func injectKeys(t *testing.T, keys string) {
	t.Helper()
	original := newTeaProgram
	t.Cleanup(func() { newTeaProgram = original })
	newTeaProgram = func(m tea.Model) *tea.Program {
		return tea.NewProgram(m,
			tea.WithoutRenderer(),
			tea.WithInput(&oneByteReader{s: []byte(keys)}),
			tea.WithOutput(io.Discard),
		)
	}
}

// uiStateEnv isolates config/projects/cache and returns the cache dir.
func uiStateEnv(t *testing.T) string {
	t.Helper()
	cacheDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config-empty"))
	t.Setenv("CCPULSE_PROJECTS_ROOT", t.TempDir())
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	// Stub the quota poller with a synthetic sample so resolveQuotaStartup
	// never calls anthro.LoadCredential/the usage API — without this, a
	// machine with a real keychain credential fires a live HTTPS request
	// to api.anthropic.com and leaves a TLS connection open past the test.
	t.Setenv("CCPULSE_FAKE_QUOTA", "55,42")
	return cacheDir
}

// TestRunTUI_PersistsUIStateOnKeypress is the write half of issue #490's
// acceptance criteria, driven through the real runTUI wiring: launch with
// no state file, press 'z' then 'u', quit — the file must exist and hold
// the post-press zoom and view.
func TestRunTUI_PersistsUIStateOnKeypress(t *testing.T) {
	cacheDir := uiStateEnv(t)
	injectKeys(t, "zuq")

	if err := runTUI(t.Context(), io.Discard); err != nil {
		t.Fatalf("runTUI: %v", err)
	}

	got := uistate.Load(cacheDir)
	want := uistate.State{Zoom: "1h", View: "tokens"}
	if got != want {
		t.Errorf("persisted state = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(cacheDir, uistate.FileName)); err != nil {
		t.Errorf("Stat %s: %v", uistate.FileName, err)
	}
}

// TestRunTUI_RestoresPersistedUIState is the read half, and closes the
// loop end-to-end (Load → New restore → keypress → Save).
//
// The assertion turns on where a single 'z' lands. Restored, the model
// opens at 24h (the last ZoomLevels entry), so 'z' wraps to 15m. Without
// restore it would open at 15m and 'z' would advance to 1h. The view is
// untouched by 'z', so a persisted "usage" surviving the round-trip
// proves the view was restored rather than reset to cost.
func TestRunTUI_RestoresPersistedUIState(t *testing.T) {
	cacheDir := uiStateEnv(t)
	seed := []byte("zoom = \"24h\"\nview = \"usage\"\n")
	if err := os.WriteFile(filepath.Join(cacheDir, uistate.FileName), seed, 0o600); err != nil {
		t.Fatalf("seed ui-state.toml: %v", err)
	}
	injectKeys(t, "zq")

	if err := runTUI(t.Context(), io.Discard); err != nil {
		t.Fatalf("runTUI: %v", err)
	}

	got := uistate.Load(cacheDir)
	want := uistate.State{Zoom: "15m", View: "usage"}
	if got != want {
		t.Errorf("state after restore+z = %+v, want %+v "+
			"(Zoom \"1h\" would mean the persisted 24h was never restored; "+
			"View \"cost\" would mean the persisted view was dropped)", got, want)
	}
}

// TestRunTUI_CorruptUIStateFallsBackSilently asserts a malformed state
// file neither fails the launch nor leaks into the restored state — the
// TUI opens at its defaults (15m/cost), so 'z' advances 15m → 1h.
//
// The fixture is deliberately partially decodable, not merely malformed:
// BurntSushi decodes Zoom = "24h" first, then fails on View (int64, wants
// string). A plain parse error never populates the local State, so
// falling back to defaults and leaking the partially-decoded Zoom would
// be indistinguishable — this fixture forces the two apart.
func TestRunTUI_CorruptUIStateFallsBackSilently(t *testing.T) {
	cacheDir := uiStateEnv(t)
	if err := os.WriteFile(filepath.Join(cacheDir, uistate.FileName), []byte("zoom = \"24h\"\nview = 42\n"), 0o600); err != nil {
		t.Fatalf("seed corrupt ui-state.toml: %v", err)
	}
	injectKeys(t, "zq")

	if err := runTUI(t.Context(), io.Discard); err != nil {
		t.Fatalf("runTUI with corrupt ui-state.toml: %v", err)
	}

	got := uistate.Load(cacheDir)
	want := uistate.State{Zoom: "1h", View: "cost"}
	if got != want {
		t.Errorf("state after corrupt-file launch + z = %+v, want %+v (defaults)", got, want)
	}
}
