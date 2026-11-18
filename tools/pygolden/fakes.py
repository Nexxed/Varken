"""Fake HTTP servers shared by the golden harness: a fixture server for the upstream APIs and a fake InfluxDB 1.x.

The Go test helpers in internal/testutil implement the same route-matching rules, so a case.json behaves the same
under both implementations.
"""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import urlsplit, parse_qsl, urlencode


def canonical_request(method, raw_path):
    """'GET /p?b=2&a=1' -> 'GET /p?a=1&b=2' (decoded, sorted by key then value) for comparing request sets."""
    parts = urlsplit(raw_path)
    query = sorted(parse_qsl(parts.query, keep_blank_values=True))
    text = f'{method} {parts.path}'
    if query:
        text += '?' + '&'.join(f'{k}={v}' for k, v in query)
    return text


def route_matches(route, method, path, query, headers):
    if route.get('method', 'GET') != method:
        return False
    if route['path'] != path:
        return False
    for key, value in (route.get('match_query') or {}).items():
        if value is None:
            if key in query:
                return False
        elif query.get(key) != str(value):
            return False
    for key, value in (route.get('match_headers') or {}).items():
        actual = headers.get(key)
        if value is None:
            if actual is not None:
                return False
        elif actual != value:
            return False
    return True


class FixtureServer(object):
    """Serves the routes of a case.json. First matching route wins; a route with "times" is used that many times."""

    def __init__(self, routes):
        self.routes = [dict(r) for r in routes]
        self.requests = []
        self.lock = threading.Lock()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def _serve(self):
                parts = urlsplit(self.path)
                query = dict(parse_qsl(parts.query, keep_blank_values=True))
                length = int(self.headers.get('Content-Length') or 0)
                if length:
                    self.rfile.read(length)
                with outer.lock:
                    outer.requests.append(canonical_request(self.command, self.path))
                    route = None
                    for candidate in outer.routes:
                        if candidate.get('times') == 0:
                            continue
                        if route_matches(candidate, self.command, parts.path, query, self.headers):
                            route = candidate
                            if 'times' in candidate:
                                candidate['times'] -= 1
                            break
                if route is None:
                    self.send_response(404)
                    self.send_header('Content-Type', 'text/plain')
                    self.end_headers()
                    self.wfile.write(b'no fixture route')
                    return
                if 'body_text' in route:
                    body = route['body_text'].encode('utf-8')
                    content_type = 'text/plain'
                elif 'body' in route:
                    body = json.dumps(route['body']).encode('utf-8')
                    content_type = 'application/json'
                else:
                    body = b''
                    content_type = 'text/plain'
                self.send_response(route.get('status', 200))
                self.send_header('Content-Type', content_type)
                for key, value in (route.get('headers') or {}).items():
                    self.send_header(key, value)
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            do_GET = _serve
            do_POST = _serve

        self.httpd = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    @property
    def url(self):
        return f'http://127.0.0.1:{self.port}'

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()


class FakeInflux(object):
    """Minimal InfluxDB 1.8 HTTP API: /ping, /query (SHOW/CREATE) and /write.

    options: databases (list), retention_policies (list), write_status (int), write_error (str).
    """

    def __init__(self, options=None):
        options = options or {}
        self.databases = list(options.get('databases', ['_internal', 'varken']))
        self.retention_policies = list(options.get('retention_policies', ['autogen']))
        self.write_status = options.get('write_status', 204)
        self.write_error = options.get('write_error', '')
        self.writes = []
        self.queries = []
        self.lock = threading.Lock()
        outer = self

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def _json(self, status, payload):
                body = json.dumps(payload).encode('utf-8')
                self.send_response(status)
                self.send_header('Content-Type', 'application/json')
                self.send_header('X-Influxdb-Version', '1.8.10')
                self.send_header('Content-Length', str(len(body)))
                self.end_headers()
                self.wfile.write(body)

            def _serve(self):
                parts = urlsplit(self.path)
                query = dict(parse_qsl(parts.query, keep_blank_values=True))
                length = int(self.headers.get('Content-Length') or 0)
                body = self.rfile.read(length) if length else b''
                if parts.path == '/ping':
                    self.send_response(204)
                    self.send_header('X-Influxdb-Version', '1.8.10')
                    self.end_headers()
                    return
                if parts.path == '/query':
                    if not query.get('q') and body:
                        query.update(dict(parse_qsl(body.decode('utf-8'))))
                    q = query.get('q', '')
                    with outer.lock:
                        outer.queries.append(f"{self.command} {q}")
                    if q == 'SHOW DATABASES':
                        values = [[name] for name in outer.databases]
                        self._json(200, {"results": [{"statement_id": 0, "series": [
                            {"name": "databases", "columns": ["name"], "values": values}]}]})
                    elif q.startswith('SHOW RETENTION POLICIES'):
                        values = [[name, '0s', '168h0m0s', 1, name == 'autogen']
                                  for name in outer.retention_policies]
                        self._json(200, {"results": [{"statement_id": 0, "series": [
                            {"columns": ["name", "duration", "shardGroupDuration", "replicaN", "default"],
                             "values": values}]}]})
                    else:
                        self._json(200, {"results": [{"statement_id": 0}]})
                    return
                if parts.path == '/write':
                    with outer.lock:
                        outer.writes.append(body.decode('utf-8'))
                    if outer.write_status == 204:
                        self.send_response(204)
                        self.send_header('X-Influxdb-Version', '1.8.10')
                        self.end_headers()
                    else:
                        self._json(outer.write_status, {"error": outer.write_error})
                    return
                self._json(404, {"error": "not found"})

            do_GET = _serve
            do_POST = _serve

        self.httpd = ThreadingHTTPServer(('127.0.0.1', 0), Handler)
        self.port = self.httpd.server_address[1]
        self.thread = threading.Thread(target=self.httpd.serve_forever, daemon=True)
        self.thread.start()

    def close(self):
        self.httpd.shutdown()
        self.httpd.server_close()


__all__ = ['FixtureServer', 'FakeInflux', 'canonical_request', 'urlencode']
