// Package watcher wraps fsnotify with a debounce so a flurry of
// writes to the same JSONL collapses into a single onChange callback.
package watcher

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"
)

type Watcher struct {
	w   *fsnotify.Watcher
	deb time.Duration
}

func New(root string) (*Watcher, error) {
	if _, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("watch root %s: %w", root, err)
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(root); err != nil {
		fw.Close()
		return nil, fmt.Errorf("watch root %s: %w", root, err)
	}
	// Walk the existing tree and add every subdirectory. Best-effort:
	// a walk error is non-fatal — the root itself is already watched.
	_ = filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if d != nil && d.IsDir() && p != root {
			_ = fw.Add(p)
		}
		return nil
	})
	return &Watcher{w: fw, deb: 100 * time.Millisecond}, nil
}

// Run consumes fsnotify events. For each .jsonl WRITE/CREATE event,
// debounces by w.deb and then calls onChange with the file path.
// Blocks until the watcher is closed; once Run returns, no further
// onChange calls fire (in-flight timers drop their event via the
// non-blocking send on `fire`).
//
// onChange is invoked sequentially from the Run loop's goroutine.
// If onChange is slow and many distinct files fire at once, events
// past the fireBufferSize cap are dropped silently — the buffer is
// sized generously (256) so in practice this never fires; a slow
// onChange is the more likely first symptom.
func (w *Watcher) Run(onChange func(path string)) {
	const fireBufferSize = 256
	// pending grows monotonically with the set of distinct files seen
	// during this Run — entries are not deleted on fire (Stop on a
	// fired timer is a no-op, and the next event for the same path
	// overwrites the entry). For ccpulse's expected file count this
	// is a non-issue; the deferred Stop loop cleans up on shutdown.
	pending := map[string]*time.Timer{}
	fire := make(chan string, fireBufferSize)
	defer func() {
		for _, t := range pending {
			t.Stop()
		}
	}()
	for {
		select {
		case e, ok := <-w.w.Events:
			if !ok {
				return
			}
			// Newly-created directory: subscribe so we see writes inside it.
			if e.Op&fsnotify.Create == fsnotify.Create {
				if info, err := os.Stat(e.Name); err == nil && info.IsDir() {
					_ = w.w.Add(e.Name)
					continue
				}
			}
			if !strings.HasSuffix(e.Name, ".jsonl") {
				continue
			}
			if e.Op&fsnotify.Write != fsnotify.Write &&
				e.Op&fsnotify.Create != fsnotify.Create {
				continue
			}
			name := e.Name
			if t, ok := pending[name]; ok {
				t.Stop()
			}
			pending[name] = time.AfterFunc(w.deb, func() {
				// Non-blocking: drops on shutdown (no receiver) and
				// on full buffer (slow onChange overwhelmed by burst).
				select {
				case fire <- name:
				default:
				}
			})
		case path := <-fire:
			onChange(path)
		case _, ok := <-w.w.Errors:
			if !ok {
				return
			}
			// ignore — fsnotify error stream
		}
	}
}

func (w *Watcher) Close() error { return w.w.Close() }
