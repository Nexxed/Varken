// Package ombi collects request and issue statistics from Ombi (port of varken/ombi.py).
package ombi

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

// API is one Ombi server.
type API struct {
	srv  config.OmbiServer
	deps collector.Deps
	log  *slog.Logger
	http *httpx.Client
}

// New returns the collector for one [ombi-N] section.
func New(srv config.OmbiServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	return &API{
		srv:  srv,
		deps: deps,
		log:  deps.Log.With("module", "ombi"),
		http: httpx.New(httpx.Options{BaseURL: srv.URL, VerifySSL: srv.VerifySSL, Log: deps.Log,
			Headers: map[string]string{"Apikey": srv.APIKey}, Transport: deps.Transport}),
	}
}

// Jobs are the scheduled jobs of Varken.py for this server.
func (a *API) Jobs() []collector.Job {
	var jobs []collector.Job
	name := func(job string) string { return fmt.Sprintf("ombi-%d-%s", a.srv.ID, job) }
	if a.srv.RequestTypeCounts {
		jobs = append(jobs, collector.Every(name("get_request_counts"), a.srv.RequestTypeRunSeconds, a.GetRequestCounts))
	}
	if a.srv.RequestTotalCounts {
		jobs = append(jobs, collector.Every(name("get_all_requests"), a.srv.RequestTotalRunSeconds, a.GetAllRequests))
	}
	if a.srv.IssueStatusCounts {
		jobs = append(jobs, collector.Every(name("get_issue_counts"), a.srv.IssueStatusRunSeconds, a.GetIssueCounts))
	}
	return jobs
}

func (a *API) serverID() py.Value { return py.Int(int64(a.srv.ID)) }

// countTags are the tags of the count points: no server tag, as before, unless add_server_tag is set.
func (a *API) countTags(kind string) collector.M {
	tags := collector.M{"type": py.String(kind)}
	if a.srv.AddServerTag {
		tags["server"] = a.serverID()
	}
	return tags
}

type requestCounts struct {
	Approved  py.Value `json:"approved"`
	Available py.Value `json:"available"`
	Pending   py.Value `json:"pending"`
}

type issueCounts struct {
	InProgress py.Value `json:"inProgress"`
	Pending    py.Value `json:"pending"`
	Resolved   py.Value `json:"resolved"`
}

// zero is the NamedTuple default (0) of a count field the response leaves out.
var zero = py.Int(0)

// GetRequestCounts is get_request_counts: pending, approved and available request totals.
func (a *API) GetRequestCounts(ctx context.Context) error {
	var counts requestCounts
	if !a.http.GetInto(ctx, "/api/v1/Request/count", nil, &counts) {
		return nil
	}
	fields := collector.M{
		"pending":   counts.Pending.Get(zero),
		"approved":  counts.Approved.Get(zero),
		"available": counts.Available.Get(zero),
	}
	return a.deps.Writer.Write(ctx, []influx.Point{
		collector.Point("Ombi", a.countTags("Request_Counts"), fields, collector.NowTime(a.deps))})
}

// GetIssueCounts is get_issue_counts: pending, in-progress and resolved issue totals.
func (a *API) GetIssueCounts(ctx context.Context) error {
	var counts issueCounts
	if !a.http.GetInto(ctx, "/api/v1/Issues/count", nil, &counts) {
		return nil
	}
	fields := collector.M{
		"pending":     counts.Pending.Get(zero),
		"in_progress": counts.InProgress.Get(zero),
		"resolved":    counts.Resolved.Get(zero),
	}
	return a.deps.Writer.Write(ctx, []influx.Point{
		collector.Point("Ombi", a.countTags("Issues_Counts"), fields, collector.NowTime(a.deps))})
}

type user struct {
	UserAlias py.Value `json:"userAlias"`
}

type movieRequest struct {
	ID            py.Value     `json:"id"`
	TheMovieDbID  py.Value     `json:"theMovieDbId"`
	Title         py.Value     `json:"title"`
	Denied        py.Value     `json:"denied"`
	Approved      py.Value     `json:"approved"`
	Available     py.Value     `json:"available"`
	RequestedDate py.Value     `json:"requestedDate"`
	RequestedUser py.Obj[user] `json:"requestedUser"`
}

