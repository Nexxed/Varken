package tautulli

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/geoip"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
	"github.com/Nexxed/Varken/internal/testutil"
)

func TestJobsFollowVarkenSchedule(t *testing.T) {
	srv := config.TautulliServer{Common: config.Common{ID: 2},
		GetActivity: true, GetActivityRunSeconds: 30, GetStats: true, GetStatsRunSeconds: 3600,
		GetServer: true, GetServerRunSeconds: 300, GetHistory: true, GetHistoryRunSeconds: 300, HistoryDays: 2,
		HistoryBackfillDays: 364, GetDailyStats: true, GetDailyStatsRunSeconds: 3600}
	jobs := New(srv, collector.Deps{}, nil).Jobs()
	want := []struct {
		name     string
		interval time.Duration
		once     bool
	}{
		{"tautulli-2-get_activity", 30 * time.Second, false},
		{"tautulli-2-get_stats", time.Hour, false},
		{"tautulli-2-get_server_status", 5 * time.Minute, false},
		{"tautulli-2-get_history_plays", 5 * time.Minute, false},
		{"tautulli-2-get_history_plays_backfill", 0, true},
		{"tautulli-2-get_concurrent_streams", time.Hour, false},
		{"tautulli-2-get_users", time.Hour, false},
		{"tautulli-2-get_library_usage", time.Hour, false},
		{"tautulli-2-get_recently_added", time.Hour, false},
	}
	if len(jobs) != len(want) {
		t.Fatalf("got %d jobs", len(jobs))
	}
	for i, w := range want {
		if jobs[i].Name != w.name || jobs[i].Interval != w.interval || jobs[i].Once != w.once || jobs[i].Run == nil {
			t.Errorf("job %d = %+v, want %+v", i, jobs[i], w)
		}
	}

	srv.HistoryBackfillDays = 2
	srv.GetActivity, srv.GetStats, srv.GetServer, srv.GetDailyStats = false, false, false, false
	jobs = New(srv, collector.Deps{}, nil).Jobs()
	if len(jobs) != 1 || jobs[0].Name != "tautulli-2-get_history_plays" {
		t.Errorf("jobs = %+v", jobs)
	}
	srv.GetHistory = false
	if jobs := New(srv, collector.Deps{}, nil).Jobs(); len(jobs) != 0 {
		t.Errorf("jobs = %+v", jobs)
	}
}

func TestMappingTables(t *testing.T) {
	str := py.String
	if decisionTitle(str("copy")) != "Direct Stream" || decisionTitle(str("direct play")) != "Direct Play" ||
		decisionTitle(py.None) != "" {
		t.Error("decisionTitle")
	}
	if videoDecision(str("")) != "Music" || videoDecision(str("copy")) != "Direct Stream" ||
		videoDecision(str("transcode")) != "Transcode" {
		t.Error("videoDecision")
	}
	qualities := []struct {
		res, full, container py.Value
		want                 string
	}{
		{str(""), py.None, str("flac"), "FLAC"},
		{py.None, py.None, py.None, ""},
		{str("sd"), py.None, py.None, "SD"},
		{str("SD"), str("x"), py.None, "SD"},
		{str("4k"), str("4k"), py.None, "4K"},
		{str("1080"), str("1080p"), py.None, "1080p"},
		{str("720"), str(""), py.None, "720p"},
		{py.Int(480), py.None, py.None, "480p"},
	}
	for _, q := range qualities {
		got := quality(session{StreamVideoResolution: q.res, StreamVideoFullRes: q.full, Container: q.container})
		if got.String() != q.want {
			t.Errorf("quality(%s, %s) = %s, want %s", q.res.Repr(), q.full.Repr(), got, q.want)
		}
	}
	states := map[string]string{"Playing": "0", "paused": "1", "BUFFERING": "3", "stopped": "stopped", "": ""}
	for in, want := range states {
		if got := playerState(str(in)).String(); got != want {
			t.Errorf("playerState(%q) = %s", in, got)
		}
	}
	if playerState(str("playing")).Kind() != py.IntKind {
		t.Error("player_state for a known state must be an int")
	}
	platforms := []struct{ platform, version, wantName, wantVersion string }{
		{"osx", "1.2.3-abc", "macOS", "1.2.3"},
		{"windows", "1.5-x-y", "Windows", "1.5"},
		{"Roku", "9.4.0", "Roku", "9.4.0"},
		{"Chrome", "4.1-beta", "Chrome", "4.1-beta"},
	}
	for _, p := range platforms {
		name, version := platform(session{Platform: str(p.platform), ProductVersion: str(p.version)})
		if name.String() != p.wantName || version.String() != p.wantVersion {
			t.Errorf("platform(%s) = %s %s", p.platform, name, version)
		}
	}
}

