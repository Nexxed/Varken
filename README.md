<p align="center">
<img width="800" src="https://raw.githubusercontent.com/Boerderij/Varken/master/assets/varken_full_banner.jpg" alt="Logo Banner">
</p>

[![pipeline status](https://gitlab.com/boerderij/Varken/badges/master/pipeline.svg)](https://gitlab.com/boerderij/Varken/commits/master)
[![Discord](https://img.shields.io/discord/518970285773422592.svg?colorB=7289DA&label=Discord&logo=Discord&logoColor=7289DA&style=flat-square)](https://discord.gg/VjZ6qSM)
[![ko-fi](https://img.shields.io/badge/Buy%20Us%20A%20Coffee-Donate-ff813f.svg?logo=CoffeeScript&style=flat-square)](https://ko-fi.com/varken)
[![Docker-Layers](https://images.microbadger.com/badges/image/boerderij/varken.svg)](https://microbadger.com/images/boerderij/varken)
[![Release](https://img.shields.io/github/release/boerderij/varken.svg?style=flat-square)](https://github.com/Boerderij/Varken/releases/latest)
[![Docker Pulls](https://img.shields.io/docker/pulls/boerderij/varken.svg)](https://hub.docker.com/r/boerderij/varken/)

> **About this fork:** this is [Nexxed/Varken](https://github.com/Nexxed/Varken), based on upstream's `develop`
> branch and rewritten in Go. It adds a [Cleanuparr](https://github.com/Cleanuparr/Cleanuparr) collector, keeps
> working when newer Sonarr/Radarr releases add API fields, and publishes images to `ghcr.io/nexxed/varken`
> (`:develop`/`:latest`, `:cleanuparr`, `:sha-<commit>`) for amd64, arm64 and armv7. Enable Cleanuparr with
> `cleanuparr_server_ids` and a `[cleanuparr-N]` section; see `data/varken.example.ini`. The API key is the one on
> Cleanuparr's account settings page. The last Python version is kept on the `python-1.x` branch.

Dutch for PIG. PIG is an Acronym for Plex/InfluxDB/Grafana

Varken is a standalone application to aggregate data from the Plex 
ecosystem into InfluxDB using Grafana for a frontend

Requirements:
* [InfluxDB 1.8.x](https://www.influxdata.com/)
* [Grafana](https://grafana.com/)
* Nothing else: Varken is a single program. Use the Docker image, or build it with [Go](https://go.dev/dl/) 1.26+
  (`go build ./cmd/varken`).

<p align="center">
Example Dashboard

<img width="800" src="https://i.imgur.com/3hNZTkC.png" alt="dashboard">
</p>

Supported Modules:
* [Sonarr](https://sonarr.tv/) - Smart PVR for newsgroup and bittorrent users.
* [SickChill](https://sickchill.github.io/) - SickChill is an automatic Video Library Manager for TV Shows.
* [Radarr](https://radarr.video/) - A fork of Sonarr to work with movies à la Couchpotato.
* [Tautulli](https://tautulli.com/) - A Python based monitoring and tracking tool for Plex Media Server.
* [Ombi](https://ombi.io/) - Want a Movie or TV Show on Plex or Emby? Use Ombi!
* [Lidarr](https://lidarr.audio/) - Looks and smells like Sonarr but made for music.
* [Overseerr](https://overseerr.dev/) - Request management and media discovery for Plex.
* [UniFi](https://ui.com/) - USG/UDM WAN throughput, CPU and memory.
* [Cleanuparr](https://github.com/Cleanuparr/Cleanuparr) - Strikes, removals, cleanups and health from Cleanuparr.

Key features:
* Multiple server support for all modules
* Geolocation mapping from [GeoLite2](https://dev.maxmind.com/geoip/geoip2/geolite2/)
* Grafana [Worldmap Panel](https://grafana.com/plugins/grafana-worldmap-panel/installation) support


## Installation Guides
Varken Installation guides can be found in the [wiki](https://wiki.cajun.pro/books/varken/chapter/installation).

### Commands
```
varken [-d DATA_FOLDER] [-ND]            run the collectors (the default)
varken import-tautulli [-d DIR] [-D N]   import N days (default 365) of Tautulli play history
varken grafana-build [flags]             create the Grafana datasource and import the Varken dashboard
varken version
```
`DATA_FOLDER` in the environment takes precedence over `-d`; the default is the `data` folder next to the program.
In Docker the data folder is `/config`, e.g. `docker exec -it varken /app/varken grafana-build -password secret`.

### Upgrading from the Python version
Nothing in your configuration changes: the same `varken.ini`, `VRKN_*` variables, `DATA_FOLDER`, `DEBUG`, `TZ`,
flags, image name and `/config` volume work as before, and the data written to InfluxDB keeps the same
measurements, tags and field types, so existing dashboards keep working. Varken no longer rewrites `varken.ini`
when a setting is missing; it uses the default and logs a warning.

* **Docker**: pull the new image. That is all.
* **Without Docker**: replace the virtualenv and `Varken.py` with the `varken` binary in the same folder, and change
  the systemd `ExecStart` to `/opt/Varken/varken` (see `varken.systemd`).
* `utilities/historical_tautulli_import.py` is now `varken import-tautulli`, and `utilities/grafana_build.py` is
  `varken grafana-build` (its settings are flags; `varken grafana-build -h` lists them).

New optional settings: `[influxdb] database` (default `varken`) and `[global] add_server_tag` (default `false`),
which adds the `server` tag to the Ombi and Overseerr count points and the UniFi point when several servers of one
kind are configured. See CHANGELOG.md for the bugs fixed along the way.

### Development
`go test -race ./...` runs the unit tests and the golden tests. The golden tests replay recorded API responses
(`testdata/fixtures`) and compare the line protocol with what the Python version wrote for the same responses;
`scripts/golden.sh` regenerates those expectations from the `python-1.x` branch and fails if any changed.

To add fixtures from your own servers, run `go run ./tools/record -d /path/to/data`: it runs every job once through a
recording proxy and writes sanitised `testdata/fixtures/<service>/recorded_<job>/case.json` files (API keys, user
names, e-mail and IP addresses and your host names are replaced). Review them, record the Python expectations with
`tools/pygolden/harness.py`, then run the tests.

## Support
Please read [Asking for Support](https://wiki.cajun.pro/books/varken/chapter/asking-for-support) before seeking support. 

[Click here for quick access to discord support](http://cyborg.decreator.dev/channels/518970285773422592/530424560504537105/). No app or account needed!
    
### InfluxDB
[InfluxDB Installation Documentation](https://wiki.cajun.pro/books/varken/page/influxdb-d1f)
Note: Only v1.8.x is currently supported.
 
Influxdb is required but not packaged as part of Varken. Varken will create
its database on its own. If you choose to give varken user permissions that
do not include database creation, please ensure you create an influx database
named `varken` (or the name set with `[influxdb] database`). Varken waits for
InfluxDB for about a minute at startup, so it can start alongside it.

### Grafana
[Grafana Installation/Dashboard Documentation](https://wiki.cajun.pro/books/varken/page/grafana) 