import platform
import schedule
import distro
from time import sleep
from queue import Queue
from sys import version
from threading import Thread
from os import environ as env
from os import access, R_OK, getenv
from os.path import isdir, abspath, dirname, join
from argparse import ArgumentParser, RawTextHelpFormatter
from logging import getLogger, StreamHandler, Formatter, DEBUG


# Needed to check version of python
from varken import structures  # noqa
from varken.ombi import OmbiAPI
from varken.overseerr import OverseerrAPI
from varken.unifi import UniFiAPI
from varken import VERSION, BRANCH, BUILD_DATE
from varken.sonarr import SonarrAPI
from varken.radarr import RadarrAPI
from varken.lidarr import LidarrAPI
from varken.iniparser import INIParser
from varken.dbmanager import DBManager
from varken.helpers import GeoIPHandler
from varken.tautulli import TautulliAPI
from varken.sickchill import SickChillAPI
from varken.cleanuparr import CleanuparrAPI
from varken.varkenlogger import VarkenLogger


PLATFORM_LINUX_DISTRO = ' '.join(distro.id() + distro.version() + distro.name())


def thread(job, **kwargs):
    worker = Thread(target=job, kwargs=dict(**kwargs))
    worker.start()


def schedule_arr_stats(api, server, name):
    """Health, wanted counts, history totals and disk space, shared by Sonarr, Radarr and Lidarr."""
    for enabled, seconds, job in ((server.get_health, server.get_health_run_seconds, api.get_health),
                                  (server.get_wanted, server.get_wanted_run_seconds, api.get_wanted),
                                  (server.get_history_totals, server.get_history_totals_run_seconds,
                                   api.get_history_totals),
                                  (server.get_disk, server.get_disk_run_seconds, api.get_disk)):
        if enabled:
            schedule.every(seconds).seconds.do(thread, job).tag(f"{name}-{server.id}-{job.__name__}")
    if server.get_history_events:
        schedule.every(server.get_history_events_run_seconds).seconds.do(
            thread, api.get_history_events, days=server.history_events_days).tag(f"{name}-{server.id}-history_events")
        if server.history_events_backfill_days > server.history_events_days:
            thread(api.get_history_events, days=server.history_events_backfill_days)
    if server.get_tasks:
        schedule.every(server.get_tasks_run_seconds).seconds.do(thread, api.get_tasks).tag(f"{name}-{server.id}-tasks")
    if server.get_library:
        schedule.every(server.get_library_run_seconds).seconds.do(thread, api.get_library).tag(
            f"{name}-{server.id}-library")


