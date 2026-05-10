# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
mise install          # fetch Go 1.25 (first-time setup)

make build            # go build -o ccpulse ./cmd/ccpulse
make install          # build → ~/.local/bin/ccpulse
make test             # go test ./...
make lint             # go vet ./...

go test ./pkg/cache/... -run TestIntegrity   # single package / single test
go test ./...                                # all packages
```

Release artifacts are produced by GoReleaser (`goreleaser release --clean`). The `version`, `commit`, and `date` vars in `cmd/ccpulse/main.go` are injected via ldflags at release time.

### Environment overrides

These env vars override `config.toml` at runtime — useful for testing against a fixture without touching the real config:

| Variable | Default (from config) | Purpose |
|---|---|---|
| `CCPULSE_PROJECTS_ROOT` | `~/.claude/projects` | Which JSONL tree to watch/index |
| `CCPULSE_CACHE_DIR` | `~/.cache/ccpulse` | Where `state.db` and `parse-errors.log` live |

## Architecture

### Data flow

1. `cmd/ccpulse/main.go` (`runTUI`) wires all packages together:
   - Opens `cache.Cache` (SQLite at `~/.cache/ccpulse/state.db`); runs `integrity_check` and auto-rebuilds on corruption.
   - Starts `watcher.Watcher` goroutine watching `~/.claude/projects/` for `.jsonl` WRITE/CREATE events (debounced 100 ms).
   - On each event: `parse.ParseFromOffsetWithErrors` tails the file from the last byte offset → `cache.InsertMessages` → `canonical.Resolver.Resolve` back-fills `project_canonical` → sends `tui.RefreshMsg` to the Bubble Tea program.
   - `ccpulse index` does a full cold walk via `parse.Walk`.

2. The TUI (`pkg/tui`) is a pure Bubble Tea model. It renders a single view: a bordered header with side-by-side 5h and 7d quota bars (`bubbles/progress`), followed by a horizontally-scrollable bar chart (`ntcharts/barchart`) of token usage per time bucket, heat-coloured relative to the peak bucket. On `RefreshMsg` it calls `status.Compute` (for the quota window) and `cache.TokenBuckets` (for the histogram at the current zoom level) and redraws. The `z` key cycles zoom (5m / 15m / 1h); `←`/`→` scroll the chart; `?` toggles a full-help overlay. The TUI never writes to the cache.

### Package map

| Package | Responsibility |
|---|---|
| `cmd/ccpulse` | Cobra CLI wiring: `runTUI`, `status`, `index`, `config`, `doctor`, `version` |
| `pkg/parse` | JSONL transcript → `[]Message`; `ParseFromOffsetWithErrors` for incremental tail |
| `pkg/cache` | SQLite via `modernc.org/sqlite`; schema embedded in `schema.sql`; tracks file cursors (`files` table), slug→canonical mapping (`slug_canonical` table), and time-bucketed token aggregates (`TokenBuckets`) |
| `pkg/watcher` | fsnotify wrapper with 100 ms debounce; auto-subscribes new subdirectories |
| `pkg/canonical` | Slug decode (`-` → `/`, `--` → `/.`); git-based canonical project root resolution |
| `pkg/pricing` | Embeds `pricing.json`; `Table.CostFor(Message)` returns USD cost |
| `pkg/status` | 5-hour rolling window + 7-day window computation; tier → token ceiling mapping; consumed by TUI header and `status --json` |
| `pkg/anthro` | Anthropic credential loading (`LoadCredential`), tier slug/pretty mapping (`TierSlug`, `TierPretty`), and the usage-API client / cache used by `runTUI` |
| `pkg/ingest` | Cold-walk indexer used by `runTUI` startup backfill and `ccpulse index`; reports progress via `IndexProgressMsg` |
| `pkg/tui` | Bubble Tea model: bordered header (5h+7d quota bars), horizontally-scrollable token histogram, full-help overlay, lipgloss styling, `bubbles/{help,key,progress,viewport}` + `ntcharts/barchart` |
| `pkg/config` | TOML config at `~/.config/ccpulse/config.toml` (respects `XDG_CONFIG_HOME`); `config.Load("")` returns safe defaults |

### SQLite schema

Five tables: `messages` (one row per assistant turn), `files` (last byte offset and line number per JSONL file for incremental parsing), `slug_canonical` (slug → canonical path cache), `meta` (schema version), `usage_samples` (one row per successful Anthropic usage-API fetch, JSON payload of `anthro.Usage`).

### Slug encoding

Claude encodes project paths as directory slugs: `/` → `-`, `.` → `--`. `canonical.DecodeSlug` reverses this by testing filesystem candidates longest-first.

### Plan tiers and the 5-hour window

`status.CeilingFor` maps tier names (`max_5x`, `max_20x`, `pro`, `api`, `custom`) to token budgets. The rolling window is computed over the 5-hour period that precedes the current time, matching Claude Max's rate-limit window. The quota bar uses the bubbles/progress default gradient at every fill level — there are no threshold-based color flips.
