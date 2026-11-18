package anthro

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	fixtureSessionID      = "00000000-0000-0000-0000-000000000000"
	fixtureTranscriptPath = "/fixture/projects/x/00000000-0000-0000-0000-000000000000.jsonl"
	// fixture5hResets / fixture7dResets are the far-future resets_at epochs
	// the scrubbed fixture carries (2100-01-01 and six days later).
	fixture5hResets = 4102444800
	fixture7dResets = 4102963200
)

// bucketJSON renders one rate_limits bucket with numeric fields.
func bucketJSON(pct float64, resets int64) string {
	return fmt.Sprintf(`{"used_percentage":%v,"resets_at":%d}`, pct, resets)
}

func TestParseStatusline(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	in2h := now.Add(2 * time.Hour).Unix()
	in3d := now.Add(72 * time.Hour).Unix()

	fixture, err := os.ReadFile("testdata/statusline-payload.json")
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's resets_at sit in 2100 so later tests never expire; its
	// case pins the clock two hours before the 5h reset, which puts the 7d
	// reset at now+6d2h — inside both plausibility bounds.
	fixtureNow := time.Unix(fixture5hResets, 0).Add(-2 * time.Hour)

	type want struct {
		five, seven *float64 // nil → bucket must be nil
		noQuota     bool     // errors.Is(err, ErrNoStatuslineQuota)
		otherErr    bool     // non-nil error that is NOT ErrNoStatuslineQuota
	}
	f := func(v float64) *float64 { return &v }

	tests := []struct {
		name string
		body string
		now  time.Time
		want want
	}{
		{name: "fixture", body: string(fixture), now: fixtureNow, want: want{five: f(12), seven: f(60)}},
		{
			name: "five hour only",
			body: `{"session_id":"s","rate_limits":{"five_hour":` + bucketJSON(12, in2h) + `}}`,
			want: want{five: f(12)},
		},
		{
			name: "numeric strings",
			body: fmt.Sprintf(`{"rate_limits":{"seven_day":{"used_percentage":"42","resets_at":"%d"}}}`, in3d),
			want: want{seven: f(42)},
		},
		{
			name: "fractional",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(42.5, in3d) + `}}`,
			want: want{seven: f(42.5)},
		},
		{
			name: "at limit",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(100.0, in3d) + `}}`,
			want: want{seven: f(100)},
		},
		{
			name: "over limit",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(104.2, in3d) + `}}`,
			want: want{seven: f(104.2)},
		},
		{
			name: "absurd",
			body: `{"rate_limits":{"five_hour":` + bucketJSON(1e308, in2h) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "at plausibility bound",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(1000, in3d) + `}}`,
			want: want{seven: f(1000)},
		},
		{
			name: "just over plausibility bound",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(1000.5, in3d) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "negative rejects that bucket only",
			body: `{"rate_limits":{"five_hour":` + bucketJSON(-1, in2h) + `,"seven_day":` + bucketJSON(54, in3d) + `}}`,
			want: want{seven: f(54)},
		},
		{
			name: "negative only bucket",
			body: `{"rate_limits":{"five_hour":` + bucketJSON(-1, in2h) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "NaN as string",
			body: fmt.Sprintf(`{"rate_limits":{"seven_day":{"used_percentage":"NaN","resets_at":%d}}}`, in3d),
			want: want{noQuota: true},
		},
		{
			name: "Inf as string",
			body: fmt.Sprintf(`{"rate_limits":{"seven_day":{"used_percentage":"+Inf","resets_at":%d}}}`, in3d),
			want: want{noQuota: true},
		},
		{
			name: "non-numeric string",
			body: fmt.Sprintf(`{"rate_limits":{"seven_day":{"used_percentage":"lots","resets_at":%d}}}`, in3d),
			want: want{noQuota: true},
		},
		{
			name: "missing used_percentage",
			body: fmt.Sprintf(`{"rate_limits":{"seven_day":{"resets_at":%d}}}`, in3d),
			want: want{noQuota: true},
		},
		{
			name: "missing resets_at",
			body: `{"rate_limits":{"seven_day":{"used_percentage":54}}}`,
			want: want{noQuota: true},
		},
		{
			name: "reset passed",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(54, now.Unix()-1) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "reset exactly now",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(54, now.Unix()) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "5h reset too far",
			body: `{"rate_limits":{"five_hour":` + bucketJSON(3, now.Add(5*time.Hour+11*time.Minute).Unix()) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "5h reset at bound",
			body: `{"rate_limits":{"five_hour":` + bucketJSON(3, now.Add(5*time.Hour+10*time.Minute).Unix()) + `}}`,
			want: want{five: f(3)},
		},
		{
			name: "7d reset too far",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(54, now.Add(7*24*time.Hour+61*time.Minute).Unix()) + `}}`,
			want: want{noQuota: true},
		},
		{
			name: "7d reset at bound",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(54, now.Add(7*24*time.Hour+time.Hour).Unix()) + `}}`,
			want: want{seven: f(54)},
		},
		{
			name: "milliseconds epoch",
			body: `{"rate_limits":{"seven_day":` + bucketJSON(54, in3d*1000) + `}}`,
			want: want{noQuota: true},
		},
		{name: "rate_limits null", body: `{"session_id":"s","rate_limits":null}`, want: want{noQuota: true}},
		{name: "rate_limits missing", body: `{"session_id":"s"}`, want: want{noQuota: true}},
		{
			name: "only spend_limit",
			body: `{"rate_limits":{"spend_limit":{"used":3,"limit":100}}}`,
			want: want{noQuota: true},
		},
		{name: "invalid json", body: `{not json`, want: want{otherErr: true}},
		{name: "empty input", body: ``, want: want{otherErr: true}},
		{
			name: "unknown fields ignored",
			body: `{"terminal_width":210,"prompt_cache":{"warm":true},"rate_limits":{"seven_day":` +
				bucketJSON(54, in3d) + `,"future_bucket":{"x":1}}}`,
			want: want{seven: f(54)},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			clock := now
			if !tt.now.IsZero() {
				clock = tt.now
			}
			got, err := ParseStatusline([]byte(tt.body), clock)
			switch {
			case tt.want.noQuota:
				if !errors.Is(err, ErrNoStatuslineQuota) {
					t.Fatalf("err = %v, want ErrNoStatuslineQuota", err)
				}
				return
			case tt.want.otherErr:
				if err == nil || errors.Is(err, ErrNoStatuslineQuota) {
					t.Fatalf("err = %v, want a decode error that is not ErrNoStatuslineQuota", err)
				}
				return
			case err != nil:
				t.Fatalf("unexpected err: %v", err)
			}
			checkBucket(t, "FiveHour", got.FiveHour, tt.want.five)
			checkBucket(t, "SevenDay", got.SevenDay, tt.want.seven)
		})
	}
}

