package arr

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

type historyEvent struct {
	ID          py.Value `json:"id"`
	Date        py.Value `json:"date"`
	EventType   py.Value `json:"eventType"`
	SourceTitle py.Value `json:"sourceTitle"`
	Data        py.Obj[struct {
		Protocol           py.Value `json:"protocol"`
		Message            py.Value `json:"message"`
		Reason             py.Value `json:"reason"`
		Indexer            py.Value `json:"indexer"`
		DownloadClient     py.Value `json:"downloadClient"`
		DownloadClientName py.Value `json:"downloadClientName"`
	}] `json:"data"`
	Quality py.Obj[quality]   `json:"quality"`
	Series  py.Obj[SeriesRef] `json:"series"`
	Movie   py.Obj[MovieRef]  `json:"movie"`
	Artist  py.Obj[struct {
		ArtistName py.Value `json:"artistName"`
	}] `json:"artist"`
}

// historyTitle is _history_title.
func (b *Base) historyTitle(e historyEvent) py.Value {
	switch b.Measurement {
	case "Radarr":
		movie := e.Movie.V()
		title := movie.Title.OrString("")
		if title.Truthy() && movie.Year.Truthy() {
			return py.String(fmt.Sprintf("%s (%s)", title, movie.Year))
		}
		return title
	case "Lidarr":
		return e.Artist.V().ArtistName.OrString("")
	}
	return e.Series.V().Title.OrString("")
}

// floorMod is Python's % for a positive modulus.
func floorMod(a, m int64) int64 { return ((a % m) + m) % m }

// HistoryEvents is get_history_events: one point per history event of the last days, at the event's own time
// (plus a per-event offset in nanoseconds), so re-polling overwrites rather than duplicates.
//
// When InfluxDB's retention policy is shorter than days, only the retained range is fetched: InfluxDB would drop
// the older points anyway (the Python version fetched and sent them, and got a partial-write error back).
func (b *Base) HistoryEvents(ctx context.Context, days int) error {
	if retention := b.Deps.Retention(); retention > 0 {
		if keep := int(retention / (24 * time.Hour)); keep < days {
			b.Log.Info(fmt.Sprintf("Fetching %d instead of %d days of %s history: older points are beyond the "+
				"retention policy", keep, days, b.Measurement))
			days = keep
		}
	}
	since := Since(b.Deps.Now(), days).Format("2006-01-02T15:04:05Z")
	params := append(httpx.P("date", since), b.HistoryIncludes...)
	body, ok := b.Get(ctx, "/history/since", params)
	if !ok || !IsList(body) {
		return nil
	}
	events, err := py.Decode[py.Objs[historyEvent]](body)
	if err != nil {
		return nil
	}
	payload := make([]influx.Point, 0, len(events))
	for _, e := range events {
		eventID, idErr := py.IntOfFloat(e.ID)
		when, dateOK := ParseArrDate(e.Date)
		if idErr != nil || !dateOK {
			continue
		}
		eventType := e.EventType.OrString("unknown").String()
		data := e.Data.V()
		protocol := ""
		if eventType == "grabbed" {
			protocol = "TORRENT"
			if strings.Contains(py.Lower(data.Protocol.OrString("").String()), "usenet") {
				protocol = "USENET"
			}
		}
		message := py.Truncate(data.Message.Or(data.Reason).OrString("").String(), 300)
		qualityName := "Unknown"
		if inner := e.Quality.V().Quality; inner.OK() {
			qualityName = inner.V().Name.OrString("Unknown").String()
		}
		at := influx.AtNS(when.Unix()*1_000_000_000 + floorMod(eventID, 1_000_000))
		payload = append(payload, b.Point("History_Event", collector.M{
			"title":        py.String(b.historyTitle(e).String()),
			"source_title": py.String(e.SourceTitle.OrString("").String()),
			"message":      py.String(message),
			"event_id":     py.Int(eventID),
			"hash":         py.String(py.Hash(fmt.Sprintf("%d%d", b.ServerID, eventID))),
		}, at, collector.M{
			"event_type":      py.String(eventType),
			"indexer":         py.String(data.Indexer.OrString("").String()),
			"download_client": py.String(data.DownloadClient.Or(data.DownloadClientName).OrString("").String()),
			"quality":         py.String(qualityName),
			"protocol":        py.String(protocol),
		}))
	}
	if len(payload) == 0 {
		return nil
	}
	return b.Deps.Writer.Write(ctx, payload)
}

// secondsBetween is int((later - earlier).total_seconds()), exact for any range.
func secondsBetween(later, earlier time.Time) int64 {
	us := (later.Unix()-earlier.Unix())*1_000_000 + int64(later.Nanosecond()/1000-earlier.Nanosecond()/1000)
	return us / 1_000_000
}

// Tasks is get_tasks: scheduled task timings and the application's version and uptime.
func (b *Base) Tasks(ctx context.Context) error {
	nowTime := py.TruncateToMicro(b.Deps.Now())
	now := influx.At(nowTime)
	var payload []influx.Point
	if body, ok := b.Get(ctx, "/system/task", nil); ok && IsList(body) {
		tasks, _ := py.Decode[py.Objs[struct {
			TaskName      py.Value `json:"taskName"`
			Name          py.Value `json:"name"`
			Interval      py.Value `json:"interval"`
			LastExecution py.Value `json:"lastExecution"`
			NextExecution py.Value `json:"nextExecution"`
			LastDuration  py.Value `json:"lastDuration"`
		}]](body)
		for _, task := range tasks {
			name := task.TaskName.Or(task.Name)
			if !name.Truthy() {
				continue
			}
			sinceLast := int64(-1)
			if last, ok := ParseArrDate(task.LastExecution); ok {
				sinceLast = secondsBetween(nowTime, last)
			}
			nextRun := ""
			if next, ok := ParseArrDate(task.NextExecution); ok {
				nextRun = py.IsoFormat(next)
			}
			payload = append(payload, b.Point("Task", collector.M{
				"interval_minutes":       py.Int(py.ToInt(task.Interval, 0)),
				"seconds_since_last_run": py.Int(sinceLast),
				"last_duration_seconds":  py.Float(DurationSeconds(task.LastDuration)),
				"next_run":               py.String(nextRun),
			}, now, collector.M{"task": py.String(name.String())}))
		}
	}
	if body, ok := b.Get(ctx, "/system/status", nil); ok && IsDict(body) {
		if status, err := py.Decode[struct {
			Version   py.Value `json:"version"`
			StartTime py.Value `json:"startTime"`
		}](body); err == nil {
			uptime := int64(-1)
			if started, ok := ParseArrDate(status.StartTime); ok {
				uptime = secondsBetween(nowTime, started)
			}
			payload = append(payload, b.Point("System", collector.M{
				"version":        py.String(status.Version.OrString("").String()),
				"uptime_seconds": py.Int(uptime),
			}, now, nil))
		}
	}
	return b.Write(ctx, payload, "tasks")
}
