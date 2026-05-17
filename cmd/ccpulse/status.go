package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON, quiet bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Print 5-hour window status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, asJSON, quiet)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	c.Flags().BoolVar(&quiet, "quiet", false, "suppress stdout (cache still written; stderr unchanged)")
	c.MarkFlagsMutuallyExclusive("json", "quiet")
	return c
}

func runStatus(cmd *cobra.Command, asJSON, quiet bool) error {
	cfg, err := loadConfigOrDefault(config.DefaultPath())
	if err != nil {
		return err
	}
	cacheDir := envOr("CCPULSE_CACHE_DIR", expand(cfg.Paths.CacheDir))
	dbPath := filepath.Join(cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return err
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return err
	}
	c.AutoRecost(cmd.Context(), hist)

	q := buildQuotaInput(cmd.Context(), cacheDir, time.Now(), cmd.ErrOrStderr())

	// Record a usage sample whenever Fetch returned genuinely fresh data.
	// Best-effort — failure to record never blocks the visible quota number.
	if q.Source == "api" && q.Usage != nil {
		if recErr := c.RecordUsageSample(*q.Usage, q.UpdatedAt); recErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "ccpulse: record sample: %v\n", recErr)
		}
		if cfg.History.RetentionDays > 0 {
			cutoff := time.Now().Add(-time.Duration(cfg.History.RetentionDays) * 24 * time.Hour)
			if _, prErr := c.PruneUsageSamples(cutoff); prErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "ccpulse: prune samples: %v\n", prErr)
			}
		}
	}

	w, err := status.Compute(c.DB(), time.Now(), q)
	if err != nil {
		return err
	}

	if quiet {
		return nil
	}

	switch {
	case asJSON:
		j, _ := status.JSON(w)
		fmt.Fprintln(cmd.OutOrStdout(), j)
	default:
		if w.Quota != nil {
			if w.MinutesToReset != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "5h window: %d%% used, resets in %dm\n", w.Percent, *w.MinutesToReset)
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "5h window: %d%% used, idle\n", w.Percent)
			}
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "5h window: no quota data — run 'claude /login' for percent display, or use --json for tokens/cost.")
		}
	}
	return nil
}

// buildQuotaInput resolves the credential and (best-effort) fetches usage data.
// Any failure → empty QuotaInput with TierSlug="unknown" so Compute falls back
// to the JSONL heuristic. Errors are silently swallowed except for diagnostics
// written to errOut.
func buildQuotaInput(ctx context.Context, cacheDir string, now time.Time, errOut io.Writer) status.QuotaInput {
	cred, err := anthro.LoadCredential()
	if err != nil {
		if !errors.Is(err, anthro.ErrNoCredential) {
			fmt.Fprintf(errOut, "ccpulse: %v\n", err)
		}
		return status.QuotaInput{TierSlug: "unknown", TierPretty: "Unknown"}
	}
	q := status.QuotaInput{
		TierSlug:   anthro.TierSlug(cred.RateLimitTier),
		TierPretty: anthro.TierPretty(cred.RateLimitTier),
	}
	if cred.Expired(now) {
		fmt.Fprintln(errOut, "ccpulse: OAuth credential expired — run /login in claude")
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := anthro.Fetch(fetchCtx, cred, cacheDir)
	if err != nil {
		return q // fall back; quota nil
	}
	q.Usage = &res.Usage
	q.Source = res.Source
	q.UpdatedAt = res.UpdatedAt
	return q
}
