package arrtest

import (
	"testing"

	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/collector/sonarr"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestRunStats(t *testing.T) {
	testutil.RunGolden(t, "sonarr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.SonarrServer
		c.Server(t, &srv)
		srv.URL = env.URL
		var b *arr.Base = sonarr.New(srv, env.Deps).Base
		switch c.Job {
		case "get_calendar", "get_queue":
			t.Skip("not a shared job")
		}
		if err := RunStats(t, c, env, b); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRunStatsRejectsOtherJobs(t *testing.T) {
	env := testutil.Env{}
	if err := RunStats(t, testutil.Case{Name: "x", Job: "get_queue"}, env, nil); err == nil {
		t.Error("expected an error for a service-specific job")
	}
	if err := RunStats(t, testutil.Case{Name: "x", Job: "get_history_events"}, env, nil); err == nil {
		t.Error("expected an error without a days argument")
	}
}
