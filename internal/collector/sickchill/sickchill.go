// Package sickchill collects missing and upcoming episodes from SickChill (port of varken/sickchill.py).
package sickchill

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// API is one SickChill server.
type API struct {
	srv  config.SickChillServer
	deps collector.Deps
	log  *slog.Logger
	http *httpx.Client
}

// New returns the collector for one [sickchill-N] section.
func New(srv config.SickChillServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	return &API{
		srv:  srv,
		deps: deps,
		log:  deps.Log.With("module", "sickchill"),
		http: httpx.New(httpx.Options{BaseURL: srv.URL, VerifySSL: srv.VerifySSL, Log: deps.Log,
			SessionParams: httpx.P("limit", "1000"), Transport: deps.Transport}),
	}
}

// Jobs are the scheduled jobs of Varken.py for this server.
func (a *API) Jobs() []collector.Job {
	if !a.srv.GetMissing {
		return nil
	}
	return []collector.Job{collector.Every(fmt.Sprintf("sickchill-%d-get_missing", a.srv.ID),
		a.srv.GetMissingRunSeconds, a.GetMissing)}
}

type show struct {
	Airdate   py.Value `json:"airdate"`
	Episode   py.Value `json:"episode"`
	EpName    py.Value `json:"ep_name"`
	IndexerID py.Value `json:"indexerid"`
	Season    py.Value `json:"season"`
	ShowName  py.Value `json:"show_name"`
}

type future struct {
	Data py.Obj[py.Map[py.Objs[show]]] `json:"data"`
}

// missingTypes is the type tag per section; any other section (snatched, unknown ones) is logged and skipped.
var missingTypes = map[string]int64{"future": 0, "later": 1, "soon": 2, "today": 3, "missed": 4}

// GetMissing is get_missing: the episodes of cmd=future, one point each, typed by section.
func (a *API) GetMissing(ctx context.Context) error {
	params := httpx.P("cmd", "future", "paused", "1", "type", "missed|today|soon|later|snatched")
	body, ok := a.http.Get(ctx, "/api/"+a.srv.APIKey, params)
	if !ok || !httpx.Truthy(body) {
		return nil
	}
	var f future
	if err := json.Unmarshal(body, &f); err != nil || !f.Data.OK() {
		a.log.Error("SickChill returned no data: check the API key")
		return nil
	}
	now := collector.NowTime(a.deps)
	sections := f.Data.V()
	var points []influx.Point
	for i, key := range sections.Keys {
		for _, s := range sections.Values[i] {
			season, err1 := py.FormatPad(s.Season, 2)
			episode, err2 := py.FormatPad(s.Episode, 2)
			if err1 != nil || err2 != nil {
				// Python crashed here with TypeError and lost the whole run.
				a.log.Warn(fmt.Sprintf("Skipping SickChill episode of %s without a season or episode number",
					s.ShowName.String()))
				continue
			}
			sxe := "S" + season + "E" + episode
			kind, known := missingTypes[key]
			if !known {
				a.log.Error("Error building payload for sickchill. Discarding. Error: list index out of range")
				continue
			}
			points = append(points, collector.Point("SickChill", collector.M{
				"type":      py.Int(kind),
				"indexerid": s.IndexerID,
				"server":    py.Int(int64(a.srv.ID)),
				"name":      s.ShowName,
				"epname":    s.EpName,
				"sxe":       py.String(sxe),
				"airdate":   s.Airdate,
			}, collector.M{"hash": py.String(py.Hash(fmt.Sprint(a.srv.ID) + s.ShowName.String() + sxe))}, now))
		}
	}
	if len(points) == 0 {
		return nil
	}
	return a.deps.Writer.Write(ctx, points)
}
