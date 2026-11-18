// Package sonarr collects queue, calendar and statistics from Sonarr (API v3), like varken/sonarr.py.
package sonarr

import (
	"context"
	"fmt"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// API is one Sonarr server.
type API struct {
	*arr.Base
	srv config.SonarrServer
}

// New returns a collector for one [sonarr-N] server.
func New(srv config.SonarrServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	deps.Log = deps.Log.With("module", "sonarr")
	// Python set pageSize=1000 on the session, so it goes with every request that does not set its own.
	client := arr.NewClient(srv.URL, srv.APIKey, srv.VerifySSL, httpx.P("pageSize", "1000"), deps)
	return &API{srv: srv, Base: &arr.Base{
		Measurement: "Sonarr",
		Prefix:      "/api/v3",
		HistoryTypes: []arr.Event{{Field: "grabbed", EventType: 1}, {Field: "imported", EventType: 3},
			{Field: "failed", EventType: 4}, {Field: "deleted", EventType: 5}},
		HistoryIncludes: httpx.P("includeSeries", "true", "includeEpisode", "true"),
		ServerID:        srv.ID,
		Client:          client,
		Deps:            deps,
		Log:             deps.Log,
	}}
}

// Jobs is the Sonarr part of Varken.py's scheduling.
func (a *API) Jobs() []collector.Job {
	s := a.srv
	name := func(job string) string { return fmt.Sprintf("sonarr-%d-%s", s.ID, job) }
	var jobs []collector.Job
	if s.Queue {
		jobs = append(jobs, collector.Every(name("get_queue"), s.QueueRunSeconds, a.Queue))
	}
	if s.MissingDays > 0 {
		jobs = append(jobs, collector.Every(name("get_missing"), s.MissingDaysRunSeconds,
			func(ctx context.Context) error { return a.Calendar(ctx, "Missing") }))
	}
	if s.FutureDays > 0 {
		jobs = append(jobs, collector.Every(name("get_future"), s.FutureDaysRunSeconds,
			func(ctx context.Context) error { return a.Calendar(ctx, "Future") }))
	}
	return append(jobs, arr.StatsJobs("sonarr", s.ID, s.ArrStats, a.Base)...)
}

type episode struct {
	SeasonNumber  py.Value `json:"seasonNumber"`
	EpisodeNumber py.Value `json:"episodeNumber"`
	Title         py.Value `json:"title"`
	AirDateUtc    py.Value `json:"airDateUtc"`
	SeriesID      py.Value `json:"seriesId"`
	HasFile       py.Value `json:"hasFile"`
	Monitored     py.Value `json:"monitored"`
	Series        py.Obj[struct {
		Title py.Value `json:"title"`
	}] `json:"series"`
}

// sxe is f'S{season:0>2}E{episode:0>2}'.
func sxe(season, number py.Value) (string, error) {
	s, err := py.FormatPad(season, 2)
	if err != nil {
		return "", err
	}
	e, err := py.FormatPad(number, 2)
	if err != nil {
		return "", err
	}
	return "S" + s + "E" + e, nil
}

// Calendar is get_calendar: episodes that aired in the last missing_days and are monitored but not downloaded
// ("Missing"), or every episode airing in the next future_days ("Future").
func (a *API) Calendar(ctx context.Context, query string) error {
	nowTime := a.Deps.Now()
	today := collector.Date(nowTime)
	now := influx.At(nowTime)
	params := httpx.P("start", collector.DateString(today), "end",
		collector.DateString(today.AddDate(0, 0, a.srv.FutureDays)), "includeSeries", "True")
	if query == "Missing" {
		params = httpx.P("start", collector.DateString(today.AddDate(0, 0, -a.srv.MissingDays)), "end",
			collector.DateString(today), "includeSeries", "True")
	}
	body, ok := a.Client.Get(ctx, "/api/v3/calendar/", params)
	if !ok || !httpx.Truthy(body) {
		return nil
	}
	// A non-list body gives no episodes (Python iterated it and skipped what was not an episode).
	episodes, _ := py.Decode[py.Objs[episode]](body)
	var payload []influx.Point
	for _, ep := range episodes {
		code, err := sxe(ep.SeasonNumber, ep.EpisodeNumber)
		if err != nil || !ep.Series.OK() || !ep.Series.V().Title.Exists() {
			// Python raised here (no season/episode number, or no series title) and lost the whole run.
			a.Log.Warn(fmt.Sprintf("Skipping Sonarr episode %s without series or episode numbers", ep.Title))
			continue
		}
		downloaded := int64(0)
		if ep.HasFile.Truthy() {
			downloaded = 1
		}
		if query == "Missing" && (!ep.Monitored.Truthy() || downloaded == 1) {
			continue
		}
		title := ep.Series.V().Title
		payload = append(payload, collector.Point("Sonarr", collector.M{
			"type":       py.String(query),
			"sonarrId":   ep.SeriesID,
			"server":     a.Server(),
			"name":       title,
			"epname":     ep.Title,
			"sxe":        py.String(code),
			"airsUTC":    ep.AirDateUtc,
			"downloaded": py.Int(downloaded),
		}, collector.M{"hash": py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, title, code)))}, now))
	}
	return collector.Write(ctx, a.Deps, payload, collector.NoData("sonarr", "calendar"))
}

// Queue is get_queue: every page of the download queue, a summary point and one point per episode.
func (a *API) Queue(ctx context.Context) error {
	now := collector.NowTime(a.Deps)
	records, ok := a.FetchQueuePages(ctx, func(page int) httpx.Params {
		p := httpx.P("pageSize", "250")
		if page > 0 {
			p = append(p, httpx.Param{Key: "page", Value: fmt.Sprint(page)})
		}
		return append(p, httpx.P("includeSeries", "True", "includeEpisode", "True",
			"includeUnknownSeriesItems", "False")...)
	})
	if !ok {
		return nil
	}
	payload := []influx.Point{a.QueueSummary(ctx, records, now)}
	for _, item := range records {
		if item.Series.Invalid() || item.Episode.Invalid() {
			a.Log.Error(fmt.Sprintf("Skipping invalid Sonarr queue item %s", item.Title))
			continue
		}
		ep := item.Episode.V
		code, err := sxe(ep.SeasonNumber, ep.EpisodeNumber)
		if err != nil {
			a.Log.Error(fmt.Sprintf("TypeError has occurred : %v while processing the sonarr queue. Remove "+
				"invalid queue entry. Data attempted is: %s", err, item.Title))
			continue
		}
		protocol := py.Upper(item.Protocol.OrString("").String())
		protocolID := int64(0)
		if protocol == "USENET" {
			protocolID = 1
		}
		series := item.Series.V.Title
		fields := arr.QueueItemFields(item)
		fields["hash"] = py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, series, code)))
		payload = append(payload, collector.Point("Sonarr", collector.M{
			"type":        py.String("Queue"),
			"sonarrId":    item.SeriesID,
			"server":      a.Server(),
			"name":        series,
			"epname":      ep.Title,
			"sxe":         py.String(code),
			"protocol":    py.String(protocol),
			"protocol_id": py.Int(protocolID),
			"quality":     arr.QualityName(item.Quality),
		}, fields, now))
	}
	return collector.Write(ctx, a.Deps, payload, collector.NoData("sonarr", "queue"))
}
