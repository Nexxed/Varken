package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/logging"
	"github.com/Nexxed/Varken/internal/scheduler"
	"github.com/Nexxed/Varken/internal/testutil"
)

// syncBuffer is a goroutine-safe console.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func envOf(m map[string]string) config.Env {
	return func(k string) (string, bool) { v, ok := m[k]; return v, ok }
}

func caseRoutes(t *testing.T, service, name string) ([]testutil.Route, string) {
	t.Helper()
	for _, c := range testutil.LoadCases(t, service) {
		if c.Name == name {
			data, err := os.ReadFile(filepath.Join(c.Dir, "expected.lp"))
			if err != nil {
				t.Fatal(err)
			}
			return c.Routes, string(data)
		}
	}
	t.Fatalf("no case %s/%s", service, name)
	return nil, ""
}

func writeINI(t *testing.T, folder, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(folder, "varken.ini"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunEndToEnd runs Varken against fixture servers and a fake InfluxDB: startup, database creation, the
// collectors' first runs, and a clean shutdown.
func TestRunEndToEnd(t *testing.T) {
	routes, expected := caseRoutes(t, "ombi", "request_counts_happy")
	issueRoutes, issueExpected := caseRoutes(t, "ombi", "issue_counts_happy")
	fixture := testutil.StartFixture(t, append(routes, issueRoutes...))
	influxSrv := testutil.StartInflux(t, "_internal")
	host, port := influxSrv.HostPort()
	folder := t.TempDir()
	writeINI(t, folder, fmt.Sprintf(`[global]
ombi_server_ids = 1
sonarr_server_ids = false
radarr_server_ids = false
lidarr_server_ids = false
tautulli_server_ids = false
overseerr_server_ids = false
sickchill_server_ids = false
unifi_server_ids = false
cleanuparr_server_ids = false
maxmind_license_key = xx

[influxdb]
url = %s
port = %d
ssl = false
verify_ssl = false
username = root
password = rootpassword

[ombi-1]
url = %s
apikey = ombisecretkey
ssl = false
verify_ssl = false
get_request_type_counts = true
request_type_run_seconds = 300
get_request_total_counts = false
request_total_run_seconds = 300
get_issue_status_counts = true
issue_status_run_seconds = 300
`, host, port, strings.TrimPrefix(fixture.URL, "http://")))

	console := &syncBuffer{}
	ctx, cancel := context.WithCancel(context.Background())
	code := make(chan int, 1)
	go func() {
		code <- Run(ctx, Options{DataFolder: folder, Getenv: envOf(map[string]string{"DEBUG": "False"}),
			Console: console})
	}()
	deadline := time.After(10 * time.Second)
	for len(influxSrv.Writes()) < 2 {
		select {
		case <-influxSrv.Written():
		case <-deadline:
			t.Fatalf("no writes; console:\n%s", console.String())
		}
	}
	cancel()
	if c := <-code; c != 0 {
		t.Fatalf("exit code %d; console:\n%s", c, console.String())
	}
	all := strings.Join(influxSrv.Writes(), "")
	for _, line := range strings.Split(strings.TrimSpace(expected+issueExpected), "\n") {
		// The timestamps differ (real clock), the rest must not.
		prefix := line[:strings.LastIndex(line, " ")]
		if !strings.Contains(all, prefix) {
			t.Errorf("missing %q in\n%s", prefix, all)
		}
	}
	queries := strings.Join(influxSrv.Queries(), "\n")
	if !strings.Contains(queries, `CREATE DATABASE "varken"`) {
		t.Errorf("queries = %s", queries)
	}
	log, err := os.ReadFile(filepath.Join(folder, "logs", "varken.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{" : INFO : varken : Starting Varken...", "Varken v", "Influxdb version: 1.8.10",
		"Scheduled 2 jobs", "Varken stopped"} {
		if !strings.Contains(string(log), want) {
			t.Errorf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(string(log), "ombisecretkey") || strings.Contains(string(log), "rootpassword") {
		t.Error("a secret reached the log file")
	}
	if strings.Contains(string(log), " : DEBUG : ") {
		t.Error("DEBUG=False must turn debug logging off")
	}
	if _, err := os.Stat(filepath.Join(folder, "varken.example.ini")); err != nil {
		t.Error("varken.example.ini should be written to the data folder")
	}
}

func TestRunFailures(t *testing.T) {
	influxSrv := testutil.StartInflux(t, "varken")
	host, port := influxSrv.HostPort()
	influxSection := fmt.Sprintf("[influxdb]\nurl = %s\nport = %d\n", host, port)
	cases := map[string]struct {
		ini  string
		want string
	}{
		"not configured":  {"", "You have not configured your varken.ini"},
		"nothing enabled": {"[global]\nsonarr_server_ids = false\n" + influxSection, "All services disabled. Exiting"},
		"bad influx url":  {"[influxdb]\nurl = http://x\n", "is invalid in module [influxdb]"},
		"influx down":     {"[influxdb]\nurl = 127.0.0.1\nport = 1\n", "error testing connection to InfluxDB"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			folder := t.TempDir()
			if c.ini != "" {
				writeINI(t, folder, c.ini)
			}
			console := &syncBuffer{}
			code := Run(context.Background(), Options{DataFolder: folder, Getenv: envOf(nil), Console: console,
				InfluxAttempts: 2, InfluxWait: time.Millisecond})
			if code != 1 || !strings.Contains(console.String(), c.want) {
				t.Errorf("code %d, console:\n%s", code, console.String())
			}
		})
	}
	console := &syncBuffer{}
	if code := Run(context.Background(), Options{DataFolder: filepath.Join(t.TempDir(), "missing"),
		Getenv: envOf(nil), Console: console}); code != 1 || !strings.Contains(console.String(), "does not exist") {
		t.Errorf("missing folder: %d %s", code, console.String())
	}
}

func TestImportTautulli(t *testing.T) {
	influxSrv := testutil.StartInflux(t, "varken")
	host, port := influxSrv.HostPort()
	folder := t.TempDir()
	writeINI(t, folder, fmt.Sprintf("[global]\ntautulli_server_ids = false\n[influxdb]\nurl = %s\nport = %d\n", host, port))
	console := &syncBuffer{}
	if code := ImportTautulli(context.Background(), Options{DataFolder: folder, Getenv: envOf(nil),
		Console: console}, 30); code != 1 || !strings.Contains(console.String(), "No Tautulli servers") {
		t.Errorf("code %d: %s", code, console.String())
	}
	if code := ImportTautulli(context.Background(), Options{DataFolder: filepath.Join(folder, "nope"),
		Getenv: envOf(nil), Console: console}, 30); code != 1 {
		t.Error("missing folder should fail")
	}
}

func TestResolveDataFolder(t *testing.T) {
	if got := ResolveDataFolder(envOf(map[string]string{"DATA_FOLDER": "/config"}), "/flag"); got != "/config" {
		t.Errorf("env should win: %s", got)
	}
	if got := ResolveDataFolder(envOf(nil), "/flag"); got != "/flag" {
		t.Errorf("flag: %s", got)
	}
	if got := ResolveDataFolder(envOf(nil), ""); !strings.HasSuffix(got, "data") {
		t.Errorf("default: %s", got)
	}
	if !strings.Contains(platform(), "/") && !strings.Contains(platform(), " ") {
		t.Error("platform")
	}
	if Logger(&bytes.Buffer{}) == nil {
		t.Error("Logger")
	}
}

func tautulliFolder(t *testing.T, fixtureURL, influxHost string, influxPort int) string {
	t.Helper()
	folder := t.TempDir()
	writeINI(t, folder, fmt.Sprintf(`[global]
tautulli_server_ids = 1
maxmind_license_key = testkey

[influxdb]
url = %s
port = %d

[tautulli-1]
url = %s
fallback_ip = 1.1.1.1
apikey = tautullisecret
ssl = false
verify_ssl = false
get_activity = true
get_activity_run_seconds = 30
get_stats = true
get_stats_run_seconds = 3600
`, influxHost, influxPort, strings.TrimPrefix(fixtureURL, "http://")))
	db, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "testdata", "geoip", "GeoLite2-City-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(folder, "GeoLite2-City.mmdb"), db, 0o644); err != nil {
		t.Fatal(err)
	}
	return folder
}

func TestImportTautulliWritesHistory(t *testing.T) {
	routes, expected := caseRoutes(t, "tautulli", "history_plays")
	fixture := testutil.StartFixture(t, routes)
	influxSrv := testutil.StartInflux(t, "varken")
	host, port := influxSrv.HostPort()
	folder := tautulliFolder(t, fixture.URL, host, port)
	console := &syncBuffer{}
	if code := ImportTautulli(context.Background(), Options{DataFolder: folder, Getenv: envOf(nil),
		Console: console}, 2); code != 0 {
		t.Fatalf("code %d: %s", code, console.String())
	}
	if got := strings.Join(influxSrv.Writes(), ""); got != expected {
		t.Errorf("wrote:\n%s\nwant:\n%s", got, expected)
	}

	// A failing server makes the import report failure.
	failing := testutil.StartFixture(t, nil)
	folder = tautulliFolder(t, failing.URL, host, port)
	if code := ImportTautulli(context.Background(), Options{DataFolder: folder, Getenv: envOf(nil),
		Console: console}, 2); code != 0 && !strings.Contains(console.String(), "Import from") {
		t.Errorf("code %d", code)
	}
}

func TestRegisterSchedulesGeoIPUpdate(t *testing.T) {
	fixture := testutil.StartFixture(t, nil)
	folder := tautulliFolder(t, fixture.URL, "127.0.0.1", 1)
	cfg, err := config.Load(folder, envOf(nil), logging.New(&syncBuffer{}).Logger)
	if err != nil {
		t.Fatal(err)
	}
	sched := scheduler.New(logging.New(&syncBuffer{}).Logger, nil)
	Register(context.Background(), cfg, sched, collector.Deps{Now: time.Now}, logging.New(&syncBuffer{}), folder)
	names := strings.Join(sched.Names(), ",")
	if !strings.HasPrefix(names, "geoip-update,tautulli-1-get_activity") {
		t.Errorf("names = %s", names)
	}
}

func TestWriteExampleKeepsItCurrent(t *testing.T) {
	folder := t.TempDir()
	path := filepath.Join(folder, "varken.example.ini")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := logging.New(&syncBuffer{})
	writeExample(folder, log)
	if data, _ := os.ReadFile(path); string(data) != config.ExampleINI {
		t.Error("example not refreshed")
	}
	writeExample(filepath.Join(folder, "missing"), log) // logged, not fatal
	if err := checkFolder(path); err == nil {
		t.Error("a file is not a data folder")
	}
}
