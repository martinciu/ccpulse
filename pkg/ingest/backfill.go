package ingest

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Progress is sent on each step of a Backfill.Run. Callers in
// cmd/ccpulse translate it into a tui.IndexProgressMsg.
type Progress struct {
	Done   int
	Total  int
	Active bool
}

// Backfill catches the cache up to current EOF for every .jsonl
// under Ingester.ProjectsRoot, in newest-mtime-first order.
type Backfill struct {
	Ingester *Ingester

	// onBeforeProcess is a test hook. Production callers leave it nil.
	onBeforeProcess func(path string)
}

type backfillEntry struct {
	path  string
	mtime time.Time
}

// collectStaleFiles walks ProjectsRoot for .jsonl files whose stored offset
// differs from their current size, returning them newest-mtime first. Per-entry
// errors are logged and skipped; the walk never aborts on one bad file. The
// offsets map is a filter hint only — ProcessFile re-checks offset == size, so
// an empty map (after a query error) is safe.
func (b *Backfill) collectStaleFiles() []backfillEntry {
	offsets, err := b.Ingester.Cache.AllFileOffsets()
	if err != nil {
		LogFileError(b.Ingester.ParseErrorsLog, b.Ingester.ProjectsRoot, err)
		offsets = map[string]int64{}
	}

	var entries []backfillEntry
	_ = filepath.WalkDir(b.Ingester.ProjectsRoot, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			LogFileError(b.Ingester.ParseErrorsLog, p, err)
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			LogFileError(b.Ingester.ParseErrorsLog, p, err)
			return nil
		}
		if off, ok := offsets[p]; ok && off == info.Size() {
			return nil
		}
		entries = append(entries, backfillEntry{path: p, mtime: info.ModTime()})
		return nil
	})

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime.After(entries[j].mtime)
	})
	return entries
}

// notify forwards progress to onProgress when a callback is registered.
func notify(onProgress func(Progress), p Progress) {
	if onProgress != nil {
		onProgress(p)
	}
}

// Run enumerates .jsonl files, filters out files that are already
// caught up, sorts the remainder newest-mtime first, and feeds each
// through Ingester.ProcessFile. onProgress is only called when
// there is at least one file to process: Active:true Done:0 Total:N
// before the loop, (i+1)/N after each file, and once at the end
// with Active:false. When every file is already caught up (the
// common case immediately after `index --rebuild`), no progress
// callback fires and the TUI never shows the indicator. Honours
// ctx cancellation between files; never aborts on a single bad
// file. ProjectsRoot missing returns nil with no callbacks.
func (b *Backfill) Run(ctx context.Context, onProgress func(Progress)) error {
	// Pre-stat the root so a missing ProjectsRoot exits cleanly with zero
	// progress callbacks (the walker can't distinguish "root vanished" from
	// "one subdirectory unreadable").
	if _, err := os.Stat(b.Ingester.ProjectsRoot); err != nil {
		LogFileError(b.Ingester.ParseErrorsLog, b.Ingester.ProjectsRoot, err)
		return nil
	}

	entries := b.collectStaleFiles()
	total := len(entries)
	if total == 0 {
		// Nothing to do — don't show the indicator at all.
		return nil
	}

	notify(onProgress, Progress{Done: 0, Total: total, Active: true})

	for i, e := range entries {
		select {
		case <-ctx.Done():
			notify(onProgress, Progress{Done: i, Total: total, Active: false})
			return nil
		default:
		}

		if b.onBeforeProcess != nil {
			b.onBeforeProcess(e.path)
		}
		_, _ = b.Ingester.ProcessFile(e.path)

		notify(onProgress, Progress{Done: i + 1, Total: total, Active: true})
	}

	notify(onProgress, Progress{Done: total, Total: total, Active: false})
	return nil
}
