"""Tautulli fixtures (API v2). Every call is GET /api/v2?cmd=<command>; routes match on cmd.

IP addresses refer to testdata/geoip/GeoLite2-City-Test.mmdb (built by tools/mkgeoip):
203.0.113.0/24 Springfield/IL, 198.51.100.0/24 London (two subdivisions), 192.0.2.0/24 no city or subdivision,
1.1.1.0/24 Research/VIC, 8.8.8.0/24 city named only in German, 9.9.9.0/24 lat/long 0, 2001:db8::/32 Sixton/CA.
Anything else (e.g. 10.x, 100.64.x) is not in the database.
"""
import copy

from common import case, route

API = '/api/v2'
SERVER = {'fallback_ip': '1.1.1.1'}
NOW_TS = 1795019400  # 2026-11-18T16:30:00Z, the common NOW


def ok(data):
    return {'response': {'result': 'success', 'message': None, 'data': data}}


def cmd(name, body=None, *, status=200, text=None, times=None, **query):
    match = {'cmd': name}
    match.update({k: v for k, v in query.items()})
    return route(API, body, status=status, query=match, text=text, times=times)


BASE_SESSION = {
    'session_id': 'sess-movie', 'session_key': '12', 'username': 'alice', 'friendly_name': 'Alice',
    'user': 'alice', 'full_title': 'The Movie', 'ip_address': '203.0.113.5', 'ip_address_public': '203.0.113.5',
    'product': 'Plex Web', 'platform': 'Chrome', 'product_version': '4.100.1', 'transcode_decision': 'transcode',
    'stream_video_decision': 'transcode', 'stream_video_resolution': '1080', 'stream_video_full_resolution': '1080p',
    'container': 'mkv', 'state': 'playing', 'transcode_hw_decoding': 1, 'transcode_hw_encoding': 1,
    'media_type': 'movie', 'audio_codec': 'eac3', 'audio_profile': '', 'stream_audio_codec': 'aac',
    'quality_profile': 'Original', 'relayed': 0, 'secure': 1, 'location': 'wan', 'bandwidth': '10000',
    'stream_bitrate': '8000', 'bitrate': '20000', 'progress_percent': '45', 'view_offset': '1234567',
    'duration': '7200000', 'transcode_speed': '1.5', 'transcode_throttled': 0, 'transcode_hw_full_pipeline': 1,
    'subtitle_decision': 'burn', 'stream_video_dynamic_range': 'HDR10', 'library_name': 'Movies',
    'audio_atmos': 1, 'video_dovi_present': 0, 'some_new_field_from_a_newer_tautulli': {'x': 1},
}


def session(**changes):
    s = copy.deepcopy(BASE_SESSION)
    s.update(changes)
    return s


SESSIONS = [
    session(),
    # LAN stream: the public address is not in the database, so the fallback_ip is used.
    session(session_id='sess-ep', session_key=13, username='bob', friendly_name='Bob', full_title='Show - S01E02',
            ip_address='192.168.1.20', ip_address_public='10.0.0.5', transcode_decision='direct play',
            stream_video_decision='direct play', stream_video_resolution='sd', state='paused', platform='osx',
            product='Plex for Mac', product_version='1.2.3-abcdef', media_type='episode', location='lan',
            transcode_hw_decoding=0, transcode_hw_encoding=0, audio_profile='lc', relayed=1, secure=0,
            progress_percent='', view_offset=None, duration='', transcode_speed=None, library_name='TV Shows',
            subtitle_decision=None, stream_video_dynamic_range=None),
    # Music: no video resolution, video decision '' -> Music, copy -> direct stream.
    session(session_id='sess-track', session_key='14', username='carol', friendly_name='Carol',
            full_title='Artist - Track', ip_address_public='198.51.100.7', transcode_decision='copy',
            stream_video_decision='', stream_video_resolution='', stream_video_full_resolution='', container='flac',
            state='buffering', platform='windows', product_version='1.50.0-xyz', media_type='track',
            audio_codec='flac', stream_audio_codec='flac', location='cellular', transcode_hw_decoding=0,
            transcode_hw_encoding=0, audio_atmos=None, video_dovi_present=None, library_name='Music'),
    # Roku, 720 with no full resolution -> 720p, a state the tables do not map, lat/long of 0.
    session(session_id='sess-roku', session_key='15', ip_address_public='9.9.9.9', platform='Roku',
            product_version='9.4.0-4200', stream_video_resolution='720', stream_video_full_resolution='',
            stream_video_decision='copy', state='stopped', transcode_hw_encoding=0, transcode_hw_decoding='1',
            transcode_throttled=1, transcode_speed='throttled', location='wan', view_offset='-1500'),
    # 4k, no city or subdivision in the database.
    session(session_id='sess-4k', session_key='16', ip_address_public='192.0.2.1', stream_video_resolution='4k',
            transcode_decision='direct play', stream_video_decision='direct play', transcode_hw_encoding=0,
            transcode_hw_decoding=0, video_dovi_present=1, location='lan'),
    # City only named in German -> alien; IPv6 address.
    session(session_id='sess-de', session_key='17', ip_address_public='8.8.8.8', stream_video_resolution='SD'),
    session(session_id='sess-v6', session_key='18', ip_address_public='2001:db8::5', stream_video_resolution='480',
            stream_video_full_resolution='480p', progress_percent='99.5'),
]


