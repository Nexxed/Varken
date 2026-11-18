#!/usr/bin/env python3
"""Generates the arr helper corpora in this directory from the Python version (branch python-1.x).

    python internal/collector/arr/testdata/gen_corpus.py --python-src /path/to/python-1.x-worktree
"""
import argparse
import json
import sys
from datetime import date
from pathlib import Path

HERE = Path(__file__).resolve().parent


def attempt(fn, *args):
    try:
        return fn(*args)
    except Exception as e:  # noqa: B902
        return {'error': type(e).__name__}


def write(name, rows):
    with open(HERE / name, 'w', encoding='utf-8') as fh:
        for row in rows:
            fh.write(json.dumps(row, ensure_ascii=False) + '\n')


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--python-src', required=True)
    sys.path.insert(0, parser.parse_args().python_src)
    from varken.arr import timeleft_seconds, quality_name
    from varken.arr_history import parse_arr_date, duration_seconds

    dates = ['2026-11-18T16:30:00Z', '2026-11-18T16:30:00.1234567Z', '2026-11-18T16:30:00.5Z',
             '2026-11-18T16:30:00+02:00', '2026-11-18T16:30:00.5+02:00', '2026-11-18T16:30:00-05:30',
             '2026-11-18T16:30:00', '2026-11-18', '2026-11-18 16:30:00', '2026-11-18T16:30', '2026-11-18T16',
             '2026-11-18T1630', '2026-11-18T163000', '20261118T163000Z', '2026-11-18T16:30:00,5Z',
             '2026-11-18T16:30:00.Z', '2026-11-18T16:30:00.123+05:30', '2026-11-18T16:30:00+0530',
             '2026-11-18T16:30:00+05', '2026-11-18T16:30:00+05:30:15', '2026-11-18T24:00:00Z',
             '2026-11-18T16:30:60Z', '2026-02-30T00:00:00Z', '2026-11-18T16:30:00 ', ' 2026-11-18T16:30:00Z',
             '2026-W47-3', '2026-W47-3T10:00:00Z', '0001-01-01T00:00:00Z', '1970-12-31T23:59:59Z',
             '1971-01-01T00:00:00Z', '1971-01-01T00:00:00+01:00', '9999-12-31T23:59:59.9999999Z', 'garbage', '',
             'Z', '2026-11-18Z', '2026-11-18T16:30:00.1234567890123Z', '2026-11-18x16:30:00Z',
             '2026-11-18T16:30:00+24:00', '2026-11-18T16:3000', '2026-11-18T16:30:00.', 'zz.zz',
             '2026-11-18T16:30:00.99999999Z', '2026-11-18T163000.5Z', '2026-11-18T16:30:00-00:00',
             '2026W473T10:00:00Z', '2026W47T10:00', '2026-W47T10:00:00Z', '2026-W53-1', '2020-W53-1T00:00Z',
             '2026-11-18T16:30:00+05:30:15.5', '2026-11-18T16:30:00+5', '2026-11-18T1', '2026-13-01',
             '2026-11-18T16:30:00+0', '2026-1118', '202611-18', 'abcd-11-18', '2026-11-18T16:30:00Zjunk']
    write('arr_dates.jsonl', [{'s': d, 'iso': attempt(lambda v: (lambda p: p.isoformat() if p else None)(
        parse_arr_date(v)), d)} for d in dates])

    lefts = ['00:00:00', '01:02:03', '1.02:03:04', '10.00:00:01', '23:59:59.5', '00:01:30.9999', '', 'bad', '1:2',
             '1.2.3:4:5', '-1:00:00', '99999.00:00:00', ':::', '1.:2:3', '00:00:-5', ' 1:2:3', '1:2:3 ',
             '1_0:00:00', '00:00:1e2', '00:00:nan', '00:00:inf', '٣:00:00', '01:02:03:04', '.1:2:3', '1.5:00:00']
    write('arr_timeleft.jsonl', [{'s': t, 'seconds': attempt(timeleft_seconds, t)} for t in lefts])

    durations = ['00:00:00', '00:00:01.5000000', '00:01:02.1234567', '1.02:03:04.5', '2.00:00:00', '', 'x',
                 '00:00:00.0001', '10:00', '1.2.3:4:5', '00:00:59.9995', '00:00:00.0005', '00:00:00.0015',
                 '-00:00:01', '00:00:1e1', '00:00:nan', '1:2:3', ' 1:2:3', '01:02:03.4567891', '3.:00:00']
    write('arr_duration.jsonl', [{'s': d, 'seconds': attempt(duration_seconds, d)} for d in durations])

    cal = ['2026-11-20T00:00:00Z', '2026-11-20', '20261120', '2026-11-2', '2026-02-30T00:00:00Z', '2026-11-20T',
           '2026-W47-3', '20261120T0', 'not a date', '', '2026-11-20 05:00', '2026-1-20T00:00:00Z',
           '2026W473', '2026-W47', '2026W47']
    write('calendar_dates.jsonl', [{'s': c, 'date': attempt(lambda v: str(date.fromisoformat(str(v)[:10])), c)}
                                   for c in cal])

    qualities = [None, {}, {'quality': None}, {'quality': {}}, {'quality': {'name': 'HDTV-720p'}},
                 {'quality': {'name': None}}, {'quality': {'name': 5}}, {'quality': 'x'}, 'x', [1],
                 {'quality': ['name']}, {'quality': {'name': ''}}]
    write('quality_name.jsonl', [{'v': q, 'name': attempt(quality_name, q)} for q in qualities])
    print('generated')


if __name__ == '__main__':
    main()
