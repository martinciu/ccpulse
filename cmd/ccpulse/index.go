package main

import (
	"errors"
	"fmt"

	"github.com/martinciu/ccpulse/pkg/ingest"
	"github.com/martinciu/ccpulse/pkg/pricing"
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

	env, logCloser, err := bootstrap(cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	if logCloser != nil {
		defer logCloser.Close()
	}

	c, err := lockedRebuildOrHint(ctx, env.dbPath, cmd.ErrOrStderr())
	if err != nil {
		return err
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return err
	}

	ing := newIngester(c, hist, env)
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
	if err := c.DB().QueryRowContext(ctx, `SELECT count(*) FROM messages`).Scan(&n); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "rebuilt: %d messages\n", n)
	return nil
}
