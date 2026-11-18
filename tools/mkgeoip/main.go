// Command mkgeoip builds the small GeoLite2-City-shaped database the tests use
// (testdata/geoip/GeoLite2-City-Test.mmdb). Both the Go tests and the Python golden harness read it, so the
// Tautulli fixtures exercise real lookups without MaxMind's licensed data.
//
//	go run ./tools/mkgeoip
package main

import (
	"flag"
	"log"
	"net"
	"os"
	"path/filepath"

	"github.com/maxmind/mmdbwriter"
	"github.com/maxmind/mmdbwriter/mmdbtype"
)

type subdivision struct {
	iso   string
	names map[string]string
}

type record struct {
	network      string
	city         map[string]string
	subdivisions []subdivision
	lat, lon     *float64
}

func f(v float64) *float64 { return &v }

// Records cover every shape the Tautulli collector handles: full data, several subdivisions, no city, a city
// named only in another locale, a 0/0 location, a subdivision without an ISO code, half a location and IPv6.
var records = []record{
	{network: "203.0.113.0/24", city: map[string]string{"en": "Springfield", "de": "Springfeld"},
		subdivisions: []subdivision{{"IL", map[string]string{"en": "Illinois"}}}, lat: f(39.7817), lon: f(-89.6501)},
	{network: "198.51.100.0/24", city: map[string]string{"en": "London", "fr": "Londres"},
		subdivisions: []subdivision{{"ENG", map[string]string{"en": "England"}}, {"LND", map[string]string{"en": "City of London"}}},
		lat:          f(51.5142), lon: f(-0.0931)},
	{network: "192.0.2.0/24", lat: f(48.8566), lon: f(2.3522)},
	{network: "1.1.1.0/24", city: map[string]string{"en": "Research"},
		subdivisions: []subdivision{{"VIC", map[string]string{"en": "Victoria"}}}, lat: f(-37.7), lon: f(145.1833)},
	{network: "8.8.8.0/24", city: map[string]string{"de": "Bergstadt"}, lat: f(37.751), lon: f(-97.822)},
	{network: "9.9.9.0/24", city: map[string]string{"en": "Zero"}, lat: f(0), lon: f(0)},
	{network: "4.4.4.0/24", city: map[string]string{"en": "Nocode"},
		subdivisions: []subdivision{{"", map[string]string{"en": "Somewhere"}}}, lat: f(10.5), lon: f(20.25)},
	{network: "5.5.5.0/24", city: map[string]string{"en": "Halfway"}, lat: f(12.5)},
	{network: "2001:db8::/32", city: map[string]string{"en": "Sixton"},
		subdivisions: []subdivision{{"CA", map[string]string{"en": "California"}}}, lat: f(37.386), lon: f(-122.0838)},
}

func names(m map[string]string) mmdbtype.Map {
	out := mmdbtype.Map{}
	for k, v := range m {
		out[mmdbtype.String(k)] = mmdbtype.String(v)
	}
	return out
}

func main() {
	out := flag.String("out", filepath.Join("testdata", "geoip", "GeoLite2-City-Test.mmdb"), "output file")
	flag.Parse()

	tree, err := mmdbwriter.New(mmdbwriter.Options{
		DatabaseType:            "GeoLite2-City",
		Description:             map[string]string{"en": "Varken test data (not real locations)"},
		Languages:               []string{"en", "de", "fr"},
		IncludeReservedNetworks: true,
		RecordSize:              24,
		BuildEpoch:              1767225600,
	})
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range records {
		_, network, err := net.ParseCIDR(r.network)
		if err != nil {
			log.Fatal(err)
		}
		data := mmdbtype.Map{}
		if r.city != nil {
			data["city"] = mmdbtype.Map{"names": names(r.city)}
		}
		if len(r.subdivisions) > 0 {
			subs := mmdbtype.Slice{}
			for _, s := range r.subdivisions {
				m := mmdbtype.Map{"names": names(s.names)}
				if s.iso != "" {
					m["iso_code"] = mmdbtype.String(s.iso)
				}
				subs = append(subs, m)
			}
			data["subdivisions"] = subs
		}
		location := mmdbtype.Map{}
		if r.lat != nil {
			location["latitude"] = mmdbtype.Float64(*r.lat)
		}
		if r.lon != nil {
			location["longitude"] = mmdbtype.Float64(*r.lon)
		}
		if len(location) > 0 {
			data["location"] = location
		}
		if err := tree.Insert(network, data); err != nil {
			log.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	fh, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if _, err := tree.WriteTo(fh); err != nil {
		log.Fatal(err)
	}
	if err := fh.Close(); err != nil {
		log.Fatal(err)
	}
}
