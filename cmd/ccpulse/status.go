package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Print 5-hour window status",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, asJSON)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return c
}

func runStatus(cmd *cobra.Command, asJSON bool) error {
	cfg, _ := config.Load(config.DefaultPath())
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return err
	}
	defer c.Close()

	q := buildQuotaInput(cacheDir, time.Now())

	// Record a usage sample whenever Fetch returned genuinely fresh data.
	// Best-effort — failure to record never blocks the visible quota number.
	if q.Source == "api" && q.Usage != nil {
		if recErr := c.RecordUsageSample(*q.Usage, q.UpdatedAt); recErr != nil {
			fmt.Fprintf(os.Stderr, "ccpulse: record sample: %v\n", recErr)
		}
		if cfg.History.RetentionDays > 0 {
			cutoff := time.Now().Add(-time.Duration(cfg.History.RetentionDays) * 24 * time.Hour)
			if _, prErr := c.PruneUsageSamples(cutoff); prErr != nil {
				fmt.Fprintf(os.Stderr, "ccpulse: prune samples: %v\n", prErr)
			}
		}
	}

	w, err := status.Compute(c.DB(), time.Now(), q)
	if err != nil {
		return err
	}

	switch {
	case asJSON:
		j, _ := status.JSON(w)
		fmt.Fprintln(cmd.OutOrStdout(), j)
	default:
		if w.Percent > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "5h window: %d%% used, resets in %dm\n", w.Percent, w.MinutesToReset)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "5h window: $%.2f, resets in %dm\n", w.Cost5hUSD, w.MinutesToReset)
		}
	}
	return nil
}

// buildQuotaInput resolves the credential and (best-effort) fetches usage data.
// Any failure → empty QuotaInput with TierSlug="unknown" so Compute falls back
// to the JSONL heuristic. Errors are silently swallowed except for diagnostics
// to stderr (visible from the status command).
func buildQuotaInput(cacheDir string, now time.Time) status.QuotaInput {
	cred, err := anthro.LoadCredential()
	if err != nil {
		if !errors.Is(err, anthro.ErrNoCredential) {
			fmt.Fprintf(os.Stderr, "ccpulse: %v\n", err)
		}
		return status.QuotaInput{TierSlug: "unknown", TierPretty: "Unknown"}
	}
	q := status.QuotaInput{
		TierSlug:   anthro.TierSlug(cred.RateLimitTier),
		TierPretty: anthro.TierPretty(cred.RateLimitTier),
	}
	if cred.Expired(now) {
		fmt.Fprintln(os.Stderr, "ccpulse: OAuth credential expired — run /login in claude")
	}
	res, err := anthro.Fetch(context.Background(), cred, cacheDir)
	if err != nil {
		return q // fall back; quota nil
	}
	q.Usage = &res.Usage
	q.Source = res.Source
	q.UpdatedAt = res.UpdatedAt
	return q
}


