"""Fixtures for the Sonarr (API v3), Radarr (API v3) and Lidarr (API v1) collectors.

Shapes follow Sonarr v4, Radarr v5 and Lidarr v2 responses, trimmed to realistic subsets. The frozen clock is
common.NOW (2026-11-18 16:30:00.123456 UTC, 10:30 in America/Chicago), so "today" is 2026-11-18.
"""
from common import case, route

SONARR_SERVER = {'missing_days': 7, 'future_days': 1}
LIDARR_SERVER = {'missing_days': 30, 'future_days': 30}


# ---------------------------------------------------------------------------------------------------------- shared
def series(title='The Expanse', sid=1, **extra):
    s = {'title': title, 'sortTitle': (title or '').lower(), 'status': 'ended', 'overview': 'Space.', 'network': 'Prime',
         'images': [{'coverType': 'poster', 'url': '/poster.jpg'}], 'seasons': [{'seasonNumber': 1}],
         'year': 2015, 'path': f'/tv/{title}', 'qualityProfileId': 1, 'seasonFolder': True, 'monitored': True,
         'tvdbId': 280619 + sid, 'id': sid, 'added': '2020-01-01T00:00:00Z', 'ratings': {'votes': 10, 'value': 8.5},
         'statistics': {'seasonCount': 6, 'episodeFileCount': 62, 'episodeCount': 62, 'totalEpisodeCount': 62,
                        'sizeOnDisk': 130000000000, 'percentOfEpisodes': 100.0}, 'lastSearchTime': None}
    s.update(extra)
    return s


def episode(sid=1, season=1, number=1, title='Dulcinea', has_file=False, monitored=True,
            air='2026-11-15T02:00:00Z', with_series=True, **extra):
    e = {'seriesId': sid, 'tvdbId': 1000 + number, 'episodeFileId': 0, 'seasonNumber': season,
         'episodeNumber': number, 'title': title, 'airDate': air[:10], 'airDateUtc': air, 'runtime': 45,
         'overview': '...', 'hasFile': has_file, 'monitored': monitored, 'absoluteEpisodeNumber': number,
         'unverifiedSceneNumbering': False, 'grabbed': False, 'id': 5000 + number, 'finaleType': None,
         'lastSearchTime': '2026-11-16T00:00:00Z'}
    if with_series:
        e['series'] = series(sid=sid) if with_series is True else with_series
    e.update(extra)
    return e


def queue_item(i, title, **extra):
    item = {'id': i, 'title': title, 'size': 1073741824, 'sizeleft': 536870912, 'timeleft': '00:10:00',
            'estimatedCompletionTime': '2026-11-18T16:40:00Z', 'added': '2026-11-18T16:00:00Z',
            'status': 'downloading', 'trackedDownloadStatus': 'ok', 'trackedDownloadState': 'downloading',
            'statusMessages': [], 'downloadId': f'SABnzbd_nzo_{i}', 'protocol': 'usenet',
            'downloadClient': 'SABnzbd', 'downloadClientHasPostImportCategory': False, 'indexer': 'NZBgeek',
            'outputPath': f'/downloads/{title}', 'quality': {'quality': {'id': 4, 'name': 'HDTV-720p',
                                                                        'source': 'television', 'resolution': 720},
                                                            'revision': {'version': 1, 'real': 0}},
            'languages': [{'id': 1, 'name': 'English'}], 'customFormats': [], 'customFormatScore': 0}
    item.update(extra)
    return item


def page(records, total=None, page_no=1, size=250):
    return {'page': page_no, 'pageSize': size, 'sortKey': 'timeleft', 'sortDirection': 'ascending',
            'totalRecords': len(records) if total is None else total, 'records': records}


QUEUE_STATUS = {'totalCount': 3, 'count': 3, 'unknownCount': 1, 'errors': False, 'warnings': True,
                'unknownErrors': False, 'unknownWarnings': False}


def health_checks():
    return [
        {'source': 'IndexerStatusCheck', 'type': 'warning', 'message': 'Indexers unavailable due to failures: X',
         'wikiUrl': 'https://wiki.servarr.com/sonarr/system#indexers-are-unavailable-due-to-failures'},
        {'source': 'IndexerStatusCheck', 'type': 'warning', 'message': 'Second indexer warning', 'wikiUrl': None},
        {'source': 'DownloadClientCheck', 'type': 'error', 'message': 'Unable to communicate with SABnzbd',
         'wikiUrl': 'https://wiki.servarr.com/x'},
        {'source': 'UpdateCheck', 'type': 'notice', 'message': 'New update is available'},
        {'source': 'DownloadClientCheck', 'type': 'warning', 'message': 'Different level, same source'},
        {'type': 'Warning', 'message': 'No source: reads as Unknown, level lower-cased'},
        {'source': 'RootFolderCheck', 'message': 'No type: unknown level ' + 'x' * 600},
    ]


