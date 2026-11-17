from logging import getLogger
from requests import Session, Request
from datetime import datetime, timezone

from varken.helpers import connection_handler, hashit


class CleanuparrAPI(object):
    """Collects statistics from Cleanuparr (https://github.com/Cleanuparr/Cleanuparr).

    Only read-only GET endpoints are used. Requests authenticate with the X-Api-Key header, using the API key shown
    on Cleanuparr's account settings page. Everything is written to the "Cleanuparr" measurement, told apart by the
    "type" tag:

    - Summary, Strikes_By_Type, Removals_By_Reason, Cleaned_By_Reason, Events_By_Type, Events_By_Severity,
      Searches_By_Reason, Job, Health: snapshots of /api/v2/stats for the last ``stats_hours`` hours.
    - Timeline: hourly counts from /api/v2/stats/timeline, written at each bucket's own timestamp, so re-polling
      simply overwrites the buckets with their latest values.
    - Strike_Item: the downloads Cleanuparr currently tracks strikes for (/api/strikes).
    - Event: recent events (/api/events), written at the event's own timestamp.
    """
    TIMELINE_METRICS = ('strikesIssued', 'recovered', 'removed', 'malwareBlocked', 'events')
    MAX_TEXT_LENGTH = 500

    def __init__(self, server, dbmanager):
        self.dbmanager = dbmanager
        self.server = server
        self.session = Session()
        self.session.headers = {'X-Api-Key': self.server.api_key}
        self.logger = getLogger()

    def __repr__(self):
        return f"<cleanuparr-{self.server.id}>"

    def _get(self, endpoint, params=None):
        req = self.session.prepare_request(Request('GET', self.server.url + endpoint, params=params))
        return connection_handler(self.session, req, self.server.verify_ssl)

    def _write(self, influx_payload, job):
        if influx_payload:
            self.dbmanager.write_points(influx_payload)
        else:
            self.logger.warning("No data to send to influx for cleanuparr-%s instance, discarding.", job)

    def _dry_run_param(self):
        return 'true' if self.server.include_dry_run else 'false'

    def _point(self, point_type, fields, time, **tags):
        return {
            "measurement": "Cleanuparr",
            "tags": {"type": point_type, "server": self.server.id, **tags},
            "time": time,
            "fields": fields
        }

    def _truncate(self, text):
        text = text or ''
        if len(text) > self.MAX_TEXT_LENGTH:
            return text[:self.MAX_TEXT_LENGTH - 1] + '…'
        return text

    def get_stats(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        params = {'hours': self.server.stats_hours, 'includeDryRun': self._dry_run_param()}
        stats = self._get('/api/v2/stats', params=params)
        if not stats:
            return

        hours = stats.get('timeframeHours', self.server.stats_hours)
        events = stats.get('events') or {}
        strikes = stats.get('strikes') or {}
        removals = stats.get('removals') or {}
        cleaned = stats.get('cleaned') or {}
        searches = stats.get('searches') or {}
        jobs = stats.get('jobs') or {}
        health = stats.get('health') or {}

        influx_payload = [
            self._point("Summary", {
                "events": int(events.get('total', 0)),
                "strikes": int(strikes.get('total', 0)),
                "strikes_recovered": int(strikes.get('recovered', 0)),
                "removals": int(removals.get('total', 0)),
                "cleaned": int(cleaned.get('total', 0)),
                "searches": int(searches.get('total', 0)),
                "searches_completed": int(searches.get('completed', 0)),
                "searches_failed": int(searches.get('failed', 0)),
                "searches_grabbed": int(searches.get('grabbed', 0)),
                "jobs": int(jobs.get('total', 0)),
                "jobs_completed": int(jobs.get('completed', 0)),
                "jobs_failed": int(jobs.get('failed', 0))
            }, now, timeframe_hours=hours)
        ]

        breakdowns = (
            ("Strikes_By_Type", strikes.get('byType')),
            ("Removals_By_Reason", removals.get('byReason')),
            ("Cleaned_By_Reason", cleaned.get('byReason')),
            ("Events_By_Type", events.get('byType')),
            ("Events_By_Severity", events.get('bySeverity')),
            ("Searches_By_Reason", searches.get('byReason')),
        )
        for point_type, counts in breakdowns:
            for name, count in (counts or {}).items():
                influx_payload.append(self._point(point_type, {"count": int(count)}, now, timeframe_hours=hours,
                                                  name=name))

        for job_name, job in (jobs.get('byType') or {}).items():
            fields = {
                "total": int(job.get('total', 0)),
                "completed": int(job.get('completed', 0)),
                "failed": int(job.get('failed', 0)),
                "last_run": job.get('lastRunAt') or '',
                "next_run": job.get('nextRunAt') or ''
            }
            influx_payload.append(self._point("Job", fields, now, timeframe_hours=hours, name=job_name))

        for kind, items in (("Download_Client", health.get('downloadClients')), ("Arr", health.get('arrInstances'))):
            for item in items or []:
                fields = {
                    "healthy": int(bool(item.get('isHealthy'))),
                    "error": self._truncate(item.get('errorMessage')),
                    "last_checked": item.get('lastChecked') or ''
                }
                if item.get('responseTimeMs') is not None:
                    fields["response_time_ms"] = float(item['responseTimeMs'])
                influx_payload.append(self._point("Health", fields, now, kind=kind, name=item.get('name') or '',
                                                  app=item.get('type') or ''))

        self._write(influx_payload, 'stats')

    def get_timeline(self):
        influx_payload = []
        for metric in self.TIMELINE_METRICS:
            params = {'metric': metric, 'hours': self.server.timeline_hours, 'bucket': 'hour',
                      'includeDryRun': self._dry_run_param()}
            buckets = self._get('/api/v2/stats/timeline', params=params)
            if not isinstance(buckets, list):
                continue
            for bucket in buckets:
                if not bucket.get('date'):
                    continue
                influx_payload.append(self._point("Timeline", {"count": int(bucket.get('count', 0))},
                                                  bucket['date'], metric=metric))

        self._write(influx_payload, 'timeline')

    def get_strikes(self):
        now = datetime.now(timezone.utc).astimezone().isoformat()
        page = self._get('/api/strikes', params={'page': 1, 'pageSize': self.server.num_strikes_to_fetch})
        if not page:
            return

        influx_payload = []
        for item in page.get('items') or []:
            strikes = item.get('strikes') or []
            if not self.server.include_dry_run and strikes and all(s.get('isDryRun') for s in strikes):
                continue

            if item.get('isRemoved'):
                status = 'Removed'
            elif item.get('isMarkedForRemoval'):
                status = 'Marked for removal'
            else:
                status = 'Active'

            download_id = item.get('downloadId') or item.get('downloadItemId') or ''
            by_type = item.get('strikesByType') or {}
            influx_payload.append(self._point("Strike_Item", {
                "title": self._truncate(item.get('title')),
                "status": status,
                "total_strikes": int(item.get('totalStrikes') or 0),
                "strike_types": ', '.join(f'{name} x{count}' for name, count in sorted(by_type.items())),
                "first_strike": item.get('firstStrikeAt') or '',
                "latest_strike": item.get('latestStrikeAt') or '',
                "returning": int(bool(item.get('isReturning'))),
                "hash": hashit(f'{self.server.id}{download_id}')
            }, now, download_id=download_id))

        self._write(influx_payload, 'strikes')

    def get_events(self):
        params = {'page': 1, 'pageSize': self.server.num_events_to_fetch}
        if self.server.events_severity:
            params['severity'] = self.server.events_severity
        page = self._get('/api/events', params=params)
        if not page:
            return

        influx_payload = []
        for event in page.get('items') or []:
            if not event.get('timestamp'):
                continue
            if event.get('isDryRun') and not self.server.include_dry_run:
                continue

            reason = (event.get('deleteReason') or event.get('cleanReason')
                      or '; '.join(event.get('failedImportReasons') or []))
            influx_payload.append(self._point("Event", {
                "title": self._truncate(event.get('itemTitle')),
                "message": self._truncate(event.get('message')),
                "reason": self._truncate(reason),
                "hash": hashit(f'{self.server.id}{event.get("id")}')
            }, event['timestamp'], event_type=event.get('eventType') or '', severity=event.get('severity') or ''))

        self._write(influx_payload, 'events')
