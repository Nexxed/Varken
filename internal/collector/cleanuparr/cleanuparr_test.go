package cleanuparr

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	py "github.com/Nexxed/Varken/internal/pycompat"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "cleanuparr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.CleanuparrServer
		c.Server(t, &srv)
		srv.URL = env.URL
		api := New(srv, env.Deps)
		jobs := map[string]func(context.Context) error{"get_stats": api.GetStats, "get_timeline": api.GetTimeline,
			"get_strikes": api.GetStrikes, "get_events": api.GetEvents}
		run, ok := jobs[c.Job]
		if !ok {
			t.Fatalf("unknown job %s", c.Job)
		}
		if err := run(env.Ctx); err != nil {
			t.Fatal(err)
		}
		for _, h := range env.Fixture.Headers {
			if h.Get("X-Api-Key") != "testkey" {
				t.Errorf("X-Api-Key = %q", h.Get("X-Api-Key"))
			}
		}
	})
}

func TestJobs(t *testing.T) {
	srv := config.CleanuparrServer{Common: config.Common{ID: 2}, GetStats: true, GetStatsRunSeconds: 10,
		GetTimeline: true, GetTimelineRunSeconds: 20, GetStrikes: true, GetStrikesRunSeconds: 30, GetEvents: true,
		GetEventsRunSeconds: 40}
	jobs := New(srv, collector.Deps{}).Jobs()
	names := []string{"cleanuparr-2-get_stats", "cleanuparr-2-get_timeline", "cleanuparr-2-get_strikes",
		"cleanuparr-2-get_events"}
	if len(jobs) != len(names) {
		t.Fatalf("jobs = %+v", jobs)
	}
	for i, name := range names {
		if jobs[i].Name != name || jobs[i].Interval != time.Duration(10*(i+1))*time.Second {
			t.Errorf("job %d = %+v", i, jobs[i])
		}
	}
	srv.GetStats, srv.GetTimeline, srv.GetStrikes, srv.GetEvents = false, false, false, false
	if len(New(srv, collector.Deps{}).Jobs()) != 0 {
		t.Error("disabled jobs scheduled")
	}
}

func value(t *testing.T, raw string) py.Value {
	var v py.Value
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCount(t *testing.T) {
	cases := map[string]string{`5`: "5", `"7"`: "7", `2.9`: "2", `true`: "1", `null`: "0", `"x"`: "0",
		`"2.5"`: "0", `[1]`: "0"}
	for in, want := range cases {
		if got := count(value(t, in)).String(); got != want {
			t.Errorf("count(%s) = %s, want %s", in, got, want)
		}
	}
	if got := count(py.Value{}).String(); got != "0" {
		t.Errorf("missing count = %s", got)
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", 501)
	got, _ := truncate(py.String(long)).AsString()
	if py.RuneLen(got) != 500 || !strings.HasSuffix(got, "é…") {
		t.Errorf("truncate(501 chars) has %d chars", py.RuneLen(got))
	}
	exact := strings.Repeat("x", 500)
	if got, _ := truncate(py.String(exact)).AsString(); got != exact {
		t.Error("500 chars should be kept")
	}
	if got := truncate(py.None).String(); got != "" {
		t.Errorf("truncate(None) = %q", got)
	}
	if got := truncate(py.Int(42)).String(); got != "42" {
		t.Errorf("truncate(42) = %q", got)
	}
}

func TestTimeOf(t *testing.T) {
	for raw, want := range map[string]int64{`"2026-11-18T16:00:00Z"`: 1795017600000000000, `12345`: 12345,
		`true`: 1, `false`: 0} {
		at, ok := timeOf(value(t, raw))
		ns, _, err := at.NS()
		if !ok || err != nil || ns != want {
			t.Errorf("timeOf(%s) = %d, %v, %v", raw, ns, ok, err)
		}
	}
	for _, raw := range []string{`1.5`, `[1]`, `null`} {
		if _, ok := timeOf(value(t, raw)); ok {
			t.Errorf("timeOf(%s) should fail", raw)
		}
	}
}

func TestEventReason(t *testing.T) {
	var e event
	if err := json.Unmarshal([]byte(`{"cleanReason": "c", "failedImportReasons": ["a"]}`), &e); err != nil {
		t.Fatal(err)
	}
	if e.reason().String() != "c" {
		t.Errorf("reason = %s", e.reason())
	}
	e = event{}
	_ = json.Unmarshal([]byte(`{"failedImportReasons": "not a list"}`), &e)
	if e.reason().String() != "" {
		t.Errorf("reason = %q", e.reason())
	}
}

func TestUnreachable(t *testing.T) {
	capture := &testutil.Capture{}
	api := New(config.CleanuparrServer{Common: config.Common{URL: "http://127.0.0.1:1"}}, collector.Deps{Writer: capture})
	ctx := context.Background()
	for _, run := range []func(context.Context) error{api.GetStats, api.GetTimeline, api.GetStrikes, api.GetEvents} {
		if err := run(ctx); err != nil {
			t.Error(err)
		}
	}
	if capture.Lines() != "" {
		t.Errorf("wrote %q", capture.Lines())
	}
}
