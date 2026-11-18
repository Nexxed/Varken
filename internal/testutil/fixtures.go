// Package testutil runs the golden fixtures in testdata/fixtures: it serves a case's routes exactly like
// tools/pygolden/fakes.py, captures what a collector writes, and compares it with what the Python version wrote.
package testutil

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/influx"
)

// Update rewrites expected.lp and expected.requests of "fix" cases from the Go output, for review with git diff.
// Python-owned expectations are never touched.
var Update = flag.Bool("update", false, "rewrite expected output of fix cases from the Go output")

// Route is one fixture route (see tools/pygolden/fakes.py).
type Route struct {
	Method       string            `json:"method"`
	Path         string            `json:"path"`
	Status       int               `json:"status"`
	MatchQuery   map[string]any    `json:"match_query"`
	MatchHeaders map[string]any    `json:"match_headers"`
	Headers      map[string]string `json:"headers"`
	Times        *int              `json:"times"`
	BodyText     *string           `json:"body_text"`
	Body         json.RawMessage   `json:"body"`
}

// Case is one testdata/fixtures/<service>/<name>/case.json.
type Case struct {
	Name          string                     `json:"-"`
	Dir           string                     `json:"-"`
	Job           string                     `json:"job"`
	Description   string                     `json:"description"`
	Fix           string                     `json:"fix"`
	Args          map[string]json.RawMessage `json:"args"`
	NowText       string                     `json:"now"`
	TZ            string                     `json:"tz"`
	BasePath      string                     `json:"base_path"`
	Routes        []Route                    `json:"routes"`
	Influx        json.RawMessage            `json:"influx"`
	PublicIP      string                     `json:"public_ip"`
	PublicIPError bool                       `json:"public_ip_error"`
}

// RepoRoot finds the module root from the test's working directory.
func RepoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// LoadCases returns every case of a service, sorted by name.
func LoadCases(t testing.TB, service string) []Case {
	t.Helper()
	dirs, err := filepath.Glob(filepath.Join(RepoRoot(t), "testdata", "fixtures", service, "*", "case.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(dirs) == 0 {
		t.Fatalf("no fixtures for %s", service)
	}
	sort.Strings(dirs)
	cases := make([]Case, 0, len(dirs))
	for _, path := range dirs {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var c Case
		if err := json.Unmarshal(data, &c); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		c.Dir = filepath.Dir(path)
		c.Name = filepath.Base(c.Dir)
		cases = append(cases, c)
	}
	return cases
}

// Location is the case's TZ.
func (c Case) Location(t testing.TB) *time.Location {
	name := c.TZ
	if name == "" {
		name = "UTC"
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("%s: %v", c.Name, err)
	}
	return loc
}

// Now is the case's frozen clock, in its TZ.
func (c Case) Now(t testing.TB) func() time.Time {
	ts, err := time.Parse(time.RFC3339Nano, c.NowText)
	if err != nil {
		t.Fatalf("%s: now: %v", c.Name, err)
	}
	ts = ts.In(c.Location(t))
	return func() time.Time { return ts }
}

// Server decodes server.json (the settings the Python job ran with) into out.
func (c Case) Server(t testing.TB, out any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(c.Dir, "server.json"))
	if err != nil {
		t.Fatalf("%s: run tools/pygolden/harness.py first: %v", c.Name, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatalf("%s: server.json: %v", c.Name, err)
	}
}

// Arg decodes a job argument; ok is false when the case does not set it.
func (c Case) Arg(t testing.TB, name string, out any) bool {
	raw, ok := c.Args[name]
	if !ok {
		return false
	}
	if err := json.Unmarshal(raw, out); err != nil {
		t.Fatalf("%s: arg %s: %v", c.Name, name, err)
	}
	return true
}

// Fixture is a running fixture server.
type Fixture struct {
	*httptest.Server
	mu       sync.Mutex
	routes   []Route
	times    []int
	requests []string
	Headers  []http.Header
}

// StartFixture serves routes until the test ends.
func StartFixture(t testing.TB, routes []Route) *Fixture {
	f := &Fixture{routes: routes, times: make([]int, len(routes))}
	for i, r := range routes {
		f.times[i] = -1
		if r.Times != nil {
			f.times[i] = *r.Times
		}
	}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// Canonical is "METHOD /path?k=v&..." with the query decoded and sorted, as tools/pygolden records it.
func Canonical(method, rawPath string) string {
	u, err := url.Parse(rawPath)
	if err != nil {
		return method + " " + rawPath
	}
	pairs := [][2]string{}
	for _, part := range strings.Split(u.RawQuery, "&") {
		if part == "" {
			continue
		}
		k, v, _ := strings.Cut(part, "=")
		k, _ = url.QueryUnescape(k)
		v, _ = url.QueryUnescape(v)
		pairs = append(pairs, [2]string{k, v})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i][0] != pairs[j][0] {
			return pairs[i][0] < pairs[j][0]
		}
		return pairs[i][1] < pairs[j][1]
	})
	text := method + " " + u.Path
	for i, p := range pairs {
		sep := "&"
		if i == 0 {
			sep = "?"
		}
		text += sep + p[0] + "=" + p[1]
	}
	return text
}

