package anthro

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// backoffBase is the instant every frozen-clock test starts from. A fixed
// date keeps deadline assertions exact: no test in this file may sleep, and
// none may derive a fixture timestamp from the real clock — a cache stamped
// with time.Now() is now in the FUTURE relative to the frozen backoffBase,
// so freshFromCache reads it as stale (#534) and Fetch would hit the API
// before ever reaching the backoff gate, passing the test for the wrong
// reason.
//
// Deliberately nowhere near the real clock: an HTTP-date Retry-After is
// resolved against a "now", and a base within an hour of the real one would
// let a test pass whether the code read the seam or time.Now().
var backoffBase = time.Date(2026, 1, 15, 9, 0, 0, 0, time.UTC)

// withFrozenClock pins timeNow at base and returns a func that steps it by d
// (negative steps the wall clock backwards, as an NTP correction would),
// reporting the new now. The instant is held in an atomic because one test
// advances it from the HTTP handler's goroutine while Fetch is mid-call.
// Not safe with t.Parallel(): timeNow is a package var, like apiURL.
func withFrozenClock(t *testing.T, base time.Time) func(time.Duration) time.Time {
	t.Helper()
	var cur atomic.Int64
	cur.Store(base.UnixNano())
	prev := timeNow
	timeNow = func() time.Time { return time.Unix(0, cur.Load()).UTC() }
	t.Cleanup(func() { timeNow = prev })
	return func(d time.Duration) time.Time {
		return time.Unix(0, cur.Add(int64(d))).UTC()
	}
}

// flipServer is a test endpoint whose handler can be swapped mid-test (a
// rate-limited endpoint recovering, say) and whose requests are counted.
// Both go through atomics so the swap can't race the server goroutine.
type flipServer struct {
	hits    atomic.Int64
	handler atomic.Pointer[http.HandlerFunc]
}

func newFlipServer(t *testing.T, h http.HandlerFunc) *flipServer {
	t.Helper()
	fs := &flipServer{}
	fs.serve(h)
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		fs.hits.Add(1)
		(*fs.handler.Load())(w, r)
	})
	withTestEndpoint(t, srv.URL)
	return fs
}

func (f *flipServer) serve(h http.HandlerFunc) { f.handler.Store(&h) }
func (f *flipServer) calls() int64             { return f.hits.Load() }

// rateLimited is the 429 the real endpoint sends. retryAfter == "" omits the
// header.
func rateLimited(retryAfter string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		if retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
		http.Error(w, `{"type":"error","error":{"type":"rate_limit_error"}}`, http.StatusTooManyRequests)
	}
}

func servesUsage(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(sampleAPIBody)) }

// seedBackoffFile writes raw bytes to the state file. Deliberately raw: the
// on-disk shape is a cross-process contract (every `ccpulse status` reads
// what the TUI wrote), so the tests pin the literal JSON rather than
// round-tripping through the writer.
func seedBackoffFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, backoffFileName), []byte(body), 0o600); err != nil {
		t.Fatalf("seed backoff file: %v", err)
	}
}

func backoffFileExists(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, backoffFileName))
	return err == nil
}

// staleCacheAge is how far back staleCacheDir stamps its fixture — well past
// cacheTTL, so Fetch always reaches the API path.
const staleCacheAge = 10 * time.Minute

// staleCacheDir returns a temp dir holding a usage cache that is already
// stale at now.
func staleCacheDir(t *testing.T, now time.Time) string {
	t.Helper()
	dir := t.TempDir()
	writeFixtureCache(t, dir, now.Add(-staleCacheAge))
	return dir
}

func fetchTest(t *testing.T, dir string) (FetchResult, error) {
	t.Helper()
	return Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
}

