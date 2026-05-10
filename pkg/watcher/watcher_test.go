package watcher

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWatcherEmitsOnWrite(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	events := make(chan string, 10)
	go w.Run(func(path string) { events <- path })

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
	// Speed up debounce so the test isn't paced by the production 100ms.
	w.deb = 50 * time.Millisecond

	var called int32
	go w.Run(func(path string) {
		atomic.AddInt32(&called, 1)
	})

	// Trigger a WRITE — schedules a debounced callback.
	target := filepath.Join(dir, "x.jsonl")
	if err := os.WriteFile(target, []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	// Give fsnotify a beat to deliver the event into Run, but close
	// before the debounce window elapses.
	time.Sleep(10 * time.Millisecond)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	// Wait well past the debounce window.
	time.Sleep(200 * time.Millisecond)

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
