package tautulli

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/httpx"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// session holds the TautulliStream fields the collector reads; every other field is ignored.
type session struct {
	SessionID               py.Value `json:"session_id"`
	SessionKey              py.Value `json:"session_key"`
	Username                py.Value `json:"username"`
	FriendlyName            py.Value `json:"friendly_name"`
	FullTitle               py.Value `json:"full_title"`
	IPAddress               py.Value `json:"ip_address"`
	IPAddressPublic         py.Value `json:"ip_address_public"`
	Product                 py.Value `json:"product"`
	Platform                py.Value `json:"platform"`
	ProductVersion          py.Value `json:"product_version"`
	TranscodeDecision       py.Value `json:"transcode_decision"`
	StreamVideoDecision     py.Value `json:"stream_video_decision"`
	StreamVideoResolution   py.Value `json:"stream_video_resolution"`
	StreamVideoFullRes      py.Value `json:"stream_video_full_resolution"`
	Container               py.Value `json:"container"`
	State                   py.Value `json:"state"`
	TranscodeHWDecoding     py.Value `json:"transcode_hw_decoding"`
	TranscodeHWEncoding     py.Value `json:"transcode_hw_encoding"`
	MediaType               py.Value `json:"media_type"`
	AudioCodec              py.Value `json:"audio_codec"`
	AudioProfile            py.Value `json:"audio_profile"`
	StreamAudioCodec        py.Value `json:"stream_audio_codec"`
	QualityProfile          py.Value `json:"quality_profile"`
	Relayed                 py.Value `json:"relayed"`
	Secure                  py.Value `json:"secure"`
	Location                py.Value `json:"location"`
	Bandwidth               py.Value `json:"bandwidth"`
	StreamBitrate           py.Value `json:"stream_bitrate"`
	Bitrate                 py.Value `json:"bitrate"`
	ProgressPercent         py.Value `json:"progress_percent"`
	ViewOffset              py.Value `json:"view_offset"`
	Duration                py.Value `json:"duration"`
	TranscodeSpeed          py.Value `json:"transcode_speed"`
	TranscodeThrottled      py.Value `json:"transcode_throttled"`
	TranscodeHWFullPipeline py.Value `json:"transcode_hw_full_pipeline"`
	SubtitleDecision        py.Value `json:"subtitle_decision"`
	StreamVideoDynamicRange py.Value `json:"stream_video_dynamic_range"`
	LibraryName             py.Value `json:"library_name"`
	AudioAtmos              py.Value `json:"audio_atmos"`
	VideoDoviPresent        py.Value `json:"video_dovi_present"`
}

type activityData struct {
	StreamCount           py.Value        `json:"stream_count"`
	TotalBandwidth        py.Value        `json:"total_bandwidth"`
	WanBandwidth          py.Value        `json:"wan_bandwidth"`
	LanBandwidth          py.Value        `json:"lan_bandwidth"`
	StreamCountTranscode  py.Value        `json:"stream_count_transcode"`
	StreamCountDirectPlay py.Value        `json:"stream_count_direct_play"`
	StreamCountDirectStr  py.Value        `json:"stream_count_direct_stream"`
	Sessions              json.RawMessage `json:"sessions"`
}

// decisionTitle maps "copy" to "direct stream" and title-cases, as for transcode_decision.
func decisionTitle(v py.Value) string {
	t := text(v)
	if t == "copy" {
		t = "direct stream"
	}
	return py.Title(t)
}

// videoDecision also maps "" (audio-only sessions) to "Music".
func videoDecision(v py.Value) string {
	t := text(v)
	switch t {
	case "copy":
		t = "direct stream"
	case "":
		t = "Music"
	}
	return py.Title(t)
}

// quality is the stream's video resolution label: the container for audio, SD and 4K upper-cased, the full
// resolution when Tautulli gives one, otherwise the resolution plus "p".
func quality(s session) py.Value {
	res := s.StreamVideoResolution
	if !res.Truthy() {
		return py.String(py.Upper(text(s.Container)))
	}
	if r, ok := res.AsString(); ok && (r == "SD" || r == "sd" || r == "4k") {
		return py.String(py.Upper(r))
	}
	if s.StreamVideoFullRes.Truthy() {
		return s.StreamVideoFullRes
	}
	return py.String(res.String() + "p")
}

