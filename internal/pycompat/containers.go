package pycompat

import (
	"bytes"
	"encoding/json"
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

// Ptr returns the object or nil.
func (o Obj[T]) Ptr() *T { return o.ptr }

// NewObj wraps a value, for tests and constructed data.
func NewObj[T any](t T) Obj[T] { return Obj[T]{ptr: &t} }

// List is a JSON array of any element type. It is empty when the value is not an array. Elements that fail to
// decode as T are kept as a zero T, so positions still line up (Python's zip over parallel lists).
type List[T any] []T

// UnmarshalJSON implements json.Unmarshaler.
func (l *List[T]) UnmarshalJSON(data []byte) error {
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
func Decode[T any](body []byte) (T, error) {
	var t T
	err := json.Unmarshal(body, &t)
	return t, err
}
