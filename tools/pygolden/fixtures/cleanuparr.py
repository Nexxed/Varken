"""Cleanuparr fixtures: /api/v2/stats, /api/v2/stats/timeline, /api/strikes and /api/events."""
import copy

from common import case, route

S = 'cleanuparr'
METRICS = ('strikesIssued', 'recovered', 'removed', 'malwareBlocked', 'events')

STATS = {
    'timeframeHours': 24,
    'events': {'total': 12, 'byType': {'StalledStrike': 4, 'QueueItemDeleted': 3, 'DownloadCleaned': 5},
               'bySeverity': {'Information': 8, 'Important': 4}},
    'strikes': {'total': 6, 'recovered': 2, 'byType': {'Stalled': 3, 'SlowSpeed': 2, 'FailedImport': 1}},
    'removals': {'total': 3, 'byReason': {'Stalled': 2, 'FailedImport': 1}},
    'cleaned': {'total': 5, 'byReason': {'MaxRatioReached': 4, 'MaxSeedTimeReached': 1}},
    'searches': {'total': 7, 'completed': 5, 'failed': 1, 'grabbed': 3,
                 'byReason': {'Replacement': 4, 'Proactive': 3}},
    'jobs': {'total': 40, 'completed': 38, 'failed': 2, 'byType': {
        'QueueCleaner': {'total': 20, 'completed': 19, 'failed': 1, 'lastRunAt': '2026-11-18T16:25:00Z',
                         'nextRunAt': '2026-11-18T16:30:00Z'},
        'MalwareBlocker': {'total': 12, 'completed': 12, 'failed': 0, 'lastRunAt': '2026-11-18T16:00:00.1234567Z',
                           'nextRunAt': None},
        'DownloadCleaner': {'total': 8, 'completed': 7, 'failed': 1}}},
    'health': {
        'downloadClients': [
            {'id': 'a1', 'name': 'qBittorrent', 'type': 'qBittorrent', 'isHealthy': True,
             'lastChecked': '2026-11-18T16:29:00Z', 'errorMessage': None, 'responseTimeMs': 12.5},
            {'id': 'a2', 'name': 'SABnzbd', 'type': 'SABnzbd', 'isHealthy': False,
             'lastChecked': '2026-11-18T16:29:01Z', 'errorMessage': 'Connection refused', 'responseTimeMs': 31}],
        'arrInstances': [
            {'name': 'Sonarr', 'type': 'Sonarr', 'isHealthy': True, 'lastChecked': '2026-11-18T16:28:00Z',
             'responseTimeMs': None},
            {'name': 'Radarr 4K', 'type': 'Radarr', 'isHealthy': False, 'lastChecked': '2026-11-18T16:28:00Z',
             'errorMessage': 'Unauthorized: check the API key', 'responseTimeMs': 250.75}]},
}


def stats_route(body, *, dry='false', hours=24, status=200, text=None):
    return route('/api/v2/stats', body, query={'hours': hours, 'includeDryRun': dry}, status=status, text=text)


case(S, 'stats_happy', 'get_stats', [stats_route(STATS)], server={'get_stats': True})
case(S, 'stats_include_dry_run', 'get_stats', [stats_route(STATS, dry='true', hours=72)],
     server={'include_dry_run': True, 'stats_hours': 72})
case(S, 'stats_minimal', 'get_stats', [stats_route({'timeframeHours': 24})],
     description='Sections missing: every count falls back to 0.')
case(S, 'stats_no_timeframe', 'get_stats', [stats_route({'events': {'total': 1}})],
     description='timeframeHours missing: falls back to the configured stats_hours.')

odd = copy.deepcopy(STATS)
odd['timeframeHours'] = None
odd['strikes']['byType'] = {'Stalled': '3', 'SlowSpeed': 2.9, 'FailedImport': True}
odd['events']['total'] = '12'
odd['searches']['grabbed'] = 3.7
odd['jobs']['byType']['QueueCleaner']['lastRunAt'] = None
odd['jobs']['byType']['QueueCleaner']['nextRunAt'] = ''
odd['health']['downloadClients'][0]['responseTimeMs'] = '15'
odd['health']['downloadClients'][1]['name'] = None
odd['health']['arrInstances'][0]['type'] = ''
odd['health']['arrInstances'][1]['lastChecked'] = 1700000000
odd['removals']['byReason'] = []
odd['cleaned'] = None
case(S, 'stats_odd_types', 'get_stats', [stats_route(odd)],
     description='Numeric strings, floats and bools as counts, nulls and wrong container types.')

