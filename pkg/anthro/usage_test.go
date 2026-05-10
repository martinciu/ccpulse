package anthro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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
	if !u.FiveHour.ResetsAt.Equal(want) {
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
