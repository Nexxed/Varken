package radarr

import (
	"testing"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr/arrtest"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "radarr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.RadarrServer
		c.Server(t, &srv)
		srv.URL = env.URL
		api := New(srv, env.Deps)
		var err error
		switch c.Job {
		case "get_missing":
			err = api.Missing(env.Ctx)
		case "get_queue":
			err = api.Queue(env.Ctx)
		case "get_calendar":
			days := 30
			c.Arg(t, "future_days", &days)
			err = api.Calendar(env.Ctx, days)
		default:
			err = arrtest.RunStats(t, c, env, api.Base)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range env.Fixture.Headers {
			if h.Get("X-Api-Key") != "testkey" {
				t.Errorf("X-Api-Key header = %q", h.Get("X-Api-Key"))
			}
		}
	})
}

func TestJobs(t *testing.T) {
	srv := config.RadarrServer{Common: config.Common{ID: 3}, GetMissing: true, GetMissingRunSeconds: 300, Queue: true,
		QueueRunSeconds: 30, GetCalendar: true, GetCalendarRunSeconds: 3600, CalendarDays: 30}
	srv.ArrStats = config.ArrStats{GetWanted: true, GetWantedRunSeconds: 900, GetHistoryTotals: true,
		GetHistoryTotalsRunSeconds: 900, GetDisk: true, GetDiskRunSeconds: 900, GetHistoryEvents: true,
		GetHistoryEventsRunSeconds: 300, HistoryEventsDays: 7, HistoryEventsBackfillDays: 7, GetTasks: true,
		GetTasksRunSeconds: 900}
	jobs := New(srv, collector.Deps{}).Jobs()
	want := []string{"radarr-3-get_missing", "radarr-3-get_queue", "radarr-3-get_wanted",
		"radarr-3-get_history_totals", "radarr-3-get_disk", "radarr-3-history_events", "radarr-3-tasks",
		"radarr-3-get_calendar"}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v", jobs)
	}
	for i, j := range jobs {
		if j.Name != want[i] || j.Once {
			t.Errorf("job %d = %s (once=%v), want %s", i, j.Name, j.Once, want[i])
		}
	}
	if jobs[7].Interval.Seconds() != 3600 {
		t.Errorf("calendar interval %v", jobs[7].Interval)
	}
}