def stats_cases(service, prefix, history_events, base_server, title_key):
    """Health, wanted, history totals, disk, history events, tasks: shared by the three services."""
    p = prefix
    case(service, 'health', 'get_health', [route(f'{p}/health', health_checks())], server=base_server)
    case(service, 'health_empty', 'get_health', [route(f'{p}/health', [])], server=base_server)
    case(service, 'health_error', 'get_health', [route(f'{p}/health', {'message': 'boom'}, status=500)],
         server=base_server)
    case(service, 'health_not_a_list', 'get_health', [route(f'{p}/health', {'source': 'x'})], server=base_server)
    case(service, 'health_unauthorized', 'get_health', [route(f'{p}/health', {'error': 'Unauthorized'}, status=401)],
         server=base_server)

    wanted = [route(f'{p}/wanted/missing', page([], 12), query={'monitored': 'true'}),
              route(f'{p}/wanted/missing', page([], 3), query={'monitored': 'false'}),
              route(f'{p}/wanted/cutoff', page([], 7), query={'monitored': 'true'})]
    case(service, 'wanted', 'get_wanted', wanted, server=base_server)
    case(service, 'wanted_partial', 'get_wanted', [
        route(f'{p}/wanted/missing', page([], 12), query={'monitored': 'true'}),
        route(f'{p}/wanted/missing', {'page': 1}, query={'monitored': 'false'}),
        route(f'{p}/wanted/cutoff', 'x', status=500)], server=base_server,
        description='a count without totalRecords and a failing count are left out')
    case(service, 'wanted_all_fail', 'get_wanted', [route(f'{p}/wanted/missing', {}, status=503),
                                                    route(f'{p}/wanted/cutoff', {}, status=503)], server=base_server)
    case(service, 'wanted_types', 'get_wanted', [
        route(f'{p}/wanted/missing', {'totalRecords': '15'}, query={'monitored': 'true'}),
        route(f'{p}/wanted/missing', {'totalRecords': 4.9}, query={'monitored': 'false'}),
        route(f'{p}/wanted/cutoff', {'totalRecords': None}, query={'monitored': 'true'})], server=base_server)

    totals = [route(f'{p}/history', page([], 100 + n), query={'eventType': n}) for n in history_events]
    totals.append(route(f'{p}/blocklist', page([], 4)))
    case(service, 'history_totals', 'get_history_totals', totals, server=base_server)
    partial = [route(f'{p}/history', page([], 50), query={'eventType': history_events[0]}),
               route(f'{p}/history', 'down', status=500),
               route(f'{p}/blocklist', [], status=200)]
    case(service, 'history_totals_partial', 'get_history_totals', partial, server=base_server)
    case(service, 'history_totals_all_fail', 'get_history_totals',
         [route(f'{p}/history', 'x', status=500), route(f'{p}/blocklist', 'x', status=500)], server=base_server)

    disks = [{'path': '/', 'label': '', 'freeSpace': 20000000000, 'totalSpace': 60000000000},
             {'path': '/data', 'label': 'data', 'freeSpace': 1000000000000, 'totalSpace': 4000000000000},
             {'path': '/mnt/tv/', 'label': None, 'freeSpace': 5.0e11, 'totalSpace': 2e12},
             {'path': None, 'freeSpace': 1, 'totalSpace': 2}]
    roots = [{'path': '/data/tv', 'accessible': True, 'freeSpace': 1000000000000, 'unmappedFolders': [
                 {'name': 'Show A', 'path': '/data/tv/Show A'}, {'name': 'Show B', 'path': '/data/tv/Show B'}],
              'id': 1},
             {'path': '/mnt/tv/shows/', 'accessible': True, 'freeSpace': 5e11, 'unmappedFolders': [], 'id': 2},
             {'path': '/srv/media', 'accessible': False, 'freeSpace': 0, 'id': 3},
             {'path': '/data/movies', 'accessible': True, 'freeSpace': 1000000000000, 'totalSpace': 123, 'id': 4},
             {'path': '', 'accessible': True, 'id': 5}]
    case(service, 'disk', 'get_disk', [route(f'{p}/diskspace', disks), route(f'{p}/rootfolder', roots)],
         server=base_server)
    case(service, 'disk_rootfolder_only', 'get_disk', [route(f'{p}/diskspace', 'x', status=500),
                                                        route(f'{p}/rootfolder', roots)], server=base_server)
    case(service, 'disk_empty', 'get_disk', [route(f'{p}/diskspace', []), route(f'{p}/rootfolder', [])],
         server=base_server)
    case(service, 'disk_windows', 'get_disk', [
        route(f'{p}/diskspace', [{'path': 'C:\\', 'label': 'System', 'freeSpace': 1, 'totalSpace': 100},
                                 {'path': 'D:\\Media', 'label': 'Media', 'freeSpace': 5, 'totalSpace': 1000}]),
        route(f'{p}/rootfolder', [{'path': 'D:\\Media\\TV', 'accessible': True, 'freeSpace': 5},
                                  {'path': 'C:\\TV', 'accessible': True, 'freeSpace': 1}])], server=base_server)
    case(service, 'disk_prefix_boundary', 'get_disk', [
        route(f'{p}/diskspace', [{'path': '/', 'label': '', 'freeSpace': 1, 'totalSpace': 50},
                                 {'path': '/data', 'label': '', 'freeSpace': 2, 'totalSpace': 4000}]),
        route(f'{p}/rootfolder', [{'path': '/data2/movies', 'accessible': True, 'freeSpace': 3},
                                  {'path': '/data/movies', 'accessible': True, 'freeSpace': 2}])],
        server=base_server,
        fix='Python matched a root folder to a volume by plain string prefix, so /data2/movies took the size of '
            'the /data volume; the match now has to end on a path separator, so it gets the / volume.')

    since = {1: '2026-11-17T16:30:00Z', 7: '2026-11-11T16:30:00Z'}
    events = history_events_body(service)
    case(service, 'history_events', 'get_history_events',
         [route(f'{p}/history/since', events, query={'date': since[1]})], args={'days': 1}, server=base_server)
    case(service, 'history_events_backfill', 'get_history_events',
         [route(f'{p}/history/since', events[:2], query={'date': since[7]})], args={'days': 7}, server=base_server)
    case(service, 'history_events_empty', 'get_history_events', [route(f'{p}/history/since', [])],
         args={'days': 1}, server=base_server)
    case(service, 'history_events_error', 'get_history_events', [route(f'{p}/history/since', 'x', status=500)],
         args={'days': 1}, server=base_server)
    case(service, 'history_events_not_a_list', 'get_history_events',
         [route(f'{p}/history/since', {'records': events})], args={'days': 1}, server=base_server)

    tasks = [{'name': 'RssSync', 'taskName': 'RssSync', 'interval': 15, 'lastExecution': '2026-11-18T16:20:00.1234567Z',
              'lastStartTime': '2026-11-18T16:19:58Z', 'nextExecution': '2026-11-18T16:35:00.5Z',
              'lastDuration': '00:00:01.9876543', 'id': 1},
             {'name': 'Housekeeping', 'interval': 1440, 'lastExecution': '0001-01-01T00:00:00Z',
              'nextExecution': None, 'lastDuration': '1.02:03:04.5', 'id': 2},
             {'taskName': '', 'name': 'NameOnly', 'interval': '60', 'lastExecution': '2026-11-18T18:00:00Z',
              'lastDuration': None, 'id': 3},
             {'interval': 5, 'id': 4},
             {'taskName': 'Backup', 'interval': 10080.0, 'lastExecution': '2026-11-12T03:00:00+02:00',
              'nextExecution': '2026-11-19T03:00:00', 'lastDuration': 'nonsense', 'id': 5}]
    status = {'appName': service.title(), 'version': '4.0.11.2680', 'buildTime': '2026-10-01T00:00:00Z',
              'startTime': '2026-11-16T08:00:00.0000001Z', 'isDocker': True}
    case(service, 'tasks', 'get_tasks', [route(f'{p}/system/task', tasks), route(f'{p}/system/status', status)],
         server=base_server)
    case(service, 'tasks_status_fails', 'get_tasks',
         [route(f'{p}/system/task', tasks[:1]), route(f'{p}/system/status', 'x', status=500)], server=base_server)
    case(service, 'tasks_all_fail', 'get_tasks',
         [route(f'{p}/system/task', 'x', status=500), route(f'{p}/system/status', 'x', status=401)],
         server=base_server)
    case(service, 'tasks_status_only', 'get_tasks',
         [route(f'{p}/system/task', {}), route(f'{p}/system/status', {'version': None, 'startTime': 'bad'})],
         server=base_server)


