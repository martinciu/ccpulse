package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
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
			return runIndex(rebuild)
		},
	}
	c.Flags().BoolVar(&rebuild, "rebuild", false, "Drop the cache before scanning")
	return c
}

func runIndex(rebuild bool) error {
	if !rebuild {
		return fmt.Errorf("`ccpulse index` (no flag) was removed; the TUI now backfills on launch. Use `ccpulse index --rebuild` to drop and rebuild the cache from JSONL")
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
	if logCloser, err := devlog.Init(channel.IsDev(), cacheDir); err == nil && logCloser != nil {
		defer logCloser.Close()
	}
	dbPath := filepath.Join(cacheDir, "state.db")

	_ = cache.RemoveWithSiblings(dbPath)

	c, err := cache.Open(dbPath)
	if err != nil {
		return err
	}
	defer c.Close()

	tab, err := pricing.Load()
	if err != nil {
		return err
	}
	if cfg.Pricing.Override != "" {
		if t, err := pricing.LoadFrom(expand(cfg.Pricing.Override)); err == nil {
			tab = t
		}
	}

	res := canonical.NewResolver(c, "/")
	ing := &ingest.Ingester{
		Cache:          c,
		Resolver:       res,
		Pricing:        tab,
		ProjectsRoot:   projectsRoot,
		ParseErrorsLog: filepath.Join(cacheDir, "parse-errors.log"),
	}
	bf := &ingest.Backfill{Ingester: ing}

	if err := bf.Run(context.Background(), nil); err != nil {
		return err
	}

	var n int
	if err := c.DB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n); err != nil {
		return err
	}
	fmt.Printf("rebuilt: %d messages\n", n)
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
