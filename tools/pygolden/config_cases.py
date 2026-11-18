#!/usr/bin/env python3
"""Writes the config differential cases in testdata/config/<name>/ (varken.ini, env.json, notes.txt).

Run tools/pygolden/config_dump.py afterwards to record what the Python version made of each.
"""
import json
import shutil
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
OUT = REPO / 'testdata' / 'config'
EXAMPLE = (REPO / 'data' / 'varken.example.ini').read_text()

INFLUX = """[influxdb]
url = influxdb
port = 8086
ssl = false
verify_ssl = false
username = varken
password = s3cret-influx
"""

ARR_STATS = """get_health = true
get_health_run_seconds = 300
get_wanted = true
get_wanted_run_seconds = 900
get_history_totals = true
get_history_totals_run_seconds = 900
get_disk = true
get_disk_run_seconds = 900
get_history_events = true
get_history_events_run_seconds = 300
history_events_days = 1
history_events_backfill_days = 7
get_tasks = true
get_tasks_run_seconds = 900
get_library = true
get_library_run_seconds = 3600
"""


def sonarr(n, url='sonarr:8989', extra=''):
    return f"""[sonarr-{n}]
url = {url}
apikey = sonarrkey{n}aaaaaaaa
ssl = false
verify_ssl = false
missing_days = 7
missing_days_run_seconds = 300
future_days = 1
future_days_run_seconds = 300
queue = true
queue_run_seconds = 300
{ARR_STATS}{extra}"""


def radarr(n, url='radarr:7878', extra=''):
    return f"""[radarr-{n}]
url = {url}
apikey = radarrkey{n}bbbbbbbb
ssl = true
verify_ssl = true
queue = true
queue_run_seconds = 300
get_missing = true
get_missing_run_seconds = 300
{ARR_STATS}get_calendar = true
get_calendar_run_seconds = 3600
calendar_days = 30
{extra}"""


def lidarr(n):
    return f"""[lidarr-{n}]
url = lidarr:8686
apikey = lidarrkey{n}cccccccc
ssl = false
verify_ssl = false
missing_days = 30
missing_days_run_seconds = 300
future_days = 30
future_days_run_seconds = 300
queue = true
queue_run_seconds = 300
{ARR_STATS}"""


TAUTULLI = """[tautulli-1]
url = tautulli:8181
fallback_ip = 1.1.1.1
apikey = tautullikeydddddddd
ssl = false
verify_ssl = false
get_activity = true
get_activity_run_seconds = 30
get_stats = true
get_stats_run_seconds = 3600
get_server = true
get_server_run_seconds = 300
get_history = true
get_history_run_seconds = 300
history_days = 2
history_backfill_days = 364
get_daily_stats = true
get_daily_stats_run_seconds = 3600
"""

OTHERS = """[ombi-1]
url = ombi.example.com/ombi
apikey = ombikeyeeeeeeee
ssl = true
verify_ssl = false
get_request_type_counts = true
request_type_run_seconds = 300
get_request_total_counts = true
request_total_run_seconds = 300
get_issue_status_counts = true
issue_status_run_seconds = 300

[overseerr-1]
url = overseerr:5055
apikey = overseerrkeyffffffff
ssl = false
verify_ssl = false
get_request_total_counts = true
request_total_run_seconds = 30
get_latest_requests = true
num_latest_requests_to_fetch = 10
num_latest_requests_seconds = 30

[sickchill-1]
url = sickchill:8081
apikey = sickchillkeygggggggg
ssl = false
verify_ssl = false
get_missing = true
get_missing_run_seconds = 300

[unifi-1]
url = unifi:8443
username = admin
password = unifi-pass
site = Default
usg_name = Gateway
ssl = true
verify_ssl = false
get_usg_stats_run_seconds = 300

[cleanuparr-1]
url = cleanuparr:11011
apikey = cleanuparrkeyhhhhhhhh
ssl = false
verify_ssl = false
include_dry_run = false
get_stats = true
stats_hours = 24
get_stats_run_seconds = 300
get_timeline = true
timeline_hours = 48
get_timeline_run_seconds = 300
get_strikes = true
num_strikes_to_fetch = 50
get_strikes_run_seconds = 300
get_events = true
events_severity = Important
num_events_to_fetch = 50
get_events_run_seconds = 300
"""


def global_section(**ids):
    defaults = dict(sonarr='false', radarr='false', lidarr='false', tautulli='false', ombi='false', overseerr='false',
                    sickchill='false', unifi='false', cleanuparr='false')
    defaults.update(ids)
    lines = ['[global]'] + [f'{k}_server_ids = {v}' for k, v in defaults.items()]
    lines.append('maxmind_license_key = mmkey1234567890')
    return '\n'.join(lines) + '\n'