def history_events_body(service):
    common_events = [
        {'id': 1234567, 'eventType': 'grabbed', 'date': '2026-11-18T10:00:00.1234567Z',
         'sourceTitle': 'Release.Name.S01E01.720p-GRP', 'quality': {'quality': {'name': 'HDTV-720p'}},
         'data': {'indexer': 'NZBgeek', 'protocol': 'Usenet', 'downloadClient': 'SABnzbd',
                  'downloadUrl': 'https://indexer/api?apikey=SECRET&id=1'}},
        {'id': 42, 'eventType': 'downloadFolderImported', 'date': '2026-11-18T11:00:00Z',
         'sourceTitle': 'Release.Name.S01E01.720p-GRP', 'quality': {'quality': {'name': 'HDTV-720p'}},
         'data': {'downloadClientName': 'qBittorrent', 'message': None, 'reason': 'Upgrade'}},
        {'id': 43, 'eventType': 'grabbed', 'date': '2026-11-18T12:00:00.5+02:00', 'sourceTitle': 'Torrent.Release',
         'quality': {}, 'data': {'protocol': 'torrent', 'indexer': None}},
        {'id': 44, 'eventType': 'downloadFailed', 'date': '2026-11-18T13:00:00Z', 'sourceTitle': None,
         'data': {'message': 'Failed: ' + 'y' * 400}},
        {'id': None, 'eventType': 'grabbed', 'date': '2026-11-18T13:00:00Z'},
        {'id': 45, 'eventType': 'grabbed', 'date': None},
        {'id': 46, 'eventType': None, 'date': '0001-01-01T00:00:00Z'},
        {'id': '47', 'eventType': None, 'date': '2026-11-18T14:00:00Z', 'quality': {'quality': {'name': None}}},
        {'id': 48.0, 'eventType': 'episodeFileDeleted', 'date': '2026-11-18T15:00:00Z', 'data': {}},
    ]
    owners = {
        'sonarr': [{'series': {'title': 'The Expanse', 'id': 1}}, {'series': {'title': 'Andor'}}, {}, {'series': None},
                   {}, {}, {}, {'series': {'title': 'Severance'}}, {}],
        'radarr': [{'movie': {'title': 'Dune', 'year': 2021}}, {'movie': {'title': 'Arrival', 'year': None}},
                   {'movie': {'title': '', 'year': 2016}}, {}, {}, {}, {}, {'movie': {'title': 'Up', 'year': 2009}},
                   {}],
        'lidarr': [{'artist': {'artistName': 'Daft Punk'}}, {'artist': {'artistName': None}}, {}, {'artist': None},
                   {}, {}, {}, {'artist': {'artistName': 'Björk'}}, {}],
    }[service]
    return [dict(e, **o) for e, o in zip(common_events, owners)]


