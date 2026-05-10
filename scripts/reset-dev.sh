#!/usr/bin/env bash
# reset-dev.sh — wipe dev-channel ccpulse paths.
#
# Deletes:
#   $XDG_CONFIG_HOME/ccpulse-dev   (or $HOME/.config/ccpulse-dev)
#   $XDG_CACHE_HOME/ccpulse-dev    (or $HOME/.cache/ccpulse-dev)
#
# Safety: refuses to remove any path lacking the "-dev" suffix in its
# final segment. Released paths can never be touched by this script.

set -euo pipefail

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

remove_if_dev() {
  local path="$1"
  local base
  base="$(basename "$path")"
  if [[ "$base" != *-dev ]]; then
    echo "reset-dev.sh: refusing to remove $path (basename lacks -dev suffix)" >&2
    exit 1
  fi
  if [[ -e "$path" ]]; then
    rm -rf "$path"
    echo "removed $path"
  else
    echo "skipped $path (does not exist)"
  fi
}

remove_if_dev "$(config_home)/ccpulse-dev"
remove_if_dev "$(cache_home)/ccpulse-dev"
