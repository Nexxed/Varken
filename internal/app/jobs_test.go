package app

import (
	"strings"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/logging"
)

// TestJobsMatchPythonSchedule checks the jobs and their order against the schedule tags Varken.py created.
func TestJobsMatchPythonSchedule(t *testing.T) {
	env := envOf(map[string]string{
		"VRKN_GLOBAL_LIDARR_SERVER_IDS": "1", "VRKN_GLOBAL_OMBI_SERVER_IDS": "1", "VRKN_GLOBAL_SICKCHILL_SERVER_IDS": "1",
		"VRKN_GLOBAL_UNIFI_SERVER_IDS": "1", "VRKN_GLOBAL_CLEANUPARR_SERVER_IDS": "1",
		"VRKN_LIDARR_1_URL": "lidarr:8686", "VRKN_SICKCHILL_1_URL": "sickchill:8081",
		"VRKN_UNIFI_1_URL": "unifi:8443", "VRKN_CLEANUPARR_1_URL": "cleanuparr:11011", "VRKN_OMBI_1_URL": "ombi",
	})
	cfg, err := config.FromINI(config.ParseINI(config.ExampleINI), env, logging.New(&syncBuffer{}).Logger)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, j := range Jobs(cfg, collector.Deps{Now: time.Now}, logging.New(&syncBuffer{}), nil) {
		if j.Once {
			names = append(names, j.Name+" (once)")
			continue
		}
		names = append(names, j.Name)
		if j.Interval <= 0 {
			t.Errorf("%s has no interval", j.Name)
		}
	}
	arr := func(svc string, head ...string) []string {
		out := []string{}
		for _, h := range head {
			out = append(out, svc+"-1-"+h)
		}
		for _, s := range []string{"get_health", "get_wanted", "get_history_totals", "get_disk", "history_events",
			"history_events_backfill (once)", "tasks", "library"} {
			out = append(out, svc+"-1-"+s)
		}
		return out
	}
	var want []string
	// The example enables sonarr 1,2 and radarr 1,2; servers 2 come right after servers 1.
	for _, id := range []string{"1", "2"} {
		for _, n := range arr("sonarr", "get_queue", "get_missing", "get_future") {
			want = append(want, strings.Replace(n, "sonarr-1-", "sonarr-"+id+"-", 1))
		}
	}
	for _, n := range []string{"get_activity", "get_stats", "get_server_status", "get_history_plays",
		"get_history_plays_backfill (once)", "get_concurrent_streams", "get_users", "get_library_usage",
		"get_recently_added"} {
		want = append(want, "tautulli-1-"+n)
	}
	for _, id := range []string{"1", "2"} {
		for _, n := range append(arr("radarr", "get_missing", "get_queue"), "radarr-1-get_calendar") {
			want = append(want, strings.Replace(n, "radarr-1-", "radarr-"+id+"-", 1))
		}
	}
	want = append(want, arr("lidarr", "get_queue", "get_missing", "get_future")...)
	want = append(want, "ombi-1-get_request_counts", "ombi-1-get_all_requests", "ombi-1-get_issue_counts",
		"overseerr-1-get_request_counts", "overseerr-1-get_latest_requests", "sickchill-1-get_missing",
		"unifi-1-get_usg_stats", "cleanuparr-1-get_stats", "cleanuparr-1-get_timeline", "cleanuparr-1-get_strikes",
		"cleanuparr-1-get_events")
	if strings.Join(names, "\n") != strings.Join(want, "\n") {
		t.Errorf("jobs:\n%s\nwant:\n%s", strings.Join(names, "\n"), strings.Join(want, "\n"))
	}
}
