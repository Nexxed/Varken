// Package unifi collects gateway (USG) statistics from a UniFi controller (port of varken/unifi.py).
package unifi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// API is one UniFi controller. Unlike the Python version it does not log in when it is created, but on the first
// run, so a controller that is down at startup no longer delays or breaks Varken's start.
type API struct {
	srv  config.UniFiServer
	deps collector.Deps
	log  *slog.Logger
	http *httpx.Client

	mu       sync.Mutex
	started  bool
	site     string
	retry    bool
	cookies  map[string]string
	cookieOK bool
}

// New returns the collector for one [unifi-N] section.
func New(srv config.UniFiServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	return &API{
		srv:     srv,
		deps:    deps,
		log:     deps.Log.With("module", "unifi"),
		site:    srv.Site,
		retry:   true,
		cookies: map[string]string{},
		http: httpx.New(httpx.Options{BaseURL: srv.URL, VerifySSL: srv.VerifySSL, Log: deps.Log,
			Transport: deps.Transport}),
	}
}

// Jobs are the scheduled jobs of Varken.py for this server: get_usg_stats always runs.
func (a *API) Jobs() []collector.Job {
	return []collector.Job{collector.Every(fmt.Sprintf("unifi-%d-get_usg_stats", a.srv.ID),
		a.srv.GetUSGStatsRunSeconds, a.GetUSGStats)}
}

// keepCookies stores the cookies a response set and sends them all from then on. Like the Python session (which
// also added the unifises cookie by hand), cookies are sent whatever domain they were set for.
func (a *API) keepCookies(resp *httpx.Response) {
	for _, c := range resp.Cookies {
		a.cookies[c.Name] = c.Value
	}
	names := make([]string, 0, len(a.cookies))
	for name := range a.cookies {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + "=" + a.cookies[name]
	}
	a.http.SetHeader("Cookie", strings.Join(parts, "; "))
}

// get is connection_handler with the response's cookies kept.
func (a *API) get(ctx context.Context, path string, out any) bool {
	resp, err := a.http.Do(ctx, http.MethodGet, path, nil, nil)
	if err != nil {
		return false
	}
	a.keepCookies(resp)
	a.http.LogStatus(resp)
	if resp.Status != http.StatusOK {
		return false
	}
	if !json.Valid(resp.Body) {
		a.log.Error("No JSON response. Response is: " + py.Truncate(string(resp.Body), 2000))
		return false
	}
	if !httpx.Truthy(resp.Body) {
		return false
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		a.log.Error(fmt.Sprintf("Unexpected JSON from %s: %v", path, err))
		return false
	}
	return true
}

type login struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Remember bool   `json:"remember"`
}

// getCookie is get_cookie: log in and keep the unifises session cookie.
func (a *API) getCookie(ctx context.Context) {
	resp, err := a.http.Do(ctx, http.MethodPost, "/api/login", nil,
		login{Username: a.srv.Username, Password: a.srv.Password, Remember: true})
	if err != nil {
		a.log.Error("Could not retrieve session cookie from UniFi Controller")
		return
	}
	a.http.LogStatus(resp)
	a.keepCookies(resp)
	if resp.Status >= 400 || !setsSessionCookie(resp) {
		a.log.Error("Could not retrieve session cookie from UniFi Controller")
	}
}

func setsSessionCookie(resp *httpx.Response) bool {
	for _, c := range resp.Cookies {
		if c.Name == "unifises" && c.Value != "" {
			return true
		}
	}
	return false
}

type site struct {
	Name py.Value `json:"name"`
	Desc py.Value `json:"desc"`
}

type sites struct {
	Data py.Objs[site] `json:"data"`
}

// getSite is get_site: map the configured site (name or description, case-insensitive) to its name.
func (a *API) getSite(ctx context.Context) {
	var s sites
	if !a.get(ctx, "/api/self/sites", &s) {
		a.log.Error("Could not get list of sites from UniFi Controller")
		return
	}
	want := py.Lower(a.srv.Site)
	for _, candidate := range s.Data {
		name, _ := candidate.Name.AsString()
		desc, _ := candidate.Desc.AsString()
		if (candidate.Name.Exists() && py.Lower(name) == want) || (candidate.Desc.Exists() && py.Lower(desc) == want) {
			a.site = name
			return
		}
	}
	a.log.Error(fmt.Sprintf("Could not map site %s to a site id/alias", a.srv.Site))
}

