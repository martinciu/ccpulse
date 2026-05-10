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
	"path/filepath"
	"strings"
	"time"
)

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
	if _, ok := profiles[opts.profile]; !ok {
		return 0, 0, fmt.Errorf("unknown profile %q: want one of light, heavy", opts.profile)
	}
	return 0, 0, errors.New("not implemented")
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

func main() {
	// Wired in Task 6.
}
