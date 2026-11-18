package arr_test

import (
	"context"
	"testing"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/collector/arr/arrtest"
	"github.com/Nexxed/Varken/internal/collector/lidarr"
	"github.com/Nexxed/Varken/internal/collector/radarr"
	"github.com/Nexxed/Varken/internal/collector/sonarr"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

// TestGoldenThroughServices runs every Sonarr, Radarr and Lidarr case, so this package's coverage includes the
// shared code the service-specific jobs use (queue paging, summary and item fields).
func TestGoldenThroughServices(t *testing.T) {
	runners := map[string]func(t *testing.T, c testutil.Case, env testutil.Env) error{
		"sonarr": func(t *testing.T, c testutil.Case, env testutil.Env) error {
			var srv config.SonarrServer
			c.Server(t, &srv)
			srv.URL = env.URL
			api := sonarr.New(srv, env.Deps)
			switch c.Job {
			case "get_calendar":
				var query string
				c.Arg(t, "query", &query)
				return api.Calendar(env.Ctx, query)
			case "get_queue":
				return api.Queue(env.Ctx)
			}
			return arrtest.RunStats(t, c, env, api.Base)
		},
		"radarr": func(t *testing.T, c testutil.Case, env testutil.Env) error {
			var srv config.RadarrServer
			c.Server(t, &srv)
			srv.URL = env.URL
			api := radarr.New(srv, env.Deps)
			switch c.Job {
			case "get_missing":
				return api.Missing(env.Ctx)
			case "get_queue":
				return api.Queue(env.Ctx)
			case "get_calendar":
				days := 30
				c.Arg(t, "future_days", &days)
				return api.Calendar(env.Ctx, days)
			}
			return arrtest.RunStats(t, c, env, api.Base)
		},
		"lidarr": func(t *testing.T, c testutil.Case, env testutil.Env) error {
			var srv config.LidarrServer
			c.Server(t, &srv)
			srv.URL = env.URL
			api := lidarr.New(srv, env.Deps)
			switch c.Job {
			case "get_calendar":
				var query string
				c.Arg(t, "query", &query)
				return api.Calendar(env.Ctx, query)
			case "get_queue":
				return api.Queue(env.Ctx)
			}
			return arrtest.RunStats(t, c, env, api.Base)
		},
	}
	for service, run := range runners {
		t.Run(service, func(t *testing.T) {
			testutil.RunGolden(t, service, func(t *testing.T, c testutil.Case, env testutil.Env) {
				if err := run(t, c, env); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}

func TestStatsJobs(t *testing.T) {
	stats := config.ArrStats{GetHealth: true, GetHealthRunSeconds: 1, GetWanted: true, GetWantedRunSeconds: 2,
		GetHistoryTotals: true, GetHistoryTotalsRunSeconds: 3, GetDisk: true, GetDiskRunSeconds: 4,
		GetHistoryEvents: true, GetHistoryEventsRunSeconds: 5, HistoryEventsDays: 1, HistoryEventsBackfillDays: 7,
		GetTasks: true, GetTasksRunSeconds: 6, GetLibrary: true, GetLibraryRunSeconds: 7}
	capture := &testutil.Capture{}
	b := &arr.Base{Measurement: "Lidarr", Prefix: "/api/v1", ServerID: 4,
		Deps: collector.Deps{Writer: capture}.WithDefaults()}
	b.Log = b.Deps.Log
	b.Client = arr.NewClient("http://127.0.0.1:1", "k", false, nil, b.Deps)
	jobs := arr.StatsJobs("lidarr", 4, stats, b)
	want := []struct {
		name    string
		seconds float64
		once    bool
	}{{"lidarr-4-get_health", 1, false}, {"lidarr-4-get_wanted", 2, false}, {"lidarr-4-get_history_totals", 3, false},
		{"lidarr-4-get_disk", 4, false}, {"lidarr-4-history_events", 5, false},
		{"lidarr-4-history_events_backfill", 0, true}, {"lidarr-4-tasks", 6, false}, {"lidarr-4-library", 7, false}}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v", jobs)
	}
	for i, j := range jobs {
		if j.Name != want[i].name || j.Interval.Seconds() != want[i].seconds || j.Once != want[i].once {
			t.Errorf("job %d = %+v, want %+v", i, j, want[i])
		}
		// Every job runs (and fails quietly: nothing listens on port 1).
		if err := j.Run(context.Background()); err != nil {
			t.Errorf("%s: %v", j.Name, err)
		}
	}
	stats.HistoryEventsBackfillDays = 1
	if n := len(arr.StatsJobs("lidarr", 4, stats, b)); n != 7 {
		t.Errorf("no backfill when it is not longer: got %d jobs", n)
	}
}
