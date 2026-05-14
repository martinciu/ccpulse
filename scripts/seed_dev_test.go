//go:build !windows

package scripts_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// scriptPath returns the absolute path of seed-dev.sh, regardless of the
// working directory go test runs from.
func scriptPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "seed-dev.sh")
}

// xdgEnv returns os.Environ() with XDG_* and HOME pinned to dir, plus
// any extra KEY=VALUE entries.
func xdgEnv(dir string, extra ...string) []string {
	env := []string{
		"HOME=" + dir,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(dir, ".cache"),
		// PATH passthrough so sqlite3 (or its absence) resolves correctly.
		"PATH=" + os.Getenv("PATH"),
	}
	return append(env, extra...)
}

func TestSeedDevConfig_CopiesReleasedTOML(t *testing.T) {
	dir := t.TempDir()
	releasedConfig := filepath.Join(dir, ".config", "ccpulse", "config.toml")
	if err := os.MkdirAll(filepath.Dir(releasedConfig), 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("# released config\n[paths]\ncache_dir = \"/explicit\"\n")
	if err := os.WriteFile(releasedConfig, body, 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(scriptPath(t), "config")
	cmd.Env = xdgEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed-dev.sh config failed: %v\n%s", err, out)
	}

	devConfig := filepath.Join(dir, ".config", "ccpulse-dev", "config.toml")
	got, err := os.ReadFile(devConfig)
	if err != nil {
		t.Fatalf("dev config not created: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("dev config contents mismatch:\ngot:  %q\nwant: %q", got, body)
	}
}

func TestSeedDevCache_CopiesReleasedDB(t *testing.T) {
	dir := t.TempDir()
	releasedCache := filepath.Join(dir, ".cache", "ccpulse")
	if err := os.MkdirAll(releasedCache, 0o755); err != nil {
		t.Fatal(err)
	}
	releasedDB := filepath.Join(releasedCache, "state.db")
	// Minimal SQLite "file" — not a real DB. The script's cp fallback
	// just copies bytes; the sqlite3 .backup path needs a real DB, so
	// run this test with a stripped PATH to force the fallback.
	dbBody := []byte("not-a-real-db-but-the-cp-fallback-doesnt-care")
	if err := os.WriteFile(releasedDB, dbBody, 0o644); err != nil {
		t.Fatal(err)
	}

	// Use a minimal PATH with system bins so bash/cp/mkdir resolve, but no
	// guarantee sqlite3 is absent (macOS ships /usr/bin/sqlite3). This test
	// covers the cp fallback path in TWO ways: when sqlite3 is missing
	// entirely, and when sqlite3 is present but the .backup invocation
	// fails on the fake non-database content we wrote. Either way, the
	// script must produce a byte-identical copy of the source.
	strippedPath := "/bin:/usr/bin:/usr/sbin:/sbin"
	cmd := exec.Command(scriptPath(t), "cache")
	cmd.Env = xdgEnv(dir, "PATH="+strippedPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed-dev.sh cache failed: %v\n%s", err, out)
	}

	devDB := filepath.Join(dir, ".cache", "ccpulse-dev", "state.db")
	got, err := os.ReadFile(devDB)
	if err != nil {
		t.Fatalf("dev cache db not created: %v", err)
	}
	if string(got) != string(dbBody) {
		t.Errorf("dev cache db mismatch:\ngot:  %q\nwant: %q", got, dbBody)
	}
}

func TestSeedDevCache_SurvivesMetacharsInCachePath(t *testing.T) {
	// Cache dir contains shell metacharacters — exercises the .backup
	// dot-command quoting path. The source DB is a real SQLite file so
	// sqlite3 will actually run .backup (rather than failing and falling
	// through to cp); without escaping, the broken SQL string would error
	// and the resulting copy would silently come from cp instead, masking
	// the bug we're guarding against. The marker row below lets the test
	// verify the sqlite3 path actually ran.
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatal("sqlite3 must be on PATH for this test; install sqlite3 in CI")
	}

	cases := []struct {
		name      string
		dirSuffix string
	}{
		{"single quote", "o'malley"},
		{"double quote", "o\"malley"},
		{"backslash", "o\\malley"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			quoted := filepath.Join(base, tc.dirSuffix)
			if err := os.MkdirAll(quoted, 0o755); err != nil {
				t.Fatal(err)
			}
			cacheHome := filepath.Join(quoted, ".cache")
			releasedCache := filepath.Join(cacheHome, "ccpulse")
			if err := os.MkdirAll(releasedCache, 0o755); err != nil {
				t.Fatal(err)
			}
			releasedDB := filepath.Join(releasedCache, "state.db")

			db, err := sql.Open("sqlite", releasedDB)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`CREATE TABLE marker (note TEXT); INSERT INTO marker VALUES ('hello-quote');`); err != nil {
				t.Fatal(err)
			}
			if err := db.Close(); err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(scriptPath(t), "cache")
			cmd.Env = []string{
				"HOME=" + quoted,
				"XDG_CONFIG_HOME=" + filepath.Join(quoted, ".config"),
				"XDG_CACHE_HOME=" + cacheHome,
				"PATH=" + os.Getenv("PATH"),
			}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("seed-dev.sh cache failed on quoted path: %v\n%s", err, out)
			}

			// The script prints distinct success markers for each branch:
			//   "(sqlite3 online backup)" vs "(cp fallback)". The fix is only
			//   exercised when the sqlite3 branch wins; otherwise the test would
			//   pass even without the escape because cp doesn't care about quoting.
			outStr := string(out)
			if !strings.Contains(outStr, "sqlite3 online backup") {
				t.Fatalf("expected sqlite3 .backup branch to run, but did not — quoting fix not exercised.\noutput:\n%s", outStr)
			}

			devDB := filepath.Join(cacheHome, "ccpulse-dev", "state.db")
			got, err := sql.Open("sqlite", devDB)
			if err != nil {
				t.Fatalf("dev cache db not openable: %v", err)
			}
			defer got.Close()
			var note string
			if err := got.QueryRow("SELECT note FROM marker").Scan(&note); err != nil {
				t.Fatalf("marker row missing from dev db: %v", err)
			}
			if note != "hello-quote" {
				t.Errorf("marker mismatch: got %q want %q", note, "hello-quote")
			}
		})
	}
}

