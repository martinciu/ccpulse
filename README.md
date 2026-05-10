# ccpulse

A native Go TUI dashboard for Claude Code usage. Reads
`~/.claude/projects/*/*.jsonl` transcripts, computes token / cost /
5-hour-window breakdowns. Local-only — no network calls during normal
operation.

![ccpulse — Live tab](docs/screenshots/live.png)

## Features

- **Live tab** — active sessions across all your projects, with `⚡`
  recency markers.
- **Today / History / Projects / Models tabs** — drill-downs by date,
  project, and model.
- **5-hour plan-window gauge** — rolling window over the Claude Max
  rate-limit period, color-bucketed at the 70% and 90% thresholds (yellow / red).
- **Worktree-aware project grouping** — collapses session slugs back
  to the canonical project via git.
- **fsnotify live updates** — file watcher (not polling) keeps the TUI
  in sync as Claude writes new turns.

## Quickstart

Requires `mise` and `git`.

```sh
git clone https://github.com/martinciu/ccpulse ~/code/ccpulse
cd ~/code/ccpulse
mise install        # fetches Go 1.25 into the project-scoped toolchain
make install        # builds → ~/.local/bin/ccpulse
```

The binary lives in `~/.local/bin/ccpulse`. On first run, populate the
cache from your existing transcripts before opening the TUI:

```sh
ccpulse index        # one-time scan of all existing JSONL history
ccpulse              # opens the TUI
```

After that, the watcher keeps the cache up to date automatically.

## Commands

### `ccpulse`

Opens the interactive TUI. Five tabs, navigated with `tab` / `shift+tab`
or number keys `1`–`5`. Press `?` for the full keybinding list.

### `ccpulse index`

Scans all `.jsonl` files under `projects_root` and populates the SQLite
cache. Run once after install to load existing history. Safe to re-run
— already-indexed turns are skipped.

```sh
ccpulse index             # incremental scan (adds new data)
ccpulse index --rebuild   # drop the cache first, then do a full scan
```

Use `--rebuild` if the cache gets out of sync or you want a clean slate.

### `ccpulse status`

Prints the current 5-hour rolling window without opening the TUI.

```sh
ccpulse status            # human-readable summary
ccpulse status --json     # JSON: percent, tokens_5h, cost_5h_usd, minutes_to_reset
```

`--json` is useful for scripting or status bars that consume structured data.

### `ccpulse config`

```sh
ccpulse config edit   # create config if missing, then open in $EDITOR
ccpulse config show   # print the live config (defaults + your overrides)
ccpulse config path   # print the path to config.toml
```

`edit` never overwrites an existing file — safe to run at any time to
check where the file lives before editing it manually.

### `ccpulse doctor`

Runs a health-check checklist and prints a pass/fail report:

- Config file loads and `projects_root` is readable
- SQLite cache opens and `PRAGMA integrity_check` passes
- Pricing table version
- `git` is on `PATH`

Run this first when something looks wrong.

### `ccpulse version`

Prints the build version, commit hash, and build date.

## Configuration

`~/.config/ccpulse/config.toml` (created on first `config edit`):

```toml
[history]
retention_days = 0           # 0 = keep usage history forever; positive int prunes older rows

[paths]
projects_root = "~/.claude/projects"
cache_dir = "~/.cache/ccpulse"

[pricing]
override = ""                # path to a custom pricing.json
```

- `[history] retention_days` — drop usage history rows older than N days on each insert. Default `0` keeps history forever. Usage history is recorded once per ~3 minutes whenever ccpulse is running and successfully reaches the Anthropic usage API.

The plan tier (used to compute the 5h / 7d quota ceilings) is read from your Claude Code OAuth credential — there is no config knob for it.

## Troubleshooting

`ccpulse doctor` runs a checklist:

- Config loads / projects_root readable
- SQLite cache opens, integrity_check passes
- Pricing version
- `git` on PATH

If the TUI launches with empty tabs, run `ccpulse index` to do a cold
scan of `~/.claude/projects/`.

If the cache is corrupt (rare; usually after a kill-during-write), the
TUI auto-rebuilds on launch. Manual: `ccpulse index --rebuild`.

Parse errors are logged to `~/.cache/ccpulse/parse-errors.log`
(rotated at 10 MB). Empty when everything is healthy.

## License

MIT (see `LICENSE`).