// playerState maps playing/paused/buffering to 0/1/3 and leaves other states as their lowercase text.
func playerState(v py.Value) py.Value {
	switch state := py.Lower(text(v)); state {
	case "playing":
		return py.Int(0)
	case "paused":
		return py.Int(1)
	case "buffering":
		return py.Int(3)
	default:
		return py.String(state)
	}
}

// platform returns the platform and product version as written: macOS and Windows renamed, and the build suffix
// dropped from Roku, macOS and Windows versions.
func platform(s session) (py.Value, py.Value) {
	name, version := s.Platform, s.ProductVersion
	p, _ := name.AsString()
	if p == "Roku" || p == "osx" || p == "windows" {
		v, _, _ := strings.Cut(text(version), "-")
		version = py.String(v)
	}
	switch p {
	case "osx":
		name = py.String("macOS")
	case "windows":
		name = py.String("Windows")
	}
	return name, version
}

// sessionFields are a session's numbers and low-value strings, written as fields so they don't create series.
func sessionFields(s session) collector.M {
	return collector.M{
		"bandwidth":                  py.Int(num(s.Bandwidth)),
		"stream_bitrate":             py.Int(num(s.StreamBitrate)),
		"source_bitrate":             py.Int(num(s.Bitrate)),
		"progress":                   py.Float(numFloat(s.ProgressPercent)),
		"view_offset":                py.Int(floorDiv(num(s.ViewOffset), 1000)),
		"duration":                   py.Int(floorDiv(num(s.Duration), 1000)),
		"transcode_speed":            py.Float(numFloat(s.TranscodeSpeed)),
		"transcode_throttled":        py.Int(num(s.TranscodeThrottled)),
		"transcode_hw_full_pipeline": py.Int(num(s.TranscodeHWFullPipeline)),
		"subtitle_decision":          s.SubtitleDecision.OrString(""),
		"dynamic_range":              s.StreamVideoDynamicRange.OrString(""),
		"network":                    s.Location.OrString(""),
		"library":                    s.LibraryName.OrString(""),
		"atmos":                      py.Int(num(s.AudioAtmos)),
		"dolby_vision":               py.Int(num(s.VideoDoviPresent)),
	}
}

// count is int(value) for the activity totals. Python raised on a blank or missing count and lost the run;
// it is 0 here.
func (a *API) count(name string, v py.Value) py.Value {
	n, err := py.IntExact(v)
	if err != nil {
		a.log.Warn(fmt.Sprintf("Tautulli returned %s=%s, writing 0", name, v.Repr()))
		n = py.ToInt(v, 0)
	}
	return py.Int(n)
}

