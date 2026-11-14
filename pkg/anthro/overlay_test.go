package anthro

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func rb(u float64, at time.Time) *reportedBucket {
	return &reportedBucket{Utilization: u, ResetsAt: at}
}

func sb(u float64, at, asOf, acc time.Time) *storedBucket {
	return &storedBucket{reportedBucket: reportedBucket{Utilization: u, ResetsAt: at}, AsOf: asOf, AcceptedAt: acc}
}

func bk(u float64, at time.Time) *Bucket {
	at = at.UTC()
	return &Bucket{Utilization: u, ResetsAt: &at}
}

func eqStored(a, b *storedBucket) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Utilization == b.Utilization && a.ResetsAt.Equal(b.ResetsAt) &&
		a.AsOf.Equal(b.AsOf) && a.AcceptedAt.Equal(b.AcceptedAt)
}

func fmtStored(s *storedBucket) string {
	if s == nil {
		return "<nil>"
	}
	return fmt.Sprintf("{util=%v resets=%s as_of=%s accepted=%s}", s.Utilization,
		s.ResetsAt.Format(time.RFC3339Nano), s.AsOf.Format(time.RFC3339), s.AcceptedAt.Format(time.RFC3339))
}

func TestMergeReading(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	R5 := now.Add(2 * time.Hour)
	R7 := now.Add(72 * time.Hour)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	recent := ago(5 * time.Minute) // a seen_at young enough not to be refreshed

	// R7j is an hour-aligned 7d reset as the headers report it; the jitter
	// variant is the API-style 08:59:59.6 for the same window.
	R7j := time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)
	R7jitter := R7j.Add(-400 * time.Millisecond)

	sess := func(five, seven *reportedBucket, seen time.Time) sessionReport {
		return sessionReport{FiveHour: five, SevenDay: seven, SeenAt: seen}
	}

	tests := []struct {
		name         string
		f            overlayFile
		r            StatuslineReading
		activity     time.Time
		want5, want7 *storedBucket
		wantChanged  bool
		check        func(t *testing.T, got overlayFile)
	}{
		{
			name:        "first ever, recent activity",
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			activity:    ago(time.Minute),
			want7:       sb(54, R7, now, now),
			wantChanged: true,
		},
		{
			name: "unknown session, old activity",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S2", SevenDay: bk(50, R7)},
			activity:    ago(6 * time.Minute),
			want7:       sb(54, R7, ago(time.Hour), ago(time.Hour)),
			wantChanged: true, // S2 remembered
			check: func(t *testing.T, got overlayFile) {
				t.Helper()
				s2, ok := got.Sessions["S2"]
				if !ok || s2.SevenDay == nil || s2.SevenDay.Utilization != 50 || !s2.SeenAt.Equal(now) {
					t.Errorf("S2 memory = %+v (present %v), want 7d=50 seen now", s2, ok)
				}
			},
		},
		{
			name:        "unknown session, no activity",
			r:           StatuslineReading{SessionID: "S2", SevenDay: bk(50, R7)},
			want7:       nil,
			wantChanged: true, // S2 remembered
			check: func(t *testing.T, got overlayFile) {
				t.Helper()
				if _, ok := got.Sessions["S2"]; !ok {
					t.Error("S2 not remembered")
				}
			},
		},
		{
			name: "changed from own last",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(55, R7)},
			want7:       sb(55, R7, now, now),
			wantChanged: true,
		},
		{
			name: "reset drop is new information",
			f: overlayFile{
				SevenDay: sb(95, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(95, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(0, R7)},
			want7:       sb(0, R7, now, now),
			wantChanged: true,
		},
		{
			name: "idle session after reset",
			f: overlayFile{
				SevenDay: sb(0, R7, ago(30*time.Minute), ago(30*time.Minute)),
				Sessions: map[string]sessionReport{"S2": sess(nil, rb(95, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S2", SevenDay: bk(95, R7)},
			activity:    ago(time.Minute),
			want7:       sb(0, R7, ago(30*time.Minute), ago(30*time.Minute)),
			wantChanged: false,
		},
		{
			name: "re-report equal, activity newer confirms",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(10*time.Minute), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			activity:    ago(8 * time.Minute),
			want7:       sb(54, R7, ago(8*time.Minute), ago(time.Hour)),
			wantChanged: true,
		},
		{
			name: "re-report equal, activity older",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(10*time.Minute), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			activity:    ago(11 * time.Minute),
			want7:       sb(54, R7, ago(10*time.Minute), ago(time.Hour)),
			wantChanged: false,
		},
		{
			name: "new info equal to stored",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(20*time.Minute), ago(time.Hour)),
				Sessions: map[string]sessionReport{
					"S1": sess(nil, rb(53, R7), recent),
					"S2": sess(nil, rb(54, R7), recent),
				},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			want7:       sb(54, R7, now, ago(time.Hour)),
			wantChanged: true,
		},
		{
			name:        "no session id raises",
			f:           overlayFile{SevenDay: sb(54, R7, ago(time.Hour), ago(time.Hour))},
			r:           StatuslineReading{SevenDay: bk(56, R7)},
			want7:       sb(56, R7, now, now),
			wantChanged: true,
		},
		{
			name:        "no session id cannot lower",
			f:           overlayFile{SevenDay: sb(54, R7, ago(time.Hour), ago(time.Hour))},
			r:           StatuslineReading{SevenDay: bk(50, R7)},
			want7:       sb(54, R7, ago(time.Hour), ago(time.Hour)),
			wantChanged: false,
		},
		{
			name:        "no session id newer window",
			f:           overlayFile{SevenDay: sb(54, now.Add(30*time.Minute), ago(time.Hour), ago(time.Hour))},
			r:           StatuslineReading{SevenDay: bk(3, now.Add(30*time.Minute+7*24*time.Hour))},
			want7:       sb(3, now.Add(30*time.Minute+7*24*time.Hour), now, now),
			wantChanged: true,
		},
		{
			name:  "no session id older window rejected",
			f:     overlayFile{SevenDay: sb(3, R7, ago(time.Hour), ago(time.Hour))},
			r:     StatuslineReading{SevenDay: bk(90, R7.Add(-7*24*time.Hour+time.Hour))},
			want7: sb(3, R7, ago(time.Hour), ago(time.Hour)),
		},
		{
			name: "absent bucket untouched",
			f: overlayFile{
				FiveHour: sb(10, R5, ago(time.Hour), ago(time.Hour)),
				SevenDay: sb(53, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(rb(10, R5), rb(53, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			want5:       sb(10, R5, ago(time.Hour), ago(time.Hour)),
			want7:       sb(54, R7, now, now),
			wantChanged: true,
		},
		{
			name: "resets_at minute jitter is a re-report",
			f: overlayFile{
				SevenDay: sb(54, R7j, ago(10*time.Minute), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7j), recent)},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7jitter)},
			activity:    ago(20 * time.Minute),
			want7:       sb(54, R7j, ago(10*time.Minute), ago(time.Hour)),
			wantChanged: false,
		},
		{
			name: "5h vanished, 7d unchanged",
			f: overlayFile{
				SevenDay: sb(0, R7, ago(30*time.Minute), ago(30*time.Minute)),
				Sessions: map[string]sessionReport{"S2": sess(rb(10, R5), rb(95, R7), recent)},
			},
			r:           StatuslineReading{SessionID: "S2", SevenDay: bk(95, R7)},
			activity:    ago(time.Minute),
			want7:       sb(0, R7, ago(30*time.Minute), ago(30*time.Minute)),
			wantChanged: true, // S2's memory drops its 5h
			check: func(t *testing.T, got overlayFile) {
				t.Helper()
				if s2 := got.Sessions["S2"]; s2.FiveHour != nil {
					t.Errorf("S2 memory still holds 5h %+v", *s2.FiveHour)
				}
			},
		},
		{
			name: "5h appeared",
			f: overlayFile{
				SevenDay: sb(50, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{
					"S1": sess(nil, rb(54, R7), recent),
					"S3": sess(nil, rb(50, R7), recent),
				},
			},
			r:           StatuslineReading{SessionID: "S1", FiveHour: bk(3, R5), SevenDay: bk(54, R7)},
			want5:       sb(3, R5, now, now),
			want7:       sb(54, R7, now, now),
			wantChanged: true,
		},
		{
			name: "other bucket changed",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(time.Hour), ago(time.Hour)),
				Sessions: map[string]sessionReport{
					"S1": sess(rb(10, R5), rb(55, R7), recent),
					"S3": sess(nil, rb(54, R7), recent),
				},
			},
			r:           StatuslineReading{SessionID: "S1", FiveHour: bk(12, R5), SevenDay: bk(55, R7)},
			want5:       sb(12, R5, now, now),
			want7:       sb(55, R7, now, now),
			wantChanged: true,
		},
		{
			name: "stale seen_at refreshed on re-report",
			f: overlayFile{
				SevenDay: sb(54, R7, ago(10*time.Minute), ago(time.Hour)),
				Sessions: map[string]sessionReport{"S1": sess(nil, rb(54, R7), ago(2*time.Hour))},
			},
			r:           StatuslineReading{SessionID: "S1", SevenDay: bk(54, R7)},
			want7:       sb(54, R7, ago(10*time.Minute), ago(time.Hour)),
			wantChanged: true,
			check: func(t *testing.T, got overlayFile) {
				t.Helper()
				if s := got.Sessions["S1"]; !s.SeenAt.Equal(now) {
					t.Errorf("S1 seen_at = %v, want %v", s.SeenAt, now)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, err := json.Marshal(tt.f)
			if err != nil {
				t.Fatal(err)
			}
			got, changed := mergeReading(tt.f, tt.r, tt.activity, now)
			if !eqStored(got.FiveHour, tt.want5) {
				t.Errorf("FiveHour = %s, want %s", fmtStored(got.FiveHour), fmtStored(tt.want5))
			}
			if !eqStored(got.SevenDay, tt.want7) {
				t.Errorf("SevenDay = %s, want %s", fmtStored(got.SevenDay), fmtStored(tt.want7))
			}
			if changed != tt.wantChanged {
				t.Errorf("changed = %v, want %v", changed, tt.wantChanged)
			}
			if tt.check != nil {
				tt.check(t, got)
			}
			// mergeReading is pure: the input file must come through untouched.
			after, err := json.Marshal(tt.f)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Errorf("mergeReading mutated its input:\nbefore %s\nafter  %s", before, after)
			}
		})
	}
}

func TestMergeReading_SessionPruning(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	R7 := now.Add(72 * time.Hour)
	r := StatuslineReading{SevenDay: bk(54, R7)} // no session id: adds no memory

	t.Run("older than 8 days dropped", func(t *testing.T) {
		t.Parallel()
		f := overlayFile{Sessions: map[string]sessionReport{
			"old":   {SevenDay: rb(1, R7), SeenAt: now.Add(-8*24*time.Hour - time.Minute)},
			"young": {SevenDay: rb(1, R7), SeenAt: now.Add(-7 * 24 * time.Hour)},
		}}
		got, changed := mergeReading(f, r, time.Time{}, now)
		if !changed {
			t.Error("changed = false, want true")
		}
		if _, ok := got.Sessions["old"]; ok {
			t.Error("old session kept")
		}
		if _, ok := got.Sessions["young"]; !ok {
			t.Error("young session dropped")
		}
	})

	t.Run("cap keeps the newest 256", func(t *testing.T) {
		t.Parallel()
		f := overlayFile{Sessions: map[string]sessionReport{}}
		for i := range 300 {
			// session i was seen i minutes ago: s299 is the oldest.
			f.Sessions[fmt.Sprintf("s%03d", i)] = sessionReport{SevenDay: rb(1, R7), SeenAt: now.Add(-time.Duration(i) * time.Minute)}
		}
		got, _ := mergeReading(f, r, time.Time{}, now)
		if n := len(got.Sessions); n != 256 {
			t.Fatalf("len(sessions) = %d, want 256", n)
		}
		for i := range 300 {
			_, ok := got.Sessions[fmt.Sprintf("s%03d", i)]
			if wantKept := i < 256; ok != wantKept {
				t.Errorf("s%03d kept = %v, want %v", i, ok, wantKept)
			}
		}
	})
}

// touchTranscript creates a transcript file under dir with the given mtime
// and returns its path.
func touchTranscript(t *testing.T, dir, name string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, name+".jsonl")
	if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func readRawOverlay(t *testing.T, cacheDir string) overlayFile {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(cacheDir, overlayFileName))
	if err != nil {
		t.Fatal(err)
	}
	var f overlayFile
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestIngestStatusline_WritesOnlyWhenChanged(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	now := time.Now()
	tp := touchTranscript(t, t.TempDir(), "s1", now.Add(-time.Minute))
	r := StatuslineReading{SessionID: "S1", TranscriptPath: tp, SevenDay: bk(54, now.Add(72*time.Hour))}

	changed, err := IngestStatusline(cacheDir, r, now)
	if err != nil || !changed {
		t.Fatalf("first ingest = (%v, %v), want (true, nil)", changed, err)
	}
	path := filepath.Join(cacheDir, overlayFileName)
	old := now.Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}

	changed, err = IngestStatusline(cacheDir, r, now.Add(time.Second))
	if err != nil || changed {
		t.Fatalf("second ingest = (%v, %v), want (false, nil)", changed, err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.ModTime().Equal(old) {
		t.Errorf("overlay rewritten on an unchanged ingest: mtime %v, want %v", fi.ModTime(), old)
	}
}

// Review Focus 1: several sessions rendering at once.
func TestIngestStatusline_ConcurrentWriters(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	tDir := t.TempDir()
	now := time.Now()
	const n = 16
	paths := make([]string, n)
	for i := range n {
		paths[i] = touchTranscript(t, tDir, fmt.Sprintf("s%02d", i), now)
	}

	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			r := StatuslineReading{
				SessionID:      fmt.Sprintf("S%02d", i),
				TranscriptPath: paths[i],
				SevenDay:       bk(float64(40+i), now.Add(72*time.Hour)),
			}
			if _, err := IngestStatusline(cacheDir, r, now); err != nil {
				t.Errorf("ingest %d: %v", i, err)
			}
		})
	}
	wg.Wait()

	if _, err := ReadOverlayFile(cacheDir); err != nil {
		t.Fatalf("ReadOverlayFile: %v", err)
	}
	if got := len(readRawOverlay(t, cacheDir).Sessions); got != n {
		t.Errorf("sessions = %d, want %d (a concurrent writer lost an entry)", got, n)
	}
}

// Review Focus 2: corrupt or future-version overlay file.
func TestIngestStatusline_RecoversFromCorruptFile(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"garbage":        `{garbage`,
		"future version": `{"v":99,"seven_day":{"utilization":12}}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cacheDir := t.TempDir()
			now := time.Now()
			path := filepath.Join(cacheDir, overlayFileName)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if ov := ReadOverlay(cacheDir, now); !ov.Empty() {
				t.Errorf("ReadOverlay = %+v, want empty", ov)
			}
			if _, err := ReadOverlayFile(cacheDir); err == nil {
				t.Error("ReadOverlayFile err = nil, want non-nil for an unusable file")
			}

			r := StatuslineReading{SevenDay: bk(54, now.Add(72*time.Hour))}
			if _, err := IngestStatusline(cacheDir, r, now); err != nil {
				t.Fatalf("IngestStatusline: %v", err)
			}
			ov := ReadOverlay(cacheDir, now)
			if ov.SevenDay == nil || ov.SevenDay.Utilization != 54 {
				t.Errorf("after ingest SevenDay = %+v, want 54", ov.SevenDay)
			}
		})
	}
}

func TestReadOverlay_ExpiresPassedBuckets(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	now := time.Now()
	r := StatuslineReading{FiveHour: bk(10, now.Add(-time.Second)), SevenDay: bk(54, now.Add(72*time.Hour))}
	if _, err := IngestStatusline(cacheDir, r, now); err != nil {
		t.Fatal(err)
	}

	ov := ReadOverlay(cacheDir, now)
	if ov.FiveHour != nil {
		t.Errorf("ReadOverlay FiveHour = %+v, want nil (reset passed)", ov.FiveHour)
	}
	if ov.SevenDay == nil || ov.SevenDay.Utilization != 54 {
		t.Errorf("ReadOverlay SevenDay = %+v, want 54", ov.SevenDay)
	}
	raw, err := ReadOverlayFile(cacheDir)
	if err != nil {
		t.Fatal(err)
	}
	if raw.FiveHour == nil || raw.SevenDay == nil {
		t.Errorf("ReadOverlayFile = %+v, want both buckets", raw)
	}
}

func TestReadOverlay_MissingFile(t *testing.T) {
	t.Parallel()
	cacheDir := t.TempDir()
	if ov := ReadOverlay(cacheDir, time.Now()); !ov.Empty() {
		t.Errorf("ReadOverlay = %+v, want empty", ov)
	}
	ov, err := ReadOverlayFile(cacheDir)
	if err != nil || !ov.Empty() {
		t.Errorf("ReadOverlayFile = (%+v, %v), want (empty, nil)", ov, err)
	}
}

func TestOverlayFilePermissions(t *testing.T) {
	t.Parallel()
	cacheDir := filepath.Join(t.TempDir(), "cache")
	now := time.Now()
	if _, err := IngestStatusline(cacheDir, StatuslineReading{SevenDay: bk(54, now.Add(72*time.Hour))}, now); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(cacheDir, overlayFileName))
	if err != nil {
		t.Fatal(err)
	}
	if perm := fi.Mode().Perm(); perm != 0o600 {
		t.Errorf("overlay mode = %o, want 600", perm)
	}
}

func TestOverlay_NewestAsOfAndEmpty(t *testing.T) {
	t.Parallel()
	t1 := time.Date(2026, 11, 14, 11, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Minute)
	if o := (Overlay{}); !o.Empty() || !o.NewestAsOf().IsZero() {
		t.Errorf("zero overlay: Empty=%v NewestAsOf=%v", o.Empty(), o.NewestAsOf())
	}
	o := Overlay{
		FiveHour: &OverlayBucket{AsOf: t2},
		SevenDay: &OverlayBucket{AsOf: t1},
	}
	if o.Empty() {
		t.Error("Empty = true for a populated overlay")
	}
	if got := o.NewestAsOf(); !got.Equal(t2) {
		t.Errorf("NewestAsOf = %v, want %v", got, t2)
	}
	if got := (Overlay{SevenDay: &OverlayBucket{AsOf: t1}}).NewestAsOf(); !got.Equal(t1) {
		t.Errorf("NewestAsOf (7d only) = %v, want %v", got, t1)
	}
}

func TestPendingStatuslineSample(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	R5 := now.Add(2 * time.Hour)
	R7 := now.Add(72 * time.Hour)

	tests := []struct {
		name   string
		raw    string // written verbatim instead of f when non-empty
		f      *overlayFile
		wantOK bool
		wantAt time.Time
		want5  any
		want7  any
	}{
		{name: "missing file"},
		{name: "corrupt file", raw: `{garbage`},
		{
			name: "accepted 10s ago is not settled",
			f:    &overlayFile{SevenDay: sb(54, R7, ago(10*time.Second), ago(10*time.Second))},
		},
		{
			name:   "held 40s, nothing recorded",
			f:      &overlayFile{SevenDay: sb(54, R7, ago(5*time.Second), ago(40*time.Second))},
			wantOK: true, wantAt: ago(40 * time.Second), want7: 54.0,
		},
		{
			name: "same values as recorded",
			f: &overlayFile{
				SevenDay: sb(54, R7, ago(5*time.Second), ago(40*time.Second)),
				Recorded: &recordedSample{At: ago(2 * time.Minute), SevenDay: rb(54, R7)},
			},
		},
		{
			name: "different from recorded, recorded 50s ago",
			f: &overlayFile{
				SevenDay: sb(55, R7, ago(5*time.Second), ago(40*time.Second)),
				Recorded: &recordedSample{At: ago(50 * time.Second), SevenDay: rb(54, R7)},
			},
		},
		{
			name: "different from recorded, recorded 70s ago",
			f: &overlayFile{
				SevenDay: sb(55, R7, ago(5*time.Second), ago(40*time.Second)),
				Recorded: &recordedSample{At: ago(70 * time.Second), SevenDay: rb(54, R7)},
			},
			wantOK: true, wantAt: ago(40 * time.Second), want7: 55.0,
		},
		{
			name: "only bucket expired",
			f:    &overlayFile{SevenDay: sb(54, ago(time.Second), ago(5*time.Minute), ago(5*time.Minute))},
		},
		{
			name: "expired bucket excluded, live one recorded",
			f: &overlayFile{
				FiveHour: sb(10, ago(time.Second), ago(5*time.Minute), ago(5*time.Minute)),
				SevenDay: sb(54, R7, ago(time.Minute), ago(time.Minute)),
			},
			wantOK: true, wantAt: ago(time.Minute), want7: 54.0,
		},
		{
			name: "both buckets: at is the latest accepted_at",
			f: &overlayFile{
				FiveHour: sb(3, R5, ago(time.Minute), ago(5*time.Minute)),
				SevenDay: sb(54, R7, ago(time.Minute), ago(40*time.Second)),
			},
			wantOK: true, wantAt: ago(40 * time.Second), want5: 3.0, want7: 54.0,
		},
		{
			name: "one bucket still settling holds the pair",
			f: &overlayFile{
				FiveHour: sb(3, R5, ago(10*time.Second), ago(10*time.Second)),
				SevenDay: sb(54, R7, ago(time.Minute), ago(5*time.Minute)),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			path := filepath.Join(dir, overlayFileName)
			switch {
			case tt.raw != "":
				if err := os.WriteFile(path, []byte(tt.raw), 0o600); err != nil {
					t.Fatal(err)
				}
			case tt.f != nil:
				f := *tt.f
				f.V = overlayVersion
				if err := writeOverlayRaw(path, f); err != nil {
					t.Fatal(err)
				}
			}
			u, at, ok := PendingStatuslineSample(dir, now)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !ok {
				return
			}
			if !at.Equal(tt.wantAt) {
				t.Errorf("at = %v, want %v", at, tt.wantAt)
			}
			util := func(b *Bucket) any {
				if b == nil {
					return nil
				}
				return b.Utilization
			}
			if got := util(u.FiveHour); got != tt.want5 {
				t.Errorf("FiveHour = %v, want %v", got, tt.want5)
			}
			if got := util(u.SevenDay); got != tt.want7 {
				t.Errorf("SevenDay = %v, want %v", got, tt.want7)
			}
			if u.Limits != nil || u.ExtraUsage != nil || u.SevenDaySonnet != nil {
				t.Errorf("sample carries non-statusline fields: %+v", u)
			}
		})
	}
}

func TestMarkStatuslineRecorded_SuppressesRepeat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	R7 := now.Add(72 * time.Hour)
	f := overlayFile{V: overlayVersion, SevenDay: sb(54, R7, now.Add(-5*time.Second), now.Add(-40*time.Second))}
	if err := writeOverlayRaw(filepath.Join(dir, overlayFileName), f); err != nil {
		t.Fatal(err)
	}

	u, at, ok := PendingStatuslineSample(dir, now)
	if !ok {
		t.Fatal("first Pending ok = false, want true")
	}
	if err := MarkStatuslineRecorded(dir, u, at); err != nil {
		t.Fatalf("MarkStatuslineRecorded: %v", err)
	}
	if _, _, ok := PendingStatuslineSample(dir, now.Add(time.Minute)); ok {
		t.Error("Pending after Mark ok = true, want false (same reading already recorded)")
	}

	// A new stored reading must hold 30s before it is pending again.
	if _, err := IngestStatusline(dir, StatuslineReading{SevenDay: bk(56, R7)}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := PendingStatuslineSample(dir, now.Add(time.Minute+10*time.Second)); ok {
		t.Error("Pending 10s after a new reading ok = true, want false")
	}
	u, at, ok = PendingStatuslineSample(dir, now.Add(time.Minute+31*time.Second))
	if !ok || u.SevenDay == nil || u.SevenDay.Utilization != 56 || !at.Equal(now.Add(time.Minute)) {
		t.Errorf("Pending after hold = (%+v, %v, %v), want 7d=56 at %v", u.SevenDay, at, ok, now.Add(time.Minute))
	}
	// Mark keeps the overlay's readings intact.
	if ov := ReadOverlay(dir, now.Add(time.Minute+31*time.Second)); ov.SevenDay == nil || ov.SevenDay.Utilization != 56 {
		t.Errorf("overlay after Mark = %+v, want 7d=56", ov.SevenDay)
	}
}