func checkBucket(t *testing.T, name string, got *Bucket, want *float64) {
	t.Helper()
	if want == nil {
		if got != nil {
			t.Errorf("%s = %+v, want nil", name, *got)
		}
		return
	}
	if got == nil {
		t.Fatalf("%s = nil, want utilization %v", name, *want)
	}
	if got.Utilization != *want {
		t.Errorf("%s.Utilization = %v, want %v", name, got.Utilization, *want)
	}
	if got.ResetsAt == nil {
		t.Errorf("%s.ResetsAt = nil; a kept bucket must carry its reset", name)
	}
}

func TestParseStatusline_FixtureIdentityAndResets(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("testdata/statusline-payload.json")
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseStatusline(b, time.Unix(fixture5hResets, 0).Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if got.SessionID != fixtureSessionID {
		t.Errorf("SessionID = %q, want %q", got.SessionID, fixtureSessionID)
	}
	if got.TranscriptPath != fixtureTranscriptPath {
		t.Errorf("TranscriptPath = %q, want %q", got.TranscriptPath, fixtureTranscriptPath)
	}
	if want := time.Unix(fixture5hResets, 0).UTC(); got.FiveHour == nil || !got.FiveHour.ResetsAt.Equal(want) {
		t.Errorf("FiveHour.ResetsAt = %v, want %v", got.FiveHour, want)
	}
	if want := time.Unix(fixture7dResets, 0).UTC(); got.SevenDay == nil || !got.SevenDay.ResetsAt.Equal(want) {
		t.Errorf("SevenDay.ResetsAt = %v, want %v", got.SevenDay, want)
	}
	if loc := got.SevenDay.ResetsAt.Location(); loc != time.UTC {
		t.Errorf("ResetsAt location = %v, want UTC", loc)
	}
}

func TestTranscriptActivity(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

	// setup builds <dir>/<id>.jsonl (mtime main) and, per extra entry,
	// <dir>/<id>/subagents/<name> with the given mtime. A zero main skips
	// creating the main transcript.
	type file struct {
		name  string
		mtime time.Time
	}
	setup := func(t *testing.T, main time.Time, subs []file) string {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, "sess.jsonl")
		touch := func(p string, m time.Time) {
			if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(p, m, m); err != nil {
				t.Fatal(err)
			}
		}
		if !main.IsZero() {
			touch(path, main)
		}
		if len(subs) > 0 {
			sd := filepath.Join(dir, "sess", "subagents")
			if err := os.MkdirAll(sd, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, s := range subs {
				touch(filepath.Join(sd, s.name), s.mtime)
			}
		}
		return path
	}

	tests := []struct {
		name string
		main time.Time
		subs []file
		path func(string) string // optional rewrite of the built path
		want time.Time
	}{
		{name: "main only", main: now.Add(-3 * time.Minute), want: now.Add(-3 * time.Minute)},
		{
			name: "newer subagent wins",
			main: now.Add(-40 * time.Minute),
			subs: []file{{"agent-a.jsonl", now.Add(-time.Minute)}, {"agent-b.jsonl", now.Add(-20 * time.Minute)}},
			want: now.Add(-time.Minute),
		},
		{
			name: "older subagent loses",
			main: now.Add(-2 * time.Minute),
			subs: []file{{"agent-a.jsonl", now.Add(-time.Hour)}},
			want: now.Add(-2 * time.Minute),
		},
		{
			name: "non-jsonl in subagents ignored",
			main: now.Add(-40 * time.Minute),
			subs: []file{{"agent-a.meta", now.Add(-time.Minute)}},
			want: now.Add(-40 * time.Minute),
		},
		{name: "future mtime clamped", main: now.Add(time.Hour), want: now},
		{
			name: "future subagent clamped",
			main: now.Add(-time.Hour),
			subs: []file{{"agent-a.jsonl", now.Add(time.Hour)}},
			want: now,
		},
		{name: "empty path", main: now, path: func(string) string { return "" }},
		{name: "missing main transcript", subs: []file{{"agent-a.jsonl", now.Add(-time.Minute)}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := setup(t, tt.main, tt.subs)
			if tt.path != nil {
				path = tt.path(path)
			}
			got := transcriptActivity(path, now)
			if !got.Equal(tt.want) {
				t.Errorf("transcriptActivity = %v, want %v", got, tt.want)
			}
		})
	}
}
