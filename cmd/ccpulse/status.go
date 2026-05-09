package main

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON, asTmux bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Print 5-hour window status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, asJSON, asTmux)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	c.Flags().BoolVar(&asTmux, "tmux", false, "tmux-formatted single line")
	return c
}

func runStatus(cmd *cobra.Command, asJSON, asTmux bool) error {
	cfg, _ := config.Load(config.DefaultPath())
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		// Cache missing: tmux mode prints nothing; others print 0s.
		if asTmux {
			return nil
		}
		return err
	}
	defer c.Close()

	ceiling := ceilingFor(cfg.Plan)
	w, err := status.Compute(c.DB(), time.Now(), cfg.Plan.Tier, ceiling)
	if err != nil {
		if asTmux {
			return nil
		}
		return err
	}

	switch {
	case asJSON:
		j, _ := status.JSON(w)
		fmt.Fprintln(cmd.OutOrStdout(), j)
	case asTmux:
		fmt.Fprint(cmd.OutOrStdout(), status.TmuxLine(w, cfg.Plan))
	default:
		fmt.Fprintf(cmd.OutOrStdout(), "5h window: %d%% used, resets in %dm\n", w.Percent, w.MinutesToReset)
	}
	return nil
}

func ceilingFor(p config.Plan) int64 {
	switch p.Tier {
	case "custom":
		return p.CustomCeilingTokens
	case "max_5x":
		return 60_000_000 // tune during impl after reading current docs
	case "max_20x":
		return 240_000_000
	case "pro":
		return 12_000_000
	case "api":
		return 0
	}
	return 0
}
