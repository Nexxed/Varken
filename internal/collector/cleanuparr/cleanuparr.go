// Package cleanuparr collects statistics from Cleanuparr (port of varken/cleanuparr.py).
//
// Only read-only GET endpoints are used, authenticated with the X-Api-Key header. Everything is written to the
// "Cleanuparr" measurement, told apart by the "type" tag:
//
//   - Summary, Strikes_By_Type, Removals_By_Reason, Cleaned_By_Reason, Events_By_Type, Events_By_Severity,
//     Searches_By_Reason, Job, Health: snapshots of /api/v2/stats for the last stats_hours hours.
//   - Timeline: hourly counts from /api/v2/stats/timeline, written at each bucket's own timestamp, so re-polling
//     overwrites the buckets with their latest values.
//   - Strike_Item: the downloads Cleanuparr currently tracks strikes for (/api/strikes).
//   - Event: recent events (/api/events), written at the event's own timestamp.
package cleanuparr

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// TimelineMetrics are fetched, in this order, by GetTimeline.
var TimelineMetrics = []string{"strikesIssued", "recovered", "removed", "malwareBlocked", "events"}

const maxTextLength = 500

// API is one Cleanuparr server.
type API struct {
	srv  config.CleanuparrServer
	deps collector.Deps
	log  *slog.Logger
	http *httpx.Client
}

// New returns the collector for one [cleanuparr-N] section.
func New(srv config.CleanuparrServer, deps collector.Deps) *API {
	deps = deps.WithDefaults()
	return &API{
		srv:  srv,
		deps: deps,
		log:  deps.Log.With("module", "cleanuparr"),
		http: httpx.New(httpx.Options{BaseURL: srv.URL, VerifySSL: srv.VerifySSL, Log: deps.Log,
			Headers: map[string]string{"X-Api-Key": srv.APIKey}, Transport: deps.Transport}),
	}
}

// Jobs are the scheduled jobs of Varken.py for this server.
func (a *API) Jobs() []collector.Job {
	var jobs []collector.Job
	name := func(job string) string { return fmt.Sprintf("cleanuparr-%d-%s", a.srv.ID, job) }
	if a.srv.GetStats {
		jobs = append(jobs, collector.Every(name("get_stats"), a.srv.GetStatsRunSeconds, a.GetStats))
	}
	if a.srv.GetTimeline {
		jobs = append(jobs, collector.Every(name("get_timeline"), a.srv.GetTimelineRunSeconds, a.GetTimeline))
	}
	if a.srv.GetStrikes {
		jobs = append(jobs, collector.Every(name("get_strikes"), a.srv.GetStrikesRunSeconds, a.GetStrikes))
	}
	if a.srv.GetEvents {
		jobs = append(jobs, collector.Every(name("get_events"), a.srv.GetEventsRunSeconds, a.GetEvents))
	}
	return jobs
}

func (a *API) dryRunParam() string {
	if a.srv.IncludeDryRun {
		return "true"
	}
	return "false"
}

func (a *API) point(kind string, fields collector.M, at influx.Time, tags collector.M) influx.Point {
	all := collector.M{"type": py.String(kind), "server": py.Int(int64(a.srv.ID))}
	for k, v := range tags {
		all[k] = v
	}
	return collector.Point("Cleanuparr", all, fields, at)
}

func (a *API) write(ctx context.Context, points []influx.Point, job string) error {
	return collector.Write(ctx, a.deps, points, collector.NoData("cleanuparr", job))
}

// count is Python's int() of a count. Python crashed on null (and on non-numeric strings), losing the whole run;
// those count as 0.
func count(v py.Value) py.Value {
	n, err := py.IntExact(v.Get(py.Int(0)))
	if err != nil {
		return py.Int(0)
	}
	return py.Int(n)
}

// truncate is _truncate: `text or ”`, shortened to 500 code points with an ellipsis.
func truncate(v py.Value) py.Value {
	if !v.Truthy() {
		return py.String("")
	}
	s := v.String()
	if py.RuneLen(s) > maxTextLength {
		return py.String(py.Truncate(s, maxTextLength-1) + "…")
	}
	return py.String(s)
}

// flag is int(bool(value)).
func flag(v py.Value) py.Value {
	if v.Truthy() {
		return py.Int(1)
	}
	return py.Int(0)
}

