// scripts/seedyear — synthesise a year of Claude Code messages into the dev
// ccpulse cache for manual bar-chart perf probing (issue #71).
//
// Run via: go run ./scripts/seedyear --profile light
//
// Idempotent: deterministic RNG seed + UNIQUE(session_id, ts) on the
// messages table mean re-running with the same flags inserts no new rows
// (within the same UTC day; cross-midnight runs may add a small number of
// new "today" sessions because day anchors snap to UTC midnight).
package main

import (
	"errors"
	"flag"
	"fmt"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
	"github.com/martinciu/ccpulse/pkg/config"
	"github.com/martinciu/ccpulse/pkg/parse"
	"github.com/martinciu/ccpulse/pkg/pricing"
)

// batchSize bounds the per-transaction row count when inserting via
// cache.InsertMessages. Each batch opens its own short tx; sized large
// enough to amortise tx overhead and small enough to keep memory bounded.
const batchSize = 5000

// seedSessionPrefix scopes all rows the seeder writes so they can be
// found and (manually) deleted in bulk:
//
//	DELETE FROM messages WHERE session_id LIKE 'seed-year-%';
const seedSessionPrefix = "seed-year-"

// profileParams parameterises the synthetic-data shape per profile.
// All ranges are uniform on the half-open interval [min, max).
type profileParams struct {
	activeRate        float64       // P(any sessions on a given day)
	sessionsPerDayMin int           // inclusive
	sessionsPerDayMax int           // inclusive
	sessionDurMin     time.Duration // inclusive
	sessionDurMax     time.Duration // exclusive
	intervalMin       time.Duration // inclusive
	intervalMax       time.Duration // exclusive
}

// profiles maps the --profile flag value to its parameter set.
//
// Approximate row counts at days=365, seed=1:
//   - light: ~22k rows  (70% active days, 1–3 sessions/day, 1–4h sessions, 2–5min interval)
//   - heavy: ~420k rows (100% active, 2–5 sessions/day, 3–8h sessions, 30–90s interval)
var profiles = map[string]profileParams{
	"light": {
		activeRate:        0.7,
		sessionsPerDayMin: 1,
		sessionsPerDayMax: 3,
		sessionDurMin:     1 * time.Hour,
		sessionDurMax:     4 * time.Hour,
		intervalMin:       2 * time.Minute,
		intervalMax:       5 * time.Minute,
	},
	"heavy": {
		activeRate:        1.0,
		sessionsPerDayMin: 2,
		sessionsPerDayMax: 5,
		sessionDurMin:     3 * time.Hour,
		sessionDurMax:     8 * time.Hour,
		intervalMin:       30 * time.Second,
		intervalMax:       90 * time.Second,
	},
}

// seedOpts is the parsed input to runSeed. All fields are explicit; main()
// resolves defaults from flags + pkg/config before calling runSeed so the
// helper itself does no I/O for path resolution.
type seedOpts struct {
	profile  string
	cacheDir string
	seed     int64
	days     int
}

// Window grids are anchored at the Unix epoch (UTC) so a boundary is a pure
// function of the timestamp — deterministic across runs.
const (
	usageFiveHourWindow = 5 * time.Hour
	usageSevenDayWindow = 7 * 24 * time.Hour
)

// windowFloor returns the most recent window boundary <= ts, on a grid
// anchored at the Unix epoch (UTC).
func windowFloor(ts time.Time, window time.Duration) time.Time {
	epoch := time.Unix(0, 0).UTC()
	n := ts.Sub(epoch) / window
	return epoch.Add(n * window)
}

type tsTokens struct {
	at     time.Time
	tokens int64
}

// densityIndex is a time-ascending view of per-message token volume with a
// prefix-sum, giving O(log n) range-sum queries over [lo, hi].
type densityIndex struct {
	pts    []tsTokens
	prefix []int64 // prefix[i] = sum of pts[0..i-1].tokens; len = len(pts)+1
}

func newDensityIndex(msgs []parse.Message) densityIndex {
	pts := make([]tsTokens, len(msgs))
	for i, m := range msgs {
		pts[i] = tsTokens{at: m.Timestamp, tokens: m.InputTokens + m.OutputTokens}
	}
	sort.Slice(pts, func(i, j int) bool { return pts[i].at.Before(pts[j].at) })
	prefix := make([]int64, len(pts)+1)
	for i, p := range pts {
		prefix[i+1] = prefix[i] + p.tokens
	}
	return densityIndex{pts: pts, prefix: prefix}
}