if __name__ == "__main__":
    parser = ArgumentParser(prog='varken',
                            description='Command-line utility to aggregate data from the plex ecosystem into InfluxDB',
                            formatter_class=RawTextHelpFormatter)

    parser.add_argument("-d", "--data-folder", help='Define an alternate data folder location')
    parser.add_argument("-D", "--debug", action='store_true', help='Use to enable DEBUG logging. (Depreciated)')
    parser.add_argument("-ND", "--no_debug", action='store_true', help='Use to disable DEBUG logging')

    opts = parser.parse_args()

    templogger = getLogger('temp')
    templogger.setLevel(DEBUG)
    tempch = StreamHandler()
    tempformatter = Formatter('%(asctime)s : %(levelname)s : %(module)s : %(message)s', '%Y-%m-%d %H:%M:%S')
    tempch.setFormatter(tempformatter)
    templogger.addHandler(tempch)

    DATA_FOLDER = env.get('DATA_FOLDER', vars(opts).get('data_folder') or abspath(join(dirname(__file__), 'data')))

    if isdir(DATA_FOLDER):
        if not access(DATA_FOLDER, R_OK):
            templogger.error("Read permission error for %s", DATA_FOLDER)
            exit(1)
    else:
        templogger.error("%s does not exist", DATA_FOLDER)
        exit(1)

    # Set Debug to True if DEBUG env is set
    enable_opts = ['True', 'true', 'yes']
    debug_opts = ['debug', 'Debug', 'DEBUG']

    opts.debug = True

    if getenv('DEBUG') is not None:
        opts.debug = True if any([getenv(string, False) for true in enable_opts
                                  for string in debug_opts if getenv(string, False) == true]) else False

    elif opts.no_debug:
        opts.debug = False

    # Initiate the logger
    vl = VarkenLogger(data_folder=DATA_FOLDER, debug=opts.debug)
    vl.logger.info('Starting Varken...')

    vl.logger.info('Data folder is "%s"', DATA_FOLDER)

    vl.logger.info(u"%s %s (%s%s)", platform.system(), platform.release(), platform.version(),
                   ' - ' + PLATFORM_LINUX_DISTRO if PLATFORM_LINUX_DISTRO else '')

    vl.logger.info(u"Python %s", version)

    vl.logger.info("Varken v%s-%s %s", VERSION, BRANCH, BUILD_DATE)

    CONFIG = INIParser(DATA_FOLDER)
    DBMANAGER = DBManager(CONFIG.influx_server)
    QUEUE = Queue()

    if CONFIG.sonarr_enabled:
        for server in CONFIG.sonarr_servers:
            SONARR = SonarrAPI(server, DBMANAGER)
            if server.queue:
                at_time = schedule.every(server.queue_run_seconds).seconds
                at_time.do(thread, SONARR.get_queue).tag("sonarr-{}-get_queue".format(server.id))
            if server.missing_days > 0:
                at_time = schedule.every(server.missing_days_run_seconds).seconds
                at_time.do(thread, SONARR.get_calendar, query="Missing").tag("sonarr-{}-get_missing".format(server.id))
            if server.future_days > 0:
                at_time = schedule.every(server.future_days_run_seconds).seconds
                at_time.do(thread, SONARR.get_calendar, query="Future").tag("sonarr-{}-get_future".format(server.id))
            schedule_arr_stats(SONARR, server, 'sonarr')

    if CONFIG.tautulli_enabled:
        GEOIPHANDLER = GeoIPHandler(DATA_FOLDER, CONFIG.tautulli_servers[0].maxmind_license_key)
        schedule.every(12).to(24).hours.do(thread, GEOIPHANDLER.update)
        for server in CONFIG.tautulli_servers:
            TAUTULLI = TautulliAPI(server, DBMANAGER, GEOIPHANDLER)
            if server.get_activity:
                at_time = schedule.every(server.get_activity_run_seconds).seconds
                at_time.do(thread, TAUTULLI.get_activity).tag("tautulli-{}-get_activity".format(server.id))
            if server.get_stats:
                at_time = schedule.every(server.get_stats_run_seconds).seconds
                at_time.do(thread, TAUTULLI.get_stats).tag("tautulli-{}-get_stats".format(server.id))
            if server.get_server:
                at_time = schedule.every(server.get_server_run_seconds).seconds
                at_time.do(thread, TAUTULLI.get_server_status).tag("tautulli-{}-get_server_status".format(server.id))
            if server.get_history:
                at_time = schedule.every(server.get_history_run_seconds).seconds
                at_time.do(thread, TAUTULLI.get_history_plays, days=server.history_days).tag(
                    "tautulli-{}-get_history_plays".format(server.id))
                if server.history_backfill_days > server.history_days:
                    thread(TAUTULLI.get_history_plays, days=server.history_backfill_days)
            if server.get_daily_stats:
                for job in (TAUTULLI.get_concurrent_streams, TAUTULLI.get_users, TAUTULLI.get_library_usage,
                            TAUTULLI.get_recently_added):
                    schedule.every(server.get_daily_stats_run_seconds).seconds.do(thread, job).tag(
                        "tautulli-{}-{}".format(server.id, job.__name__))

    if CONFIG.radarr_enabled:
        for server in CONFIG.radarr_servers:
            RADARR = RadarrAPI(server, DBMANAGER)
            if server.get_missing:
                at_time = schedule.every(server.get_missing_run_seconds).seconds
                at_time.do(thread, RADARR.get_missing).tag("radarr-{}-get_missing".format(server.id))
            if server.queue:
                at_time = schedule.every(server.queue_run_seconds).seconds
                at_time.do(thread, RADARR.get_queue).tag("radarr-{}-get_queue".format(server.id))
            schedule_arr_stats(RADARR, server, 'radarr')
            if server.get_calendar:
                schedule.every(server.get_calendar_run_seconds).seconds.do(
                    thread, RADARR.get_calendar, future_days=server.calendar_days).tag(
                    "radarr-{}-get_calendar".format(server.id))

    if CONFIG.lidarr_enabled:
        for server in CONFIG.lidarr_servers:
            LIDARR = LidarrAPI(server, DBMANAGER)
            if server.queue:
                at_time = schedule.every(server.queue_run_seconds).seconds
                at_time.do(thread, LIDARR.get_queue).tag("lidarr-{}-get_queue".format(server.id))
            if server.missing_days > 0:
                at_time = schedule.every(server.missing_days_run_seconds).seconds
                at_time.do(thread, LIDARR.get_calendar, query="Missing").tag(
                    "lidarr-{}-get_missing".format(server.id))
            if server.future_days > 0:
                at_time = schedule.every(server.future_days_run_seconds).seconds
                at_time.do(thread, LIDARR.get_calendar, query="Future").tag("lidarr-{}-get_future".format(
                    server.id))
            schedule_arr_stats(LIDARR, server, 'lidarr')

    if CONFIG.ombi_enabled:
        for server in CONFIG.ombi_servers:
            OMBI = OmbiAPI(server, DBMANAGER)
            if server.request_type_counts:
                at_time = schedule.every(server.request_type_run_seconds).seconds
                at_time.do(thread, OMBI.get_request_counts).tag("ombi-{}-get_request_counts".format(server.id))
            if server.request_total_counts:
                at_time = schedule.every(server.request_total_run_seconds).seconds
                at_time.do(thread, OMBI.get_all_requests).tag("ombi-{}-get_all_requests".format(server.id))
            if server.issue_status_counts:
                at_time = schedule.every(server.issue_status_run_seconds).seconds
                at_time.do(thread, OMBI.get_issue_counts).tag("ombi-{}-get_issue_counts".format(server.id))

    if CONFIG.overseerr_enabled:
        for server in CONFIG.overseerr_servers:
            OVERSEER = OverseerrAPI(server, DBMANAGER)
            if server.get_request_total_counts:
                at_time = schedule.every(server.request_total_run_seconds).seconds
                at_time.do(thread, OVERSEER.get_request_counts).tag("overseerr-{}-get_request_counts"
                                                                    .format(server.id))
            if server.num_latest_requests_to_fetch > 0:
                at_time = schedule.every(server.num_latest_requests_seconds).seconds
                at_time.do(thread, OVERSEER.get_latest_requests).tag("overseerr-{}-get_latest_requests"
                                                                     .format(server.id))

    if CONFIG.sickchill_enabled:
        for server in CONFIG.sickchill_servers:
            SICKCHILL = SickChillAPI(server, DBMANAGER)
            if server.get_missing:
                at_time = schedule.every(server.get_missing_run_seconds).seconds
                at_time.do(thread, SICKCHILL.get_missing).tag("sickchill-{}-get_missing".format(server.id))

    if CONFIG.unifi_enabled:
        for server in CONFIG.unifi_servers:
            UNIFI = UniFiAPI(server, DBMANAGER)
            at_time = schedule.every(server.get_usg_stats_run_seconds).seconds
            at_time.do(thread, UNIFI.get_usg_stats).tag("unifi-{}-get_usg_stats".format(server.id))

    if CONFIG.cleanuparr_enabled:
        for server in CONFIG.cleanuparr_servers:
            CLEANUPARR = CleanuparrAPI(server, DBMANAGER)
            if server.get_stats:
                at_time = schedule.every(server.get_stats_run_seconds).seconds
                at_time.do(thread, CLEANUPARR.get_stats).tag("cleanuparr-{}-get_stats".format(server.id))
            if server.get_timeline:
                at_time = schedule.every(server.get_timeline_run_seconds).seconds
                at_time.do(thread, CLEANUPARR.get_timeline).tag("cleanuparr-{}-get_timeline".format(server.id))
            if server.get_strikes:
                at_time = schedule.every(server.get_strikes_run_seconds).seconds
                at_time.do(thread, CLEANUPARR.get_strikes).tag("cleanuparr-{}-get_strikes".format(server.id))
            if server.get_events:
                at_time = schedule.every(server.get_events_run_seconds).seconds
                at_time.do(thread, CLEANUPARR.get_events).tag("cleanuparr-{}-get_events".format(server.id))

    # Run all on startup
    SERVICES_ENABLED = [CONFIG.ombi_enabled, CONFIG.radarr_enabled, CONFIG.tautulli_enabled, CONFIG.unifi_enabled,
                        CONFIG.sonarr_enabled, CONFIG.sickchill_enabled, CONFIG.lidarr_enabled,
                        CONFIG.overseerr_enabled, CONFIG.cleanuparr_enabled]
    if not [enabled for enabled in SERVICES_ENABLED if enabled]:
        vl.logger.error("All services disabled. Exiting")
        exit(1)

    schedule.run_all()

    while schedule.jobs:
        schedule.run_pending()
        sleep(1)