# ---------------------------------------------------------------------------------------------------------- sonarr
def sonarr_cases():
    s = 'sonarr'
    p = '/api/v3'
    missing_q = {'start': '2026-11-11', 'end': '2026-11-18', 'includeSeries': 'True'}
    future_q = {'start': '2026-11-18', 'end': '2026-11-19', 'includeSeries': 'True'}
    eps = [
        episode(1, 1, 1, 'Dulcinea'),
        episode(1, 1, 2, 'The Big Empty', has_file=True),
        episode(1, 1, 3, 'Remember the Cant', monitored=False),
        episode(2, 1.0, 4, 'Title, with=specials and ünïcode', with_series=series('Andor', 2)),
        episode(3, '3', 10, 'String season', with_series=series('Severance', 3)),
        episode(4, 0, 0, 'Special', with_series=series('Show "Quoted"', 4), air='2026-11-17T23:59:59Z'),
        episode(5, 12, 105, 'Long numbers', with_series=series('Severance', 5)),
        episode(6, 2, 2, 'No air date', air='', airDateUtc=None),
    ]
    case(s, 'calendar_missing', 'get_calendar', [route(f'{p}/calendar/', eps, query=missing_q)],
         args={'query': 'Missing'}, server=SONARR_SERVER)
    case(s, 'calendar_future', 'get_calendar', [route(f'{p}/calendar/', eps[:5], query=future_q)],
         args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_utc_date', 'get_calendar', [route(f'{p}/calendar/', eps[:1],
                                                         query={'start': '2026-11-15', 'end': '2026-11-19'})],
         args={'query': 'Missing'}, server={'missing_days': 4, 'future_days': 1}, tz='Pacific/Kiritimati',
         now='2026-11-19T06:30:00.123456+14:00',
         description='the dates come from the local date, which is a day ahead of UTC in this zone (now has a '
                     'non-UTC offset so time-machine keeps TZ instead of switching to UTC)')
    case(s, 'calendar_empty', 'get_calendar', [route(f'{p}/calendar/', [])], args={'query': 'Missing'},
         server=SONARR_SERVER)
    case(s, 'calendar_nothing_missing', 'get_calendar', [route(f'{p}/calendar/', [eps[1], eps[2]])],
         args={'query': 'Missing'}, server=SONARR_SERVER)
    case(s, 'calendar_error', 'get_calendar', [route(f'{p}/calendar/', {'message': 'x'}, status=500)],
         args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_unauthorized', 'get_calendar', [route(f'{p}/calendar/', {'error': 'Unauthorized'}, status=401)],
         args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_invalid_json', 'get_calendar', [route(f'{p}/calendar/', text='<html>proxy error</html>')],
         args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_dict_body', 'get_calendar', [route(f'{p}/calendar/', {'records': eps})],
         args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_non_dict_items', 'get_calendar', [route(f'{p}/calendar/', [None, 'x', 5, eps[0]])],
         args={'query': 'Missing'}, server=SONARR_SERVER)
    case(s, 'calendar_series_without_title_unmonitored', 'get_calendar', [route(f'{p}/calendar/', [
        episode(9, 1, 1, 'Unmonitored, no series', monitored=False, with_series=False), eps[0]])],
        args={'query': 'Missing'}, server=SONARR_SERVER,
        description='Python only read the series title of episodes it kept, so this one did not fail the run')
    case(s, 'calendar_series_title_null', 'get_calendar', [route(f'{p}/calendar/', [
        episode(9, 1, 1, 'Null title', with_series=series(None, 9))])], args={'query': 'Future'}, server=SONARR_SERVER)
    case(s, 'calendar_null_season', 'get_calendar', [route(f'{p}/calendar/', [
        episode(1, None, 1, 'No season number'), eps[0]])], args={'query': 'Future'}, server=SONARR_SERVER,
        fix='Python raised TypeError formatting a null season number and wrote nothing; that episode is now '
            'skipped and the others are written.')
    case(s, 'calendar_no_series', 'get_calendar', [route(f'{p}/calendar/', [
        episode(1, 1, 7, 'No series object', with_series=False), eps[0]])], args={'query': 'Future'},
        server=SONARR_SERVER,
        fix='Python raised TypeError reading the title of a missing series and wrote nothing; that episode is '
            'now skipped and the others are written.')
    case(s, 'calendar_series_without_title_key', 'get_calendar', [route(f'{p}/calendar/', [
        episode(1, 1, 8, 'Series without title', with_series={'id': 1}), eps[0]])], args={'query': 'Future'},
        server=SONARR_SERVER,
        fix='Python raised KeyError on a series without a title and wrote nothing; that episode is now skipped.')

    def sq(i, title, ep, **extra):
        item = queue_item(i, title, seriesId=ep['seriesId'], episodeId=ep['id'], seasonNumber=ep['seasonNumber'],
                          episode={k: v for k, v in ep.items() if k != 'series'}, series=ep.get('series'))
        item.update(extra)
        return item

    q1 = sq(1, 'The.Expanse.S01E01.720p', eps[0])
    q2 = sq(2, 'Andor.S01E04.1080p', eps[3], protocol='torrent', downloadClient='qBittorrent', size=3.0,
            sizeleft=1, timeleft='1.02:03:04', trackedDownloadStatus='warning', trackedDownloadState='importPending',
            status='completed', statusMessages=[{'title': 'Andor.S01E04.1080p', 'messages': [
                '', 'Unable to determine if file is a sample']}],
            quality={'quality': {'name': 'WEBDL-1080p', 'resolution': 1080}})
    q3 = sq(3, 'Severance.S03E10', eps[4], size=0, sizeleft=0, timeleft=None, trackedDownloadStatus='Error',
            trackedDownloadState='importBlocked', status='warning', statusMessages=[
                {'title': 'Only a title', 'messages': []}], errorMessage='ignored when a message exists',
            quality=None, indexer=None, downloadClient=None)
    queue_routes = [
        route(f'{p}/queue/status', QUEUE_STATUS),
        route(f'{p}/queue', page([q1, q2], total=3, page_no=1, size=2), query={'page': None}),
        route(f'{p}/queue', page([q3], total=3, page_no=2, size=2), query={'page': '2'}),
    ]
    case(s, 'queue_two_pages', 'get_queue', queue_routes, server=SONARR_SERVER)
    case(s, 'queue_empty', 'get_queue', [route(f'{p}/queue/status', {'totalCount': 0, 'errors': False}),
                                         route(f'{p}/queue', page([]))], server=SONARR_SERVER)
    case(s, 'queue_error', 'get_queue', [route(f'{p}/queue', 'x', status=500)], server=SONARR_SERVER)
    case(s, 'queue_page_two_fails', 'get_queue', [
        route(f'{p}/queue/status', QUEUE_STATUS),
        route(f'{p}/queue', page([q1, q2], total=3, page_no=1, size=2), query={'page': None}),
        route(f'{p}/queue', 'x', status=500, query={'page': '2'})], server=SONARR_SERVER)
    case(s, 'queue_status_fails', 'get_queue', [route(f'{p}/queue/status', 'x', status=500),
                                                route(f'{p}/queue', page([q1]))], server=SONARR_SERVER)
    case(s, 'queue_records_null', 'get_queue', [route(f'{p}/queue', dict(page([]), records=None))],
         server=SONARR_SERVER)
    long_message = 'z' * 700
    odd = [
        sq(4, 'No.Episode.Object', eps[0], episode=None, series=None),
        sq(5, 'Series.Is.A.String', eps[0], series='not an object'),
        sq(6, 'Episode.Without.Numbers', eps[0], episode={'title': 'x'}),
        sq(7, 'Long.Status', eps[0], statusMessages=[{'title': None, 'messages': [long_message]}],
           timeleft='bad', size='2048', sizeleft='1024.5', protocol=None),
        sq(8, 'Error.Message.Only', eps[0], statusMessages=None, errorMessage='Download failed',
           trackedDownloadState=None, quality={'quality': {'name': None}}, timeleft='00:00:30.75'),
        sq(9, 'Empty.Message.Lists', eps[0], statusMessages=[{'messages': None}, {'title': '', 'messages': ['']}],
           quality={'quality': 'x'}, timeleft='23:59:59', episode=dict(eps[0], seasonNumber=True,
                                                                         episodeNumber=False)),
    ]
    case(s, 'queue_odd_items', 'get_queue', [route(f'{p}/queue/status', QUEUE_STATUS),
                                             route(f'{p}/queue', page(odd))], server=SONARR_SERVER)
    case(s, 'queue_non_dict_record', 'get_queue', [route(f'{p}/queue/status', QUEUE_STATUS),
                                                   route(f'{p}/queue', page([None, q1, 'x']))], server=SONARR_SERVER,
         fix='Python raised AttributeError summarising a queue record that was not an object and wrote nothing; '
             'such records are now ignored and the rest is written.')
    case(s, 'queue_page_numbers_as_text', 'get_queue', [
        route(f'{p}/queue/status', QUEUE_STATUS),
        route(f'{p}/queue', dict(page([q1, q2], total='3', page_no=1, size=2)))], server=SONARR_SERVER,
        fix='Python raised TypeError comparing a text totalRecords with a number and wrote nothing; paging now '
            'stops and the records received are written.')

    lib = [series('The Expanse', 1, status='ended'),
           series('Andor', 2, status='Continuing', statistics={'episodeCount': 10, 'episodeFileCount': 4,
                                                                'totalEpisodeCount': 24, 'sizeOnDisk': 50000000000,
                                                                'percentOfEpisodes': 40.123456}),
           series('Upcoming', 3, status='upcoming', monitored=False,
                  statistics={'episodeCount': 0, 'episodeFileCount': 0, 'sizeOnDisk': 0, 'percentOfEpisodes': 0}),
           series('No Stats', 4, statistics=None),
           series('Weird Percent', 5, statistics={'episodeCount': '3', 'episodeFileCount': 3.0,
                                                  'percentOfEpisodes': 'abc', 'sizeOnDisk': '1e3'}),
           series('No Id', 6, id=None),
           series(None, 7, status=None)]
    case(s, 'library', 'get_library', [route(f'{p}/series', lib)], server=SONARR_SERVER)
    case(s, 'library_empty', 'get_library', [route(f'{p}/series', [])], server=SONARR_SERVER)
    case(s, 'library_error', 'get_library', [route(f'{p}/series', 'x', status=500)], server=SONARR_SERVER)
    case(s, 'library_not_a_list', 'get_library', [route(f'{p}/series', {'x': 1})], server=SONARR_SERVER)

    stats_cases(s, p, [1, 3, 4, 5], SONARR_SERVER, 'series')


