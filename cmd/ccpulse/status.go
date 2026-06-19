package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
	"github.com/martinciu/ccpulse/pkg/status"
	"github.com/spf13/cobra"
)

func newStatusCmd() *cobra.Command {
	var asJSON, quiet, doIndex bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Print 5-hour window status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, asJSON, quiet, doIndex)
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	c.Flags().BoolVar(&quiet, "quiet", false, "suppress stdout (cache still written; stderr unchanged)")
	c.Flags().BoolVar(&doIndex, "index", false, "tail new JSONL into the cache before reporting")
	c.MarkFlagsMutuallyExclusive("json", "quiet")
	return c
}

func runStatus(cmd *cobra.Command, asJSON, quiet, doIndex bool) error {
	env, logCloser, err := bootstrap(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	c, err := openCacheOrHint(cmd.Context(), env.dbPath, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return err
	}
	c.AutoRecost(cmd.Context(), hist)

	// --index: tail any not-yet-indexed JSONL into the cache before computing
	// the window, so a standalone status (prompt/hook/cron, no TUI watching)
	// reports fresh token/cost history. Opt-in — bare status stays cheap.
	var indexed int
	if doIndex {
		indexed = backfillBeforeStatus(cmd, c, hist, env)
	}

	q := buildQuotaInput(cmd.Context(), env.cacheDir, time.Now(), cmd.ErrOrStderr())

	// Record a usage sample whenever Fetch returned genuinely fresh data.
	// Best-effort — failure to record never blocks the visible quota number.
	recordUsageSample(cmd, c, env.cfg, q)

	w, err := status.Compute(cmd.Context(), c.DB(), time.Now(), q)
	if err != nil {
		return err
	}

	// Period rollups and the live throughput rate are JSON-only; the human and
	// --quiet paths skip the extra scans, and the TUI never reaches this code.
	if asJSON {
		p, err := status.ComputePeriods(cmd.Context(), c.DB(), time.Now(), q)
		if err != nil {
			return err
		}
		w.Periods = p

		th, err := status.ComputeThroughput(cmd.Context(), c.DB(), time.Now())
		if err != nil {
			return err
		}
		w.Throughput = th
	}

	if quiet {
		return nil
	}

	// Past the quiet early-return, so quiet is already excluded; the helper
	// handles the remaining --json / warm-cache gating.
	printIndexedCount(cmd.OutOrStdout(), indexed, asJSON)

	printStatus(cmd.OutOrStdout(), w, asJSON)
	return nil
}

// printIndexedCount writes the "indexed N file(s)" summary for --index. Gated:
// nothing under --json (the line would corrupt the JSON document) and nothing
// on a warm cache (indexed == 0 → no noise on every prompt invocation). Callers
// must already have excluded --quiet.
func printIndexedCount(out io.Writer, indexed int, asJSON bool) {
	if indexed == 0 || asJSON {
		return
	}
	noun := "files"
	if indexed == 1 {
		noun = "file"
	}
	fmt.Fprintf(out, "indexed %d %s\n", indexed, noun)
}

// backfillBeforeStatus tails any not-yet-indexed JSONL into the cache before
// status reports. Best-effort: per-file parse errors are logged to
// parse-errors.log by the ingester and never surface here, matching how
// recordUsageSample and buildQuotaInput swallow their errors. Returns the
// number of stale files processed — those whose stored cache offset differs
// from the file's current size (0 when the cache was already current).
func backfillBeforeStatus(cmd *cobra.Command, c *cache.Cache, hist pricing.History, env appEnv) int {
	ing := newIngester(c, hist, env)
	bf := &ingest.Backfill{Ingester: ing}

	var indexed int
	// Backfill.Run fires the terminal Active:false tick with Done == files
	// processed (Done:total on normal completion, Done:i on ctx-cancel). When
	// every file is already caught up it returns early with no callback, so
	// indexed stays 0. Errors are best-effort — discard the return.
	_ = bf.Run(cmd.Context(), func(p ingest.Progress) {
		if !p.Active {
			indexed = p.Done
		}
	})
	return indexed
}

// recordUsageSample persists a freshly-fetched usage sample and prunes samples
// past the retention horizon. Best-effort: failures go to stderr and never block
// the status output.
func recordUsageSample(cmd *cobra.Command, c *cache.Cache, cfg config.Config, q status.QuotaInput) {
	if q.Source != "api" || q.Usage == nil {
		return
	}
	errOut := cmd.ErrOrStderr()
	if recErr := c.RecordUsageSample(cmd.Context(), *q.Usage, q.UpdatedAt); recErr != nil {
		fmt.Fprintf(errOut, "ccpulse: record sample: %v\n", recErr)
	}
	if cfg.History.RetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(cfg.History.RetentionDays) * 24 * time.Hour)
		if _, prErr := c.PruneUsageSamples(cmd.Context(), cutoff); prErr != nil {
			fmt.Fprintf(errOut, "ccpulse: prune samples: %v\n", prErr)
		}
	}
}

// printStatus renders the computed window to out, as JSON or the human summary.
func printStatus(out io.Writer, w status.Window, asJSON bool) {
	if asJSON {
		j, _ := status.JSON(w)
		fmt.Fprintln(out, j)
		return
	}
	if w.Quota == nil {
		fmt.Fprintln(out, "5h window: no quota data — run 'claude /login' for percent display, or use --json for tokens/cost.")
		return
	}
	if w.MinutesToReset != nil {
		fmt.Fprintf(out, "5h window: %d%% used, resets in %dm\n", w.Percent, *w.MinutesToReset)
		return
	}
	fmt.Fprintf(out, "5h window: %d%% used, idle\n", w.Percent)
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