def activity(sessions, **counts):
    data = {'stream_count': str(len(sessions)), 'stream_count_direct_play': 2, 'stream_count_direct_stream': 1,
            'stream_count_transcode': 4, 'total_bandwidth': 54000, 'lan_bandwidth': 9000, 'wan_bandwidth': 45000,
            'sessions': sessions}
    data.update(counts)
    return ok(data)


case('tautulli', 'activity_sessions', 'get_activity', [cmd('get_activity', activity(SESSIONS))], server=SERVER,
     description='every mapping table and every GeoIP outcome that resolves')
case('tautulli', 'activity_empty', 'get_activity',
     [cmd('get_activity', activity([], stream_count='0', total_bandwidth=0, lan_bandwidth=0, wan_bandwidth=0,
                                   stream_count_direct_play=0, stream_count_direct_stream=0,
                                   stream_count_transcode=0))], server=SERVER)
case('tautulli', 'activity_public_ip_lookup', 'get_activity',
     [cmd('get_activity', activity([session(ip_address_public='10.1.1.1'),
                                    session(session_id='second', ip_address_public='')]))],
     server={'fallback_ip': '100.64.0.1'}, public_ip='203.0.113.77',
     description='LAN address and a fallback_ip that are not in the database: the public IP is looked up once')
case('tautulli', 'activity_public_ip_lookup_fails', 'get_activity',
     [cmd('get_activity', activity([session(ip_address_public='10.1.1.1')]))],
     server={'fallback_ip': '100.64.0.1'}, public_ip_error=True,
     fix='Python looked the fallback_ip up again after the public-IP lookup failed, raised AddressNotFoundError '
         'and lost the whole run; the stream is now written with the unknown-location defaults')
case('tautulli', 'activity_header_auth_rejected', 'get_activity',
     [cmd('get_activity', activity(SESSIONS[:1]), apikey='testkey'),
      cmd('get_activity', status=401, text='{"response": {"result": "error"}}')], server=SERVER,
     description='Tautulli older than 2.18 rejects the X-Api-Key header: retried with the apikey parameter')
case('tautulli', 'activity_error_payload', 'get_activity',
     [cmd('get_activity', {'response': {'result': 'error', 'message': 'Invalid apikey', 'data': {}}})],
     server=SERVER, description='an error answered with HTTP 200 writes nothing')
case('tautulli', 'activity_unreachable', 'get_activity', [cmd('get_activity', status=500, text='oops')],
     server=SERVER)
case('tautulli', 'activity_not_json', 'get_activity', [cmd('get_activity', text='<html>login</html>')],
     server=SERVER)
case('tautulli', 'activity_blank_counts', 'get_activity',
     [cmd('get_activity', activity(SESSIONS[:2], stream_count='', wan_bandwidth=None))], server=SERVER,
     fix='Python raised ValueError on int("") and TypeError on int(None) for the stream counts and lost the whole '
         'run; blank counts are now written as 0')
case('tautulli', 'activity_missing_fields', 'get_activity',
     [cmd('get_activity', activity([
         {'session_id': 'bare', 'ip_address_public': '203.0.113.5'},
         session(session_id='no-ip', ip_address_public=None, media_type=None, state=None),
         'not a session',
     ]))], server=SERVER,
     fix='Python crashed on the first missing string field (None.lower(), None.title(), None.split()) and on a '
         'session that is not an object, losing the whole run; missing strings are now treated as empty, '
         'non-object sessions are skipped and a missing address goes through the GeoIP fallback')
