#!/usr/bin/env bash
# Regenerates every Python-produced expectation from the Python version (branch python-1.x) and fails if anything
# in testdata changed: proof that the golden files really are what the Python code writes.
#
#   scripts/golden.sh [path-to-python-1.x-checkout]
#
# Needs Python 3.13. Creates a virtualenv in .golden-venv.
set -euo pipefail
cd "$(dirname "$0")/.."
src="${1:-.golden-python}"
if [ ! -d "$src/varken" ]; then
  git fetch --depth 1 origin python-1.x
  git worktree add --detach "$src" FETCH_HEAD
fi
if [ ! -x .golden-venv/bin/python ]; then
  python3 -m venv .golden-venv
  .golden-venv/bin/pip install --quiet -r "$src/requirements.txt" time-machine
fi
py=.golden-venv/bin/python
$py tools/pygolden/pycompat_gen.py --python-src "$src" --skip-tables
$py internal/collector/arr/testdata/gen_corpus.py --python-src "$src"
$py tools/pygolden/fixtures/build.py
$py tools/pygolden/harness.py --python-src "$src"
$py tools/pygolden/config_cases.py
$py tools/pygolden/config_dump.py --python-src "$src"
paths=(testdata internal/collector/arr/testdata)
git status --porcelain -- "${paths[@]}"
git diff --exit-code -- "${paths[@]}"
test -z "$(git status --porcelain -- "${paths[@]}")"
