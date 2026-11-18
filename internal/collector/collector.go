// Package collector holds what every service collector shares: its dependencies, its scheduled jobs and the
// helpers for building points the way the Python collectors did.
package collector

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// Deps are a collector's dependencies.
type Deps struct {
	Writer influx.PointWriter
	Log    *slog.Logger
	// Now returns the current time in the local zone (TZ), which Python used for dates and timestamps.
	Now func() time.Time
	// Retention returns InfluxDB's default retention (0 = forever or unknown). Backfills skip older data.
	Retention func() time.Duration
	// Transport overrides the HTTP transport (tests).
	Transport http.RoundTripper
}

// WithDefaults fills in missing dependencies.
func (d Deps) WithDefaults() Deps {
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Retention == nil {
		d.Retention = func() time.Duration { return 0 }
	}
	return d
}

// Job is one scheduled collection.
type Job struct {
	// Name is the Python schedule tag, e.g. "sonarr-1-get_queue".
	Name     string
	Interval time.Duration
	Run      func(ctx context.Context) error
	// Once runs the job a single time at startup (history backfills) instead of on an interval.
	Once bool
}

// Every builds a recurring job.
func Every(name string, seconds int, run func(context.Context) error) Job {
	return Job{Name: name, Interval: time.Duration(seconds) * time.Second, Run: run}
}

// Once builds a startup-only job.
func Once(name string, run func(context.Context) error) Job {
	return Job{Name: name, Run: run, Once: true}
}

// Point builds a point. Tag and field values are Python-typed; see Tags/Fields helpers.
func Point(measurement string, tags, fields map[string]py.Value, at influx.Time) influx.Point {
	return influx.Point{Measurement: measurement, Tags: tags, Fields: fields, Time: at}
}

// Write sends points, or logs the Python "No data to send" warning when there are none.
func Write(ctx context.Context, d Deps, points []influx.Point, emptyWarning string) error {
	if len(points) == 0 {
		if emptyWarning != "" {
			d.Log.Warn(emptyWarning)
		}
		return nil
	}
	return d.Writer.Write(ctx, points)
}

// NoData is the Python warning for an empty payload, e.g. NoData("sonarr", "queue").
func NoData(service, job string) string {
	return fmt.Sprintf("No data to send to influx for %s-%s instance, discarding.", service, job)
}

// M is a set of tags or fields.
type M = map[string]py.Value

// NowTime is datetime.now(timezone.utc).astimezone(): the current time with microsecond precision, which the
// Python collectors used as every snapshot point's timestamp.
func NowTime(d Deps) influx.Time { return influx.At(d.Now()) }

// Date returns the local calendar date of t at midnight (Python's date.today()).
func Date(t time.Time) time.Time {
	y, m, day := t.Date()
	return time.Date(y, m, day, 0, 0, 0, 0, t.Location())
}

// DateString is str(date): YYYY-MM-DD.
func DateString(t time.Time) string { return t.Format("2006-01-02") }