func valueText(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", false
	case string:
		return t, true
	case bool:
		if t {
			return "True", true
		}
		return "False", true
	case float64:
		return strings.TrimSuffix(fmt.Sprintf("%v", t), ".0"), true
	}
	return fmt.Sprint(v), true
}

func (r Route) matches(req *http.Request) bool {
	method := r.Method
	if method == "" {
		method = http.MethodGet
	}
	if method != req.Method || r.Path != req.URL.Path {
		return false
	}
	query := req.URL.Query()
	for key, want := range r.MatchQuery {
		text, present := valueText(want)
		if !present {
			if query.Has(key) {
				return false
			}
			continue
		}
		if !query.Has(key) || query.Get(key) != text {
			return false
		}
	}
	for key, want := range r.MatchHeaders {
		text, present := valueText(want)
		actual := req.Header.Values(key)
		if !present {
			if len(actual) > 0 {
				return false
			}
			continue
		}
		if len(actual) == 0 || actual[0] != text {
			return false
		}
	}
	return true
}

func (f *Fixture) serve(w http.ResponseWriter, req *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, Canonical(req.Method, req.URL.RequestURI()))
	f.Headers = append(f.Headers, req.Header.Clone())
	var route *Route
	for i := range f.routes {
		if f.times[i] == 0 {
			continue
		}
		if f.routes[i].matches(req) {
			route = &f.routes[i]
			if f.times[i] > 0 {
				f.times[i]--
			}
			break
		}
	}
	f.mu.Unlock()
	if route == nil {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte("no fixture route"))
		return
	}
	var body []byte
	contentType := "text/plain"
	switch {
	case route.BodyText != nil:
		body = []byte(*route.BodyText)
	case route.Body != nil:
		body = route.Body
		contentType = "application/json"
	}
	w.Header().Set("Content-Type", contentType)
	for k, v := range route.Headers {
		w.Header().Add(k, v)
	}
	status := route.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// Requests returns the sorted, de-duplicated canonical requests received.
func (f *Fixture) Requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, r := range f.requests {
		if !seen[r] {
			seen[r] = true
			out = append(out, r)
		}
	}
	sort.Strings(out)
	return out
}

// Capture is an influx.PointWriter that keeps the encoded line protocol.
type Capture struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	Skipped []error
	Calls   int
}

// Write implements influx.PointWriter.
func (c *Capture) Write(_ context.Context, points []influx.Point) error {
	body, skipped := influx.Encode(points)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.Calls++
	c.buf.Write(body)
	c.Skipped = append(c.Skipped, skipped...)
	return nil
}

// Lines returns everything written so far.
func (c *Capture) Lines() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}

// CheckGolden compares a job's output and requests with the case's expected files.
func CheckGolden(t *testing.T, c Case, lines string, requests []string) {
	t.Helper()
	expectedLP := filepath.Join(c.Dir, "expected.lp")
	expectedReq := filepath.Join(c.Dir, "expected.requests")
	reqText := ""
	if len(requests) > 0 {
		reqText = strings.Join(requests, "\n") + "\n"
	}
	if *Update && c.Fix != "" {
		writeOrRemove(t, expectedLP, lines)
		writeOrRemove(t, expectedReq, reqText)
		return
	}
	want, _ := os.ReadFile(expectedLP)
	if lines != string(want) {
		t.Errorf("%s: line protocol differs from %s\n%s", c.Name, rel(expectedLP), Diff(string(want), lines))
	}
	if wantReq, err := os.ReadFile(expectedReq); err == nil || c.Fix == "" {
		if reqText != string(wantReq) {
			t.Errorf("%s: requests differ from %s\n%s", c.Name, rel(expectedReq), Diff(string(wantReq), reqText))
		}
	}
}

func rel(path string) string {
	if i := strings.Index(path, "testdata"); i >= 0 {
		return path[i:]
	}
	return path
}

func writeOrRemove(t *testing.T, path, text string) {
	if text == "" {
		_ = os.Remove(path)
		return
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Diff is a small line diff for test failures.
func Diff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	n := max(len(w), len(g))
	shown := 0
	for i := 0; i < n && shown < 40; i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			fmt.Fprintf(&b, "line %d\n  want: %s\n  got:  %s\n", i+1, wl, gl)
			shown++
		}
	}
	return b.String()
}

// Logger returns a logger that writes to the test log.
func Logger(t testing.TB) *slog.Logger {
	return slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

type testWriter struct{ t testing.TB }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Helper()
	w.t.Log(strings.TrimRight(string(p), "\n"))
	return len(p), nil
}
