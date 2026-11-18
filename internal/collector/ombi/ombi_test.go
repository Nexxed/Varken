package ombi

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	py "github.com/Nexxed/Varken/internal/pycompat"
	"github.com/Nexxed/Varken/internal/testutil"
)

// loadServer reads the settings the Python job ran with, then the case's own overrides, which carry the
// Go-only settings (add_server_tag) Python's server tuple dropped.
func loadServer(t *testing.T, c testutil.Case, out any) {
	c.Server(t, out)
	raw, err := os.ReadFile(filepath.Join(c.Dir, "case.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Server json.RawMessage `json:"server"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Server != nil {
		if err := json.Unmarshal(doc.Server, out); err != nil {
			t.Fatal(err)
		}
	}
}

func TestGolden(t *testing.T) {
	testutil.RunGolden(t, "ombi", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.OmbiServer
		loadServer(t, c, &srv)
		srv.URL = env.URL
		api := New(srv, env.Deps)
		var err error
		switch c.Job {
		case "get_request_counts":
			err = api.GetRequestCounts(env.Ctx)
		case "get_issue_counts":
			err = api.GetIssueCounts(env.Ctx)
		case "get_all_requests":
			err = api.GetAllRequests(env.Ctx)
		default:
			t.Fatalf("unknown job %s", c.Job)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, h := range env.Fixture.Headers {
			if h.Get("Apikey") != "testkey" {
				t.Errorf("Apikey header = %q", h.Get("Apikey"))
			}
		}
	})
}

func TestJobs(t *testing.T) {
	srv := config.OmbiServer{Common: config.Common{ID: 4}, RequestTypeCounts: true, RequestTypeRunSeconds: 30,
		RequestTotalCounts: true, RequestTotalRunSeconds: 60, IssueStatusCounts: true, IssueStatusRunSeconds: 90}
	jobs := New(srv, collector.Deps{}).Jobs()
	want := []struct {
		name     string
		interval time.Duration
	}{{"ombi-4-get_request_counts", 30 * time.Second}, {"ombi-4-get_all_requests", time.Minute},
		{"ombi-4-get_issue_counts", 90 * time.Second}}
	if len(jobs) != len(want) {
		t.Fatalf("jobs = %+v", jobs)
	}
	for i, w := range want {
		if jobs[i].Name != w.name || jobs[i].Interval != w.interval || jobs[i].Once || jobs[i].Run == nil {
			t.Errorf("job %d = %+v, want %+v", i, jobs[i], w)
		}
	}
	srv.RequestTypeCounts, srv.RequestTotalCounts, srv.IssueStatusCounts = false, false, false
	if jobs := New(srv, collector.Deps{}).Jobs(); len(jobs) != 0 {
		t.Errorf("disabled jobs scheduled: %+v", jobs)
	}
}

func TestStatus(t *testing.T) {
	cases := []struct {
		denied, approved, available py.Value
		want                        int64
	}{
		{py.Bool(true), py.Bool(true), py.Bool(true), 0},
		{py.None, py.Bool(true), py.Bool(true), 2},
		{py.Bool(false), py.Int(1), py.String(""), 1},
		{py.Value{}, py.Value{}, py.Bool(true), 3},
		{py.Bool(false), py.Bool(false), py.Bool(false), 3},
	}
	for _, c := range cases {
		if got := status(c.denied, c.approved, c.available); got != c.want {
			t.Errorf("status(%v, %v, %v) = %d, want %d", c.denied, c.approved, c.available, got, c.want)
		}
	}
}

func TestUnreachableServer(t *testing.T) {
	capture := &testutil.Capture{}
	api := New(config.OmbiServer{Common: config.Common{ID: 1, URL: "http://127.0.0.1:1"}},
		collector.Deps{Writer: capture})
	ctx := context.Background()
	for _, run := range []func(context.Context) error{api.GetRequestCounts, api.GetIssueCounts, api.GetAllRequests} {
		if err := run(ctx); err != nil {
			t.Error(err)
		}
	}
	if capture.Lines() != "" {
		t.Errorf("wrote %q", capture.Lines())
	}
}
