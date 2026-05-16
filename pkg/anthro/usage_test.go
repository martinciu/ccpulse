package anthro

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const sampleAPIBody = `{
  "five_hour":            {"utilization": 5.0,  "resets_at": "2026-05-09T16:10:00.151311+00:00"},
  "seven_day":            {"utilization": 89.0, "resets_at": "2026-05-10T09:00:00.151331+00:00"},
  "seven_day_oauth_apps": null,
  "seven_day_opus":       null,
  "seven_day_sonnet":     {"utilization": 5.0,  "resets_at": "2026-05-10T09:00:00.151340+00:00"},
  "seven_day_cowork":     null,
  "seven_day_omelette":   {"utilization": 21.0, "resets_at": "2026-05-10T09:00:01.151348+00:00"},
  "tangelo":              null,
  "iguana_necktie":       null,
  "omelette_promotional": null,
  "extra_usage":          {"is_enabled": true, "monthly_limit": 2000, "used_credits": 0.0, "utilization": null, "currency": "EUR"}
}`

func TestUsageUnmarshalFull(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.FiveHour == nil || u.FiveHour.Utilization != 5.0 {
		t.Errorf("five_hour utilization: %+v", u.FiveHour)
	}
	want, _ := time.Parse(time.RFC3339Nano, "2026-05-09T16:10:00.151311+00:00")
	if u.FiveHour.ResetsAt == nil || !u.FiveHour.ResetsAt.Equal(want) {
		t.Errorf("five_hour.ResetsAt = %v, want %v", u.FiveHour.ResetsAt, want)
	}
	if u.SevenDay == nil || u.SevenDay.Utilization != 89.0 {
		t.Errorf("seven_day: %+v", u.SevenDay)
	}
	if u.SevenDayOpus != nil {
		t.Errorf("seven_day_opus should be nil, got %+v", u.SevenDayOpus)
	}
	if u.Tangelo != nil {
		t.Errorf("tangelo should be nil, got %+v", u.Tangelo)
	}
	if u.ExtraUsage == nil || !u.ExtraUsage.IsEnabled {
		t.Errorf("extra_usage: %+v", u.ExtraUsage)
	}
	if u.ExtraUsage.MonthlyLimit != 2000 || u.ExtraUsage.Currency != "EUR" {
		t.Errorf("extra_usage fields: %+v", u.ExtraUsage)
	}
	if u.ExtraUsage.Utilization != nil {
		t.Errorf("extra_usage.utilization should be nil pointer, got %v", *u.ExtraUsage.Utilization)
	}
}

func TestUsageUnmarshalAllNull(t *testing.T) {
	body := `{
	  "five_hour": null, "seven_day": null,
	  "seven_day_oauth_apps": null, "seven_day_opus": null,
	  "seven_day_sonnet": null, "seven_day_cowork": null,
	  "seven_day_omelette": null, "tangelo": null,
	  "iguana_necktie": null, "omelette_promotional": null,
	  "extra_usage": null
	}`
	var u Usage
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.FiveHour != nil || u.ExtraUsage != nil {
		t.Errorf("expected all nil, got %+v", u)
	}
}

func TestUsageUnmarshalNullResetsAt(t *testing.T) {
	body := `{
	  "five_hour":   {"utilization": 0.0,  "resets_at": null},
	  "seven_day":   {"utilization": 89.0, "resets_at": "2026-05-10T09:00:00.151331+00:00"},
	  "seven_day_oauth_apps": null,
	  "seven_day_opus":       null,
	  "seven_day_sonnet":     null,
	  "seven_day_cowork":     null,
	  "seven_day_omelette":   {"utilization": 0.0, "resets_at": null},
	  "tangelo":              null,
	  "iguana_necktie":       null,
	  "omelette_promotional": null,
	  "extra_usage":          null
	}`
	var u Usage
	if err := json.Unmarshal([]byte(body), &u); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if u.FiveHour == nil {
		t.Fatal("five_hour should be present (utilization 0)")
	}
	if u.FiveHour.ResetsAt != nil {
		t.Errorf("five_hour.ResetsAt should be nil pointer, got %v", *u.FiveHour.ResetsAt)
	}
	if u.SevenDay == nil || u.SevenDay.ResetsAt == nil {
		t.Fatalf("seven_day should be present with non-nil ResetsAt, got %+v", u.SevenDay)
	}
	if u.SevenDayOmelette == nil || u.SevenDayOmelette.ResetsAt != nil {
		t.Errorf("seven_day_omelette: want bucket present with nil ResetsAt, got %+v", u.SevenDayOmelette)
	}
}

