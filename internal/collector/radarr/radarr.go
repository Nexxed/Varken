// Package radarr collects queue, missing movies, upcoming releases and statistics from Radarr (API v3), like
// varken/radarr.py.
package radarr

import (
	"context"
	"fmt"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// wantedPageSize is RadarrAPI.WANTED_PAGE_SIZE.
const wantedPageSize = 500

// maxWantedPages stops paging a server that never reports the end (Python looped while it got records).
const maxWantedPages = 1000

// API is one Radarr server.
type API struct {
	*arr.Base
	srv config.RadarrServer
}

// New returns a collector for one [radarr-N] server.
func New(srv config.RadarrServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	deps.Log = deps.Log.With("module", "radarr")
	return &API{srv: srv, Base: &arr.Base{
		Measurement: "Radarr",
		Prefix:      "/api/v3",
		HistoryTypes: []arr.Event{{Field: "grabbed", EventType: 1}, {Field: "imported", EventType: 3},
			{Field: "failed", EventType: 4}, {Field: "deleted", EventType: 6}, {Field: "renamed", EventType: 8},
			{Field: "ignored", EventType: 9}},
		HistoryIncludes: httpx.P("includeMovie", "true"),
		ServerID:        srv.ID,
		Client:          arr.NewClient(srv.URL, srv.APIKey, srv.VerifySSL, nil, deps),
		Deps:            deps,
		Log:             deps.Log,
	}}
}

// Jobs is the Radarr part of Varken.py's scheduling.
func (a *API) Jobs() []collector.Job {
	s := a.srv
	name := func(job string) string { return fmt.Sprintf("radarr-%d-%s", s.ID, job) }
	var jobs []collector.Job
	if s.GetMissing {
		jobs = append(jobs, collector.Every(name("get_missing"), s.GetMissingRunSeconds, a.Missing))
	}
	if s.Queue {
		jobs = append(jobs, collector.Every(name("get_queue"), s.QueueRunSeconds, a.Queue))
	}
	jobs = append(jobs, arr.StatsJobs("radarr", s.ID, s.ArrStats, a.Base)...)
	if s.GetCalendar {
		days := s.CalendarDays
		jobs = append(jobs, collector.Every(name("get_calendar"), s.GetCalendarRunSeconds,
			func(ctx context.Context) error { return a.Calendar(ctx, days) }))
	}
	return jobs
}

type movie struct {
	Title       py.Value `json:"title"`
	Year        py.Value `json:"year"`
	TmdbID      py.Value `json:"tmdbId"`
	TitleSlug   py.Value `json:"titleSlug"`
	Monitored   py.Value `json:"monitored"`
	HasFile     py.Value `json:"hasFile"`
	IsAvailable py.Value `json:"isAvailable"`
}

// Missing is get_missing: monitored movies without a file, from /wanted/missing.
func (a *API) Missing(ctx context.Context) error {
	now := collector.NowTime(a.Deps)
	var records []movie
	for page := 1; page <= maxWantedPages; page++ {
		body, ok := a.Client.Get(ctx, "/api/v3/wanted/missing", httpx.P("page", fmt.Sprint(page),
			"pageSize", fmt.Sprint(wantedPageSize), "monitored", "true"))
		if !ok || !httpx.Truthy(body) || !arr.IsDict(body) {
			return nil
		}
		resp, err := py.Decode[arr.Page[movie]](body)
		if err != nil {
			return nil
		}
		records = append(records, resp.Items...)
		if !resp.RecordsTruthy {
			break
		}
		total, err := py.IntExact(resp.TotalRecords.Or(py.Int(0)))
		if err != nil {
			return nil // Python's int() raised on totalRecords and the run wrote nothing
		}
		if total <= int64(page*wantedPageSize) {
			break
		}
	}

	var payload []influx.Point
	for _, m := range records {
		if !m.Monitored.Truthy() || m.HasFile.Truthy() {
			continue
		}
		available := int64(1)
		if m.IsAvailable.Truthy() {
			available = 0
		}
		name := fmt.Sprintf("%s (%s)", m.Title, m.Year)
		payload = append(payload, collector.Point("Radarr", collector.M{
			"type":              py.String("Missing"),
			"Missing":           py.Bool(true),
			"Missing_Available": py.Int(available),
			"tmdbId":            m.TmdbID,
			"server":            a.Server(),
			"name":              py.String(name),
			"titleSlug":         m.TitleSlug,
		}, collector.M{"hash": py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, name, m.TmdbID)))}, now))
	}
	return collector.Write(ctx, a.Deps, payload, collector.NoData("radarr", "missing"))
}

