// Package tautulli collects Plex activity, library and play statistics from Tautulli's API v2, writing the
// "Tautulli" measurement exactly as the Python collector did.
package tautulli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/geoip"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

const (
	endpoint = "/api/v2"
	// DefaultPublicIPURL answers with the caller's public address.
	DefaultPublicIPURL = "https://api.ipify.org"
	// publicIPTTL is how long a looked-up public address is reused. Python kept it for the life of the process.
	publicIPTTL = time.Hour
	// Area 51, the Python default when a location has no coordinates.
	defaultLatitude  = 37.234332396
	defaultLongitude = -115.80666344
	unknownCity      = "👽"
	measurement      = "Tautulli"
)

// API is one Tautulli server.
type API struct {
	srv    config.TautulliServer
	d      collector.Deps
	geo    *geoip.Handler
	client *httpx.Client
	log    *slog.Logger

	// PublicIPURL is where the public address is looked up when neither a stream's address nor fallback_ip
	// is in the GeoIP database. Only a plain GET is sent there, never the Tautulli API key.
	PublicIPURL string
	// PublicHTTP is the client for PublicIPURL.
	PublicHTTP *http.Client

	mu         sync.Mutex
	headerAuth bool
	myIP       string
	myIPAt     time.Time
}

// New returns a collector for srv. geo may be nil: locations are then written as unknown.
func New(srv config.TautulliServer, deps collector.Deps, geo *geoip.Handler) *API {
	deps = deps.WithDefaults()
	client := httpx.New(httpx.Options{
		BaseURL:   srv.URL,
		VerifySSL: srv.VerifySSL,
		// Tautulli 2.18+ accepts the key as a header, which keeps it out of URLs and logs.
		Headers:   map[string]string{"X-Api-Key": srv.APIKey},
		Log:       deps.Log,
		Transport: deps.Transport,
	})
	public := &http.Client{Timeout: 10 * time.Second}
	if deps.Transport != nil {
		public.Transport = deps.Transport
	}
	return &API{srv: srv, d: deps, geo: geo, client: client, log: deps.Log.With("module", "tautulli"),
		PublicIPURL: DefaultPublicIPURL, PublicHTTP: public, headerAuth: true}
}

func (a *API) String() string { return fmt.Sprintf("<tautulli-%d>", a.srv.ID) }

// Jobs are the scheduled collections, named like the Python schedule tags.
func (a *API) Jobs() []collector.Job {
	s := a.srv
	name := func(method string) string { return fmt.Sprintf("tautulli-%d-%s", s.ID, method) }
	var jobs []collector.Job
	if s.GetActivity {
		jobs = append(jobs, collector.Every(name("get_activity"), s.GetActivityRunSeconds, a.GetActivity))
	}
	if s.GetStats {
		jobs = append(jobs, collector.Every(name("get_stats"), s.GetStatsRunSeconds, a.GetStats))
	}
	if s.GetServer {
		jobs = append(jobs, collector.Every(name("get_server_status"), s.GetServerRunSeconds, a.GetServerStatus))
	}
	if s.GetHistory {
		days := s.HistoryDays
		jobs = append(jobs, collector.Every(name("get_history_plays"), s.GetHistoryRunSeconds,
			func(ctx context.Context) error { return a.HistoryPlays(ctx, days) }))
		if s.HistoryBackfillDays > s.HistoryDays {
			backfill := s.HistoryBackfillDays
			jobs = append(jobs, collector.Once(name("get_history_plays_backfill"),
				func(ctx context.Context) error { return a.HistoryPlays(ctx, backfill) }))
		}
	}
	if s.GetDailyStats {
		daily := []struct {
			method string
			run    func(context.Context) error
		}{
			{"get_concurrent_streams", func(ctx context.Context) error { return a.ConcurrentStreams(ctx, 30) }},
			{"get_users", a.Users},
			{"get_library_usage", a.LibraryUsage},
			{"get_recently_added", a.RecentlyAdded},
		}
		for _, j := range daily {
			jobs = append(jobs, collector.Every(name(j.method), s.GetDailyStatsRunSeconds, j.run))
		}
	}
	return jobs
}

// call is the Python _api: the parsed JSON of a 200 answer, falling back once from header to URL-parameter
// authentication when Tautulli rejects the header (versions before 2.18).
func (a *API) call(ctx context.Context, cmd string, params ...httpx.Param) (json.RawMessage, bool) {
	query := append(httpx.Params{}, params...)
	query = append(query, httpx.Param{Key: "cmd", Value: cmd})
	resp, err := a.client.Do(ctx, http.MethodGet, endpoint, query, nil)
	if err != nil {
		return nil, false
	}
	a.client.LogStatus(resp)
	if resp.Status == http.StatusUnauthorized && a.switchToParamAuth() {
		return a.call(ctx, cmd, params...)
	}
	if resp.Status != http.StatusOK {
		return nil, false
	}
	if !json.Valid(resp.Body) {
		a.log.Error("No JSON response from Tautulli for " + cmd)
		return nil, false
	}
	return resp.Body, true
}

func (a *API) switchToParamAuth() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.headerAuth {
		return false
	}
	a.log.Warn("Tautulli rejected the X-Api-Key header (it needs v2.18 or newer). Falling back to sending the API " +
		"key as a URL parameter.")
	a.headerAuth = false
	a.client.SetHeader("X-Api-Key", "")
	a.client.SetSessionParams(httpx.P("apikey", a.srv.APIKey))
	return true
}

type envelope struct {
	Response py.Obj[struct {
		Result py.Value        `json:"result"`
		Data   json.RawMessage `json:"data"`
	}] `json:"response"`
}

