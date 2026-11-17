from datetime import datetime, timezone
from requests import Request

from varken.helpers import connection_handler


def to_int(value, default=0):
    try:
        return int(float(value))
    except (TypeError, ValueError):
        return default


def timeleft_seconds(timeleft):
    """Convert an *arr timeleft string ("HH:MM:SS" or "D.HH:MM:SS") to seconds. None if absent/unparseable."""
    if not timeleft:
        return None
    try:
        days = 0
        if '.' in timeleft.split(':')[0]:
            day_part, timeleft = timeleft.split('.', 1)
            days = int(day_part)
        hours, minutes, seconds = timeleft.split(':')
        return days * 86400 + int(hours) * 3600 + int(minutes) * 60 + int(float(seconds))
    except ValueError:
        return None


def quality_name(quality):
    try:
        return quality['quality']['name']
    except (TypeError, KeyError):
        return 'Unknown'


def queue_item_fields(item):
    """Numeric/state fields for one raw *arr queue record. Strings go in fields, not tags, so a state change does
    not create a new series."""
    size = to_int(item.get('size'))
    sizeleft = to_int(item.get('sizeleft'))
    fields = {
        "size": size,
        "sizeleft": sizeleft,
        "progress": round((size - sizeleft) / size * 100, 2) if size else 0.0,
        "status": item.get('status') or '',
        "tracked_state": item.get('trackedDownloadState') or '',
        "tracked_status": item.get('trackedDownloadStatus') or '',
        "status_message": first_status_message(item),
        "download_client": item.get('downloadClient') or '',
        "indexer": item.get('indexer') or ''
    }
    seconds = timeleft_seconds(item.get('timeleft'))
    if seconds is not None:
        fields["timeleft"] = seconds
    return fields


def first_status_message(item):
    for status in item.get('statusMessages') or []:
        for message in status.get('messages') or []:
            if message:
                return str(message)[:500]
        if status.get('title'):
            return str(status['title'])[:500]
    return item.get('errorMessage') or ''


