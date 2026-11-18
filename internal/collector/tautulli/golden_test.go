package tautulli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/geoip"
	"github.com/Nexxed/Varken/internal/testutil"
)

// openTestGeoIP opens a copy of testdata/geoip/GeoLite2-City-Test.mmdb.
func openTestGeoIP(t testing.TB) *geoip.Handler {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "testdata", "geoip", "GeoLite2-City-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, geoip.DBFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	h := geoip.Open(context.Background(), dir, "", nil)
	if !h.Loaded() {
		t.Fatal("test GeoIP database did not load")
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func publicIPServer(t testing.TB, ip string, fail bool) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(ip + "\n"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestGolden(t *testing.T) {
	geo := openTestGeoIP(t)
	testutil.RunGolden(t, "tautulli", func(t *testing.T, c testutil.Case, env testutil.Env) {
		var srv config.TautulliServer
		c.Server(t, &srv)
		srv.URL = env.URL
		handler := geo
		if c.Name == "activity_no_geoip_database" {
			handler = nil
		}
		api := New(srv, env.Deps, handler)
		api.PublicIPURL = publicIPServer(t, c.PublicIP, c.PublicIPError)
		var err error
		switch c.Job {
		case "get_activity":
			err = api.GetActivity(env.Ctx)
		case "get_stats":
			err = api.GetStats(env.Ctx)
		case "get_server_status":
			err = api.GetServerStatus(env.Ctx)
		case "get_history_plays":
			days := 2
			c.Arg(t, "days", &days)
			err = api.HistoryPlays(env.Ctx, days)
		case "get_concurrent_streams":
			err = api.ConcurrentStreams(env.Ctx, 30)
		case "get_users":
			err = api.Users(env.Ctx)
		case "get_library_usage":
			err = api.LibraryUsage(env.Ctx)
		case "get_recently_added":
			err = api.RecentlyAdded(env.Ctx)
		default:
			t.Fatalf("unknown job %s", c.Job)
		}
		if err != nil {
			t.Errorf("%s: %v", c.Job, err)
		}
	})
}
