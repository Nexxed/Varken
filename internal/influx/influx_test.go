package influx

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	py "github.com/Nexxed/Varken/internal/pycompat"
)

// TestEncodeMatchesPythonClient replays points encoded by influxdb 5.3.2's make_lines (testdata/pycompat).
func TestEncodeMatchesPythonClient(t *testing.T) {
	f, err := os.Open(filepath.Join("..", "..", "testdata", "pycompat", "lines.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for scanner.Scan() {
		n++
		var row struct {
			Point struct {
				Measurement string              `json:"measurement"`
				Tags        map[string]py.Value `json:"tags"`
				Fields      map[string]py.Value `json:"fields"`
				Time        *py.Value           `json:"time"`
			} `json:"point"`
			Line string `json:"line"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		p := Point{Measurement: row.Point.Measurement, Tags: row.Point.Tags, Fields: row.Point.Fields}
		if row.Point.Time != nil && !row.Point.Time.IsNone() {
			if s, ok := row.Point.Time.AsString(); ok {
				p.Time = AtString(s)
			} else if i, ok := row.Point.Time.AsInt(); ok {
				p.Time = AtNS(i)
			}
		}
		var buf bytes.Buffer
		err := AppendLine(&buf, p)
		if pythonInvalid(row.Line) {
			// Python produced a line InfluxDB rejects (no fields), failing the whole write. It is left out here.
			if err == nil {
				t.Errorf("line %d: expected the point to be skipped, got %q", n, buf.String())
			}
			continue
		}
		if err != nil || buf.String() != row.Line {
			t.Errorf("line %d:\n got  %q (%v)\n want %q", n, buf.String(), err, row.Line)
		}
	}
	if n < 100 {
		t.Fatalf("only %d rows", n)
	}
}

// pythonInvalid detects a line with no field set: "measurement[,tags] [timestamp]".
func pythonInvalid(line string) bool {
	line = strings.TrimSuffix(line, "\n")
	inEscape, spaces := false, 0
	for _, r := range line {
		switch {
		case inEscape:
			inEscape = false
		case r == '\\':
			inEscape = true
		case r == ' ':
			spaces++
		case r == '"':
			return false
		}
	}
	return !strings.Contains(line, "=") || (spaces <= 1 && !strings.Contains(afterFirstSpace(line), "="))
}

func afterFirstSpace(line string) string {
	escaped := false
	for i, r := range line {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == ' ' {
			return line[i+1:]
		}
	}
	return ""
}

func TestEncodeSkipsUnrepresentableValues(t *testing.T) {
	points := []Point{
		{Measurement: "m", Fields: map[string]py.Value{"inf": py.Float(math.Inf(1)), "ok": py.Int(1)}, Time: AtNS(1)},
		{Measurement: "m", Fields: map[string]py.Value{"n": py.None}, Time: AtNS(2)},
		{Measurement: "m", Fields: map[string]py.Value{"v": py.Int(1)}, Time: AtString("not a time")},
		{Measurement: "m", Tags: map[string]py.Value{"t": py.Bool(true)}, Fields: map[string]py.Value{"v": py.Float(50)},
			Time: At(time.Date(2026, 1, 2, 3, 4, 5, 6789, time.UTC))},
	}
	body, errs := Encode(points)
	want := "m ok=1i 1\nm,t=True v=50.0 1767323045000006000\n"
	if string(body) != want {
		t.Errorf("got %q, want %q", body, want)
	}
	if len(errs) != 2 {
		t.Errorf("want 2 skipped points, got %v", errs)
	}
	var ee EncodeError
	if !errors.As(errs[0], &ee) || ee.Measurement != "m" || !strings.Contains(errs[1].Error(), "invalid timestamp") {
		t.Errorf("errors = %v", errs)
	}
}

// fakeInflux is a small InfluxDB 1.x for client tests.
type fakeInflux struct {
	mu          sync.Mutex
	databases   []string
	policies    []string
	queries     []string
	writes      []string
	writeStatus []int // consumed per write; 204 when empty
	writeBody   string
	pingFails   int
	auth        []string
}

func (f *fakeInflux) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		user, pass, _ := r.BasicAuth()
		f.auth = append(f.auth, user+":"+pass)
		switch r.URL.Path {
		case "/ping":
			if f.pingFails > 0 {
				f.pingFails--
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			w.Header().Set("X-Influxdb-Version", "1.8.10")
			w.WriteHeader(http.StatusNoContent)
		case "/query":
			q := r.URL.Query().Get("q")
			f.queries = append(f.queries, r.Method+" "+q)
			switch {
			case q == "SHOW DATABASES":
				values := [][]any{}
				for _, d := range f.databases {
					values = append(values, []any{d})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"series": []any{
					map[string]any{"name": "databases", "columns": []string{"name"}, "values": values}}}}})
			case strings.HasPrefix(q, "SHOW RETENTION POLICIES"):
				values := [][]any{}
				for _, p := range f.policies {
					values = append(values, []any{p, "720h0m0s", "1h0m0s", 1, p == RetentionPolicy})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"series": []any{
					map[string]any{"columns": []string{"name", "duration", "shardGroupDuration", "replicaN", "default"},
						"values": values}}}}})
			default:
				if strings.HasPrefix(q, "CREATE DATABASE") {
					f.databases = append(f.databases, "varken")
				}
				if strings.HasPrefix(q, "CREATE RETENTION POLICY") {
					f.policies = append(f.policies, RetentionPolicy)
				}
				_, _ = w.Write([]byte(`{"results":[{"statement_id":0}]}`))
			}
		case "/write":
			var reader io.Reader = r.Body
			if r.Header.Get("Content-Encoding") == "gzip" {
				gz, err := gzip.NewReader(r.Body)
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				reader = gz
			}
			body, _ := io.ReadAll(reader)
			status := http.StatusNoContent
			if len(f.writeStatus) > 0 {
				status, f.writeStatus = f.writeStatus[0], f.writeStatus[1:]
			}
			if status == http.StatusNoContent {
				f.writes = append(f.writes, string(body))
				w.WriteHeader(status)
				return
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"` + f.writeBody + `"}`))
		}
	})
}

