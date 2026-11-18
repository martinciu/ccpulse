package anthro

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"
)

// lockProbeBudget is how long an operation that must not touch
// usage.json.lock may take while another process holds it. Real work is
// sub-millisecond; the 5 s a fetcher can hold the lock is far above this.
const lockProbeBudget = 200 * time.Millisecond

// holdFetchLock takes usage.json.lock and returns an idempotent release that
// is also registered with t.Cleanup, so a regression that blocks on the lock
// can be unblocked and its goroutine drained (the package runs under goleak).
func holdFetchLock(t *testing.T, dir string) (release func()) {
	t.Helper()
	rel, err := acquireFetchLock(dir)
	if err != nil {
		t.Fatalf("acquireFetchLock: %v", err)
	}
	var once sync.Once
	release = func() { once.Do(rel) }
	t.Cleanup(release)
	return release
}

// runWithin runs fn in a goroutine and fails the test if it does not return
// within lockProbeBudget. On timeout it releases the held lock first so fn can finish.
func runWithin(t *testing.T, release func(), name string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(lockProbeBudget):
		release()
		<-done
		t.Fatalf("%s did not return within %v while usage.json.lock was held", name, lockProbeBudget)
	}
}

// The overlay's operations run on every statusline render (killed at the
// custom-command timeout) and must never queue behind a fetcher holding
// usage.json.lock across HTTP.
// They use their own lock file.
func TestOverlayOps_IgnoreFetchLock(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	// A seven-day reading held long enough to be pending as a sample.
	seed := overlayFile{V: overlayVersion, SevenDay: sb(54, now.Add(72*time.Hour), now.Add(-5*time.Second), now.Add(-40*time.Second))}
	if err := writeOverlayRaw(overlayPath(dir), seed); err != nil {
		t.Fatal(err)
	}
	release := holdFetchLock(t, dir)

	// Order matters: ingest mutates the overlay, so it runs after the
	// pending/mark pair that relies on the seeded reading.
	t.Run("PendingStatuslineSample", func(t *testing.T) {
		var ok bool
		runWithin(t, release, "PendingStatuslineSample", func() {
			_, _, ok = PendingStatuslineSample(dir, now)
		})
		if !ok {
			t.Fatal("PendingStatuslineSample ok = false, want a pending sample")
		}
	})

	t.Run("MarkStatuslineRecorded", func(t *testing.T) {
		u, at, ok := PendingStatuslineSample(dir, now)
		if !ok {
			t.Fatal("no pending sample to mark")
		}
		var err error
		runWithin(t, release, "MarkStatuslineRecorded", func() {
			err = MarkStatuslineRecorded(dir, u, at)
		})
		if err != nil {
			t.Fatalf("MarkStatuslineRecorded: %v", err)
		}
	})

	t.Run("IngestStatusline", func(t *testing.T) {
		var changed bool
		var err error
		runWithin(t, release, "IngestStatusline", func() {
			changed, err = IngestStatusline(dir, StatuslineReading{FiveHour: bk(12, now.Add(2*time.Hour))}, now)
		})
		if err != nil || !changed {
			t.Fatalf("IngestStatusline = (%v, %v), want (true, nil)", changed, err)
		}
		if ov := ReadOverlay(dir, now); ov.FiveHour == nil || ov.FiveHour.Utilization != 12 {
			t.Fatalf("overlay FiveHour = %+v, want 12 written", ov.FiveHour)
		}
	})

	t.Run("ReadOverlay", func(t *testing.T) {
		var ov Overlay
		runWithin(t, release, "ReadOverlay", func() {
			ov = ReadOverlay(dir, now)
		})
		if ov.SevenDay == nil || ov.SevenDay.Utilization != 54 {
			t.Fatalf("ReadOverlay SevenDay = %+v, want 54", ov.SevenDay)
		}
	})
}

// With a fresh overlay the API cache TTL is the slow one, so a 20-minute-old
// cache is still fresh. Fetch must decide that before queueing on
// usage.json.lock, which another fetcher may hold for seconds across HTTP.
func TestFetch_FreshOverlaySkipsFetchLock(t *testing.T) {
	base := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	withFrozenClock(t, base)
	srv := newTestServer(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)

	dir := t.TempDir()
	writeFixtureCache(t, dir, base.Add(-20*time.Minute))
	reading := StatuslineReading{SevenDay: bk(54, base.Add(72*time.Hour))}
	if _, err := IngestStatusline(dir, reading, base.Add(-5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	release := holdFetchLock(t, dir)

	var (
		res FetchResult
		err error
	)
	runWithin(t, release, "Fetch", func() {
		res, err = Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	})
	if err != nil || res.Source != "cache_fresh" {
		t.Fatalf("Fetch = (%q, %v), want (cache_fresh, nil)", res.Source, err)
	}
}
