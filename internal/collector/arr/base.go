// Package arr holds what the Sonarr, Radarr and Lidarr collectors share (Python: arr.py, arr_history.py and
// arr_library.py): health, wanted counts, history totals, disk space, per-event history, scheduled tasks, library
// statistics, and the queue summary and per-item fields.
package arr

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// Event is one history event type counted by get_history_totals.
type Event struct {
	Field     string
	EventType int
}

// Base is the shared collector for one *arr server.
type Base struct {
	// Measurement is "Sonarr", "Radarr" or "Lidarr".
	Measurement string
	// Prefix is the API path prefix, "/api/v3" or "/api/v1".
	Prefix string
	// HistoryTypes are the history totals to count, in Python's dict order.
	HistoryTypes []Event
	// HistoryIncludes are the extra /history/since parameters.
	HistoryIncludes httpx.Params
	ServerID        int
	Client          *httpx.Client
	Deps            collector.Deps
	Log             *slog.Logger
}

// NewClient builds the server's HTTP client: the API key header and, for Sonarr, the session-wide pageSize.
func NewClient(url, apiKey string, verifySSL bool, session httpx.Params, d collector.Deps) *httpx.Client {
	return httpx.New(httpx.Options{
		BaseURL:       url,
		VerifySSL:     verifySSL,
		Headers:       map[string]string{"X-Api-Key": apiKey},
		SessionParams: session,
		Log:           d.Log,
		Transport:     d.Transport,
	})
}

// Server is the server tag value (a Python int).
func (b *Base) Server() py.Value { return py.Int(int64(b.ServerID)) }

// Get fetches Prefix+path; ok is false unless the server answered 200 with JSON.
func (b *Base) Get(ctx context.Context, path string, params httpx.Params) ([]byte, bool) {
	return b.Client.Get(ctx, b.Prefix+path, params)
}

// Point is _arr_point: the measurement, a type tag, the server tag plus extra tags.
func (b *Base) Point(pointType string, fields collector.M, at influx.Time, tags collector.M) influx.Point {
	all := collector.M{"type": py.String(pointType), "server": b.Server()}
	for k, v := range tags {
		all[k] = v
	}
	return collector.Point(b.Measurement, all, fields, at)
}

// Write is _write_arr: write, or warn that there was nothing to write.
func (b *Base) Write(ctx context.Context, payload []influx.Point, job string) error {
	return collector.Write(ctx, b.Deps, payload, collector.NoData(strings.ToLower(b.Measurement), job))
}

// valueKey identifies a Python value as a dict key (type and text).
func valueKey(v py.Value) string { return fmt.Sprintf("%d:%s", v.Kind(), v.String()) }

// Health is get_health: one point per health check plus a summary.
func (b *Base) Health(ctx context.Context) error {
	now := collector.NowTime(b.Deps)
	body, ok := b.Get(ctx, "/health", nil)
	if !ok || !IsList(body) {
		return nil
	}
	checks, err := py.Decode[py.Objs[struct {
		Type    py.Value `json:"type"`
		Source  py.Value `json:"source"`
		Message py.Value `json:"message"`
		WikiURL py.Value `json:"wikiUrl"`
	}]](body)
	if err != nil {
		return nil
	}
	var errors, warnings, notices int64
	seen := map[string]int{}
	payload := make([]influx.Point, 0, len(checks)+1)
	for _, check := range checks {
		level := py.Lower(check.Type.OrString("unknown").String())
		switch level {
		case "error":
			errors++
		case "warning":
			warnings++
		case "notice":
			notices++
		}
		source := check.Source.OrString("Unknown")
		key := valueKey(source) + "\x00" + level
		seen[key]++
		name := source
		if seen[key] > 1 {
			name = py.String(fmt.Sprintf("%s#%d", source, seen[key]))
		}
		payload = append(payload, b.Point("Health", collector.M{
			"message":  py.String(py.Truncate(check.Message.OrString("").String(), 500)),
			"wiki_url": py.String(check.WikiURL.OrString("").String()),
		}, now, collector.M{"source": name, "level": py.String(level)}))
	}
	payload = append(payload, b.Point("Health_Summary", collector.M{
		"errors": py.Int(errors), "warnings": py.Int(warnings), "notices": py.Int(notices),
		"issues": py.Int(errors + warnings),
	}, now, nil))
	return b.Write(ctx, payload, "health")
}

