# Changelog

All notable changes to ccpulse are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [0.4.0] — 2026-06-02

### Added
- Animated `z` zoom transitions across chart modes: right-anchored width
  squeeze in remaining mode (#375), cross-faded x-axis labels in line mode
  (#383), and a skyline morph in cost/output bar mode (#394)
- `status --json`: today / 7d / 30d token + cost rollups with per-model
  breakdowns (#386)
- `status --json`: live throughput rate (tokens/hr + $/hr) (#388)
- `status --index` flag to backfill new JSONL before reporting (#391)

### Fixed
- Dedupe usage by `message.id` so each assistant turn is counted once — fixes
  an up-to-~100× token/cost over-count on Opus 4.8 turns with interleaved
  thinking and parallel tool use. First launch after upgrade does a one-time
  cache rebuild that preserves Anthropic quota history (#374, #376)
- 7d `slope_pct_per_hour` now uses recency-weighted regression, so dip-recover
  usage series no longer report a flat-zero slope (#395, #397)
- Incremental tail defers an unterminated final line so it never drops the
  last turn (#380)
- Pricing falls forward to a model's earliest-known rate, so usage on a model
  that predates its first pricing snapshot is still costed (#368, #372)

### Internal
- Memoize the immutable chart-bucket tail to cut per-frame rebuild cost
  (#378, #390)
- Bump `modernc.org/sqlite` in the go-deps group (#384)
- Correct the `backfillBeforeStatus` count comment (#392)

## [0.3.0] — 2026-05-29

### Added
- Pricing snapshot for 2026-05-28 with Claude Opus 4.8 rates ($5 / $25 per
  Mtok in/out); Opus 4.8 usage on or after that date is now costed correctly
  (#367)

### Internal
- Wrap the previously-dropped `tx.Commit` error in `cache.InsertMessages` (#365)
- Thread `context.Context` through `pkg/cache` and enable the `noctx` linter
  (#355)
- Enable `gosec` with tuned excludes (#354)
- Enable the `bodyclose`, `misspell`, and `errorlint` linters (#353)

## [0.2.0] — 2026-05-26

### Added
- "Terminal too small" notice when the window is below 80×24 (#357)
- Exact per-bucket numbers on 24h chart bars (#310, #325)
- Chart auto-advances so the right edge tracks "now" (#317)
- 7d reset timer formatted as `5d 12h` / `18h 34m` (#316)
- Underfilling chart data is glued to the right edge (#312)
- Cleaner Y-axis labels on the token chart (#348)
- Status-bar integration cookbook for tmux + starship (#326)

### Fixed
- Quota header no longer wraps on narrow terminals (#319, #322)
- 24h chart stays flush-right while scrolling (#307)

## [0.1.0] — 2026-05-22

Initial public release.

### Added
- Live TUI: 5h + 7d quota bars and a zoomable token/cost histogram
- Anthropic usage-API integration for accurate quota numbers
- `ccpulse status --json` with burn-rate projection
- `ccpulse index` cold-walk indexer
- Distribution: Homebrew tap, .deb / .rpm packages, cross-platform
  binaries (macOS + Linux, amd64 + arm64) with shell completions
