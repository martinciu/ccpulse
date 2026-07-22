# Changelog

All notable changes to ccpulse are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [0.8.0] — 2026-07-22

### Added
- Scoped per-model weekly limits in the quota header: for accounts whose usage
  API reports `weekly_scoped` ceilings, the header renders one gradient bar per
  model below the 5h and 7d bars, each with its own reset countdown; the cache
  parses and persists the usage-API `limits` array into a new `usage_limits`
  table. `status --json` gains an additive `scoped_limits` array. Accounts
  without scoped limits render the same header as before (#458, #463, #467)
- The parser now persists two more fields of the Claude Code JSONL envelope per
  turn — the top-level `effort` field and, when informative, the verbatim
  `message.usage.iterations` blob (e.g. multi-model fallback attempts). Storage
  only; groundwork for multi-model cost attribution (#464)

### Changed
- Cache schema bumped v8 → v11 across this release (the new `usage_limits` table
  and its per-model dedupe key, plus the stored `effort` / `iterations_json`
  columns). Existing caches rebuild automatically on first launch, preserving
  Anthropic quota history (#458, #462, #464)

### Fixed
- The quota poller now logs `RecordUsageSample` / `PruneUsageSamples` errors
  instead of silently discarding them — a dropped usage sample or a persistently
  failing prune (unbounded table growth) is now visible in the log rather than
  surfacing only as an unexplained gap in usage history (#461)

### Internal
- pricing-drift CI now compares the currently-effective pricing snapshot rather
  than the lexically-largest one, so a future-dated snapshot no longer trips the
  drift check (396a94c)
- Bump `actions/setup-go` 6.5.0 → 7.0.0 (#454), `anthropics/claude-code-action`
  (#451, #453), and `modernc.org/sqlite` (#452)

## [0.7.0] — 2026-07-08

### Changed
- The quota poller now honors `Retry-After` and backs off on sustained 429s:
  consecutive rate-limits stretch the poll interval 6 → 12 → 24 → 30-min cap
  (a server `Retry-After` is honored past the cap, clamped to 1 h), and any
  non-429 outcome resets to the 3-minute base. One-shot callers
  (`ccpulse status`, first paint) are unchanged and never sleep (#447, #448)

### Internal
- Bump the gha-deps group with 3 updates (#446)

## [0.6.0] — 2026-07-02

### Added
- Animated projects-box toggle: `p` now slides the box open and closed with a
  spring, the chart re-flowing to the freed or reclaimed rows every frame. A
  second `p` mid-slide reverses from the current height; zoom/unit switches
  and data refreshes cut straight to the steady view. All frames render
  within the 60 fps budget, in bar and line modes alike (#416, #436)
- Pricing snapshots for 2026-07-01 (intro) and 2026-09-01 (standard) with
  Claude Sonnet 5 rates, so usage on the new model is costed correctly
  (#443, #444)

### Internal
- Shared bootstrap for `cmd/ccpulse` subcommands: the five divergent "cache
  locked" messages collapse to one canonical hint, and `status` now creates
  the cache directory if missing (#421, #438)
- Bump `modernc.org/sqlite` to 1.53.0 (#439) and the GitHub Actions
  dependency groups (#437, #440, #441, #442)

## [0.5.0] — 2026-06-10

### Added
- Per-project cost & token breakdown in the TUI: a table below the chart,
  aggregated over the chart's currently-visible time window, with each
  repo's worktrees and subdirectories rolled up to the parent repo. Hidden
  by default — press `p` to toggle. Compact token counts, right-aligned
  columns; the box sizes to its content and the chart reclaims the spare
  rows (#408, #409, #410, #411, #413, #414, #415, #420, #429)
- Pricing snapshot for 2026-06-09 with Claude Fable 5 and Mythos 5 rates
  ($10 / $50 per Mtok in/out), so usage on the new models is costed
  correctly (#418, #419)

### Changed
- Cache schema bumped to v8 (new `repo_root` column). Existing caches rebuild
  automatically on first launch to backfill it (#408)

### Fixed
- Projects box no longer shows "no activity in this window" (or data for the
  wrong range) on the usage-line view after zooming or scrolling — its query
  window now follows the chart's visible time range (#430, #431)
- Ingest writes message rows and the file cursor in one transaction, closing
  a crash window that left rows persisted without the cursor advancing
  (forcing a redundant re-parse); a real `GetFile` DB error now logs and
  skips the file instead of silently re-parsing from offset 0 (#401, #405)

### Internal
- Regenerate the demo GIF — smaller file, same tape (#399)
- Bump `modernc.org/sqlite` in the go-deps group (#406)
- Bump the gha-deps group with 2 updates (#407)

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