// count is _count: totalRecords of a paged endpoint, asking for a single record.
func (b *Base) count(ctx context.Context, path string, params ...string) (int64, bool) {
	body, ok := b.Get(ctx, path, append(httpx.P(params...), httpx.Param{Key: "pageSize", Value: "1"}))
	if !ok || !IsDict(body) {
		return 0, false
	}
	page, err := py.Decode[struct {
		TotalRecords py.Value `json:"totalRecords"`
	}](body)
	if err != nil || !page.TotalRecords.Exists() {
		return 0, false
	}
	return py.ToInt(page.TotalRecords, 0), true
}

type countJob struct {
	field  string
	path   string
	params []string
}

// counts runs several count requests in parallel and returns the fields that succeeded.
func (b *Base) counts(ctx context.Context, jobs []countJob) collector.M {
	results := make([]struct {
		n  int64
		ok bool
	}, len(jobs))
	var wg sync.WaitGroup
	for i, j := range jobs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i].n, results[i].ok = b.count(ctx, j.path, j.params...)
		}()
	}
	wg.Wait()
	fields := collector.M{}
	for i, j := range jobs {
		if results[i].ok {
			fields[j.field] = py.Int(results[i].n)
		}
	}
	return fields
}

// Wanted is get_wanted: missing (monitored and not) and cutoff-unmet counts.
func (b *Base) Wanted(ctx context.Context) error {
	now := collector.NowTime(b.Deps)
	fields := b.counts(ctx, []countJob{
		{"missing_monitored", "/wanted/missing", []string{"monitored", "true"}},
		{"missing_unmonitored", "/wanted/missing", []string{"monitored", "false"}},
		{"cutoff_unmet", "/wanted/cutoff", []string{"monitored", "true"}},
	})
	var payload []influx.Point
	if len(fields) > 0 {
		payload = append(payload, b.Point("Wanted", fields, now, nil))
	}
	return b.Write(ctx, payload, "wanted")
}

// HistoryTotals is get_history_totals: all-time counts per history event type, plus the blocklist size.
func (b *Base) HistoryTotals(ctx context.Context) error {
	now := collector.NowTime(b.Deps)
	jobs := make([]countJob, 0, len(b.HistoryTypes)+1)
	for _, e := range b.HistoryTypes {
		jobs = append(jobs, countJob{e.Field, "/history", []string{"eventType", fmt.Sprint(e.EventType)}})
	}
	jobs = append(jobs, countJob{"blocklisted", "/blocklist", nil})
	fields := b.counts(ctx, jobs)
	var payload []influx.Point
	if len(fields) > 0 {
		payload = append(payload, b.Point("History_Totals", fields, now, nil))
	}
	return b.Write(ctx, payload, "history")
}

type disk struct {
	Path       py.Value `json:"path"`
	Label      py.Value `json:"label"`
	FreeSpace  py.Value `json:"freeSpace"`
	TotalSpace py.Value `json:"totalSpace"`
}