// TestNextBackoff pins the escalation policy, migrated from the TUI-local
// pollBackoff it replaces (#447 → #529). The function is pure: prev is the
// count the previous attempt persisted, which is how the escalation now
// survives between unrelated processes.
func TestNextBackoff(t *testing.T) {
	st := func(code int, ra time.Duration) *StatusError {
		return &StatusError{Code: code, RetryAfter: ra}
	}
	tests := []struct {
		name  string
		seq   []*StatusError
		want  []time.Duration
		wantN []int
	}{
		{
			name:  "non-status failures stay at base cadence",
			seq:   []*StatusError{nil, nil},
			want:  []time.Duration{3 * time.Minute, 3 * time.Minute},
			wantN: []int{0, 0},
		},
		{
			name: "consecutive 429s escalate and cap",
			seq: []*StatusError{
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				st(http.StatusTooManyRequests, 0),
			},
			want: []time.Duration{
				6 * time.Minute, 12 * time.Minute, 24 * time.Minute,
				30 * time.Minute, 30 * time.Minute,
			},
			wantN: []int{1, 2, 3, 4, 5},
		},
		{
			name: "a non-status failure resets escalation",
			seq: []*StatusError{
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				nil, st(http.StatusTooManyRequests, 0),
			},
			want:  []time.Duration{6 * time.Minute, 12 * time.Minute, 3 * time.Minute, 6 * time.Minute},
			wantN: []int{1, 2, 0, 1},
		},
		{
			name:  "non-429 status resets escalation",
			seq:   []*StatusError{st(http.StatusTooManyRequests, 0), st(http.StatusInternalServerError, 0)},
			want:  []time.Duration{6 * time.Minute, 3 * time.Minute},
			wantN: []int{1, 0},
		},
		{
			name:  "retry-after below exponential is floored by exponential",
			seq:   []*StatusError{st(http.StatusTooManyRequests, 30*time.Second)},
			want:  []time.Duration{6 * time.Minute},
			wantN: []int{1},
		},
		{
			name:  "retry-after between exponential and cap is honored",
			seq:   []*StatusError{st(http.StatusTooManyRequests, 20*time.Minute)},
			want:  []time.Duration{20 * time.Minute},
			wantN: []int{1},
		},
		{
			name: "honored retry-after still escalates the counter",
			seq: []*StatusError{
				st(http.StatusTooManyRequests, 45*time.Minute), st(http.StatusTooManyRequests, 0),
			},
			want:  []time.Duration{45 * time.Minute, 12 * time.Minute},
			wantN: []int{1, 2},
		},
		{
			name: "escalated exponential floors smaller retry-after",
			seq: []*StatusError{
				st(http.StatusTooManyRequests, 0), st(http.StatusTooManyRequests, 0),
				st(http.StatusTooManyRequests, 10*time.Minute),
			},
			want:  []time.Duration{6 * time.Minute, 12 * time.Minute, 24 * time.Minute},
			wantN: []int{1, 2, 3},
		},
		{
			name:  "retry-after above cap is honored",
			seq:   []*StatusError{st(http.StatusTooManyRequests, 45*time.Minute)},
			want:  []time.Duration{45 * time.Minute},
			wantN: []int{1},
		},
		{
			name:  "retry-after clamped to one hour",
			seq:   []*StatusError{st(http.StatusTooManyRequests, 2*time.Hour)},
			want:  []time.Duration{time.Hour},
			wantN: []int{1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var prev int
			for i, s := range tt.seq {
				n, delay := nextBackoff(prev, s)
				if delay != tt.want[i] {
					t.Errorf("step %d: delay = %v, want %v", i, delay, tt.want[i])
				}
				if n != tt.wantN[i] {
					t.Errorf("step %d: consecutive = %d, want %d", i, n, tt.wantN[i])
				}
				prev = n
			}
		})
	}
}

func TestBackoffStateRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	path := filepath.Join(dir, backoffFileName)
	want := BackoffState{RetryAt: backoffBase.Add(12 * time.Minute), Consecutive429: 2}
	if err := writeBackoffState(path, want); err != nil {
		t.Fatalf("writeBackoffState: %v", err)
	}

	got, err := readBackoffState(path, backoffBase)
	if err != nil {
		t.Fatalf("readBackoffState: %v", err)
	}
	if !got.RetryAt.Equal(want.RetryAt) {
		t.Errorf("RetryAt = %v, want %v", got.RetryAt, want.RetryAt)
	}
	if got.Consecutive429 != want.Consecutive429 {
		t.Errorf("Consecutive429 = %d, want %d", got.Consecutive429, want.Consecutive429)
	}

	// Same 0700/0600 discipline as usage.json — the deadline is written into
	// the same private cache dir.
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("dir mode: got %o want %o", got, want)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("file mode: got %o want %o", got, want)
	}
}