nulls = copy.deepcopy(STATS)
nulls['events']['total'] = None
nulls['strikes']['byType']['Stalled'] = None
nulls['jobs']['byType']['QueueCleaner']['failed'] = None
case(S, 'stats_null_counts', 'get_stats', [stats_route(nulls)],
     fix='Python crashed with TypeError on int(None) for a null count and wrote nothing; null counts are 0.')

case(S, 'stats_upstream_error', 'get_stats', [stats_route(None, status=500, text='Internal Server Error')])
case(S, 'stats_unauthorized', 'get_stats', [stats_route(None, status=401, text='Unauthorized')])
case(S, 'stats_not_json', 'get_stats', [stats_route(None, text='<html>login</html>')])
case(S, 'stats_not_object', 'get_stats', [stats_route([1, 2])],
     description='A JSON array instead of an object: Python crashed on .get, nothing is written either way.')


def bucket(hour, count, offset='Z'):
    return {'date': f'2026-11-18T{hour:02d}:00:00{offset}', 'count': count}


def timeline_routes(per_metric, *, hours=48, dry='false'):
    routes = []
    for metric in METRICS:
        body = per_metric.get(metric, [])
        if isinstance(body, dict) and body.get('__status'):
            routes.append(route('/api/v2/stats/timeline', None, status=body['__status'], text='error',
                                query={'metric': metric, 'hours': hours, 'bucket': 'hour', 'includeDryRun': dry}))
        else:
            routes.append(route('/api/v2/stats/timeline', body,
                                query={'metric': metric, 'hours': hours, 'bucket': 'hour', 'includeDryRun': dry}))
    return routes


case(S, 'timeline_happy', 'get_timeline', timeline_routes({
    'strikesIssued': [bucket(14, 2), bucket(15, 0), bucket(16, 5)],
    'recovered': [bucket(15, 1, '+00:00'), {'date': '2026-11-18T17:00:00+02:00', 'count': 3}],
    'removed': [{'date': '2026-11-18T16:00:00.1234567Z', 'count': 1}, {'date': '2026-11-18T16:00:00', 'count': 4}],
    'malwareBlocked': [],
    'events': [bucket(13, 9), bucket(14, 11), bucket(15, 7), bucket(16, 2)],
}))
case(S, 'timeline_include_dry_run', 'get_timeline', timeline_routes({
    'events': [bucket(16, 1)]}, hours=6, dry='true'), server={'include_dry_run': True, 'timeline_hours': 6})
case(S, 'timeline_partial', 'get_timeline', timeline_routes({
    'strikesIssued': {'__status': 500},
    'recovered': {'not': 'a list'},
    'removed': [{'count': 3}, {'date': None, 'count': 1}, {'date': '', 'count': 1}, {'date': '2026-11-18T15:00:00Z'},
                {'date': '2026-11-18T14:00:00Z', 'count': '6'}, {'date': '2026-11-18T13:00:00Z', 'count': 2.5}],
    'malwareBlocked': [{'date': 1795010400000000000, 'count': 1}],
    'events': {'__status': 404},
}), description='Errors, a non-list body, buckets without dates, string/float counts and an epoch-ns date.')
case(S, 'timeline_empty', 'get_timeline', timeline_routes({}))
case(S, 'timeline_null_count', 'get_timeline', timeline_routes({
    'strikesIssued': [bucket(14, None), bucket(15, 2)], 'events': [bucket(15, 1)]}),
    fix='Python crashed with TypeError on int(None) for a null bucket count and wrote nothing; it counts as 0.')
case(S, 'timeline_bad_date', 'get_timeline', timeline_routes({
    'strikesIssued': [{'date': 'yesterday', 'count': 1}, bucket(15, 2)], 'events': [bucket(15, 1)]}),
    fix='Python failed the whole write (ValueError from the InfluxDB client) on an unparseable bucket date; '
        'only that bucket is skipped.')

LONG = 'L' * 499 + 'é' + 'tail'
EXACT = 'E' * 500


def strike(title, *, status=None, dry=None, by_type=None, total=None, download_id='abc', item_id=None, **extra):
    item = {'downloadId': download_id, 'title': title, 'totalStrikes': total, 'strikesByType': by_type or {},
            'firstStrikeAt': '2026-11-17T08:00:00Z', 'latestStrikeAt': '2026-11-18T16:00:00Z',
            'isReturning': False, 'isMarkedForRemoval': status == 'marked', 'isRemoved': status == 'removed',
            'strikes': [{'type': 'Stalled', 'isDryRun': d} for d in (dry or [False])]}
    if item_id is not None:
        item['downloadItemId'] = item_id
    item.update(extra)
    return item


