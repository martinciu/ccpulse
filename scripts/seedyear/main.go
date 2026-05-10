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
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/martinciu/ccpulse/pkg/cache"
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

// runSeed validates inputs, opens the dev cache, generates synthetic
// messages, inserts them in batches, and returns (newly inserted rows this
// run, total seed-year rows in the DB after the run, error). Idempotent
// re-runs with the same opts return inserted=0 and an unchanged total.
func runSeed(opts seedOpts) (inserted int64, total int64, err error) {
	if err := validateCacheDir(opts.cacheDir); err != nil {
		return 0, 0, err
	}
	params, ok := profiles[opts.profile]
	if !ok {
		return 0, 0, fmt.Errorf("unknown profile %q: want one of light, heavy", opts.profile)
	}
	if opts.days <= 0 {
		return 0, 0, fmt.Errorf("days must be > 0 (got %d)", opts.days)
	}

	if err := os.MkdirAll(opts.cacheDir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("create cache dir: %w", err)
	}
	dbPath := filepath.Join(opts.cacheDir, "state.db")
	c, err := cache.Open(dbPath)
	if err != nil {
		return 0, 0, fmt.Errorf("open cache %s: %w", dbPath, err)
	}
	defer c.Close()

	priceTable, err := pricing.Load()
	if err != nil {
		return 0, 0, fmt.Errorf("load pricing: %w", err)
	}

	pre, err := countSeedRows(c)
	if err != nil {
		return 0, 0, fmt.Errorf("count rows (pre): %w", err)
	}

	rng := rand.New(rand.NewPCG(uint64(opts.seed), 0xCCCCCCCCCCCCCCCC))
	msgs := generate(opts.profile, params, opts.days, rng)

	for start := 0; start < len(msgs); start += batchSize {
		end := start + batchSize
		if end > len(msgs) {
			end = len(msgs)
		}
		if err := c.InsertMessages(msgs[start:end], priceTable); err != nil {
			return 0, 0, fmt.Errorf("insert batch [%d:%d]: %w", start, end, err)
		}
	}

	post, err := countSeedRows(c)
	if err != nil {
		return 0, 0, fmt.Errorf("count rows (post): %w", err)
	}
	return post - pre, post, nil
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

	for day := 0; day < days; day++ {
		if rng.Float64() > p.activeRate {
			continue
		}
		dayStart := today.Add(-time.Duration(day) * 24 * time.Hour)
		dayLabel := dayStart.Format("20060102")

		sessionsThisDay := p.sessionsPerDayMin +
			rng.IntN(p.sessionsPerDayMax-p.sessionsPerDayMin+1)

		for s := 0; s < sessionsThisDay; s++ {
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
	// Wired in Task 6.
}