// sum returns total tokens for messages with lo <= at <= hi.
func (d densityIndex) sum(lo, hi time.Time) int64 {
	i := sort.Search(len(d.pts), func(k int) bool { return !d.pts[k].at.Before(lo) }) // first at >= lo
	j := sort.Search(len(d.pts), func(k int) bool { return d.pts[k].at.After(hi) })   // first at > hi
	if j < i {
		return 0
	}
	return d.prefix[j] - d.prefix[i]
}

// seedResult reports what a runSeed call wrote: rows newly inserted this run
// (pre/post count diff) and the post-run totals, for both tables.
type seedResult struct {
	msgsInserted, msgsTotal       int64
	samplesInserted, samplesTotal int64
}

// runSeed validates inputs, opens the dev cache, generates synthetic
// messages, inserts them in batches, and returns a seedResult. Idempotent
// re-runs with the same opts return zero inserted counts and unchanged totals.
func runSeed(opts seedOpts) (seedResult, error) {
	if err := validateCacheDir(opts.cacheDir); err != nil {
		return seedResult{}, err
	}
	params, ok := profiles[opts.profile]
	if !ok {
		return seedResult{}, fmt.Errorf("unknown profile %q: want one of light, heavy", opts.profile)
	}
	if opts.days <= 0 {
		return seedResult{}, fmt.Errorf("days must be > 0 (got %d)", opts.days)
	}

	if err := os.MkdirAll(opts.cacheDir, 0o755); err != nil {
		return seedResult{}, fmt.Errorf("create cache dir: %w", err)
	}
	dbPath := filepath.Join(opts.cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return seedResult{}, fmt.Errorf("open cache %s: %w", dbPath, err)
	}
	defer c.Close()

	hist, err := pricing.Load()
	if err != nil {
		return seedResult{}, fmt.Errorf("load pricing: %w", err)
	}

	pre, err := countSeedRows(c)
	if err != nil {
		return seedResult{}, fmt.Errorf("count rows (pre): %w", err)
	}

	rng := rand.New(rand.NewPCG(uint64(opts.seed), 0xCCCCCCCCCCCCCCCC))
	msgs := generate(opts.profile, params, opts.days, rng)

	for start := 0; start < len(msgs); start += batchSize {
		end := min(start+batchSize, len(msgs))
		if err := c.InsertMessages(msgs[start:end], hist); err != nil {
			return seedResult{}, fmt.Errorf("insert batch [%d:%d]: %w", start, end, err)
		}
	}

	post, err := countSeedRows(c)
	if err != nil {
		return seedResult{}, fmt.Errorf("count rows (post): %w", err)
	}
	return seedResult{msgsInserted: post - pre, msgsTotal: post}, nil
}

// countSeedRows returns the total rows in messages whose session_id
// starts with seedSessionPrefix. Used to compute "rows inserted this
// run" by diffing pre vs post counts (cleaner than tracking the
// SQLite-side rowsAffected, which counts INSERT OR IGNORE skips
// inconsistently across drivers).
func countSeedRows(c *cache.Cache) (int64, error) {
	var n int64
	err := c.DB().QueryRow(
		`SELECT COUNT(*) FROM messages WHERE session_id LIKE ?`,
		seedSessionPrefix+"%",
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("query seed row count: %w", err)
	}
	return n, nil
}

// expandPath replaces a leading "~" with $HOME. Mirrors the unexported
// expand() in cmd/ccpulse/index.go — kept local to keep the seeder a
// self-contained script that doesn't depend on cmd/ internals.
func expandPath(p string) string {
	if p == "" || p[0] != '~' {
		return p
	}
	home, _ := os.UserHomeDir()
	return home + p[1:]
}

// validateCacheDir is the belt-and-braces guard that prevents the seeder
// from writing to a release cache. The basename of cacheDir must end in
// "-dev" — otherwise we reject before opening anything. Channel routing
// in pkg/config already produces "-dev" paths under `go run`, so this
// guard is a defensive double-check, not the primary gate.
func validateCacheDir(cacheDir string) error {
	if cacheDir == "" {
		return errors.New("cache dir is empty")
	}
	base := filepath.Base(cacheDir)
	if !strings.HasSuffix(base, "-dev") {
		return fmt.Errorf("refusing to seed: cache dir basename %q does not end in '-dev' (got path %q)", base, cacheDir)
	}
	return nil
}

