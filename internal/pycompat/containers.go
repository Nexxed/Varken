package pycompat

import (
	"bytes"
	"encoding/json"
	"errors"
)

// API responses are decoded into structs whose fields are only Value, Obj, List, Objs and Map. None of these
// ever fail to decode, whatever type the upstream API sends, so a field that changes type in a new release can
// no longer drop a whole response the way it did in the Python version.

// Obj is a JSON object decoded into T. It is empty when the value is missing, null or not an object.
type Obj[T any] struct{ ptr *T }

// UnmarshalJSON implements json.Unmarshaler.
func (o *Obj[T]) UnmarshalJSON(data []byte) error {
	o.ptr = nil
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	var t T
	if err := json.Unmarshal(data, &t); err != nil {
		return nil //nolint:nilerr // a malformed nested object reads as absent instead of failing the response
	}
	o.ptr = &t
	return nil
}

// OK reports whether an object was present.
func (o Obj[T]) OK() bool { return o.ptr != nil }

// V returns the object, or a zero T when it was absent: Python's `(value or {})`.
func (o Obj[T]) V() T {
	if o.ptr == nil {
		var zero T
		return zero
	}
	return *o.ptr
}

// List is a JSON array of any element type. It is empty when the value is not an array. Elements that fail to
// decode as T are kept as a zero T, so positions still line up (Python's zip over parallel lists).
type List[T any] []T

// UnmarshalJSON implements json.Unmarshaler.
func (l *List[T]) UnmarshalJSON(data []byte) error {
	*l = nil
	// Fast path: one decoding pass. It fails only when an element has the wrong type for T; the slow path then
	// decodes element by element, keeping positions.
	var fast []T
	if err := json.Unmarshal(data, &fast); err == nil && isArray(data) {
		*l = fast
		return nil
	}
	return l.slow(data)
}

func (l *List[T]) slow(data []byte) error {
	*l = nil
	items, ok := rawItems(data)
	if !ok {
		return nil
	}
	out := make(List[T], len(items))
	for i, raw := range items {
		_ = json.Unmarshal(raw, &out[i])
	}
	*l = out
	return nil
}

// Objs is a JSON array of objects. Elements that are not objects (null, strings, numbers) are skipped, where
// the Python version either skipped them or crashed the whole job.
type Objs[T any] []T

// UnmarshalJSON implements json.Unmarshaler.
func (l *Objs[T]) UnmarshalJSON(data []byte) error {
	*l = nil
	// Fast path: one decoding pass when every element is an object or null (null decodes to nil and is skipped).
	// Any other element makes it fail, and the slow path skips those elements one by one.
	var fast []*T
	if err := json.Unmarshal(data, &fast); err == nil && isArray(data) {
		l.keep(fast)
		return nil
	}
	return l.slow(data)
}

// keep stores the decoded objects, skipping nulls.
func (l *Objs[T]) keep(items []*T) {
	out := make(Objs[T], 0, len(items))
	for _, t := range items {
		if t != nil {
			out = append(out, *t)
		}
	}
	*l = out
}

func (l *Objs[T]) slow(data []byte) error {
	*l = nil
	items, ok := rawItems(data)
	if !ok {
		return nil
	}
	out := make(Objs[T], 0, len(items))
	for _, raw := range items {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 || raw[0] != '{' {
			continue
		}
		var t T
		if err := json.Unmarshal(raw, &t); err != nil {
			continue
		}
		out = append(out, t)
	}
	*l = out
	return nil
}

func isArray(data []byte) bool {
	data = bytes.TrimSpace(data)
	return len(data) > 0 && data[0] == '['
}

func rawItems(data []byte) ([]json.RawMessage, bool) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '[' {
		return nil, false
	}
	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return nil, false
	}
	return items, true
}

// Map is a JSON object with arbitrary keys, kept in document order like a Python dict. Duplicate keys keep the
// first position and the last value, as json.loads does. It is empty when the value is not an object.
type Map[V any] struct {
	Keys   []string
	Values []V
}

// Len is the number of keys.
func (m Map[V]) Len() int { return len(m.Keys) }

// Lookup returns the value for key.
func (m Map[V]) Lookup(key string) (V, bool) {
	for i, k := range m.Keys {
		if k == key {
			return m.Values[i], true
		}
	}
	var zero V
	return zero, false
}

// Set adds or replaces a key, keeping first-insertion order.
func (m *Map[V]) Set(key string, value V) {
	for i, k := range m.Keys {
		if k == key {
			m.Values[i] = value
			return
		}
	}
	m.Keys = append(m.Keys, key)
	m.Values = append(m.Values, value)
}

// UnmarshalJSON implements json.Unmarshaler.
func (m *Map[V]) UnmarshalJSON(data []byte) error {
	*m = Map[V]{}
	data = bytes.TrimSpace(data)
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if _, err := dec.Token(); err != nil {
		return nil //nolint:nilerr
	}
	index := map[string]int{}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			*m = Map[V]{}
			return nil //nolint:nilerr
		}
		key, _ := tok.(string)
		var raw json.RawMessage
		if err := dec.Decode(&raw); err != nil {
			*m = Map[V]{}
			return nil //nolint:nilerr
		}
		var value V
		_ = json.Unmarshal(raw, &value)
		if i, ok := index[key]; ok {
			m.Values[i] = value
			continue
		}
		index[key] = len(m.Keys)
		m.Keys = append(m.Keys, key)
		m.Values = append(m.Values, value)
	}
	return nil
}

// Decode unmarshals an API response body into T. It only fails when the body is not JSON at all.
//
// A top-level List or Objs decodes the body directly: going through json.Unmarshal would scan the whole body
// once to find the value and once more to decode it.
func Decode[T any](body []byte) (T, error) {
	var t T
	if top, ok := any(&t).(topLevel); ok {
		err := top.decodeTop(body)
		return t, err
	}
	err := json.Unmarshal(body, &t)
	return t, err
}

type topLevel interface{ decodeTop([]byte) error }

var errNotJSON = errors.New("response is not valid JSON")

func (l *List[T]) decodeTop(body []byte) error {
	var fast []T
	if err := json.Unmarshal(body, &fast); err == nil {
		if isArray(body) {
			*l = fast
		}
		return nil
	}
	if !json.Valid(body) {
		return errNotJSON
	}
	return l.slow(body)
}

func (l *Objs[T]) decodeTop(body []byte) error {
	if !isArray(body) {
		if !json.Valid(body) {
			return errNotJSON
		}
		*l = nil
		return nil
	}
	var fast []*T
	if err := json.Unmarshal(body, &fast); err == nil {
		l.keep(fast)
		return nil
	}
	if !json.Valid(body) {
		return errNotJSON
	}
	return l.slow(body)
}