// timeOf is how the InfluxDB client read a timestamp taken from the API: a string is parsed, an int is epoch
// nanoseconds. Other types made Python's whole write fail; such points are skipped.
func timeOf(v py.Value) (influx.Time, bool) {
	if s, ok := v.AsString(); ok {
		return influx.AtString(s), true
	}
	if n, ok := v.AsInt(); ok {
		return influx.AtNS(n), true
	}
	if b, ok := v.AsBool(); ok {
		if b {
			return influx.AtNS(1), true
		}
		return influx.AtNS(0), true
	}
	return influx.Time{}, false
}

type section struct {
	Total      py.Value         `json:"total"`
	Recovered  py.Value         `json:"recovered"`
	Completed  py.Value         `json:"completed"`
	Failed     py.Value         `json:"failed"`
	Grabbed    py.Value         `json:"grabbed"`
	ByType     py.Map[py.Value] `json:"byType"`
	BySeverity py.Map[py.Value] `json:"bySeverity"`
	ByReason   py.Map[py.Value] `json:"byReason"`
}

type jobStat struct {
	Total     py.Value `json:"total"`
	Completed py.Value `json:"completed"`
	Failed    py.Value `json:"failed"`
	LastRunAt py.Value `json:"lastRunAt"`
	NextRunAt py.Value `json:"nextRunAt"`
}

type jobs struct {
	Total     py.Value                `json:"total"`
	Completed py.Value                `json:"completed"`
	Failed    py.Value                `json:"failed"`
	ByType    py.Map[py.Obj[jobStat]] `json:"byType"`
}

type healthItem struct {
	Name           py.Value `json:"name"`
	Type           py.Value `json:"type"`
	IsHealthy      py.Value `json:"isHealthy"`
	ErrorMessage   py.Value `json:"errorMessage"`
	LastChecked    py.Value `json:"lastChecked"`
	ResponseTimeMs py.Value `json:"responseTimeMs"`
}

type health struct {
	DownloadClients py.Objs[healthItem] `json:"downloadClients"`
	ArrInstances    py.Objs[healthItem] `json:"arrInstances"`
}

type stats struct {
	TimeframeHours py.Value        `json:"timeframeHours"`
	Events         py.Obj[section] `json:"events"`
	Strikes        py.Obj[section] `json:"strikes"`
	Removals       py.Obj[section] `json:"removals"`
	Cleaned        py.Obj[section] `json:"cleaned"`
	Searches       py.Obj[section] `json:"searches"`
	Jobs           py.Obj[jobs]    `json:"jobs"`
	Health         py.Obj[health]  `json:"health"`
}

// GetStats is get_stats: the /api/v2/stats snapshot for the last stats_hours hours.
func (a *API) GetStats(ctx context.Context) error {
	now := collector.NowTime(a.deps)
	params := httpx.P("hours", strconv.Itoa(a.srv.StatsHours), "includeDryRun", a.dryRunParam())
	var s stats
	if !a.http.GetInto(ctx, "/api/v2/stats", params, &s) {
		return nil
	}
	hours := s.TimeframeHours.Get(py.Int(int64(a.srv.StatsHours)))
	events, strikes, removals, cleaned := s.Events.V(), s.Strikes.V(), s.Removals.V(), s.Cleaned.V()
	searches, jobStats, hlth := s.Searches.V(), s.Jobs.V(), s.Health.V()
	timeframe := collector.M{"timeframe_hours": hours}

	points := []influx.Point{a.point("Summary", collector.M{
		"events":             count(events.Total),
		"strikes":            count(strikes.Total),
		"strikes_recovered":  count(strikes.Recovered),
		"removals":           count(removals.Total),
		"cleaned":            count(cleaned.Total),
		"searches":           count(searches.Total),
		"searches_completed": count(searches.Completed),
		"searches_failed":    count(searches.Failed),
		"searches_grabbed":   count(searches.Grabbed),
		"jobs":               count(jobStats.Total),
		"jobs_completed":     count(jobStats.Completed),
		"jobs_failed":        count(jobStats.Failed),
	}, now, timeframe)}

	breakdowns := []struct {
		kind   string
		counts py.Map[py.Value]
	}{
		{"Strikes_By_Type", strikes.ByType},
		{"Removals_By_Reason", removals.ByReason},
		{"Cleaned_By_Reason", cleaned.ByReason},
		{"Events_By_Type", events.ByType},
		{"Events_By_Severity", events.BySeverity},
		{"Searches_By_Reason", searches.ByReason},
	}
	for _, b := range breakdowns {
		for i, name := range b.counts.Keys {
			points = append(points, a.point(b.kind, collector.M{"count": count(b.counts.Values[i])}, now,
				collector.M{"timeframe_hours": hours, "name": py.String(name)}))
		}
	}

	for i, name := range jobStats.ByType.Keys {
		job := jobStats.ByType.Values[i]
		if !job.OK() {
			continue
		}
		j := job.V()
		points = append(points, a.point("Job", collector.M{
			"total":     count(j.Total),
			"completed": count(j.Completed),
			"failed":    count(j.Failed),
			"last_run":  j.LastRunAt.OrString(""),
			"next_run":  j.NextRunAt.OrString(""),
		}, now, collector.M{"timeframe_hours": hours, "name": py.String(name)}))
	}

	for _, group := range []struct {
		kind  string
		items py.Objs[healthItem]
	}{{"Download_Client", hlth.DownloadClients}, {"Arr", hlth.ArrInstances}} {
		for _, item := range group.items {
			fields := collector.M{
				"healthy":      flag(item.IsHealthy),
				"error":        truncate(item.ErrorMessage),
				"last_checked": item.LastChecked.OrString(""),
			}
			if !item.ResponseTimeMs.IsNone() {
				if ms, err := py.ToFloat(item.ResponseTimeMs); err == nil {
					fields["response_time_ms"] = py.Float(ms)
				}
			}
			points = append(points, a.point("Health", fields, now, collector.M{
				"kind": py.String(group.kind), "name": item.Name.OrString(""), "app": item.Type.OrString("")}))
		}
	}
	return a.write(ctx, points, "stats")
}

