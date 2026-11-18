#!/usr/bin/env python3
"""Golden-output harness: runs one job of the Python Varken (branch python-1.x) against a fixture case and records
exactly what it sends to InfluxDB, so the Go port can be tested byte for byte against it.

Usage (from the repository root, with a venv built from python-1.x's requirements.txt plus time-machine):

    python tools/pygolden/harness.py --python-src /path/to/python-1.x-worktree [case dirs or globs...]

With no case arguments every testdata/fixtures/*/*/case.json is run.

case.json keys:
    job            Python method name, e.g. "get_queue"
    args           keyword arguments for the job (optional)
    now            frozen wall-clock time, ISO 8601 with offset
    tz             TZ for the run (default "UTC"); the Python code uses local time for dates and timestamps
    server         overrides for the service's server NamedTuple (url is always the fixture server)
    base_path      path appended to the fixture server URL (optional)
    routes         fixture routes, see fakes.FixtureServer / internal/testutil
    influx         FakeInflux options (optional)
    public_ip      value returned by the public-IP lookup (Tautulli); public_ip_error: true makes it fail
    fix            set when the Go port deliberately differs. The harness then writes python.* files for
                   reference and leaves expected.* (hand-reviewed) alone.

Outputs per case (next to case.json):
    server.json          resolved server settings the job ran with (the Go test loads these)
    expected.lp          concatenated non-empty InfluxDB write bodies       (python.lp for fix cases)
    expected.requests    sorted unique upstream requests                    (python.requests for fix cases)
    expected.err         exception that ended the Python job, if any        (python.err for fix cases)
"""
import argparse
import glob
import json
import os
import subprocess
import sys
import time
import traceback
import zoneinfo
from concurrent.futures import ThreadPoolExecutor
from datetime import datetime
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
FIXTURES = REPO / 'testdata' / 'fixtures'
GEOIP_DB = REPO / 'testdata' / 'geoip' / 'GeoLite2-City-Test.mmdb'

SERVICES = {
    'sonarr': ('varken.sonarr', 'SonarrAPI', 'SonarrServer'),
    'radarr': ('varken.radarr', 'RadarrAPI', 'RadarrServer'),
    'lidarr': ('varken.lidarr', 'LidarrAPI', 'SonarrServer'),
    'tautulli': ('varken.tautulli', 'TautulliAPI', 'TautulliServer'),
    'ombi': ('varken.ombi', 'OmbiAPI', 'OmbiServer'),
    'overseerr': ('varken.overseerr', 'OverseerrAPI', 'OverseerrServer'),
    'sickchill': ('varken.sickchill', 'SickChillAPI', 'SickChillServer'),
    'unifi': ('varken.unifi', 'UniFiAPI', 'UniFiServer'),
    'cleanuparr': ('varken.cleanuparr', 'CleanuparrAPI', 'CleanuparrServer'),
}


def write_or_remove(path, text):
    if text:
        path.write_text(text, encoding='utf-8')
    elif path.exists():
        path.unlink()


