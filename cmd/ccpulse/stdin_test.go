package main

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"
)

// Not t.Parallel(): the timeout case leaves a reader goroutine blocked on
// the pipe until the deferred Close, and the package's per-test goleak
// checks must never observe it.
func TestReadStdinBounded(t *testing.T) {
	t.Run("small input", func(t *testing.T) {
		got, err := readStdinBounded(bytes.NewReader([]byte("0123456789")), 1<<20, time.Second)
		if err != nil || string(got) != "0123456789" {
			t.Fatalf("got (%q, %v), want the 10 bytes", got, err)
		}
	})
	t.Run("exactly the limit", func(t *testing.T) {
		got, err := readStdinBounded(bytes.NewReader(make([]byte, 64)), 64, time.Second)
		if err != nil || len(got) != 64 {
			t.Fatalf("got (%d bytes, %v), want 64 bytes", len(got), err)
		}
	})
	t.Run("over the limit", func(t *testing.T) {
		_, err := readStdinBounded(bytes.NewReader(make([]byte, 65)), 64, time.Second)
		if !errors.Is(err, errStdinTooLarge) {
			t.Fatalf("err = %v, want errStdinTooLarge", err)
		}
	})
	t.Run("writer never closes", func(t *testing.T) {
		pr, pw := io.Pipe()
		defer pw.Close() // unblocks the read goroutine
		start := time.Now()
		_, err := readStdinBounded(pr, 1<<20, 20*time.Millisecond)
		if !errors.Is(err, errStdinTimeout) {
			t.Fatalf("err = %v, want errStdinTimeout", err)
		}
		if el := time.Since(start); el > time.Second {
			t.Errorf("returned after %v, want ~20ms", el)
		}
	})
	t.Run("read error", func(t *testing.T) {
		boom := errors.New("boom")
		_, err := readStdinBounded(io.MultiReader(bytes.NewReader([]byte("x")), errReader{boom}), 1<<20, time.Second)
		if !errors.Is(err, boom) {
			t.Fatalf("err = %v, want boom", err)
		}
	})
}

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }
