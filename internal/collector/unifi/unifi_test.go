package unifi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	testutil.RunGolden(t, "unifi", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.UniFiServer
		loadServer(t, c, &srv)
		srv.URL = env.URL
		if c.Job != "get_usg_stats" {
			t.Fatalf("unknown job %s", c.Job)
		}
		if err := New(srv, env.Deps).GetUSGStats(env.Ctx); err != nil {
			t.Fatal(err)
		}
		// Every request after a successful login carries the session cookie.
		loggedIn := false
		for i, h := range env.Fixture.Headers {
			if loggedIn && !strings.Contains(h.Get("Cookie"), "unifises=SESSION123") {
				t.Errorf("request %d has no session cookie: %q", i, h.Get("Cookie"))
			}
			if i == 0 && c.Name != "usg_login_fails" {
				loggedIn = true
			}
		}
	})
}

func TestJobs(t *testing.T) {
	jobs := New(config.UniFiServer{Common: config.Common{ID: 5}, GetUSGStatsRunSeconds: 60}, collector.Deps{}).Jobs()
	if len(jobs) != 1 || jobs[0].Name != "unifi-5-get_usg_stats" || jobs[0].Interval != time.Minute {
		t.Fatalf("jobs = %+v", jobs)
	}
}

// TestLazyLoginAndRetryState checks that New does no I/O, that the login happens once, and that the one-shot
// re-login is re-armed after a run is disregarded.
func TestLazyLoginAndRetryState(t *testing.T) {
	var logins, stats atomic.Int32
	var fail atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("/api/login", func(w http.ResponseWriter, r *http.Request) {
		logins.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["username"] != "u" || body["password"] != "p" || body["remember"] != true {
			t.Errorf("login body = %v", body)
		}
		http.SetCookie(w, &http.Cookie{Name: "unifises", Value: "S", Path: "/"})
		http.SetCookie(w, &http.Cookie{Name: "csrf_token", Value: "C", Path: "/"})
	})
	mux.HandleFunc("/api/self/sites", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data": [{"name": "default", "desc": "Default"}]}`))
	})
	mux.HandleFunc("/api/s/default/stat/device", func(w http.ResponseWriter, r *http.Request) {
		stats.Add(1)
		if r.Header.Get("Cookie") != "csrf_token=C; unifises=S" {
			t.Errorf("cookie = %q", r.Header.Get("Cookie"))
		}
		if fail.Load() {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"meta": {"msg": "api.err.LoginRequired"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data": [{"name": "gw", "model": "UXG", "wan1": {"bytes-r": 1, "rx_bytes": 2,
			"rx_bytes-r": 3, "tx_bytes": 4, "tx_bytes-r": 5}, "sys_stats": {"loadavg_1": "1", "loadavg_5": 2,
			"loadavg_15": 3.5}, "system-stats": {"cpu": "4", "mem": "5"}}]}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	capture := &testutil.Capture{}
	api := New(config.UniFiServer{Common: config.Common{ID: 1, URL: srv.URL}, Username: "u", Password: "p",
		Site: "default", USGName: "gw"}, collector.Deps{Writer: capture,
		Now: func() time.Time { return time.Unix(10, 0) }})
	if logins.Load() != 0 {
		t.Fatal("New logged in")
	}
	ctx := context.Background()
	if err := api.GetUSGStats(ctx); err != nil {
		t.Fatal(err)
	}
	want := "UniFi,model=UXG,name=gw bytes_current=1.0,cpu_loadavg_1=1.0,cpu_loadavg_15=3.5,cpu_loadavg_5=2.0," +
		"cpu_util=4.0,mem_util=5.0,rx_bytes_current=3.0,rx_bytes_total=2.0,tx_bytes_current=5.0,tx_bytes_total=4.0 " +
		"10000000000\n"
	if capture.Lines() != want {
		t.Errorf("got %q", capture.Lines())
	}
	fail.Store(true)
	_ = api.GetUSGStats(ctx) // fails, logs in again, fails again: disregarded
	if logins.Load() != 2 || stats.Load() != 3 {
		t.Errorf("logins = %d, stats = %d", logins.Load(), stats.Load())
	}
	_ = api.GetUSGStats(ctx) // the retry is armed again
	if logins.Load() != 3 || stats.Load() != 5 {
		t.Errorf("logins = %d, stats = %d", logins.Load(), stats.Load())
	}
	fail.Store(false)
	if err := api.GetUSGStats(ctx); err != nil || strings.Count(capture.Lines(), "\n") != 2 {
		t.Errorf("err = %v, lines = %q", err, capture.Lines())
	}
}

func TestUSGFieldsRejectsNonNumbers(t *testing.T) {
	var d device
	if err := json.Unmarshal([]byte(`{"wan1": {"bytes-r": "x", "rx_bytes": 1, "rx_bytes-r": 1, "tx_bytes": 1,
		"tx_bytes-r": 1}, "sys_stats": {"loadavg_1": 1, "loadavg_5": 1, "loadavg_15": 1},
		"system-stats": {"cpu": 1, "mem": 1}}`), &d); err != nil {
		t.Fatal(err)
	}
	if _, missing := usgFields(&d); !strings.Contains(missing, "bytes-r") {
		t.Errorf("missing = %q", missing)
	}
	for _, body := range []string{`{}`, `{"wan1": {}}`, `{"wan1": {}, "sys_stats": {}}`,
		`{"wan1": {}, "sys_stats": {}, "system-stats": {}}`} {
		var d device
		_ = json.Unmarshal([]byte(body), &d)
		if _, missing := usgFields(&d); missing == "" {
			t.Errorf("%s: expected a missing key", body)
		}
	}
}

func TestUnreachable(t *testing.T) {
	capture := &testutil.Capture{}
	api := New(config.UniFiServer{Common: config.Common{URL: "http://127.0.0.1:1"}, Site: "default"},
		collector.Deps{Writer: capture})
	if err := api.GetUSGStats(context.Background()); err != nil || capture.Lines() != "" {
		t.Errorf("err = %v, lines = %q", err, capture.Lines())
	}
}
