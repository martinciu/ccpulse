#!/usr/bin/env bash
# seed-front-loaded.sh — populate usage_samples with a front-loaded shape so
# `./ccpulse` can be visually inspected for the recency-weighted 7d
# projection (issue #170).
#
# Shape: 50% utilization for the first 2 days, flat for the last 5 days.
# Expected readout: 7d slope ≈ 0%/day, projection ≈ 50% at reset.

set -euo pipefail

CACHE_DIR="${CCPULSE_CACHE_DIR:-$HOME/.cache/ccpulse}"
DB="$CACHE_DIR/state.db"

if [[ ! -f "$DB" ]]; then
  echo "no cache db at $DB — run \`make seed-dev\` first" >&2
  exit 1
fi

NOW_UNIX=$(date -u +%s)
RESETS_AT=$(date -u -v+3d '+%Y-%m-%dT%H:%M:%S.000000000Z' 2>/dev/null \
  || date -u -d '+3 days' '+%Y-%m-%dT%H:%M:%S.000000000Z')

if [[ ! "$RESETS_AT" =~ ^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]+)?Z$ ]]; then
  echo "RESETS_AT shape unexpected: $RESETS_AT" >&2
  exit 1
fi

# 14 samples over 7 days: ramp 0→50 over days 1-2, flat at 50 thereafter.
sqlite3 "$DB" "DELETE FROM usage_samples;"
for i in $(seq 0 13); do
  HOURS_BACK=$((168 - i*12)) # 7d = 168h, samples every 12h
  TS=$((NOW_UNIX - HOURS_BACK*3600))
  if [[ $i -le 4 ]]; then
    PCT=$(awk -v i=$i 'BEGIN { printf "%.2f", i*12.5 }') # 0, 12.5, 25, 37.5, 50
  else
    PCT="50.00"
  fi
  sqlite3 "$DB" \
    "INSERT INTO usage_samples(ts, source, seven_day_pct, seven_day_resets_at) VALUES ($TS, 'api', $PCT, '$RESETS_AT');"
done

echo "seeded 14 front-loaded samples; run \`./ccpulse\` to inspect 7d tile"