// Queue is get_queue: every page of the download queue, a summary point and one point per movie.
func (a *API) Queue(ctx context.Context) error {
	now := collector.NowTime(a.Deps)
	records, ok := a.FetchQueuePages(ctx, func(page int) httpx.Params {
		p := httpx.P("pageSize", "250")
		if page > 0 {
			p = append(p, httpx.Param{Key: "page", Value: fmt.Sprint(page)})
		}
		return append(p, httpx.P("includeMovie", "True", "includeUnknownMovieItems", "False")...)
	})
	if !ok {
		return nil
	}
	payload := []influx.Point{a.QueueSummary(ctx, records, now)}
	for _, item := range records {
		// Python skipped an item whose movie was not an object, or whose protocol or timeleft was not text
		// (TypeError/AttributeError).
		_, protocolText := item.Protocol.AsString()
		_, timeleftText := item.Timeleft.AsString()
		if item.Movie.Invalid() || (item.Protocol.Truthy() && !protocolText) ||
			(item.Timeleft.Truthy() && !timeleftText) {
			a.Log.Warn(fmt.Sprintf("Skipping Radarr queue item %s: unexpected movie, protocol or timeleft", item.ID))
			continue
		}
		m := item.Movie.V
		name := fmt.Sprintf("%s (%s)", m.Title, m.Year)
		quality := arr.QualityName(item.Quality)
		protocol := py.Upper(item.Protocol.OrString("").String())
		protocolID := int64(0)
		if protocol == "USENET" {
			protocolID = 1
		}
		tmdbID := m.TmdbID
		if tmdbID.IsNone() {
			tmdbID = item.ID
		}
		fields := arr.QueueItemFields(item)
		fields["hash"] = py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, name, quality)))
		payload = append(payload, collector.Point("Radarr", collector.M{
			"type":        py.String("Queue"),
			"tmdbId":      tmdbID,
			"server":      a.Server(),
			"name":        py.String(name),
			"quality":     quality,
			"protocol":    py.String(protocol),
			"protocol_id": py.Int(protocolID),
			"titleSlug":   m.TitleSlug,
		}, fields, now))
	}
	return collector.Write(ctx, a.Deps, payload, collector.NoData("radarr", "queue"))
}

type release struct {
	date time.Time
	kind string
}

// Calendar is get_calendar: one point per movie, for the earliest of its cinema, digital or physical release
// dates that falls between today and today + days.
func (a *API) Calendar(ctx context.Context, days int) error {
	nowTime := a.Deps.Now()
	start := collector.Date(nowTime)
	end := start.AddDate(0, 0, days)
	now := influx.At(nowTime)
	body, ok := a.Get(ctx, "/calendar", httpx.P("start", collector.DateString(start), "end",
		collector.DateString(end)))
	if !ok || !arr.IsList(body) {
		return nil
	}
	movies, err := py.Decode[py.Objs[struct {
		Title           py.Value `json:"title"`
		Year            py.Value `json:"year"`
		TmdbID          py.Value `json:"tmdbId"`
		HasFile         py.Value `json:"hasFile"`
		InCinemas       py.Value `json:"inCinemas"`
		DigitalRelease  py.Value `json:"digitalRelease"`
		PhysicalRelease py.Value `json:"physicalRelease"`
	}]](body)
	if err != nil {
		return nil
	}
	startDate, endDate := dateOnly(start), dateOnly(end)
	var payload []influx.Point
	for _, m := range movies {
		var best *release
		for _, r := range []struct {
			value py.Value
			kind  string
		}{{m.InCinemas, "cinema"}, {m.DigitalRelease, "digital"}, {m.PhysicalRelease, "physical"}} {
			if !r.value.Truthy() {
				continue
			}
			d, ok := arr.DateFromISOFormat(py.Truncate(r.value.String(), 10))
			if !ok || d.Before(startDate) || d.After(endDate) {
				continue
			}
			if best == nil || d.Before(best.date) || (d.Equal(best.date) && r.kind < best.kind) {
				best = &release{d, r.kind}
			}
		}
		if best == nil {
			continue
		}
		tmdbID, err := py.IntExact(m.TmdbID.Or(py.Int(0)))
		if err != nil {
			// Python's int() raised here and lost the whole run; this movie is skipped instead.
			a.Log.Warn(fmt.Sprintf("Skipping Radarr calendar movie %s: tmdbId %s is not a number", m.Title, m.TmdbID))
			continue
		}
		title := m.Title.OrString("Unknown")
		name := title.String()
		if m.Year.Truthy() {
			name = fmt.Sprintf("%s (%s)", title, m.Year)
		}
		downloaded := "0"
		if m.HasFile.Truthy() {
			downloaded = "1"
		}
		payload = append(payload, a.Point("Future", collector.M{
			"hash":    py.String(py.Hash(fmt.Sprintf("%d%d%s", a.srv.ID, tmdbID, best.kind))),
			"tmdb_id": py.Int(tmdbID),
		}, now, collector.M{
			"name":         py.String(name),
			"release_type": py.String(best.kind),
			"release_date": py.String(best.date.Format("2006-01-02")),
			"downloaded":   py.String(downloaded),
		}))
	}
	return a.Write(ctx, payload, "calendar")
}

func dateOnly(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}
