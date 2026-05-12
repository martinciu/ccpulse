# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Commands

```sh
mise install          # fetch Go 1.25 (first-time setup)

make build            # go build -o ccpulse ./cmd/ccpulse
make install          # build → ~/.local/bin/ccpulse (release channel)
make test             # go test ./...
make lint             # go vet ./...

make seed-dev         # populate fixture config + cache for TUI probes
make reset-dev        # blow away seeded dev state

go test ./pkg/cache/... -run TestIntegrity   # single package / single test
go test ./...                                # all packages
```

Release artifacts are produced by GoReleaser (`goreleaser release --clean`). The `version`, `commit`, and `date` vars in `cmd/ccpulse/main.go` are injected via ldflags at release time.

`make install` and GoReleaser both inject `-X main.buildChannel=release`, which `pkg/channel` reads at startup. Anything else (including `make build`) is a dev build: `pkg/devlog` writes DEBUG-level slog output to `~/.cache/ccpulse/debug.log`.

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
   - On each event: `parse.ParseFromOffsetWithErrors` tails the file from the last byte offset → `cache.InsertMessages` (writes `cwd` and `git_branch` captured per-message from the JSONL envelope) → sends `tui.RefreshMsg` to the Bubble Tea program.
   - `ccpulse index` does a full cold walk via `parse.Walk`.

2. The TUI (`pkg/tui`) is a pure Bubble Tea model. It renders a single view: a bordered header with side-by-side 5h and 7d quota bars (`bubbles/progress`), followed by a horizontally-scrollable bar chart (`ntcharts/barchart`) of token usage per time bucket, heat-coloured relative to the peak bucket. On `RefreshMsg` it calls `status.Compute` (for the quota window) and `cache.TokenBuckets` (for the histogram at the current zoom level) and redraws. The `z` key cycles zoom (5m / 15m / 1h); `←`/`→` scroll the chart; `?` toggles a full-help overlay. The TUI never writes to the cache.

### Package map

| Package | Responsibility |
|---|---|
| `cmd/ccpulse` | Cobra CLI wiring: `runTUI`, `status`, `index`, `config`, `doctor`, `version` |
| `pkg/parse` | JSONL transcript → `[]Message`; `ParseFromOffsetWithErrors` for incremental tail |
| `pkg/cache` | SQLite via `modernc.org/sqlite`; schema embedded in `schema.sql`; tracks file cursors (`files` table), per-message rows in `messages` (including `cwd` / `git_branch` from JSONL), and time-bucketed token aggregates (`TokenBuckets`) |
| `pkg/watcher` | fsnotify wrapper with 100 ms debounce; auto-subscribes new subdirectories |
| `pkg/pricing` | Embeds `pricing.json`; `Table.CostFor(Message)` returns USD cost |
| `pkg/status` | 5-hour rolling window + 7-day window computation; tier → token ceiling mapping; consumed by TUI header and `status --json` |
| `pkg/anthro` | Anthropic credential loading (`LoadCredential`), tier slug/pretty mapping (`TierSlug`, `TierPretty`), and the usage-API client / cache used by `runTUI` |
| `pkg/ingest` | Cold-walk indexer used by `runTUI` startup backfill and `ccpulse index`; reports progress via `IndexProgressMsg` |
| `pkg/tui` | Bubble Tea model: bordered header (5h+7d quota bars), horizontally-scrollable token histogram, full-help overlay, lipgloss styling, `bubbles/{help,key,progress,viewport}` + `ntcharts/barchart` |
| `pkg/config` | TOML config at `~/.config/ccpulse/config.toml` (respects `XDG_CONFIG_HOME`); `config.Load("")` returns safe defaults |
| `pkg/channel` | Build-channel flag (`dev` / `release`) set once at startup from `main.buildChannel` via ldflag; unknown values normalise to `dev` |
| `pkg/devlog` | Wires `slog.Default()` to `<cacheDir>/debug.log` in dev, `io.Discard` in release; uses `charmbracelet/log` for `YYYY-MM-DD HH:MM:SS.mmm DEBU <file:line>` formatted output with caller info; best-effort (falls back to discard on any setup error) |
| `pkg/secfile` | Filesystem helpers enforcing 0700 dirs / 0600 files; chmods pre-existing entries tighter on access |

### SQLite schema

