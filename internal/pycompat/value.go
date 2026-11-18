// Package pycompat reproduces the Python behaviours that Varken's stored data depends on: how JSON values keep
// their Python types, how str(), repr() and truthiness print and test them, and the string, number and time
// conversions the Python collectors used. Matching these exactly keeps tag values, field types and hashes identical
// to what the Python version wrote, so existing InfluxDB series and Grafana dashboards keep working.
package pycompat

import (
	"bytes"
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Kind is the Python type a Value holds.
type Kind uint8

const (
	// Absent is a key that was not in the JSON object. dict.get() returns None for it, but dict.get(k, default)
	// returns the default, so it is kept apart from Null.
	AbsentKind Kind = iota
	NullKind
	BoolKind
	IntKind
	FloatKind
	StrKind
	ListKind
	DictKind
)

// Value is a JSON scalar (or container) with the Python type json.loads would have given it.
// The zero Value is Absent.
type Value struct {
	kind Kind
	b    bool
	i    int64
	f    float64
	s    string // Str: the text. Int: the decimal digits when the value does not fit in int64. List/Dict: raw JSON.
	n    int    // List/Dict: len()
}

// None is Python's None.
var None = Value{kind: NullKind}

// String returns a Python str. The constructor shares its name with the Stringer method on purpose.
func String(s string) Value { return Value{kind: StrKind, s: s} }

// Int returns a Python int.
func Int(i int64) Value { return Value{kind: IntKind, i: i} }

// Float returns a Python float.
func Float(f float64) Value { return Value{kind: FloatKind, f: f} }

// Bool returns a Python bool.
func Bool(b bool) Value { return Value{kind: BoolKind, b: b} }

// Kind reports the Python type.
func (v Value) Kind() Kind { return v.kind }

// IsNone reports whether the value reads as None (missing or null).
func (v Value) IsNone() bool { return v.kind == AbsentKind || v.kind == NullKind }

// Exists reports whether the key was present in the JSON object (even if null).
func (v Value) Exists() bool { return v.kind != AbsentKind }

// IsBig reports an int too large for int64.
func (v Value) IsBig() bool { return v.kind == IntKind && v.s != "" }

// Truthy is Python's bool(value).
func (v Value) Truthy() bool {
	switch v.kind {
	case BoolKind:
		return v.b
	case IntKind:
		return v.i != 0 || v.s != ""
	case FloatKind:
		return v.f != 0
	case StrKind:
		return v.s != ""
	case ListKind, DictKind:
		return v.n > 0
	}
	return false
}

// Or is Python's `value or alt`.
func (v Value) Or(alt Value) Value {
	if v.Truthy() {
		return v
	}
	return alt
}

// OrString is `value or alt` for a string fallback.
func (v Value) OrString(alt string) Value { return v.Or(String(alt)) }

// Get is dict.get(key, def): the default only when the key is missing, not when it is null.
func (v Value) Get(def Value) Value {
	if v.kind == AbsentKind {
		return def
	}
	return v
}

// Len is len() of a str, list or dict, and ok is false for other types.
func (v Value) Len() (int, bool) {
	switch v.kind {
	case StrKind:
		return len([]rune(v.s)), true
	case ListKind, DictKind:
		return v.n, true
	}
	return 0, false
}

// AsString returns the text of a str value.
func (v Value) AsString() (string, bool) { return v.s, v.kind == StrKind }

// AsBool returns the value of a bool.
func (v Value) AsBool() (bool, bool) { return v.b, v.kind == BoolKind }

// AsInt returns the value of an int that fits in int64 (bools are not ints here).
func (v Value) AsInt() (int64, bool) { return v.i, v.kind == IntKind && v.s == "" }

// AsFloat returns the value of a float.
func (v Value) AsFloat() (float64, bool) { return v.f, v.kind == FloatKind }

// Raw returns the JSON text of a list or dict value.
func (v Value) Raw() string {
	if v.kind == ListKind || v.kind == DictKind {
		return v.s
	}
	return ""
}

// String is Python's str(value). A missing key prints as None, like dict.get() would return.
func (v Value) String() string {
	switch v.kind {
	case AbsentKind, NullKind:
		return "None"
	case BoolKind:
		if v.b {
			return "True"
		}
		return "False"
	case IntKind:
		if v.s != "" {
			return v.s
		}
		return strconv.FormatInt(v.i, 10)
	case FloatKind:
		return FloatRepr(v.f)
	case StrKind:
		return v.s
	case ListKind, DictKind:
		return reprJSON(v.s)
	}
	return ""
}

// Repr is Python's repr(value).
func (v Value) Repr() string {
	if v.kind == StrKind {
		return ReprString(v.s)
	}
	return v.String()
}

// TagText is what the InfluxDB client writes for a tag value: str(value), except None becomes the empty string,
// which drops the tag.
func (v Value) TagText() string {
	if v.IsNone() {
		return ""
	}
	return v.String()
}

// Equal compares type and value. Python's 1 == 1.0 == True is deliberately not followed: different types print
// differently.
func (v Value) Equal(o Value) bool {
	if v.kind != o.kind {
		return false
	}
	switch v.kind {
	case BoolKind:
		return v.b == o.b
	case IntKind:
		return v.i == o.i && v.s == o.s
	case FloatKind:
		return v.f == o.f || (math.IsNaN(v.f) && math.IsNaN(o.f))
	case StrKind, ListKind, DictKind:
		return v.s == o.s
	}
	return true
}

// UnmarshalJSON keeps the Python type json.loads would produce: numbers with a fraction or exponent are floats,
// all others ints (of any size).
func (v *Value) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		*v = Value{kind: NullKind}
		return nil
	}
	switch data[0] {
	case 'n':
		*v = Value{kind: NullKind}
	case 't':
		*v = Value{kind: BoolKind, b: true}
	case 'f':
		*v = Value{kind: BoolKind}
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		*v = Value{kind: StrKind, s: s}
	case '[':
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		*v = Value{kind: ListKind, s: string(data), n: len(items)}
	case '{':
		var items map[string]json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		*v = Value{kind: DictKind, s: string(data), n: len(items)}
	default:
		*v = parseJSONNumber(string(data))
	}
	return nil
}