# ---------------------------------------------------------------------------------------------------------- radarr
def movie(title='Dune', year=2021, tmdb=438631, monitored=True, has_file=False, available=True, **extra):
    m = {'title': title, 'originalTitle': title, 'sortTitle': (title or '').lower(), 'sizeOnDisk': 0,
         'status': 'released', 'overview': '...', 'inCinemas': '2021-10-22T00:00:00Z',
         'physicalRelease': '2022-01-11T00:00:00Z', 'digitalRelease': '2021-10-21T00:00:00Z',
         'images': [{'coverType': 'poster'}], 'website': '', 'year': year, 'hasFile': has_file,
         'youTubeTrailerId': '', 'studio': 'Legendary', 'path': f'/movies/{title}', 'qualityProfileId': 1,
         'monitored': monitored, 'minimumAvailability': 'released', 'isAvailable': available,
         'folderName': f'/movies/{title}', 'runtime': 155, 'cleanTitle': 'dune', 'imdbId': 'tt1160419',
         'tmdbId': tmdb, 'titleSlug': f'{tmdb}', 'genres': ['Science Fiction'], 'tags': [],
         'added': '2021-01-01T00:00:00Z', 'ratings': {'imdb': {'votes': 1, 'value': 8.0}}, 'popularity': 50.5,
         'id': tmdb % 1000 if isinstance(tmdb, int) else 999, 'lastSearchTime': '2026-11-01T00:00:00Z', 'releaseDate': '2021-10-21'}
    m.update(extra)
    return m