// TestReadBackoffState pins the untrusted-input contract. Everything this
// file can hold came off disk, so every unusable shape must resolve to "no
// window" — never to a window that blocks fetching.
func TestReadBackoffState(t *testing.T) {
	future := backoffBase.Add(20 * time.Minute).Format(time.RFC3339Nano)
	tests := []struct {
		name            string
		body            string // "" means: write no file at all
		wantErr         bool
		wantActive      bool
		wantConsecutive int
	}{
		{name: "absent", body: ""},
		{
			name:            "well formed window",
			body:            fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":2}`, future),
			wantActive:      true,
			wantConsecutive: 2,
		},
		{
			// Drained, not cleared: only a successful API fetch clears the
			// count. Zeroing it here would flatten the escalation to a
			// permanent six minutes, because every window would restart at 0.
			name:            "expired window keeps the escalation count",
			body:            fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":4}`, backoffBase.Add(-time.Minute).Format(time.RFC3339Nano)),
			wantConsecutive: 4,
		},
		{name: "empty file", body: " ", wantErr: true},
		{name: "not json", body: "not json", wantErr: true},
		{name: "unknown version", body: fmt.Sprintf(`{"v":99,"retry_at":%q}`, future), wantErr: true},
		{name: "unparseable retry_at", body: `{"v":1,"retry_at":"soon"}`, wantErr: true},
		{
			// The wedge guard: a deadline this code can never write is
			// corrupt by construction, and must be discarded rather than
			// capped — see readBackoffState's comment.
			name:    "retry_at beyond the ceiling",
			body:    `{"v":1,"retry_at":"3000-01-01T00:00:00Z","consecutive_429":3}`,
			wantErr: true,
		},
		{
			// …but the ceiling carries slack, because a maximal window is
			// written at exactly now+retryAfterMax and the wall clock can
			// step backwards under it.
			name:            "a maximal window just over the ceiling is kept",
			body:            fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":1}`, backoffBase.Add(retryAfterMax+30*time.Second).Format(time.RFC3339Nano)),
			wantActive:      true,
			wantConsecutive: 1,
		},
		{
			name:            "negative count clamps to zero",
			body:            fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":-7}`, future),
			wantActive:      true,
			wantConsecutive: 0,
		},
		{
			name:            "absurd count clamps to the ceiling",
			body:            fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":9223372036854775807}`, future),
			wantActive:      true,
			wantConsecutive: maxConsecutive429,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.body != "" {
				seedBackoffFile(t, dir, tt.body)
			}
			got, err := ReadBackoffState(dir, backoffBase)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got.Active(backoffBase) != tt.wantActive {
				t.Errorf("Active = %v (RetryAt %v), want %v", got.Active(backoffBase), got.RetryAt, tt.wantActive)
			}
			if got.Consecutive429 != tt.wantConsecutive {
				t.Errorf("Consecutive429 = %d, want %d", got.Consecutive429, tt.wantConsecutive)
			}
		})
	}
}

// TestFetch_BackoffWindowSuppressesAPICall is the regression guard for #529.
// Before it, every stale-cache invocation re-hit a rate-limited endpoint —
// 2,596 429s in one hour, because each `ccpulse status` is a fresh process
// with no memory of the last one. The second Fetch here stands in for that
// second process: it must not touch the network.
func TestFetch_BackoffWindowSuppressesAPICall(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	srv := newFlipServer(t, rateLimited(""))

	res, err := fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if srv.calls() != 1 {
		t.Fatalf("API hits = %d, want 1", srv.calls())
	}
	if res.Source != "cache_stale" {
		t.Errorf("Source = %q, want cache_stale", res.Source)
	}
	if want := backoffBase.Add(6 * time.Minute); !res.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", res.RetryAt, want)
	}
	if res.Consecutive429 != 1 {
		t.Errorf("Consecutive429 = %d, want 1", res.Consecutive429)
	}
	if !backoffFileExists(t, dir) {
		t.Error("backoff state file not written")
	}

	advance(5 * time.Minute) // still inside the six-minute window
	res, err = fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch inside window: %v", err)
	}
	if srv.calls() != 1 {
		t.Errorf("API hits = %d, want 1 — the window must suppress the call", srv.calls())
	}
	if res.Source != "cache_stale" {
		t.Errorf("Source = %q, want cache_stale", res.Source)
	}
	if want := backoffBase.Add(6 * time.Minute); !res.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want the unchanged %v", res.RetryAt, want)
	}
	if res.APIStatus != nil {
		t.Errorf("APIStatus = %+v, want nil — no request was made", res.APIStatus)
	}
	if want := backoffBase.Add(-staleCacheAge); !res.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want the original cache timestamp %v", res.UpdatedAt, want)
	}

	advance(2 * time.Minute) // past the deadline
	if _, err := fetchTest(t, dir); err != nil {
		t.Fatalf("Fetch after window: %v", err)
	}
	if srv.calls() != 2 {
		t.Errorf("API hits = %d, want 2 — the endpoint must be retried once the window drains", srv.calls())
	}
}

