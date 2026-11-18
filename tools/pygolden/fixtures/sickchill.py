"""SickChill fixtures: /api/<key>/?cmd=future."""
import copy

from common import case, route

S = 'sickchill'
PATH = '/api/testkey'
QUERY = {'cmd': 'future', 'paused': 1, 'type': 'missed|today|soon|later|snatched', 'limit': 1000}


def show(name, season, episode, ep_name='Episode', indexerid=100, airdate='2026-11-18', **extra):
    s = {'airdate': airdate, 'airs': 'Wednesday 9:00 PM', 'ep_name': ep_name, 'ep_plot': 'Plot.', 'episode': episode,
         'indexerid': indexerid, 'network': 'HBO', 'paused': 0, 'quality': 'HD1080p', 'season': season,
         'show_name': name, 'show_status': 'Continuing', 'tvdbid': indexerid, 'weekday': 3}
    s.update(extra)
    return s


DATA = {'data': {
    'missed': [show('Missed Show', 1, 2, 'Two', 11, '2026-11-10'), show('Other Missed', 10, 12, 'Twelve', 12)],
    'today': [show('Today Show', 3, 4, "It's Today", 13)],
    'soon': [show('Soon Show', '2', '5', 'String Numbers', 14, '2026-11-20', newfield='ignored')],
    'later': [show('Later Show', 2.0, True, 'Float Season, Bool Episode', 15, '2026-12-25')],
    'snatched': [show('Snatched Show', 1, 1, 'Snatched', 16)],
    'future': [show('Future Show', 5, 0, 'Zero Episode', 17)],
    'unknown': [show('Unknown Section', 1, 1, 'Unknown', 18)],
}, 'message': '', 'result': 'success'}

case(S, 'missing_happy', 'get_missing', [route(PATH, DATA, query=QUERY)], server={'get_missing': True},
     description='Every section: snatched and unknown sections are logged and skipped per show; numbers as '
                 'strings, floats and bools; unknown keys ignored.')
case(S, 'missing_empty', 'get_missing', [route(PATH, {'data': {}, 'message': '', 'result': 'success'},
                                               query=QUERY)])
case(S, 'missing_empty_sections', 'get_missing', [route(PATH, {'data': {'missed': [], 'today': []},
                                                               'result': 'success'}, query=QUERY)])
case(S, 'missing_error', 'get_missing', [route(PATH, None, status=500, text='error', query=QUERY)])
case(S, 'missing_bad_key', 'get_missing', [route(PATH, {'result': 'denied', 'message': 'Invalid API key'},
                                                 query=QUERY)],
     description='No "data": Python crashed with KeyError, nothing is written either way.')

null_season = copy.deepcopy(DATA)
null_season['data']['missed'][0]['season'] = None
null_season['data']['today'][0]['episode'] = None
case(S, 'missing_null_season', 'get_missing', [route(PATH, null_season, query=QUERY)],
     fix='Python crashed with TypeError formatting a null season/episode and wrote nothing; that episode is '
         'skipped.')

bad_entry = copy.deepcopy(DATA)
bad_entry['data']['missed'].insert(1, 'not an episode')
bad_entry['data']['soon'].append(None)
case(S, 'missing_bad_entry', 'get_missing', [route(PATH, bad_entry, query=QUERY)],
     fix='Python dropped the whole job (TypeError building SickChillTVShow) when one entry was not an object; '
         'only that entry is skipped.')
