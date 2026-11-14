package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/tui"
)

// msgLog collects the QuotaMsgs a poller sends.
type msgLog struct {
	mu   sync.Mutex
	msgs []tui.QuotaMsg
}

func (l *msgLog) send(m tui.QuotaMsg) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.msgs = append(l.msgs, m)
}

func (l *msgLog) snapshot() []tui.QuotaMsg {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]tui.QuotaMsg(nil), l.msgs...)
}

// ingestNoSession seeds the overlay with a reading that carries no session
// id: accepted into an empty overlay, and allowed to raise within a window,
// with no transcript needed. Inside a synctest bubble real file mtimes sit
// decades past the fake clock, so a session-keyed reading would never count
// as fresh.
func ingestNoSession(t *testing.T, dir string, five, seven *anthro.Bucket, now time.Time) {
	t.Helper()
	if _, err := anthro.IngestStatusline(dir, anthro.StatuslineReading{FiveHour: five, SevenDay: seven}, now); err != nil {
		t.Fatal(err)
	}
}

func bucketAt(util float64, resets time.Time) *anthro.Bucket {
	resets = resets.UTC()
	return &anthro.Bucket{Utilization: util, ResetsAt: &resets}
}

// startPoller runs runQuotaPoller until the returned stop is called; stop
// cancels and waits for the poller to return.
func startPoller(t *testing.T, d quotaPollerDeps) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runQuotaPoller(ctx, d)
	}()
	return func() {
		cancel()
		<-done
	}
}

func TestQuotaPoller_OverlayOnly(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		ingestNoSession(t, dir, nil, bucketAt(60, now.Add(72*time.Hour)), now)
		var log msgLog
		stop := startPoller(t, quotaPollerDeps{cacheDir: dir, send: log.send})

		time.Sleep(overlayRereadInterval + time.Second)
		synctest.Wait()
		msgs := log.snapshot()
		if len(msgs) != 1 {
			t.Fatalf("messages after the first tick = %d, want 1", len(msgs))
		}
		if m := msgs[0]; m.Source != anthro.SourceStatusline || m.Usage == nil || m.Usage.SevenDay == nil || m.Usage.SevenDay.Utilization != 60 {
			t.Errorf("QuotaMsg = %+v, want statusline 7d=60", m)
		}
		if !msgs[0].APIUpdatedAt.IsZero() {
			t.Errorf("APIUpdatedAt = %v with no credential, want zero", msgs[0].APIUpdatedAt)
		}

		time.Sleep(overlayRereadInterval)
		synctest.Wait()
		if n := len(log.snapshot()); n != 1 {
			t.Errorf("messages after an unchanged tick = %d, want still 1", n)
		}
		stop()
	})
}

func TestQuotaPoller_PicksUpNewReadingWithinTick(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		now := time.Now()
		resets := now.Add(72 * time.Hour)
		ingestNoSession(t, dir, nil, bucketAt(60, resets), now)
		var log msgLog
		stop := startPoller(t, quotaPollerDeps{cacheDir: dir, send: log.send})
		synctest.Wait()
		if n := len(log.snapshot()); n != 1 {
			t.Fatalf("messages at start = %d, want 1", n)
		}

		time.Sleep(10 * time.Second)
		ingestNoSession(t, dir, nil, bucketAt(61, resets), time.Now())
		time.Sleep(overlayRereadInterval)
		synctest.Wait()
		msgs := log.snapshot()
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2 (new reading picked up within one re-read interval)", len(msgs))
		}
		if got := msgs[1].Usage.SevenDay.Utilization; got != 61 {
			t.Errorf("second QuotaMsg 7d = %v, want 61", got)
		}
		stop()
	})
}

// Review Focus 4: a window expiring with no new readings.
func TestQuotaPoller_ExpiryWithoutFileChange(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		start := time.Now()
		ingestNoSession(t, dir, bucketAt(10, start.Add(45*time.Second)), bucketAt(60, start.Add(72*time.Hour)), start)
		path := filepath.Join(dir, "usage-statusline.json")
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		var log msgLog
		stop := startPoller(t, quotaPollerDeps{cacheDir: dir, send: log.send})
		synctest.Wait()
		if msgs := log.snapshot(); len(msgs) != 1 || msgs[0].Usage.FiveHour == nil {
			t.Fatalf("start: %d messages, want 1 carrying the 5h bucket", len(msgs))
		}

		time.Sleep(2*overlayRereadInterval + time.Second) // past the 45 s reset
		synctest.Wait()
		msgs := log.snapshot()
		if len(msgs) != 2 {
			t.Fatalf("messages = %d, want 2 (expiry noticed on a re-read tick)", len(msgs))
		}
		if last := msgs[1]; last.Usage.FiveHour != nil || last.Usage.SevenDay == nil {
			t.Errorf("after expiry: 5h=%v 7d=%v, want 5h nil and 7d kept", last.Usage.FiveHour, last.Usage.SevenDay)
		}
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			t.Error("overlay file rewritten; the expiry must come from the re-read alone")
		}
		stop()
	})
}

