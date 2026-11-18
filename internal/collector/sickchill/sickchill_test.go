package sickchill

import (
	"context"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "sickchill", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.SickChillServer
		c.Server(t, &srv)
		srv.URL = env.URL
		if c.Job != "get_missing" {
			t.Fatalf("unknown job %s", c.Job)
		}
		if err := New(srv, env.Deps).GetMissing(env.Ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJobs(t *testing.T) {
	srv := config.SickChillServer{Common: config.Common{ID: 3}, GetMissing: true, GetMissingRunSeconds: 300}
	jobs := New(srv, collector.Deps{}).Jobs()
	if len(jobs) != 1 || jobs[0].Name != "sickchill-3-get_missing" || jobs[0].Interval != 5*time.Minute {
		t.Fatalf("jobs = %+v", jobs)
	}
	srv.GetMissing = false
	if jobs := New(srv, collector.Deps{}).Jobs(); len(jobs) != 0 {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestUnreachable(t *testing.T) {
	capture := &testutil.Capture{}
	api := New(config.SickChillServer{Common: config.Common{URL: "http://127.0.0.1:1"}}, collector.Deps{Writer: capture})
	if err := api.GetMissing(context.Background()); err != nil || capture.Lines() != "" {
		t.Errorf("err = %v, lines = %q", err, capture.Lines())
	}
}