func TestPythonIntegerArithmetic(t *testing.T) {
	cases := []struct{ a, b, div, mod int64 }{
		{7, 1000, 0, 7}, {-1500, 1000, -2, 500}, {-1000, 1000, -1, 0}, {1234567, 1000000, 1, 234567},
		{-5, 1000000, -1, 999995},
	}
	for _, c := range cases {
		if floorDiv(c.a, c.b) != c.div || floorMod(c.a, c.b) != c.mod {
			t.Errorf("%d //,%% %d = %d, %d", c.a, c.b, floorDiv(c.a, c.b), floorMod(c.a, c.b))
		}
	}
}

func TestParseDayMatchesStrptime(t *testing.T) {
	good := map[string]string{"2026-11-16": "2026-11-16", "2026-1-5": "2026-01-05", "2026-01- 5": "2026-01-05",
		"2024-02-29": "2024-02-29"}
	for in, want := range good {
		day, ok := parseDay(py.String(in))
		if !ok || day.Format("2006-01-02") != want {
			t.Errorf("parseDay(%q) = %v %v", in, day, ok)
		}
	}
	for _, bad := range []string{"2026-02-30", "2025-02-29", "26-1-1", "2026-13-01", "2026-11-16x", "0000-01-01", ""} {
		if _, ok := parseDay(py.String(bad)); ok {
			t.Errorf("parseDay(%q) should fail", bad)
		}
	}
	if _, ok := parseDay(py.Int(5)); ok {
		t.Error("a number is not a day")
	}
}

func TestLocationTags(t *testing.T) {
	tags := locationTags(geoip.Location{City: "", HasCity: true, Latitude: 1, HasLatitude: true})
	if tags["location"].String() != unknownCity || tags["full_location"].String() != "None - " ||
		tags["latitude"].String() != "37.234332396" || !tags["region_code"].IsNone() {
		t.Errorf("tags = %v", tags)
	}
}

// fakeTautulli answers every command with fn(cmd, query).
func fakeTautulli(t *testing.T, fn func(w http.ResponseWriter, r *http.Request)) string {
	srv := httptest.NewServer(http.HandlerFunc(fn))
	t.Cleanup(srv.Close)
	return srv.URL
}

func ok(data any) []byte {
	b, _ := json.Marshal(map[string]any{"response": map[string]any{"result": "success", "data": data}})
	return b
}

func TestHeaderRejectedTwiceStops(t *testing.T) {
	var calls atomic.Int32
	url := fakeTautulli(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	})
	capture := &testutil.Capture{}
	api := New(config.TautulliServer{Common: config.Common{ID: 1, URL: url, APIKey: "k"}},
		collector.Deps{Writer: capture}, nil)
	if err := api.GetActivity(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || capture.Lines() != "" {
		t.Errorf("calls=%d lines=%q", calls.Load(), capture.Lines())
	}
	if _, ok := api.call(context.Background(), "x"); ok {
		t.Error("a second 401 must not retry again")
	}
}

func TestPublicIPIsReResolvedAfterAnHour(t *testing.T) {
	var lookups atomic.Int32
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		lookups.Add(1)
		_, _ = w.Write([]byte("203.0.113.40"))
	}))
	defer public.Close()
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	api := New(config.TautulliServer{FallbackIP: "100.64.0.1"},
		collector.Deps{Now: func() time.Time { return now }}, openTestGeoIP(t))
	api.PublicIPURL = public.URL
	for i := 0; i < 3; i++ {
		if loc := api.locate(context.Background(), py.String("10.0.0.1")); loc.City != "Springfield" {
			t.Fatalf("loc = %+v", loc)
		}
	}
	if lookups.Load() != 1 {
		t.Errorf("lookups = %d, want the address reused", lookups.Load())
	}
	now = now.Add(61 * time.Minute)
	api.locate(context.Background(), py.String("10.0.0.1"))
	if lookups.Load() != 2 {
		t.Errorf("lookups = %d, want a fresh lookup after an hour", lookups.Load())
	}
}

