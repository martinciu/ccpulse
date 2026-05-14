#!/usr/bin/env bash
# seed-dev.sh — copy released ccpulse config or cache into the dev namespace.
#
# Subcommands:
#   config    Copy released config.toml into the dev config dir.
#   cache     Copy released state.db into the dev cache dir. Uses
#             sqlite3 .backup for online consistency; falls back to cp -f
#             with a warning if sqlite3 is not on PATH.
#
# Path resolution honors XDG_CONFIG_HOME and XDG_CACHE_HOME, mirroring
# pkg/config in the binary. The script is idempotent: re-running it always
# overwrites dev with the released snapshot.

set -euo pipefail

usage() {
  cat >&2 <<EOF
usage: seed-dev.sh <subcommand>

subcommands:
  config   copy released config.toml -> dev config dir
  cache    copy released state.db    -> dev cache dir
EOF
  exit 2
}

config_home() {
  if [[ -n "${XDG_CONFIG_HOME:-}" ]]; then
    echo "$XDG_CONFIG_HOME"
  else
    echo "$HOME/.config"
  fi
}

cache_home() {
  if [[ -n "${XDG_CACHE_HOME:-}" ]]; then
    echo "$XDG_CACHE_HOME"
  else
    echo "$HOME/.cache"
  fi
}

seed_config() {
  local src dst
  src="$(config_home)/ccpulse/config.toml"
  dst="$(config_home)/ccpulse-dev/config.toml"

  if [[ ! -f "$src" ]]; then
    echo "seed-dev.sh: released config not found at $src — nothing to seed from" >&2
    exit 1
  fi

  mkdir -p "$(dirname "$dst")"
  cp -f "$src" "$dst"
  echo "seeded $dst from $src"
}

seed_cache() {
  local src dst
  src="$(cache_home)/ccpulse/state.db"
  dst="$(cache_home)/ccpulse-dev/state.db"

  if [[ ! -f "$src" ]]; then
    echo "seed-dev.sh: released cache db not found at $src — nothing to seed from" >&2
    exit 1
  fi

  mkdir -p "$(dirname "$dst")"

  # Escape $dst for sqlite3's .backup dot-command. XDG_CACHE_HOME is
  # user-controlled and could contain quote-like characters
  # (e.g. /Users/o'malley/.cache). sqlite3 dot-commands don't support
  # SQL-style '' quote escaping, but they DO support \\ and \" inside
  # double-quoted args. Wrap in double quotes; escape \ and " in the path.
  local bs='\'
  local dq='"'
  local dst_escaped="${dst//$bs/$bs$bs}"
  dst_escaped="${dst_escaped//$dq/$bs$dq}"
  if command -v sqlite3 >/dev/null 2>&1 && sqlite3 "$src" ".backup \"$dst_escaped\""; then
    echo "seeded $dst from $src (sqlite3 online backup)"
  else
    echo "seed-dev.sh: sqlite3 unavailable or backup failed (see message above); falling back to cp (released ccpulse must be paused for consistency)" >&2
    cp -f "$src" "$dst"
    echo "seeded $dst from $src (cp fallback)"
  fi
}

case "${1:-}" in
  config) seed_config ;;
  cache)  seed_cache ;;
  *)      usage ;;
esac