// TestFetch_BackoffEscalatesAcrossCalls: the escalation lives on disk, so it
// holds between calls that share nothing in memory — which is the only
// situation ccpulse's statusline ever runs in.
func TestFetch_BackoffEscalatesAcrossCalls(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	srv := newFlipServer(t, rateLimited(""))

	steps := []struct {
		wantConsecutive int
		wantDelay       time.Duration
	}{
		{1, 6 * time.Minute},
		{2, 12 * time.Minute},
		{3, 24 * time.Minute},
		{4, 30 * time.Minute},
		{5, 30 * time.Minute},
	}
	now := backoffBase
	for i, step := range steps {
		res, err := fetchTest(t, dir)
		if err != nil {
			t.Fatalf("step %d: Fetch: %v", i, err)
		}
		if got := srv.calls(); got != int64(i+1) {
			t.Fatalf("step %d: API hits = %d, want %d", i, got, i+1)
		}
		if res.Consecutive429 != step.wantConsecutive {
			t.Errorf("step %d: Consecutive429 = %d, want %d", i, res.Consecutive429, step.wantConsecutive)
		}
		if want := now.Add(step.wantDelay); !res.RetryAt.Equal(want) {
			t.Errorf("step %d: RetryAt = %v, want %v", i, res.RetryAt, want)
		}
		now = advance(step.wantDelay) // land exactly on the deadline
	}
}

func TestFetch_SuccessClearsBackoffState(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	srv := newFlipServer(t, rateLimited(""))

	if _, err := fetchTest(t, dir); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if !backoffFileExists(t, dir) {
		t.Fatal("backoff state file not written after 429")
	}

	advance(6 * time.Minute)
	srv.serve(servesUsage)
	res, err := fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch after recovery: %v", err)
	}
	if res.Source != "api" {
		t.Fatalf("Source = %q, want api", res.Source)
	}
	if !res.RetryAt.IsZero() {
		t.Errorf("RetryAt = %v, want zero on success", res.RetryAt)
	}
	if backoffFileExists(t, dir) {
		t.Error("backoff state file survived a successful fetch")
	}
}

func TestFetch_RetryAfterShapesTheWindow(t *testing.T) {
	tests := []struct {
		name       string
		retryAfter string
		want       time.Duration
	}{
		{name: "below the exponential is floored", retryAfter: "30", want: 6 * time.Minute},
		{name: "above the exponential is honoured", retryAfter: "1200", want: 20 * time.Minute},
		{name: "beyond the ceiling is clamped", retryAfter: "7200", want: time.Hour},
		{
			// Delta-seconds needs no clock, so it cannot tell whether the
			// header was read against timeNow or time.Now. An HTTP-date can:
			// backoffBase sits months away from the real clock, so reading
			// the wrong one collapses this to the 6-minute exponential.
			name:       "an http-date is resolved against the frozen clock",
			retryAfter: backoffBase.Add(20 * time.Minute).Format(http.TimeFormat),
			want:       20 * time.Minute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFrozenClock(t, backoffBase)
			dir := staleCacheDir(t, backoffBase)
			newFlipServer(t, rateLimited(tt.retryAfter))

			res, err := fetchTest(t, dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if want := backoffBase.Add(tt.want); !res.RetryAt.Equal(want) {
				t.Errorf("RetryAt = %v, want %v", res.RetryAt, want)
			}
		})
	}
}

// TestFetch_NonRateLimitFailureSetsBaseCadence: an offline machine must stop
// firing a timing-out request every five seconds too, but without inheriting
// the 429 escalation — the endpoint never asked it to slow down.
func TestFetch_NonRateLimitFailureSetsBaseCadence(t *testing.T) {
	t.Run("non-429 status", func(t *testing.T) {
		advance := withFrozenClock(t, backoffBase)
		dir := staleCacheDir(t, backoffBase)
		srv := newFlipServer(t, rateLimited(""))

		if _, err := fetchTest(t, dir); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		now := advance(6 * time.Minute)
		srv.serve(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		})
		res, err := fetchTest(t, dir)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if res.Consecutive429 != 0 {
			t.Errorf("Consecutive429 = %d, want 0 — a 500 is not a rate limit", res.Consecutive429)
		}
		if want := now.Add(BaseRetryInterval); !res.RetryAt.Equal(want) {
			t.Errorf("RetryAt = %v, want %v", res.RetryAt, want)
		}

		// Reporting the window is not the requirement — persisting it is.
		// The next process has only the file to go on.
		hitsBefore := srv.calls()
		advance(time.Minute)
		srv.serve(servesUsage)
		if _, err := fetchTest(t, dir); err != nil {
			t.Fatalf("Fetch inside the base window: %v", err)
		}
		if got := srv.calls(); got != hitsBefore {
			t.Errorf("API hits = %d, want %d — the base window must be written, not just returned",
				got, hitsBefore)
		}
	})

	t.Run("transport error", func(t *testing.T) {
		advance := withFrozenClock(t, backoffBase)
		dir := staleCacheDir(t, backoffBase)
		newFlipServer(t, rateLimited(""))

		if _, err := fetchTest(t, dir); err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		now := advance(6 * time.Minute)
		// Reserved port: nothing ever listens, so the request is refused at
		// the transport layer and carries no HTTP status at all.
		withTestEndpoint(t, "http://127.0.0.1:1")

		res, err := fetchTest(t, dir)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if res.APIStatus != nil {
			t.Errorf("APIStatus = %+v, want nil on a transport error", res.APIStatus)
		}
		if res.Consecutive429 != 0 {
			t.Errorf("Consecutive429 = %d, want 0", res.Consecutive429)
		}
		if want := now.Add(BaseRetryInterval); !res.RetryAt.Equal(want) {
			t.Errorf("RetryAt = %v, want %v", res.RetryAt, want)
		}
		if !backoffFileExists(t, dir) {
			t.Fatal("no state file after a transport failure — an offline machine would keep dialling")
		}

		// A reachable endpoint again, and a counting one: the window has to
		// hold the request back on its own.
		advance(time.Minute)
		probe := newFlipServer(t, servesUsage)
		if _, err := fetchTest(t, dir); err != nil {
			t.Fatalf("Fetch inside the base window: %v", err)
		}
		if probe.calls() != 0 {
			t.Errorf("API hits = %d, want 0 — the base window must be written, not just returned",
				probe.calls())
		}
	})
}