// fastReread shortens the re-read tick for the real-time (httptest) tests.
func fastReread(t *testing.T) {
	t.Helper()
	prev := overlayRereadInterval
	overlayRereadInterval = 5 * time.Millisecond
	t.Cleanup(func() { overlayRereadInterval = prev })
}

// usageEndpoint serves handler at a test URL Fetch is redirected to.
func usageEndpoint(t *testing.T, handler http.HandlerFunc) *atomic.Int64 {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(anthro.SetAPIURLForTest(srv.URL))
	return &hits
}

// waitFor polls cond for up to 2 s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestQuotaPoller_ReReadNeverFetches(t *testing.T) {
	fastReread(t)
	dir := t.TempDir()
	hits := usageEndpoint(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, sampleAPIBody) })
	var log msgLog
	stop := startPoller(t, quotaPollerDeps{cred: &anthro.Credential{AccessToken: "tok"}, cacheDir: dir, send: log.send})
	defer stop()

	waitFor(t, "the first QuotaMsg", func() bool { return len(log.snapshot()) >= 1 })
	// A change only the re-read can see proves the ticks are running.
	ingestNoSession(t, dir, nil, bucketAt(97, time.Now().Add(72*time.Hour)), time.Now())
	waitFor(t, "the re-read QuotaMsg", func() bool { return len(log.snapshot()) >= 2 })
	time.Sleep(20 * overlayRereadInterval) // ≥ 10 more re-read ticks
	if n := hits.Load(); n != 1 {
		t.Errorf("API hits = %d, want 1 (only the initial fetch; re-reads never call Fetch)", n)
	}
}

func TestQuotaPoller_FetchErrorStillSendsOverlay(t *testing.T) {
	fastReread(t)
	dir := t.TempDir()
	ingestNoSession(t, dir, nil, bucketAt(60, time.Now().Add(72*time.Hour)), time.Now())
	usageEndpoint(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTooManyRequests) })
	var log msgLog
	var recorded atomic.Int64
	stop := startPoller(t, quotaPollerDeps{
		cred: &anthro.Credential{AccessToken: "tok"}, cacheDir: dir, send: log.send,
		recordAPI: func(context.Context, anthro.FetchResult) { recorded.Add(1) },
	})
	defer stop()

	waitFor(t, "a QuotaMsg", func() bool { return len(log.snapshot()) >= 1 })
	if m := log.snapshot()[0]; m.Source != anthro.SourceStatusline || m.Usage.SevenDay == nil || m.Usage.SevenDay.Utilization != 60 {
		t.Errorf("QuotaMsg = %+v, want statusline 7d=60 despite the failed fetch", m)
	}
	if n := recorded.Load(); n != 0 {
		t.Errorf("recordAPI called %d times on a failed fetch, want 0", n)
	}
}

func TestQuotaPoller_RecordsOnlyRawAPIResults(t *testing.T) {
	fastReread(t)
	dir := t.TempDir()
	usageEndpoint(t, func(w http.ResponseWriter, r *http.Request) { _, _ = fmt.Fprint(w, sampleAPIBody) })
	var log msgLog
	var mu sync.Mutex
	var got []anthro.FetchResult
	stop := startPoller(t, quotaPollerDeps{
		cred: &anthro.Credential{AccessToken: "tok"}, cacheDir: dir, send: log.send,
		recordAPI: func(_ context.Context, res anthro.FetchResult) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, res)
		},
	})
	defer stop()

	waitFor(t, "the API QuotaMsg", func() bool { return len(log.snapshot()) >= 1 })
	// A statusline reading newer than the fetch overlays the 7d bucket.
	ingestNoSession(t, dir, nil, bucketAt(60, time.Now().Add(72*time.Hour)), time.Now())
	waitFor(t, "the merged QuotaMsg", func() bool { return len(log.snapshot()) >= 2 })

	msgs := log.snapshot()
	if m := msgs[len(msgs)-1]; m.Source != anthro.SourceStatusline || m.Usage.SevenDay.Utilization != 60 || m.APIUpdatedAt.IsZero() {
		t.Errorf("merged QuotaMsg = source %q 7d %v api_at %v, want statusline 60 with an API age",
			m.Source, m.Usage.SevenDay.Utilization, m.APIUpdatedAt)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(got) != 1 {
		t.Fatalf("recordAPI calls = %d, want 1", len(got))
	}
	if got[0].Source != "api" || got[0].Usage.SevenDay == nil || got[0].Usage.SevenDay.Utilization != 89 {
		t.Errorf("recorded %q 7d=%v, want the raw api result (89)", got[0].Source, got[0].Usage.SevenDay)
	}
}
