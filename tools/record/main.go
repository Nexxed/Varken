// Command record captures real API responses from your own services as golden-test fixtures.
//
//	go run ./tools/record -d /path/to/varken/data [-service sonarr] [-out testdata/fixtures]
//
// It reads varken.ini like Varken does, runs every scheduled job once against a local recording proxy in front of
// each configured server, and writes one testdata/fixtures/<service>/recorded_<job>/case.json per job. Nothing is
// written to InfluxDB. Responses are sanitised: API keys, user names, e-mail addresses, IP addresses and your
// server names are replaced with placeholders. Review the files before committing them, then record the Python
// expectations with tools/pygolden/harness.py and run go test.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nexxed/Varken/internal/app"
	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/influx"
	"github.com/Nexxed/Varken/internal/logging"
)

type exchange struct {
	Method string
	Path   string
	Query  url.Values
	Status int
	Body   []byte
	Cookie bool
}

type recorder struct {
	mu        sync.Mutex
	exchanges []exchange
}

func (r *recorder) take() []exchange {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.exchanges
	r.exchanges = nil
	return out
}

// proxy starts a recording reverse proxy to target and returns its URL (with target's path).
func (r *recorder) proxy(target *url.URL, verify bool) (string, func()) {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetURL(&url.URL{Scheme: target.Scheme, Host: target.Host})
			pr.Out.Host = target.Host
		},
		Transport: &http.Transport{Proxy: http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: !verify}}, //nolint:gosec // the server's own setting
		ModifyResponse: func(resp *http.Response) error {
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				return err
			}
			resp.Body = io.NopCloser(bytes.NewReader(body))
			r.mu.Lock()
			r.exchanges = append(r.exchanges, exchange{Method: resp.Request.Method, Path: resp.Request.URL.Path,
				Query: resp.Request.URL.Query(), Status: resp.StatusCode, Body: body,
				Cookie: len(resp.Header.Values("Set-Cookie")) > 0})
			r.mu.Unlock()
			return nil
		},
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	srv := &http.Server{Handler: rp, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return "http://" + ln.Addr().String() + strings.TrimRight(target.Path, "/"), func() { _ = srv.Close() }
}

// pythonJob maps a schedule tag suffix to the Python method and arguments the harness calls.
func pythonJob(service, suffix string, settings map[string]any) (string, map[string]any) {
	switch {
	case (service == "sonarr" || service == "lidarr") && suffix == "get_missing":
		return "get_calendar", map[string]any{"query": "Missing"}
	case (service == "sonarr" || service == "lidarr") && suffix == "get_future":
		return "get_calendar", map[string]any{"query": "Future"}
	case suffix == "history_events":
		return "get_history_events", map[string]any{"days": settings["history_events_days"]}
	case suffix == "tasks":
		return "get_tasks", nil
	case suffix == "library":
		return "get_library", nil
	case service == "radarr" && suffix == "get_calendar":
		return "get_calendar", map[string]any{"future_days": settings["calendar_days"]}
	case service == "tautulli" && suffix == "get_history_plays":
		return "get_history_plays", map[string]any{"days": settings["history_days"]}
	}
	return suffix, nil
}

// volatile query parameters depend on the clock or carry secrets; routes do not match on them.
var volatile = map[string]bool{"start": true, "end": true, "date": true, "after": true, "since": true,
	"apikey": true, "api_key": true}

var sensitiveKeys = map[string]bool{"apikey": true, "api_key": true, "password": true, "token": true,
	"username": true, "user": true, "user_name": true, "friendly_name": true, "displayname": true,
	"display_name": true, "email": true, "plexusername": true, "plex_username": true, "useralias": true,
	"ip_address": true, "ip_address_public": true, "machine_id": true, "machine_identifier": true,
	"mac": true, "serial": true, "player": true, "address": true, "remote_address": true, "user_thumb": true}

var (
	ipv4RE  = regexp.MustCompile(`\b(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3})\b`)
	emailRE = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

type sanitizer struct {
	hosts  []string
	names  map[string]string
	ips    map[string]string
	nextIP int
}

func (s *sanitizer) placeholder(key, value string) string {
	if value == "" {
		return value
	}
	if strings.Contains(key, "ip") || strings.Contains(key, "address") {
		return s.ip(value)
	}
	if p, ok := s.names[value]; ok {
		return p
	}
	p := fmt.Sprintf("%s-%d", strings.ReplaceAll(key, "_", "-"), len(s.names)+1)
	s.names[value] = p
	return p
}

func (s *sanitizer) ip(value string) string {
	if p, ok := s.ips[value]; ok {
		return p
	}
	s.nextIP++
	p := fmt.Sprintf("203.0.113.%d", s.nextIP%250+1)
	s.ips[value] = p
	return p
}

func (s *sanitizer) text(value string) string {
	for _, h := range s.hosts {
		if h != "" {
			value = strings.ReplaceAll(value, h, "example.invalid")
		}
	}
	value = emailRE.ReplaceAllString(value, "user@example.com")
	return ipv4RE.ReplaceAllStringFunc(value, s.ip)
}

func (s *sanitizer) walk(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = s.walk(strings.ToLower(k), child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = s.walk(key, child)
		}
		return t
	case string:
		if sensitiveKeys[key] {
			return s.placeholder(key, t)
		}
		return s.text(t)
	}
	return v
}

func (s *sanitizer) body(raw []byte) (json.RawMessage, string) {
	var v any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, s.text(string(raw))
	}
	out, _ := json.Marshal(s.walk("", v))
	return out, ""
}