// rawData returns response.data of an answer, whatever its result; ok is false when there is no response object.
func rawData(body json.RawMessage) (json.RawMessage, bool) {
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || !env.Response.OK() {
		return nil, false
	}
	return env.Response.V().Data, true
}

// data is the Python _data: response.data of a successful answer.
func (a *API) data(ctx context.Context, cmd string, params ...httpx.Param) (json.RawMessage, bool) {
	body, ok := a.call(ctx, cmd, params...)
	if !ok {
		return nil, false
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil || !env.Response.OK() ||
		!env.Response.V().Result.Equal(py.String("success")) {
		return nil, false
	}
	return env.Response.V().Data, true
}

func decode[T any](raw json.RawMessage) (T, bool) {
	var out T
	if len(raw) == 0 || raw[0] != '{' && raw[0] != '[' {
		return out, false
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, false
	}
	return out, true
}

func isObject(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	return strings.HasPrefix(trimmed, "{")
}

// num is tautulli._num with cast=int: int(float(value)), 0 when that fails.
func num(v py.Value) int64 { return py.ToInt(v, 0) }

// numFloat is _num with cast=float.
func numFloat(v py.Value) float64 { return py.ToFloatOr(v, 0) }

// floorDiv is Python's // for ints.
func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// floorMod is Python's % for ints.
func floorMod(a, b int64) int64 {
	m := a % b
	if m != 0 && (m < 0) != (b < 0) {
		m += b
	}
	return m
}

// text is the string a Python str method would have been called on. Python raised on None; it reads as "" here.
func text(v py.Value) string {
	if v.IsNone() {
		return ""
	}
	return v.String()
}

func (a *API) server() py.Value { return py.Int(int64(a.srv.ID)) }

// isoUTC is the Python _iso: epoch seconds to an ISO string in UTC, "" when missing.
func isoUTC(ts py.Value) string {
	n := num(ts)
	if n <= 0 {
		return ""
	}
	return py.IsoFormat(time.Unix(n, 0).UTC())
}

func (a *API) write(ctx context.Context, points []influx.Point) error {
	if len(points) == 0 {
		return nil
	}
	return a.d.Writer.Write(ctx, points)
}

// lookupPublicIP asks PublicIPURL for this network's public address, falling back to fallback_ip.
func (a *API) lookupPublicIP(ctx context.Context) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.PublicIPURL, nil)
	if err == nil {
		var resp *http.Response
		if resp, err = a.PublicHTTP.Do(req); err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
			_ = resp.Body.Close()
			ip := strings.TrimSpace(string(body))
			if _, perr := netip.ParseAddr(ip); resp.StatusCode == http.StatusOK && perr == nil {
				return ip
			}
			err = fmt.Errorf("HTTP %d: %q", resp.StatusCode, py.Truncate(ip, 60))
		}
	}
	a.log.Error(fmt.Sprintf("Could not look up the public IP: %v", err))
	return a.srv.FallbackIP
}

// cachedIP returns the looked-up public address while it is fresh.
func (a *API) cachedIP() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.myIP != "" && a.d.Now().Sub(a.myIPAt) > publicIPTTL {
		a.myIP = ""
	}
	return a.myIP
}

func (a *API) setIP(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.myIP, a.myIPAt = ip, a.d.Now()
}

// locate is the Python lookup chain: the stream's public address, else fallback_ip, else this network's public
// address. Python raised when the last step failed and lost the whole run; here the location is then unknown.
func (a *API) locate(ctx context.Context, ip py.Value) geoip.Location {
	if !a.geo.Loaded() {
		return geoip.Location{}
	}
	loc, err := a.geo.Lookup(text(ip))
	if err == nil {
		return loc
	}
	a.log.Debug("Public IP missing for Tautulli session...")
	if myIP := a.cachedIP(); myIP != "" {
		if loc, err = a.geo.Lookup(myIP); err == nil {
			return loc
		}
		a.log.Error(fmt.Sprintf("Could not locate the public IP %s: %v", myIP, err))
		return geoip.Location{}
	}
	a.log.Debug("Attempting to use the fallback IP...")
	if loc, err = a.geo.Lookup(a.srv.FallbackIP); err == nil {
		return loc
	}
	a.log.Error(err.Error())
	myIP := a.lookupPublicIP(ctx)
	a.setIP(myIP)
	a.log.Debug("Looked the public IP and set it to " + myIP)
	if loc, err = a.geo.Lookup(myIP); err == nil {
		return loc
	}
	a.log.Error(fmt.Sprintf("Could not locate the public IP %s, writing the location as unknown: %v", myIP, err))
	return geoip.Location{}
}

// optString is a Python value that may be None.
func optString(s string, ok bool) py.Value {
	if !ok {
		return py.None
	}
	return py.String(s)
}

// locationTags are the GeoIP tags of a session.
func locationTags(loc geoip.Location) collector.M {
	lat, lon := py.Float(defaultLatitude), py.Float(defaultLongitude)
	if loc.HasLatitude && loc.HasLongitude && loc.Latitude != 0 && loc.Longitude != 0 {
		lat, lon = py.Float(loc.Latitude), py.Float(loc.Longitude)
	}
	city := optString(loc.City, loc.HasCity)
	location := py.String(unknownCity)
	if loc.HasCity && loc.City != "" {
		location = py.String(loc.City)
	}
	sub := optString(loc.Subdivision, loc.HasSubdivision)
	return collector.M{
		"region_code":   optString(loc.ISOCode, loc.HasISOCode),
		"location":      location,
		"full_location": py.String(sub.String() + " - " + city.String()),
		"latitude":      lat,
		"longitude":     lon,
	}
}
