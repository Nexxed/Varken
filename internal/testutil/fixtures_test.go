package testutil

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

func TestCanonical(t *testing.T) {
	cases := map[string]string{
		"/api/v3/queue?pageSize=250&includeSeries=True&page=2": "GET /api/v3/queue?includeSeries=True&page=2&pageSize=250",
		"/x?b=2&a=1&a=0": "GET /x?a=0&a=1&b=2",
		"/x":             "GET /x",
		"/x?q=a+b&e=%3A": "GET /x?e=:&q=a b",
		"/x?flag&empty=": "GET /x?empty=&flag=",
	}
	for in, want := range cases {
		if got := Canonical("GET", in); got != want {
			t.Errorf("Canonical(%q) = %q, want %q", in, got, want)
		}
	}
}

func get(t *testing.T, url string, header map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestFixtureRouting(t *testing.T) {
	once := 1
	text := "plain"
	routes := []Route{
		{Path: "/a", MatchQuery: map[string]any{"page": "2"}, Body: json.RawMessage(`{"page":2}`)},
		{Path: "/a", Times: &once, Body: json.RawMessage(`{"page":1}`)},
		{Path: "/a", Status: 500, BodyText: &text},
		{Path: "/h", MatchHeaders: map[string]any{"X-Api-Key": "k"}, Body: json.RawMessage(`[1]`)},
		{Path: "/h", MatchHeaders: map[string]any{"X-Api-Key": nil}, Status: 401},
		{Path: "/n", MatchQuery: map[string]any{"apikey": nil, "flag": true}, Body: json.RawMessage(`[]`)},
	}
	f := StartFixture(t, routes)
	checks := []struct {
		path   string
		header map[string]string
		status int
		body   string
	}{
		{"/a?page=2", nil, 200, `{"page":2}`},
		{"/a", nil, 200, `{"page":1}`},
		{"/a", nil, 500, "plain"},
		{"/h", map[string]string{"X-Api-Key": "k"}, 200, "[1]"},
		{"/h", nil, 401, ""},
		{"/h", map[string]string{"X-Api-Key": "other"}, 404, "no fixture route"},
		{"/n?flag=True", nil, 200, "[]"},
		{"/n?flag=True&apikey=x", nil, 404, "no fixture route"},
		{"/missing", nil, 404, "no fixture route"},
	}
	for _, c := range checks {
		status, body := get(t, f.URL+c.path, c.header)
		if status != c.status || body != c.body {
			t.Errorf("%s: got %d %q, want %d %q", c.path, status, body, c.status, c.body)
		}
	}
	reqs := f.Requests()
	if len(reqs) != 6 || reqs[0] != "GET /a" || reqs[4] != "GET /n?apikey=x&flag=True" {
		t.Errorf("requests = %v", reqs)
	}
}

func TestCaptureAndGolden(t *testing.T) {
	dir := t.TempDir()
	c := Case{Name: "demo", Dir: dir}
	capture := &Capture{}
	points := []influx.Point{
		{Measurement: "m", Fields: map[string]py.Value{"v": py.Int(1)}, Time: influx.AtNS(5)},
		{Measurement: "m", Fields: map[string]py.Value{"n": py.None}, Time: influx.AtNS(6)},
	}
	if err := capture.Write(context.Background(), points); err != nil {
		t.Fatal(err)
	}
	if capture.Lines() != "m v=1i 5\n" || len(capture.Skipped) != 1 || capture.Calls != 1 {
		t.Fatalf("capture = %q %v", capture.Lines(), capture.Skipped)
	}
	if err := os.WriteFile(filepath.Join(dir, "expected.lp"), []byte("m v=1i 5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "expected.requests"), []byte("GET /x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	CheckGolden(t, c, capture.Lines(), []string{"GET /x"})

	if d := Diff("a\nb\n", "a\nc\n"); !strings.Contains(d, "want: b") || !strings.Contains(d, "got:  c") {
		t.Errorf("diff = %q", d)
	}
}

func TestCaseHelpers(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.json"), []byte(`{"id": 3, "url": "/base"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Case{Name: "x", Dir: dir, NowText: "2026-11-18T16:30:00.123456+00:00", TZ: "America/Chicago",
		Args: map[string]json.RawMessage{"days": json.RawMessage("7")}}
	now := c.Now(t)()
	if now.Location().String() != "America/Chicago" || now.Hour() != 10 || now.Nanosecond() != 123456000 {
		t.Errorf("now = %v", now)
	}
	var srv struct {
		ID  int    `json:"id"`
		URL string `json:"url"`
	}
	c.Server(t, &srv)
	if srv.ID != 3 || srv.URL != "/base" {
		t.Errorf("server = %+v", srv)
	}
	var days int
	if !c.Arg(t, "days", &days) || days != 7 || c.Arg(t, "missing", &days) {
		t.Error("Arg")
	}
	if RepoRoot(t) == "" {
		t.Error("RepoRoot")
	}
}