// TestFetch_NoCacheInsideWindowErrors: with nothing to serve, a suppressed
// attempt must be an error. Returning a FetchResult holding a zero-value
// Usage would render as a perfectly believable 0% utilisation.
func TestFetch_NoCacheInsideWindowErrors(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := t.TempDir() // no usage.json
	srv := newFlipServer(t, rateLimited(""))

	_, err := fetchTest(t, dir)
	if err == nil {
		t.Fatal("Fetch: want an error when no cache and the API 429s")
	}
	var re *RetryError
	if !errors.As(err, &re) {
		t.Fatalf("errors.As(*RetryError) failed on %v (%T)", err, err)
	}
	if want := backoffBase.Add(6 * time.Minute); !re.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v", re.RetryAt, want)
	}
	if re.Consecutive429 != 1 {
		t.Errorf("Consecutive429 = %d, want 1", re.Consecutive429)
	}
	// The API failure still has to be reachable through the chain: callers
	// added in #447 branch on it.
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusTooManyRequests {
		t.Errorf("errors.As(*StatusError) = %+v, want Code 429", se)
	}
	if want := "anthro fetch: api status 429"; err.Error() != want {
		t.Errorf("Error() = %q, want the unchanged %q", err.Error(), want)
	}

	advance(time.Minute)
	res, err := fetchTest(t, dir)
	if err == nil {
		t.Fatal("Fetch inside window: want an error, got nil")
	}
	if srv.calls() != 1 {
		t.Errorf("API hits = %d, want 1 — the window must suppress the call", srv.calls())
	}
	if !errors.Is(err, ErrBackoff) {
		t.Errorf("errors.Is(err, ErrBackoff) = false for %v", err)
	}
	if res.Source != "" || res.Usage.FiveHour != nil {
		t.Errorf("result = %+v, want the zero value — a zero Usage renders as 0%% used", res)
	}
	if !errors.As(err, &re) || !re.RetryAt.Equal(backoffBase.Add(6*time.Minute)) {
		t.Errorf("suppressed error lost the deadline: %v", err)
	}
}

// TestFetch_FarFutureRetryAtNeverWedges guards the failure mode this repo
// already lived through once: one garbage timestamp on disk bricking the
// app. Capping such a value on every read would look like a fix and behave
// like a wedge — the ceiling would slide forward with every call and
// fetching would never resume — so the second phase re-checks well past
// retryAfterMax.
func TestFetch_FarFutureRetryAtNeverWedges(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	srv := newFlipServer(t, rateLimited(""))
	const farFuture = `{"v":1,"retry_at":"3000-01-01T00:00:00Z","consecutive_429":3}`

	for i, phase := range []string{"immediately", "two hours later"} {
		seedBackoffFile(t, dir, farFuture)
		if _, err := fetchTest(t, dir); err != nil {
			t.Fatalf("%s: Fetch: %v", phase, err)
		}
		if got := srv.calls(); got != int64(i+1) {
			t.Fatalf("%s: API hits = %d, want %d — a corrupt deadline must not block fetching",
				phase, got, i+1)
		}
		advance(2 * time.Hour)
	}
}

