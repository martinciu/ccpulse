// Package watcher wraps fsnotify with a debounce so a flurry of
// writes to the same JSONL collapses into a single onChange callback.
package watcher

import (
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