// VolumeContains reports whether a root folder lives on a volume. The Python version compared plain string
// prefixes, so a root folder in /data2 was matched to the volume /data; here the match must end on a path
// separator.
func VolumeContains(volume, path string) bool {
	v := strings.TrimRight(volume, "/")
	p := strings.TrimRight(path, "/")
	if !strings.HasPrefix(p, v) {
		return false
	}
	if v == "" || len(p) == len(v) || strings.HasSuffix(v, `\`) || strings.HasSuffix(v, ":") {
		return true
	}
	next := p[len(v)]
	return next == '/' || next == '\\'
}

// Disk is get_disk: free and total space per volume and per root folder.
func (b *Base) Disk(ctx context.Context) error {
	now := collector.NowTime(b.Deps)
	var disks py.Objs[disk]
	if body, ok := b.Get(ctx, "/diskspace", nil); ok && IsList(body) {
		disks, _ = py.Decode[py.Objs[disk]](body)
	}
	var roots py.Objs[struct {
		Path            py.Value `json:"path"`
		FreeSpace       py.Value `json:"freeSpace"`
		TotalSpace      py.Value `json:"totalSpace"`
		Accessible      py.Value `json:"accessible"`
		UnmappedFolders py.Value `json:"unmappedFolders"`
	}]
	if body, ok := b.Get(ctx, "/rootfolder", nil); ok && IsList(body) {
		_ = roots.UnmarshalJSON(body)
	}

	var payload []influx.Point
	for _, d := range disks {
		if !d.Path.Truthy() {
			continue
		}
		payload = append(payload, b.Point("Disk_Space", collector.M{
			"free_bytes":  py.Int(py.ToInt(d.FreeSpace, 0)),
			"total_bytes": py.Int(py.ToInt(d.TotalSpace, 0)),
		}, now, collector.M{"path": d.Path, "label": d.Label.OrString("")}))
	}
	for _, root := range roots {
		if !root.Path.Truthy() {
			continue
		}
		unmapped := 0
		if root.UnmappedFolders.Truthy() {
			unmapped, _ = root.UnmappedFolders.Len()
		}
		fields := collector.M{
			"free_bytes":       py.Int(py.ToInt(root.FreeSpace, 0)),
			"accessible":       py.Int(boolInt(root.Accessible.Truthy())),
			"unmapped_folders": py.Int(int64(unmapped)),
		}
		total, hasTotal := intOrNone(root.TotalSpace)
		if !hasTotal || total == 0 {
			// Sonarr and Radarr report no totalSpace for root folders: use the volume it lives on.
			var best *disk
			for i := range disks {
				d := &disks[i]
				if !d.Path.Truthy() || !VolumeContains(d.Path.String(), root.Path.String()) {
					continue
				}
				if best == nil || py.RuneLen(d.Path.String()) > py.RuneLen(best.Path.String()) {
					best = d
				}
			}
			if best != nil {
				total, hasTotal = intOrNone(best.TotalSpace)
			}
		}
		if hasTotal && total != 0 {
			fields["total_bytes"] = py.Int(total)
		}
		payload = append(payload, b.Point("Root_Folder", fields, now, collector.M{"path": root.Path}))
	}
	return b.Write(ctx, payload, "disk")
}

// intOrNone is to_int(value, None).
func intOrNone(v py.Value) (int64, bool) {
	n, err := py.IntOfFloat(v)
	return n, err == nil
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// FirstStatusMessage is arr.first_status_message.
func FirstStatusMessage(item QueueItem) py.Value {
	for _, status := range item.StatusMessages {
		for _, message := range status.Messages {
			if message.Truthy() {
				return py.String(py.Truncate(message.String(), 500))
			}
		}
		if status.Title.Truthy() {
			return py.String(py.Truncate(status.Title.String(), 500))
		}
	}
	return item.ErrorMessage.OrString("")
}

// QueueItemFields is arr.queue_item_fields: the numeric and state fields of one queue record.
func QueueItemFields(item QueueItem) collector.M {
	size := py.ToInt(item.Size, 0)
	sizeleft := py.ToInt(item.Sizeleft, 0)
	progress := 0.0
	if size != 0 {
		progress = py.Round(float64(size-sizeleft)/float64(size)*100, 2)
	}
	fields := collector.M{
		"size":            py.Int(size),
		"sizeleft":        py.Int(sizeleft),
		"progress":        py.Float(progress),
		"status":          item.Status.OrString(""),
		"tracked_state":   item.TrackedDownloadState.OrString(""),
		"tracked_status":  item.TrackedDownloadStatus.OrString(""),
		"status_message":  FirstStatusMessage(item),
		"download_client": item.DownloadClient.OrString(""),
		"indexer":         item.Indexer.OrString(""),
	}
	if seconds, ok := TimeleftSeconds(item.Timeleft); ok {
		fields["timeleft"] = py.Int(seconds)
	}
	return fields
}

// queueStates are counted into state_* fields, in Python's order.
var queueStates = []string{"downloading", "importBlocked", "importPending", "importing", "imported",
	"failedPending", "failed", "ignored"}

func snakeCase(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('_')
			b.WriteRune(r + ('a' - 'A'))
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// QueueSummary is queue_summary_point: totals over the raw records plus /queue/status.
func (b *Base) QueueSummary(ctx context.Context, records []QueueItem, now influx.Time) influx.Point {
	var size, sizeleft, warnings, errors int64
	for _, r := range records {
		size += py.ToInt(r.Size, 0)
		sizeleft += py.ToInt(r.Sizeleft, 0)
		switch py.Lower(r.TrackedDownloadStatus.OrString("").String()) {
		case "warning":
			warnings++
		case "error":
			errors++
		}
	}
	fields := collector.M{
		"items":         py.Int(int64(len(records))),
		"size":          py.Int(size),
		"sizeleft":      py.Int(sizeleft),
		"warning_items": py.Int(warnings),
		"error_items":   py.Int(errors),
	}
	for _, state := range queueStates {
		var n int64
		for _, r := range records {
			if s, ok := r.TrackedDownloadState.AsString(); ok && s == state {
				n++
			}
		}
		fields["state_"+snakeCase(state)] = py.Int(n)
	}
	if body, ok := b.Get(ctx, "/queue/status", nil); ok && IsDict(body) {
		if status, err := py.Decode[struct {
			TotalCount   py.Value `json:"totalCount"`
			UnknownCount py.Value `json:"unknownCount"`
			Errors       py.Value `json:"errors"`
			Warnings     py.Value `json:"warnings"`
		}](body); err == nil {
			fields["total_count"] = py.Int(py.ToInt(status.TotalCount, 0))
			fields["unknown_count"] = py.Int(py.ToInt(status.UnknownCount, 0))
			fields["has_errors"] = py.Int(boolInt(status.Errors.Truthy()))
			fields["has_warnings"] = py.Int(boolInt(status.Warnings.Truthy()))
		}
	}
	return b.Point("Queue_Summary", fields, now, nil)
}

// FetchQueuePages fetches every page of /queue (Sonarr and Radarr). Python stopped (writing nothing) when any
// page failed. It also looped forever when a server kept answering with the same page; paging stops here once
// the page number stops advancing or after maxQueuePages.
func (b *Base) FetchQueuePages(ctx context.Context, params func(page int) httpx.Params) ([]QueueItem, bool) {
	var records []QueueItem
	body, ok := b.Get(ctx, "/queue", params(0))
	if !ok || !httpx.Truthy(body) || !IsDict(body) {
		return nil, false
	}
	page, err := py.Decode[Page[QueueItem]](body)
	if err != nil || !page.RecordsList {
		return nil, false
	}
	records = append(records, page.Items...)
	for n := 0; n < maxQueuePages; n++ {
		total, okT := page.TotalRecords.AsInt()
		current, okP := page.Page.AsInt()
		size, okS := page.PageSize.AsInt()
		if !okT || !okP || !okS || total <= current*size {
			break
		}
		body, ok = b.Get(ctx, "/queue", params(int(current+1)))
		if !ok || !httpx.Truthy(body) || !IsDict(body) {
			return nil, false
		}
		next, err := py.Decode[Page[QueueItem]](body)
		if err != nil || !next.RecordsList {
			return nil, false
		}
		records = append(records, next.Items...)
		if nextPage, okN := next.Page.AsInt(); !okN || nextPage <= current {
			break
		}
		page = next
	}
	return records, true
}

const maxQueuePages = 1000

// Since is the current time minus days, as Python's datetime arithmetic gives it.
func Since(now time.Time, days int) time.Time {
	return now.UTC().Add(-time.Duration(days) * 24 * time.Hour)
}
