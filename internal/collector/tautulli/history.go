package tautulli

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

const historyPageSize = 1000

type historyLibrary struct {
	SectionID   py.Value `json:"section_id"`
	SectionName py.Value `json:"section_name"`
}

type historyPage struct {
	RecordsFiltered py.Value                     `json:"recordsFiltered"`
	Data            py.Objs[map[string]py.Value] `json:"data"`
}

// HistoryPlays writes one point per finished play of the last days days, timestamped at its stop time so that
// re-polling overwrites the same points. The import-tautulli command calls it with a long range.
//
// When InfluxDB's retention policy is shorter than days, only what it keeps is fetched (Python fetched it all and
// InfluxDB dropped the rest), and points are written page by page instead of all at the end.
func (a *API) HistoryPlays(ctx context.Context, days int) error {
	if keep := a.d.Retention(); keep > 0 {
		if limit := int(math.Ceil(keep.Hours() / 24)); days > limit {
			a.log.Info(fmt.Sprintf("Fetching %d days of Tautulli history instead of %d: InfluxDB keeps %s",
				limit, days, keep))
			days = limit
		}
	}
	raw, ok := a.data(ctx, "get_libraries")
	libraries, decoded := decode[py.Objs[historyLibrary]](raw)
	if !ok || !decoded || len(libraries) == 0 {
		return nil
	}
	after := collector.DateString(collector.Date(a.d.Now()).AddDate(0, 0, -days))
	var errs []error
	for _, lib := range libraries {
		section := lib.SectionName.OrString("Unknown")
		var start int64
		for {
			params := httpx.Params{}
			if !lib.SectionID.IsNone() {
				params = append(params, httpx.Param{Key: "section_id", Value: lib.SectionID.String()})
			}
			params = append(params, httpx.P("after", after, "length", strconv.Itoa(historyPageSize),
				"start", strconv.FormatInt(start, 10))...)
			raw, ok := a.data(ctx, "get_history", params...)
			if !ok || !isObject(raw) {
				break
			}
			page, _ := decode[historyPage](raw)
			if points := a.playPoints(page.Data, section); len(points) > 0 {
				if err := a.write(ctx, points); err != nil {
					errs = append(errs, err)
				}
			}
			start += int64(len(page.Data))
			if len(page.Data) == 0 || start >= num(page.RecordsFiltered) {
				break
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("writing Tautulli history: %v", errs)
	}
	return nil
}

func (a *API) playPoints(rows []map[string]py.Value, section py.Value) []influx.Point {
	points := make([]influx.Point, 0, len(rows))
	for _, row := range rows {
		rowID, stopped := num(row["id"]), num(row["stopped"])
		if rowID == 0 || stopped == 0 {
			continue
		}
		platformName := row["platform"].OrString("")
		switch text(platformName) {
		case "osx":
			platformName = py.String("macOS")
		case "windows":
			platformName = py.String("Windows")
		}
		duration := num(row["duration"])
		if played := row["play_duration"]; !played.IsNone() && !played.Equal(py.String("")) {
			duration = num(played)
		}
		watched := int64(0)
		if py.ToFloatOr(row["watched_status"], 0) == 1 {
			watched = 1
		}
		tags := collector.M{
			"type":               py.String("Play"),
			"server":             a.server(),
			"user":               row["friendly_name"].Or(row["user"]).OrString(""),
			"platform":           platformName,
			"product":            row["product"].OrString(""),
			"media_type":         row["media_type"].OrString(""),
			"transcode_decision": row["transcode_decision"].OrString(""),
			"network":            row["location"].OrString(""),
			"library":            section,
		}
		fields := collector.M{
			"title":            py.String(row["full_title"].OrString("").String()),
			"duration":         py.Int(duration),
			"paused":           py.Int(num(row["paused_counter"])),
			"percent_complete": py.Int(num(row["percent_complete"])),
			"watched":          py.Int(watched),
			"row_id":           py.Int(rowID),
			"hash":             py.String(py.Hash(fmt.Sprintf("%d%d", a.srv.ID, rowID))),
		}
		ns := stopped*1_000_000_000 + floorMod(rowID, 1_000_000)
		points = append(points, collector.Point(measurement, tags, fields, influx.AtNS(ns)))
	}
	return points
}

// dayPattern is what Python's strptime(day, '%Y-%m-%d') accepts.
var dayPattern = regexp.MustCompile(`^(\d{4})-(1[0-2]|0[1-9]|[1-9])-(3[01]|[12]\d|0[1-9]|[1-9]| [1-9])$`)

func parseDay(v py.Value) (time.Time, bool) {
	s, ok := v.AsString()
	if !ok {
		return time.Time{}, false
	}
	m := dayPattern.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	y, _ := strconv.Atoi(m[1])
	mo, _ := strconv.Atoi(m[2])
	d, _ := strconv.Atoi(trimSpace(m[3]))
	t := time.Date(y, time.Month(mo), d, 0, 0, 0, 0, time.UTC)
	if y < 1 || t.Day() != d || int(t.Month()) != mo {
		return time.Time{}, false
	}
	return t, true
}

func trimSpace(s string) string {
	if len(s) > 0 && s[0] == ' ' {
		return s[1:]
	}
	return s
}

// ConcurrentStreams writes the daily concurrent stream counts per stream type of the last days days.
func (a *API) ConcurrentStreams(ctx context.Context, days int) error {
	raw, ok := a.data(ctx, "get_concurrent_streams_by_stream_type", httpx.P("time_range", strconv.Itoa(days))...)
	if !ok || !isObject(raw) {
		return nil
	}
	data, _ := decode[struct {
		Categories py.List[py.Value] `json:"categories"`
		Series     py.Objs[struct {
			Name py.Value          `json:"name"`
			Data py.List[py.Value] `json:"data"`
		}] `json:"series"`
	}](raw)
	var points []influx.Point
	for _, series := range data.Series {
		for i := 0; i < len(data.Categories) && i < len(series.Data); i++ {
			day, ok := parseDay(data.Categories[i])
			if !ok {
				continue
			}
			points = append(points, collector.Point(measurement, collector.M{
				"type":        py.String("Concurrent_Streams"),
				"server":      a.server(),
				"stream_type": series.Name.OrString("Unknown"),
			}, collector.M{"count": py.Int(num(series.Data[i]))}, influx.At(day)))
		}
	}
	return a.write(ctx, points)
}

// Users writes each user's lifetime plays and watch time.
func (a *API) Users(ctx context.Context) error {
	now := collector.NowTime(a.d)
	raw, ok := a.data(ctx, "get_users_table", httpx.P("length", "100")...)
	if !ok || !isObject(raw) {
		return nil
	}
	data, _ := decode[struct {
		Data py.Objs[map[string]py.Value] `json:"data"`
	}](raw)
	points := make([]influx.Point, 0, len(data.Data))
	for _, user := range data.Data {
		points = append(points, collector.Point(measurement, collector.M{
			"type":   py.String("User"),
			"server": a.server(),
			"user":   user["friendly_name"].Or(user["username"]).OrString("Unknown"),
		}, collector.M{
			"plays":     py.Int(num(user["plays"])),
			"duration":  py.Int(num(user["duration"])),
			"last_seen": py.String(isoUTC(user["last_seen"])),
			"is_active": py.Int(num(user["is_active"])),
		}, now))
	}
	return a.write(ctx, points)
}

// LibraryUsage writes each active library's lifetime plays and watch time.
func (a *API) LibraryUsage(ctx context.Context) error {
	now := collector.NowTime(a.d)
	raw, ok := a.data(ctx, "get_libraries_table", httpx.P("length", "50")...)
	if !ok || !isObject(raw) {
		return nil
	}
	data, _ := decode[struct {
		Data py.Objs[map[string]py.Value] `json:"data"`
	}](raw)
	var points []influx.Point
	for _, lib := range data.Data {
		if num(lib["is_active"]) != 1 {
			continue
		}
		points = append(points, collector.Point(measurement, collector.M{
			"type":       py.String("Library_Usage"),
			"server":     a.server(),
			"library":    lib["section_name"].OrString("Unknown"),
			"section_id": py.String(lib["section_id"].String()),
		}, collector.M{
			"plays":       py.Int(num(lib["plays"])),
			"duration":    py.Int(num(lib["duration"])),
			"items":       py.Int(num(lib["count"])),
			"last_played": py.String(isoUTC(lib["last_accessed"])),
		}, now))
	}
	return a.write(ctx, points)
}

// RecentlyAdded writes how many items each library gained in the last 24 hours and 7 days.
func (a *API) RecentlyAdded(ctx context.Context) error {
	nowTime := a.d.Now()
	nowTS := float64(nowTime.UnixMicro()) / 1e6
	now := influx.At(nowTime)
	raw, ok := a.data(ctx, "get_recently_added", httpx.P("count", "50")...)
	if !ok || !isObject(raw) {
		return nil
	}
	data, _ := decode[struct {
		RecentlyAdded py.Objs[map[string]py.Value] `json:"recently_added"`
	}](raw)
	type counts struct {
		library   py.Value
		day, week int64
	}
	var order []string
	byLibrary := map[string]*counts{}
	for _, item := range data.RecentlyAdded {
		library := item["library_name"].OrString("Unknown")
		key := fmt.Sprintf("%d:%s", library.Kind(), library.String())
		c, seen := byLibrary[key]
		if !seen {
			c = &counts{library: library}
			byLibrary[key] = c
			order = append(order, key)
		}
		age := nowTS - float64(num(item["added_at"]))
		if age < 0 {
			continue
		}
		if age <= 86400 {
			c.day++
		}
		if age <= 7*86400 {
			c.week++
		}
	}
	points := make([]influx.Point, 0, len(order))
	for _, key := range order {
		c := byLibrary[key]
		points = append(points, collector.Point(measurement, collector.M{
			"type":    py.String("Recently_Added"),
			"server":  a.server(),
			"library": c.library,
		}, collector.M{"added_24h": py.Int(c.day), "added_7d": py.Int(c.week)}, now))
	}
	return a.write(ctx, points)
}
