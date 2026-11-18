#!/usr/bin/env python3
"""Records how the Python INIParser (branch python-1.x) reads each config case, for the Go differential test.

    python tools/pygolden/config_dump.py --python-src /path/to/python-1.x-worktree [case dirs...]

A case is testdata/config/<name>/ with varken.ini (optional: missing means first run) and env.json (optional,
{"VRKN_...": "value"}). Each run happens in a scratch copy of the folder together with data/varken.example.ini,
in a subprocess with only the case's VRKN_* variables set. Writes python.json next to the inputs:

    exit        SystemExit code, or null
    exception   uncaught exception ("Type: message"), or null
    rewritten   true when Python rewrote (rectified) varken.ini
    influx      InfluxServer settings
    services    {service: {"enabled": [ids] | false | null, "servers": [server settings]}}
    filtered    sorted strings the log filter masks
"""
import argparse
import glob
import json
import os
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SERVICES = ['sonarr', 'radarr', 'lidarr', 'ombi', 'overseerr', 'tautulli', 'sickchill', 'unifi', 'cleanuparr']


def run_one(case_dir, python_src):
    sys.path.insert(0, python_src)
    import logging
    logging.disable(logging.CRITICAL)
    sys.setrecursionlimit(200)
    from varken.iniparser import INIParser

    folder = Path(os.environ['VARKEN_CASE_FOLDER'])
    before = (folder / 'varken.ini').read_text() if (folder / 'varken.ini').exists() else None
    result = {'exit': None, 'exception': None}
    parser = None
    try:
        parser = INIParser(str(folder))
    except SystemExit as e:
        result['exit'] = e.code if isinstance(e.code, int) else (1 if e.code else 0)
    except BaseException as e:  # noqa: B902
        result['exception'] = f'{type(e).__name__}'
    after = (folder / 'varken.ini').read_text() if (folder / 'varken.ini').exists() else None
    result['rewritten'] = before is not None and after != before
    result['created'] = before is None and after is not None
    if parser is not None and result['exit'] is None and result['exception'] is None:
        result['influx'] = parser.influx_server._asdict()
        services = {}
        for service in SERVICES:
            enabled = getattr(parser, f'{service}_enabled', None)
            servers = [s._asdict() for s in getattr(parser, f'{service}_servers', [])]
            services[service] = {'enabled': enabled if enabled else (False if enabled is False else None),
                                 'servers': servers}
        result['services'] = services
        result['filtered'] = sorted(set(parser.filtered_strings or []))
    out = Path(case_dir) / 'python.json'
    out.write_text(json.dumps(result, indent=2, sort_keys=True) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--python-src', required=True)
    parser.add_argument('--one', help=argparse.SUPPRESS)
    parser.add_argument('cases', nargs='*')
    opts = parser.parse_args()
    if opts.one:
        run_one(opts.one, opts.python_src)
        return
    patterns = opts.cases or [str(REPO / 'testdata' / 'config' / '*')]
    dirs = sorted({Path(p) for pat in patterns for p in glob.glob(pat) if Path(p).is_dir()})
    failed = 0
    for case_dir in dirs:
        with tempfile.TemporaryDirectory() as tmp:
            folder = Path(tmp) / 'data'
            folder.mkdir()
            shutil.copy(REPO / 'data' / 'varken.example.ini', folder / 'varken.example.ini')
            if (case_dir / 'varken.ini').exists():
                shutil.copy(case_dir / 'varken.ini', folder / 'varken.ini')
            env = {k: v for k, v in os.environ.items() if not k.startswith('VRKN_')}
            if (case_dir / 'env.json').exists():
                env.update(json.loads((case_dir / 'env.json').read_text()))
            env['VARKEN_CASE_FOLDER'] = str(folder)
            proc = subprocess.run([sys.executable, '-I', __file__, '--python-src', opts.python_src, '--one',
                                   str(case_dir)], env=env, capture_output=True, text=True, timeout=60)
            if proc.returncode != 0:
                failed += 1
                print(f'FAILED {case_dir}\n{proc.stdout}{proc.stderr}', file=sys.stderr)
    print(f'{len(dirs) - failed}/{len(dirs)} config cases recorded')
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
