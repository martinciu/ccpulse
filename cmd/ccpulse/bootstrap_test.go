package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/secfile"
)

func TestDeriveEnv_EnvOverridesAndExpand(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	// No env vars set → values come from cfg, with ~ expanded.
	t.Setenv("CCPULSE_CACHE_DIR", "")
	t.Setenv("CCPULSE_PROJECTS_ROOT", "")
	cfg := config.Config{}
	cfg.Paths.CacheDir = "~/.cache/ccpulse-x"
	cfg.Paths.ProjectsRoot = "~/projects-x"

	env := deriveEnv(cfg)
	if want := filepath.Join(home, ".cache/ccpulse-x"); env.cacheDir != want {
		t.Errorf("cacheDir = %q, want %q", env.cacheDir, want)
	}
	if want := filepath.Join(home, "projects-x"); env.projectsRoot != want {
		t.Errorf("projectsRoot = %q, want %q", env.projectsRoot, want)
	}
	if want := filepath.Join(home, ".cache/ccpulse-x", "state.db"); env.dbPath != want {
		t.Errorf("dbPath = %q, want %q", env.dbPath, want)
	}

	// Env vars win over cfg and are not tilde-expanded (already absolute).
	t.Setenv("CCPULSE_CACHE_DIR", "/tmp/cc-cache")
	t.Setenv("CCPULSE_PROJECTS_ROOT", "/tmp/cc-projects")
	env = deriveEnv(cfg)
	if env.cacheDir != "/tmp/cc-cache" {
		t.Errorf("cacheDir override = %q, want /tmp/cc-cache", env.cacheDir)
	}
	if env.projectsRoot != "/tmp/cc-projects" {
		t.Errorf("projectsRoot override = %q, want /tmp/cc-projects", env.projectsRoot)
	}
	if env.dbPath != "/tmp/cc-cache/state.db" {
		t.Errorf("dbPath = %q, want /tmp/cc-cache/state.db", env.dbPath)
	}
}

func TestBootstrap_AbsentConfigUsesDefaults(t *testing.T) {
	// XDG points at a dir with no ccpulse-dev/config.toml → os.IsNotExist,
	// tolerated; cache dir is created.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "no-config-here"))
	cacheDir := filepath.Join(t.TempDir(), "made-by-bootstrap")
	t.Setenv("CCPULSE_CACHE_DIR", cacheDir)
	t.Setenv("CCPULSE_PROJECTS_ROOT", t.TempDir())

	env, closer, err := bootstrap(&strings.Builder{})
	if err != nil {
		t.Fatalf("bootstrap absent config: %v", err)
	}
	if closer != nil {
		t.Cleanup(func() { _ = closer.Close() })
	}
	if env.cacheDir != cacheDir {
		t.Errorf("cacheDir = %q, want %q", env.cacheDir, cacheDir)
	}
	if fi, statErr := os.Stat(cacheDir); statErr != nil || !fi.IsDir() {
		t.Errorf("bootstrap did not create cache dir %q: %v", cacheDir, statErr)
	}
}

func TestBootstrap_MalformedConfigErrors(t *testing.T) {
	cfgDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", cfgDir)
	t.Setenv("CCPULSE_CACHE_DIR", t.TempDir())
	t.Setenv("CCPULSE_PROJECTS_ROOT", t.TempDir())
	// channel is "dev" in tests → DefaultPath() = $XDG_CONFIG_HOME/ccpulse-dev/config.toml
	dir := filepath.Join(cfgDir, "ccpulse-dev")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.toml"), []byte("[[broken\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, closer, err := bootstrap(&strings.Builder{})
	if closer != nil {
		_ = closer.Close()
	}
	if err == nil {
		t.Fatal("bootstrap should error on malformed config")
	}
	var perr toml.ParseError
	if !errors.As(err, &perr) {
		t.Errorf("error should unwrap to toml.ParseError, got: %v", err)
	}
}

func TestOpenCacheWrappers_EmitCanonicalLockHint(t *testing.T) {
	// Pre-lock the cache lock file from a sibling fd so Open/LockedRebuild
	// hit ErrLockHeld. Mirrors TestDoctor_SurfacesLockHeld.
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.db")
	lockPath := dbPath + ".lock"
	holder, err := secfile.OpenFile(lockPath, os.O_RDWR|os.O_CREATE)
	if err != nil {
		t.Fatalf("open holder: %v", err)
	}
	t.Cleanup(func() { _ = holder.Close() })
	if err := syscall.Flock(int(holder.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}

	var b1, b2 strings.Builder
	if c, err := openCacheOrHint(t.Context(), dbPath, &b1); err == nil {
		c.Close()
		t.Fatal("openCacheOrHint should fail on locked cache")
	}
	if c, err := lockedRebuildOrHint(t.Context(), dbPath, &b2); err == nil {
		c.Close()
		t.Fatal("lockedRebuildOrHint should fail on locked cache")
	}
	if !strings.Contains(b1.String(), lockHeldHint) {
		t.Errorf("openCacheOrHint hint = %q, want canonical %q", b1.String(), lockHeldHint)
	}
	if !strings.Contains(b2.String(), lockHeldHint) {
		t.Errorf("lockedRebuildOrHint hint = %q, want canonical %q", b2.String(), lockHeldHint)
	}
}

func TestNewIngester_MapsEnvFields(t *testing.T) {
	env := appEnv{cacheDir: "/c", projectsRoot: "/p"}
	ing := newIngester(nil, pricing.History{}, env)
	if ing.ProjectsRoot != "/p" {
		t.Errorf("ProjectsRoot = %q, want /p", ing.ProjectsRoot)
	}
	if ing.ParseErrorsLog != "/c/parse-errors.log" {
		t.Errorf("ParseErrorsLog = %q, want /c/parse-errors.log", ing.ParseErrorsLog)
	}
	if ing.Resolver == nil {
		t.Error("Resolver should be non-nil")
	}
}