CASES = {}


def case(name, ini=None, env=None, notes=''):
    CASES[name] = (ini, env or {}, notes)


ALL = '\n'.join([global_section(sonarr='1,2', radarr='1', lidarr='1', tautulli='1', ombi='1', overseerr='1',
                                sickchill='1', unifi='1', cleanuparr='1'), INFLUX, sonarr(1), sonarr(2, 'tv.example.com:443/sonarr'),
                 radarr(1), lidarr(1), TAUTULLI, OTHERS])

case('example', EXAMPLE, notes='The shipped example, unchanged.')
case('all_services', ALL, notes='Every service enabled with realistic values.')
case('env_overrides', ALL, {
    'VRKN_INFLUXDB_URL': 'influx.lan', 'VRKN_INFLUXDB_PORT': '8087', 'VRKN_INFLUXDB_SSL': 'True',
    'VRKN_INFLUXDB_VERIFY_SSL': 'yes', 'VRKN_INFLUXDB_USERNAME': 'envuser', 'VRKN_INFLUXDB_PASSWORD': 'envpass',
    'VRKN_GLOBAL_RADARR_SERVER_IDS': 'false', 'VRKN_SONARR_1_APIKEY': 'envsonarrkey1234',
    'VRKN_SONARR_1_QUEUE': 'no', 'VRKN_SONARR_1_QUEUE_RUN_SECONDS': ' 60 ', 'VRKN_SONARR_2_SSL': 'TRUE',
    'VRKN_SONARR_2_VERIFY_SSL': 'true', 'VRKN_SONARR_1_GET_LIBRARY': 'false',
    'VRKN_SONARR_1_GET_LIBRARY_RUN_SECONDS': '7200', 'VRKN_TAUTULLI_1_FALLBACK_IP': '8.8.8.8',
    'VRKN_GLOBAL_MAXMIND_LICENSE_KEY': 'envmaxmindkey', 'VRKN_UNIFI_1_SITE': 'MySite',
    'VRKN_OMBI_1_GET_ISSUE_STATUS_COUNTS': 'false', 'VRKN_CLEANUPARR_1_EVENTS_SEVERITY': '  Warning  ',
    'VRKN_CLEANUPARR_1_INCLUDE_DRY_RUN': 'yes', 'VRKN_OVERSEERR_1_NUM_LATEST_REQUESTS_SECONDS': '45',
    'VRKN_TAUTULLI_1_HISTORY_BACKFILL_DAYS': '30', 'VRKN_RADARR_1_CALENDAR_DAYS': '14',
}, notes='Environment variables override the ini, including globals, booleans and padded ints.')
case('env_selects_section', global_section(sonarr='1') + INFLUX + sonarr(1) + sonarr(2, 'sonarr2:8989'),
     {'VRKN_GLOBAL_SONARR_SERVER_IDS': '2'}, 'The env var picks section 2 instead of the ini value.')
case('server_ids_parsing', global_section(sonarr='1, x, 2', radarr='No', lidarr='', ombi=' , ') + INFLUX
     + sonarr(1) + sonarr(2), notes='Spaces and invalid ids are dropped; "No" disables; nothing valid disables.')
case('ssl_and_verify', global_section(sonarr='1,2,3') + INFLUX
     + sonarr(1).replace('verify_ssl = false', 'verify_ssl = true')
     + sonarr(2).replace('ssl = false\nverify_ssl = false', 'ssl = Yes\nverify_ssl = TRUE')
     + sonarr(3).replace('ssl = false\nverify_ssl = false', 'ssl = on\nverify_ssl = 1'),
     notes='verify_ssl is forced off without ssl; only true/yes (any case) are true.')
case('invalid_int_skips_server', global_section(sonarr='1,2') + INFLUX
     + sonarr(1).replace('queue_run_seconds = 300', 'queue_run_seconds = 30.5') + sonarr(2),
     notes='A ValueError skips only that server.')
case('url_with_path_and_port', global_section(sonarr='1', ombi='1') + INFLUX.replace('url = influxdb', 'url = influx.example.com/db')
     + sonarr(1, 'media.example.com:8443/tv/sonarr/') + OTHERS.split('[overseerr-1]')[0],
     notes='Base paths and ports are kept; the influx url may have a path.')