func TestSeedDevConfig_RefusesWhenReleasedAbsent(t *testing.T) {
	dir := t.TempDir() // no released config exists

	cmd := exec.Command(scriptPath(t), "config")
	cmd.Env = xdgEnv(dir)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected non-zero exit; output:\n%s", out)
	}
}

func TestSeedDevUnknownSubcommand(t *testing.T) {
	dir := t.TempDir()
	cmd := exec.Command(scriptPath(t), "garbage")
	cmd.Env = xdgEnv(dir)
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Fatalf("expected non-zero exit on unknown subcommand; output:\n%s", out)
	}
}

func resetScriptPath(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(wd, "reset-dev.sh")
}

func TestResetDev_RemovesDevPathsOnly(t *testing.T) {
	dir := t.TempDir()
	devCfg := filepath.Join(dir, ".config", "ccpulse-dev")
	devCache := filepath.Join(dir, ".cache", "ccpulse-dev")
	relCfg := filepath.Join(dir, ".config", "ccpulse")
	relCache := filepath.Join(dir, ".cache", "ccpulse")
	for _, p := range []string{devCfg, devCache, relCfg, relCache} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p, "marker"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	cmd := exec.Command(resetScriptPath(t))
	cmd.Env = xdgEnv(dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("reset-dev.sh failed: %v\n%s", err, out)
	}

	if _, err := os.Stat(devCfg); !os.IsNotExist(err) {
		t.Errorf("dev config dir not removed: err=%v", err)
	}
	if _, err := os.Stat(devCache); !os.IsNotExist(err) {
		t.Errorf("dev cache dir not removed: err=%v", err)
	}
	if _, err := os.Stat(relCfg); err != nil {
		t.Errorf("released config dir was touched: err=%v", err)
	}
	if _, err := os.Stat(relCache); err != nil {
		t.Errorf("released cache dir was touched: err=%v", err)
	}
}
