package arr

import (
	"bytes"
	"encoding/json"

	"github.com/Nexxed/Varken/internal/httpx"
	py "github.com/Nexxed/Varken/internal/pycompat"
)

// Nested is a JSON value the Python code unpacked with Structure(**(value or {})): a falsy value reads as an empty
// object, an object is decoded into T, and any other value made Python raise TypeError (Invalid).
type Nested[T any] struct {
	V     T
	kind  uint8 // 0 falsy or missing, 1 object, 2 truthy non-object
	empty bool  // an empty object, which is falsy
}

// UnmarshalJSON implements json.Unmarshaler.
func (n *Nested[T]) UnmarshalJSON(data []byte) error {
	*n = Nested[T]{}
	data = bytes.TrimSpace(data)
	if len(data) > 0 && data[0] == '{' {
		_ = json.Unmarshal(data, &n.V)
		n.kind = 1
		rest := bytes.TrimSpace(data[1:])
		n.empty = len(rest) > 0 && rest[0] == '}'
		return nil
	}
	var v py.Value
	if err := v.UnmarshalJSON(data); err == nil && v.Truthy() {
		n.kind = 2
	}
	return nil
}

// IsObject reports a JSON object.
func (n Nested[T]) IsObject() bool { return n.kind == 1 }

// Truthy is Python's bool() of the value.
func (n Nested[T]) Truthy() bool { return (n.kind == 1 && !n.empty) || n.kind == 2 }

// Invalid reports a truthy value that is not an object.
func (n Nested[T]) Invalid() bool { return n.kind == 2 }

type statusMessage struct {
	Title    py.Value          `json:"title"`
	Messages py.List[py.Value] `json:"messages"`
}

// SeriesRef is the part of a Sonarr series the collectors read.
type SeriesRef struct {
	Title py.Value `json:"title"`
}

// EpisodeRef is the part of a Sonarr episode the collectors read.
type EpisodeRef struct {
	SeasonNumber  py.Value `json:"seasonNumber"`
	EpisodeNumber py.Value `json:"episodeNumber"`
	Title         py.Value `json:"title"`
}

// MovieRef is the part of a Radarr movie the collectors read.
type MovieRef struct {
	Title     py.Value `json:"title"`
	Year      py.Value `json:"year"`
	TmdbID    py.Value `json:"tmdbId"`
	TitleSlug py.Value `json:"titleSlug"`
}

// QueueItem is one record of /queue for any of the three services.
type QueueItem struct {
	ID                    py.Value               `json:"id"`
	Title                 py.Value               `json:"title"`
	Size                  py.Value               `json:"size"`
	Sizeleft              py.Value               `json:"sizeleft"`
	Status                py.Value               `json:"status"`
	TrackedDownloadState  py.Value               `json:"trackedDownloadState"`
	TrackedDownloadStatus py.Value               `json:"trackedDownloadStatus"`
	StatusMessages        py.Objs[statusMessage] `json:"statusMessages"`
	ErrorMessage          py.Value               `json:"errorMessage"`
	DownloadClient        py.Value               `json:"downloadClient"`
	Indexer               py.Value               `json:"indexer"`
	Timeleft              py.Value               `json:"timeleft"`
	Protocol              py.Value               `json:"protocol"`
	Quality               py.Obj[quality]        `json:"quality"`
	SeriesID              py.Value               `json:"seriesId"`
	ArtistID              py.Value               `json:"artistId"`
	Series                Nested[SeriesRef]      `json:"series"`
	Episode               Nested[EpisodeRef]     `json:"episode"`
	Movie                 Nested[MovieRef]       `json:"movie"`
}

// Page is a paged *arr response.
type Page[T any] struct {
	Page         py.Value `json:"page"`
	PageSize     py.Value `json:"pageSize"`
	TotalRecords py.Value `json:"totalRecords"`
	// RecordsList and RecordsTruthy describe "records" itself: Python extended its list with it (failing on
	// None) and stopped paging when it was empty.
	RecordsList   bool
	RecordsTruthy bool
	Items         py.Objs[T]
}

// UnmarshalJSON keeps both the typed records and whether "records" was a list.
func (p *Page[T]) UnmarshalJSON(data []byte) error {
	var raw struct {
		Page         py.Value        `json:"page"`
		PageSize     py.Value        `json:"pageSize"`
		TotalRecords py.Value        `json:"totalRecords"`
		Records      json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	p.Page, p.PageSize, p.TotalRecords = raw.Page, raw.PageSize, raw.TotalRecords
	p.RecordsList, p.RecordsTruthy, p.Items = false, false, nil
	if raw.Records != nil {
		p.RecordsList = IsList(raw.Records)
		p.RecordsTruthy = httpx.Truthy(raw.Records)
		_ = p.Items.UnmarshalJSON(raw.Records)
	}
	return nil
}

// isJSON reports whether a body is a JSON array ('[') or object ('{').
func isJSON(body []byte, open byte) bool {
	body = bytes.TrimSpace(body)
	return len(body) > 0 && body[0] == open
}

// IsList reports a JSON array body (Python's isinstance(x, list)).
func IsList(body []byte) bool { return isJSON(body, '[') }

// IsDict reports a JSON object body (Python's isinstance(x, dict)).
func IsDict(body []byte) bool { return isJSON(body, '{') }
