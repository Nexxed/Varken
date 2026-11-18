// Package lidarr collects queue, calendar and statistics from Lidarr (API v1), like varken/lidarr.py.
package lidarr

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/collector/arr"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// API is one Lidarr server.
type API struct {
	*arr.Base
	srv config.LidarrServer
}

// New returns a collector for one [lidarr-N] server.
func New(srv config.LidarrServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	deps.Log = deps.Log.With("module", "lidarr")
	return &API{srv: srv, Base: &arr.Base{
		Measurement: "Lidarr",
		Prefix:      "/api/v1",
		HistoryTypes: []arr.Event{{Field: "grabbed", EventType: 1}, {Field: "track_imported", EventType: 3},
			{Field: "failed", EventType: 4}, {Field: "deleted", EventType: 5}, {Field: "renamed", EventType: 6},
			{Field: "import_incomplete", EventType: 7}, {Field: "imported", EventType: 8}},
		HistoryIncludes: httpx.P("includeArtist", "true", "includeAlbum", "true"),
		ServerID:        srv.ID,
		Client:          arr.NewClient(srv.URL, srv.APIKey, srv.VerifySSL, nil, deps),
		Deps:            deps,
		Log:             deps.Log,
	}}
}

// Jobs is the Lidarr part of Varken.py's scheduling.
func (a *API) Jobs() []collector.Job {
	s := a.srv
	name := func(job string) string { return fmt.Sprintf("lidarr-%d-%s", s.ID, job) }
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
	return append(jobs, arr.StatsJobs("lidarr", s.ID, s.ArrStats, a.Base)...)
}

type statistics struct {
	PercentOfTracks py.Value `json:"percentOfTracks"`
	TrackFileCount  py.Value `json:"trackFileCount"`
	TrackCount      py.Value `json:"trackCount"`
}

type album struct {
	ID          py.Value `json:"id"`
	Title       py.Value `json:"title"`
	ReleaseDate py.Value `json:"releaseDate"`
	Artist      py.Obj[struct {
		ArtistName py.Value `json:"artistName"`
	}] `json:"artist"`
	Statistics py.Obj[statistics] `json:"statistics"`
}

// is100 is Python's `value == 100` (ints and floats compare by value, other types never equal).
func is100(v py.Value) bool {
	if i, ok := v.AsInt(); ok {
		return i == 100
	}
	if f, ok := v.AsFloat(); ok {
		return f == 100
	}
	return false
}

// Calendar is get_calendar: albums released in the last missing_days that are not complete ("Missing"), or
// every album releasing in the next future_days ("Future").
func (a *API) Calendar(ctx context.Context, query string) error {
	nowTime := a.Deps.Now()
	today := collector.Date(nowTime)
	now := influx.At(nowTime)
	params := httpx.P("start", collector.DateString(today), "end",
		collector.DateString(today.AddDate(0, 0, a.srv.FutureDays)))
	if query == "Missing" {
		params = httpx.P("start", collector.DateString(today.AddDate(0, 0, -a.srv.MissingDays)), "end",
			collector.DateString(today))
	}
	body, ok := a.Client.Get(ctx, "/api/v1/calendar", params)
	if !ok || !httpx.Truthy(body) {
		return nil
	}
	albums, _ := py.Decode[py.Objs[album]](body)
	var payload []influx.Point
	for _, al := range albums {
		// A missing statistics object reads as {}. Python raised on a null one and lost the whole run.
		stats := al.Statistics.V()
		percent := stats.PercentOfTracks.Get(py.Int(0))
		if query == "Missing" && is100(percent) {
			continue
		}
		if !al.Artist.OK() || !al.Artist.V().ArtistName.Exists() {
			// Python raised here (no artist, or no artistName) and lost the whole run.
			a.Log.Warn(fmt.Sprintf("Skipping Lidarr album %s without an artist name", al.Title))
			continue
		}
		complete := fmt.Sprintf("%s/%s", stats.TrackFileCount.Get(py.Int(0)), stats.TrackCount.Get(py.Int(0)))
		payload = append(payload, collector.Point("Lidarr", collector.M{
			"type":            py.String(query),
			"albumId":         al.ID,
			"server":          a.Server(),
			"albumName":       al.Title,
			"artistName":      al.Artist.V().ArtistName,
			"percentComplete": percent,
			"completeCount":   py.String(complete),
			"releaseDate":     al.ReleaseDate,
		}, collector.M{"hash": py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, al.Title, al.ID)))}, now))
	}
	if len(payload) == 0 {
		return nil
	}
	return a.Deps.Writer.Write(ctx, payload)
}

// Queue is get_queue: the download queue (one page of up to 1000), a summary point and one point per release.
func (a *API) Queue(ctx context.Context) error {
	now := collector.NowTime(a.Deps)
	body, ok := a.Client.Get(ctx, "/api/v1/queue", httpx.P("pageSize", "1000"))
	if !ok || !httpx.Truthy(body) || !arr.IsDict(body) {
		return nil
	}
	page, err := py.Decode[arr.Page[arr.QueueItem]](body)
	if err != nil {
		return nil
	}
	records := page.Items
	payload := []influx.Point{a.QueueSummary(ctx, records, now)}
	for _, item := range records {
		// Lidarr 3 reports "UsenetDownloadProtocol"/"TorrentDownloadProtocol" rather than "usenet"/"torrent".
		protocol, protocolID := "TORRENT", int64(0)
		if strings.Contains(py.Lower(item.Protocol.OrString("").String()), "usenet") {
			protocol, protocolID = "USENET", 1
		}
		fields := arr.QueueItemFields(item)
		fields["hash"] = py.String(py.Hash(fmt.Sprintf("%d%s%s", a.srv.ID, item.Title, item.ArtistID)))
		payload = append(payload, collector.Point("Lidarr", collector.M{
			"type":        py.String("Queue"),
			"id":          item.ID,
			"server":      a.Server(),
			"title":       item.Title,
			"quality":     arr.QualityName(item.Quality),
			"protocol":    py.String(protocol),
			"protocol_id": py.Int(protocolID),
			"indexer":     item.Indexer,
		}, fields, now))
	}
	return a.Deps.Writer.Write(ctx, payload)
}
