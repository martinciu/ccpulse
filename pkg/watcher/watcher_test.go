package watcher

import (
	"os"
	"path/filepath"
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
