package geoip

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testDB(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "geoip", "GeoLite2-City-Test.mmdb"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// openTest copies the test database into a temp data folder and opens it.
func openTest(t *testing.T) *Handler {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DBFile), testDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	h := Open(context.Background(), dir, "key", nil)
	if !h.Loaded() {
		t.Fatal("not loaded")
	}
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func tarGz(t *testing.T, members map[string][]byte, extra ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, hdr := range extra {
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
	}
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(data)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

// TestLookupMatchesPythonGeoip2 uses the values Python's geoip2 returns for the same database
// (city.name, subdivisions.most_specific.name/.iso_code, location.latitude/.longitude).
func TestLookupMatchesPythonGeoip2(t *testing.T) {
	h := openTest(t)
	type want struct {
		city, sub, iso string
		lat, lon       any
	}
	cases := map[string]want{
		"203.0.113.5":        {"Springfield", "Illinois", "IL", 39.7817, -89.6501},
		"198.51.100.7":       {"London", "City of London", "LND", 51.5142, -0.0931},
		"192.0.2.1":          {"", "", "", 48.8566, 2.3522},
		"1.1.1.1":            {"Research", "Victoria", "VIC", -37.7, 145.1833},
		"8.8.8.8":            {"", "", "", 37.751, -97.822},
		"9.9.9.9":            {"Zero", "", "", 0.0, 0.0},
		"4.4.4.4":            {"Nocode", "Somewhere", "", 10.5, 20.25},
		"5.5.5.5":            {"Halfway", "", "", 12.5, nil},
		"2001:db8::1":        {"Sixton", "California", "CA", 37.386, -122.0838},
		"::ffff:203.0.113.5": {"Springfield", "Illinois", "IL", 39.7817, -89.6501},
	}
	for ip, w := range cases {
		loc, err := h.Lookup(ip)
		if err != nil {
			t.Fatalf("%s: %v", ip, err)
		}
		if loc.City != w.city || loc.HasCity != (w.city != "") || loc.Subdivision != w.sub ||
			loc.HasSubdivision != (w.sub != "") || loc.ISOCode != w.iso || loc.HasISOCode != (w.iso != "") {
			t.Errorf("%s: %+v", ip, loc)
		}
		if w.lat != nil && (!loc.HasLatitude || loc.Latitude != w.lat.(float64)) {
			t.Errorf("%s: latitude %+v", ip, loc)
		}
		if w.lon == nil && loc.HasLongitude || w.lon != nil && (!loc.HasLongitude || loc.Longitude != w.lon.(float64)) {
			t.Errorf("%s: longitude %+v", ip, loc)
		}
	}
	for _, ip := range []string{"", "bad", "1.1.1", "203.0.113.5 "} {
		if _, err := h.Lookup(ip); !errors.Is(err, ErrInvalidIP) {
			t.Errorf("%q: err = %v, want ErrInvalidIP", ip, err)
		}
	}
	for i := 0; i < 2; i++ { // second time from the cache
		if _, err := h.Lookup("10.0.0.1"); !errors.Is(err, ErrNotFound) {
			t.Errorf("10.0.0.1: err = %v, want ErrNotFound", err)
		}
	}
	if _, err := h.Lookup("fe80::1%eth0"); !errors.Is(err, ErrNotFound) {
		t.Errorf("zoned address: %v", err)
	}
}

func TestNilAndUnloadedHandlers(t *testing.T) {
	var nilHandler *Handler
	if nilHandler.Loaded() {
		t.Error("nil handler loaded")
	}
	if _, err := nilHandler.Lookup("1.1.1.1"); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("nil: %v", err)
	}
	h := New(t.TempDir(), "", nil)
	if _, err := h.Lookup("1.1.1.1"); !errors.Is(err, ErrNotLoaded) {
		t.Errorf("unloaded: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Error(err)
	}
}

type mmServer struct {
	*httptest.Server
	hits     atomic.Int32
	statuses []int // per request; 200 with body afterwards
	body     []byte
	mu       sync.Mutex
	query    string
}

func newMMServer(t *testing.T, body []byte, statuses ...int) *mmServer {
	s := &mmServer{statuses: statuses, body: body}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.hits.Add(1)
		s.mu.Lock()
		s.query = r.URL.RawQuery
		status := http.StatusOK
		if len(s.statuses) > 0 {
			status, s.statuses = s.statuses[0], s.statuses[1:]
		}
		s.mu.Unlock()
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write(s.body)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func newDownloading(t *testing.T, srv *mmServer) *Handler {
	h := New(t.TempDir(), "secret", nil)
	h.DownloadURL = srv.URL
	h.Sleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { _ = h.Close() })
	return h
}

func TestOpenDownloadsMissingDatabase(t *testing.T) {
	archive := tarGz(t, map[string][]byte{"GeoLite2-City_20261101/GeoLite2-City.mmdb": testDB(t),
		"GeoLite2-City_20261101/LICENSE.txt": []byte("license")},
		&tar.Header{Name: "GeoLite2-City_20261101/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "evil/GeoLite2-City.mmdb", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	srv := newMMServer(t, archive, http.StatusInternalServerError)
	h := newDownloading(t, srv)
	h.Load(context.Background())
	if !h.Loaded() {
		t.Fatal("database not loaded after download")
	}
	if srv.hits.Load() != 2 {
		t.Errorf("hits = %d, want a retry after the 500", srv.hits.Load())
	}
	if srv.query != "edition_id=GeoLite2-City&license_key=secret&suffix=tar.gz" {
		t.Errorf("query = %s", srv.query)
	}
	if loc, err := h.Lookup("203.0.113.9"); err != nil || loc.City != "Springfield" {
		t.Errorf("lookup after download: %+v %v", loc, err)
	}
	leftovers, _ := filepath.Glob(filepath.Join(h.dataFolder, "*.tmp"))
	if len(leftovers) != 0 {
		t.Errorf("temporary files left: %v", leftovers)
	}
}

func TestLicenseRejectedIsNotRetried(t *testing.T) {
	srv := newMMServer(t, nil, http.StatusUnauthorized, http.StatusUnauthorized)
	h := newDownloading(t, srv)
	h.Load(context.Background())
	if h.Loaded() {
		t.Fatal("should not be loaded")
	}
	if srv.hits.Load() != 1 {
		t.Errorf("hits = %d", srv.hits.Load())
	}
	if err := h.download(context.Background()); !errors.Is(err, ErrLicense) {
		t.Errorf("err = %v", err)
	}
}

func TestDownloadGivesUpAfterThreeAttempts(t *testing.T) {
	srv := newMMServer(t, nil, 500, 502, 503, 200)
	h := newDownloading(t, srv)
	if err := h.download(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	if srv.hits.Load() != 3 {
		t.Errorf("hits = %d", srv.hits.Load())
	}
}

func TestBadArchivesKeepTheOldDatabase(t *testing.T) {
	bad := map[string][]byte{
		"not gzip":      []byte("garbage"),
		"no mmdb":       tarGz(t, map[string][]byte{"README": []byte("x")}),
		"corrupt mmdb":  tarGz(t, map[string][]byte{"x/GeoLite2-City.mmdb": []byte("not a database")}),
		"truncated tar": tarGz(t, map[string][]byte{"x/GeoLite2-City.mmdb": testDB(t)})[:60],
	}
	for name, body := range bad {
		t.Run(name, func(t *testing.T) {
			srv := newMMServer(t, body)
			h := newDownloading(t, srv)
			if err := os.WriteFile(h.Path(), testDB(t), 0o644); err != nil {
				t.Fatal(err)
			}
			h.Load(context.Background())
			if err := h.download(context.Background()); err == nil {
				t.Fatal("expected an error")
			}
			if loc, err := h.Lookup("203.0.113.1"); err != nil || loc.City != "Springfield" {
				t.Errorf("old database lost: %+v %v", loc, err)
			}
			if data, _ := os.ReadFile(h.Path()); !bytes.Equal(data, testDB(t)) {
				t.Error("database file was replaced")
			}
		})
	}
}

func TestUpdateFollowsThe30DayRule(t *testing.T) {
	archive := tarGz(t, map[string][]byte{"d/GeoLite2-City.mmdb": testDB(t)})
	srv := newMMServer(t, archive)
	h := newDownloading(t, srv)
	if err := os.WriteFile(h.Path(), testDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Load(context.Background())
	now := time.Now()
	h.Now = func() time.Time { return now }

	if err := h.Update(context.Background()); err != nil || srv.hits.Load() != 0 {
		t.Fatalf("fresh database: err=%v hits=%d", err, srv.hits.Load())
	}
	old := now.Add(-31 * 24 * time.Hour)
	if err := os.Chtimes(h.Path(), old, old); err != nil {
		t.Fatal(err)
	}
	if err := h.Update(context.Background()); err != nil || srv.hits.Load() != 1 {
		t.Fatalf("old database: err=%v hits=%d", err, srv.hits.Load())
	}
	if !h.Loaded() {
		t.Fatal("not loaded after update")
	}
	if err := os.Remove(h.Path()); err != nil {
		t.Fatal(err)
	}
	if err := h.Update(context.Background()); err != nil || srv.hits.Load() != 2 {
		t.Fatalf("missing database: err=%v hits=%d", err, srv.hits.Load())
	}
}

func TestUpdateOpensAnExistingFileWhenNotLoaded(t *testing.T) {
	h := New(t.TempDir(), "", nil)
	if err := os.WriteFile(h.Path(), testDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := h.Update(context.Background()); err != nil || !h.Loaded() {
		t.Fatalf("err=%v loaded=%v", err, h.Loaded())
	}
	_ = h.Close()
}

func TestOpenReportsUnreadableFile(t *testing.T) {
	h := New(t.TempDir(), "", nil)
	if err := os.WriteFile(h.Path(), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Load(context.Background())
	if h.Loaded() {
		t.Error("junk should not load")
	}
}

// TestConcurrentLookupsDuringSwap runs under -race in CI.
func TestConcurrentLookupsDuringSwap(t *testing.T) {
	archive := tarGz(t, map[string][]byte{"d/GeoLite2-City.mmdb": testDB(t)})
	srv := newMMServer(t, archive)
	h := newDownloading(t, srv)
	if err := os.WriteFile(h.Path(), testDB(t), 0o644); err != nil {
		t.Fatal(err)
	}
	h.Load(context.Background())
	var wg sync.WaitGroup
	stop := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ips := []string{"203.0.113.5", "198.51.100.7", "10.0.0.1", "2001:db8::1"}
			for n := 0; ; n++ {
				select {
				case <-stop:
					return
				default:
				}
				loc, err := h.Lookup(ips[(n+i)%len(ips)])
				if err == nil && loc.City == "" && loc.HasCity {
					t.Error("torn record")
					return
				}
			}
		}(i)
	}
	for i := 0; i < 5; i++ {
		if err := h.download(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}

func TestSleepCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Error("cancelled sleep should fail")
	}
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
}