func parseJSONNumber(text string) Value {
	if strings.ContainsAny(text, ".eE") {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !isRangeErr(err) {
			return Value{kind: NullKind}
		}
		return Value{kind: FloatKind, f: f}
	}
	i, err := strconv.ParseInt(text, 10, 64)
	if err == nil {
		return Value{kind: IntKind, i: i}
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		return Value{kind: NullKind}
	}
	sat := int64(math.MaxInt64)
	if n.Sign() < 0 {
		sat = math.MinInt64
	}
	return Value{kind: IntKind, i: sat, s: n.String()}
}

func isRangeErr(err error) bool {
	numErr, ok := err.(*strconv.NumError)
	return ok && numErr.Err == strconv.ErrRange
}

// MarshalJSON writes the value back as JSON (missing becomes null).
func (v Value) MarshalJSON() ([]byte, error) {
	switch v.kind {
	case AbsentKind, NullKind:
		return []byte("null"), nil
	case BoolKind:
		return []byte(strconv.FormatBool(v.b)), nil
	case IntKind:
		return []byte(v.String()), nil
	case FloatKind:
		if math.IsInf(v.f, 0) || math.IsNaN(v.f) {
			return json.Marshal(FloatRepr(v.f))
		}
		text := FloatRepr(v.f)
		return []byte(text), nil
	case StrKind:
		return json.Marshal(v.s)
	}
	return []byte(v.s), nil
}

// FromAny converts a value decoded by encoding/json (with UseNumber) or a Go scalar to a Value.
func FromAny(x any) Value {
	switch t := x.(type) {
	case nil:
		return None
	case Value:
		return t
	case bool:
		return Bool(t)
	case string:
		return String(t)
	case json.Number:
		return parseJSONNumber(string(t))
	case int:
		return Int(int64(t))
	case int64:
		return Int(t)
	case float64:
		return Float(t)
	default:
		raw, err := json.Marshal(t)
		if err != nil {
			return None
		}
		var v Value
		if err := v.UnmarshalJSON(raw); err != nil {
			return None
		}
		return v
	}
}
