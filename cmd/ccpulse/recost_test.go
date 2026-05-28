package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
)

func TestRecostUsesConfiguredCacheDir(t *testing.T) {
	// Arrange: set up temp dirs and pin channel to release so DefaultPath behaves consistently
	cacheDir := t.TempDir()
	cfgDir := t.TempDir()
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	channel.Set("release")
	t.Cleanup(func() { channel.Set("dev") })

	// Write config with the temp cache dir
	if err := os.MkdirAll(filepath.Join(cfgDir, "ccpulse"), 0o700); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(cfgDir, "ccpulse", "config.toml")
	configContent := `[paths]
cache_dir = "` + cacheDir + `"
`
	if err := os.WriteFile(configPath, []byte(configContent), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create and populate cache with at least one message so recost has something to scan
	dbPath := filepath.Join(cacheDir, "state.db")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := cache.Open(t.Context(), dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Act: run `recost --dry-run` and check it succeeds (meaning it found and used the configured cache dir)
	root := newRootCmd()
	root.SetArgs([]string{"recost", "--dry-run"})
	var out bytes.Buffer
	var errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)

	err = root.ExecuteContext(t.Context())
	if err != nil {
		t.Fatalf("recost --dry-run failed: %v\nstdout: %s\nstderr: %s", err, out.String(), errOut.String())
	}

	// Assert: output indicates the command completed successfully
	output := out.String()
	if output == "" {
		t.Error("recost --dry-run produced no output")
	}
	if !bytes.Contains(out.Bytes(), []byte("Scanned")) {
		t.Errorf("expected 'Scanned' in output, got: %s", output)
	}
}
