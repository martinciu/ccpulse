# Changelog

All notable changes to ccpulse are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

## [0.11.0] — 2026-09-02

### Added
- Pricing snapshot `2026-09-02` with Claude Fable 5.1 and Claude Mythos 5.1
  at $10 / $50 / $0.25 / $12.50 / $20 per MTok. Cache hits and refreshes on
  these two models are 0.025× base input (an explicit footnote on the
  pricing page), four times cheaper than Fable 5; the other four rates match.
  The bare `fable` / `mythos` aliases stay era-frozen at Fable 5 rates, the
  same way `opus` was never re-pointed past Opus 4.1 (#511, #513)

### Changed
- Sonnet 5's $2 / $10 per MTok introductory rates are now the standard price:
  the `2026-09-01` snapshot no longer carries the cancelled $3 / $15
  increase, so Sonnet 5 messages dated September 1 or later are priced
  correctly instead of ~50% too high. Sonnet 4.x and the bare `sonnet` alias
  are unchanged (#496, #498)
- The startup recost fingerprint now covers snapshot *contents*, not just
  the set of snapshot versions: an in-place edit to an existing pricing
  snapshot (an added model, a corrected rate) triggers exactly one
  `AutoRecost` on the next launch instead of waiting for a manual
  `ccpulse recost`. Because the stored fingerprint format changed, the first
  launch after upgrading runs that one-time recost for every cache — bounded
  by the existing 5 s timeout, and only rows whose cost, version or
  unknown flag actually differ are rewritten (#512, #517)
- Pricing snapshots are validated when loaded: a missing or empty `models`
  object, an empty model name, or any rate that is missing, zero, negative
  or non-finite is rejected up front instead of silently pricing that
  dimension at $0 with `pricing_unknown = false`. The carry-forward test now
  compares every rate between consecutive snapshots and requires intentional
  changes to be declared explicitly (#514, #518)

### Fixed
- A quota poll landing mid-animation no longer desyncs the viewport from the
  chart: spring, unit-toggle and zoom frames re-sync the viewport height on
  every frame, so the x-labels no longer float one row up and the header's
  top border is no longer pushed off-screen until the animation settles
  (#499, #500)

### Internal
- Go toolchain 1.25.12 → 1.25.13, clearing four reachable standard-library
  vulnerabilities flagged by govulncheck (#508, #509)
- The pricing-drift checker treats every bare alias (any `models` key
  without a `claude-` prefix) as informational, so the era-frozen `fable` /
  `mythos` aliases cannot raise a false drift report (#515, #516)
- Tests: two breakdown tests now assert the layout constants they silently
  depended on (#482, #502); the full-lifecycle `runTUI` tests bound their
  wait so a dropped `q` fails one test instead of the whole package (#492,
  #503)
- Bump `modernc.org/sqlite` 1.55.0 → 1.57.0, `charmbracelet/x/ansi`
  0.11.7 → 0.11.8, `lucasb-eyer/go-colorful` 1.4.0 → 1.4.1 (#493, #504,
  #506) and `anthropics/claude-code-action` 1.0.183 → 1.0.210 (#494, #505,
  #507, #510)

## [0.10.0] — 2026-08-10

### Added
- Models breakdown panel: `m` slides in a per-model breakdown — cost, tokens
  and share over the chart's visible window — in the same box as the projects
  panel, and `p`/`m` swap panels with a sequential down-then-up slide. Model
  ids render as derived display names (`claude-opus-4-7` → `Opus 4.7`), dated
  and undated variants folding into one row; ids that aren't unambiguously
  modern Claude ids show verbatim. Zero-contribution rows (e.g. `<synthetic>`)
  are hidden, and unpriced models are kept at $0 so the panel's token total
  reconciles with the projects panel (#475, #476, #484, #485)
- Zoom level and active view persist across restarts: `z`/`u` immediately
  write `<cacheDir>/ui-state.toml` (0600, written atomically), and the next
  launch opens where you left off. A missing or corrupt file falls back
  silently to the defaults (15m / cost) (#490, #491)

### Changed
- Billed `usage.iterations` entries served by a different model than the
  turn's — refused fallback attempts, cross-model advisor calls — now expand
  at parse time into per-model attempt rows (`message_id` keyed
  `<id>:it:<idx>`), so the models panel, projects panel, chart buckets and
  recost all attribute those tokens and their cost to the model that actually
  consumed them. Cache schema bumped v11 → v12 (no schema-text change): the
  first launch after upgrading wipes and re-indexes the message cache from
  JSONL to backfill attempt rows, preserving Anthropic quota history;
  per-model totals shift accordingly (#456, #487)
- The breakdown slide settles the moment its integer height arrives and skips
  re-renders while the painted state is unchanged: a projects↔models swap
  drops from 134 to ~74 ticks, eliminating ~660 ms of CPU and ~640 MB of
  allocation churn per swap, and the dead pause at height 0 between swap legs
  is gone. Mid-slide ←/→ scrolls and quota-driven header growth still repaint
  immediately (#477, #489)

### Fixed
- A bare dated model id (e.g. `-20250101`) no longer folds to the empty
  string — the cache's reserved unknown-model sentinel — so it renders as its
  own row in the models panel instead of being reclassified as
  "(unknown model)" and force-sorted last (#479, #488)

### Internal
- Bump `modernc.org/sqlite` 1.54.0 → 1.55.0 (#486) and the gha-deps group:
  `actions/checkout` 7.0.0 → 7.0.1, `anthropics/claude-code-action`
  1.0.178 → 1.0.183 (#474)

## [0.9.0] — 2026-07-25

### Added
- Pricing snapshot for 2026-07-24 with Claude Opus 5 rates ($5 / $25 per Mtok
  in/out, $0.50 cache-read, $6.25 / $10 cache-write 5m / 1h). The same rates are
  propagated into the future-dated 2026-09-01 snapshot, so Opus 5 stays costed
  past the Sonnet 5 introductory window. Opus 5 usage previously priced at $0
  and was stamped `pricing_unknown`; because the new snapshot changes the recost
  fingerprint, cached messages are re-stamped once on next launch and historical
  totals rise by the accumulated Opus 5 spend — no other model's cost moves
  (#470, #471)

### Internal
- Pricing snapshots are now asserted by full `ModelRate` equality
  (`TestOpus5Snapshots`, plus `2026-07-24` added to `TestSonnet5Snapshots`)
  rather than resolution alone, which had left four of five rates per model
  unpinned. The pricing-drift checker's generated fix instruction also covers
  corrected rates as well as new models, names the resolved effective snapshot,
  and warns that inserting a snapshot shifts date-pinned test expectations
  (#471)

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