type bucket struct {
	Date  py.Value `json:"date"`
	Count py.Value `json:"count"`
}

// GetTimeline is get_timeline: hourly buckets of each metric, fetched in parallel and written in metric order.
func (a *API) GetTimeline(ctx context.Context) error {
	results := make([]py.Objs[bucket], len(TimelineMetrics))
	fetched := make([]bool, len(TimelineMetrics))
	var wg sync.WaitGroup
	for i, metric := range TimelineMetrics {
		wg.Add(1)
		go func(i int, metric string) {
			defer wg.Done()
			params := httpx.P("metric", metric, "hours", strconv.Itoa(a.srv.TimelineHours), "bucket", "hour",
				"includeDryRun", a.dryRunParam())
			body, ok := a.http.Get(ctx, "/api/v2/stats/timeline", params)
			if !ok || !isArray(body) {
				return
			}
			results[i], _ = py.Decode[py.Objs[bucket]](body)
			fetched[i] = true
		}(i, metric)
	}
	wg.Wait()

	var points []influx.Point
	for i, metric := range TimelineMetrics {
		if !fetched[i] {
			continue
		}
		for _, b := range results[i] {
			if !b.Date.Truthy() {
				continue
			}
			at, ok := timeOf(b.Date)
			if !ok {
				a.log.Warn(fmt.Sprintf("Skipping a Cleanuparr %s bucket with date %s", metric, b.Date.Repr()))
				continue
			}
			points = append(points, a.point("Timeline", collector.M{"count": count(b.Count)}, at,
				collector.M{"metric": py.String(metric)}))
		}
	}
	return a.write(ctx, points, "timeline")
}

func isArray(body []byte) bool {
	trimmed := strings.TrimLeft(string(body[:min(len(body), 64)]), " \t\r\n")
	return strings.HasPrefix(trimmed, "[")
}

type strike struct {
	IsDryRun py.Value `json:"isDryRun"`
}

type strikeItem struct {
	DownloadID         py.Value         `json:"downloadId"`
	DownloadItemID     py.Value         `json:"downloadItemId"`
	Title              py.Value         `json:"title"`
	TotalStrikes       py.Value         `json:"totalStrikes"`
	StrikesByType      py.Map[py.Value] `json:"strikesByType"`
	FirstStrikeAt      py.Value         `json:"firstStrikeAt"`
	LatestStrikeAt     py.Value         `json:"latestStrikeAt"`
	IsReturning        py.Value         `json:"isReturning"`
	IsMarkedForRemoval py.Value         `json:"isMarkedForRemoval"`
	IsRemoved          py.Value         `json:"isRemoved"`
	Strikes            py.Objs[strike]  `json:"strikes"`
}

type strikePage struct {
	Items py.Objs[strikeItem] `json:"items"`
}