class ArrStatsMixin(object):
    """Collectors shared by Sonarr (API v3), Radarr (API v3) and Lidarr (API v1).

    Subclasses set MEASUREMENT, API_PREFIX and HISTORY_EVENTS ({field name: history eventType number}) and provide
    self.server, self.session and self.dbmanager. All endpoints used here are read-only GETs.
    """
    MEASUREMENT = None
    API_PREFIX = '/api/v3'
    HISTORY_EVENTS = {}

    def _arr_get(self, path, params=None):
        req = self.session.prepare_request(Request('GET', self.server.url + self.API_PREFIX + path, params=params))
        return connection_handler(self.session, req, self.server.verify_ssl)

    def _count(self, path, **params):
        """totalRecords of a paged endpoint, fetching a single record. None on failure."""
        params['pageSize'] = 1
        result = self._arr_get(path, params)
        if not isinstance(result, dict) or 'totalRecords' not in result:
            return None
        return to_int(result['totalRecords'])

    def _arr_point(self, point_type, fields, now, **tags):
        return {
            "measurement": self.MEASUREMENT,
            "tags": {"type": point_type, "server": self.server.id, **tags},
            "time": now,
            "fields": fields
        }

    def _write_arr(self, payload, job):
        if payload:
            self.dbmanager.write_points(payload)
        else:
            self.logger.warning("No data to send to influx for %s-%s instance, discarding.",
                                self.MEASUREMENT.lower(), job)

    def get_health(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        checks = self._arr_get('/health')
        if not isinstance(checks, list):
            return

        counts = {"errors": 0, "warnings": 0, "notices": 0}
        payload = []
        seen = {}
        for check in checks:
            level = (check.get('type') or 'unknown').lower()
            if level == 'error':
                counts["errors"] += 1
            elif level == 'warning':
                counts["warnings"] += 1
            elif level == 'notice':
                counts["notices"] += 1
            source = check.get('source') or 'Unknown'
            # Two checks with the same source and level would otherwise overwrite each other.
            seen[(source, level)] = seen.get((source, level), 0) + 1
            check_name = source if seen[(source, level)] == 1 else f'{source}#{seen[(source, level)]}'
            payload.append(self._arr_point("Health", {
                "message": (check.get('message') or '')[:500],
                "wiki_url": str(check.get('wikiUrl') or '')
            }, now, source=check_name, level=level))

        counts["issues"] = counts["errors"] + counts["warnings"]
        payload.append(self._arr_point("Health_Summary", counts, now))
        self._write_arr(payload, 'health')

    def get_wanted(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        fields = {
            "missing_monitored": self._count('/wanted/missing', monitored='true'),
            "missing_unmonitored": self._count('/wanted/missing', monitored='false'),
            "cutoff_unmet": self._count('/wanted/cutoff', monitored='true')
        }
        fields = {key: value for key, value in fields.items() if value is not None}
        self._write_arr([self._arr_point("Wanted", fields, now)] if fields else [], 'wanted')

    def get_history_totals(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        fields = {name: self._count('/history', eventType=event_type)
                  for name, event_type in self.HISTORY_EVENTS.items()}
        fields["blocklisted"] = self._count('/blocklist')
        fields = {key: value for key, value in fields.items() if value is not None}
        self._write_arr([self._arr_point("History_Totals", fields, now)] if fields else [], 'history')

    def get_disk(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        disks = self._arr_get('/diskspace')
        disks = disks if isinstance(disks, list) else []
        roots = self._arr_get('/rootfolder')
        roots = roots if isinstance(roots, list) else []

        payload = []
        for disk in disks:
            if not disk.get('path'):
                continue
            payload.append(self._arr_point("Disk_Space", {
                "free_bytes": to_int(disk.get('freeSpace')),
                "total_bytes": to_int(disk.get('totalSpace'))
            }, now, path=disk['path'], label=disk.get('label') or ''))

        for root in roots:
            path = root.get('path')
            if not path:
                continue
            fields = {
                "free_bytes": to_int(root.get('freeSpace')),
                "accessible": int(bool(root.get('accessible'))),
                "unmapped_folders": len(root.get('unmappedFolders') or [])
            }
            total = to_int(root.get('totalSpace'), None)
            if not total:
                # Sonarr/Radarr don't report totalSpace for root folders: use the volume it lives on.
                volumes = [d for d in disks if d.get('path') and path.rstrip('/').startswith(d['path'].rstrip('/'))]
                if volumes:
                    total = to_int(max(volumes, key=lambda d: len(d['path']))['totalSpace'], None)
            if total:
                fields["total_bytes"] = total
            payload.append(self._arr_point("Root_Folder", fields, now, path=path))

        self._write_arr(payload, 'disk')

    def queue_summary_point(self, records, now):
        """Queue totals from raw queue records plus /queue/status. Written even when the queue is empty."""
        fields = {
            "items": len(records),
            "size": sum(to_int(r.get('size')) for r in records),
            "sizeleft": sum(to_int(r.get('sizeleft')) for r in records),
            "warning_items": sum(1 for r in records if (r.get('trackedDownloadStatus') or '').lower() == 'warning'),
            "error_items": sum(1 for r in records if (r.get('trackedDownloadStatus') or '').lower() == 'error')
        }
        for state in ('downloading', 'importBlocked', 'importPending', 'importing', 'imported', 'failedPending',
                      'failed', 'ignored'):
            key = 'state_' + ''.join('_' + c.lower() if c.isupper() else c for c in state)
            fields[key] = sum(1 for r in records if r.get('trackedDownloadState') == state)

        status = self._arr_get('/queue/status')
        if isinstance(status, dict):
            fields["total_count"] = to_int(status.get('totalCount'))
            fields["unknown_count"] = to_int(status.get('unknownCount'))
            fields["has_errors"] = int(bool(status.get('errors')))
            fields["has_warnings"] = int(bool(status.get('warnings')))
        return self._arr_point("Queue_Summary", fields, now)