func newTestWriter(t *testing.T, fake *fakeInflux) (*Writer, *Client) {
	t.Helper()
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	name, port, _ := strings.Cut(host, ":")
	var portNum int
	for _, c := range port {
		portNum = portNum*10 + int(c-'0')
	}
	client, err := NewClient(Settings{URL: name, Port: portNum, Username: "root", Password: "root"},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(client, slog.New(slog.DiscardHandler))
	w.sleep = func(context.Context, time.Duration) error { return nil }
	return w, client
}

func TestConnectCreatesDatabaseAndPolicy(t *testing.T) {
	fake := &fakeInflux{databases: []string{"_internal"}, policies: []string{"autogen"}, pingFails: 2}
	w, _ := newTestWriter(t, fake)
	if err := w.Connect(context.Background(), 5, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET SHOW DATABASES",
		`POST CREATE DATABASE "varken"`,
		`GET SHOW RETENTION POLICIES ON "varken"`,
		`POST CREATE RETENTION POLICY "varken 30d-1h" ON "varken" DURATION 30d REPLICATION 1 SHARD DURATION 1h DEFAULT`,
		`GET SHOW RETENTION POLICIES ON "varken"`,
	}
	if strings.Join(fake.queries, "\n") != strings.Join(want, "\n") {
		t.Errorf("queries:\n%s", strings.Join(fake.queries, "\n"))
	}
	if w.Retention() != 720*time.Hour {
		t.Errorf("retention = %v", w.Retention())
	}
	for _, a := range fake.auth {
		if a != "root:root" {
			t.Errorf("auth = %q", a)
		}
	}
}

func TestConnectLeavesExistingDatabaseAlone(t *testing.T) {
	fake := &fakeInflux{databases: []string{"varken"}, policies: []string{"autogen"}}
	w, _ := newTestWriter(t, fake)
	if err := w.Connect(context.Background(), 1, time.Millisecond); err != nil {
		t.Fatal(err)
	}
	for _, q := range fake.queries {
		if strings.Contains(q, "CREATE") {
			t.Errorf("unexpected %s", q)
		}
	}
}

func TestConnectGivesUp(t *testing.T) {
	fake := &fakeInflux{pingFails: 10}
	w, _ := newTestWriter(t, fake)
	if err := w.Connect(context.Background(), 3, time.Millisecond); err == nil {
		t.Fatal("expected an error")
	}
}

func point(i int) Point {
	return Point{Measurement: "m", Fields: map[string]py.Value{"v": py.Int(int64(i))}, Time: AtNS(int64(i))}
}

func TestWriteBatchesAndGzips(t *testing.T) {
	fake := &fakeInflux{}
	w, _ := newTestWriter(t, fake)
	w.batchSize = 2
	points := []Point{point(1), point(2), point(3)}
	if err := w.Write(context.Background(), points); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 2 || fake.writes[0] != "m v=1i 1\nm v=2i 2\n" || fake.writes[1] != "m v=3i 3\n" {
		t.Errorf("writes = %q", fake.writes)
	}
	big := make([]Point, 200)
	for i := range big {
		big[i] = point(i)
	}
	w.batchSize = 5000
	if err := w.Write(context.Background(), big); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(fake.writes[2], "m v=0i 0\n") {
		t.Errorf("gzipped body not received intact")
	}
	if err := w.Write(context.Background(), nil); err != nil || len(fake.writes) != 3 {
		t.Error("empty write should send nothing")
	}
}

func TestWriteRetriesServerErrors(t *testing.T) {
	fake := &fakeInflux{writeStatus: []int{500, 503}, writeBody: "busy"}
	w, _ := newTestWriter(t, fake)
	if err := w.Write(context.Background(), []Point{point(1)}); err != nil {
		t.Fatal(err)
	}
	if len(fake.writes) != 1 {
		t.Errorf("writes = %v", fake.writes)
	}
}

func TestWriteGivesUpAfterAttempts(t *testing.T) {
	fake := &fakeInflux{writeStatus: []int{500, 500, 500, 500, 500}, writeBody: "down"}
	w, _ := newTestWriter(t, fake)
	if err := w.Write(context.Background(), []Point{point(1)}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestWriteDoesNotRetryClientErrors(t *testing.T) {
	fake := &fakeInflux{writeStatus: []int{400, 204}, writeBody: "unable to parse"}
	w, _ := newTestWriter(t, fake)
	err := w.Write(context.Background(), []Point{point(1)})
	var werr *WriteError
	if !errors.As(err, &werr) || werr.Status != 400 || werr.Body != "unable to parse" {
		t.Fatalf("err = %v", err)
	}
	if len(fake.writes) != 0 {
		t.Error("a 400 must not be retried")
	}
}

func TestWriteBeyondRetentionIsNotAnError(t *testing.T) {
	fake := &fakeInflux{writeStatus: []int{400},
		writeBody: "partial write: points beyond retention policy dropped=3"}
	w, _ := newTestWriter(t, fake)
	if err := w.Write(context.Background(), []Point{point(1)}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteRetriesConnectionErrors(t *testing.T) {
	client, err := NewClient(Settings{URL: "127.0.0.1", Port: 1}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(client, slog.New(slog.DiscardHandler))
	calls := 0
	w.sleep = func(context.Context, time.Duration) error { calls++; return nil }
	if err := w.Write(context.Background(), []Point{point(1)}); err == nil {
		t.Fatal("expected an error")
	}
	if calls != w.attempts-1 {
		t.Errorf("slept %d times", calls)
	}
}

func TestClientURLAndDatabase(t *testing.T) {
	c, err := NewClient(Settings{URL: "influx.local/sub", Port: 8086, SSL: true, Database: "shadow"},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if got := c.endpoint("/write", nil); got != "https://influx.local:8086/sub/write" {
		t.Errorf("endpoint = %s", got)
	}
	if c.Database() != "shadow" {
		t.Error("database")
	}
	if _, err := NewClient(Settings{URL: ""}, slog.New(slog.DiscardHandler)); err == nil {
		t.Error("empty url should fail")
	}
}

func FuzzAppendLine(f *testing.F) {
	f.Add("m", "k", "v", "f", "s", int64(1))
	f.Add("a b", "c,d", "e=f", "g\\", "h\"i\nj", int64(-5))
	f.Fuzz(func(t *testing.T, m, tk, tv, fk, fv string, ns int64) {
		p := Point{Measurement: m, Tags: map[string]py.Value{tk: py.String(tv)},
			Fields: map[string]py.Value{fk: py.String(fv)}, Time: AtNS(ns)}
		var buf bytes.Buffer
		if err := AppendLine(&buf, p); err != nil {
			return
		}
		line := buf.String()
		if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
			t.Fatalf("line breaks in %q", line)
		}
	})
}
