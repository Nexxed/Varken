package arr

import (
	"context"
	"fmt"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/influx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// Library is get_library: library size and composition from /series, /movie or /artist.
func (b *Base) Library(ctx context.Context) error {
	now := collector.NowTime(b.Deps)
	var payload []influx.Point
	var ok bool
	switch b.Measurement {
	case "Sonarr":
		payload, ok = b.sonarrLibrary(ctx, now)
	case "Radarr":
		payload, ok = b.radarrLibrary(ctx, now)
	case "Lidarr":
		payload, ok = b.lidarrLibrary(ctx, now)
	}
	if !ok {
		return nil
	}
	return b.Write(ctx, payload, "library")
}

// ResolutionName is arr_library.resolution_name: "2160p", or "Unknown" when absent or zero.
func ResolutionName(v py.Value) string {
	if n := py.ToInt(v, 0); n > 0 {
		return fmt.Sprintf("%dp", n)
	}
	return "Unknown"
}

func (b *Base) sonarrLibrary(ctx context.Context, now influx.Time) ([]influx.Point, bool) {
	body, ok := b.Get(ctx, "/series", nil)
	if !ok || !IsList(body) {
		return nil, false
	}
	seriesList, err := py.Decode[py.Objs[struct {
		ID         py.Value `json:"id"`
		Title      py.Value `json:"title"`
		Status     py.Value `json:"status"`
		Monitored  py.Value `json:"monitored"`
		Statistics py.Obj[struct {
			EpisodeCount      py.Value `json:"episodeCount"`
			EpisodeFileCount  py.Value `json:"episodeFileCount"`
			TotalEpisodeCount py.Value `json:"totalEpisodeCount"`
			SizeOnDisk        py.Value `json:"sizeOnDisk"`
			PercentOfEpisodes py.Value `json:"percentOfEpisodes"`
		}] `json:"statistics"`
	}]](body)
	if err != nil {
		return nil, false
	}
	var series, continuing, ended, complete, episodes, total, files, size int64
	payload := make([]influx.Point, 0, len(seriesList)+1)
	for _, s := range seriesList {
		stats := s.Statistics.V()
		status := py.Lower(s.Status.OrString("").String())
		episodeCount := py.ToInt(stats.EpisodeCount, 0)
		fileCount := py.ToInt(stats.EpisodeFileCount, 0)
		sizeOnDisk := py.ToInt(stats.SizeOnDisk, 0)

		series++
		switch status {
		case "continuing":
			continuing++
		case "ended":
			ended++
		}
		if episodeCount > 0 && fileCount >= episodeCount {
			complete++
		}
		episodes += episodeCount
		total += py.ToInt(stats.TotalEpisodeCount, 0)
		files += fileCount
		size += sizeOnDisk

		if s.ID.IsNone() {
			continue
		}
		percent := 0.0
		if f, err := py.ToFloat(stats.PercentOfEpisodes.Or(py.Int(0))); err == nil {
			percent = py.Round(f, 2)
		}
		payload = append(payload, b.Point("Series", collector.M{
			"title":         py.String(s.Title.OrString("").String()),
			"size_bytes":    py.Int(sizeOnDisk),
			"episodes":      py.Int(episodeCount),
			"episode_files": py.Int(fileCount),
			"percent":       py.Float(percent),
			"status":        py.String(status),
			"monitored":     py.Int(boolInt(s.Monitored.Truthy())),
		}, now, collector.M{"series_id": py.String(s.ID.String())}))
	}
	payload = append(payload, b.Point("Library", collector.M{
		"series": py.Int(series), "series_continuing": py.Int(continuing), "series_ended": py.Int(ended),
		"series_complete": py.Int(complete), "episodes": py.Int(episodes), "episodes_total": py.Int(total),
		"episode_files": py.Int(files), "size_bytes": py.Int(size),
	}, now, nil))
	return payload, true
}

// bucket counts files (and their size) per quality, resolution or dynamic range, in first-seen order.
type bucket struct {
	keys   []string
	names  map[string]py.Value
	counts map[string]int64
	sizes  map[string]int64
}

func newBucket() *bucket {
	return &bucket{names: map[string]py.Value{}, counts: map[string]int64{}, sizes: map[string]int64{}}
}

func (bk *bucket) add(name py.Value, size int64) {
	key := valueKey(name)
	if _, ok := bk.names[key]; !ok {
		bk.keys = append(bk.keys, key)
		bk.names[key] = name
	}
	bk.counts[key]++
	bk.sizes[key] += size
}

func (b *Base) radarrLibrary(ctx context.Context, now influx.Time) ([]influx.Point, bool) {
	body, ok := b.Get(ctx, "/movie", nil)
	if !ok || !IsList(body) {
		return nil, false
	}
	movies, err := py.Decode[py.Objs[struct {
		Monitored  py.Value `json:"monitored"`
		SizeOnDisk py.Value `json:"sizeOnDisk"`
		File       Nested[struct {
			Size      py.Value        `json:"size"`
			Quality   py.Obj[quality] `json:"quality"`
			MediaInfo py.Obj[struct {
				VideoDynamicRangeType py.Value `json:"videoDynamicRangeType"`
			}] `json:"mediaInfo"`
		}] `json:"movieFile"`
	}]](body)
	if err != nil {
		return nil, false
	}
	var monitored, withFile, size int64
	qualities, resolutions, ranges := newBucket(), newBucket(), newBucket()
	for _, m := range movies {
		if m.Monitored.Truthy() {
			monitored++
		}
		movieSize := py.ToInt(m.SizeOnDisk, 0)
		size += movieSize
		if !m.File.IsObject() || !m.File.Truthy() {
			// No file (null, missing or {}). A movieFile that is not an object made Python raise; it is
			// skipped too.
			continue
		}
		file := m.File.V
		withFile++
		fileSize := movieSize
		if n, err := py.IntOfFloat(file.Size); err == nil {
			fileSize = n
		}
		q := py.Obj[struct {
			Name       py.Value `json:"name"`
			Resolution py.Value `json:"resolution"`
		}]{}
		if file.Quality.OK() {
			q = file.Quality.V().Quality
		}
		name := q.V().Name.OrString("Unknown")
		qualities.add(name, fileSize)
		resolutions.add(py.String(ResolutionName(q.V().Resolution)), fileSize)
		ranges.add(file.MediaInfo.V().VideoDynamicRangeType.OrString("SDR"), 0)
	}
	payload := []influx.Point{b.Point("Library", collector.M{
		"movies": py.Int(int64(len(movies))), "monitored": py.Int(monitored), "with_file": py.Int(withFile),
		"size_bytes": py.Int(size),
	}, now, nil)}
	for _, key := range qualities.keys {
		payload = append(payload, b.Point("Library_Quality", collector.M{
			"count": py.Int(qualities.counts[key]), "size_bytes": py.Int(qualities.sizes[key]),
		}, now, collector.M{"quality": qualities.names[key]}))
	}
	for _, key := range resolutions.keys {
		payload = append(payload, b.Point("Library_Resolution", collector.M{
			"count": py.Int(resolutions.counts[key]), "size_bytes": py.Int(resolutions.sizes[key]),
		}, now, collector.M{"resolution": resolutions.names[key]}))
	}
	for _, key := range ranges.keys {
		payload = append(payload, b.Point("Library_Dynamic_Range", collector.M{
			"count": py.Int(ranges.counts[key]),
		}, now, collector.M{"dynamic_range": ranges.names[key]}))
	}
	return payload, true
}

func (b *Base) lidarrLibrary(ctx context.Context, now influx.Time) ([]influx.Point, bool) {
	body, ok := b.Get(ctx, "/artist", nil)
	if !ok || !IsList(body) {
		return nil, false
	}
	artists, err := py.Decode[py.Objs[struct {
		Statistics py.Obj[struct {
			AlbumCount      py.Value `json:"albumCount"`
			TotalTrackCount py.Value `json:"totalTrackCount"`
			TrackFileCount  py.Value `json:"trackFileCount"`
			SizeOnDisk      py.Value `json:"sizeOnDisk"`
		}] `json:"statistics"`
	}]](body)
	if err != nil {
		return nil, false
	}
	var albums, tracks, trackFiles, size int64
	for _, a := range artists {
		stats := a.Statistics.V()
		albums += py.ToInt(stats.AlbumCount, 0)
		tracks += py.ToInt(stats.TotalTrackCount, 0)
		trackFiles += py.ToInt(stats.TrackFileCount, 0)
		size += py.ToInt(stats.SizeOnDisk, 0)
	}
	return []influx.Point{b.Point("Library", collector.M{
		"artists": py.Int(int64(len(artists))), "albums": py.Int(albums), "tracks": py.Int(tracks),
		"track_files": py.Int(trackFiles), "size_bytes": py.Int(size),
	}, now, nil)}, true
}
