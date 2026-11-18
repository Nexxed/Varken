package config

import (
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// ExampleINI is data/varken.example.ini, embedded so a first run can create varken.ini even when the example
// file is not in the data folder. A test keeps the two copies identical.
//
//go:embed varken.example.ini
var ExampleINI string

// Config is the effective configuration.
type Config struct {
	Influx            influx.Settings
	MaxmindLicenseKey string
	// AddServerTag ([global] add_server_tag, Go only) adds the server tag to the Ombi and Overseerr count points
	// and the UniFi point, which the Python version wrote without one.
	AddServerTag bool

	Sonarr     []SonarrServer
	Radarr     []RadarrServer
	Lidarr     []LidarrServer
	Tautulli   []TautulliServer
	Ombi       []OmbiServer
	Overseerr  []OverseerrServer
	SickChill  []SickChillServer
	UniFi      []UniFiServer
	Cleanuparr []CleanuparrServer

	// Enabled holds the server ids each service was enabled with; a missing service is disabled.
	Enabled map[string][]int
	// Secrets are masked in logs completely; Hosts keep their last five characters, as before.
	Secrets []string
	Hosts   []string
	// Created is set when varken.ini did not exist and was created from the example.
	Created bool
}

// AnyEnabled reports whether at least one server of any service is configured.
func (c *Config) AnyEnabled() bool {
	return len(c.Sonarr)+len(c.Radarr)+len(c.Lidarr)+len(c.Tautulli)+len(c.Ombi)+len(c.Overseerr)+
		len(c.SickChill)+len(c.UniFi)+len(c.Cleanuparr) > 0
}

// Env looks up an environment variable.
type Env func(string) (string, bool)

// OSEnv reads the process environment.
func OSEnv(name string) (string, bool) { return os.LookupEnv(name) }

// ErrNotConfigured is returned when [influxdb] still points at the example host.
var ErrNotConfigured = errors.New("you have not configured your varken.ini. Please read Wiki page for configuration")

// Load reads varken.ini from the data folder, creating it from the example on first run. It never rewrites an
// existing file: missing options fall back to their defaults in memory.
func Load(dataFolder string, env Env, log *slog.Logger) (*Config, error) {
	path := filepath.Join(dataFolder, "varken.ini")
	created := false
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		log.Error(fmt.Sprintf("File missing (varken.ini) in %s", dataFolder))
		example, rerr := os.ReadFile(filepath.Join(dataFolder, "varken.example.ini"))
		if rerr != nil {
			example = []byte(ExampleINI)
		}
		log.Debug("Creating varken.ini from varken.example.ini")
		if werr := os.WriteFile(path, example, 0o644); werr != nil {
			return nil, fmt.Errorf("varken does not have permission to write to %s: %w", dataFolder, werr)
		}
		created = true
	}
	log.Debug("Reading from varken.ini")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %w", path, err)
	}
	cfg, err := FromINI(ParseINI(string(data)), env, log)
	if cfg != nil {
		cfg.Created = created
	}
	return cfg, err
}

type loader struct {
	ini     *INI
	env     Env
	log     *slog.Logger
	secrets []string
	hosts   []string
}

// FromINI builds the configuration from a parsed file and the environment.
func FromINI(ini *INI, env Env, log *slog.Logger) (*Config, error) {
	for _, w := range ini.Warnings {
		log.Warn("varken.ini " + w)
	}
	l := &loader{ini: ini, env: env, log: log}
	cfg := &Config{Enabled: map[string][]int{}}

	settings, err := l.influx()
	if err != nil {
		return nil, err
	}
	cfg.Influx = settings
	cfg.MaxmindLicenseKey, _ = l.value("global", "VRKN_GLOBAL_MAXMIND_LICENSE_KEY", "maxmind_license_key")
	if cfg.MaxmindLicenseKey != "" {
		l.secrets = append(l.secrets, cfg.MaxmindLicenseKey)
	}
	if raw, ok := l.value("global", "VRKN_GLOBAL_ADD_SERVER_TAG", "add_server_tag"); ok {
		cfg.AddServerTag = boolcheck(raw)
	}

	for _, service := range Services {
		ids := l.enabledIDs(service)
		if ids == nil {
			continue
		}
		cfg.Enabled[service] = ids
		for _, id := range ids {
			l.addServer(cfg, service, id)
		}
		if serverCount(cfg, service) == 0 {
			log.Error(fmt.Sprintf("No usable %s servers: %s is disabled.", service, upper(service)))
		}
	}
	cfg.Secrets = dedupe(l.secrets)
	cfg.Hosts = dedupe(l.hosts)
	return cfg, nil
}