func TestPublicIPLookupRejectsGarbage(t *testing.T) {
	public := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html>captive portal</html>"))
	}))
	defer public.Close()
	api := New(config.TautulliServer{FallbackIP: "1.1.1.1"}, collector.Deps{}, nil)
	api.PublicIPURL = public.URL
	if ip := api.lookupPublicIP(context.Background()); ip != "1.1.1.1" {
		t.Errorf("ip = %q, want the fallback", ip)
	}
	api.PublicIPURL = "http://127.0.0.1:1/"
	if ip := api.lookupPublicIP(context.Background()); ip != "1.1.1.1" {
		t.Errorf("ip = %q, want the fallback", ip)
	}
	api.PublicIPURL = "::bad url"
	if ip := api.lookupPublicIP(context.Background()); ip != "1.1.1.1" {
		t.Errorf("ip = %q, want the fallback", ip)
	}
}

func TestCachedPublicIPThatFailsIsUnknown(t *testing.T) {
	api := New(config.TautulliServer{FallbackIP: "100.64.0.1"}, collector.Deps{}, openTestGeoIP(t))
	api.setIP("10.9.9.9")
	if loc := api.locate(context.Background(), py.String("bad")); loc.HasCity || loc.HasLatitude {
		t.Errorf("loc = %+v", loc)
	}
}

func TestHistoryRespectsRetentionAndPages(t *testing.T) {
	var afters []string
	url := fakeTautulli(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch q.Get("cmd") {
		case "get_libraries":
			_, _ = w.Write(ok([]any{map[string]any{"section_id": 1, "section_name": "Movies"}}))
		case "get_history":
			afters = append(afters, q.Get("after"))
			_, _ = w.Write(ok(map[string]any{"recordsFiltered": 1,
				"data": []any{map[string]any{"id": 5, "stopped": 1795000000}}}))
		}
	})
	capture := &testutil.Capture{}
	now := time.Date(2026, 11, 18, 10, 0, 0, 0, time.UTC)
	api := New(config.TautulliServer{Common: config.Common{ID: 1, URL: url}}, collector.Deps{
		Writer: capture, Now: func() time.Time { return now },
		Retention: func() time.Duration { return 720 * time.Hour }}, nil)
	if err := api.HistoryPlays(context.Background(), 364); err != nil {
		t.Fatal(err)
	}
	if len(afters) != 1 || afters[0] != "2026-10-19" {
		t.Errorf("after = %v, want 30 days back", afters)
	}
	if !strings.Contains(capture.Lines(), "row_id=5i") {
		t.Errorf("lines = %q", capture.Lines())
	}
}

type failingWriter struct{}

func (failingWriter) Write(context.Context, []influx.Point) error { return errors.New("influx down") }

func TestHistoryReportsWriteErrors(t *testing.T) {
	url := fakeTautulli(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("cmd") {
		case "get_libraries":
			_, _ = w.Write(ok([]any{map[string]any{"section_id": 1}}))
		default:
			_, _ = w.Write(ok(map[string]any{"recordsFiltered": 1, "data": []any{map[string]any{"id": 5, "stopped": 9}}}))
		}
	})
	api := New(config.TautulliServer{Common: config.Common{URL: url}}, collector.Deps{Writer: failingWriter{}}, nil)
	if err := api.HistoryPlays(context.Background(), 1); err == nil {
		t.Error("expected the write error")
	}
}

func TestMalformedAnswersWriteNothing(t *testing.T) {
	answers := map[string]string{
		"get_activity":                          `{"response": "nope"}`,
		"get_libraries":                         `{"response": {"result": "success", "data": {"not": "a list"}}}`,
		"get_users_table":                       `{"response": {"result": "success", "data": []}}`,
		"get_libraries_table":                   `{"response": {"result": "error", "data": {"data": []}}}`,
		"get_recently_added":                    `[1, 2]`,
		"get_concurrent_streams_by_stream_type": `{"response": {"result": "success", "data": "x"}}`,
	}
	url := fakeTautulli(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(answers[r.URL.Query().Get("cmd")]))
	})
	capture := &testutil.Capture{}
	api := New(config.TautulliServer{Common: config.Common{URL: url}}, collector.Deps{Writer: capture}, nil)
	ctx := context.Background()
	for _, run := range []func(context.Context) error{api.GetActivity, api.GetStats, api.Users, api.LibraryUsage,
		api.RecentlyAdded, func(ctx context.Context) error { return api.ConcurrentStreams(ctx, 30) },
		func(ctx context.Context) error { return api.HistoryPlays(ctx, 2) }} {
		if err := run(ctx); err != nil {
			t.Error(err)
		}
	}
	if capture.Lines() != "" {
		t.Errorf("lines = %q", capture.Lines())
	}
	if api.String() != "<tautulli-0>" {
		t.Error(api.String())
	}
}
