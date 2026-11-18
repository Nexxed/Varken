package radarr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nexxed/Varken/internal/collector"
	"github.com/Nexxed/Varken/internal/config"
	"github.com/Nexxed/Varken/internal/influx"
)

// libraryBody is a /api/v3/movie response shaped like a real Radarr v5 library of n movies (about 2.8 KB each).
func libraryBody(n int) []byte {
	qualities := []struct {
		name string
		res  int
	}{{"Bluray-1080p", 1080}, {"WEBDL-2160p", 2160}, {"HDTV-720p", 720}, {"DVD", 480}}
	movies := make([]map[string]any, n)
	for i := range movies {
		q := qualities[i%len(qualities)]
		alt := make([]map[string]any, 6)
		for k := range alt {
			alt[k] = map[string]any{"title": fmt.Sprintf("Alt %d %d", i, k), "sourceType": "tmdb"}
		}
		m := map[string]any{
			"id": i + 1, "title": fmt.Sprintf("Movie %d", i), "year": 1990 + i%35, "tmdbId": 1000 + i,
			"titleSlug": fmt.Sprintf("movie-%d", i), "monitored": i%7 != 0, "hasFile": i%5 != 0,
			"sizeOnDisk": int64(1e9) + int64(i)*7919, "overview": strings.Repeat("x", 1500),
			"images": []any{map[string]any{"coverType": "poster", "url": fmt.Sprintf("/MediaCover/%d/poster.jpg", i),
				"remoteUrl": "https://image.tmdb.org/t/p/original/abc.jpg"}},
			"genres": []string{"Action", "Drama"}, "alternateTitles": alt,
			"ratings":   map[string]any{"imdb": map[string]any{"votes": 1000, "value": 7.1}},
			"movieFile": nil,
		}
		if i%5 != 0 {
			m["movieFile"] = map[string]any{"id": i, "size": int64(1e9) + int64(i), "relativePath": fmt.Sprintf("Movie %d.mkv", i),
				"quality": map[string]any{"quality": map[string]any{"id": 7, "name": q.name, "source": "bluray",
					"resolution": q.res}, "revision": map[string]any{"version": 1}},
				"mediaInfo": map[string]any{"videoDynamicRangeType": []string{"", "HDR10", "DV"}[i%3],
					"videoCodec": "x265", "audioChannels": 5.1, "subtitles": "English / French"}}
		}
		movies[i] = m
	}
	data, _ := json.Marshal(movies)
	return data
}

type discard struct{}

func (discard) Write(context.Context, []influx.Point) error { return nil }

// BenchmarkRadarrLibrary is the largest response Varken reads: a whole Radarr library.
func BenchmarkRadarrLibrary(b *testing.B) {
	body := libraryBody(2500)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	defer srv.Close()
	var s config.RadarrServer
	s.ID, s.URL, s.APIKey = 1, srv.URL, "k"
	api := New(s, collector.Deps{Writer: discard{}, Now: time.Now})
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	for b.Loop() {
		if err := api.Library(context.Background()); err != nil {
			b.Fatal(err)
		}
	}
}
