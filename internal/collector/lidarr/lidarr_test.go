package lidarr

import (
	"testing"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr/arrtest"
	"github.com/Nexxed/Varken/internal/config"
	py "github.com/Nexxed/Varken/internal/pycompat"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "lidarr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.LidarrServer
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
	srv := config.LidarrServer{Common: config.Common{ID: 1}, Queue: true, QueueRunSeconds: 30, MissingDays: 30,
		MissingDaysRunSeconds: 300, FutureDays: 30, FutureDaysRunSeconds: 300}
	srv.ArrStats = config.ArrStats{GetDisk: true, GetDiskRunSeconds: 900, GetLibrary: true, GetLibraryRunSeconds: 60}
	jobs := New(srv, collector.Deps{}).Jobs()
	want := []string{"lidarr-1-get_queue", "lidarr-1-get_missing", "lidarr-1-get_future", "lidarr-1-get_disk",
		"lidarr-1-library"}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %v", jobs)
	}
	for i, j := range jobs {
		if j.Name != want[i] {
			t.Errorf("job %d = %s, want %s", i, j.Name, want[i])
		}
	}
}

func TestIs100(t *testing.T) {
	for v, want := range map[string]bool{"100": true, "100.0": true, "99.9": false, `"100"`: false, "true": false,
		"null": false} {
		got := is100(decode(t, v))
		if got != want {
			t.Errorf("is100(%s) = %v", v, got)
		}
	}
}

func decode(t *testing.T, raw string) (v py.Value) {
	t.Helper()
	if err := v.UnmarshalJSON([]byte(raw)); err != nil {
		t.Fatal(err)
	}
	return v
}
