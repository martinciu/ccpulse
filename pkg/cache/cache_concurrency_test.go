package cache

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// TestConcurrentWritersNoBusyError exercises two distinct write paths
// (InsertMessages and RecordUsageSample) concurrently against one shared
// *Cache to confirm that SetMaxOpenConns(1) serializes all in-process
// writers and never produces SQLITE_BUSY / SQLITE_LOCKED errors.
//
// Before Fix 1 (pool cap), the unbounded database/sql pool let multiple
// goroutines each check out a distinct SQLite connection and race into
// concurrent write transactions. busy_timeout masked the contention up to
// 5 s but could not eliminate it on long-running write transactions such
// as the recost batch. This test would have flaked or failed with
// "database is locked" under the pre-fix code.
//
// Do NOT add t.Parallel() — withTimeLocal mutates the global time.Local
// (see TestNoParallelInCacheTests) and this package enforces no parallel
// tests.
func TestConcurrentWritersNoBusyError(t *testing.T) {
	c, err := Open(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	tab, _ := pricing.Load()
	ctx := context.Background()

	const (
		insertWorkers = 8  // goroutines calling InsertMessages
		sampleWorkers = 8  // goroutines calling RecordUsageSample
		opsPerWorker  = 50 // operations each goroutine performs
	)
	// Total goroutines: 16 (≥ 16 per brief). Total ops: 800.

	base := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

	var wg sync.WaitGroup
	errs := make(chan error, insertWorkers+sampleWorkers)

	// Spawn InsertMessages writers. Each goroutine inserts opsPerWorker
	// messages with distinct session IDs to avoid upsert collisions
	// masking errors.
	for w := range insertWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := range opsPerWorker {
				msg := parse.Message{
					SessionID:    fmt.Sprintf("w%d-s%d", workerID, i),
					MessageID:    fmt.Sprintf("w%d-m%d", workerID, i),
					ProjectSlug:  "concurrent-test",
					Model:        "claude-sonnet-4-6",
					Timestamp:    base.Add(time.Duration(workerID*opsPerWorker+i) * time.Second),
					InputTokens:  100,
					OutputTokens: 50,
				}
				if err := c.InsertMessages(ctx, []parse.Message{msg}, tab); err != nil {
					errs <- fmt.Errorf("InsertMessages worker=%d op=%d: %w", workerID, i, err)
					return
				}
			}
		}(w)
	}

	// Spawn RecordUsageSample writers. Each goroutine uses a ts offset
	// per (workerID, i) so INSERT OR IGNORE doesn't silently drop rows
	// (a no-op isn't an error, but distinct ts values surface any real
	// lock errors that would otherwise be swallowed by OR IGNORE).
	fiveHourBase := base.Add(5 * time.Hour)
	for w := range sampleWorkers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := range opsPerWorker {
				when := base.Add(time.Duration((insertWorkers+workerID)*opsPerWorker+i) * time.Second)
				resetsAt := fiveHourBase.Add(time.Duration(workerID) * time.Hour)
				u := anthro.Usage{
					FiveHour: &anthro.Bucket{
						Utilization: float64(i),
						ResetsAt:    &resetsAt,
					},
				}
				if err := c.RecordUsageSample(ctx, u, when); err != nil {
					errs <- fmt.Errorf("RecordUsageSample worker=%d op=%d: %w", workerID, i, err)
					return
				}
			}
		}(w)
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Errorf("concurrent write error: %v", err)
		}
	}
}
