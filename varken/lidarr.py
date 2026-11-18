from logging import getLogger
from requests import Session, Request
from datetime import datetime, timezone, date, timedelta

from varken.structures import LidarrQueue, LidarrAlbum
from varken.helpers import hashit, connection_handler
from varken.arr import ArrStatsMixin, queue_item_fields, quality_name


class LidarrAPI(ArrStatsMixin):
    MEASUREMENT = 'Lidarr'
    API_PREFIX = '/api/v1'
    HISTORY_EVENTS = {'grabbed': 1, 'track_imported': 3, 'failed': 4, 'deleted': 5, 'renamed': 6,
                      'import_incomplete': 7, 'imported': 8}

    def __init__(self, server, dbmanager):
        self.dbmanager = dbmanager
        self.server = server
        # Create session to reduce server web thread load, and globally define pageSize for all requests
        self.session = Session()
        self.session.headers = {'X-Api-Key': self.server.api_key}
        self.logger = getLogger()

    def __repr__(self):
        return f"<lidarr-{self.server.id}>"

    def get_calendar(self, query="Missing"):
        endpoint = '/api/v1/calendar'
        today = str(date.today())
        last_days = str(date.today() - timedelta(days=self.server.missing_days))
        future = str(date.today() + timedelta(days=self.server.future_days))
        now = datetime.now(timezone.utc).astimezone().isoformat()
        if query == "Missing":
            params = {'start': last_days, 'end': today}
        else:
            params = {'start': today, 'end': future}
        influx_payload = []
        influx_albums = []

        req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
        get = connection_handler(self.session, req, self.server.verify_ssl)

        if not get:
            return

        # Iteratively create a list of LidarrAlbum Objects from response json
        albums = []
        for album in get:
            try:
                albums.append(LidarrAlbum(**album))
            except TypeError as e:
                self.logger.error('TypeError has occurred : %s while creating LidarrAlbum structure for album. Data '
                                  'attempted is: %s', e, album)

        # Missing: only albums that aren't complete. Future: every upcoming album, like Sonarr's Future.
        for album in albums:
            percent_of_tracks = album.statistics.get('percentOfTracks', 0)
            if query != "Missing" or percent_of_tracks != 100:
                influx_albums.append(
                    (album.title, album.releaseDate, album.artist['artistName'], album.id, percent_of_tracks,
                     f"{album.statistics.get('trackFileCount', 0)}/{album.statistics.get('trackCount', 0)}")
                )

        for title, release_date, artist_name, album_id, percent_complete, complete_count in influx_albums:
            hash_id = hashit(f'{self.server.id}{title}{album_id}')
            influx_payload.append(
                {
                    "measurement": "Lidarr",
                    "tags": {
                        "type": query,
                        "albumId": album_id,
                        "server": self.server.id,
                        "albumName": title,
                        "artistName": artist_name,
                        "percentComplete": percent_complete,
                        "completeCount": complete_count,
                        "releaseDate": release_date
                    },
                    "time": now,
                    "fields": {
                        "hash": hash_id

                    }
                }
            )

        self.dbmanager.write_points(influx_payload)

    def get_queue(self):
        endpoint = '/api/v1/queue'
        now = datetime.now(timezone.utc).astimezone().isoformat()
        influx_payload = []
        params = {'pageSize': 1000}

        req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
        get = connection_handler(self.session, req, self.server.verify_ssl)

        if not get:
            return

        records = get.get('records') or []
        influx_payload.append(self.queue_summary_point(records, now))

        queue = []
        for song in records:
            try:
                queue.append((song, LidarrQueue(**song)))
            except TypeError as e:
                self.logger.error('TypeError has occurred : %s while creating LidarrQueue structure for show. Data '
                                  'attempted is: %s', e, song)

        for raw_item, song in queue:
            # Lidarr 3 reports "UsenetDownloadProtocol"/"TorrentDownloadProtocol" rather than "usenet"/"torrent".
            protocol = 'USENET' if 'usenet' in (song.protocol or '').lower() else 'TORRENT'
            protocol_id = 1 if protocol == 'USENET' else 0
            hash_id = hashit(f'{self.server.id}{song.title}{song.artistId}')
            influx_payload.append(
                {
                    "measurement": "Lidarr",
                    "tags": {
                        "type": "Queue",
                        "id": song.id,
                        "server": self.server.id,
                        "title": song.title,
                        "quality": quality_name(song.quality),
                        "protocol": protocol,
                        "protocol_id": protocol_id,
                        "indexer": song.indexer
                    },
                    "time": now,
                    "fields": {
                        "hash": hash_id,
                        **queue_item_fields(raw_item)
                    }
                }
            )

        self.dbmanager.write_points(influx_payload)
