// Package geoip looks up stream locations in MaxMind's GeoLite2-City database and keeps that database current,
// like the Python GeoIPHandler: download when missing, refresh once the file is over 30 days old.
//
// Unlike the Python version it never deletes the old database before a new one is ready, never exits the
// process when a download fails (callers keep working with unknown locations), and lookups stay safe while the
// database is being replaced.
package geoip

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const (
	// DBFile is the database file name inside the data folder.
	DBFile = "GeoLite2-City.mmdb"
	// DefaultDownloadURL is MaxMind's download endpoint.
	DefaultDownloadURL = "https://download.maxmind.com/app/geoip_download"
	maxAge             = 30 * 24 * time.Hour
	cacheSize          = 1024
	downloadAttempts   = 3
)

var (
	// ErrInvalidIP is an address that does not parse, or cannot be looked up in this database (Python ValueError).
	ErrInvalidIP = errors.New("invalid IP address")
	// ErrNotFound is an address with no record (Python AddressNotFoundError).
	ErrNotFound = errors.New("address not in the database")
	// ErrNotLoaded means no database is available.
	ErrNotLoaded = errors.New("no GeoIP database loaded")
	// ErrLicense is MaxMind rejecting the license key.
	ErrLicense = errors.New("MaxMind license key rejected")
)

// Location is the part of a GeoIP2 City record Varken uses. The OK flags say whether a value exists, since the
// Python code treated a missing name (None) differently from an empty one.
type Location struct {
	City           string
	HasCity        bool
	Subdivision    string // most specific (last) subdivision's English name
	HasSubdivision bool
	ISOCode        string
	HasISOCode     bool
	Latitude       float64
	HasLatitude    bool
	Longitude      float64
	HasLongitude   bool
}

type names map[string]string

type record struct {
	City struct {
		Names names `maxminddb:"names"`
	} `maxminddb:"city"`
	Subdivisions []struct {
		ISOCode *string `maxminddb:"iso_code"`
		Names   names   `maxminddb:"names"`
	} `maxminddb:"subdivisions"`
	Location struct {
		Latitude  *float64 `maxminddb:"latitude"`
		Longitude *float64 `maxminddb:"longitude"`
	} `maxminddb:"location"`
}

type cached struct {
	loc Location
	err error
}

// Handler owns the database file and reader.
type Handler struct {
	dataFolder string
	licenseKey string
	log        *slog.Logger

	// DownloadURL is MaxMind's endpoint; tests point it elsewhere.
	DownloadURL string
	// HTTP downloads the database.
	HTTP *http.Client
	// Now is the clock for the 30-day rule.
	Now func() time.Time
	// Sleep waits between download attempts.
	Sleep func(context.Context, time.Duration) error

	mu     sync.RWMutex // guards reader
	reader *maxminddb.Reader
	cmu    sync.Mutex // guards cache
	cache  map[netip.Addr]cached
	update sync.Mutex
}

// New returns a handler without touching the disk or network.
func New(dataFolder, licenseKey string, log *slog.Logger) *Handler {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &Handler{
		dataFolder:  dataFolder,
		licenseKey:  licenseKey,
		log:         log.With("module", "helpers"),
		DownloadURL: DefaultDownloadURL,
		HTTP:        &http.Client{Timeout: 5 * time.Minute, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
		Now:         time.Now,
		Sleep:       sleepCtx,
		cache:       map[netip.Addr]cached{},
	}
}

// Open opens the database in dataFolder, downloading it first when it is missing. A failed download is logged
// and returns a handler without a database: lookups then report ErrNotLoaded until Update succeeds.
func Open(ctx context.Context, dataFolder, licenseKey string, log *slog.Logger) *Handler {
	h := New(dataFolder, licenseKey, log)
	h.Load(ctx)
	return h
}

// Load opens the database file, downloading it when it does not exist.
func (h *Handler) Load(ctx context.Context) {
	if err := h.openFile(); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			h.log.Error("Could not find MaxMind DB! Downloading!")
			if err := h.download(ctx); err != nil {
				h.log.Error(fmt.Sprintf("Could not download MaxMind DB! Locations will show as unknown until "+
					"it can be downloaded or is installed manually. Error: %v", err))
				return
			}
		} else {
			h.log.Error(fmt.Sprintf("Could not open MaxMind DB %s: %v", h.Path(), err))
			return
		}
	}
	h.log.Info("Opening persistent connection to the MaxMind DB...")
}

