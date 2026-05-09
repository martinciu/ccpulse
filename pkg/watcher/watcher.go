// Package watcher wraps fsnotify with a debounce so a flurry of
// writes to the same JSONL collapses into a single onChange callback.
package watcher

import (
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
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fw.Add(root); err != nil {
		fw.Close()
		return nil, err
	}
	// Walk the existing tree and add every subdirectory.
	if err := filepath.WalkDir(root, func(p string, d os.DirEntry, _ error) error {
		if d != nil && d.IsDir() && p != root {
			_ = fw.Add(p)
		}
		return nil
	}); err != nil {
		// non-fatal: best-effort
	}
	return &Watcher{w: fw, deb: 100 * time.Millisecond}, nil
}

// Run consumes fsnotify events. For each .jsonl WRITE/CREATE event,
// debounces 100ms and then calls onChange with the file path. Blocks
// until the watcher is closed.
func (w *Watcher) Run(onChange func(path string)) {
	pending := map[string]*time.Timer{}
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
			if !(e.Op&fsnotify.Write == fsnotify.Write ||
				e.Op&fsnotify.Create == fsnotify.Create) {
				continue
			}
			name := e.Name
			if t, ok := pending[name]; ok {
				t.Stop()
			}
			pending[name] = time.AfterFunc(w.deb, func() { onChange(name) })
		case <-w.w.Errors:
			// ignore — fsnotify error stream
		}
	}
}

func (w *Watcher) Close() error { return w.w.Close() }
