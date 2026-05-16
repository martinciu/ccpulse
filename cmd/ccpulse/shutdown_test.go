package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"go.uber.org/goleak"

	"github.com/martinciu/ccpulse/pkg/anthro"
)

// TestRunTUIQuitsCleanly drives runTUI through to completion via a
// fake stdin that sends `q`, then asserts no goroutine leaks. The
// TUI must register the watcher, quota poller, and initial-refresh
// goroutines with the shared WaitGroup so they finish before runTUI
// returns.
func TestRunTUIQuitsCleanly(t *testing.T) {
	// Redirect the Anthropic usage API at a local httptest server so
	// the quota poller (which fires unconditionally on machines with
	// a real credential in the keychain) doesn't make a real HTTPS
	// call. Plain HTTP/1.1 against an httptest server tears its idle
	// connection pool entries down cleanly when we close it; HTTP/2
	// against api.anthropic.com leaves a documented stdlib readLoop
	// alive in the connection pool past runTUI's return.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 401 ensures Fetch errors out fast (the cred is bogus on a
		// CI box anyway) without entering the success path that would
		// touch the cache file.
		w.WriteHeader(http.StatusUnauthorized)
	}))
	restoreURL := anthro.SetAPIURLForTest(srv.URL)
	defer restoreURL()

	// Snapshot baseline goroutines (testing framework, sqlite
	// background workers, etc.) so we only assert no NEW leaks.
	leakOpts := []goleak.Option{goleak.IgnoreCurrent()}

	// Isolate filesystem state so the test doesn't read the user's
	// real ~/.claude/projects or ~/.cache/ccpulse.
	tmpProjects := t.TempDir()
	tmpCache := t.TempDir()
	t.Setenv("CCPULSE_PROJECTS_ROOT", tmpProjects)
	t.Setenv("CCPULSE_CACHE_DIR", tmpCache)
	// Ensure config.Load resolves to a non-existent path so defaults
	// (with our env overrides) win.
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config-empty"))

	// Substitute the tea.Program factory with a renderer-less,
	// stdin-controlled program that quits as soon as it reads `q`.
	originalNewTeaProgram := newTeaProgram
	defer func() { newTeaProgram = originalNewTeaProgram }()
	newTeaProgram = func(m tea.Model) *tea.Program {
		return tea.NewProgram(m,
			tea.WithoutRenderer(),
			tea.WithInput(strings.NewReader("q")),
			tea.WithOutput(io.Discard),
		)
	}

	// We exit via the `q` keypress, not via signal — context.Background
	// is fine here.
	done := make(chan error, 1)
	go func() {
		done <- runTUI(context.Background(), io.Discard)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runTUI returned error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runTUI did not return within 5s after `q` keypress")
	}

	// Belt-and-suspenders: the cache file should exist (proves runTUI
	// actually reached the body, not just bailed on env config).
	if _, err := os.Stat(filepath.Join(tmpCache, "state.db")); err != nil {
		t.Errorf("cache db not created: %v", err)
	}

	// Tear down the local HTTP server and drain idle connections
	// from net/http's connection pool BEFORE measuring goroutines.
	// Otherwise the http.DefaultClient's idle HTTP/1.1 conn keeps
	// an internal/poll.runtime_pollWait goroutine alive, which
	// would (correctly) flag as a leak.
	http.DefaultClient.CloseIdleConnections()
	srv.Close()

	// goleak.VerifyNone retries internally (~1s of exponential backoff),
	// so we don't need an explicit sleep to let fsnotify's kqueue/inotify
	// reader or sqlite background helpers drain — the retry loop covers
	// it.
	goleak.VerifyNone(t, leakOpts...)
}