// Path is the database file.
func (h *Handler) Path() string { return filepath.Join(h.dataFolder, DBFile) }

// Loaded reports whether a database is open.
func (h *Handler) Loaded() bool {
	if h == nil {
		return false
	}
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.reader != nil
}

func (h *Handler) openFile() error {
	reader, err := maxminddb.Open(h.Path())
	if err != nil {
		return err
	}
	h.swap(reader)
	return nil
}

// swap installs a new reader and closes the old one once no lookup is using it.
func (h *Handler) swap(reader *maxminddb.Reader) {
	h.mu.Lock()
	old := h.reader
	h.reader = reader
	h.cmu.Lock()
	h.cache = map[netip.Addr]cached{}
	h.cmu.Unlock()
	h.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// Close releases the database.
func (h *Handler) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reader == nil {
		return nil
	}
	err := h.reader.Close()
	h.reader = nil
	return err
}

// Lookup finds ip. A nil handler or one without a database returns ErrNotLoaded.
func (h *Handler) Lookup(ip string) (Location, error) {
	if h == nil {
		return Location{}, ErrNotLoaded
	}
	if i := strings.LastIndexByte(ip, '.'); i >= 0 {
		h.log.Debug("Getting lat/long for Tautulli stream using ip with last octet ending in " + ip[i+1:])
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return Location{}, fmt.Errorf("%w: %q does not appear to be an IPv4 or IPv6 address", ErrInvalidIP, ip)
	}
	addr = addr.WithZone("")

	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.reader == nil {
		return Location{}, ErrNotLoaded
	}
	h.cmu.Lock()
	c, ok := h.cache[addr]
	h.cmu.Unlock()
	if ok {
		return c.loc, c.err
	}
	loc, err := h.lookup(addr)
	h.cmu.Lock()
	if len(h.cache) >= cacheSize {
		h.cache = map[netip.Addr]cached{}
	}
	h.cache[addr] = cached{loc, err}
	h.cmu.Unlock()
	return loc, err
}

