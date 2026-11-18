// Package overseerr collects request statistics and the latest requests from Overseerr (port of
// varken/overseerr.py).
package overseerr

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

const (
	// lookupWorkers bounds the parallel movie/TV lookups of one run.
	lookupWorkers = 4
	// maxCachedTitles bounds the title cache; it is cleared when full.
	maxCachedTitles = 2000
)

// API is one Overseerr server.
type API struct {
	srv  config.OverseerrServer
	deps collector.Deps
	log  *slog.Logger
	http *httpx.Client

	mu     sync.Mutex
	titles map[string]media // "movie/603" -> looked-up id and title
}

// media is what a movie/TV lookup contributes to a request point.
type media struct {
	id     py.Value
	title  py.Value
	status py.Value // mediaInfo.status, used only when the request itself has none
}

// New returns the collector for one [overseerr-N] section.
func New(srv config.OverseerrServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	return &API{
		srv:    srv,
		deps:   deps,
		log:    deps.Log.With("module", "overseerr"),
		titles: map[string]media{},
		http: httpx.New(httpx.Options{BaseURL: srv.URL, VerifySSL: srv.VerifySSL, Log: deps.Log,
			Headers: map[string]string{"X-Api-Key": srv.APIKey}, Transport: deps.Transport}),
	}
}

// Jobs are the scheduled jobs of Varken.py for this server.
func (a *API) Jobs() []collector.Job {
	var jobs []collector.Job
	name := func(job string) string { return fmt.Sprintf("overseerr-%d-%s", a.srv.ID, job) }
	if a.srv.GetRequestTotalCounts {
		jobs = append(jobs, collector.Every(name("get_request_counts"), a.srv.RequestTotalRunSeconds,
			a.GetRequestCounts))
	}
	if a.srv.NumLatestRequestsToFetch > 0 {
		jobs = append(jobs, collector.Every(name("get_latest_requests"), a.srv.NumLatestRequestsSeconds,
			a.GetLatestRequests))
	}
	return jobs
}

func (a *API) serverID() py.Value { return py.Int(int64(a.srv.ID)) }

type requestCounts struct {
	Pending    py.Value `json:"pending"`
	Approved   py.Value `json:"approved"`
	Processing py.Value `json:"processing"`
	Available  py.Value `json:"available"`
	Total      py.Value `json:"total"`
	Movie      py.Value `json:"movie"`
	TV         py.Value `json:"tv"`
	Declined   py.Value `json:"declined"`
}

// GetRequestCounts is get_request_counts.
func (a *API) GetRequestCounts(ctx context.Context) error {
	var c requestCounts
	if !a.http.GetInto(ctx, "/api/v1/request/count", nil, &c) {
		return nil
	}
	tags := collector.M{"type": py.String("Request_Counts")}
	if a.srv.AddServerTag {
		tags["server"] = a.serverID()
	}
	fields := collector.M{"pending": c.Pending, "approved": c.Approved, "processing": c.Processing,
		"available": c.Available, "total": c.Total, "movies": c.Movie, "tv": c.TV, "declined": c.Declined}
	return a.deps.Writer.Write(ctx, []influx.Point{collector.Point("Overseerr", tags, fields, collector.NowTime(a.deps))})
}

type user struct {
	DisplayName py.Value `json:"displayName"`
}

type requestMedia struct {
	TmdbID py.Value `json:"tmdbId"`
	Status py.Value `json:"status"`
}

type request struct {
	Type        py.Value             `json:"type"`
	CreatedAt   py.Value             `json:"createdAt"`
	Media       py.Obj[requestMedia] `json:"media"`
	RequestedBy py.Obj[user]         `json:"requestedBy"`
}

type latest struct {
	Results py.Objs[request] `json:"results"`
}

type detail struct {
	ID        py.Value `json:"id"`
	Title     py.Value `json:"title"`
	Name      py.Value `json:"name"`
	MediaInfo py.Obj[struct {
		Status py.Value `json:"status"`
	}] `json:"mediaInfo"`
}