func TestFetch_CorruptBackoffStateFailsOpen(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "truncated", body: "{"},
		{name: "not json", body: "not json at all"},
		{name: "unknown version", body: `{"v":99,"retry_at":"3000-01-01T00:00:00Z"}`},
		{name: "wrong shape", body: `{"v":1,"retry_at":{"nested":true}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFrozenClock(t, backoffBase)
			dir := staleCacheDir(t, backoffBase)
			seedBackoffFile(t, dir, tt.body)
			srv := newFlipServer(t, servesUsage)

			res, err := fetchTest(t, dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if res.Source != "api" {
				t.Errorf("Source = %q, want api", res.Source)
			}
			if srv.calls() != 1 {
				t.Errorf("API hits = %d, want 1 — an unusable state file must fail open", srv.calls())
			}
		})
	}
}

// TestFetch_ConcurrentCallersShareOneWindow: the state file is read and
// written under the same flock that already serialises the cache refresh, so
// eight simultaneous callers produce one request, not eight.
//
// Real clock on purpose — the goroutines would race a frozen one, and this
// test needs no time travel.
func TestFetch_ConcurrentCallersShareOneWindow(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))
	srv := newFlipServer(t, func(w http.ResponseWriter, r *http.Request) {
		// Hold the response open so the others pile up on the lock rather
		// than serializing trivially behind a finished first caller.
		time.Sleep(50 * time.Millisecond)
		rateLimited("")(w, r)
	})

	const N = 8
	results := make([]FetchResult, N)
	errs := make([]error, N)
	start := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(i int) {
			defer wg.Done()
			<-start
			results[i], errs[i] = fetchTest(t, dir)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := srv.calls(); got != 1 {
		t.Errorf("API hits = %d, want 1", got)
	}
	for i, err := range errs {
		if err != nil {
			t.Errorf("Fetch[%d]: %v", i, err)
			continue
		}
		if results[i].Source != "cache_stale" {
			t.Errorf("Fetch[%d].Source = %q, want cache_stale", i, results[i].Source)
		}
		if results[i].RetryAt.IsZero() {
			t.Errorf("Fetch[%d].RetryAt is zero, want the shared deadline", i)
		}
	}
}

// TestFetchLogs_BodySnippetOnlyForNon429: with the window in place there is
// at most one 429 line per backoff period, and on that line the status code
// is the whole message — the body is a fixed rate_limit_error string. Other
// statuses keep the snippet, where it is the only diagnostic there is.
func TestFetchLogs_BodySnippetOnlyForNon429(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		wantSnippet bool
	}{
		{name: "429 drops the snippet", status: http.StatusTooManyRequests},
		{name: "503 keeps the snippet", status: http.StatusServiceUnavailable, wantSnippet: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFrozenClock(t, backoffBase)
			dir := staleCacheDir(t, backoffBase)
			newFlipServer(t, func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, `{"type":"error","error":{"type":"rate_limit_error"}}`, tt.status)
			})
			recs := captureLogs(t)

			if _, err := fetchTest(t, dir); err != nil {
				t.Fatalf("Fetch: %v", err)
			}

			got := recs()
			var warn *slog.Record
			for i := range got {
				if got[i].Message == "anthro.fetchAPI non-2xx" {
					warn = &got[i]
				}
			}
			if warn == nil {
				t.Fatal("anthro.fetchAPI non-2xx record missing")
			}
			attrs := attrMap(*warn)
			if got, _ := attrs["status"].(int64); got != int64(tt.status) {
				t.Errorf("status = %v, want %d", attrs["status"], tt.status)
			}
			if _, ok := attrs["body_snippet"]; ok != tt.wantSnippet {
				t.Errorf("body_snippet present = %v, want %v (attrs %v)", ok, tt.wantSnippet, attrs)
			}
		})
	}
}

// TestFetch_CancelledContextRecordsNothing: a caller that walks away is not
// evidence about the endpoint. runTUI cancels the poller's context on quit
// and the poller fires again immediately on the next launch, so recording a
// window here would gate that launch — and, worse, flatten a real 429
// escalation to zero on the way out.
func TestFetch_CancelledContextRecordsNothing(t *testing.T) {
	cancelled := func() context.Context {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return ctx
	}

	t.Run("no window is created", func(t *testing.T) {
		withFrozenClock(t, backoffBase)
		dir := staleCacheDir(t, backoffBase)
		srv := newFlipServer(t, servesUsage)

		res, err := Fetch(cancelled(), Credential{AccessToken: "tok"}, dir)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if res.Source != "cache_stale" {
			t.Errorf("Source = %q, want cache_stale", res.Source)
		}
		if !res.RetryAt.IsZero() {
			t.Errorf("RetryAt = %v, want zero — a cancelled caller sets no deadline", res.RetryAt)
		}
		if srv.calls() != 0 {
			t.Errorf("API hits = %d, want 0 — the request never left", srv.calls())
		}
		if backoffFileExists(t, dir) {
			t.Error("a cancelled fetch wrote a backoff window")
		}
	})

	t.Run("an existing escalation is left untouched", func(t *testing.T) {
		withFrozenClock(t, backoffBase)
		dir := staleCacheDir(t, backoffBase)
		newFlipServer(t, servesUsage)
		// A drained window carrying a real escalation: the gate lets the
		// attempt through, so the cancellation lands on the write path.
		seedBackoffFile(t, dir, fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":3}`,
			backoffBase.Add(-time.Minute).Format(time.RFC3339Nano)))
		before, err := os.ReadFile(filepath.Join(dir, backoffFileName))
		if err != nil {
			t.Fatalf("read seeded state: %v", err)
		}

		if _, err := Fetch(cancelled(), Credential{AccessToken: "tok"}, dir); err != nil {
			t.Fatalf("Fetch: %v", err)
		}

		after, err := os.ReadFile(filepath.Join(dir, backoffFileName))
		if err != nil {
			t.Fatalf("read state after cancel: %v", err)
		}
		if string(after) != string(before) {
			t.Errorf("a cancelled fetch rewrote the state file:\nbefore %s\nafter  %s", before, after)
		}
	})

	t.Run("a deadline exceeded still records", func(t *testing.T) {
		withFrozenClock(t, backoffBase)
		dir := staleCacheDir(t, backoffBase)
		newFlipServer(t, servesUsage)
		// `status` wraps Fetch in a 5 s timeout equal to httpTimeout, so an
		// offline or hung endpoint arrives here as the PARENT's deadline,
		// not as a cancellation. That one must still back off, or the
		// five-second statusline loop returns the moment a laptop goes
		// offline — the other half of what #529 is for.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()

		res, err := Fetch(ctx, Credential{AccessToken: "tok"}, dir)
		if err != nil {
			t.Fatalf("Fetch: %v", err)
		}
		if want := backoffBase.Add(BaseRetryInterval); !res.RetryAt.Equal(want) {
			t.Errorf("RetryAt = %v, want %v", res.RetryAt, want)
		}
		if !backoffFileExists(t, dir) {
			t.Error("a deadline-exceeded fetch recorded no window")
		}
	})
}

