from datetime import datetime, timezone, timedelta

from varken.helpers import hashit
from varken.arr import to_int

# Extra /history/since parameters per app, so each event carries the series/movie/artist it belongs to.
HISTORY_INCLUDES = {
    'Sonarr': {'includeSeries': 'true', 'includeEpisode': 'true'},
    'Radarr': {'includeMovie': 'true'},
    'Lidarr': {'includeArtist': 'true', 'includeAlbum': 'true'}
}


def parse_arr_date(value):
    """Parse an *arr ISO-8601 timestamp to an aware UTC datetime. None if absent, unparseable or a 0001 sentinel."""
    if not value or not isinstance(value, str):
        return None
    text = value.strip().replace('Z', '+00:00')
    # .NET emits up to 7 fractional digits; fromisoformat on older Pythons only takes 6.
    if '.' in text:
        head, _, rest = text.partition('.')
        digits = ''.join(c for c in rest if c.isdigit())
        tz = rest[len(digits):]
        text = f"{head}.{digits[:6].ljust(6, '0')}{tz}"
    try:
        parsed = datetime.fromisoformat(text)
    except ValueError:
        return None
    if parsed.tzinfo is None:
        parsed = parsed.replace(tzinfo=timezone.utc)
    if parsed.year < 1971:
        return None
    return parsed.astimezone(timezone.utc)


def duration_seconds(value):
    """Convert a .NET TimeSpan string ("HH:MM:SS.fffffff" or "D.HH:MM:SS[.fffffff]") to float seconds."""
    if not value or not isinstance(value, str):
        return 0.0
    try:
        days = 0
        first, _, rest = value.partition(':')
        if '.' in first:
            day_part, first = first.split('.', 1)
            days = int(day_part)
        minutes, seconds = rest.split(':')
        return round(days * 86400 + int(first) * 3600 + int(minutes) * 60 + float(seconds), 3)
    except ValueError:
        return 0.0


class ArrHistoryMixin(object):
    """Per-event history and scheduled task/system status collectors for Sonarr/Radarr/Lidarr.

    Relies only on ArrStatsMixin helpers (_arr_get, _arr_point, _write_arr) and MEASUREMENT. Read-only GETs.
    """

    def _history_title(self, event):
        if self.MEASUREMENT == 'Radarr':
            movie = event.get('movie') or {}
            title = movie.get('title') or ''
            return f"{title} ({movie['year']})" if title and movie.get('year') else title
        if self.MEASUREMENT == 'Lidarr':
            return (event.get('artist') or {}).get('artistName') or ''
        return (event.get('series') or {}).get('title') or ''

    def get_history_events(self, days=1):
        since = (datetime.now(timezone.utc) - timedelta(days=days)).strftime('%Y-%m-%dT%H:%M:%SZ')
        params = {'date': since, **HISTORY_INCLUDES.get(self.MEASUREMENT, {})}
        events = self._arr_get('/history/since', params)
        if not isinstance(events, list):
            return

        payload = []
        for event in events:
            event_id = to_int(event.get('id'), None)
            when = parse_arr_date(event.get('date'))
            if event_id is None or when is None:
                continue
            event_type = str(event.get('eventType') or 'unknown')
            # Only explicitly named keys are read from data: grab events' downloadUrl embeds an indexer API key.
            data = event.get('data') or {}
            quality = event.get('quality') or {}
            protocol = ''
            if event_type == 'grabbed':
                protocol = 'USENET' if 'usenet' in str(data.get('protocol') or '').lower() else 'TORRENT'
            message = str(data.get('message') or data.get('reason') or '')[:300]
            payload.append(self._arr_point("History_Event", {
                "title": str(self._history_title(event)),
                "source_title": str(event.get('sourceTitle') or ''),
                "message": message,
                "event_id": event_id,
                "hash": hashit(f'{self.server.id}{event_id}')
            }, int(when.timestamp()) * 10 ** 9 + event_id % 10 ** 6,
                event_type=event_type,
                indexer=str(data.get('indexer') or ''),
                download_client=str(data.get('downloadClient') or data.get('downloadClientName') or ''),
                quality=str((quality.get('quality') or {}).get('name') or 'Unknown'),
                protocol=protocol))

        if payload:
            self.dbmanager.write_points(payload)

    def get_tasks(self):
        now_dt = datetime.now(timezone.utc)
        now = now_dt.astimezone().isoformat()
        payload = []

        tasks = self._arr_get('/system/task')
        for task in tasks if isinstance(tasks, list) else []:
            name = task.get('taskName') or task.get('name')
            if not name:
                continue
            last = parse_arr_date(task.get('lastExecution'))
            next_run = parse_arr_date(task.get('nextExecution'))
            payload.append(self._arr_point("Task", {
                "interval_minutes": to_int(task.get('interval')),
                "seconds_since_last_run": int((now_dt - last).total_seconds()) if last else -1,
                "last_duration_seconds": float(duration_seconds(task.get('lastDuration'))),
                "next_run": next_run.isoformat() if next_run else ''
            }, now, task=str(name)))

        status = self._arr_get('/system/status')
        if isinstance(status, dict):
            started = parse_arr_date(status.get('startTime'))
            payload.append(self._arr_point("System", {
                "version": str(status.get('version') or ''),
                "uptime_seconds": int((now_dt - started).total_seconds()) if started else -1
            }, now))

        self._write_arr(payload, 'tasks')
