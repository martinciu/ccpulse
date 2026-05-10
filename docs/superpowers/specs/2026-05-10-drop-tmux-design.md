# Drop tmux integration — design spec

## Context

`ccpulse status --tmux` (`cmd/ccpulse/status.go:28`) emits tmux-format color escapes
(`#[fg=#6c71c4]`, etc.) with hardcoded Solarized hex values. This conflicts with
user-configured tmux themes (Dracula, Catppuccin, gruvbox, …). The dominant pattern
for status-bar tooling is *tool emits structured output; ecosystem wraps it* — so
`--json` is the idiomatic exit point for any external tool.

## Decision

Drop all tmux integration entirely. `pkg/tmux/` (already noted as vestigial in CLAUDE.md)
and the `--tmux` flag both go. No replacement flags or config.

## Changes

### `cmd/ccpulse/status.go`

- Remove `asTmux bool` parameter and flag
- Remove two `if asTmux { return nil }` early-exit paths
- Remove `status.TmuxLine(...)` case
- Remove `resolveDisplayMode` and `status.DisplayBudget` — output format is always
  percent when OAuth is available, cost otherwise (no user-configurable mode)
- Switch to three cases: JSON (`--json`), default human-readable, error

### `pkg/status/status.go`

- Delete `DisplayMode`, `DisplayPercent`, `DisplayCost` consts
- Delete `DisplayBudget` struct
- Delete `clrViolet`, `clrYellow`, `clrRed`, `speedometer` consts
- Delete `TmuxLine`, `bucketColorByPercent`, `bucketColorByCost`, `dur`
- Update `Window` comment: "consumed by the TUI header and `status --json`"
- Update `DisplayMode` comment to reflect it no longer exists

### `pkg/status/status_test.go`

- Delete `TestTmuxLinePercent`, `TestTmuxLineCost`, `TestTmuxLineHot`

### `cmd/ccpulse/status_integration_test.go`

- Add `TestStatusTmuxFlagRemoved` asserting `ccpulse status --tmux` exits with error
  containing "unknown flag"

### `pkg/tmux/` (entire package)

- Delete `pkg/tmux/tmux.go` and `pkg/tmux/tmux_test.go`

### `cmd/ccpulse/doctor.go`

- Remove `tmuxErr` and the `tmux on PATH` check

### `pkg/config/config.go:13`

- Update comment: "Display controls the TUI presentation. Mode is one of..."

### `pkg/anthro/tier.go:20`

- Update comment: "TierPretty returns a human-readable label for the TUI."

### README.md

- Remove "integrates with tmux" from tagline
- Remove "◆ for sessions in your current tmux session" feature bullet
- Remove "tmux statusline export" bullet and `--tmux` from the command examples
- Remove the tmux integration section entirely
- In `doctor` section, remove "git and tmux on PATH"
- In `[ui] default_scope`, remove the `this_tmux` option hint

### CLAUDE.md

- Remove `pkg/tmux` and `pkg/state` rows from the package map (both vestigial post-#34)

### `pkg/state/` — no change

`pkg/state` is a separate vestigial package; not tmux-specific, left as-is.