STRIKES = {'items': [
    strike('Show.S01E01.1080p', by_type={'Stalled': 2, 'FailedImport': 1, 'DownloadingMetadata': 1}, total=4,
           download_id='HASH1'),
    strike('Movie.2024.2160p', status='marked', by_type={'SlowSpeed': 3}, total=3, download_id='HASH2',
           isReturning=True),
    strike('Removed.Thing', status='removed', total=5, download_id=None, item_id='item-77',
           by_type={'Stalled': 5}),
    strike('Dry.Run.Only', dry=[True, True], total=2, download_id='HASH4'),
    strike('Mixed.Dry', dry=[True, False], total=2, download_id='HASH5', by_type={'b': 1, 'a': 1}),
    strike(LONG, total=None, download_id='', item_id=None, by_type=None),
    strike(EXACT, total='7', download_id='HASH7', strikes=None),
    strike(None, total=1.9, download_id='HASH8', strikes=[], firstStrikeAt=None, latestStrikeAt=None),
], 'page': 1, 'pageSize': 50, 'totalCount': 8}

case(S, 'strikes_happy', 'get_strikes', [route('/api/strikes', STRIKES, query={'page': 1, 'pageSize': 50})],
     description='Statuses, dry-run filtering, id fallback, sorted strike types, 500-code-point truncation.')
case(S, 'strikes_include_dry_run', 'get_strikes',
     [route('/api/strikes', STRIKES, query={'page': 1, 'pageSize': 10})],
     server={'include_dry_run': True, 'num_strikes_to_fetch': 10})
case(S, 'strikes_empty', 'get_strikes', [route('/api/strikes', {'items': [], 'totalCount': 0})])
case(S, 'strikes_error', 'get_strikes', [route('/api/strikes', None, status=404, text='Not Found')])
case(S, 'strikes_no_items_key', 'get_strikes', [route('/api/strikes', {'page': 1})])


def event(eid, ts, etype='StalledStrike', severity='Important', **extra):
    e = {'id': eid, 'timestamp': ts, 'eventType': etype, 'severity': severity, 'message': f'Event {eid}',
         'itemTitle': f'Item {eid}', 'isDryRun': False}
    e.update(extra)
    return e


EVENTS = {'items': [
    event('e1', '2026-11-18T16:20:00Z', deleteReason='Stalled', cleanReason='ignored'),
    event('e2', '2026-11-18T16:21:00.5+01:00', 'DownloadCleaned', 'Information', cleanReason='MaxRatioReached'),
    event('e3', '2026-11-18T16:22:00Z', 'FailedImportStrike',
          failedImportReasons=['No files found', 'Sample only']),
    event('e4', None),
    event('e5', '', message='no timestamp'),
    event('e6', '2026-11-18T16:23:00Z', isDryRun=True),
    event('e7', '2026-11-18T16:24:00Z', message=LONG, itemTitle=EXACT),
    event('e8', '2026-11-18T16:25:00Z', message='first of a pair'),
    event('e9', '2026-11-18T16:25:00Z', message='second of a pair (same type, severity, time)'),
    event(10, '2026-11-18T16:26:00.1234567Z', eventType=None, severity=None, message=None, itemTitle=None,
          failedImportReasons=[]),
], 'page': 1, 'pageSize': 50, 'totalCount': 10}

case(S, 'events_happy', 'get_events', [route('/api/events', EVENTS,
                                             query={'page': 1, 'pageSize': 50, 'severity': 'Important'})],
     description='Reason precedence, skipped/dry-run events, truncation, duplicate timestamps keep Python order.')
case(S, 'events_all_severities', 'get_events',
     [route('/api/events', EVENTS, query={'page': 1, 'pageSize': 5, 'severity': None})],
     server={'events_severity': '', 'num_events_to_fetch': 5, 'include_dry_run': True})
case(S, 'events_empty', 'get_events', [route('/api/events', {'items': []})])
case(S, 'events_error', 'get_events', [route('/api/events', None, status=503, text='busy')])
case(S, 'events_non_string_reasons', 'get_events', [route('/api/events', {'items': [
    event('n1', '2026-11-18T16:20:00Z', failedImportReasons=['ok', 5, None, 'x'])]})],
    fix='Python crashed with TypeError joining a non-string failedImportReasons entry; numbers are printed and '
        'nulls left out.')