// TestFetch_WindowStartsWhenTheAttemptEnds: the deadline is measured from
// when the request came back, not from when Fetch started. fetchAPI is
// allowed five seconds, and anchoring the window to the pre-call clock would
// quietly hand every one of them back to the caller.
func TestFetch_WindowStartsWhenTheAttemptEnds(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	const inFlight = 4 * time.Second
	newFlipServer(t, func(w http.ResponseWriter, r *http.Request) {
		advance(inFlight) // the request "took" four seconds
		rateLimited("")(w, r)
	})

	res, err := fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if want := backoffBase.Add(inFlight + 6*time.Minute); !res.RetryAt.Equal(want) {
		t.Errorf("RetryAt = %v, want %v — the window must start when the attempt ended", res.RetryAt, want)
	}
}

// TestFetch_WindowHoldsWithoutTheFlock: the gate must not be conditional on
// having taken the lock. acquireFetchLock is allowed to fail — ENOLCK on a
// quirky filesystem is the documented case — and Fetch degrades rather than
// refusing to work. Degrading must not mean ignoring the window, or exactly
// the filesystems that cannot lock get the 2,596-requests-an-hour behaviour
// back.
func TestFetch_WindowHoldsWithoutTheFlock(t *testing.T) {
	withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	// secfile.OpenFile passes O_NOFOLLOW, so a symlink where the lock file
	// goes makes acquireFetchLock fail and touches nothing else.
	if err := os.Symlink(filepath.Join(dir, "lock-target"), filepath.Join(dir, "usage.json.lock")); err != nil {
		t.Fatalf("seed lock symlink: %v", err)
	}
	seedBackoffFile(t, dir, fmt.Sprintf(`{"v":1,"retry_at":%q,"consecutive_429":2}`,
		backoffBase.Add(10*time.Minute).Format(time.RFC3339Nano)))
	srv := newFlipServer(t, servesUsage)
	recs := captureLogs(t)

	res, err := fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	// Without this the test proves nothing: if the lock were acquired after
	// all, the gate would be exercised on its ordinary path.
	var locked any = "record missing"
	for _, r := range recs() {
		if r.Message == "anthro.Fetch" {
			locked = attrMap(r)["lock_acquired"]
		}
	}
	if locked != false {
		t.Fatalf("lock_acquired = %v, want false — the symlink did not defeat the lock", locked)
	}
	if srv.calls() != 0 {
		t.Errorf("API hits = %d, want 0 — the window must hold without the lock", srv.calls())
	}
	if res.Source != "cache_stale" {
		t.Errorf("Source = %q, want cache_stale", res.Source)
	}
	if res.Consecutive429 != 2 {
		t.Errorf("Consecutive429 = %d, want 2", res.Consecutive429)
	}
}

