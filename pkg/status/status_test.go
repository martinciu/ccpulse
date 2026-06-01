package status

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/martinciu/ccpulse/pkg/anthro"
	"github.com/martinciu/ccpulse/pkg/cache"
)

// intPtr returns &n. Test-only helper for the *int Window fields.
func intPtr(n int) *int { return &n }

// timePtr returns &t. Test-only helper for the *time.Time
// anthro.Bucket.ResetsAt field on sites that build the value inline.
func timePtr(t time.Time) *time.Time { return &t }

func freshDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE messages (
		ts TEXT, input_tokens INTEGER, output_tokens INTEGER,
		cache_read_tokens INTEGER, cache_write_5m_tokens INTEGER,
		cache_write_1h_tokens INTEGER, cost_usd_estimate REAL)`); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestComputeWithoutQuota(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	_, _ = db.ExecContext(t.Context(), `INSERT INTO messages VALUES (?, 100, 50, 0, 0, 0, 0.01)`,
		now.Add(-1*time.Hour).Format("2006-01-02T15:04:05.000Z07:00"))
	w, err := Compute(t.Context(), db, now, QuotaInput{TierSlug: "unknown", TierPretty: "Unknown"})
	if err != nil {
		t.Fatal(err)
	}
	if w.Percent != 0 {
		t.Errorf("Percent = %d, want 0 without quota", w.Percent)
	}
	if w.MinutesToReset == nil || *w.MinutesToReset < 230 || *w.MinutesToReset > 250 {
		t.Errorf("MinutesToReset = %v, want ~240", w.MinutesToReset)
	}
	if w.CeilingLabel != "unknown" {
		t.Errorf("CeilingLabel = %q", w.CeilingLabel)
	}
}

func TestComputeWithQuota(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC)
	resetsAt := now.Add(70 * time.Minute)
	usage := &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.7, ResetsAt: &resetsAt}}
	w, err := Compute(t.Context(), db, now, QuotaInput{
		Usage: usage, Source: "api", UpdatedAt: now,
		TierSlug: "max_20x", TierPretty: "Max 20x",
	})
	if err != nil {
		t.Fatal(err)
	}
	if w.Percent != 13 {
		t.Errorf("Percent = %d, want 13 (rounded)", w.Percent)
	}
	if w.MinutesToReset == nil || *w.MinutesToReset != 70 {
		t.Errorf("MinutesToReset = %v, want 70", w.MinutesToReset)
	}
	if w.CeilingLabel != "max_20x" || w.CeilingPretty != "Max 20x" {
		t.Errorf("Ceiling labels: %q / %q", w.CeilingLabel, w.CeilingPretty)
	}
	if w.Quota == nil {
		t.Errorf("Quota should be set")
	}
}

func TestJSONOutputIncludesQuota(t *testing.T) {
	w := Window{
		Percent: 13, MinutesToReset: intPtr(70),
		CeilingLabel: "max_20x", CeilingPretty: "Max 20x",
		Quota:          &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12.7}},
		QuotaSource:    "api",
		QuotaUpdatedAt: time.Date(2026, 5, 9, 15, 0, 0, 0, time.UTC),
	}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	for _, want := range []string{`"quota":`, `"quota_source":"api"`, `"five_hour":`, `"ceiling_pretty":"Max 20x"`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s in %s", want, out)
		}
	}
}

func TestJSONOutputOmitsQuotaWhenAbsent(t *testing.T) {
	w := Window{Percent: 0, CeilingLabel: "unknown"}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "quota") {
		t.Errorf("JSON should omit quota fields when nil: %s", out)
	}
}

func TestCompute_PopulatesSevenDay(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	resets5h := now.Add(2*time.Hour + 3*time.Minute)
	resets7d := now.Add(17*time.Hour + 33*time.Minute)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 14.0, ResetsAt: &resets5h},
		SevenDay: &anthro.Bucket{Utilization: 89.0, ResetsAt: &resets7d},
	}
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if !w.Has7d {
		t.Errorf("Has7d = false, want true")
	}
	if w.Percent7d != 89 {
		t.Errorf("Percent7d = %d, want 89", w.Percent7d)
	}
	if w.MinutesToReset7d == nil || *w.MinutesToReset7d != 17*60+33 {
		t.Errorf("MinutesToReset7d = %v, want %d", w.MinutesToReset7d, 17*60+33)
	}
}

func TestCompute_OmitsSevenDayWhenSevenDayNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 14.0, ResetsAt: timePtr(now.Add(2 * time.Hour))},
	}
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Has7d {
		t.Errorf("Has7d = true, want false")
	}
	if w.Percent7d != 0 || w.MinutesToReset7d != nil {
		t.Errorf("7d fields nonzero: percent=%d minutes=%v", w.Percent7d, w.MinutesToReset7d)
	}
}

func TestCompute_OmitsSevenDayWhenUsageNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: nil, Source: "cache_stale", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Has7d {
		t.Errorf("Has7d = true, want false")
	}
}

func TestCompute_PopulatesProjection(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	// 5h: 1h elapsed, 12% used → projects 60% at reset, ok confidence.
	resets5h := now.Add(4 * time.Hour)
	// 7d: 3 days elapsed, 30% used → projects 70% at reset, ok confidence.
	resets7d := now.Add(4 * 24 * time.Hour)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: &resets5h},
		SevenDay: &anthro.Bucket{Utilization: 30.0, ResetsAt: &resets7d},
	}
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Projection == nil {
		t.Fatal("Window.Projection = nil, want populated")
	}
	if w.Projection.FiveHour == nil {
		t.Fatal("Projection.FiveHour = nil, want populated")
	}
	if got := w.Projection.FiveHour.ProjectedPctAtReset; got != 60 {
		t.Errorf("FiveHour.ProjectedPctAtReset = %d, want 60", got)
	}
	if got := w.Projection.FiveHour.Confidence; got != "ok" {
		t.Errorf("FiveHour.Confidence = %q, want ok", got)
	}
	if w.Projection.SevenDay == nil {
		t.Fatal("Projection.SevenDay = nil, want populated")
	}
	if got := w.Projection.SevenDay.ProjectedPctAtReset; got != 70 {
		t.Errorf("SevenDay.ProjectedPctAtReset = %d, want 70", got)
	}
}

func TestCompute_OmitsProjectionWhenQuotaNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: nil, Source: "cache_stale", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Projection != nil {
		t.Errorf("Window.Projection = %+v, want nil when Usage is nil", w.Projection)
	}
}

func TestCompute_OmitsSevenDayProjectionWhenSevenDayNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	resets5h := now.Add(4 * time.Hour)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: &resets5h},
	}
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Projection == nil {
		t.Fatal("Window.Projection = nil, want populated when 5h is set")
	}
	if w.Projection.FiveHour == nil {
		t.Errorf("Projection.FiveHour = nil, want populated")
	}
	if w.Projection.SevenDay != nil {
		t.Errorf("Projection.SevenDay = %+v, want nil when SevenDay is nil", w.Projection.SevenDay)
	}
}

func TestCompute_OmitsSevenDayProjectionWhenResetsAtNil(t *testing.T) {
	db := freshDB(t)
	now := time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)
	resets5h := now.Add(4 * time.Hour)
	usage := &anthro.Usage{
		FiveHour: &anthro.Bucket{Utilization: 12.0, ResetsAt: &resets5h},
		SevenDay: &anthro.Bucket{Utilization: 30.0, ResetsAt: nil},
	}
	w, err := Compute(t.Context(), db, now, QuotaInput{Usage: usage, Source: "api", UpdatedAt: now})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Has7d {
		t.Errorf("Has7d = true, want false when SevenDay.ResetsAt is nil")
	}
	if w.Projection != nil && w.Projection.SevenDay != nil {
		t.Errorf("Projection.SevenDay = %+v, want nil when SevenDay.ResetsAt is nil (avoid 'warming up' on the 7d-glitch path)", w.Projection.SevenDay)
	}
}

func TestJSONOutputIncludesProjection(t *testing.T) {
	mins := 165
	w := Window{
		Percent:      13,
		CeilingLabel: "max_20x",
		Projection: &Projections{
			FiveHour: &Projection{
				ElapsedMinutes:      120,
				SlopePctPerHour:     21.00,
				ProjectedPctAtReset: 105,
				WillOverreach:       true,
				MinutesTo100Pct:     &mins,
				Confidence:          "ok",
			},
		},
	}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"projection":`,
		`"five_hour":`,
		`"slope_pct_per_hour":21`,
		`"will_overreach":true`,
		`"minutes_to_100_pct":165`,
		`"confidence":"ok"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s in %s", want, out)
		}
	}
}

func TestJSONOutputOmitsProjectionWhenNil(t *testing.T) {
	w := Window{Percent: 0, CeilingLabel: "unknown"}
	out, err := JSON(w)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "projection") {
		t.Errorf("JSON should omit projection when Projection is nil: %s", out)
	}
}

func TestCompute_Tokens5hBreakdown_SumsCorrectly(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(t.Context(), dir+"/state.db")
	if err != nil {
		t.Fatalf("Open cache: %v", err)
	}
	defer c.Close()
	db := c.DB()

	now := time.Now().UTC()
	ts := now.Add(-1 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")

	if _, err := db.ExecContext(t.Context(), `
