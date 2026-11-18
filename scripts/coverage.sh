#!/usr/bin/env bash
# Runs the tests with the race detector and fails when an internal package is below the coverage floor.
set -euo pipefail
floor="${COVERAGE_FLOOR:-90}"
out="$(go test -race -count=1 -cover ./... 2>&1)" || { echo "$out"; exit 1; }
echo "$out"
fail=0
while read -r pkg pct; do
  if awk -v p="$pct" -v f="$floor" 'BEGIN { exit !(p < f) }'; then
    echo "coverage of $pkg is ${pct}%, below ${floor}%"
    fail=1
  fi
done < <(echo "$out" | awk '/^ok/ && /internal\// && /coverage:/ && !/internal\/testutil/ {
  for (i = 1; i <= NF; i++) if ($i == "coverage:") { sub("%", "", $(i+1)); print $2, $(i+1) } }')
exit $fail
