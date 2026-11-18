package grafana

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type fakeGrafana struct {
	mu          sync.Mutex
	datasources string
	created     map[string]any
	imported    map[string]any
	importOK    bool
	gnetStatus  int
}

func (f *fakeGrafana) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if u, p, _ := r.BasicAuth(); u != "admin" || p != "secret" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid username or password"}`))
			return
		}
		body, _ := io.ReadAll(r.Body)
		switch r.Method + " " + r.URL.Path {
		case "GET /api/datasources":
			_, _ = w.Write([]byte(f.datasources))
		case "POST /api/datasources":
			_ = json.Unmarshal(body, &f.created)
			_, _ = w.Write([]byte(`{"datasource":{"id":7},"message":"Datasource added"}`))
		case "GET /api/gnet/dashboards/9585":
			if f.gnetStatus != 0 {
				w.WriteHeader(f.gnetStatus)
				return
			}
			_, _ = w.Write([]byte(`{"json":{"title":"Varken Official","panels":[]}}`))
		case "POST /api/dashboards/import":
			_ = json.Unmarshal(body, &f.imported)
			_, _ = w.Write([]byte(`{"imported":` + map[bool]string{true: "true", false: "false"}[f.importOK] + `}`))
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func run(t *testing.T, f *fakeGrafana, mutate func(*Options)) (string, error) {
	t.Helper()
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	o := Defaults()
	o.GrafanaURL, o.Password, o.Out = srv.URL+"/", "secret", &out
	if mutate != nil {
		mutate(&o)
	}
	err := Build(context.Background(), o)
	return out.String(), err
}

func TestCreatesDatasourceAndImports(t *testing.T) {
	f := &fakeGrafana{datasources: `[{"name":"Other","database":"telegraf"}]`, importOK: true}
	out, err := run(t, f, nil)
	if err != nil {
		t.Fatal(err)
	}
	if f.created["name"] != "Varken-Script" || f.created["url"] != "http://influxdb:8086" || f.created["database"] != "varken" {
		t.Errorf("created = %v", f.created)
	}
	inputs := f.imported["inputs"].([]any)
	if len(inputs) != 13 || inputs[0].(map[string]any)["value"] != "Varken-Script" ||
		inputs[2].(map[string]any)["value"] != "Movies 4K" || f.imported["overwrite"] != true {
		t.Errorf("imported = %v", f.imported)
	}
	if !strings.Contains(out, "Created Varken-Script datasource (id:7)") ||
		!strings.Contains(out, `Created dashboard "Varken Official"`) {
		t.Errorf("out = %s", out)
	}
}

func TestReusesExistingDatasource(t *testing.T) {
	f := &fakeGrafana{datasources: `[{"name":"Mine","jsonData":{"dbName":"varken"}}]`, importOK: true}
	out, err := run(t, f, func(o *Options) { o.Docker = false; o.HostIP = "10.0.0.5" })
	if err != nil {
		t.Fatal(err)
	}
	if f.created != nil {
		t.Error("must not create a second datasource")
	}
	if f.imported["inputs"].([]any)[0].(map[string]any)["value"] != "Mine" || !strings.Contains(out, `"Mine"`) {
		t.Errorf("imported = %v", f.imported)
	}

	f = &fakeGrafana{datasources: `[]`, importOK: true}
	if _, err := run(t, f, func(o *Options) { o.Docker = false; o.HostIP = "10.0.0.5" }); err != nil {
		t.Fatal(err)
	}
	if f.created["url"] != "http://10.0.0.5:8086" {
		t.Errorf("url = %v", f.created["url"])
	}
}

func TestFailures(t *testing.T) {
	if _, err := run(t, &fakeGrafana{}, func(o *Options) { o.Password = "wrong" }); err == nil ||
		!strings.Contains(err.Error(), "Check URL/Username/Password") {
		t.Errorf("bad credentials: %v", err)
	}
	if _, err := run(t, &fakeGrafana{datasources: `[]`, gnetStatus: 500}, nil); err == nil ||
		!strings.Contains(err.Error(), "could not download dashboard") {
		t.Errorf("gnet: %v", err)
	}
	if _, err := run(t, &fakeGrafana{datasources: `[]`}, nil); err == nil ||
		!strings.Contains(err.Error(), "import failed") {
		t.Errorf("import: %v", err)
	}
	if err := Build(context.Background(), Options{GrafanaURL: "http://127.0.0.1:1"}); err == nil {
		t.Error("unreachable grafana")
	}
}
