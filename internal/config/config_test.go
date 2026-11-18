package config

import (
	"bufio"
	"encoding/json"
	"flag"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

var updateGo = flag.Bool("update-config", false, "rewrite go.json for the cases listed in divergences")

// divergences are the config cases where Go deliberately differs from Python. Their expected Go result is
// go.json (reviewed by hand); every other case must match python.json.
var divergences = map[string]string{
	"duplicate_server_ids":   "Python created the same server twice; Go keeps one.",
	"section_for_id_missing": "Python crashed (NoSectionError); Go skips the server with no section.",
	"private_fallback_ip":    "Python exited; Go drops only the Tautulli server.",
	"ipv6_fallback_ip":       "Python skipped the server (AddressValueError); Go accepts a public IPv6 address.",
}

func repoFile(parts ...string) string {
	return filepath.Join(append([]string{"..", ".."}, parts...)...)
}

func quiet() *slog.Logger { return slog.New(slog.DiscardHandler) }

// goResult renders a Config in python.json's shape.
func goResult(t *testing.T, cfg *Config) map[string]any {
	t.Helper()
	services := map[string]any{}
	for _, service := range Services {
		var enabled any
		if ids, ok := cfg.Enabled[service]; ok {
			enabled = ids
		}
		list := reflect.ValueOf(cfg).Elem().FieldByName(serviceField(service))
		servers := []any{}
		for i := 0; i < list.Len(); i++ {
			data, err := json.Marshal(list.Index(i).Interface())
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			_ = json.Unmarshal(data, &m)
			// Go-only settings.
			delete(m, "add_server_tag")
			delete(m, "get_latest_requests")
			if service == "unifi" {
				delete(m, "api_key") // UniFi has no API key
			}
			servers = append(servers, m)
		}
		services[service] = map[string]any{"enabled": enabled, "servers": servers}
	}
	return map[string]any{
		"influx": map[string]any{"url": cfg.Influx.URL, "port": cfg.Influx.Port, "ssl": cfg.Influx.SSL,
			"verify_ssl": cfg.Influx.VerifySSL, "username": cfg.Influx.Username, "password": cfg.Influx.Password},
		"services": services,
	}
}

func normalize(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	_ = json.Unmarshal(data, &generic)
	out, _ := json.MarshalIndent(generic, "", "  ")
	return string(out)
}

func pythonEnabled(v any) any {
	if v == false {
		return nil
	}
	return v
}

// TestMatchesPythonConfig loads every testdata/config case with the same ini and environment the Python parser
// was given (tools/pygolden/config_dump.py) and compares the effective settings.
func TestMatchesPythonConfig(t *testing.T) {
	dirs, _ := filepath.Glob(repoFile("testdata", "config", "*"))
	if len(dirs) < 15 {
		t.Fatalf("only %d config cases", len(dirs))
	}
	for _, dir := range dirs {
		name := filepath.Base(dir)
		t.Run(name, func(t *testing.T) {
			folder := t.TempDir()
			if data, err := os.ReadFile(filepath.Join(dir, "varken.ini")); err == nil {
				if err := os.WriteFile(filepath.Join(folder, "varken.ini"), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			example, _ := os.ReadFile(repoFile("data", "varken.example.ini"))
			_ = os.WriteFile(filepath.Join(folder, "varken.example.ini"), example, 0o644)
			env := map[string]string{}
			if data, err := os.ReadFile(filepath.Join(dir, "env.json")); err == nil {
				_ = json.Unmarshal(data, &env)
			}
			before, _ := os.ReadFile(filepath.Join(folder, "varken.ini"))
			cfg, err := Load(folder, func(k string) (string, bool) { v, ok := env[k]; return v, ok }, quiet())
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			after, _ := os.ReadFile(filepath.Join(folder, "varken.ini"))
			if len(before) > 0 && string(before) != string(after) {
				t.Error("varken.ini must never be rewritten")
			}
			got := normalize(t, goResult(t, cfg))

			if reason, ok := divergences[name]; ok {
				path := filepath.Join(dir, "go.json")
				if *updateGo {
					if err := os.WriteFile(path, []byte(got+"\n"), 0o644); err != nil {
						t.Fatal(err)
					}
					return
				}
				want, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("%s: %v (reason: %s)", path, err, reason)
				}
				if got+"\n" != string(want) {
					t.Errorf("differs from go.json (%s):\n%s", reason, got)
				}
				return
			}

			var py map[string]any
			data, err := os.ReadFile(filepath.Join(dir, "python.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(data, &py); err != nil {
				t.Fatal(err)
			}
			if py["exit"] != nil || py["exception"] != nil {
				t.Fatalf("Python failed on this case (exit=%v exception=%v): list it in divergences",
					py["exit"], py["exception"])
			}
			services := py["services"].(map[string]any)
			for _, v := range services {
				m := v.(map[string]any)
				m["enabled"] = pythonEnabled(m["enabled"])
			}
			want := normalize(t, map[string]any{"influx": py["influx"], "services": services})
			if got != want {
				t.Errorf("effective config differs from Python:\n%s", diffLines(want, got))
			}
			if created, _ := py["created"].(bool); created != cfg.Created {
				t.Errorf("created = %v, Python %v", cfg.Created, created)
			}
		})
	}
}

func diffLines(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	var b strings.Builder
	for i := 0; i < len(w) || i < len(g); i++ {
		var wl, gl string
		if i < len(w) {
			wl = w[i]
		}
		if i < len(g) {
			gl = g[i]
		}
		if wl != gl {
			b.WriteString("- " + wl + "\n+ " + gl + "\n")
		}
	}
	return b.String()
}

func readCorpus(t *testing.T, name string, each func(row map[string]json.RawMessage)) {
	t.Helper()
	f, err := os.Open(repoFile("testdata", "pycompat", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var row map[string]json.RawMessage
		if err := json.Unmarshal(sc.Bytes(), &row); err != nil {
			t.Fatal(err)
		}
		each(row)
	}
}

func TestURLCheckMatchesPython(t *testing.T) {
	readCorpus(t, "url_check.jsonl", func(row map[string]json.RawMessage) {
		var url string
		var includePort, valid bool
		_ = json.Unmarshal(row["url"], &url)
		_ = json.Unmarshal(row["include_port"], &includePort)
		_ = json.Unmarshal(row["valid"], &valid)
		if got := urlCheck(url, includePort); got != valid {
			t.Errorf("url_check(%q, include_port=%v) = %v, Python %v", url, includePort, got, valid)
		}
	})
}

func TestBoolcheckMatchesPython(t *testing.T) {
	readCorpus(t, "boolcheck.jsonl", func(row map[string]json.RawMessage) {
		var s string
		var want bool
		_ = json.Unmarshal(row["s"], &s)
		_ = json.Unmarshal(row["bool"], &want)
		if boolcheck(s) != want {
			t.Errorf("boolcheck(%q) != %v", s, want)
		}
	})
}

func TestPrivateIPv4MatchesPython(t *testing.T) {
	readCorpus(t, "rfc1918.jsonl", func(row map[string]json.RawMessage) {
		var ip string
		_ = json.Unmarshal(row["ip"], &ip)
		var private bool
		if json.Unmarshal(row["private"], &private) != nil {
			// Python raised AddressValueError: not an IPv4 address.
			if err := checkFallbackIP(ip); err == nil && !strings.Contains(ip, ":") {
				t.Errorf("%q accepted, Python rejected it", ip)
			}
			return
		}
		if got := checkFallbackIP(ip) != nil; got != private {
			t.Errorf("fallback %q rejected=%v, Python is_private=%v", ip, got, private)
		}
	})
}

func TestFallbackIPv6(t *testing.T) {
	for ip, ok := range map[string]bool{"2606:4700:4700::1111": true, "::1": false, "fe80::1": false,
		"fd00::1": false, "2001:db8::1": false, "::ffff:1.1.1.1": false, "example.com": false} {
		if (checkFallbackIP(ip) == nil) != ok {
			t.Errorf("checkFallbackIP(%q) ok=%v", ip, !ok)
		}
	}
}

func TestINIParser(t *testing.T) {
	ini := ParseINI("\ufeff[DEFAULT]\nshared = yes\n\n[a]\nKey = value # not a comment\nsemi: x;y\nmulti = one\n" +
		"   two\n\n   three\n  # comment inside\n\nempty =\n[a]\nkey = again\n= nameless\njunk line\n" +
		"[b] trailing\nother = 1\n")
	if v, _ := ini.Get("a", "key"); v != "again" {
		t.Errorf("key = %q", v)
	}
	if v, _ := ini.Get("a", "semi"); v != "x;y" {
		t.Errorf("semi = %q", v)
	}
	if v, _ := ini.Get("a", "multi"); v != "one\ntwo\n\nthree" {
		t.Errorf("multi = %q", v)
	}
	if v, ok := ini.Get("a", "empty"); !ok || v != "" {
		t.Errorf("empty = %q %v", v, ok)
	}
	if v, _ := ini.Get("b", "shared"); v != "yes" {
		t.Errorf("DEFAULT not inherited: %q", v)
	}
	if _, ok := ini.Get("missing", "shared"); ok {
		t.Error("DEFAULT must not create sections")
	}
	if !ini.HasSection("b") || ini.HasSection("DEFAULT") {
		t.Error("sections")
	}
	if got := ini.Sections(); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("sections = %v", got)
	}
	if len(ini.Options("b")) != 2 {
		t.Errorf("options = %v", ini.Options("b"))
	}
	sort.Strings(ini.Warnings)
	if len(ini.Warnings) != 4 {
		t.Errorf("warnings = %q", ini.Warnings)
	}
	orphan := ParseINI("key = value\n")
	if len(orphan.Warnings) != 1 {
		t.Error("an option before any section should warn")
	}
}

func TestEmbeddedExampleMatchesDataFolder(t *testing.T) {
	data, err := os.ReadFile(repoFile("data", "varken.example.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != ExampleINI {
		t.Fatal("internal/config/varken.example.ini must be a copy of data/varken.example.ini")
	}
}

func TestFirstRunWithoutExampleUsesEmbedded(t *testing.T) {
	folder := t.TempDir()
	cfg, err := Load(folder, func(string) (string, bool) { return "", false }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Created || cfg.Influx.URL != "influxdb.domain.tld" {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := os.Stat(filepath.Join(folder, "varken.ini")); err != nil {
		t.Error("varken.ini not created")
	}
}

func TestLoadErrors(t *testing.T) {
	folder := t.TempDir()
	write := func(text string) {
		if err := os.WriteFile(filepath.Join(folder, "varken.ini"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	noEnv := func(string) (string, bool) { return "", false }
	write("[influxdb]\nurl = http://influx\n")
	if _, err := Load(folder, noEnv, quiet()); err == nil {
		t.Error("a scheme in the influx url must be rejected")
	}
	write("[influxdb]\nurl = influx\nport = 80.5\n")
	if _, err := Load(folder, noEnv, quiet()); err == nil {
		t.Error("a bad port must be rejected")
	}
	if err := os.Chmod(folder, 0o500); err == nil && os.Getuid() != 0 {
		_ = os.Remove(filepath.Join(folder, "varken.ini"))
		if _, err := Load(folder, noEnv, quiet()); err == nil {
			t.Error("an unwritable folder must fail on first run")
		}
		_ = os.Chmod(folder, 0o700)
	}
}

func TestEnvOnlyServerAndSettings(t *testing.T) {
	env := map[string]string{
		"VRKN_GLOBAL_SONARR_SERVER_IDS": "4",
		"VRKN_SONARR_4_URL":             "sonarr.lan:8989",
		"VRKN_SONARR_4_APIKEY":          "envonlykey",
		"VRKN_GLOBAL_ADD_SERVER_TAG":    "true",
		"VRKN_GLOBAL_UNIFI_SERVER_IDS":  "1",
		"VRKN_UNIFI_1_URL":              "unifi:8443",
		"VRKN_UNIFI_1_SITE":             "ΣITE",
		"VRKN_INFLUXDB_DATABASE":        "shadow",
	}
	cfg, err := FromINI(ParseINI("[influxdb]\nurl = influx\n"), func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Sonarr) != 1 || cfg.Sonarr[0].URL != "http://sonarr.lan:8989" || cfg.Sonarr[0].APIKey != "envonlykey" ||
		cfg.Sonarr[0].QueueRunSeconds != 300 || cfg.Sonarr[0].MissingDays != 7 {
		t.Errorf("sonarr = %+v", cfg.Sonarr)
	}
	if !cfg.AddServerTag || !cfg.UniFi[0].AddServerTag || cfg.UniFi[0].Site != "σite" {
		t.Errorf("unifi = %+v", cfg.UniFi[0])
	}
	if cfg.Influx.Database != "shadow" {
		t.Errorf("database = %q", cfg.Influx.Database)
	}
	if !contains(cfg.Secrets, "envonlykey") {
		t.Errorf("env secrets must be masked too: %v", cfg.Secrets)
	}
	if !cfg.AnyEnabled() {
		t.Error("AnyEnabled")
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func FuzzParseINI(f *testing.F) {
	f.Add("[a]\nk = v\n  cont\n")
	f.Add("[DEFAULT]\nx:1\n[b]\n#c\n;d\n= e\n")
	f.Fuzz(func(t *testing.T, text string) {
		ini := ParseINI(text)
		for _, s := range ini.Sections() {
			for k := range ini.Options(s) {
				if k != strings.ToLower(k) {
					t.Fatalf("option %q not lower-cased", k)
				}
			}
		}
	})
}

func FuzzURLCheck(f *testing.F) {
	for _, s := range []string{"localhost", "sonarr.domain.tld:8989/x", "1.2.3.4", "ı.com", "a b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, u string) {
		// The port is optional when allowed, so anything valid without one is valid with one allowed.
		if urlCheck(u, false) && !urlCheck(u, true) {
			t.Fatalf("%q valid without a port but not with one allowed", u)
		}
	})
}
