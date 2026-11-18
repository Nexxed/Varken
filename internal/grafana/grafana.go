// Package grafana creates the Varken InfluxDB datasource and imports the Varken dashboard (grafana.com 9585),
// as utilities/grafana_build.py did.
package grafana

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DashboardID is the Varken dashboard on grafana.com.
const DashboardID = 9585

// Options are the script's settings.
type Options struct {
	GrafanaURL string
	Username   string
	Password   string
	Verify     bool
	// Docker selects http://influxdb:8086 as the datasource URL; otherwise http://<HostIP>:8086.
	Docker   bool
	HostIP   string
	Database string

	MoviesLibrary   string
	Movies4KLibrary string
	TVLibrary       string
	TV4KLibrary     string
	MusicLibrary    string
	USGName         string
	OmbiURL         string
	TautulliURL     string
	SonarrURL       string
	RadarrURL       string
	SickChillURL    string
	LidarrURL       string
	Out             io.Writer
	HTTPClient      *http.Client
}

// Defaults are the values at the top of grafana_build.py.
func Defaults() Options {
	return Options{
		GrafanaURL: "http://grafana:3000", Username: "admin", Password: "admin", Docker: true, HostIP: "127.0.0.1",
		Database: "varken", MoviesLibrary: "Movies", Movies4KLibrary: "Movies 4K", TVLibrary: "TV Shows",
		TV4KLibrary: "TV Shows 4K", MusicLibrary: "Music", USGName: "Gateway",
		OmbiURL: "https://yourdomain.com/ombi", TautulliURL: "https://yourdomain.com/tautulli",
		SonarrURL: "https://yourdomain.com/sonarr", RadarrURL: "https://yourdomain.com/radarr",
		SickChillURL: "https://yourdomain.com/sickchill", LidarrURL: "https://yourdomain.com/lidarr",
	}
}

type client struct {
	base string
	o    Options
	http *http.Client
}

func (c *client) call(ctx context.Context, method, path string, body any, out any) (int, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, reader)
	if err != nil {
		return 0, err
	}
	req.SetBasicAuth(c.o.Username, c.o.Password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return resp.StatusCode, err
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return resp.StatusCode, fmt.Errorf("HTTP %d from %s: not JSON: %.200s", resp.StatusCode, path, data)
		}
	}
	return resp.StatusCode, nil
}

// Build creates the datasource when there is none for the database, then imports the dashboard.
func Build(ctx context.Context, o Options) error {
	httpClient := o.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second, Transport: &http.Transport{
			Proxy:           http.ProxyFromEnvironment,
			TLSClientConfig: &tls.Config{InsecureSkipVerify: !o.Verify}, //nolint:gosec // user setting
		}}
	}
	out := o.Out
	if out == nil {
		out = io.Discard
	}
	c := &client{base: strings.TrimRight(o.GrafanaURL, "/") + "/api", o: o, http: httpClient}

	var datasources []struct {
		Name     string `json:"name"`
		Database string `json:"database"`
		JSONData struct {
			DBName string `json:"dbName"`
		} `json:"jsonData"`
	}
	if _, err := c.call(ctx, http.MethodGet, "/datasources", nil, &datasources); err != nil {
		return fmt.Errorf("could not talk to grafana at %s. Check URL/Username/Password: %w", o.GrafanaURL, err)
	}
	name := "Varken-Script"
	found := false
	for _, ds := range datasources {
		if ds.Database == o.Database || ds.JSONData.DBName == o.Database {
			name, found = ds.Name, true
			fmt.Fprintf(out, "varken datasource already exists with the name %q\n", name)
			break
		}
	}
	if !found {
		host := o.HostIP
		if o.Docker {
			host = "influxdb"
		}
		payload := map[string]any{"name": name, "type": "influxdb", "url": fmt.Sprintf("http://%s:8086", host),
			"access": "proxy", "basicAuth": false, "database": o.Database,
			"jsonData": map[string]any{"dbName": o.Database}}
		var created struct {
			Datasource struct {
				ID int `json:"id"`
			} `json:"datasource"`
			Message string `json:"message"`
		}
		status, err := c.call(ctx, http.MethodPost, "/datasources", payload, &created)
		if err != nil || status != http.StatusOK {
			return fmt.Errorf("could not create the %s datasource (HTTP %d): %v %s", name, status, err, created.Message)
		}
		fmt.Fprintf(out, "Created %s datasource (id:%d)\n", name, created.Datasource.ID)
	}

	var gnet struct {
		JSON json.RawMessage `json:"json"`
	}
	status, err := c.call(ctx, http.MethodGet, fmt.Sprintf("/gnet/dashboards/%d", DashboardID), nil, &gnet)
	if err != nil || status != http.StatusOK || len(gnet.JSON) == 0 {
		return fmt.Errorf("could not download dashboard %d through grafana (HTTP %d): %v", DashboardID, status, err)
	}
	constant := func(name, label, value string) map[string]any {
		return map[string]any{"name": name, "type": "constant", "label": label, "value": value, "description": ""}
	}
	payload := map[string]any{
		"dashboard": gnet.JSON,
		"overwrite": true,
		"inputs": []any{
			map[string]any{"name": "DS_VARKEN", "label": "varken", "description": "", "type": "datasource",
				"pluginId": "influxdb", "pluginName": "InfluxDB", "value": name},
			constant("VAR_MOVIESLIBRARY", "Movies Library Name", o.MoviesLibrary),
			constant("VAR_MOVIES4KLIBRARY", "4K Movies Library Name", o.Movies4KLibrary),
			constant("VAR_TVLIBRARY", "TV Library Name", o.TVLibrary),
			constant("VAR_TV4KLIBRARY", "TV 4K Library Name", o.TV4KLibrary),
			constant("VAR_MUSICLIBRARY", "Music Library Name", o.MusicLibrary),
			constant("VAR_USGNAME", "Unifi USG Name", o.USGName),
			constant("VAR_OMBIURL", "Ombi URL", o.OmbiURL),
			constant("VAR_TAUTULLIURL", "Tautulli URL", o.TautulliURL),
			constant("VAR_SONARRURL", "Sonarr URL", o.SonarrURL),
			constant("VAR_RADARRURL", "Radarr URL", o.RadarrURL),
			constant("VAR_SICKCHILLURL", "Sickchill URL", o.SickChillURL),
			constant("VAR_LIDARRURL", "lidarr URL", o.LidarrURL),
		},
	}
	var imported struct {
		Imported bool   `json:"imported"`
		Title    string `json:"title"`
		Message  string `json:"message"`
	}
	status, err = c.call(ctx, http.MethodPost, "/dashboards/import", payload, &imported)
	if err != nil || status != http.StatusOK || !imported.Imported {
		return fmt.Errorf("dashboard import failed (HTTP %d): %v %s", status, err, imported.Message)
	}
	var title struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(gnet.JSON, &title)
	fmt.Fprintf(out, "Created dashboard %q\n", title.Title)
	return nil
}