// generate produces synthetic parse.Message rows shaped by params over
// the last `days` days. Day anchors snap to UTC midnight so that two
// runs on the same UTC day with the same seed produce identical rows
// (idempotent under INSERT OR IGNORE on UNIQUE(session_id, ts)). Runs
// across UTC midnight may add a small number of "today" sessions.
func generate(profileName string, p profileParams, days int, rng *rand.Rand) []parse.Message {
	models := []string{
		"claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-haiku-4-5-20251001",
	}
	slugs := []string{
		"-Users-seed-projectA",
		"-Users-seed-projectB",
		"-Users-seed-projectC",
		"-Users-seed-projectD",
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)

	// Pre-size: rough upper bound to reduce reallocation churn.
	// (~5 sessions/day × 8h × 120 rows/h ≈ 4800 rows/day at heavy.)
	out := make([]parse.Message, 0, days*5000)

	for day := range days {
		if rng.Float64() > p.activeRate {
			continue
		}
		dayStart := today.Add(-time.Duration(day) * 24 * time.Hour)
		dayLabel := dayStart.Format("20060102")

		sessionsThisDay := p.sessionsPerDayMin +
			rng.IntN(p.sessionsPerDayMax-p.sessionsPerDayMin+1)

		for s := range sessionsThisDay {
			// Session ID encodes the date directly so cross-day re-runs
			// don't accumulate stacked rows under the same id.
			sessionID := fmt.Sprintf("%s%s-%s-%d", seedSessionPrefix, profileName, dayLabel, s)

			// Start somewhere in the first 18h of the day; long sessions
			// may bleed past midnight (realistic, harmless to the cache).
			startOffsetSec := rng.IntN(int(18 * time.Hour / time.Second))
			sessionStart := dayStart.Add(time.Duration(startOffsetSec) * time.Second)

			durRange := int64(p.sessionDurMax - p.sessionDurMin)
			duration := p.sessionDurMin + time.Duration(rng.Int64N(durRange))
			sessionEnd := sessionStart.Add(duration)

			model := models[s%len(models)]
			slug := slugs[s%len(slugs)]

			ts := sessionStart
			intervalRange := int64(p.intervalMax - p.intervalMin)
			for ts.Before(sessionEnd) {
				out = append(out, parse.Message{
					SessionID:          sessionID,
					ProjectSlug:        slug,
					Timestamp:          ts,
					Role:               "assistant",
					Model:              model,
					InputTokens:        int64(100 + rng.IntN(1900)),
					OutputTokens:       int64(200 + rng.IntN(7800)),
					CacheReadTokens:    int64(1000 + rng.IntN(49000)),
					CacheWrite5mTokens: int64(rng.IntN(5000)),
					CacheWrite1hTokens: 0,
					IsSubagent:         false,
					ParentSessionID:    "",
				})
				interval := p.intervalMin + time.Duration(rng.Int64N(intervalRange))
				ts = ts.Add(interval)
			}
		}
	}
	return out
}

func main() {
	profile := flag.String("profile", "light", "synthetic data profile: light or heavy")
	cacheDir := flag.String("cache-dir", "", "override the dev cache dir (must end in '-dev'); default: resolve from pkg/config")
	seed := flag.Int64("seed", 1, "RNG seed; same value reproduces the same rows for idempotent re-runs")
	days := flag.Int("days", 365, "window length back from today, in days")
	flag.Parse()

	resolved := *cacheDir
	if resolved == "" {
		cfg, err := config.Load("")
		if err != nil {
			// pkg/config returns a usable default even when the file is
			// missing; only a parse error is fatal.
			log.Fatalf("seedyear: load config: %v", err)
		}
		resolved = expandPath(cfg.Paths.CacheDir)
	} else {
		resolved = expandPath(resolved)
	}

	res, err := runSeed(seedOpts{
		profile:  *profile,
		cacheDir: resolved,
		seed:     *seed,
		days:     *days,
	})
	if err != nil {
		log.Fatalf("seedyear: %v", err)
	}
	fmt.Printf("seeded %s/state.db: %d new messages (%d total), %d new usage samples (%d total)\n",
		resolved, res.msgsInserted, res.msgsTotal, res.samplesInserted, res.samplesTotal)
}