def radarr_cases():
    s = 'radarr'
    p = '/api/v3'
    wanted = [movie('Dune', 2021, 438631), movie('Arrival', 2016, 329865, available=False),
              movie('Up', 2009, 14160, has_file=True), movie('Unmonitored', 2000, 1, monitored=False),
              movie('Amélie', 2001, 194), movie('No Tmdb', None, None, tmdbId=None, titleSlug=None)]
    case(s, 'missing_two_pages', 'get_missing', [
        route(f'{p}/wanted/missing', page(wanted[:4], total=501, size=500), query={'page': '1'}),
        route(f'{p}/wanted/missing', page(wanted[4:], total=501, page_no=2, size=500), query={'page': '2'})])
    case(s, 'missing_one_page', 'get_missing', [
        route(f'{p}/wanted/missing', page(wanted, total='6', size=500), query={'page': '1'})])
    case(s, 'missing_empty', 'get_missing', [route(f'{p}/wanted/missing', page([], 0, size=500))])
    case(s, 'missing_none_missing', 'get_missing', [route(f'{p}/wanted/missing', page(wanted[2:4], 2, size=500))])
    case(s, 'missing_error', 'get_missing', [route(f'{p}/wanted/missing', 'x', status=500)])
    case(s, 'missing_page_two_fails', 'get_missing', [
        route(f'{p}/wanted/missing', page(wanted[:2], total=900, size=500), query={'page': '1'}),
        route(f'{p}/wanted/missing', 'x', status=500, query={'page': '2'})])
    case(s, 'missing_records_empty_but_total', 'get_missing', [
        route(f'{p}/wanted/missing', page([], total=1000, size=500), query={'page': '1'})],
        description='an empty page ends the paging even when totalRecords says there is more')
    case(s, 'missing_non_dict_record', 'get_missing', [
        route(f'{p}/wanted/missing', page([None, wanted[0]], total=2, size=500))],
        fix='Python raised TypeError on a record that was not an object and wrote nothing; such records are '
            'now ignored and the rest is written.')

    def rq(i, title, mv, **extra):
        item = queue_item(i, title, movieId=mv.get('id') if isinstance(mv, dict) else None, movie=mv)
        item.update(extra)
        return item

    queue = [rq(1, 'Dune.2021.2160p', movie(), quality={'quality': {'name': 'Bluray-2160p'}}),
             rq(2, 'Arrival.2016.1080p', movie('Arrival', 2016, 329865), protocol='torrent',
                trackedDownloadState='importPending', size=10, sizeleft=3, timeleft='00:00:09.9'),
             rq(3, 'Unknown.Movie', None, quality=None),
             rq(4, 'No.Tmdb', movie('No Tmdb', None, None), protocol='Usenet'),
             rq(5, 'Movie.Is.Text', 'oops'),
             rq(6, 'Protocol.Is.Number', movie(), protocol=5),
             rq(7, 'Timeleft.Is.Number', movie(), timeleft=60)]
    case(s, 'queue', 'get_queue', [route(f'{p}/queue/status', QUEUE_STATUS), route(f'{p}/queue', page(queue))])
    case(s, 'queue_two_pages', 'get_queue', [
        route(f'{p}/queue/status', QUEUE_STATUS),
        route(f'{p}/queue', page(queue[:1], total=2, size=1), query={'page': None}),
        route(f'{p}/queue', page(queue[1:2], total=2, page_no=2, size=1), query={'page': '2'})])
    case(s, 'queue_empty', 'get_queue', [route(f'{p}/queue/status', {'totalCount': 0}),
                                         route(f'{p}/queue', page([]))])
    case(s, 'queue_error', 'get_queue', [route(f'{p}/queue', 'x', status=500)])

    cal = [movie('Cinema Soon', 2026, 1, inCinemas='2026-11-20T00:00:00Z', digitalRelease='2027-01-01T00:00:00Z',
                 physicalRelease=None),
           movie('Digital First', 2026, 2, inCinemas='2026-10-01T00:00:00Z', digitalRelease='2026-11-25',
                 physicalRelease='2026-11-25T00:00:00Z', hasFile=True),
           movie('Today', 2026, 3, inCinemas='2026-11-18T05:00:00Z', digitalRelease=None, physicalRelease=None),
           movie('Edge Of Range', None, 4, inCinemas=None, digitalRelease='2026-12-18T00:00:00Z',
                 physicalRelease='2026-12-19'),
           movie('Out Of Range', 2027, 5, inCinemas='2027-03-01T00:00:00Z', digitalRelease=None,
                 physicalRelease=None),
           movie(None, 2026, None, tmdbId=None, inCinemas='2026-11-30', digitalRelease='', physicalRelease='bad'),
           movie('Number Date', 2026, '77', inCinemas=20261201, digitalRelease=None, physicalRelease=None),
           movie('Float Tmdb', 0, 6.9, inCinemas='2026-11-19T00:00:00Z', digitalRelease=None, physicalRelease=None)]
    cal_q = {'start': '2026-11-18', 'end': '2026-12-18'}
    case(s, 'calendar', 'get_calendar', [route(f'{p}/calendar', cal, query=cal_q)], args={'future_days': 30})
    case(s, 'calendar_empty', 'get_calendar', [route(f'{p}/calendar', [])], args={'future_days': 30})
    case(s, 'calendar_nothing_in_range', 'get_calendar', [route(f'{p}/calendar', [cal[4]])], args={'future_days': 7})
    case(s, 'calendar_error', 'get_calendar', [route(f'{p}/calendar', 'x', status=500)], args={'future_days': 30})
    case(s, 'calendar_bad_tmdb', 'get_calendar', [route(f'{p}/calendar', [
        movie('Bad Id', 2026, 'tt123', inCinemas='2026-11-20T00:00:00Z'), cal[0]])], args={'future_days': 30},
        fix="Python's int() raised ValueError on a non-numeric tmdbId and wrote nothing; that movie is now "
            "skipped and the others are written.")

    def mfile(name, resolution, size, dynamic=None, **extra):
        f = {'size': size, 'quality': {'quality': {'name': name, 'resolution': resolution}},
             'mediaInfo': {'videoDynamicRangeType': dynamic} if dynamic is not False else None}
        f.update(extra)
        return f
    lib = [movie('A', sizeOnDisk=10, movieFile=mfile('Bluray-2160p', 2160, 10, 'HDR10')),
           movie('B', sizeOnDisk=20, movieFile=mfile('WEBDL-1080p', 1080, None, '')),
           movie('C', sizeOnDisk=30, monitored=False, movieFile=mfile('Bluray-2160p', '2160', 30, 'DV HDR10')),
           movie('D', sizeOnDisk=40, movieFile={}),
           movie('E', sizeOnDisk=50, movieFile=None),
           movie('F', sizeOnDisk=60, movieFile=mfile(None, 0, 'x', False, quality=None)),
           movie('G', sizeOnDisk=70, movieFile=mfile(720, -1, 70.9, 'HDR10')),
           movie('H', sizeOnDisk='80')]
    case(s, 'library', 'get_library', [route(f'{p}/movie', lib)])
    case(s, 'library_empty', 'get_library', [route(f'{p}/movie', [])])
    case(s, 'library_error', 'get_library', [route(f'{p}/movie', 'x', status=500)])

    stats_cases(s, p, [1, 3, 4, 6, 8, 9], None, 'movie')


