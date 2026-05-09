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
	// Pre-stat the root so a missing ProjectsRoot exits cleanly with
	// zero progress callbacks. The walker below can't distinguish
	// "root vanished" from "one subdirectory unreadable" — we want
	// the former to skip the indicator entirely.
	if _, err := os.Stat(b.Ingester.ProjectsRoot); err != nil {
		LogFileError(b.Ingester.ParseErrorsLog, b.Ingester.ProjectsRoot, err)
		return nil
	}

	type entry struct {
		path  string
		mtime time.Time
	}

	// The visitor swallows per-entry errors (logged, return nil) so
	// the walk continues across permission glitches in single
	// subdirectories. Root-not-found is handled by the pre-stat
	// above, so WalkDir's return value is always nil here.
	//
	// We also drop files whose stored offset already matches the
	// current size — Ingester.ProcessFile would skip them anyway,
	// but enqueueing them here would inflate Total and tick the
	// indicator across files that need no work (very visible after
	// `index --rebuild`).
	var entries []entry
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
		_, offset, _, found, _ := b.Ingester.Cache.GetFile(p)
		if found && offset == info.Size() {
			return nil
		}
		entries = append(entries, entry{path: p, mtime: info.ModTime()})
		return nil
	})

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime.After(entries[j].mtime)
	})

	total := len(entries)
	if total == 0 {
		// Nothing to do — don't show the indicator at all.
		return nil
	}

	if onProgress != nil {
		onProgress(Progress{Done: 0, Total: total, Active: true})
	}

	for i, e := range entries {
		select {
		case <-ctx.Done():
			if onProgress != nil {
				onProgress(Progress{Done: i, Total: total, Active: false})
			}
			return nil
		default:
		}

		if b.onBeforeProcess != nil {
			b.onBeforeProcess(e.path)
		}
		_, _ = b.Ingester.ProcessFile(e.path)

		if onProgress != nil {
			onProgress(Progress{Done: i + 1, Total: total, Active: true})
		}
	}

	if onProgress != nil {
		onProgress(Progress{Done: total, Total: total, Active: false})
	}
	return nil
}