// GetActivity writes one point per current stream plus the stream totals.
func (a *API) GetActivity(ctx context.Context) error {
	now := collector.NowTime(a.d)
	body, ok := a.call(ctx, "get_activity")
	if !ok {
		return nil
	}
	raw, ok := rawData(body)
	if !ok || !isObject(raw) {
		a.log.Error("Tautulli get_activity returned no data")
		return nil
	}
	data, _ := decode[activityData](raw)
	if data.Sessions == nil {
		a.log.Error("Tautulli get_activity returned no sessions: " + py.Truncate(string(body), 300))
		return nil
	}
	var sessions py.Objs[session]
	_ = json.Unmarshal(data.Sessions, &sessions)

	points := make([]influx.Point, 0, len(sessions)+1)
	for _, s := range sessions {
		loc := a.locate(ctx, s.IPAddressPublic)
		name, version := platform(s)
		tags := collector.M{
			"type":                  py.String("Session"),
			"session_id":            s.SessionID,
			"ip_address":            s.IPAddress,
			"friendly_name":         s.FriendlyName,
			"username":              s.Username,
			"title":                 s.FullTitle,
			"product":               s.Product,
			"platform":              name,
			"product_version":       version,
			"quality":               quality(s),
			"video_decision":        py.String(videoDecision(s.StreamVideoDecision)),
			"transcode_decision":    py.String(decisionTitle(s.TranscodeDecision)),
			"transcode_hw_decoding": s.TranscodeHWDecoding,
			"transcode_hw_encoding": s.TranscodeHWEncoding,
			"media_type":            py.String(py.Title(text(s.MediaType))),
			"audio_codec":           py.String(py.Upper(text(s.AudioCodec))),
			"audio_profile":         py.String(py.Upper(text(s.AudioProfile))),
			"stream_audio_codec":    py.String(py.Upper(text(s.StreamAudioCodec))),
			"quality_profile":       s.QualityProfile,
			"player_state":          playerState(s.State),
			"device_type":           name,
			"relayed":               s.Relayed,
			"secure":                s.Secure,
			"network":               s.Location.OrString(""),
			"server":                a.server(),
		}
		for k, v := range locationTags(loc) {
			tags[k] = v
		}
		fields := sessionFields(s)
		fields["hash"] = py.String(py.Hash(s.SessionID.String() + s.SessionKey.String() + s.Username.String() +
			s.FullTitle.String()))
		points = append(points, collector.Point(measurement, tags, fields, now))
	}

	var transcodes, hw, throttled, lan, wan int64
	for _, s := range sessions {
		if s.TranscodeDecision.Equal(py.String("transcode")) {
			transcodes++
			if num(s.TranscodeHWEncoding) != 0 || num(s.TranscodeHWDecoding) != 0 {
				hw++
			}
		}
		if num(s.TranscodeThrottled) != 0 {
			throttled++
		}
		switch text(s.Location.OrString("")) {
		case "lan":
			lan++
		case "wan", "cellular":
			wan++
		}
	}
	points = append(points, collector.Point(measurement,
		collector.M{"type": py.String("current_stream_stats"), "server": a.server()},
		collector.M{
			"stream_count":        a.count("stream_count", data.StreamCount),
			"total_bandwidth":     a.count("total_bandwidth", data.TotalBandwidth),
			"wan_bandwidth":       a.count("wan_bandwidth", data.WanBandwidth),
			"lan_bandwidth":       a.count("lan_bandwidth", data.LanBandwidth),
			"transcode_streams":   a.count("stream_count_transcode", data.StreamCountTranscode),
			"direct_play_streams": a.count("stream_count_direct_play", data.StreamCountDirectPlay),
			"direct_streams":      a.count("stream_count_direct_stream", data.StreamCountDirectStr),
			"hw_transcodes":       py.Int(hw),
			"sw_transcodes":       py.Int(transcodes - hw),
			"throttled_streams":   py.Int(throttled),
			"lan_streams":         py.Int(lan),
			"wan_streams":         py.Int(wan),
		}, now))
	return a.write(ctx, points)
}

type library struct {
	SectionID   py.Value `json:"section_id"`
	SectionName py.Value `json:"section_name"`
	SectionType py.Value `json:"section_type"`
	Count       py.Value `json:"count"`
	ParentCount py.Value `json:"parent_count"`
	ChildCount  py.Value `json:"child_count"`
}

// GetStats writes item counts per library.
func (a *API) GetStats(ctx context.Context) error {
	now := collector.NowTime(a.d)
	body, ok := a.call(ctx, "get_libraries")
	if !ok {
		return nil
	}
	raw, _ := rawData(body)
	libraries, ok := decode[py.Objs[library]](raw)
	if !ok {
		a.log.Error("Tautulli get_libraries returned no libraries")
		return nil
	}
	points := make([]influx.Point, 0, len(libraries))
	for _, lib := range libraries {
		fields := collector.M{}
		// int() of each count; Python lost every library on one blank count, it is left out here.
		set := func(name string, v py.Value) {
			n, err := py.IntExact(v)
			if err != nil {
				a.log.Warn(fmt.Sprintf("Tautulli library %s has %s=%s, leaving it out", lib.SectionName, name, v.Repr()))
				return
			}
			fields[name] = py.Int(n)
		}
		set("total", lib.Count)
		switch {
		case lib.SectionType.Equal(py.String("show")):
			set("seasons", lib.ParentCount)
			set("episodes", lib.ChildCount)
		case lib.SectionType.Equal(py.String("artist")):
			set("artists", lib.Count)
			set("albums", lib.ParentCount)
			set("tracks", lib.ChildCount)
		}
		sectionID := py.String("")
		if lib.SectionID.Exists() {
			sectionID = py.String(lib.SectionID.String())
		}
		points = append(points, collector.Point(measurement, collector.M{
			"type":         py.String("library_stats"),
			"server":       a.server(),
			"section_name": lib.SectionName,
			"section_type": lib.SectionType,
			"section_id":   sectionID,
		}, fields, now))
	}
	return a.write(ctx, points)
}

