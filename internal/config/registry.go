package config

// The key registry: every option Varken reads, with its type, the struct field it fills and the default used
// when it is missing. Defaults are the values in varken.example.ini (which the Python version copied into
// varken.ini when a key was missing) or, for settings added later, the defaults the Python code used.

type kind int

const (
	kindBool kind = iota
	kindInt
	kindString
)

type key struct {
	ini   string // option name in varken.ini
	field string // JSON name of the server struct field
	kind  kind
	def   string
	// env lists the suffixes accepted after VRKN_<SERVICE>_<ID>_, in order of preference. Empty means the
	// upper-cased ini name only.
	env []string
	// optional settings were added after release: Python used the default silently.
	optional bool
}

func (k key) envNames() []string {
	if len(k.env) > 0 {
		return k.env
	}
	return []string{upper(k.ini)}
}

func b(ini, field, def string) key                                         { return key{ini: ini, field: field, kind: kindBool, def: def} }
func n(ini, field, def string) key                                         { return key{ini: ini, field: field, kind: kindInt, def: def} }
func s(ini, field, def string) key                                         { return key{ini: ini, field: field, kind: kindString, def: def} }
func opt(k key) key                                                        { k.optional = true; return k }
func same(ini string, mk func(string, string, string) key, def string) key { return mk(ini, ini, def) }

// Services lists the services in the order Varken.py checked them.
var Services = []string{"sonarr", "radarr", "lidarr", "ombi", "overseerr", "tautulli", "sickchill", "unifi",
	"cleanuparr"}

func arrStats(libraryRunSeconds string) []key {
	return []key{
		opt(same("get_health", b, "true")), opt(same("get_health_run_seconds", n, "300")),
		opt(same("get_wanted", b, "true")), opt(same("get_wanted_run_seconds", n, "900")),
		opt(same("get_history_totals", b, "true")), opt(same("get_history_totals_run_seconds", n, "900")),
		opt(same("get_disk", b, "true")), opt(same("get_disk_run_seconds", n, "900")),
		opt(same("get_history_events", b, "true")), opt(same("get_history_events_run_seconds", n, "300")),
		opt(same("history_events_days", n, "1")), opt(same("history_events_backfill_days", n, "7")),
		opt(same("get_tasks", b, "true")), opt(same("get_tasks_run_seconds", n, "900")),
		opt(same("get_library", b, "true")), opt(same("get_library_run_seconds", n, libraryRunSeconds)),
	}
}

func calendarKeys(missingDays, futureDays string) []key {
	return []key{
		same("missing_days", n, missingDays), same("missing_days_run_seconds", n, "300"),
		same("future_days", n, futureDays), same("future_days_run_seconds", n, "300"),
	}
}

var queueKeys = []key{same("queue", b, "true"), same("queue_run_seconds", n, "300")}

// serviceKeys are the per-service options beyond url, apikey, ssl and verify_ssl.
var serviceKeys = map[string][]key{
	"sonarr": concat(queueKeys, calendarKeys("7", "1"), arrStats("3600")),
	"lidarr": concat(queueKeys, calendarKeys("30", "30"), arrStats("3600")),
	"radarr": concat(queueKeys,
		[]key{same("get_missing", b, "true"), same("get_missing_run_seconds", n, "300")},
		arrStats("21600"),
		[]key{opt(same("get_calendar", b, "true")), opt(same("get_calendar_run_seconds", n, "3600")),
			opt(same("calendar_days", n, "30"))}),
	"tautulli": {
		same("fallback_ip", s, "1.1.1.1"),
		same("get_activity", b, "true"), same("get_activity_run_seconds", n, "30"),
		same("get_stats", b, "true"), same("get_stats_run_seconds", n, "3600"),
		opt(same("get_server", b, "true")), opt(same("get_server_run_seconds", n, "300")),
		opt(same("get_history", b, "true")), opt(same("get_history_run_seconds", n, "300")),
		opt(same("history_days", n, "2")), opt(same("history_backfill_days", n, "364")),
		opt(same("get_daily_stats", b, "true")), opt(same("get_daily_stats_run_seconds", n, "3600")),
	},
	"ombi": {
		b("get_request_type_counts", "request_type_counts", "true"), same("request_type_run_seconds", n, "300"),
		b("get_request_total_counts", "request_total_counts", "true"), same("request_total_run_seconds", n, "300"),
		b("get_issue_status_counts", "issue_status_counts", "true"), same("issue_status_run_seconds", n, "300"),
	},
	"overseerr": {
		same("get_request_total_counts", b, "true"), same("request_total_run_seconds", n, "30"),
		// Python only read the env var under the old name; both names work now.
		{ini: "num_latest_requests_to_fetch", field: "num_latest_requests_to_fetch", kind: kindInt, def: "10",
			env: []string{"NUM_LATEST_REQUESTS_TO_FETCH", "GET_LATEST_REQUESTS_TO_FETCH"}},
		same("num_latest_requests_seconds", n, "30"),
		opt(same("get_latest_requests", b, "true")),
	},
	"sickchill": {same("get_missing", b, "true"), same("get_missing_run_seconds", n, "300")},
	"unifi": {
		same("username", s, "ubnt"), same("password", s, "ubnt"), same("site", s, "default"),
		s("usg_name", "usg_name", "MyRouter"), same("get_usg_stats_run_seconds", n, "300"),
	},
	"cleanuparr": {
		same("include_dry_run", b, "false"),
		same("get_stats", b, "true"), same("stats_hours", n, "24"), same("get_stats_run_seconds", n, "300"),
		same("get_timeline", b, "true"), same("timeline_hours", n, "48"), same("get_timeline_run_seconds", n, "300"),
		same("get_strikes", b, "true"), same("num_strikes_to_fetch", n, "50"),
		same("get_strikes_run_seconds", n, "300"),
		same("get_events", b, "true"), same("events_severity", s, "Important"),
		same("num_events_to_fetch", n, "50"), same("get_events_run_seconds", n, "300"),
	},
}

func concat(lists ...[]key) []key {
	var out []key
	for _, l := range lists {
		out = append(out, l...)
	}
	return out
}

// influxKeys are the [influxdb] options; database is new and optional.
var influxKeys = []key{
	same("url", s, "influxdb.domain.tld"), same("port", n, "8086"), same("ssl", b, "false"),
	same("verify_ssl", b, "false"), same("username", s, "root"), same("password", s, "root"),
	opt(same("database", s, "varken")),
}