# ---------------------------------------------------------------------------------------------------------- lidarr
def album(title, aid, percent=None, artist='Daft Punk', stats=True, **extra):
    a = {'title': title, 'disambiguation': '', 'overview': '', 'artistId': 1, 'foreignAlbumId': 'mbid',
         'monitored': True, 'anyReleaseOk': True, 'profileId': 1, 'duration': 3000, 'albumType': 'Album',
         'secondaryTypes': [], 'mediumCount': 1, 'ratings': {'votes': 0, 'value': 0}, 'releaseDate':
         '2026-11-10T00:00:00Z', 'releases': [], 'genres': [], 'media': [], 'images': [], 'links': [], 'id': aid,
         'artist': {'artistName': artist, 'id': 1} if isinstance(artist, str) or artist is None else artist}
    if stats is True:
        a['statistics'] = {'trackFileCount': 7, 'trackCount': 14, 'totalTrackCount': 14, 'sizeOnDisk': 1,
                           'percentOfTracks': percent}
    elif stats is not False:
        a['statistics'] = stats
    a.update(extra)
    return a


def lidarr_cases():
    s = 'lidarr'
    p = '/api/v1'
    albums = [album('Random Access Memories', 1, 50.0),
              album('Discovery', 2, 100),
              album('Homework', 3, 100.0),
              album('No Stats Key', 4, stats=False),
              album('Null Percent', 5, None),
              album('Fragments, Live = 2', 6, 0, artist='Björk',
                    statistics={'trackCount': 10, 'percentOfTracks': 0}),
              album('Int Percent', 7, 64, releaseDate=None)]
    missing_q = {'start': '2026-10-19', 'end': '2026-11-18'}
    future_q = {'start': '2026-11-18', 'end': '2026-12-18'}
    case(s, 'calendar_missing', 'get_calendar', [route(f'{p}/calendar', albums, query=missing_q)],
         args={'query': 'Missing'}, server=LIDARR_SERVER)
    case(s, 'calendar_future', 'get_calendar', [route(f'{p}/calendar', albums, query=future_q)],
         args={'query': 'Future'}, server=LIDARR_SERVER)
    case(s, 'calendar_empty', 'get_calendar', [route(f'{p}/calendar', [])], args={'query': 'Missing'},
         server=LIDARR_SERVER)
    case(s, 'calendar_all_complete', 'get_calendar', [route(f'{p}/calendar', albums[1:3])],
         args={'query': 'Missing'}, server=LIDARR_SERVER)
    case(s, 'calendar_error', 'get_calendar', [route(f'{p}/calendar', 'x', status=500)], args={'query': 'Future'},
         server=LIDARR_SERVER)
    case(s, 'calendar_non_dict_items', 'get_calendar', [route(f'{p}/calendar', [None, 1, albums[0]])],
         args={'query': 'Future'}, server=LIDARR_SERVER)
    case(s, 'calendar_null_statistics', 'get_calendar', [route(f'{p}/calendar', [
        album('Null Stats', 8, statistics=None, stats=False), albums[0]])], args={'query': 'Future'},
        server=LIDARR_SERVER,
        fix='Python raised AttributeError on a null statistics object and wrote nothing; it now reads as no '
            'statistics (0%, 0/0), like a missing one.')
    case(s, 'calendar_no_artist', 'get_calendar', [route(f'{p}/calendar', [
        dict(album('No Artist', 9, 10), artist=None), albums[0]])], args={'query': 'Future'},
        server=LIDARR_SERVER,
        fix='Python raised TypeError reading the name of a missing artist and wrote nothing; that album is now '
            'skipped and the others are written.')

    queue = [queue_item(1, 'Daft Punk - Discovery', artistId=1, albumId=2, protocol='UsenetDownloadProtocol'),
             queue_item(2, 'Björk - Fragments', artistId=2, albumId=6, protocol='torrent', indexer=None,
                        trackedDownloadState='failedPending', trackedDownloadStatus='warning'),
             queue_item(3, None, artistId=None, protocol=None, quality=None, size=None, sizeleft=None,
                        timeleft=None, status=None)]
    case(s, 'queue', 'get_queue', [route(f'{p}/queue/status', QUEUE_STATUS),
                                   route(f'{p}/queue', page(queue, size=1000), query={'pageSize': '1000'})],
         server=LIDARR_SERVER)
    case(s, 'queue_empty', 'get_queue', [route(f'{p}/queue/status', {'totalCount': 0}),
                                         route(f'{p}/queue', page([], size=1000))], server=LIDARR_SERVER)
    case(s, 'queue_no_records_key', 'get_queue', [route(f'{p}/queue/status', 'x', status=500),
                                                  route(f'{p}/queue', {'page': 1})], server=LIDARR_SERVER)
    case(s, 'queue_error', 'get_queue', [route(f'{p}/queue', 'x', status=500)], server=LIDARR_SERVER)

    artists = [{'artistName': 'Daft Punk', 'id': 1, 'statistics': {'albumCount': 4, 'trackFileCount': 50,
                                                                    'trackCount': 60, 'totalTrackCount': 61,
                                                                    'sizeOnDisk': 3000000000, 'percentOfTracks': 82}},
               {'artistName': 'Björk', 'id': 2, 'statistics': None},
               {'artistName': 'Text', 'id': 3, 'statistics': {'albumCount': '2', 'sizeOnDisk': 1.5e9}}]
    case(s, 'library', 'get_library', [route(f'{p}/artist', artists)], server=LIDARR_SERVER)
    case(s, 'library_empty', 'get_library', [route(f'{p}/artist', [])], server=LIDARR_SERVER)
    case(s, 'library_error', 'get_library', [route(f'{p}/artist', 'x', status=500)], server=LIDARR_SERVER)

    stats_cases(s, p, [1, 3, 4, 5, 6, 7, 8], LIDARR_SERVER, 'artist')


sonarr_cases()
radarr_cases()
lidarr_cases()
