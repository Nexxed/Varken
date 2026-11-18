from logging import getLogger
from requests import Session, Request
from datetime import date, datetime, timedelta, timezone

from varken.structures import QueuePages, RadarrMovie, RadarrQueue
from varken.helpers import hashit, connection_handler
from varken.arr import ArrStatsMixin, queue_item_fields, quality_name


class RadarrAPI(ArrStatsMixin):
    MEASUREMENT = 'Radarr'
    API_PREFIX = '/api/v3'
    HISTORY_EVENTS = {'grabbed': 1, 'imported': 3, 'failed': 4, 'deleted': 6, 'renamed': 8, 'ignored': 9}
    WANTED_PAGE_SIZE = 500

    def __init__(self, server, dbmanager):
        self.dbmanager = dbmanager
        self.server = server
        # Create session to reduce server web thread load, and globally define pageSize for all requests
        self.session = Session()
        self.session.headers = {'X-Api-Key': self.server.api_key}
        self.logger = getLogger()

    def __repr__(self):
        return f"<radarr-{self.server.id}>"

    def get_missing(self):
        # /wanted/missing returns only monitored movies without a file, instead of the whole library from /movie
        # (about 12 MB every run for a ~2,000 movie library).
        endpoint = '/api/v3/wanted/missing'
        now = datetime.now(timezone.utc).astimezone().isoformat()
        influx_payload = []
        missing = []

        records = []
        page = 1
        while True:
            params = {'page': page, 'pageSize': self.WANTED_PAGE_SIZE, 'monitored': 'true'}
            req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
            get = connection_handler(self.session, req, self.server.verify_ssl)
            if not get:
                return
            response = QueuePages(**get)
            records.extend(response.records or [])
            if not response.records or int(response.totalRecords or 0) <= page * self.WANTED_PAGE_SIZE:
                break
            page += 1

        try:
            movies = [RadarrMovie(**movie) for movie in records]
        except TypeError as e:
            self.logger.error('TypeError has occurred : %s while creating RadarrMovie structure', e)
            return

        for movie in movies:
            if movie.monitored and not movie.hasFile:
                if movie.isAvailable:
                    ma = 0
                else:
                    ma = 1

                movie_name = f'{movie.title} ({movie.year})'
                missing.append((movie_name, ma, movie.tmdbId, movie.titleSlug))

        for title, ma, mid, title_slug in missing:
            hash_id = hashit(f'{self.server.id}{title}{mid}')
            influx_payload.append(
                {
                    "measurement": "Radarr",
                    "tags": {
                        "type": "Missing",
                        "Missing": True,
                        "Missing_Available": ma,
                        "tmdbId": mid,
                        "server": self.server.id,
                        "name": title,
                        "titleSlug": title_slug
                    },
                    "time": now,
                    "fields": {
                        "hash": hash_id
                    }
                }
            )

        if influx_payload:
            self.dbmanager.write_points(influx_payload)
        else:
            self.logger.warning("No data to send to influx for radarr-missing instance, discarding.")

    def get_queue(self):
        endpoint = '/api/v3/queue'
        now = datetime.now(timezone.utc).astimezone().isoformat()
        influx_payload = []
        pageSize = 250
        params = {'pageSize': pageSize, 'includeMovie': True, 'includeUnknownMovieItems': False}
        queueResponse = []
        queue = []

        req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
        get = connection_handler(self.session, req, self.server.verify_ssl)

        if not get:
            return

        response = QueuePages(**get)
        queueResponse.extend(response.records)

        while response.totalRecords > response.page * response.pageSize:
            page = response.page + 1
            params = {'pageSize': pageSize, 'page': page, 'includeMovie': True, 'includeUnknownMovieItems': False}
            req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
            get = connection_handler(self.session, req, self.server.verify_ssl)
            if not get:
                return

            response = QueuePages(**get)
            queueResponse.extend(response.records)

        influx_payload.append(self.queue_summary_point(queueResponse, now))

        download_queue = []
        for queueItem in queueResponse:
            try:
                download_queue.append((queueItem, RadarrQueue(**queueItem)))
            except TypeError as e:
                # Skip just this item; previously one bad record dropped the whole queue.
                self.logger.warning('TypeError has occurred : %s while creating RadarrQueue structure', e)

        for raw_item, queue_item in download_queue:
            try:
                movie = RadarrMovie(**(queue_item.movie or {}))
                name = f'{movie.title} ({movie.year})'
                protocol = (queue_item.protocol or '').upper()
                protocol_id = 1 if protocol == 'USENET' else 0
                # tmdbId used to hold the queue item id; write the movie's real TMDb id.
                queue.append((name, quality_name(queue_item.quality), protocol, protocol_id,
                              movie.tmdbId if movie.tmdbId is not None else queue_item.id, movie.titleSlug,
                              queue_item_fields(raw_item)))
            except (TypeError, KeyError, AttributeError) as e:
                self.logger.warning('Skipping Radarr queue item %s: %s', queue_item.id, e)

        for name, quality, protocol, protocol_id, qid, title_slug, extra_fields in queue:
            hash_id = hashit(f'{self.server.id}{name}{quality}')
            influx_payload.append(
                {
                    "measurement": "Radarr",
                    "tags": {
                        "type": "Queue",
                        "tmdbId": qid,
                        "server": self.server.id,
                        "name": name,
                        "quality": quality,
                        "protocol": protocol,
                        "protocol_id": protocol_id,
                        "titleSlug": title_slug
                    },
                    "time": now,
                    "fields": {
                        "hash": hash_id,
                        **extra_fields
                    }
                }
            )

        if influx_payload:
            self.dbmanager.write_points(influx_payload)
        else:
            self.logger.warning("No data to send to influx for radarr-queue instance, discarding.")

    def get_calendar(self, future_days=30):
        """Upcoming movie releases: one point per movie, for the earliest of its cinema/digital/physical release
        dates that falls between today and today + future_days."""
        start = date.today()
        end = start + timedelta(days=future_days)
        now = datetime.now(timezone.utc).astimezone().isoformat()
        movies = self._arr_get('/calendar', {'start': str(start), 'end': str(end)})
        if not isinstance(movies, list):
            return

        payload = []
        for movie in movies:
            releases = []
            for key, release_type in (('inCinemas', 'cinema'), ('digitalRelease', 'digital'),
                                      ('physicalRelease', 'physical')):
                value = movie.get(key)
                if not value:
                    continue
                try:
                    release_date = date.fromisoformat(str(value)[:10])
                except ValueError:
                    continue
                if start <= release_date <= end:
                    releases.append((release_date, release_type))
            if not releases:
                continue
            release_date, release_type = min(releases)
            tmdb_id = int(movie.get('tmdbId') or 0)
            year = movie.get('year')
            title = movie.get('title') or 'Unknown'
            name = f'{title} ({year})' if year else title
            payload.append(self._arr_point("Future", {
                "hash": hashit(f'{self.server.id}{tmdb_id}{release_type}'),
                "tmdb_id": tmdb_id
            }, now, name=name, release_type=release_type, release_date=str(release_date),
                downloaded="1" if movie.get('hasFile') else "0"))

        self._write_arr(payload, 'calendar')