func (h *Handler) lookup(addr netip.Addr) (Location, error) {
	result := h.reader.Lookup(addr)
	if err := result.Err(); err != nil {
		return Location{}, fmt.Errorf("%w: %v", ErrInvalidIP, err)
	}
	if !result.Found() {
		return Location{}, fmt.Errorf("%w: the address %s is not in the database", ErrNotFound, addr)
	}
	var rec record
	if err := result.Decode(&rec); err != nil {
		return Location{}, fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	var loc Location
	loc.City, loc.HasCity = rec.City.Names["en"]
	if n := len(rec.Subdivisions); n > 0 {
		sub := rec.Subdivisions[n-1]
		loc.Subdivision, loc.HasSubdivision = sub.Names["en"]
		if sub.ISOCode != nil {
			loc.ISOCode, loc.HasISOCode = *sub.ISOCode, true
		}
	}
	if rec.Location.Latitude != nil {
		loc.Latitude, loc.HasLatitude = *rec.Location.Latitude, true
	}
	if rec.Location.Longitude != nil {
		loc.Longitude, loc.HasLongitude = *rec.Location.Longitude, true
	}
	return loc, nil
}

// Update downloads a new database once the current file is more than 30 days old (or missing), as the Python
// update job did. It is safe to call concurrently with lookups.
func (h *Handler) Update(ctx context.Context) error {
	h.update.Lock()
	defer h.update.Unlock()
	today := dateOf(h.Now())
	info, err := os.Stat(h.Path())
	if err != nil {
		h.log.Error("Could not find MaxMind DB as: " + h.Path())
		return h.download(ctx)
	}
	dbDate := dateOf(info.ModTime().In(today.Location()))
	next := dbDate.Add(maxAge)
	if next.Before(today) {
		h.log.Info("Newer MaxMind DB available, Updating...")
		h.log.Debug(fmt.Sprintf("MaxMind DB date %s, DB updates after: %s, Today: %s",
			dbDate.Format(time.DateOnly), next.Format(time.DateOnly), today.Format(time.DateOnly)))
		return h.download(ctx)
	}
	if !h.Loaded() {
		if err := h.openFile(); err != nil {
			return err
		}
	}
	days := int(next.Sub(today).Hours()/24 + 0.5)
	h.log.Debug(fmt.Sprintf("MaxMind DB will update in %d days", days))
	h.log.Debug(fmt.Sprintf("MaxMind DB date %s, DB updates after: %s, Today: %s",
		dbDate.Format(time.DateOnly), next.Format(time.DateOnly), today.Format(time.DateOnly)))
	return nil
}

func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (h *Handler) downloadURL() string {
	q := url.Values{"edition_id": {"GeoLite2-City"}, "suffix": {"tar.gz"}, "license_key": {h.licenseKey}}
	return h.DownloadURL + "?" + q.Encode()
}

// download fetches and installs a new database: tar.gz to a temporary file, the .mmdb extracted next to the
// current one, opened to check it, then renamed over it.
func (h *Handler) download(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= downloadAttempts; attempt++ {
		h.log.Info("Downloading GeoLite2 DB from MaxMind...")
		lastErr = h.downloadOnce(ctx)
		if lastErr == nil {
			return nil
		}
		if errors.Is(lastErr, ErrLicense) {
			h.log.Error(fmt.Sprintf("Your MaxMind license key is incorrect! Check your config: %v", lastErr))
			return lastErr
		}
		if ctx.Err() != nil {
			return lastErr
		}
		if attempt < downloadAttempts {
			h.log.Error(fmt.Sprintf("Problem downloading new MaxMind DB... Trying again: %v", lastErr))
			if err := h.Sleep(ctx, 2*time.Second); err != nil {
				return err
			}
		}
	}
	h.log.Error("Retried downloading the new MaxMind DB 3 times and failed... Aborting!")
	return lastErr
}

func (h *Handler) downloadOnce(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.downloadURL(), nil)
	if err != nil {
		return err
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("%w: HTTP 401", ErrLicense)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if err := os.MkdirAll(h.dataFolder, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(h.dataFolder, DBFile+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after the rename
	h.log.Debug("Opening MaxMind tar file")
	if err := extractMMDB(resp.Body, tmp); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	reader, err := maxminddb.Open(tmpName)
	if err != nil {
		return fmt.Errorf("downloaded database does not open: %w", err)
	}
	if err := os.Rename(tmpName, h.Path()); err != nil {
		_ = reader.Close()
		return err
	}
	h.swap(reader)
	h.log.Debug(fmt.Sprintf("%s has been extracted to %s", DBFile, h.dataFolder))
	return nil
}

// extractMMDB copies the GeoLite2-City.mmdb member of a tar.gz stream to w. Only that one regular file is read;
// paths, links and other members are ignored.
func extractMMDB(r io.Reader, w io.Writer) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("not a tar.gz: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New(DBFile + " not found in the archive")
		}
		if err != nil {
			return fmt.Errorf("reading the archive: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || path.Base(hdr.Name) != DBFile {
			continue
		}
		if _, err := io.Copy(w, io.LimitReader(tr, 1<<30)); err != nil {
			return err
		}
		return nil
	}
}
