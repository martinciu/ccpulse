# ccpulse

A native Go TUI dashboard for Claude Code usage. Reads
`~/.claude/projects/*/*.jsonl` transcripts, computes token / cost /
5-hour-window breakdowns. Local-only — no network calls during normal
operation.

![ccpulse TUI demo](demo/ccpulse.gif)

## Features

- **5h + 7d quota bars** — rolling-window gauges in the header, fed by
  the Anthropic usage API where available with a JSONL fallback.
- **Token histogram** — horizontally-scrollable bar chart of usage per
  time bucket, heat-coloured relative to the peak bucket. Zoom cycles
  between 15m / 1h / 24h granularity.
- **fsnotify live updates** — file watcher (not polling) keeps the
  cache in sync as Claude writes new turns; the TUI redraws on each
  refresh.

## Installation

### Homebrew (macOS)

```sh
brew install martinciu/tap/ccpulse
```

Shell completions are installed automatically. This is a macOS **cask**,
which Homebrew only supports on macOS — on Linux, use the `.deb`/`.rpm`
packages or the tarball below.

### Debian / Ubuntu (.deb)

```sh
curl -LO https://github.com/martinciu/ccpulse/releases/latest/download/ccpulse_<version>_linux_amd64.deb
sudo dpkg -i ccpulse_*.deb
```

Replace `<version>` with the actual release number (e.g. `0.1.0`) — it is
part of the asset filename, so the URL 404s if left literal. Swap `amd64`
for `arm64` on ARM. The
[releases page](https://github.com/martinciu/ccpulse/releases) lists the
current version.

### Fedora / RHEL / openSUSE (.rpm)

```sh
curl -LO https://github.com/martinciu/ccpulse/releases/latest/download/ccpulse_<version>_linux_amd64.rpm
sudo rpm -i ccpulse_*.rpm
```

Same `<version>` and `amd64`→`arm64` substitutions as above.

### Manual / no-root (tarball)

For hosts where a package manager isn't an option — e.g. a sandbox with
`no-new-privileges` where `sudo`/`dpkg` is blocked — install the static
binary from the tarball into a directory you own:

```sh
# Debian/Ubuntu: dpkg --print-architecture → amd64 or arm64
ARCH=$(dpkg --print-architecture)
curl -fsSL "https://github.com/martinciu/ccpulse/releases/latest/download/ccpulse_<version>_linux_${ARCH}.tar.gz" | tar xz
install -Dm755 ccpulse ~/.local/bin/ccpulse   # ensure ~/.local/bin is on PATH
```

ccpulse is a single static binary with no runtime dependencies. On
non-Debian systems substitute the arch by hand (artifacts use Go's
`amd64`/`arm64`, not `uname -m`'s `x86_64`/`aarch64`). macOS users without
Homebrew can install the matching `darwin_{amd64,arm64}.tar.gz` the same way.

### Verifying the download (optional)

Each release ships a `checksums.txt` alongside the binaries. To verify
integrity before installing:

```sh
curl -LO https://github.com/martinciu/ccpulse/releases/latest/download/checksums.txt
sha256sum --ignore-missing -c checksums.txt
```

Run this from the directory where you downloaded the `.deb` / `.rpm` /
tarball. The Homebrew install path is already integrity-checked by
`brew` against the cask's `sha256`.

### `go install`

```sh
go install github.com/martinciu/ccpulse/cmd/ccpulse@latest
```

Requires Go 1.25+. Skips the build-channel ldflag, so the binary writes
dev debug logs to `~/.cache/ccpulse/debug.log`.

After install, launch the TUI:

```sh
ccpulse
```

On first launch the TUI cold-walks `~/.claude/projects/` and backfills
the cache (progress shown in the header). After that, the fsnotify
watcher keeps the cache up to date automatically.

## Build from source

Requires `mise` and `git`.

```sh
git clone https://github.com/martinciu/ccpulse ~/code/ccpulse
cd ~/code/ccpulse
mise install        # fetches Go 1.25 into the project-scoped toolchain
make install        # builds → ~/.local/bin/ccpulse
```

The binary lives in `~/.local/bin/ccpulse`.

## Commands

### `ccpulse`

Opens the interactive TUI: 5h + 7d quota bars and a horizontally-scrollable
token-usage histogram. `←` / `→` scroll the chart, `z` cycles bucket zoom
(15m / 1h / 24h), `?` toggles full help, `q` quits.

### `ccpulse index`

Drops the SQLite cache and rebuilds it from a full scan of `projects_root`.

```sh
ccpulse index --rebuild   # drop the cache, then do a full scan
```

The TUI backfills automatically on launch, so this is only needed if
the cache gets out of sync or you want a clean slate. The bare
`ccpulse index` (no `--rebuild`) is intentionally an error.

### `ccpulse status`

Prints the current 5-hour rolling window without opening the TUI.

```sh
ccpulse status            # human-readable summary
ccpulse status --json     # JSON: 5h + 7d percent/reset, tokens_5h, cost_5h_usd,
                          #       ceiling, optional projection block
```

`--json` is useful for scripting or status bars that consume structured data.

#### Claude Code hook

`ccpulse status --quiet` is designed to be called from Claude Code's `Stop`
hook, which fires after every assistant turn. The internal 3-minute cache
TTL absorbs the burst rate, so the Anthropic usage API is hit at most
once every few minutes even during heavy use — no daemon, no blind
polling. Add this to `~/.claude/settings.json` (or merge the `"hooks"` key
if you already have one):

```json
{
  "hooks": {
    "Stop": [
      {
        "matcher": "",
        "hooks": [
          { "type": "command", "command": "ccpulse status --quiet" }
        ]
      }
    ]
  }
}
```

`ccpulse doctor` reports whether this hook is configured.

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
