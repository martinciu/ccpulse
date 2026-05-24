package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/channel"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/devlog"
	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/secfile"
	"github.com/spf13/cobra"
)

func newIndexCmd() *cobra.Command {
	var rebuild bool
	c := &cobra.Command{
		Use:   "index",
		Short: "Rebuild SQLite cache from JSONL",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIndex(cmd, rebuild)
		},
	}
	c.Flags().BoolVar(&rebuild, "rebuild", false, "Drop the cache before scanning")
	return c
}

func runIndex(cmd *cobra.Command, rebuild bool) error {
	ctx := cmd.Context()
	if !rebuild {
		return errors.New("`ccpulse index` (no flag) was removed; the TUI now backfills on launch. Use `ccpulse index --rebuild` to drop and rebuild the cache from JSONL")
	}

	cfg, err := config.Load(config.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	projectsRoot := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	if err := secfile.MkdirAll(cacheDir); err != nil {
		return err
	}
	if logCloser, err := devlog.Init(devlog.Options{
		IsDev:    channel.IsDev(),
		CacheDir: cacheDir,
		Level:    resolvedLogLevel,
	}); err == nil && logCloser != nil {
		defer logCloser.Close()
	}
	dbPath := filepath.Join(cacheDir, "state.db")

	c, err := cache.LockedRebuild(dbPath)
	if err != nil {
		if errors.Is(err, cache.ErrLockHeld) {
			fmt.Fprintln(cmd.ErrOrStderr(),
				"ccpulse index --rebuild: cache locked by another ccpulse process. Close the TUI or any other ccpulse command and retry.")
		}
		return err
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return err
	}

	ing := &ingest.Ingester{
		Cache:          c,
		Pricing:        hist,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}
	bf := &ingest.Backfill{Ingester: ing}

	if err := bf.Run(ctx, nil); err != nil {
		return err
	}

	// Surface SIGINT/SIGTERM as a non-zero exit. Backfill.Run returns
	// nil on context cancel, so we check ctx ourselves.
	if err := ctx.Err(); err != nil {
		return err
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "rebuilt: %d messages\n", n)
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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
