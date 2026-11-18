"""Ombi fixtures: request counts, issue counts and the full movie/TV request lists (Ombi v4 API)."""
import copy

from common import case, route

S = 'ombi'
ALL = {'request_type_counts': True, 'request_total_counts': True, 'issue_status_counts': True}


def movie(mid, title, *, denied=False, approved=False, available=False, user='alice', **extra):
    m = {'id': mid, 'theMovieDbId': 1000 + mid, 'title': title, 'denied': denied, 'approved': approved,
         'available': available, 'requestedDate': f'2026-11-{mid:02d}T10:00:00', 'requestedUserId': 'u1',
         'requestedUser': {'id': 'u1', 'userAlias': user, 'userName': user.lower() if user else None},
         'requestType': 1, 'imdbId': f'tt{mid:07d}', 'overview': 'An overview', 'released': True,
         'playedByUsersCount': 0, 'watchedByRequestedUser': False, 'canApprove': True, 'source': 1}
    m.update(extra)
    return m


def child(*, denied=None, approved=False, available=False, user='bob', date='2026-11-10T09:30:00.123'):
    return {'id': 7, 'denied': denied, 'approved': approved, 'available': available, 'requestedDate': date,
            'requestedUser': {'userAlias': user}, 'seasonRequests': [{'seasonNumber': 1}]}


def tv(tid, title, *children, **extra):
    t = {'id': tid, 'tvDbId': 70000 + tid, 'title': title, 'childRequests': list(children), 'totalSeasons': 3,
         'imdbId': None, 'status': 'Continuing', 'background': 'x.jpg', 'requestStatus': 'Common.Approved'}
    t.update(extra)
    return t


MOVIES = [
    movie(1, 'Denied Movie', denied=True),
    movie(2, 'Available Movie', approved=True, available=True, user='Carol'),
    movie(3, 'Approved Movie', approved=True),
    movie(4, 'Pending Movie'),
    movie(5, 'Denied But Available', denied=True, approved=True, available=True),
    movie(6, 'Available Not Approved', available=True),
    movie(7, 'Wrong Types', approved=1, available=''),
]
MOVIES[-1]['title'] = None
SHOWS = [
    tv(1, 'Denied Show', child(denied=True)),
    tv(2, 'Available Show', child(approved=True, available=True), child(denied=True)),
    tv(3, 'Approved Show', child(approved=True, user='Dana')),
    tv(4, 'Pending Show', child()),
    tv(5.0, 'Float Id Show', child(approved=True, available=False)),
]


def lists(tv_body=SHOWS, movie_body=MOVIES, tv_status=200, movie_status=200):
    return [route('/api/v1/Request/tv', tv_body, status=tv_status, text=None if tv_status == 200 else 'error'),
            route('/api/v1/Request/movie', movie_body, status=movie_status,
                  text=None if movie_status == 200 else 'error')]


case(S, 'request_counts_happy', 'get_request_counts',
     [route('/api/v1/Request/count', {'pending': 3, 'approved': 10, 'available': 42})], server=ALL)
case(S, 'request_counts_null_field', 'get_request_counts',
     [route('/api/v1/Request/count', {'pending': None, 'approved': 2.5, 'extra': 1})],
     description='A null count is left out; available is missing so the NamedTuple default 0 is written.')
case(S, 'request_counts_error', 'get_request_counts',
     [route('/api/v1/Request/count', None, status=401, text='Unauthorized')])
case(S, 'request_counts_empty', 'get_request_counts', [route('/api/v1/Request/count', {})])
case(S, 'request_counts_server_tag', 'get_request_counts',
     [route('/api/v1/Request/count', {'pending': 3, 'approved': 10, 'available': 42})],
     server={'id': 2, 'add_server_tag': True},
     fix='With add_server_tag = true (new, off by default) the count points carry the server tag; Python had none.')

case(S, 'issue_counts_happy', 'get_issue_counts',
     [route('/api/v1/Issues/count', {'pending': 1, 'inProgress': 2, 'resolved': 30})])
case(S, 'issue_counts_empty', 'get_issue_counts', [route('/api/v1/Issues/count', {})])
case(S, 'issue_counts_error', 'get_issue_counts', [route('/api/v1/Issues/count', None, status=500, text='x')])
case(S, 'issue_counts_server_tag', 'get_issue_counts',
     [route('/api/v1/Issues/count', {'pending': 1, 'inProgress': 2, 'resolved': 30})],
     server={'add_server_tag': True},
     fix='With add_server_tag = true the count points carry the server tag; Python had none.')

case(S, 'all_requests_happy', 'get_all_requests', lists(),
     description='Every status branch for movies and TV, wrong-typed flags and a float id.')
case(S, 'all_requests_tv_only', 'get_all_requests', lists(movie_status=500))
case(S, 'all_requests_movies_only', 'get_all_requests', lists(tv_body=[]))
case(S, 'all_requests_both_fail', 'get_all_requests', lists(tv_status=500, movie_status=404))
case(S, 'all_requests_empty_lists', 'get_all_requests', lists(tv_body=[], movie_body=[]))
case(S, 'all_requests_non_object_items', 'get_all_requests', lists(tv_body=[None, 'x', SHOWS[0]],
                                                                   movie_body=[5, MOVIES[0]]),
     description='Non-object list items are skipped (Python logged a TypeError for each) but still counted.')

empty_child = copy.deepcopy(SHOWS)
empty_child.insert(1, tv(9, 'No Child Requests'))
empty_child.append(tv(10, 'Null Child Requests', childRequests=None))
case(S, 'all_requests_empty_child_requests', 'get_all_requests', lists(tv_body=empty_child),
     fix='Python crashed (IndexError/TypeError) on a TV request without child requests and wrote nothing; '
         'that request is skipped.')

null_user = copy.deepcopy(MOVIES)
null_user[1]['requestedUser'] = None
null_user[2]['requestedUser'] = {'userName': 'no alias'}
null_user_tv = copy.deepcopy(SHOWS)
null_user_tv[0]['childRequests'][0]['requestedUser'] = None
del null_user_tv[1]['childRequests'][0]['requestedDate']
case(S, 'all_requests_null_user', 'get_all_requests', lists(tv_body=null_user_tv, movie_body=null_user),
     fix='Python crashed on a null requestedUser (or a missing userAlias/requestedDate) and wrote nothing; '
         'the point is written without that tag.')