type childRequest struct {
	Denied        py.Value     `json:"denied"`
	Approved      py.Value     `json:"approved"`
	Available     py.Value     `json:"available"`
	RequestedDate py.Value     `json:"requestedDate"`
	RequestedUser py.Obj[user] `json:"requestedUser"`
}

type tvRequest struct {
	ID            py.Value                      `json:"id"`
	TvDbID        py.Value                      `json:"tvDbId"`
	Title         py.Value                      `json:"title"`
	ChildRequests py.List[py.Obj[childRequest]] `json:"childRequests"`
}

// status maps Ombi's flags to Varken's request status: Denied 0, Approved 1, Completed 2, Pending 3.
func status(denied, approved, available py.Value) int64 {
	switch {
	case denied.Truthy():
		return 0
	case approved.Truthy() && available.Truthy():
		return 2
	case approved.Truthy():
		return 1
	}
	return 3
}

// fetchList is `connection_handler(...) or []`: the decoded list and its length (Python's len() of whatever
// truthy JSON came back).
func (a *API) fetchList(ctx context.Context, path string) (body []byte, count int64) {
	body, ok := a.http.Get(ctx, path, nil)
	if !ok || !httpx.Truthy(body) {
		return nil, 0
	}
	var whole py.Value
	if err := json.Unmarshal(body, &whole); err == nil {
		if n, ok := whole.Len(); ok {
			count = int64(n)
		}
	}
	return body, count
}

// GetAllRequests is get_all_requests: totals plus one point per movie and TV request.
func (a *API) GetAllRequests(ctx context.Context) error {
	now := collector.NowTime(a.deps)
	tvBody, tvCount := a.fetchList(ctx, "/api/v1/Request/tv")
	movieBody, movieCount := a.fetchList(ctx, "/api/v1/Request/movie")
	if tvBody == nil && movieBody == nil {
		a.log.Error("No json replies. Discarding job")
		return nil
	}
	var shows py.Objs[tvRequest]
	var movies py.Objs[movieRequest]
	_ = json.Unmarshal(tvBody, &shows)
	_ = json.Unmarshal(movieBody, &movies)

	points := []influx.Point{collector.Point("Ombi",
		collector.M{"type": py.String("Request_Total"), "server": a.serverID()},
		collector.M{"total": py.Int(movieCount + tvCount), "movies": py.Int(movieCount), "tv_shows": py.Int(tvCount)},
		now)}

	// Request Type: Movie = 1, TV Show = 0
	for _, m := range movies {
		points = append(points, a.requestPoint(1, status(m.Denied, m.Approved, m.Available), m.Title,
			m.RequestedUser.V().UserAlias, m.RequestedDate,
			py.Hash(m.ID.String()+m.TheMovieDbID.String()+m.Title.String()), now))
	}
	for _, s := range shows {
		if len(s.ChildRequests) == 0 || !s.ChildRequests[0].OK() {
			// Python crashed here (IndexError/TypeError) and lost every point of the run.
			a.log.Warn(fmt.Sprintf("Skipping Ombi TV request %s: it has no child requests", s.Title.String()))
			continue
		}
		c := s.ChildRequests[0].V()
		points = append(points, a.requestPoint(0, status(c.Denied, c.Approved, c.Available), s.Title,
			c.RequestedUser.V().UserAlias, c.RequestedDate,
			py.Hash(s.ID.String()+s.TvDbID.String()+s.Title.String()), now))
	}
	return a.deps.Writer.Write(ctx, points)
}

func (a *API) requestPoint(kind, status int64, title, user, date py.Value, hash string, now influx.Time) influx.Point {
	return collector.Point("Ombi", collector.M{
		"type":           py.String("Requests"),
		"server":         a.serverID(),
		"request_type":   py.Int(kind),
		"status":         py.Int(status),
		"title":          title,
		"requested_user": user,
		"requested_date": date,
	}, collector.M{"hash": py.String(hash)}, now)
}