type responseData struct {
	Data py.Value `json:"data"`
}

// GetServerStatus writes whether Tautulli and Plex are up, their versions and the time since the last play.
func (a *API) GetServerStatus(ctx context.Context) error {
	now := a.d.Now()
	fields := collector.M{}

	// The first call settles the authentication method; the rest run in parallel.
	status, statusOK := a.call(ctx, "server_status")
	fields["tautulli_reachable"] = py.Int(boolInt(statusOK && truthyJSON(status)))
	if statusOK {
		if data, ok := rawData(status); ok {
			var d struct {
				Connected py.Value `json:"connected"`
			}
			if isObject(data) {
				_ = json.Unmarshal(data, &d)
			}
			fields["connected"] = py.Int(boolInt(d.Connected.Truthy()))
		}
	}

	calls := []struct {
		cmd    string
		params []string
	}{
		{"get_server_info", nil},
		{"get_pms_update", nil},
		{"get_tautulli_info", nil},
		{"get_history", []string{"length", "1", "order_column", "date", "order_dir", "desc"}},
	}
	results := make([]json.RawMessage, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if body, ok := a.call(ctx, c.cmd, httpx.P(c.params...)...); ok && truthyJSON(body) {
				results[i], _ = rawData(body)
				if results[i] == nil {
					results[i] = json.RawMessage("null")
				}
			}
		}()
	}
	wg.Wait()

	object := func(raw json.RawMessage) map[string]py.Value {
		out := map[string]py.Value{}
		if isObject(raw) {
			_ = json.Unmarshal(raw, &out)
		}
		return out
	}
	if raw := results[0]; raw != nil {
		d := object(raw)
		fields["plex_name"] = py.String(d["pms_name"].OrString("").String())
		fields["plex_version"] = py.String(d["pms_version"].OrString("").String())
		fields["plex_platform"] = py.String(d["pms_platform"].OrString("").String())
		fields["plexpass"] = py.Int(num(d["pms_plexpass"]))
	}
	if raw := results[1]; raw != nil {
		d := object(raw)
		fields["plex_update_available"] = py.Int(boolInt(d["update_available"].Truthy()))
		fields["plex_update_version"] = py.String(d["version"].OrString("").String())
	}
	if raw := results[2]; raw != nil {
		fields["tautulli_version"] = py.String(object(raw)["tautulli_version"].OrString("").String())
	}
	if raw := results[3]; raw != nil && isObject(raw) {
		var history struct {
			Data py.Objs[map[string]py.Value] `json:"data"`
		}
		_ = json.Unmarshal(raw, &history)
		if len(history.Data) > 0 {
			row := history.Data[0]
			last := num(row["stopped"])
			if last == 0 {
				last = num(row["date"])
			}
			if last != 0 {
				fields["last_play"] = py.String(py.IsoFormat(time.Unix(last, 0).UTC()))
				fields["seconds_since_last_play"] = py.Int(py.SecondsSince(now, float64(last)))
			}
		}
	}
	return a.write(ctx, []influx.Point{collector.Point(measurement,
		collector.M{"type": py.String("Server_Status"), "server": a.server()}, fields, influx.At(now))})
}

func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

func truthyJSON(raw json.RawMessage) bool {
	var v py.Value
	return json.Unmarshal(raw, &v) == nil && v.Truthy()
}
