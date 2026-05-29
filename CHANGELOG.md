# Changelog

All notable changes to ccpulse are documented here.
The format is based on [Keep a Changelog](https://keepachangelog.com/).

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