case('missing_optional_keys', global_section(sonarr='1', radarr='1', tautulli='1') + INFLUX
     + sonarr(1).replace(ARR_STATS, '') + radarr(1).replace(ARR_STATS, '').replace(
         'get_calendar = true\nget_calendar_run_seconds = 3600\ncalendar_days = 30\n', '')
     + TAUTULLI.split('get_server = true')[0], notes='Settings added after release fall back to their defaults.')
case('missing_required_key', global_section(sonarr='1', radarr='1') + INFLUX
     + sonarr(1).replace('queue_run_seconds = 300\n', '') + radarr(1),
     notes='Python rewrote varken.ini from the example to fill the key; Go uses the same default in memory.')
case('missing_global_key', '\n'.join(l for l in global_section(sonarr='1').splitlines()
                                     if not l.startswith('cleanuparr')) + '\n' + INFLUX + sonarr(1),
     notes='A missing *_server_ids key means the service is off.')
case('missing_influx_section', global_section(sonarr='1') + sonarr(1),
     notes='No [influxdb] section: Python filled it from the example (influxdb.domain.tld).')
case('section_for_id_missing', global_section(sonarr='1,3') + INFLUX + sonarr(1),
     notes='Python crashed (NoSectionError after rectifying); Go skips server 3.')
case('ini_syntax', """; leading comment
# another comment
[DEFAULT]
verify_ssl = false

[global]
sonarr_server_ids = 1
radarr_server_ids : false
lidarr_server_ids=false
tautulli_server_ids = false
ombi_server_ids = false
overseerr_server_ids = false
sickchill_server_ids = false
unifi_server_ids = false
cleanuparr_server_ids = false
maxmind_license_key = abc;def#ghi

[influxdb]
URL = influxdb
Port = 8086
ssl = false
username = user
password = pa ss#word;x
  continued

[sonarr-1]   ; trailing text after the header
url = sonarr:8989
apikey = key#with;chars
ssl = false
  # an indented comment line inside the section
missing_days = 7
missing_days_run_seconds = 300
future_days = 1
future_days_run_seconds = 300
queue = true
queue_run_seconds = 300
""", notes='Comments, DEFAULT, ":" and "=" delimiters, key case, continuation lines, # and ; inside values.')
case('private_fallback_ip', global_section(tautulli='1') + INFLUX + TAUTULLI.replace('1.1.1.1', '192.168.1.10'),
     notes='Python exited; Go drops the Tautulli server.')
case('ipv6_fallback_ip', global_section(tautulli='1') + INFLUX + TAUTULLI.replace('1.1.1.1', '2606:4700:4700::1111'),
     notes='Python crashed (AddressValueError); Go accepts a public IPv6 address.')
case('first_run', None, notes='No varken.ini: it is created from the example.')
case('overseerr_env_names', global_section(overseerr='1') + INFLUX + OTHERS.split('[sickchill-1]')[0].split('[ombi-1]')[0]
     + '[overseerr-1]' + OTHERS.split('[overseerr-1]')[1].split('[sickchill-1]')[0],
     {'VRKN_OVERSEERR_1_GET_LATEST_REQUESTS_TO_FETCH': '25'},
     notes='The legacy env name for num_latest_requests_to_fetch still works.')
case('unifi_and_cleanuparr', global_section(unifi='1', cleanuparr='1') + INFLUX + '[unifi-1]'
     + OTHERS.split('[unifi-1]')[1], {'VRKN_UNIFI_1_PASSWORD': 'envpass!', 'VRKN_CLEANUPARR_1_STATS_HOURS': '12'},
     notes='UniFi has no apikey; site is lower-cased; Cleanuparr settings from env.')
case('duplicate_server_ids', global_section(sonarr='1,1') + INFLUX + sonarr(1),
     notes='Python created the same server twice (double writes); Go keeps one.')
case('negative_and_zero', global_section(sonarr='1') + INFLUX
     + sonarr(1).replace('missing_days = 7', 'missing_days = 0').replace('future_days = 1', 'future_days = -1'),
     notes='Zero and negative day counts disable the calendar jobs.')


def main():
    for name, (ini, env, notes) in CASES.items():
        folder = OUT / name
        if folder.exists():
            for f in ('varken.ini', 'env.json'):
                if (folder / f).exists():
                    (folder / f).unlink()
        folder.mkdir(parents=True, exist_ok=True)
        if ini is not None:
            (folder / 'varken.ini').write_text(ini)
        if env:
            (folder / 'env.json').write_text(json.dumps(env, indent=2, sort_keys=True) + '\n')
        (folder / 'notes.txt').write_text(notes + '\n')
    print(f'{len(CASES)} config cases written')


if __name__ == '__main__':
    main()