func TestUsageRoundTrip(t *testing.T) {
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	out, err := json.Marshal(u)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"five_hour"`, `"seven_day_sonnet"`, `"extra_usage"`, `"tangelo":null`, `"is_enabled":true`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("round-trip missing %s in %s", want, out)
		}
	}
}

func TestReadCacheFresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	wrote := writeFixtureCache(t, dir, now.Add(-1*time.Minute))
	got, err := readCache(filepath.Join(dir, "usage.json"))
	if err != nil {
		t.Fatalf("readCache: %v", err)
	}
	if got.UpdatedAt.Sub(wrote).Abs() > time.Second {
		t.Errorf("UpdatedAt drift: got %v, want %v", got.UpdatedAt, wrote)
	}
	if got.Usage.FiveHour == nil {
		t.Errorf("usage.FiveHour nil")
	}
}

func TestReadCacheMissing(t *testing.T) {
	_, err := readCache(filepath.Join(t.TempDir(), "missing.json"))
	if err == nil {
		t.Errorf("expected error on missing cache")
	}
}

func TestReadCacheCorrupt(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(p, []byte("not json"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readCache(p)
	if err == nil {
		t.Errorf("expected error on corrupt cache")
	}
}

func TestReadCacheWrongVersion(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	body := `{"v":99,"updated_at":"2026-05-09T15:00:00Z","data":{}}`
	if err := os.WriteFile(p, []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := readCache(p)
	if err == nil {
		t.Errorf("expected error on wrong cache version")
	}
}

func TestWriteCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "usage.json")
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	if err := writeCache(p, u, when); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	got, err := readCache(p)
	if err != nil {
		t.Fatalf("readCache: %v", err)
	}
	if !got.UpdatedAt.Equal(when) {
		t.Errorf("UpdatedAt = %v, want %v", got.UpdatedAt, when)
	}
	if got.Usage.SevenDay == nil || got.Usage.SevenDay.Utilization != 89.0 {
		t.Errorf("seven_day round-trip lost: %+v", got.Usage.SevenDay)
	}
}

// writeFixtureCache writes sampleAPIBody as a v:1 cache file with the given
// updated_at and returns the timestamp actually written.
func writeFixtureCache(t *testing.T, dir string, when time.Time) time.Time {
	t.Helper()
	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatal(err)
	}
	if err := writeCache(filepath.Join(dir, "usage.json"), u, when); err != nil {
		t.Fatal(err)
	}
	return when
}

func newTestServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	return s
}

// withTestEndpoint redirects Fetch to a test server for the duration of the test.
func withTestEndpoint(t *testing.T, url string) {
	t.Helper()
	prev := apiURL
	apiURL = url
	t.Cleanup(func() { apiURL = prev })
}

func TestFetchAPISuccess(t *testing.T) {
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)

	dir := t.TempDir()
	cred := Credential{AccessToken: "tok"}
	res, err := Fetch(context.Background(), cred, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "api" {
		t.Errorf("Source = %q, want api", res.Source)
	}
	if res.Usage.FiveHour == nil {
		t.Errorf("FiveHour nil")
	}
	if _, err := os.Stat(filepath.Join(dir, "usage.json")); err != nil {
		t.Errorf("cache not written: %v", err)
	}
}

func TestFetchUsesFreshCache(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-1*time.Minute))
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("should not hit API on fresh cache")
	})
	withTestEndpoint(t, srv.URL)
	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "cache_fresh" {
		t.Errorf("Source = %q, want cache_fresh", res.Source)
	}
}

func TestFetchCacheStaleAPIOK(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))
	hit := 0
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hit++
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)
	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if hit != 1 {
		t.Errorf("API hits = %d, want 1", hit)
	}
	if res.Source != "api" {
		t.Errorf("Source = %q, want api", res.Source)
	}
}

func TestFetchCacheStaleAPIFail(t *testing.T) {
	dir := t.TempDir()
	wrote := time.Now().Add(-10 * time.Minute)
	writeFixtureCache(t, dir, wrote)
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	withTestEndpoint(t, srv.URL)
	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "cache_stale" {
		t.Errorf("Source = %q, want cache_stale", res.Source)
	}
	if res.UpdatedAt.Sub(wrote).Abs() > time.Second {
		t.Errorf("UpdatedAt should be original write time")
	}
}

func TestFetchNoCacheAPIFail(t *testing.T) {
	dir := t.TempDir()
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	withTestEndpoint(t, srv.URL)
	_, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err == nil {
		t.Errorf("expected error when no cache and API fails")
	}
}

func TestFetchEmptyTokenErrors(t *testing.T) {
	dir := t.TempDir()
	_, err := Fetch(context.Background(), Credential{}, dir)
	if err == nil {
		t.Errorf("expected error for empty token")
	}
}

func TestUsageCache_FreshModes(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	path := filepath.Join(dir, "usage.json")
	if err := writeCache(path, Usage{}, time.Now()); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat dir: %v", err)
	}
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode: got %o want %o", got, want)
	}
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}

// TestWriteCacheConcurrent stresses writeCache + readCache under contention.
// N writers each loop calling writeCache with their own distinct timestamp;
// R readers continuously re-parse the cache and assert each successful read
// returns one of the N input timestamps. A torn JSON file would surface as
// a readCache error; a missing-file window would too.
func TestWriteCacheConcurrent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "usage.json")

	var u Usage
	if err := json.Unmarshal([]byte(sampleAPIBody), &u); err != nil {
		t.Fatalf("seed unmarshal: %v", err)
	}

	const (
		N     = 16 // writers
		R     = 8  // readers
		Iters = 20 // writes per writer
	)

	timestamps := make([]time.Time, N)
	base := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	for i := range timestamps {
		timestamps[i] = base.Add(time.Duration(i) * time.Second)
	}

	// Seed so readers always see a valid file from t=0.
	if err := writeCache(path, u, timestamps[0]); err != nil {
		t.Fatalf("seed writeCache: %v", err)
	}

	start := make(chan struct{})
	stop := make(chan struct{})

	var writers sync.WaitGroup
	writers.Add(N)
	for i := range N {
		go func(when time.Time) {
			defer writers.Done()
			<-start
			for range Iters {
				if err := writeCache(path, u, when); err != nil {
					t.Errorf("writeCache: %v", err)
					return
				}
			}
		}(timestamps[i])
	}

	var readers sync.WaitGroup
	readers.Add(R)
	for range R {
		go func() {
			defer readers.Done()
			<-start
			for {
				select {
				case <-stop:
					return
				default:
				}
				got, err := readCache(path)
				if err != nil {
					t.Errorf("torn read: readCache: %v", err)
					return
				}
				if !slices.ContainsFunc(timestamps, got.UpdatedAt.Equal) {
					t.Errorf("torn read: UpdatedAt %v not in input set", got.UpdatedAt)
					return
				}
			}
		}()
	}

	close(start)
	writers.Wait()
	close(stop)
	readers.Wait()

	got, err := readCache(path)
	if err != nil {
		t.Fatalf("final readCache: %v", err)
	}
	if !slices.ContainsFunc(timestamps, got.UpdatedAt.Equal) {
		t.Errorf("final UpdatedAt %v matched none of the N input timestamps", got.UpdatedAt)
	}
}

// TestFetchSerializesConcurrentStaleCallers proves the fix for #76: N
// concurrent Fetch callers that all observe a stale cache must produce
// exactly one upstream API hit. The first caller through the lock refreshes
// the cache; the rest re-read under the lock and return cache_fresh.
func TestFetchSerializesConcurrentStaleCallers(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))

	var hits atomic.Int64
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		// Hold the response open long enough for the other goroutines to
		// pile up on the lock; without the sleep they could finish their
		// fast-path check, hit the lock, and serialize trivially.
		time.Sleep(50 * time.Millisecond)
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)

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
			results[i], errs[i] = Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
		}(i)
	}
	close(start)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Errorf("API hits = %d, want 1", got)
	}
	apiCount, freshCount := 0, 0
	for i, err := range errs {
		if err != nil {
			t.Errorf("Fetch[%d]: %v", i, err)
			continue
		}
		switch results[i].Source {
		case "api":
			apiCount++
		case "cache_fresh":
			freshCount++
		default:
			t.Errorf("Fetch[%d].Source = %q, want api or cache_fresh", i, results[i].Source)
		}
	}
	if apiCount != 1 {
		t.Errorf("Source=api count = %d, want 1", apiCount)
	}
	if apiCount+freshCount != N {
		t.Errorf("api+fresh = %d, want %d", apiCount+freshCount, N)
	}
}

func TestUsageCache_TightensExisting(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("seed dir: %v", err)
	}
	path := filepath.Join(dir, "usage.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if err := writeCache(path, Usage{}, time.Now()); err != nil {
		t.Fatalf("writeCache: %v", err)
	}
	dirInfo, _ := os.Stat(dir)
	if got, want := dirInfo.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Fatalf("dir mode: got %o want %o", got, want)
	}
	fileInfo, _ := os.Stat(path)
	if got, want := fileInfo.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("file mode: got %o want %o", got, want)
	}
}

// captureLogs swaps slog.Default for a slice-backed handler at the given
// level (or above) and restores via t.Cleanup. Returns a snapshot getter:
// each call returns a fresh copy of records observed so far, taken under
// the handler mutex so callers can read mid-test safely if needed.
//
// Caveat: slog.SetDefault is process-global. Tests using captureLogs must
// NOT call t.Parallel() — concurrent tests would cross-contaminate the
// captured default. ccpulse's pkg/anthro tests are all serial today.
func captureLogs(t *testing.T, minLevel slog.Level) func() []slog.Record {
	t.Helper()
	var (
		mu   sync.Mutex
		recs []slog.Record
	)
	prev := slog.Default()
	h := &captureHandler{level: minLevel, mu: &mu, recs: &recs}
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return func() []slog.Record {
		mu.Lock()
		defer mu.Unlock()
		snap := make([]slog.Record, len(recs))
		copy(snap, recs)
		return snap
	}
}

type captureHandler struct {
	level slog.Level
	mu    *sync.Mutex
	recs  *[]slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, l slog.Level) bool { return l >= h.level }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	*h.recs = append(*h.recs, r)
	return nil
}

// WithAttrs / WithGroup return the receiver unchanged. This is incorrect
// against the slog.Handler interface (real handlers must apply prefixed
// attrs/group to subsequent records), but ccpulse never calls slog.With()
// before logging in the call paths these tests exercise. If that ever
// changes, captured records will silently drop the prefix attrs — extend
// these to accumulate (clone-and-append) before relying on the helper there.
func (h *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(string) slog.Handler      { return h }

// attrMap collects record attributes into a key->value map for assertions.
func attrMap(r slog.Record) map[string]any {
	m := map[string]any{}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value.Any(); return true })
	return m
}

func TestFetchLogs_CacheFresh(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-1*time.Minute))
	recs := captureLogs(t, slog.LevelDebug)

	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "cache_fresh" {
		t.Fatalf("Source = %q, want cache_fresh", res.Source)
	}
	got := recs()
	if len(got) != 1 {
		t.Fatalf("captured %d records, want 1: %+v", len(got), got)
	}
	r := got[0]
	if r.Level != slog.LevelDebug {
		t.Errorf("level = %v, want DEBUG", r.Level)
	}
	if r.Message != "anthro.Fetch" {
		t.Errorf("message = %q, want anthro.Fetch", r.Message)
	}
	attrs := attrMap(r)
	if attrs["source"] != "cache_fresh" {
		t.Errorf("source = %v, want cache_fresh", attrs["source"])
	}
	if _, ok := attrs["cache_age_s"]; !ok {
		t.Errorf("cache_age_s attribute missing")
	}
	if got, ok := attrs["lock_acquired"].(bool); !ok || got != false {
		t.Errorf("lock_acquired = %v, want false", attrs["lock_acquired"])
	}
}

func TestFetchLogs_API(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)
	recs := captureLogs(t, slog.LevelDebug)

	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "api" {
		t.Fatalf("Source = %q, want api", res.Source)
	}
	got := recs()
	var apiDbg, fetchDbg *slog.Record
	for i := range got {
		r := &got[i]
		switch r.Message {
		case "anthro.fetchAPI":
			apiDbg = r
		case "anthro.Fetch":
			fetchDbg = r
		}
	}
	if apiDbg == nil {
		t.Fatalf("anthro.fetchAPI record missing: %+v", got)
	}
	if apiDbg.Level != slog.LevelDebug {
		t.Errorf("fetchAPI level = %v, want DEBUG", apiDbg.Level)
	}
	if got, _ := attrMap(*apiDbg)["status"].(int64); got != 200 {
		t.Errorf("fetchAPI status = %v, want 200", attrMap(*apiDbg)["status"])
	}
	if fetchDbg == nil {
		t.Fatalf("anthro.Fetch record missing")
	}
	if attrMap(*fetchDbg)["source"] != "api" {
		t.Errorf("Fetch source = %v, want api", attrMap(*fetchDbg)["source"])
	}
}

func TestFetchLogs_CacheStale(t *testing.T) {
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, http.StatusTooManyRequests)
	})
	withTestEndpoint(t, srv.URL)
	recs := captureLogs(t, slog.LevelDebug)

	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if res.Source != "cache_stale" {
		t.Fatalf("Source = %q, want cache_stale", res.Source)
	}
	got := recs()
	var apiWarn, fetchDbg *slog.Record
	for i := range got {
		r := &got[i]
		if r.Message == "anthro.fetchAPI non-2xx" {
			apiWarn = r
		}
		if r.Message == "anthro.Fetch" {
			fetchDbg = r
		}
	}
	if apiWarn == nil {
		t.Fatalf("anthro.fetchAPI non-2xx record missing: %+v", got)
	}
	if apiWarn.Level != slog.LevelWarn {
		t.Errorf("non-2xx level = %v, want WARN", apiWarn.Level)
	}
	attrs := attrMap(*apiWarn)
	if got, _ := attrs["status"].(int64); got != 429 {
		t.Errorf("non-2xx status = %v, want 429", attrs["status"])
	}
	if snip, _ := attrs["body_snippet"].(string); snip == "" {
		t.Errorf("body_snippet empty, want non-empty")
	}
	if fetchDbg == nil || attrMap(*fetchDbg)["source"] != "cache_stale" {
		t.Errorf("Fetch DEBUG missing or wrong source: %+v", fetchDbg)
	}
}

func TestFetchLogs_BodySnippetEscapesControlBytes(t *testing.T) {
	// Pins the security property: a malicious or MitM'd response body
	// containing ANSI escapes / CR / NUL must NOT land in the log as
	// raw control bytes (would execute in the user's terminal on `tail`).
	dir := t.TempDir()
	writeFixtureCache(t, dir, time.Now().Add(-10*time.Minute))
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte("\x1b[2J\r\x00malicious"))
	})
	withTestEndpoint(t, srv.URL)
	recs := captureLogs(t, slog.LevelDebug)

	_, _ = Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)

	got := recs()
	var seen bool
	for i := range got {
		r := &got[i]
		if r.Message != "anthro.fetchAPI non-2xx" {
			continue
		}
		seen = true
		snip, _ := attrMap(*r)["body_snippet"].(string)
		if strings.ContainsAny(snip, "\x1b\r\x00") {
			t.Errorf("body_snippet leaks raw control bytes: %q", snip)
		}
	}
	if !seen {
		t.Fatalf("anthro.fetchAPI non-2xx record not captured: %+v", got)
	}
}

func TestFetchLogs_TransportError(t *testing.T) {
	dir := t.TempDir()
	// httptest server immediately closed: URL is valid but nothing listens.
	// Relies on the kernel not handing the port to another listener in the
	// gap between Close() and Fetch(). Standard pattern; if it ever flakes,
	// switch to "http://127.0.0.1:1" (reserved port, always refuses).
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {})
	url := srv.URL
	srv.Close()
	withTestEndpoint(t, url)
	recs := captureLogs(t, slog.LevelDebug)

	_, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, dir)
	if err == nil {
		t.Fatalf("Fetch returned nil err, want transport error")
	}
	got := recs()
	var transport *slog.Record
	for i := range got {
		r := &got[i]
		if r.Message == "anthro.fetchAPI transport error" {
			transport = r
		}
	}
	if transport == nil {
		t.Fatalf("anthro.fetchAPI transport error record missing: %+v", got)
	}
	if transport.Level != slog.LevelWarn {
		t.Errorf("transport level = %v, want WARN", transport.Level)
	}
	attrs := attrMap(*transport)
	if _, ok := attrs["status"]; ok {
		t.Errorf("status attr present, want absent on transport error")
	}
	if _, ok := attrs["err"]; !ok {
		t.Errorf("err attr missing on transport error")
	}
}

func TestFetchLogs_WriteCacheFailure(t *testing.T) {
	// cacheDir is a regular file → secfile.MkdirAll fails → writeCache fails.
	tmp := t.TempDir()
	cacheDir := filepath.Join(tmp, "not-a-dir")
	if err := os.WriteFile(cacheDir, []byte{}, 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)
	recs := captureLogs(t, slog.LevelDebug)

	res, err := Fetch(context.Background(), Credential{AccessToken: "tok"}, cacheDir)
	if err != nil {
		t.Fatalf("Fetch: %v (want nil — writeCache failure must not propagate)", err)
	}
	if res.Source != "api" {
		t.Fatalf("Source = %q, want api", res.Source)
	}
	if res.Usage.FiveHour == nil {
		t.Errorf("FiveHour nil — Fetch should still return the in-memory response")
	}
	got := recs()
	var wc *slog.Record
	for i := range got {
		r := &got[i]
		if r.Message == "anthro.writeCache" {
			wc = r
		}
	}
	if wc == nil {
		t.Fatalf("anthro.writeCache record missing: %+v", got)
	}
	if wc.Level != slog.LevelWarn {
		t.Errorf("writeCache level = %v, want WARN", wc.Level)
	}
	if _, ok := attrMap(*wc)["err"]; !ok {
		t.Errorf("err attr missing on writeCache WARN")
	}
}

// TestFetch_NoCredentialFieldsInLogs is the privacy guard for the only
// call site in ccpulse that handles a credential: the Anthropic fetch
// path. Every string-shaped Credential field is planted with a sentinel
// (Bearer/refresh tokens, subscription type, rate-limit tier, scope) and
// the credential is threaded through Fetch → fetchAPI → req.Header. None
// of those code paths should emit any sentinel to slog. The test renders
// captured records to a real slog.TextHandler so the assertion is on the
// literal bytes that would land in ccpulse.log.
//
// Scope deliberately narrow: this does NOT plant sentinels inside the
// HTTP response body. body_snippet is logged on non-2xx and decode paths
// (bounded and Quote'd per the design spec); planting body sentinels
// would fail by design. See privacy_sentinels_test.go for the policy.
//
// Verifying this test is not vacuous after a refactor: temporarily add
//
//	slog.Info("probe", "tok", token)
//
// inside fetchAPI between req.Header.Set("Authorization", ...) and the
// http.DefaultClient.Do call. Run:
//
//	go test ./pkg/anthro/ -run TestFetch_NoCredentialFieldsInLogs
//
// Expect FAIL with the planted Bearer sentinel visible in the rendered
// TextHandler output. Revert the probe afterwards.
func TestFetch_NoCredentialFieldsInLogs(t *testing.T) {
	dir := t.TempDir()

	srv := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(sampleAPIBody))
	})
	withTestEndpoint(t, srv.URL)

	var buf bytes.Buffer
	prev := slog.Default()
	t.Cleanup(func() { slog.SetDefault(prev) })
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{
		Level: slog.LevelDebug,
	})))

	cred := Credential{
		AccessToken:      privacyAccessToken,
		RefreshToken:     privacyRefreshToken,
		Scopes:           []string{privacyScope},
		SubscriptionType: privacySubscriptionType,
		RateLimitTier:    privacyRateLimitTier,
	}
	_, err := Fetch(context.Background(), cred, dir)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}

	out := buf.String()
	for _, sentinel := range []string{
		privacyAccessToken,
		privacyRefreshToken,
		privacySubscriptionType,
		privacyRateLimitTier,
		privacyScope,
	} {
		if strings.Contains(out, sentinel) {
			t.Errorf("slog output leaked credential sentinel %q:\n%s", sentinel, out)
		}
	}
}

func TestCaptureLogsHelper(t *testing.T) {
	recs := captureLogs(t, slog.LevelDebug)
	slog.Debug("first", "k", "v")
	slog.Warn("second", "n", 42)
	got := recs()
	if len(got) != 2 {
		t.Fatalf("captured %d records, want 2", len(got))
	}
	if got[0].Level != slog.LevelDebug || got[0].Message != "first" {
		t.Errorf("rec[0] = %v %q", got[0].Level, got[0].Message)
	}
	if got[1].Level != slog.LevelWarn || got[1].Message != "second" {
		t.Errorf("rec[1] = %v %q", got[1].Level, got[1].Message)
	}
	if n, ok := attrMap(got[1])["n"].(int64); !ok || n != 42 {
		t.Errorf("rec[1].n = %v, want int64(42)", attrMap(got[1])["n"])
	}
}
