package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/canonical"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/spf13/cobra"
)

func newIndexCmd() *cobra.Command {
	var rebuild bool
	c := &cobra.Command{
		Use:   "index",
		Short: "Rebuild SQLite cache from JSONL",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runIndex(rebuild)
		},
	}
	c.Flags().BoolVar(&rebuild, "rebuild", false, "Drop the cache before scanning")
	return c
}

func runIndex(rebuild bool) error {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	projectsRoot := envOr("CCPULSE_PROJECTS_ROOT", expand(cfg.Paths.ProjectsRoot))
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	if err := os.MkdirAll(cacheDir, 0755); err != nil {
		return err
	}
	dbPath := filepath.Join(cacheDir, "state.db")
	if rebuild {
		_ = os.Remove(dbPath)
	}
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

	msgs, err := parse.WalkProjects(projectsRoot)
	if err != nil {
		return err
	}
	if err := c.InsertMessages(msgs, tab); err != nil {
		return err
	}

	// Resolve slugs and back-fill messages.project_canonical so the
	// Projects tab can collapse worktrees correctly.
	res := canonical.NewResolver(c, "/")
	seen := map[string]bool{}
	for _, m := range msgs {
		if seen[m.ProjectSlug] {
			continue
		}
		seen[m.ProjectSlug] = true
		r, err := res.Resolve(m.ProjectSlug)
		if err != nil || r.CanonicalPath == "" {
			continue
		}
		if _, err := c.DB().Exec(
			`UPDATE messages SET project_canonical = ?, worktree_branch = ? WHERE project_slug = ?`,
			r.CanonicalPath, r.Branch, m.ProjectSlug,
		); err != nil {
			return err
		}
	}
	fmt.Printf("indexed %d messages from %d slugs\n", len(msgs), len(seen))
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
