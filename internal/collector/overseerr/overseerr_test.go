package overseerr

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/testutil"
)

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
	testutil.RunGolden(t, "overseerr", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.OverseerrServer
		loadServer(t, c, &srv)
		srv.URL = env.URL
		api := New(srv, env.Deps)
		var err error
		switch c.Job {
		case "get_request_counts":
			err = api.GetRequestCounts(env.Ctx)
		case "get_latest_requests":
			err = api.GetLatestRequests(env.Ctx)
		default:
			t.Fatalf("unknown job %s", c.Job)
		}
		if err != nil {
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
	srv := config.OverseerrServer{Common: config.Common{ID: 2}, GetRequestTotalCounts: true,
		RequestTotalRunSeconds: 30, NumLatestRequestsToFetch: 10, NumLatestRequestsSeconds: 45}
	jobs := New(srv, collector.Deps{}).Jobs()
	if len(jobs) != 2 || jobs[0].Name != "overseerr-2-get_request_counts" || jobs[0].Interval != 30*time.Second ||
		jobs[1].Name != "overseerr-2-get_latest_requests" || jobs[1].Interval != 45*time.Second {
		t.Fatalf("jobs = %+v", jobs)
	}
	// get_latest_requests = false does not switch the job off (as in Python); fetching 0 requests does.
	srv.GetRequestTotalCounts, srv.GetLatestRequests, srv.NumLatestRequestsToFetch = false, false, 0
	if jobs := New(srv, collector.Deps{}).Jobs(); len(jobs) != 0 {
		t.Errorf("jobs = %+v", jobs)
	}
}

// TestTitleCache checks that a second run reuses the looked-up titles and still writes every request.
func TestTitleCache(t *testing.T) {
	var lookups atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/request", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("take") != "2" {
			t.Errorf("query = %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"results": [
			{"type": "movie", "createdAt": "d1", "media": {"tmdbId": 603, "status": 5}, "requestedBy": {"displayName": "A"}},
			{"type": "movie", "createdAt": "d2", "media": {"tmdbId": 603, "status": 5}, "requestedBy": {"displayName": "B"}}]}`))
	})
	mux.HandleFunc("/api/v1/movie/603", func(w http.ResponseWriter, r *http.Request) {
		lookups.Add(1)
		_, _ = w.Write([]byte(`{"id": 603, "title": "The Matrix", "mediaInfo": {"status": 5}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	capture := &testutil.Capture{}
	now := time.Date(2026, 11, 18, 16, 30, 0, 0, time.UTC)
	api := New(config.OverseerrServer{Common: config.Common{ID: 1, URL: srv.URL}, NumLatestRequestsToFetch: 2},
		collector.Deps{Writer: capture, Now: func() time.Time { return now }})
	for i := 0; i < 2; i++ {
		if err := api.GetLatestRequests(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	lines := strings.Split(strings.TrimSpace(capture.Lines()), "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %q", lines)
	}
	if !strings.Contains(lines[0], "requested_user=A") || !strings.Contains(lines[1], "requested_user=B") {
		t.Errorf("each request should keep its own user: %q", lines)
	}
	// Both requests share the media: one lookup for the first run, none for the second.
	if n := lookups.Load(); n != 1 {
		t.Errorf("lookups = %d", n)
	}
	before := lookups.Load()
	if err := api.GetLatestRequests(context.Background()); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != before {
		t.Error("cached title was looked up again")
	}
}

func TestCacheIsBounded(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/tv/1", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id": 1, "name": "Show", "mediaInfo": {"status": 3}}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	api := New(config.OverseerrServer{Common: config.Common{URL: srv.URL}}, collector.Deps{})
	for i := 0; i < maxCachedTitles; i++ {
		api.titles[strconv.Itoa(i)] = media{}
	}
	m, ok := api.lookup(context.Background(), "tv", "1")
	if !ok || m.title.String() != "Show" || m.status.String() != "3" || len(api.titles) != 1 {
		t.Errorf("lookup = %+v %v, cache size %d", m, ok, len(api.titles))
	}
}
