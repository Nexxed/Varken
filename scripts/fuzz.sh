#!/usr/bin/env bash
# Runs every fuzz target for a short time (FUZZTIME, default 10s each).
set -euo pipefail
fuzztime="${FUZZTIME:-10s}"
grep -rl --include='*_test.go' '^func Fuzz' internal | while read -r file; do
  pkg="./$(dirname "$file")"
  grep -o '^func Fuzz[A-Za-z0-9_]*' "$file" | sed 's/^func //' | while read -r target; do
    echo "== $pkg $target"
    go test -run='^$' -fuzz="^${target}\$" -fuzztime="$fuzztime" "$pkg"
  done
done