// GetStrikes is get_strikes: the downloads Cleanuparr tracks strikes for.
func (a *API) GetStrikes(ctx context.Context) error {
	now := collector.NowTime(a.deps)
	var page strikePage
	params := httpx.P("page", "1", "pageSize", strconv.Itoa(a.srv.NumStrikesToFetch))
	if !a.http.GetInto(ctx, "/api/strikes", params, &page) {
		return nil
	}
	var points []influx.Point
	for _, item := range page.Items {
		if !a.srv.IncludeDryRun && len(item.Strikes) > 0 && allDryRun(item.Strikes) {
			continue
		}
		status := "Active"
		switch {
		case item.IsRemoved.Truthy():
			status = "Removed"
		case item.IsMarkedForRemoval.Truthy():
			status = "Marked for removal"
		}
		downloadID := item.DownloadID.Or(item.DownloadItemID).OrString("")
		types := make([]string, len(item.StrikesByType.Keys))
		copy(types, item.StrikesByType.Keys)
		sort.Strings(types)
		parts := make([]string, len(types))
		for i, name := range types {
			n, _ := item.StrikesByType.Lookup(name)
			parts[i] = name + " x" + n.String()
		}
		points = append(points, a.point("Strike_Item", collector.M{
			"title":         truncate(item.Title),
			"status":        py.String(status),
			"total_strikes": count(item.TotalStrikes.Or(py.Int(0))),
			"strike_types":  py.String(strings.Join(parts, ", ")),
			"first_strike":  item.FirstStrikeAt.OrString(""),
			"latest_strike": item.LatestStrikeAt.OrString(""),
			"returning":     flag(item.IsReturning),
			"hash":          py.String(py.Hash(strconv.Itoa(a.srv.ID) + downloadID.String())),
		}, now, collector.M{"download_id": downloadID}))
	}
	return a.write(ctx, points, "strikes")
}

func allDryRun(strikes py.Objs[strike]) bool {
	for _, s := range strikes {
		if !s.IsDryRun.Truthy() {
			return false
		}
	}
	return true
}

type event struct {
	ID                  py.Value          `json:"id"`
	Timestamp           py.Value          `json:"timestamp"`
	EventType           py.Value          `json:"eventType"`
	Severity            py.Value          `json:"severity"`
	Message             py.Value          `json:"message"`
	ItemTitle           py.Value          `json:"itemTitle"`
	IsDryRun            py.Value          `json:"isDryRun"`
	DeleteReason        py.Value          `json:"deleteReason"`
	CleanReason         py.Value          `json:"cleanReason"`
	FailedImportReasons py.List[py.Value] `json:"failedImportReasons"`
}

type eventPage struct {
	Items py.Objs[event] `json:"items"`
}

// reason is deleteReason, else cleanReason, else the failed-import reasons joined with "; ". Python crashed on a
// non-string reason; numbers are printed and nulls left out.
func (e event) reason() py.Value {
	if r := e.DeleteReason.Or(e.CleanReason); r.Truthy() {
		return r
	}
	parts := make([]string, 0, len(e.FailedImportReasons))
	for _, r := range e.FailedImportReasons {
		if !r.IsNone() {
			parts = append(parts, r.String())
		}
	}
	return py.String(strings.Join(parts, "; "))
}

// GetEvents is get_events: recent events, written at their own timestamps.
func (a *API) GetEvents(ctx context.Context) error {
	params := httpx.P("page", "1", "pageSize", strconv.Itoa(a.srv.NumEventsToFetch))
	if a.srv.EventsSeverity != "" {
		params = append(params, httpx.Param{Key: "severity", Value: a.srv.EventsSeverity})
	}
	var page eventPage
	if !a.http.GetInto(ctx, "/api/events", params, &page) {
		return nil
	}
	var points []influx.Point
	for _, e := range page.Items {
		if !e.Timestamp.Truthy() {
			continue
		}
		if e.IsDryRun.Truthy() && !a.srv.IncludeDryRun {
			continue
		}
		at, ok := timeOf(e.Timestamp)
		if !ok {
			a.log.Warn(fmt.Sprintf("Skipping a Cleanuparr event with timestamp %s", e.Timestamp.Repr()))
			continue
		}
		points = append(points, a.point("Event", collector.M{
			"title":   truncate(e.ItemTitle),
			"message": truncate(e.Message),
			"reason":  truncate(e.reason()),
			"hash":    py.String(py.Hash(strconv.Itoa(a.srv.ID) + e.ID.String())),
		}, at, collector.M{"event_type": e.EventType.OrString(""), "severity": e.Severity.OrString("")}))
	}
	return a.write(ctx, points, "events")
}