func serverCount(cfg *Config, service string) int {
	return reflect.ValueOf(cfg).Elem().FieldByName(serviceField(service)).Len()
}

func serviceField(service string) string {
	switch service {
	case "sickchill":
		return "SickChill"
	case "unifi":
		return "UniFi"
	}
	return strings.ToUpper(service[:1]) + service[1:]
}

func dedupe(items []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range items {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func upper(s string) string { return py.Upper(s) }

// value returns the env var if set, else the ini option. ok is false when neither is set.
func (l *loader) value(section, envName, option string) (string, bool) {
	if v, ok := l.env(envName); ok {
		return v, true
	}
	return l.ini.Get(section, option)
}

func (l *loader) influx() (influx.Settings, error) {
	var out influx.Settings
	vals := map[string]string{}
	for _, k := range influxKeys {
		v, ok := l.value("influxdb", "VRKN_INFLUXDB_"+upper(k.ini), k.ini)
		if !ok {
			if !k.optional {
				l.log.Error(fmt.Sprintf("Missing key in influxdb: %s. Using the default %q", k.ini, k.def))
			}
			v = k.def
		}
		vals[k.ini] = v
	}
	if !urlCheck(vals["url"], false) {
		return out, fmt.Errorf("%s is invalid in module [influxdb]! URL must host/IP. ie. localhost", vals["url"])
	}
	port, err := py.ParseInt(vals["port"])
	if err != nil {
		return out, fmt.Errorf("invalid configuration value in [influxdb]: port = %q is not a whole number",
			vals["port"])
	}
	out = influx.Settings{URL: vals["url"], Port: int(port), SSL: boolcheck(vals["ssl"]),
		VerifySSL: boolcheck(vals["verify_ssl"]), Username: vals["username"], Password: vals["password"],
		Database: vals["database"]}
	l.secrets = append(l.secrets, out.Username, out.Password)
	l.addHost(out.URL)
	return out, nil
}

// enabledIDs is enable_check + clean_sid_check: nil when the service is off.
func (l *loader) enabledIDs(service string) []int {
	option := service + "_server_ids"
	label := upper(option)
	raw, ok := l.value("global", "VRKN_GLOBAL_"+label, option)
	if !ok {
		raw = "false"
	}
	if lower := py.Lower(raw); lower == "false" || lower == "no" {
		l.log.Info(label + " disabled.")
		return nil
	}
	var ids []int
	for _, part := range strings.Split(strings.ReplaceAll(raw, " ", ""), ",") {
		id, err := py.ParseInt(part)
		if err != nil {
			l.log.Error(fmt.Sprintf("%s is not a valid server id number", part))
			continue
		}
		if slices.Contains(ids, int(id)) {
			l.log.Warn(fmt.Sprintf("%s lists server %d more than once; it is used once", label, id))
			continue
		}
		ids = append(ids, int(id))
	}
	if len(ids) == 0 {
		l.log.Error("No valid " + label)
		return nil
	}
	l.log.Info(fmt.Sprintf("%s : %v", label, pyList(ids)))
	return ids
}

func pyList(ids []int) string {
	parts := make([]string, len(ids))
	for i, id := range ids {
		parts[i] = strconv.Itoa(id)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func boolcheck(v string) bool {
	lower := py.Lower(v)
	return lower == "true" || lower == "yes"
}

// urlCheck is iniparser.url_check: a host name, domain or IPv4 address, optionally a port, then an optional path.
var (
	urlWithPort    = regexp.MustCompile(`(?i)^` + urlHost + `(?::` + pyDigit + `+)?(?:/?|[/?]` + pyNonSpace + `+)$`)
	urlWithoutPort = regexp.MustCompile(`(?i)^` + urlHost + `(?:/?|[/?]` + pyNonSpace + `+)$`)
)

// Python's re with IGNORECASE also matches the dotless ı for i, and its \d and \S are Unicode-aware; the classes
// below spell that out for Go's regexp.
const (
	pyDigit    = `\p{Nd}`
	pyNonSpace = `[^\s\x{0b}\x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]`
	urlHost    = `(?:(?:[a-z0-9\x{131}]+(?:-[a-z0-9\x{131}]+)*\.)+[a-z\x{131}]{2,}|localhost|[a-zA-Z0-9_\x{131}-]*|` +
		pyDigit + `{1,3}\.` + pyDigit + `{1,3}\.` + pyDigit + `{1,3}\.` + pyDigit + `{1,3})`
)

func urlCheck(u string, includePort bool) bool {
	if includePort {
		return urlWithPort.MatchString(u)
	}
	return urlWithoutPort.MatchString(u)
}

func (l *loader) addHost(u string) {
	l.hosts = append(l.hosts, u)
	if host, _, ok := strings.Cut(u, "/"); ok {
		l.hosts = append(l.hosts, host)
	}
	if host, _, ok := strings.Cut(u, ":"); ok {
		l.hosts = append(l.hosts, host)
	}
}

// addServer reads one [service-id] section (and its VRKN_SERVICE_ID_* variables) into the config.
func (l *loader) addServer(cfg *Config, service string, id int) {
	section := fmt.Sprintf("%s-%d", service, id)
	prefix := fmt.Sprintf("VRKN_%s_", upper(fmt.Sprintf("%s_%d", service, id)))
	get := func(option string, envNames []string) (string, bool) {
		for _, name := range envNames {
			if v, ok := l.env(prefix + name); ok {
				return v, true
			}
		}
		return l.ini.Get(section, option)
	}
	if _, ok := get("url", []string{"URL"}); !ok {
		l.log.Error(fmt.Sprintf("Missing section [%s] (and no %sURL): skipping %s server %d", section, prefix,
			service, id))
		return
	}
	fail := func(format string, args ...any) {
		l.log.Error(fmt.Sprintf("Invalid configuration value in %s. Error: %s", section, fmt.Sprintf(format, args...)))
	}

	rawURL, _ := get("url", []string{"URL"})
	if !urlCheck(rawURL, true) {
		fail("%s is invalid in module [%s]! URL must host/IP and port if not 80 or 443. ie. localhost:8080",
			rawURL, section)
		return
	}
	sslRaw, _ := get("ssl", []string{"SSL"})
	verifyRaw, _ := get("verify_ssl", []string{"VERIFY_SSL"})
	ssl, verify := boolcheck(sslRaw), boolcheck(verifyRaw)
	scheme := "http://"
	if ssl {
		scheme = "https://"
	} else {
		verify = false
	}
	common := Common{ID: id, URL: scheme + rawURL, VerifySSL: verify}
	if service != "unifi" {
		apikey, ok := get("apikey", []string{"APIKEY"})
		if !ok {
			l.log.Error(fmt.Sprintf("Missing key in %s. Error: No option 'apikey' in section: '%s'", section, section))
		}
		common.APIKey = apikey
		l.secrets = append(l.secrets, apikey)
	}
	l.addHost(rawURL)

	values := map[string]any{}
	for _, k := range serviceKeys[service] {
		raw, ok := get(k.ini, k.envNames())
		if !ok {
			if !k.optional {
				l.log.Warn(fmt.Sprintf("Missing key in %s: %s. Using the default %q", section, k.ini, k.def))
			}
			raw = k.def
		}
		switch k.kind {
		case kindBool:
			values[k.field] = boolcheck(raw)
		case kindInt:
			v, err := py.ParseInt(raw)
			if err != nil {
				fail("invalid literal for int() with base 10: %s (%s)", py.ReprString(raw), k.ini)
				return
			}
			values[k.field] = int(v)
		default:
			values[k.field] = raw
		}
	}

	switch service {
	case "sonarr", "lidarr":
		var srv SonarrServer
		srv.Common = common
		setFields(&srv, values)
		if service == "sonarr" {
			cfg.Sonarr = append(cfg.Sonarr, srv)
		} else {
			cfg.Lidarr = append(cfg.Lidarr, srv)
		}
	case "radarr":
		var srv RadarrServer
		srv.Common = common
		setFields(&srv, values)
		cfg.Radarr = append(cfg.Radarr, srv)
	case "tautulli":
		var srv TautulliServer
		srv.Common = common
		setFields(&srv, values)
		if err := checkFallbackIP(srv.FallbackIP); err != nil {
			fail("Invalid fallback_ip [%s] set for %s: %v", srv.FallbackIP, section, err)
			return
		}
		srv.MaxmindLicenseKey = cfg.MaxmindLicenseKey
		cfg.Tautulli = append(cfg.Tautulli, srv)
	case "ombi":
		var srv OmbiServer
		srv.Common = common
		setFields(&srv, values)
		srv.AddServerTag = cfg.AddServerTag
		cfg.Ombi = append(cfg.Ombi, srv)
	case "overseerr":
		var srv OverseerrServer
		srv.Common = common
		setFields(&srv, values)
		srv.AddServerTag = cfg.AddServerTag
		if !srv.GetLatestRequests && srv.NumLatestRequestsToFetch > 0 {
			l.log.Warn(fmt.Sprintf("%s: get_latest_requests = false has never switched the latest-requests job off "+
				"and still does not; set num_latest_requests_to_fetch = 0 to turn it off", section))
		}
		cfg.Overseerr = append(cfg.Overseerr, srv)
	case "sickchill":
		var srv SickChillServer
		srv.Common = common
		setFields(&srv, values)
		cfg.SickChill = append(cfg.SickChill, srv)
	case "unifi":
		var srv UniFiServer
		srv.Common = common
		setFields(&srv, values)
		srv.Site = py.Lower(srv.Site)
		srv.AddServerTag = cfg.AddServerTag
		l.secrets = append(l.secrets, srv.Username, srv.Password)
		cfg.UniFi = append(cfg.UniFi, srv)
	case "cleanuparr":
		var srv CleanuparrServer
		srv.Common = common
		setFields(&srv, values)
		srv.EventsSeverity = py.Strip(srv.EventsSeverity)
		cfg.Cleanuparr = append(cfg.Cleanuparr, srv)
	}
}

// setFields fills struct fields (including embedded ones) by their JSON names.
func setFields(ptr any, values map[string]any) {
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		t := v.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.Anonymous {
				walk(v.Field(i))
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if val, ok := values[name]; ok {
				v.Field(i).Set(reflect.ValueOf(val).Convert(f.Type))
			}
		}
	}
	walk(reflect.ValueOf(ptr).Elem())
}

// checkFallbackIP accepts a public IPv4 address (Python's rule) or a public IPv6 address (which crashed Python).
func checkFallbackIP(ip string) error {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" {
		return fmt.Errorf("not an IP address")
	}
	if addr.Is4() {
		if isPrivateV4(addr) {
			return fmt.Errorf("it is a private address; use your public (WAN) IP")
		}
		return nil
	}
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || netip.MustParsePrefix("2001:db8::/32").Contains(addr) ||
		addr.Is4In6() {
		return fmt.Errorf("it is not a public address; use your public (WAN) IP")
	}
	return nil
}

// isPrivateV4 is Python's IPv4Address.is_private (3.13).
func isPrivateV4(addr netip.Addr) bool {
	for _, p := range []string{"192.0.0.9/32", "192.0.0.10/32"} {
		if netip.MustParsePrefix(p).Contains(addr) {
			return false
		}
	}
	for _, p := range []string{"0.0.0.0/8", "10.0.0.0/8", "127.0.0.0/8", "169.254.0.0/16", "172.16.0.0/12",
		"192.0.0.0/24", "192.0.0.170/31", "192.0.2.0/24", "192.168.0.0/16", "198.18.0.0/15", "198.51.100.0/24",
		"203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32"} {
		if netip.MustParsePrefix(p).Contains(addr) {
			return true
		}
	}
	return false
}
