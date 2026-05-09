# ccpulse

A native Go TUI dashboard for Claude Code usage. Reads
`~/.claude/projects/*/*.jsonl` transcripts, computes token / cost /
5-hour-window breakdowns, and integrates with tmux. Local-only — no
network calls during normal operation.

![ccpulse — Live tab](docs/screenshots/live.png)

## Features

- **Live tab** — active sessions across all your projects, with `⚡`
  recency markers and `◆` for sessions in your current tmux session.
- **Today / History / Projects / Models tabs** — drill-downs by date,
  project, and model.
- **5-hour plan-window gauge** — rolling window over the Claude Max
  rate-limit period, color-bucketed (violet / yellow / red).
- **tmux statusline export** — `ccpulse status --tmux` returns a
  one-liner suitable for `status-right`.
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

The binary lives in `~/.local/bin/ccpulse`. Run from anywhere:

```sh
ccpulse              # opens the TUI
ccpulse status --tmux   # one-line status, for tmux status-right
ccpulse status --json   # JSON for scripting
ccpulse index --rebuild # full rebuild of the SQLite cache
ccpulse config edit  # open $EDITOR on config.toml
ccpulse doctor       # health-check checklist
```

## Configuration

`~/.config/ccpulse/config.toml` (created on first `config edit`):

```toml
[plan]
tier = "max_20x"             # max_5x | max_20x | pro | api | custom
custom_ceiling_tokens = 0    # used when tier = "custom"
# api_warn_usd = 5.0         # tier = "api" only
# api_hot_usd  = 10.0        # tier = "api" only

[ui]
accent = "#6c71c4"           # any hex; default Solarized violet
default_tab = "live"
default_scope = "global"     # global | this_tmux
tick_ms = 1000

[history]
default_window_days = 30
include_subagents = true

[paths]
projects_root = "~/.claude/projects"
cache_dir = "~/.cache/ccpulse"

[pricing]
override = ""                # path to a custom pricing.json
```

Plan-tier ceilings are rough estimates (Anthropic doesn't publish exact
caps). For precision, use `tier = "custom"` and tune
`custom_ceiling_tokens` from your observed rate-limit warnings.

## tmux integration

Add to `status-right` in `tmux.conf`:

```tmux
set -g status-right "#(/Users/<you>/.local/bin/ccpulse status --tmux) ..."
```

The chip emits `#[fg=...]` color escapes inline (no separate color
config needed). Refresh interval and chip placement are up to your
status-right layout.

## Troubleshooting

`ccpulse doctor` runs a checklist:

- Config loads / projects_root readable
- SQLite cache opens, integrity_check passes
- Pricing version
- `git` and `tmux` on PATH

If the TUI launches with empty tabs, run `ccpulse index` to do a cold
scan of `~/.claude/projects/`.

If the cache is corrupt (rare; usually after a kill-during-write), the
TUI auto-rebuilds on launch. Manual: `ccpulse index --rebuild`.

Parse errors are logged to `~/.cache/ccpulse/parse-errors.log`
(rotated at 10 MB). Empty when everything is healthy.

## License

MIT (see `LICENSE`).
