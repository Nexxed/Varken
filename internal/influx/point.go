// Package influx writes points to InfluxDB 1.x in exactly the line protocol the Python InfluxDB client produced,
// and creates the varken database and retention policy the way Varken always has.
package influx

import (
	"bytes"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	py "github.com/Nexxed/Varken/internal/pycompat"
)

// Point is one InfluxDB point. Tags and fields hold Python-typed values, so that they print exactly as they did.
type Point struct {
	Measurement string
	Tags        map[string]py.Value
	Fields      map[string]py.Value
	Time        Time
}

// Time is a point's timestamp: a time, epoch nanoseconds, or a string parsed at encoding time (as the Python
// client parsed the ISO strings collectors handed it).
type Time struct {
	ns   int64
	text string
	kind uint8 // 0 unset, 1 ns, 2 text
}

// At is a wall-clock time, truncated to microseconds like a Python datetime.
func At(t time.Time) Time {
	ns, err := py.UnixNanoChecked(t)
	if err != nil {
		return Time{kind: 2, text: "out of range"}
	}
	return Time{kind: 1, ns: ns}
}

// AtNS is an exact epoch-nanosecond timestamp.
func AtNS(ns int64) Time { return Time{kind: 1, ns: ns} }

// AtString is a timestamp string from an API, parsed like the Python client did (dateutil, naive is UTC).
func AtString(s string) Time { return Time{kind: 2, text: s} }

// NS resolves the timestamp. ok is false for an unset time.
func (t Time) NS() (ns int64, ok bool, err error) {
	switch t.kind {
	case 1:
		return t.ns, true, nil
	case 2:
		ns, err := py.ParseTimestampNS(t.text)
		return ns, true, err
	}
	return 0, false, nil
}

var tagEscaper = strings.NewReplacer(`\`, `\\`, " ", `\ `, ",", `\,`, "=", `\=`, "\n", `\n`)
var stringEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

// escapeTag is the client's _escape_tag, used for measurements, tag keys, tag values and field keys.
func escapeTag(s string) string { return tagEscaper.Replace(s) }

// fieldValue is the client's _escape_value. ok is false when the field must be left out: None (as Python did) or
// a value that would have made the whole line invalid (a list, a dict, inf or nan).
func fieldValue(v py.Value) (string, bool) {
	switch v.Kind() {
	case py.AbsentKind, py.NullKind:
		return "", false
	case py.StrKind:
		s, _ := v.AsString()
		return `"` + stringEscaper.Replace(s) + `"`, true
	case py.IntKind:
		return v.String() + "i", true
	case py.BoolKind:
		return v.String(), true
	case py.FloatKind:
		f, _ := v.AsFloat()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			return "", false
		}
		return py.FloatRepr(f), true
	}
	return "", false
}

// EncodeError explains why a point was left out of a write.
type EncodeError struct {
	Measurement string
	Reason      string
}

func (e EncodeError) Error() string {
	return fmt.Sprintf("skipped a %s point: %s", e.Measurement, e.Reason)
}

// AppendLine appends the point's line (with a trailing newline) to buf. Points the Python client would have
// turned into an invalid line (no fields left, or an unparseable timestamp), which made InfluxDB reject the whole
// write, are left out with an error instead.
func AppendLine(buf *bytes.Buffer, p Point) error {
	keys := make([]string, 0, len(p.Fields))
	for k := range p.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var fields strings.Builder
	for _, k := range keys {
		key := escapeTag(k)
		value, ok := fieldValue(p.Fields[k])
		if key == "" || !ok {
			continue
		}
		if fields.Len() > 0 {
			fields.WriteByte(',')
		}
		fields.WriteString(key)
		fields.WriteByte('=')
		fields.WriteString(value)
	}
	if fields.Len() == 0 {
		return EncodeError{p.Measurement, "no field values"}
	}
	ns, hasTime, err := p.Time.NS()
	if err != nil {
		return EncodeError{p.Measurement, fmt.Sprintf("invalid timestamp %q", p.Time.text)}
	}

	buf.WriteString(escapeTag(p.Measurement))
	keys = keys[:0]
	for k := range p.Tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		key, value := escapeTag(k), escapeTag(p.Tags[k].TagText())
		if key == "" || value == "" {
			continue
		}
		buf.WriteByte(',')
		buf.WriteString(key)
		buf.WriteByte('=')
		buf.WriteString(value)
	}
	buf.WriteByte(' ')
	buf.WriteString(fields.String())
	if hasTime {
		buf.WriteByte(' ')
		buf.WriteString(strconv.FormatInt(ns, 10))
	}
	buf.WriteByte('\n')
	return nil
}

// Encode returns the line protocol for points, in order, plus an error for each point left out.
func Encode(points []Point) ([]byte, []error) {
	var buf bytes.Buffer
	var errs []error
	for _, p := range points {
		if err := AppendLine(&buf, p); err != nil {
			errs = append(errs, err)
		}
	}
	return buf.Bytes(), errs
}
