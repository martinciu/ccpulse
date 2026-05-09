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

// Run enumerates .jsonl files, sorts newest-mtime first, and feeds
// each one through Ingester.ProcessFile. onProgress is called with
// Active:true Done:0 Total:N before the loop starts, with each
// (i+1)/N step after each file, and once at the end with
// Active:false. Honours ctx cancellation between files. Never
// returns an error from a single bad file; only top-level walk
// failure (e.g. ProjectsRoot does not exist) returns nil with no
// progress callbacks.
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
		entries = append(entries, entry{path: p, mtime: info.ModTime()})
		return nil
	})

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].mtime.After(entries[j].mtime)
	})

	total := len(entries)
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
