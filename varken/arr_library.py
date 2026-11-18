from datetime import datetime, timezone

from varken.arr import to_int


def resolution_name(resolution):
    """'2160p' from an *arr quality resolution number; 'Unknown' when absent or zero."""
    value = to_int(resolution)
    return f'{value}p' if value > 0 else 'Unknown'


class ArrLibraryMixin(object):
    """Library size/composition for Sonarr (/series), Radarr (/movie) and Lidarr (/artist).

    Mixed into SonarrAPI/RadarrAPI/LidarrAPI alongside ArrStatsMixin, which provides MEASUREMENT, _arr_get,
    _arr_point and _write_arr. Read-only GETs; the Radarr /movie response is large, so poll it rarely.
    """

    def get_library(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        if self.MEASUREMENT == 'Sonarr':
            payload = self._sonarr_library(now)
        elif self.MEASUREMENT == 'Radarr':
            payload = self._radarr_library(now)
        elif self.MEASUREMENT == 'Lidarr':
            payload = self._lidarr_library(now)
        else:
            return
        if payload is None:
            return
        self._write_arr(payload, 'library')

    def _sonarr_library(self, now):
        series_list = self._arr_get('/series')
        if not isinstance(series_list, list):
            return None

        totals = {"series": 0, "series_continuing": 0, "series_ended": 0, "series_complete": 0, "episodes": 0,
                  "episodes_total": 0, "episode_files": 0, "size_bytes": 0}
        payload = []
        for series in series_list:
            stats = series.get('statistics') or {}
            status = (series.get('status') or '').lower()
            episodes = to_int(stats.get('episodeCount'))
            files = to_int(stats.get('episodeFileCount'))
            size = to_int(stats.get('sizeOnDisk'))

            totals["series"] += 1
            if status == 'continuing':
                totals["series_continuing"] += 1
            elif status == 'ended':
                totals["series_ended"] += 1
            if episodes > 0 and files >= episodes:
                totals["series_complete"] += 1
            totals["episodes"] += episodes
            totals["episodes_total"] += to_int(stats.get('totalEpisodeCount'))
            totals["episode_files"] += files
            totals["size_bytes"] += size

            if series.get('id') is None:
                continue
            try:
                percent = round(float(stats.get('percentOfEpisodes') or 0), 2)
            except (TypeError, ValueError):
                percent = 0.0
            payload.append(self._arr_point("Series", {
                "title": str(series.get('title') or ''),
                "size_bytes": size,
                "episodes": episodes,
                "episode_files": files,
                "percent": percent,
                "status": status,
                "monitored": int(bool(series.get('monitored')))
            }, now, series_id=str(series['id'])))

        payload.append(self._arr_point("Library", totals, now))
        return payload

    def _radarr_library(self, now):
        movies = self._arr_get('/movie')
        if not isinstance(movies, list):
            return None

        totals = {"movies": len(movies), "monitored": 0, "with_file": 0, "size_bytes": 0}
        qualities, resolutions, ranges = {}, {}, {}
        for movie in movies:
            if movie.get('monitored'):
                totals["monitored"] += 1
            size = to_int(movie.get('sizeOnDisk'))
            totals["size_bytes"] += size
            movie_file = movie.get('movieFile')
            if not movie_file:
                continue
            totals["with_file"] += 1
            file_size = to_int(movie_file.get('size'), size)

            quality = ((movie_file.get('quality') or {}).get('quality')) or {}
            name = quality.get('name') or 'Unknown'
            entry = qualities.setdefault(name, {"count": 0, "size_bytes": 0})
            entry["count"] += 1
            entry["size_bytes"] += file_size

            entry = resolutions.setdefault(resolution_name(quality.get('resolution')), {"count": 0, "size_bytes": 0})
            entry["count"] += 1
            entry["size_bytes"] += file_size

            dynamic_range = (movie_file.get('mediaInfo') or {}).get('videoDynamicRangeType') or 'SDR'
            entry = ranges.setdefault(dynamic_range, {"count": 0})
            entry["count"] += 1

        payload = [self._arr_point("Library", totals, now)]
        payload += [self._arr_point("Library_Quality", fields, now, quality=name)
                    for name, fields in qualities.items()]
        payload += [self._arr_point("Library_Resolution", fields, now, resolution=name)
                    for name, fields in resolutions.items()]
        payload += [self._arr_point("Library_Dynamic_Range", fields, now, dynamic_range=name)
                    for name, fields in ranges.items()]
        return payload

    def _lidarr_library(self, now):
        artists = self._arr_get('/artist')
        if not isinstance(artists, list):
            return None

        totals = {"artists": len(artists), "albums": 0, "tracks": 0, "track_files": 0, "size_bytes": 0}
        for artist in artists:
            stats = artist.get('statistics') or {}
            totals["albums"] += to_int(stats.get('albumCount'))
            totals["tracks"] += to_int(stats.get('totalTrackCount'))
            totals["track_files"] += to_int(stats.get('trackFileCount'))
            totals["size_bytes"] += to_int(stats.get('sizeOnDisk'))
        return [self._arr_point("Library", totals, now)]
