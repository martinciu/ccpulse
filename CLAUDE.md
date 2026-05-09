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

2. The TUI (`pkg/tui`) is a pure Bubble Tea model. On `RefreshMsg` it re-queries the cache for all five tabs (Live, Today, History, Projects, Models) and redraws. It never writes to the cache itself.

### Package map

| Package | Responsibility |
|---|---|
| `cmd/ccpulse` | Cobra CLI wiring: `runTUI`, `status`, `index`, `config`, `doctor`, `version` |
| `pkg/parse` | JSONL transcript → `[]Message`; `ParseFromOffsetWithErrors` for incremental tail |
| `pkg/cache` | SQLite via `modernc.org/sqlite`; schema embedded in `schema.sql`; tracks file cursors (`files` table) and slug→canonical mapping (`slug_canonical` table) |
| `pkg/watcher` | fsnotify wrapper with 100 ms debounce; auto-subscribes new subdirectories |
| `pkg/canonical` | Slug decode (`-` → `/`, `--` → `/.`); git-based canonical project root resolution |
| `pkg/pricing` | Embeds `pricing.json`; `Table.CostFor(Message)` returns USD cost; override via config |
| `pkg/status` | 5-hour rolling window computation; tier → token ceiling mapping; `TmuxLine` for status-right |
| `pkg/tui` | Bubble Tea model, 5 tabs, tmux-aware Live scope filtering, lipgloss styling |
| `pkg/tmux` | Thin wrapper around `tmux` CLI for current-session pane paths |
| `pkg/config` | TOML config at `~/.config/ccpulse/config.toml` (respects `XDG_CONFIG_HOME`); `config.Load("")` returns safe defaults |
| `pkg/state` | JSON sidecar at `~/.local/state/ccpulse/state.json` (respects `XDG_STATE_HOME`) persisting tab and live-scope across restarts |

### SQLite schema

Five tables: `messages` (one row per assistant turn), `files` (last byte offset and line number per JSONL file for incremental parsing), `slug_canonical` (slug → canonical path cache), `meta` (schema version), `usage_samples` (one row per successful Anthropic usage-API fetch, JSON payload of `anthro.Usage`).

### Slug encoding

Claude encodes project paths as directory slugs: `/` → `-`, `.` → `--`. `canonical.DecodeSlug` reverses this by testing filesystem candidates longest-first.

### Plan tiers and the 5-hour window

`status.CeilingFor` maps tier names (`max_5x`, `max_20x`, `pro`, `api`, `custom`) to token budgets. The rolling window is computed over the 5-hour period that precedes the current time, matching Claude Max's rate-limit window. Color buckets: violet (<70%), yellow (70–89%), red (≥90%).
