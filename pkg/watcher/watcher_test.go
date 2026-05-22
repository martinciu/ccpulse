package watcher

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNew_MissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	w, err := New(missing)
	if w != nil {
		t.Fatalf("New returned a non-nil watcher for a missing root: %v", w)
	}
	if err == nil {
		t.Fatal("New returned nil error for a missing root; want fs.ErrNotExist")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("errors.Is(err, fs.ErrNotExist) = false; err = %v", err)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error %q does not name the missing path %q", err, missing)
	}
}

func TestWatcherEmitsOnWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	events := make(chan string, 10)
	done := make(chan struct{})
	go func() {
		w.Run(func(path string) { events <- path })
		close(done)
	}()
	defer func() {
		_ = w.Close()
		<-done
	}()

	target := filepath.Join(dir, "x.jsonl")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-events:
		if got != target {
			t.Errorf("got %s, want %s", got, target)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for fsnotify event")
	}
}

func TestWatcherNoCallbackAfterClose(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Use a long debounce so we can reliably interleave: fsnotify delivers
	// the event into Run and Run schedules a timer, then Close happens
	// during the debounce window before the timer fires.
	w.deb = 500 * time.Millisecond

	var called int32
	done := make(chan struct{})
	go func() {
		w.Run(func(path string) {
			atomic.AddInt32(&called, 1)
		})
		close(done)
	}()

	// Trigger a WRITE — schedules a debounced callback.
	target := filepath.Join(dir, "x.jsonl")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	// Give fsnotify plenty of time to deliver the event into Run and for
	// Run to schedule a timer, but close well before the debounce fires.
	time.Sleep(150 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	// Once <-done returns, Run's deferred Stop() loop has cancelled
	// every pending debounce timer, so no late callback can reach the
	// fire channel — no sleep-past-debounce needed.
	<-done

	if got := atomic.LoadInt32(&called); got != 0 {
		t.Errorf("onChange fired %d times after Close; want 0", got)
	}
}

func TestWatcherRunReturnsOnClose(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		w.Run(func(string) {})
		close(done)
	}()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
		// good — Run returned
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s of Close")
	}
}