def run_one(case_dir, python_src):
    """Runs inside a fresh subprocess: TZ, module state and the frozen clock must not leak between cases."""
    case_dir = Path(case_dir)
    case = json.loads((case_dir / 'case.json').read_text(encoding='utf-8'))
    service = case.get('service') or case_dir.parent.name

    os.environ['TZ'] = case.get('tz', 'UTC')
    time.tzset()
    sys.path.insert(0, str(HERE))
    sys.path.insert(0, python_src)

    import importlib
    import time_machine
    from fakes import FixtureServer, FakeInflux
    from varken import structures
    from varken.dbmanager import DBManager

    module_name, api_name, server_name = SERVICES[service]
    module = importlib.import_module(module_name)
    api_class = getattr(module, api_name)
    server_class = getattr(structures, server_name)

    fixture = FixtureServer(case.get('routes', []))
    influx = FakeInflux(case.get('influx'))

    settings = {'id': 1, 'api_key': 'testkey', 'verify_ssl': False}
    settings.update(case.get('server') or {})
    settings['url'] = fixture.url + case.get('base_path', '')
    server = server_class(**settings)

    if service == 'tautulli':
        public_ip = case.get('public_ip', '')
        fail = case.get('public_ip_error', False)

        class _Reply(object):
            text = public_ip

        def fake_plain_get(url, timeout=None):
            from requests.exceptions import ConnectionError
            if fail:
                raise ConnectionError('public ip lookup disabled in tests')
            return _Reply()

        module.plain_get = fake_plain_get

    error = ''
    # time-machine switches the process to the frozen time's own zone: give it the case's zone, so local dates
    # (date.today()) and astimezone() follow the case's TZ even when "now" is written in UTC.
    now = datetime.fromisoformat(case['now']).astimezone(zoneinfo.ZoneInfo(case.get('tz', 'UTC')))
    with time_machine.travel(now, tick=False):
        dbmanager = DBManager(structures.InfluxServer(url='127.0.0.1', port=influx.port))
        try:
            if service == 'tautulli':
                api = api_class(server, dbmanager, make_geoip_handler())
            else:
                api = api_class(server, dbmanager)
            getattr(api, case['job'])(**(case.get('args') or {}))
        except BaseException as e:  # noqa: B902 - SystemExit included: record how the Python job ended
            frames = traceback.extract_tb(e.__traceback__)
            where = next((f for f in reversed(frames) if '/varken/' in f.filename), frames[-1] if frames else None)
            location = f' at {Path(where.filename).name}:{where.lineno}' if where else ''
            error = f'{type(e).__name__}: {e}{location}\n'

    fixture.close()
    influx.close()

    resolved = {key: value for key, value in server._asdict().items()}
    resolved['url'] = case.get('base_path', '')
    (case_dir / 'server.json').write_text(json.dumps(resolved, indent=2, sort_keys=True) + '\n', encoding='utf-8')

    lines = ''.join(body for body in influx.writes if body.strip())
    requests_text = ''.join(f'{r}\n' for r in sorted(set(fixture.requests)))
    prefix = 'python' if case.get('fix') else 'expected'
    write_or_remove(case_dir / f'{prefix}.lp', lines)
    write_or_remove(case_dir / f'{prefix}.requests', requests_text)
    write_or_remove(case_dir / f'{prefix}.err', error)


def make_geoip_handler():
    from geoip2.database import Reader
    from logging import getLogger
    from varken.helpers import GeoIPHandler

    handler = GeoIPHandler.__new__(GeoIPHandler)
    handler.data_folder = str(GEOIP_DB.parent)
    handler.maxmind_license_key = 'test'
    handler.dbfile = str(GEOIP_DB)
    handler.logger = getLogger()
    handler.reader = Reader(str(GEOIP_DB))
    return handler


def main():
    parser = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    parser.add_argument('--python-src', required=True, help='checkout of the python-1.x branch')
    parser.add_argument('--one', help=argparse.SUPPRESS)
    parser.add_argument('-j', '--jobs', type=int, default=os.cpu_count() or 4)
    parser.add_argument('cases', nargs='*', help='case directories or globs (default: all)')
    opts = parser.parse_args()

    if opts.one:
        run_one(opts.one, opts.python_src)
        return

    patterns = opts.cases or [str(FIXTURES / '*' / '*')]
    case_dirs = sorted({Path(p) for pattern in patterns for p in glob.glob(pattern)
                        if (Path(p) / 'case.json').exists()})
    if not case_dirs:
        sys.exit('no cases found')

    env = dict(os.environ)
    env['NO_PROXY'] = env['no_proxy'] = '127.0.0.1,localhost'

    def run(case_dir):
        proc = subprocess.run([sys.executable, '-I', __file__, '--python-src', opts.python_src, '--one',
                               str(case_dir)], env=env, capture_output=True, text=True, timeout=120)
        return case_dir, proc

    failed = 0
    with ThreadPoolExecutor(max_workers=opts.jobs) as pool:
        for case_dir, proc in pool.map(run, case_dirs):
            if proc.returncode != 0:
                failed += 1
                print(f'HARNESS FAILURE {case_dir}\n{proc.stdout}{proc.stderr}', file=sys.stderr)
    print(f'{len(case_dirs) - failed}/{len(case_dirs)} cases recorded')
    sys.exit(1 if failed else 0)


if __name__ == '__main__':
    main()