Four tables: `messages` (one row per assistant turn, including `cwd` and `git_branch` captured from each JSONL line's envelope), `files` (last byte offset and line number per JSONL file for incremental parsing), `meta` (schema version), `usage_samples` (one row per successful Anthropic usage-API fetch, JSON payload of `anthro.Usage`).

### Project metadata

ccpulse does not derive a canonical "project" identifier. Each row stores `project_slug` (the directory name under `~/.claude/projects/`, Claude-encoded with `/` → `-` and `.` → `--`), plus `cwd` and `git_branch` taken directly from the JSONL envelope. Worktrees of the same repo therefore appear as distinct `project_slug` / `cwd` entries — each correctly labelled with its branch. There is no runtime `git` dependency.

### Plan tiers and the 5-hour window

Tier strings (`max_5x`, `max_20x`, `pro`, `api`, `custom`) flow through `Window.CeilingLabel` / `Window.CeilingPretty` for display; ccpulse does not maintain a token-budget table per tier. The rolling 5-hour window matches Claude Max's rate-limit window. The quota bar uses the project's green→red gradient (Solarized `#859900` → `#dc322f` via `progress.WithGradient`), so cool cells indicate headroom and warm cells indicate approaching the limit.

## TUI rendering conventions

The TUI is built on four upstream libraries — no custom equivalents should be reintroduced:

- `charmbracelet/bubbletea` — model/update/view loop
- `charmbracelet/lipgloss` — styling, layout, box composition
- `charmbracelet/bubbles` — pre-built components (`progress`, `viewport`, `help`, `key`)
- `NimbleMarkets/ntcharts` — bar/line/sparkline charts

### Prefer library primitives over manual string math

Reach for the library function first; manual width/padding arithmetic is the smell.

| Need | Use | Don't use |
|---|---|---|
| Center text in a `w×h` block | `lipgloss.Place` | `strings.Repeat(" ", ...)` + `strings.Join` |
| Right-align inside a known width | `lipgloss.PlaceHorizontal` | manual `m.w - W(left) - W(right)` padding |
| Compose styled blocks side-by-side | `lipgloss.JoinHorizontal` | string concat with a padded spacer |
| Stack styled blocks vertically | `lipgloss.JoinVertical` | `strings.Join(parts, "\n")` |
| Fixed-width slot or padding | `lipgloss.NewStyle().Width(n).Render(...)` | manual rune counting |
| Progress bars | `bubbles/progress` | hand-rolled fill characters |
| Scrollable content | `bubbles/viewport` | manual `XOffset`/line-slice math |
| Keybinding help footer/overlay | `bubbles/help` + `bubbles/key` | hand-formatted help strings |
| Bar charts | `ntcharts/barchart` | per-row block-character drawing |

`lipgloss.Width()` — not `len()` or `utf8.RuneCountInString` — is the authority for visual width on styled strings; it accounts for ANSI sequences and wide runes.

Hand-rolled rendering is fine when no primitive fits — e.g. a per-cell row built from a loop over data where no library has a 1:1 mapping. The rule is "primitive exists → use it", not "never write a `for` loop that builds a string".

### Animation — consider `harmonica` when motion conveys information

`charmbracelet/harmonica` (already a transitive dep via `bubbles/progress`) provides spring physics for smooth, natural motion. When adding or changing a TUI component, ask whether motion would convey *information* — not just polish.

Harmonica fits where:

- ✅ The transition has a discrete, user-initiated trigger (e.g. a single keypress).
- ✅ The data shape is the same on both sides of the transition (same bucket count, same axis, 1:1 mapping between old and new values).
- ✅ Motion makes a delta visible that would otherwise require the user to remember the previous state.

Harmonica does *not* fit where:

- ❌ Input is continuous (key-repeat scroll, watcher-driven refresh). Spring latency fights with rapid events; convention everywhere from `less` to `k9s` is hard snap.
- ❌ The transition changes the layout shape (bucket count, axis range, panel swap). That's a cut or fade, not a spring.
- ❌ The motion is purely decorative — terminal users notice polish but resent latency more.

When wiring harmonica directly: drive it from a `tea.Tick` command returned by `Update`, and stop ticking the moment all springs are settled — idle TUI must remain zero-animation-cost. The chart can be thousands of buckets wide and `ntcharts/barchart` redraws fully each frame, so benchmark the per-frame rebuild cost before committing (`BenchmarkBarChartRender` at chartW = 100/1000/5000, per the `golang-benchmark` skill).

### v1 line — don't introduce v2 imports

All four libraries have shipped stable v2 majors (`charm.land/bubbletea/v2`, `charmbracelet/{bubbles,lipgloss}/v2`, `NimbleMarkets/ntcharts/v2`). ccpulse stays on the v1 line until a deliberate migration. Don't add v2 imports as part of unrelated work — the v2 cutover is a planned, separate effort because it touches every `pkg/tui/*.go` file (`View() string` → `View() tea.View`, key-message types split, import path changes).