// TestFetch_MaximalWindowSurvivesABackwardClockStep: a window opened by a
// Retry-After of an hour or more is written at exactly now+retryAfterMax, so
// it sits on the discard ceiling. Any backward step of the wall clock before
// the next read — an NTP correction is enough — would put it past a ceiling
// with no slack, throw it away, and send another request to an endpoint that
// just asked for an hour of quiet.
func TestFetch_MaximalWindowSurvivesABackwardClockStep(t *testing.T) {
	advance := withFrozenClock(t, backoffBase)
	dir := staleCacheDir(t, backoffBase)
	srv := newFlipServer(t, rateLimited("7200")) // asks two hours, clamped to one

	res, err := fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if want := backoffBase.Add(retryAfterMax); !res.RetryAt.Equal(want) {
		t.Fatalf("RetryAt = %v, want %v", res.RetryAt, want)
	}

	advance(-5 * time.Second) // the clock slips backwards
	res, err = fetchTest(t, dir)
	if err != nil {
		t.Fatalf("Fetch after the clock slipped: %v", err)
	}
	if srv.calls() != 1 {
		t.Errorf("API hits = %d, want 1 — a maximal window must survive a small backward step", srv.calls())
	}
	if res.Source != "cache_stale" {
		t.Errorf("Source = %q, want cache_stale", res.Source)
	}
}

// TestFetch_FutureCacheTimestampIsStale is the regression guard for #534.
// A cache entry whose UpdatedAt is ahead of now produces a negative age,
// which was always < cacheTTL — so freshFromCache served it as cache_fresh
// forever, with zero API calls, until the wall clock caught up. Both a
// wildly future stamp (a decade out — a hand-edited or restored cache) and
// a barely future one (one second — an NTP-corrected clock) must fall
// through to the API and get the cache rewritten with a sane timestamp.
func TestFetch_FutureCacheTimestampIsStale(t *testing.T) {
	tests := []struct {
		name   string
		future time.Duration
	}{
		{"decade in the future", 10 * 365 * 24 * time.Hour},
		{"one second in the future", time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFrozenClock(t, backoffBase)
			dir := t.TempDir()
			writeFixtureCache(t, dir, backoffBase.Add(tt.future))
			srv := newFlipServer(t, servesUsage)

			res, err := fetchTest(t, dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if res.Source != "api" {
				t.Errorf("Source = %q, want api", res.Source)
			}
			if srv.calls() != 1 {
				t.Errorf("API hits = %d, want 1", srv.calls())
			}
			if !res.UpdatedAt.Equal(backoffBase) {
				t.Errorf("UpdatedAt = %v, want %v (rewritten to now)", res.UpdatedAt, backoffBase)
			}

			got, err := readCache(filepath.Join(dir, "usage.json"))
			if err != nil {
				t.Fatalf("readCache: %v", err)
			}
			if !got.UpdatedAt.Equal(backoffBase) {
				t.Errorf("on-disk cache UpdatedAt = %v, want %v — the future stamp must not survive the rewrite", got.UpdatedAt, backoffBase)
			}
		})
	}
}

// TestFetch_FreshCacheAgeBoundary pins the edges of the freshness window
// that #534's fix must not disturb: an entry stamped at exactly now (age
// 0) still reads fresh, and one just under cacheTTL still reads fresh —
// only a negative age is new territory.
func TestFetch_FreshCacheAgeBoundary(t *testing.T) {
	tests := []struct {
		name string
		age  time.Duration
	}{
		{"zero age", 0},
		{"just under cacheTTL", cacheTTL - time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withFrozenClock(t, backoffBase)
			dir := t.TempDir()
			writeFixtureCache(t, dir, backoffBase.Add(-tt.age))
			srv := newFlipServer(t, func(w http.ResponseWriter, r *http.Request) {
				t.Errorf("should not hit API on fresh cache")
			})

			res, err := fetchTest(t, dir)
			if err != nil {
				t.Fatalf("Fetch: %v", err)
			}
			if res.Source != "cache_fresh" {
				t.Errorf("Source = %q, want cache_fresh", res.Source)
			}
			if srv.calls() != 0 {
				t.Errorf("API hits = %d, want 0", srv.calls())
			}
		})
	}
}
