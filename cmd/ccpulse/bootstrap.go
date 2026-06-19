package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/projects"
	"github.com/martinciu/ccpulse/pkg/secfile"
)

// appEnv is the resolved per-command environment: the loaded config plus the
// env-var-overridable paths derived from it.
type appEnv struct {
	cfg          config.Config
	cacheDir     string
	projectsRoot string
	dbPath       string
}

// deriveEnv computes the env-var-overridable paths from an already-loaded
// config. Pure: no filesystem I/O, no side effects. Shared by bootstrap (the
// work commands) and doctor (which keeps its own non-aborting config.Load +
// check and must not create directories or open the log file).
func deriveEnv(cfg config.Config) appEnv {
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	return appEnv{
		cfg:          cfg,
		cacheDir:     cacheDir,
		projectsRoot: envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot)),
		dbPath:       filepath.Join(cacheDir, "state.db"),
	}
}

// bootstrap is the shared startup prefix for the work commands (runTUI, index,
// status, recost): load config (tolerating a missing file), derive paths,
// create the cache dir, and init devlog. Returns the resolved env and the
// devlog closer (the caller MUST defer Close when it is non-nil). devlog
// failure is best-effort — initDevlog prints a hint to errOut and returns a
// nil closer; it is never fatal here.
//
// Takes errOut (not *cobra.Command) so runTUI — whose signature is
// runTUI(ctx, errOut) for direct test invocation — can call it.
func bootstrap(errOut io.Writer) (appEnv, io.Closer, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return appEnv{}, nil, fmt.Errorf("load config %s: %w", config.DefaultPath(), err)
	}
	env := deriveEnv(cfg)
	if err := secfile.MkdirAll(env.cacheDir); err != nil {
		return appEnv{}, nil, err
	}
	closer := initDevlog(channel.IsDev(), env.cacheDir, resolvedLogLevel, errOut)
	return env, closer, nil
}

// lockHeldHint is the single canonical message printed when the cache is locked
// by another ccpulse process. Every lock-held site funnels through lockHint so
// the wording cannot drift (issue #421).
const lockHeldHint = "ccpulse: cache locked by another ccpulse process. " +
	"Close any other ccpulse (a TUI in another terminal, or `ccpulse index --rebuild`) and retry."

// lockHint prints the canonical lock-held message to errOut.
func lockHint(errOut io.Writer) {
	fmt.Fprintln(errOut, lockHeldHint)
}

// openCacheOrHint opens the cache read/write, printing the canonical lock hint
// on ErrLockHeld. Used by status, recost, and runTUI's first open.
func openCacheOrHint(ctx context.Context, dbPath string, errOut io.Writer) (*cache.Cache, error) {
	c, err := cache.Open(ctx, dbPath)
	if err != nil {
		if errors.Is(err, cache.ErrLockHeld) {
			lockHint(errOut)
		}
		return nil, err
	}
	return c, nil
}

// lockedRebuildOrHint drops and rebuilds the cache from JSONL, printing the
// canonical lock hint on ErrLockHeld. Used by index and runTUI's rebuild branch.
func lockedRebuildOrHint(ctx context.Context, dbPath string, errOut io.Writer) (*cache.Cache, error) {
	c, err := cache.LockedRebuild(ctx, dbPath)
	if err != nil {
		if errors.Is(err, cache.ErrLockHeld) {
			lockHint(errOut)
		}
		return nil, err
	}
	return c, nil
}

// newIngester builds the ingester used by the cold-walk and watcher paths.
// Replaces the verbatim ingest.Ingester{...} literal previously pasted in
// runTUI, runIndex, and status's backfillBeforeStatus.
func newIngester(c *cache.Cache, hist pricing.History, env appEnv) *ingest.Ingester {
	return &ingest.Ingester{
		Cache:          c,
		Pricing:        hist,
		ProjectsRoot:   env.projectsRoot,
		ParseErrorsLog: filepath.Join(env.cacheDir, "parse-errors.log"),
		Resolver:       projects.New(),
	}
}

// envOr returns the environment variable key if set and non-empty, else
// fallback. Relocated here from index.go — every command file uses it.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// expand resolves a leading ~ to the user's home directory. Relocated here
// from index.go.
func expand(p string) string {
	if p == "" {
		return p
	}
	if p[0] == '~' {
		home, _ := os.UserHomeDir()
		return home + p[1:]
	}
	return p
}