case('tautulli', 'activity_no_geoip_database', 'get_activity', [cmd('get_activity', activity(SESSIONS[:2]))],
     server=SERVER,
     fix='Python exited at startup when the GeoLite2 database could not be downloaded; Tautulli now runs without '
         'it and writes every stream with the unknown-location defaults')

LIBRARIES = [
    {'section_id': 1, 'section_name': 'Movies', 'section_type': 'movie', 'count': '1200', 'parent_count': '',
     'child_count': ''},
    {'section_id': '2', 'section_name': 'TV Shows', 'section_type': 'show', 'count': '150', 'parent_count': '600',
     'child_count': '9000'},
    {'section_id': 3, 'section_name': 'Music', 'section_type': 'artist', 'count': 80, 'parent_count': 300,
     'child_count': 4000.0},
    {'section_name': 'Photos', 'section_type': 'photo', 'count': '10'},
    {'section_id': None, 'section_name': 'Odd', 'section_type': 'movie', 'count': True},
]
case('tautulli', 'stats_libraries', 'get_stats', [cmd('get_libraries', ok(LIBRARIES))], server=SERVER)
case('tautulli', 'stats_empty', 'get_stats', [cmd('get_libraries', ok([]))], server=SERVER)
case('tautulli', 'stats_unreachable', 'get_stats', [cmd('get_libraries', status=404, text='nope')], server=SERVER)
case('tautulli', 'stats_blank_count', 'get_stats',
     [cmd('get_libraries', ok([LIBRARIES[0], {'section_id': 5, 'section_name': 'New', 'section_type': 'show',
                                             'count': '', 'parent_count': '3', 'child_count': None}]))],
     server=SERVER,
     fix='Python raised ValueError on int("") for a library count and lost every library; unparseable counts are '
         'now left out of that point')


def server_routes(history_rows, **overrides):
    routes = {
        'server_status': cmd('server_status', ok({'result': 'success', 'connected': True})),
        'get_server_info': cmd('get_server_info', ok({'pms_name': 'Plex', 'pms_version': '1.41.0.1234',
                                                      'pms_platform': 'Linux', 'pms_plexpass': '1'})),
        'get_pms_update': cmd('get_pms_update', ok({'update_available': True, 'version': '1.42.0.1'})),
        'get_tautulli_info': cmd('get_tautulli_info', ok({'tautulli_version': 'v2.18.1'})),
        'get_history': cmd('get_history', ok({'recordsFiltered': 1, 'data': history_rows}), length=1),
    }
    routes.update(overrides)
    return [r for r in routes.values() if r is not None]


case('tautulli', 'server_status', 'get_server_status',
     server_routes([{'id': 9, 'stopped': NOW_TS - 7200, 'date': NOW_TS - 9000}]), server=SERVER)
case('tautulli', 'server_status_date_only', 'get_server_status',
     server_routes([{'id': 9, 'stopped': '', 'date': str(NOW_TS - 100)}],
                   get_pms_update=cmd('get_pms_update', ok({'update_available': False})),
                   server_status=cmd('server_status', ok({'connected': False}))), server=SERVER)
case('tautulli', 'server_status_partial', 'get_server_status',
     server_routes([], get_server_info=None, get_tautulli_info=cmd('get_tautulli_info', status=500),
                   get_pms_update=cmd('get_pms_update', ok(None))), server=SERVER)
case('tautulli', 'server_status_unreachable', 'get_server_status', [], server=SERVER)
case('tautulli', 'server_status_header_rejected', 'get_server_status',
     [cmd('server_status', status=401, apikey=None)] + server_routes([], **{
         k: cmd(k, ok({'connected': True}), apikey='testkey') for k in ['server_status']}), server=SERVER,
     description='after one 401 every later call in the run uses the apikey parameter')


def play(row_id, stopped, **extra):
    row = {'id': row_id, 'stopped': stopped, 'started': NOW_TS - 9000, 'friendly_name': 'Alice', 'user': 'alice',
           'platform': 'Chrome', 'product': 'Plex Web', 'media_type': 'movie', 'transcode_decision': 'transcode',
           'location': 'wan', 'full_title': f'Title {row_id}', 'play_duration': 3000, 'duration': 3600,
           'paused_counter': 60, 'percent_complete': 85, 'watched_status': 1}
    row.update(extra)
    return row