func main() {
	folder := flag.String("d", "", "Varken data folder (with varken.ini)")
	only := flag.String("service", "", "record only this service")
	out := flag.String("out", filepath.Join("testdata", "fixtures"), "fixtures folder")
	flag.Parse()
	log := logging.New(os.Stderr)
	dataFolder := app.ResolveDataFolder(config.OSEnv, *folder)
	cfg, err := config.Load(dataFolder, config.OSEnv, log.Module("iniparser"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	rec := &recorder{}
	san := &sanitizer{names: map[string]string{}, ips: map[string]string{}}
	settings := map[string]map[string]any{}
	basePaths := map[string]string{}
	var stops []func()
	rewrite := func(service string, c *config.Common, full any) {
		u, err := url.Parse(c.URL)
		if err != nil {
			return
		}
		san.hosts = append(san.hosts, u.Hostname())
		var m map[string]any
		data, _ := json.Marshal(full)
		_ = json.Unmarshal(data, &m)
		for _, k := range []string{"url", "api_key", "username", "password", "maxmind_license_key", "fallback_ip"} {
			delete(m, k)
		}
		settings[fmt.Sprintf("%s-%d", service, c.ID)] = m
		basePaths[fmt.Sprintf("%s-%d", service, c.ID)] = strings.TrimRight(u.Path, "/")
		proxyURL, stop := rec.proxy(u, c.VerifySSL)
		stops = append(stops, stop)
		c.URL = proxyURL
	}
	for i := range cfg.Sonarr {
		rewrite("sonarr", &cfg.Sonarr[i].Common, cfg.Sonarr[i])
	}
	for i := range cfg.Radarr {
		rewrite("radarr", &cfg.Radarr[i].Common, cfg.Radarr[i])
	}
	for i := range cfg.Lidarr {
		rewrite("lidarr", &cfg.Lidarr[i].Common, cfg.Lidarr[i])
	}
	for i := range cfg.Tautulli {
		rewrite("tautulli", &cfg.Tautulli[i].Common, cfg.Tautulli[i])
	}
	for i := range cfg.Ombi {
		rewrite("ombi", &cfg.Ombi[i].Common, cfg.Ombi[i])
	}
	for i := range cfg.Overseerr {
		rewrite("overseerr", &cfg.Overseerr[i].Common, cfg.Overseerr[i])
	}
	for i := range cfg.SickChill {
		rewrite("sickchill", &cfg.SickChill[i].Common, cfg.SickChill[i])
	}
	for i := range cfg.UniFi {
		rewrite("unifi", &cfg.UniFi[i].Common, cfg.UniFi[i])
	}
	for i := range cfg.Cleanuparr {
		rewrite("cleanuparr", &cfg.Cleanuparr[i].Common, cfg.Cleanuparr[i])
	}
	defer func() {
		for _, stop := range stops {
			stop()
		}
	}()

	now := time.Now()
	deps := collector.Deps{Writer: discard{}, Now: func() time.Time { return now }}
	ctx := context.Background()
	written := 0
	for _, job := range app.Jobs(cfg, deps, log, nil) {
		if job.Once {
			continue
		}
		parts := strings.SplitN(job.Name, "-", 3)
		if len(parts) != 3 || (*only != "" && parts[0] != *only) {
			continue
		}
		service, id, suffix := parts[0], parts[1], parts[2]
		rec.take()
		if err := job.Run(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", job.Name, err)
		}
		exchanges := rec.take()
		if len(exchanges) == 0 {
			continue
		}
		method, args := pythonJob(service, suffix, settings[service+"-"+id])
		routes := []map[string]any{}
		for _, ex := range exchanges {
			route := map[string]any{"method": ex.Method, "path": ex.Path, "status": ex.Status}
			match := map[string]any{}
			keys := make([]string, 0, len(ex.Query))
			for k := range ex.Query {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if !volatile[strings.ToLower(k)] {
					match[k] = ex.Query.Get(k)
				}
			}
			if len(match) > 0 {
				route["match_query"] = match
			}
			if ex.Cookie {
				route["headers"] = map[string]string{"Set-Cookie": "unifises=recorded; Path=/"}
			}
			if body, text := san.body(ex.Body); body != nil {
				route["body"] = body
			} else if text != "" {
				route["body_text"] = text
			}
			routes = append(routes, route)
		}
		tz := os.Getenv("TZ")
		if tz == "" {
			tz = "UTC"
		}
		c := map[string]any{
			"job": method, "description": fmt.Sprintf("Recorded from a live %s on %s; review before committing.",
				service, now.Format("2006-01-02")),
			"now": now.Format(time.RFC3339Nano), "tz": tz, "server": settings[service+"-"+id], "routes": routes,
		}
		if len(args) > 0 {
			c["args"] = args
		}
		if bp := basePaths[service+"-"+id]; bp != "" {
			c["base_path"] = bp
		}
		dir := filepath.Join(*out, service, "recorded_"+strings.ReplaceAll(suffix, "-", "_"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		data, _ := json.MarshalIndent(c, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "case.json"), append(data, '\n'), 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		written++
		fmt.Printf("wrote %s (%d responses)\n", dir, len(exchanges))
	}
	fmt.Printf("%d cases recorded. Review them, then run tools/pygolden/harness.py on them and go test ./...\n", written)
}

type discard struct{}

func (discard) Write(context.Context, []influx.Point) error { return nil }