INSERT INTO messages (
	session_id, message_id, project_slug, ts, role, model,
	input_tokens, output_tokens, cache_read_tokens,
	cache_write_5m_tokens, cache_write_1h_tokens,
	cost_usd_estimate, pricing_version, pricing_unknown
) VALUES ('s', 'synthetic:'||?, 'p', ?, 'assistant', 'claude-opus-4-7',
	100, 50, 1000, 200, 75, 0.012345, 'v1', 0)`, ts, ts); err != nil {
		t.Fatalf("seed: %v", err)
	}

	w, err := Compute(t.Context(), db, now, QuotaInput{})
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}

	// tokens_5h is input + output only (no cache).
	if got, want := w.Tokens5h, int64(150); got != want {
		t.Errorf("Tokens5h = %d, want %d (= 100 input + 50 output)", got, want)
	}

	// Breakdown carries all five kinds verbatim.
	want := TokensBreakdown{
		Input:        100,
		Output:       50,
		CacheRead:    1000,
		CacheWrite5m: 200,
		CacheWrite1h: 75,
	}
	if w.Tokens5hBreakdown != want {
		t.Errorf("Tokens5hBreakdown = %+v, want %+v", w.Tokens5hBreakdown, want)
	}

	// Invariant: tokens_5h == breakdown.Input + breakdown.Output.
	if w.Tokens5h != w.Tokens5hBreakdown.Input+w.Tokens5hBreakdown.Output {
		t.Errorf("Tokens5h (%d) != Breakdown.Input+Output (%d+%d)",
			w.Tokens5h, w.Tokens5hBreakdown.Input, w.Tokens5hBreakdown.Output)
	}

	// Regression guard: breakdown five-field sum equals the pre-change broad total.
	broad := w.Tokens5hBreakdown.Input + w.Tokens5hBreakdown.Output +
		w.Tokens5hBreakdown.CacheRead + w.Tokens5hBreakdown.CacheWrite5m +
		w.Tokens5hBreakdown.CacheWrite1h
	if got, want := broad, int64(1425); got != want {
		t.Errorf("breakdown sum = %d, want %d (= 100+50+1000+200+75)", got, want)
	}
}

func TestJSON_IncludesTokens5hBreakdown(t *testing.T) {
	w := Window{
		Tokens5h: 150,
		Tokens5hBreakdown: TokensBreakdown{
			Input:        100,
			Output:       50,
			CacheRead:    1000,
			CacheWrite5m: 200,
			CacheWrite1h: 75,
		},
	}
	s, err := JSON(w)
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal([]byte(s), &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	b, ok := got["tokens_5h_breakdown"].(map[string]any)
	if !ok {
		t.Fatalf("tokens_5h_breakdown missing or wrong type: %v", got["tokens_5h_breakdown"])
	}

	for _, key := range []string{"input", "output", "cache_read", "cache_write_5m", "cache_write_1h"} {
		if _, ok := b[key]; !ok {
			t.Errorf("tokens_5h_breakdown missing key %q (got keys: %v)", key, b)
		}
	}
}

func TestCompute_SevenDayUsesRecencyWeightedProjection(t *testing.T) {
	dir := t.TempDir()
	c, err := cache.Open(t.Context(), dir+"/state.db")
	if err != nil {
		t.Fatalf("Open cache: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	now := time.Date(2026, 5, 14, 12, 0, 0, 0, time.UTC)
	resetsAt := now.Add(72 * time.Hour)

	// Front-loaded shape: pct at 50% for the last 24h (slope ≈ 0).
	for _, hoursBack := range []int{24, 18, 12, 6, 0} {
		when := now.Add(-time.Duration(hoursBack) * time.Hour)
		if err := c.RecordUsageSample(t.Context(), anthro.Usage{
			SevenDay: &anthro.Bucket{Utilization: 50.0, ResetsAt: &resetsAt},
		}, when); err != nil {
			t.Fatalf("RecordUsageSample: %v", err)
		}
	}

	q := QuotaInput{
		Usage: &anthro.Usage{
			SevenDay: &anthro.Bucket{Utilization: 50.0, ResetsAt: &resetsAt},
		},
		Source:    "api",
		UpdatedAt: now,
	}

	w, err := Compute(t.Context(), c.DB(), now, q)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if w.Projection == nil || w.Projection.SevenDay == nil {
		t.Fatalf("Projection.SevenDay nil")
	}
	got := w.Projection.SevenDay
	if got.SlopePctPerHour != 0 {
		t.Errorf("front-loaded SlopePctPerHour = %v, want 0 (recency-weighted, not linear)", got.SlopePctPerHour)
	}
	if got.WillOverreach {
		t.Errorf("WillOverreach = true, want false (50 + 0*72 = 50)")
	}
	if got.ProjectedPctAtReset != 50 {
		t.Errorf("ProjectedPctAtReset = %d, want 50", got.ProjectedPctAtReset)
	}
}

func TestResolveFiveHour(t *testing.T) {
	now := time.Date(2026, 5, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(90 * time.Minute)
	t.Run("api with reset", func(t *testing.T) {
		q := QuotaInput{Usage: &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 55, ResetsAt: &reset}}}
		pct, mins := resolveFiveHour(q, "", now)
		if pct != 55 {
			t.Errorf("pct = %d, want 55", pct)
		}
		if mins == nil || *mins != 90 {
			t.Errorf("mins = %v, want 90", mins)
		}
	})
	t.Run("api idle (no reset)", func(t *testing.T) {
		q := QuotaInput{Usage: &anthro.Usage{FiveHour: &anthro.Bucket{Utilization: 12}}}
		pct, mins := resolveFiveHour(q, "", now)
		if pct != 12 {
			t.Errorf("pct = %d, want 12", pct)
		}
		if mins != nil {
			t.Errorf("mins = %v, want nil", mins)
		}
	})
	t.Run("no usage falls back to oldest", func(t *testing.T) {
		oldest := now.Add(-2 * time.Hour).Format("2006-01-02T15:04:05.000Z07:00")
		pct, mins := resolveFiveHour(QuotaInput{}, oldest, now)
		if pct != 0 {
			t.Errorf("pct = %d, want 0", pct)
		}
		if mins == nil || *mins != 180 {
			t.Errorf("mins = %v, want 180", mins)
		}
	})
}

// insertMsg seeds one assistant-turn row into the freshDB messages table.
// ts is stored as its UTC tsFormat string, matching the cache writer.
func insertMsg(t *testing.T, db *sql.DB, ts time.Time, in, out, cr, cw5, cw1 int64, cost float64) {
	t.Helper()
	if _, err := db.ExecContext(t.Context(),
		`INSERT INTO messages VALUES (?,?,?,?,?,?,?)`,
		ts.UTC().Format(tsFormat), in, out, cr, cw5, cw1, cost); err != nil {
		t.Fatalf("insertMsg: %v", err)
	}
}

// approxEqual compares two USD sums with float tolerance (0.1+0.2 != 0.3 in
// IEEE-754).
func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-9
}

// pinLocalUTC forces time.Local to UTC for the duration of the test so the
// local-calendar boundaries (DayStartLocal) are deterministic on any host.
// Not parallel-safe — callers must not t.Parallel().
func pinLocalUTC(t *testing.T) {
	t.Helper()
	orig := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = orig })
}

func TestComputePeriods_SumsPerWindow(t *testing.T) {
	pinLocalUTC(t)
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// today (also in 7d, 30d)
	insertMsg(t, db, time.Date(2026, 5, 15, 6, 0, 0, 0, time.UTC), 100, 10, 1, 2, 3, 0.1)
	// in 7d (calendar) + 30d, not today
	insertMsg(t, db, time.Date(2026, 5, 12, 10, 0, 0, 0, time.UTC), 200, 20, 4, 5, 6, 0.2)
	// in 30d only
	insertMsg(t, db, time.Date(2026, 5, 1, 10, 0, 0, 0, time.UTC), 400, 40, 7, 8, 9, 0.4)
	// older than 30d — excluded everywhere
	insertMsg(t, db, time.Date(2026, 4, 10, 10, 0, 0, 0, time.UTC), 800, 80, 99, 99, 99, 0.8)

	p, err := ComputePeriods(t.Context(), db, now, QuotaInput{})
	if err != nil {
		t.Fatalf("ComputePeriods: %v", err)
	}

	// today = msg1
	if got, want := p.Today.Tokens, int64(110); got != want {
		t.Errorf("today.Tokens = %d, want %d", got, want)
	}
	if p.Today.TokensBreakdown != (TokensBreakdown{Input: 100, Output: 10, CacheRead: 1, CacheWrite5m: 2, CacheWrite1h: 3}) {
		t.Errorf("today.TokensBreakdown = %+v", p.Today.TokensBreakdown)
	}
	if !approxEqual(p.Today.CostUSD, 0.1) {
		t.Errorf("today.CostUSD = %v, want 0.1", p.Today.CostUSD)
	}

	// 7d (calendar) = msg1 + msg2
	if got, want := p.SevenDay.Tokens, int64(330); got != want {
		t.Errorf("7d.Tokens = %d, want %d", got, want)
	}
	if p.SevenDay.TokensBreakdown != (TokensBreakdown{Input: 300, Output: 30, CacheRead: 5, CacheWrite5m: 7, CacheWrite1h: 9}) {
		t.Errorf("7d.TokensBreakdown = %+v", p.SevenDay.TokensBreakdown)
	}
	if !approxEqual(p.SevenDay.CostUSD, 0.3) {
		t.Errorf("7d.CostUSD = %v, want 0.3", p.SevenDay.CostUSD)
	}

	// 30d = msg1 + msg2 + msg3
	if got, want := p.ThirtyDay.Tokens, int64(770); got != want {
		t.Errorf("30d.Tokens = %d, want %d", got, want)
	}
	if p.ThirtyDay.TokensBreakdown != (TokensBreakdown{Input: 700, Output: 70, CacheRead: 12, CacheWrite5m: 15, CacheWrite1h: 18}) {
		t.Errorf("30d.TokensBreakdown = %+v", p.ThirtyDay.TokensBreakdown)
	}
	if !approxEqual(p.ThirtyDay.CostUSD, 0.7) {
		t.Errorf("30d.CostUSD = %v, want 0.7", p.ThirtyDay.CostUSD)
	}
}

func TestComputePeriods_EmptyDB(t *testing.T) {
	pinLocalUTC(t)
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	p, err := ComputePeriods(t.Context(), db, now, QuotaInput{})
	if err != nil {
		t.Fatalf("ComputePeriods: %v", err)
	}
	for name, per := range map[string]Period{"today": p.Today, "7d": p.SevenDay, "30d": p.ThirtyDay} {
		if per.Tokens != 0 || per.CostUSD != 0 || per.TokensBreakdown != (TokensBreakdown{}) {
			t.Errorf("%s = %+v, want zeroed", name, per)
		}
	}
}

func TestComputePeriods_SevenDayQuotaAnchor(t *testing.T) {
	pinLocalUTC(t)
	db := freshDB(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)

	// Reset 48h out (in range) → 7d window starts at now-120h = 2026-05-10 12:00 UTC.
	resetsAt := now.Add(48 * time.Hour)
	q := QuotaInput{Usage: &anthro.Usage{SevenDay: &anthro.Bucket{Utilization: 50, ResetsAt: &resetsAt}}}

	// after the quota start (2026-05-11 08:00) → counts in 7d
	insertMsg(t, db, time.Date(2026, 5, 11, 8, 0, 0, 0, time.UTC), 100, 10, 0, 0, 0, 0.1)
	// before the quota start but after the *calendar* start (2026-05-09 06:00,
	// calendar-7d start = 2026-05-09 00:00) → excluded under the quota anchor
	insertMsg(t, db, time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC), 500, 50, 0, 0, 0, 0.5)

	p, err := ComputePeriods(t.Context(), db, now, q)
	if err != nil {
		t.Fatalf("ComputePeriods: %v", err)
	}
	if got, want := p.SevenDay.Tokens, int64(110); got != want {
		t.Errorf("7d.Tokens = %d, want %d (quota anchor excludes the 05-09 row)", got, want)
	}
}

func TestComputePeriods_SevenDayCalendarFallback(t *testing.T) {
	pinLocalUTC(t)
	now := time.Date(2026, 5, 15, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour)
	farFuture := now.Add(300 * time.Hour) // > 168h → implausible

	cases := []struct {
		name string
		q    QuotaInput
	}{
		{"usage nil", QuotaInput{}},
		{"seven-day nil", QuotaInput{Usage: &anthro.Usage{}}},
		{"resets-at nil", QuotaInput{Usage: &anthro.Usage{SevenDay: &anthro.Bucket{ResetsAt: nil}}}},
		{"resets-at past", QuotaInput{Usage: &anthro.Usage{SevenDay: &anthro.Bucket{ResetsAt: &past}}}},
		{"resets-at far future", QuotaInput{Usage: &anthro.Usage{SevenDay: &anthro.Bucket{ResetsAt: &farFuture}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := freshDB(t)
			// 2026-05-09 06:00 is inside the calendar window (start 2026-05-09 00:00)
			// but would be outside a 48h-out quota window — so a hit here proves
			// the calendar fallback is active.
			insertMsg(t, db, time.Date(2026, 5, 9, 6, 0, 0, 0, time.UTC), 100, 10, 0, 0, 0, 0.1)
			p, err := ComputePeriods(t.Context(), db, now, tc.q)
			if err != nil {
				t.Fatalf("ComputePeriods: %v", err)
			}
			if got, want := p.SevenDay.Tokens, int64(110); got != want {
				t.Errorf("7d.Tokens = %d, want %d (calendar fallback should include 05-09)", got, want)
			}
		})
	}
}