type wan struct {
	BytesR   py.Value `json:"bytes-r"`
	RxBytes  py.Value `json:"rx_bytes"`
	RxBytesR py.Value `json:"rx_bytes-r"`
	TxBytes  py.Value `json:"tx_bytes"`
	TxBytesR py.Value `json:"tx_bytes-r"`
}

type sysStats struct {
	Loadavg1  py.Value `json:"loadavg_1"`
	Loadavg5  py.Value `json:"loadavg_5"`
	Loadavg15 py.Value `json:"loadavg_15"`
}

type systemStats struct {
	CPU py.Value `json:"cpu"`
	Mem py.Value `json:"mem"`
}

type device struct {
	Name        py.Value            `json:"name"`
	Model       py.Value            `json:"model"`
	Wan1        py.Obj[wan]         `json:"wan1"`
	SysStats    py.Obj[sysStats]    `json:"sys_stats"`
	SystemStats py.Obj[systemStats] `json:"system-stats"`
}

type devices struct {
	Data py.Objs[device] `json:"data"`
}

// GetUSGStats is get_usg_stats: WAN throughput and load of the gateway named usg_name.
func (a *API) GetUSGStats(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.started {
		a.started = true
		a.getCookie(ctx)
		a.getSite(ctx)
	}
	return a.usgStats(ctx)
}

func (a *API) usgStats(ctx context.Context) error {
	now := collector.NowTime(a.deps)
	var d devices
	if !a.get(ctx, "/api/s/"+a.site+"/stat/device", &d) {
		if a.retry {
			a.retry = false
			a.log.Error(fmt.Sprintf("Attempting to reauthenticate for unifi-%d", a.srv.ID))
			a.getCookie(ctx)
			return a.usgStats(ctx)
		}
		a.retry = true
		a.log.Error(fmt.Sprintf("Disregarding Job get_usg_stats for unifi-%d", a.srv.ID))
		return nil
	}
	a.retry = true

	// Like the Python dict, a later device with the same name replaces an earlier one.
	var usg *device
	for i := range d.Data {
		if !d.Data[i].Name.Truthy() {
			continue
		}
		if name, ok := d.Data[i].Name.AsString(); ok && name == a.srv.USGName {
			usg = &d.Data[i]
		}
	}
	if usg == nil {
		a.log.Error(fmt.Sprintf("Could not find a USG named %s from your UniFi Controller", a.srv.USGName))
		return nil
	}

	fields, missing := usgFields(usg)
	if missing != "" {
		a.log.Error("Error building payload for unifi. Discarding. Error: " + missing)
		return nil
	}
	tags := collector.M{"model": usg.Model, "name": usg.Name}
	if a.srv.AddServerTag {
		tags["server"] = py.Int(int64(a.srv.ID))
	}
	return a.deps.Writer.Write(ctx, []influx.Point{collector.Point("UniFi", tags, fields, now)})
}

// usgFields builds the fields, all floats (Python wrote the WAN counters as whatever JSON number type the
// controller sent, which conflicts in InfluxDB between polls). missing names the first value that is absent or
// not a number, which discards the point as Python's KeyError did.
func usgFields(d *device) (collector.M, string) {
	if !d.Wan1.OK() {
		return nil, "'wan1'"
	}
	if !d.SysStats.OK() {
		return nil, "'sys_stats'"
	}
	if !d.SystemStats.OK() {
		return nil, "'system-stats'"
	}
	w, s, sys := d.Wan1.V(), d.SysStats.V(), d.SystemStats.V()
	values := []struct {
		field, key string
		v          py.Value
	}{
		{"bytes_current", "bytes-r", w.BytesR},
		{"rx_bytes_total", "rx_bytes", w.RxBytes},
		{"rx_bytes_current", "rx_bytes-r", w.RxBytesR},
		{"tx_bytes_total", "tx_bytes", w.TxBytes},
		{"tx_bytes_current", "tx_bytes-r", w.TxBytesR},
		{"cpu_loadavg_1", "loadavg_1", s.Loadavg1},
		{"cpu_loadavg_5", "loadavg_5", s.Loadavg5},
		{"cpu_loadavg_15", "loadavg_15", s.Loadavg15},
		{"cpu_util", "cpu", sys.CPU},
		{"mem_util", "mem", sys.Mem},
	}
	fields := collector.M{}
	for _, v := range values {
		if !v.v.Exists() {
			return nil, "'" + v.key + "'"
		}
		f, err := py.ToFloat(v.v)
		if err != nil {
			return nil, fmt.Sprintf("%s is not a number (%s)", v.key, v.v.Repr())
		}
		fields[v.field] = py.Float(f)
	}
	return fields, ""
}
