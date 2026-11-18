"""Helpers for the fixture definitions. Each service module calls case(...) at import time; build.py writes them.

Fixtures are synthetic: shaped after each API's documented responses and the fields the Python code reads.
Real responses can be added with tools/record.
"""
import copy
import json
from pathlib import Path

REPO = Path(__file__).resolve().parents[3]
FIXTURES = REPO / 'testdata' / 'fixtures'

# A fixed instant with microseconds, in a zone with a non-zero offset, so timestamp maths is exercised.
NOW = '2026-11-18T16:30:00.123456+00:00'
TZ = 'America/Chicago'

CASES = {}


def route(path, body=None, *, status=200, query=None, headers=None, method='GET', times=None, text=None,
          match_headers=None):
    r = {'method': method, 'path': path, 'status': status}
    if query:
        r['match_query'] = query
    if match_headers:
        r['match_headers'] = match_headers
    if headers:
        r['headers'] = headers
    if times is not None:
        r['times'] = times
    if text is not None:
        r['body_text'] = text
    elif body is not None:
        r['body'] = copy.deepcopy(body)
    return r


def case(service, name, job, routes, *, args=None, server=None, now=NOW, tz=TZ, description='', fix=None,
         **extra):
    key = (service, name)
    if key in CASES:
        raise ValueError(f'duplicate case {service}/{name}')
    data = {'job': job}
    if description:
        data['description'] = description
    if fix:
        data['fix'] = fix
    if args:
        data['args'] = args
    data['now'] = now
    data['tz'] = tz
    if server:
        data['server'] = server
    data.update(extra)
    data['routes'] = routes
    CASES[key] = data
    return data


def write_all():
    written = 0
    for (service, name), data in sorted(CASES.items()):
        case_dir = FIXTURES / service / name
        case_dir.mkdir(parents=True, exist_ok=True)
        text = json.dumps(data, indent=2, ensure_ascii=False) + '\n'
        path = case_dir / 'case.json'
        if not path.exists() or path.read_text(encoding='utf-8') != text:
            path.write_text(text, encoding='utf-8')
            written += 1
    return written
