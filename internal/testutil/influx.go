package testutil

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// FakeInflux is a minimal InfluxDB 1.8 HTTP API (/ping, /query, /write) like tools/pygolden's.
type FakeInflux struct {
	*httptest.Server
	mu        sync.Mutex
	databases []string
	queries   []string
	writes    []string
	notify    chan struct{}
}

// StartInflux runs a fake InfluxDB until the test ends. databases lists the existing databases.
func StartInflux(t testing.TB, databases ...string) *FakeInflux {
	f := &FakeInflux{databases: databases, notify: make(chan struct{}, 1024)}
	f.Server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.Close)
	return f
}

// HostPort returns the server's host and port for an [influxdb] section.
func (f *FakeInflux) HostPort() (string, int) {
	host := strings.TrimPrefix(f.URL, "http://")
	name, port, _ := strings.Cut(host, ":")
	n, _ := strconv.Atoi(port)
	return name, n
}

func (f *FakeInflux) serve(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/ping":
		w.Header().Set("X-Influxdb-Version", "1.8.10")
		w.WriteHeader(http.StatusNoContent)
	case "/query":
		q := r.URL.Query().Get("q")
		f.mu.Lock()
		f.queries = append(f.queries, r.Method+" "+q)
		var body any = map[string]any{"results": []any{map[string]any{"statement_id": 0}}}
		switch {
		case q == "SHOW DATABASES":
			values := [][]any{}
			for _, d := range f.databases {
				values = append(values, []any{d})
			}
			body = map[string]any{"results": []any{map[string]any{"series": []any{
				map[string]any{"name": "databases", "columns": []string{"name"}, "values": values}}}}}
		case strings.HasPrefix(q, "CREATE DATABASE"):
			f.databases = append(f.databases, strings.Trim(strings.TrimPrefix(q, "CREATE DATABASE "), `"`))
		case strings.HasPrefix(q, "SHOW RETENTION POLICIES"):
			body = map[string]any{"results": []any{map[string]any{"series": []any{map[string]any{
				"columns": []string{"name", "duration", "shardGroupDuration", "replicaN", "default"},
				"values":  [][]any{{"varken 30d-1h", "720h0m0s", "1h0m0s", 1, true}}}}}}}
		}
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(body)
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
		data, _ := io.ReadAll(reader)
		f.mu.Lock()
		f.writes = append(f.writes, string(data))
		f.mu.Unlock()
		select {
		case f.notify <- struct{}{}:
		default:
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// Writes returns the write bodies received.
func (f *FakeInflux) Writes() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

// Queries returns the queries received ("METHOD statement").
func (f *FakeInflux) Queries() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.queries...)
}

// Written signals each write.
func (f *FakeInflux) Written() <-chan struct{} { return f.notify }
