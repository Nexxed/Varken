#!/usr/bin/env python3
"""Writes testdata/fixtures/<service>/<case>/case.json from the definitions in this package."""
import importlib
import pkgutil
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import common  # noqa: E402

for module in sorted(m.name for m in pkgutil.iter_modules([str(Path(__file__).resolve().parent)])):
    if module not in ('common', 'build'):
        importlib.import_module(module)

print(f'{len(common.CASES)} cases defined, {common.write_all()} case.json files written')
