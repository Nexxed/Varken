"""Overseerr fixtures: /api/v1/request/count and the latest requests with their movie/TV details."""
import copy

from common import case, route

S = 'overseerr'
COUNTS = {'total': 20, 'movie': 12, 'tv': 8, 'pending': 2, 'approved': 15, 'declined': 1, 'processing': 3,
          'available': 10}


def request(rid, kind, tmdb, user, created, media_status=5):
    return {'id': rid, 'status': 2, 'createdAt': created, 'updatedAt': created, 'type': kind, 'is4k': False,
            'media': {'id': rid + 100, 'tmdbId': tmdb, 'tvdbId': None, 'status': media_status, 'mediaType': kind},
            'requestedBy': {'id': rid, 'displayName': user, 'email': f'{user.lower()}@example.com'},
            'modifiedBy': None, 'seasons': []}


def detail(kind, tmdb, title, requests, media_status=5):
    body = {'id': tmdb, 'mediaInfo': {'id': tmdb + 1, 'tmdbId': tmdb, 'status': media_status,
                                      'requests': [{'id': r['id'], 'createdAt': r['createdAt'],
                                                    'requestedBy': r['requestedBy']} for r in requests]}}
    body['name' if kind == 'tv' else 'title'] = title
    return body


R1 = request(1, 'movie', 603, 'Alice', '2026-11-17T10:00:00.000Z')
R2 = request(2, 'tv', 1399, 'Bob', '2026-11-16T09:00:00.000Z', media_status=4)
R3 = request(3, 'movie', 550, 'Carol', '2026-11-15T08:00:00.000Z', media_status=2)
R4 = request(4, 'music', 1, 'Dave', '2026-11-14T08:00:00.000Z')
LATEST = {'pageInfo': {'pages': 1, 'pageSize': 10, 'results': 4, 'page': 1}, 'results': [R1, R2, R3, R4]}


def latest_routes(latest=LATEST, take=10, details=None):
    routes = [route('/api/v1/request', latest, query={'take': take, 'filter': 'all', 'sort': 'added'})]
    for path, body, status in details if details is not None else [
            ('/api/v1/movie/603', detail('movie', 603, 'The Matrix', [R1]), 200),
            ('/api/v1/tv/1399', detail('tv', 1399, 'Game of Thrones', [R2], 4), 200),
            ('/api/v1/movie/550', detail('movie', 550, 'Fight Club', [R3], 2), 200)]:
        routes.append(route(path, body, status=status, text=None if status == 200 else 'error'))
    return routes


case(S, 'request_counts_happy', 'get_request_counts', [route('/api/v1/request/count', COUNTS)],
     server={'get_request_total_counts': True})
case(S, 'request_counts_missing_fields', 'get_request_counts',
     [route('/api/v1/request/count', {'total': 5, 'movie': None, 'tv': 2.0})])
case(S, 'request_counts_error', 'get_request_counts',
     [route('/api/v1/request/count', None, status=403, text='Forbidden')])
case(S, 'request_counts_server_tag', 'get_request_counts', [route('/api/v1/request/count', COUNTS)],
     server={'id': 3, 'add_server_tag': True},
     fix='With add_server_tag = true (new, off by default) the count point carries the server tag; Python had none.')

case(S, 'latest_happy', 'get_latest_requests', latest_routes(),
     description='Movie and TV requests, each the only request for its media; other types are ignored.')
small = copy.deepcopy(LATEST)
small['results'] = small['results'][:1]
case(S, 'latest_take_1', 'get_latest_requests', latest_routes(small, take=1, details=[
    ('/api/v1/movie/603', detail('movie', 603, 'The Matrix', [R1]), 200)]),
    server={'num_latest_requests_to_fetch': 1})
case(S, 'latest_empty', 'get_latest_requests', latest_routes({'pageInfo': {}, 'results': []}, details=[]))
case(S, 'latest_error', 'get_latest_requests', [route('/api/v1/request', None, status=401, text='Unauthorized')])

R5 = request(5, 'movie', 603, 'Erin', '2026-11-18T12:00:00.000Z')
several = copy.deepcopy(LATEST)
several['results'] = [R5, R1]
case(S, 'latest_several_requests', 'get_latest_requests', latest_routes(several, details=[
    ('/api/v1/movie/603', detail('movie', 603, 'The Matrix', [R1, R5]), 200)]),
    fix="Python tagged every request with the user and date of the media's first request; each request now "
        "carries its own requestedBy and createdAt. The title is looked up once per media.")

case(S, 'latest_detail_fails', 'get_latest_requests', latest_routes(details=[
    ('/api/v1/movie/603', None, 500),
    ('/api/v1/tv/1399', detail('tv', 1399, 'Game of Thrones', [R2], 4), 200),
    ('/api/v1/movie/550', detail('movie', 550, 'Fight Club', [R3], 2), 200)]),
    fix='Python crashed (TypeError) when a movie/TV lookup failed and wrote nothing; that request is skipped.')

odd = copy.deepcopy(LATEST)
odd['results'][0]['media'] = None
odd['results'][1]['requestedBy'] = None
odd['results'][2]['media']['status'] = None
case(S, 'latest_missing_fields', 'get_latest_requests', latest_routes(odd, details=[
    ('/api/v1/tv/1399', detail('tv', 1399, 'Game of Thrones', [R2], 4), 200),
    ('/api/v1/movie/550', detail('movie', 550, 'Fight Club', [R3], 2), 200)]),
    fix='Python crashed on a request without media and wrote nothing; it is skipped. A request without '
        'requestedBy is written without requested_user, and a null media status falls back to the lookup.')
