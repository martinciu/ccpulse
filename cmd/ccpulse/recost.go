package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

func newRecostCmd() *cobra.Command {
	var dryRun, verbose bool
	c := &cobra.Command{
		Use:    "recost",
		Short:  "Re-cost message rows against the embedded pricing history",
		Hidden: true,
		Args:   cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(config.DefaultPath())
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
			hist, err := pricing.Load()
			if err != nil {
				return fmt.Errorf("load pricing: %w", err)
			}
			ca, err := cache.Open(filepath.Join(cacheDir, "state.db"))
			if err != nil {
				return fmt.Errorf("open cache: %w", err)
			}
			defer ca.Close()

			stats, err := ca.Recost(cmd.Context(), hist, cache.RecostOpts{DryRun: dryRun})
			if err != nil {
				return fmt.Errorf("recost: %w", err)
			}

			out := cmd.OutOrStdout()
			if dryRun {
				fmt.Fprintln(out, "Dry run — no rows written.")
			}
			fmt.Fprintf(out, "Scanned %d rows; %d updated; %d previously unknown -> %d after.\n",
				stats.Scanned, stats.Updated, stats.UnknownBefore, stats.UnknownAfter)
			if verbose && len(stats.ByVersion) > 0 {
				fmt.Fprintln(out, "By version:")
				vers := make([]string, 0, len(stats.ByVersion))
				for ver := range stats.ByVersion {
					vers = append(vers, ver)
				}
				sort.Strings(vers)
				for _, ver := range vers {
					fmt.Fprintf(out, "  %s: %d rows\n", ver, stats.ByVersion[ver])
				}
			}
			fmt.Fprintf(out, "Elapsed: %s\n", stats.Elapsed)
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "plan without writing")
	c.Flags().BoolVar(&verbose, "verbose", false, "print per-version breakdown")
	return c
}
