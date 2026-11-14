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

// statusOpts carries the status command's flags.
type statusOpts struct {
	json, quiet, index, stdin bool
}

func newStatusCmd() *cobra.Command {
	var o statusOpts
	c := &cobra.Command{
		Use:   "status",
		Short: "Print 5-hour window status",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStatus(cmd, o)
		},
	}
	c.Flags().BoolVar(&o.json, "json", false, "emit JSON")
	c.Flags().BoolVar(&o.quiet, "quiet", false, "suppress stdout (cache still written; stderr unchanged)")
	c.Flags().BoolVar(&o.index, "index", false, "tail new JSONL into the cache before reporting")
	c.Flags().BoolVar(&o.stdin, "stdin", false, "read Claude Code's statusline JSON from stdin and ingest its rate_limits")
	c.MarkFlagsMutuallyExclusive("json", "quiet")
	return c
}

func runStatus(cmd *cobra.Command, o statusOpts) error {
	env, logCloser, err := bootstrap(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}
	// --stdin ingests before the SQLite cache is opened (and before recost
	// and --index backfill), so a locked or slow database can never drop a
	// statusline reading (#568).
	if o.stdin {
		if err := ingestStatuslineStdin(cmd, env.cacheDir, time.Now()); err != nil {
			return err
		}
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
	if o.index {
		indexed = backfillBeforeStatus(cmd, c, hist, env)
	}

	q, api := buildQuotaInput(cmd.Context(), env.cacheDir, time.Now(), cmd.ErrOrStderr())

	// Record a usage sample whenever Fetch returned genuinely fresh data, and
	// a settled statusline reading on --stdin runs. Best-effort — failure to
	// record never blocks the visible quota number.
	recordUsageSample(cmd, c, env.cfg, api)
	if o.stdin {
		recordStatuslineSample(cmd, c, env.cacheDir, time.Now())
	}

	w, err := status.Compute(cmd.Context(), c.DB(), time.Now(), q)
	if err != nil {
		return err
	}

	// Period rollups and the live throughput rate are JSON-only; the human and
	// --quiet paths skip the extra scans, and the TUI never reaches this code.
	if o.json {
		if err := addJSONRollups(cmd.Context(), c, &w, q); err != nil {
			return err
		}
	}

	if o.quiet {
		return nil
	}

	// Past the quiet early-return, so quiet is already excluded; the helper
	// handles the remaining --json / warm-cache gating.
	printIndexedCount(cmd.OutOrStdout(), indexed, o.json)

	printStatus(cmd.OutOrStdout(), w, o.json)
	return nil
}

// addJSONRollups fills the JSON-only period rollups and throughput rate.
func addJSONRollups(ctx context.Context, c *cache.Cache, w *status.Window, q status.QuotaInput) error {
	p, err := status.ComputePeriods(ctx, c.DB(), time.Now(), q)
	if err != nil {
		return err
	}
	w.Periods = p
	th, err := status.ComputeThroughput(ctx, c.DB(), time.Now())
	if err != nil {
		return err
	}
	w.Throughput = th
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
// past the retention horizon. It records the raw API result, never the merged
// quota: a statusline-overlaid bucket must not be filed as an 'api' row.
// Best-effort: failures go to stderr and never block the status output.
func recordUsageSample(cmd *cobra.Command, c *cache.Cache, cfg config.Config, api *anthro.FetchResult) {
	if api == nil || api.Source != "api" {
		return
	}
	errOut := cmd.ErrOrStderr()
	if recErr := c.RecordUsageSample(cmd.Context(), api.Usage, api.UpdatedAt); recErr != nil {
		fmt.Fprintf(errOut, "ccpulse: record sample: %v\n", recErr)
	}
	if cfg.History.RetentionDays > 0 {
		cutoff := time.Now().Add(-time.Duration(cfg.History.RetentionDays) * 24 * time.Hour)
		if _, prErr := c.PruneUsageSamples(cmd.Context(), cutoff); prErr != nil {
			fmt.Fprintf(errOut, "ccpulse: prune samples: %v\n", prErr)
		}
	}
}

// recordStatuslineSample files the stored statusline reading as a
// source='statusline' usage sample once it has settled (held 30 s), then marks
// it recorded so later runs skip it. Best-effort, like recordUsageSample.
func recordStatuslineSample(cmd *cobra.Command, c *cache.Cache, cacheDir string, now time.Time) {
	u, at, ok := anthro.PendingStatuslineSample(cacheDir, now)
	if !ok {
		return
	}
	errOut := cmd.ErrOrStderr()
	if err := c.RecordUsageSampleFrom(cmd.Context(), u, at, cache.SampleSourceStatusline); err != nil {
		fmt.Fprintf(errOut, "ccpulse: record statusline sample: %v\n", err)
		return
	}
	if err := anthro.MarkStatuslineRecorded(cacheDir, u, at); err != nil {
		fmt.Fprintf(errOut, "ccpulse: mark statusline sample: %v\n", err)
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

// buildQuotaInput resolves the credential, (best-effort) fetches usage data,
// and merges it with the statusline overlay. It also returns the raw API
// result (nil when there is none) for history recording.
//
// No credential skips Fetch; a Fetch failure is swallowed. Either way the
// overlay alone still yields quota (#568). Only when neither source has data
// is Usage nil, so Compute falls back to the JSONL heuristic. Diagnostics go
// to errOut.
func buildQuotaInput(ctx context.Context, cacheDir string, now time.Time, errOut io.Writer) (status.QuotaInput, *anthro.FetchResult) {
	q := status.QuotaInput{TierSlug: "unknown", TierPretty: "Unknown"}
	var api *anthro.FetchResult
	cred, err := anthro.LoadCredential()
	switch {
	case err != nil:
		if !errors.Is(err, anthro.ErrNoCredential) {
			fmt.Fprintf(errOut, "ccpulse: %v\n", err)
		}
	default:
		q.TierSlug = anthro.TierSlug(cred.RateLimitTier)
		q.TierPretty = anthro.TierPretty(cred.RateLimitTier)
		if cred.Expired(now) {
			fmt.Fprintln(errOut, "ccpulse: OAuth credential expired — run /login in claude")
		}
		fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		res, ferr := anthro.Fetch(fetchCtx, cred, cacheDir)
		cancel()
		if ferr == nil {
			api = &res
		}
	}

	merged, ok := anthro.MergeQuota(api, anthro.ReadOverlay(cacheDir, now), now)
	if !ok {
		return q, api
	}
	q.Usage = &merged.Usage
	q.Source = merged.Source
	q.UpdatedAt = merged.UpdatedAt
	q.APIUpdatedAt = merged.APIUpdatedAt()
	return q, api
}
