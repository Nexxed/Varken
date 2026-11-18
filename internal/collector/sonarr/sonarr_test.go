package sonarr

import (
	"testing"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr/arrtest"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "sonarr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.SonarrServer
		c.Server(t, &srv)
		srv.URL = env.URL
		api := New(srv, env.Deps)
		var err error
		switch c.Job {
		case "get_calendar":
			var query string
			c.Arg(t, "query", &query)
			err = api.Calendar(env.Ctx, query)
		case "get_queue":
			err = api.Queue(env.Ctx)
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
	srv := config.SonarrServer{Common: config.Common{ID: 2}, Queue: true, QueueRunSeconds: 30, MissingDays: 7,
		MissingDaysRunSeconds: 300, FutureDays: 1, FutureDaysRunSeconds: 600}
	srv.ArrStats = config.ArrStats{GetHealth: true, GetHealthRunSeconds: 300, GetHistoryEvents: true,
		GetHistoryEventsRunSeconds: 300, HistoryEventsDays: 1, HistoryEventsBackfillDays: 7, GetLibrary: true,
		GetLibraryRunSeconds: 3600}
	jobs := New(srv, collector.Deps{}).Jobs()
	want := []string{"sonarr-2-get_queue", "sonarr-2-get_missing", "sonarr-2-get_future", "sonarr-2-get_health",
		"sonarr-2-history_events", "sonarr-2-history_events_backfill", "sonarr-2-library"}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v", jobs)
	}
	for i, j := range jobs {
		if j.Name != want[i] {
			t.Errorf("job %d = %s, want %s", i, j.Name, want[i])
		}
	}
	if jobs[1].Interval.Seconds() != 300 || jobs[2].Interval.Seconds() != 600 || !jobs[5].Once {
		t.Errorf("intervals: %v %v once=%v", jobs[1].Interval, jobs[2].Interval, jobs[5].Once)
	}
	srv.MissingDays, srv.FutureDays, srv.Queue = 0, 0, false
	srv.ArrStats = config.ArrStats{}
	if jobs := New(srv, collector.Deps{}).Jobs(); len(jobs) != 0 {
		t.Errorf("disabled server has jobs %v", jobs)
	}
}