// lookup returns the id and title of a movie or TV show, from the cache or /api/v1/{movie,tv}/{tmdbId}.
func (a *API) lookup(ctx context.Context, kind, tmdb string) (media, bool) {
	key := kind + "/" + tmdb
	a.mu.Lock()
	m, ok := a.titles[key]
	a.mu.Unlock()
	if ok {
		return m, true
	}
	var d detail
	if !a.http.GetInto(ctx, "/api/v1/"+kind+"/"+tmdb, nil, &d) {
		return media{}, false
	}
	m = media{id: d.ID, title: d.Title, status: d.MediaInfo.V().Status}
	if kind == "tv" {
		m.title = d.Name
	}
	a.mu.Lock()
	if len(a.titles) >= maxCachedTitles {
		a.titles = map[string]media{}
	}
	a.titles[key] = m
	a.mu.Unlock()
	return m, true
}

// GetLatestRequests is get_latest_requests: one point per recent movie or TV request.
//
// Unlike the Python version, each point carries the request's own requester and date (Python took the media's
// first request), and the title lookup is cached instead of repeated every run.
func (a *API) GetLatestRequests(ctx context.Context) error {
	warning := collector.NoData("overseerr", "latest-requests")
	path := "/api/v1/request?take=" + strconv.Itoa(a.srv.NumLatestRequestsToFetch) + "&filter=all&sort=added"
	var l latest
	if !a.http.GetInto(ctx, path, nil, &l) {
		a.log.Warn(warning)
		return nil
	}
	now := collector.NowTime(a.deps)

	type item struct {
		req     request
		key     string
		reqType int64
	}
	type found struct {
		media media
		ok    bool
	}
	var items []item
	lookups := map[string]*found{}
	var order []string
	for _, r := range l.Results {
		kind, _ := r.Type.AsString()
		var reqType int64
		switch kind {
		case "tv":
			reqType = 0
		case "movie":
			reqType = 1
		default:
			continue
		}
		if !r.Media.OK() || r.Media.V().TmdbID.IsNone() {
			a.log.Warn(fmt.Sprintf("Skipping an Overseerr %s request without media", kind))
			continue
		}
		key := kind + "/" + r.Media.V().TmdbID.String()
		if _, seen := lookups[key]; !seen {
			lookups[key] = &found{}
			order = append(order, key)
		}
		items = append(items, item{req: r, key: key, reqType: reqType})
	}

	// Each media is looked up once per run (and then cached), a few at a time.
	sem := make(chan struct{}, lookupWorkers)
	var wg sync.WaitGroup
	for _, key := range order {
		wg.Add(1)
		sem <- struct{}{}
		go func(key string) {
			defer wg.Done()
			defer func() { <-sem }()
			kind, tmdb, _ := strings.Cut(key, "/")
			f := lookups[key]
			f.media, f.ok = a.lookup(ctx, kind, tmdb)
		}(key)
	}
	wg.Wait()

	var points []influx.Point
	for _, j := range items {
		f := lookups[j.key]
		if !f.ok {
			a.log.Warn(fmt.Sprintf("Skipping the Overseerr request for %s: the lookup failed", j.key))
			continue
		}
		status := j.req.Media.V().Status
		if status.IsNone() {
			status = f.media.status
		}
		points = append(points, collector.Point("Overseerr", collector.M{
			"type":           py.String("Requests"),
			"server":         a.serverID(),
			"request_type":   py.Int(j.reqType),
			"status":         status,
			"title":          f.media.title,
			"requested_user": j.req.RequestedBy.V().DisplayName,
			"requested_date": j.req.CreatedAt,
		}, collector.M{"hash": py.String(py.Hash(f.media.id.String() + f.media.title.String()))}, now))
	}
	return collector.Write(ctx, a.deps, points, warning)
}