HISTORY_LIBS = ok([{'section_id': 1, 'section_name': 'Movies'}, {'section_id': '2', 'section_name': 'TV Shows'},
                   {'section_id': None, 'section_name': None}])
case('tautulli', 'history_plays', 'get_history_plays', [
    cmd('get_libraries', HISTORY_LIBS),
    cmd('get_history', ok({'recordsFiltered': 3, 'data': [
        play(101, NOW_TS - 4000),
        play(1234567, NOW_TS - 5000, platform='osx', friendly_name='', user='bob', play_duration='',
             duration='1800', watched_status=0.5, location=None, full_title=None),
    ]}), section_id='1', start='0'),
    cmd('get_history', ok({'recordsFiltered': 3, 'data': [
        play(103, NOW_TS - 6000, platform='windows', play_duration=None, watched_status='1', paused_counter=''),
        play(0, NOW_TS - 6100), play(104, None), play(105, ''),
    ]}), section_id='1', start='2'),
    cmd('get_history', ok({'recordsFiltered': 1, 'data': [play(201, NOW_TS - 100, media_type='episode')]}),
        section_id='2', start='0'),
    cmd('get_history', {'response': {'result': 'error', 'message': 'bad section'}}),
], args={'days': 2}, server=SERVER)
case('tautulli', 'history_plays_no_libraries', 'get_history_plays', [cmd('get_libraries', ok([]))],
     args={'days': 2}, server=SERVER)
case('tautulli', 'history_plays_empty', 'get_history_plays', [
    cmd('get_libraries', ok([{'section_id': 1, 'section_name': 'Movies'}])),
    cmd('get_history', ok({'recordsFiltered': 0, 'data': []})),
], args={'days': 364}, server=SERVER)

case('tautulli', 'concurrent_streams', 'get_concurrent_streams', [cmd('get_concurrent_streams_by_stream_type', ok({
    'categories': ['2026-11-16', '2026-11-17', '2026-1-5', 'bad', 5, '2026-02-30'],
    'series': [{'name': 'Direct Play', 'data': [1, 2, 3, 4, 5, 6]},
               {'name': 'Transcode', 'data': [0, None, '2']},
               {'data': [7]}, {'name': 'Empty'}],
}), time_range='30')], server=SERVER)
case('tautulli', 'concurrent_streams_error', 'get_concurrent_streams',
     [cmd('get_concurrent_streams_by_stream_type', {'response': {'result': 'error', 'data': None}})], server=SERVER)

case('tautulli', 'users', 'get_users', [cmd('get_users_table', ok({'data': [
    {'friendly_name': 'Alice', 'username': 'alice', 'plays': 120, 'duration': 360000, 'last_seen': NOW_TS - 50,
     'is_active': 1},
    {'friendly_name': '', 'username': 'bob', 'plays': '5', 'duration': None, 'last_seen': None, 'is_active': 0},
    {},
    {'username': 'dave', 'last_seen': '1795000000.9', 'plays': 'x'},
]}), length='100')], server=SERVER)
case('tautulli', 'users_empty', 'get_users', [cmd('get_users_table', ok({'data': []}))], server=SERVER)

case('tautulli', 'library_usage', 'get_library_usage', [cmd('get_libraries_table', ok({'data': [
    {'section_name': 'Movies', 'section_id': 1, 'plays': 10, 'duration': 100, 'count': 50,
     'last_accessed': NOW_TS - 10, 'is_active': 1},
    {'section_name': 'Old', 'section_id': 2, 'is_active': 0},
    {'section_id': None, 'is_active': '1'},
    {'section_name': 'Music', 'is_active': 1, 'count': '7', 'last_accessed': 0},
]}), length='50')], server=SERVER)

case('tautulli', 'recently_added', 'get_recently_added', [cmd('get_recently_added', ok({'recently_added': [
    {'library_name': 'Movies', 'added_at': str(NOW_TS - 3600)},
    {'library_name': 'Movies', 'added_at': NOW_TS - 3 * 86400},
    {'library_name': 'TV Shows', 'added_at': NOW_TS - 10 * 86400},
    {'added_at': NOW_TS + 100},
    {'library_name': 'TV Shows', 'added_at': 'bad'},
    {'library_name': 'Movies', 'added_at': NOW_TS - 86400},
]}), count='50')], server=SERVER)
case('tautulli', 'recently_added_none', 'get_recently_added',
     [cmd('get_recently_added', ok({'recently_added': []}))], server=SERVER)
