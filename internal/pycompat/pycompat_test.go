package pycompat

import (
	"bufio"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// The corpora in testdata/pycompat were produced by Python (tools/pygolden/pycompat_gen.py).

func readJSONL(t *testing.T, name string, each func(line int, row map[string]json.RawMessage)) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "testdata", "pycompat", name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1<<20), 1<<20)
	n := 0
	for scanner.Scan() {
		n++
		var row map[string]json.RawMessage
		if err := json.Unmarshal(scanner.Bytes(), &row); err != nil {
			t.Fatalf("%s:%d: %v", name, n, err)
		}
		each(n, row)
	}
	if n == 0 {
		t.Fatalf("%s is empty", name)
	}
}

func str(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("not a string: %s", raw)
	}
	return s
}

// pyErr returns the Python exception name of an {"error": "..."} cell, or "".
func pyErr(raw json.RawMessage) string {
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil {
		return e.Error
	}
	return ""
}

func TestFloatReprAndRoundMatchPython(t *testing.T) {
	readJSONL(t, "floats.jsonl", func(line int, row map[string]json.RawMessage) {
		bits, err := strconv.ParseUint(str(t, row["bits"]), 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		f := math.Float64frombits(bits)
		if got, want := FloatRepr(f), str(t, row["repr"]); got != want {
			t.Errorf("line %d: FloatRepr(%v) = %q, want %q", line, f, got, want)
		}
		for key, n := range map[string]int{"round0": 0, "round2": 2, "round3": 3} {
			if got, want := FloatRepr(Round(f, n)), str(t, row[key]); got != want {
				t.Errorf("line %d: round(%s, %d) = %q, want %q", line, FloatRepr(f), n, got, want)
			}
		}
	})
}

func TestCaseMappingMatchesPython(t *testing.T) {
	readJSONL(t, "strings.jsonl", func(line int, row map[string]json.RawMessage) {
		s := str(t, row["s"])
		checks := map[string]func(string) string{"upper": Upper, "lower": Lower, "title": Title,
			"md5": Hash, "trunc3": func(s string) string { return Truncate(s, 3) }}
		for key, fn := range checks {
			if got, want := fn(s), str(t, row[key]); got != want {
				t.Errorf("line %d: %s(%q) = %q, want %q", line, key, s, got, want)
			}
		}
	})
}

func TestNumberParsingMatchesPython(t *testing.T) {
	readJSONL(t, "numbers.jsonl", func(line int, row map[string]json.RawMessage) {
		s := str(t, row["s"])
		f, err := ParseFloat(s)
		if want := pyErr(row["float"]); want != "" {
			if err == nil {
				t.Errorf("float(%q) = %v, want %s", s, f, want)
			}
		} else if err != nil || FloatRepr(f) != str(t, row["float"]) {
			t.Errorf("float(%q) = %v, %v; want %s", s, FloatRepr(f), err, row["float"])
		}

		n, err := IntOfFloat(String(s))
		if want := pyErr(row["int_float"]); want != "" {
			if err == nil || err.Error() != want {
				t.Errorf("int(float(%q)) = %d, %v; want %s", s, n, err, want)
			}
		} else {
			var wantN json.Number
			_ = json.Unmarshal(row["int_float"], &wantN)
			if w, convErr := wantN.Int64(); convErr == nil && (err != nil || n != w) {
				t.Errorf("int(float(%q)) = %d, %v; want %d", s, n, err, w)
			}
		}

		i, err := ParseInt(s)
		if want := pyErr(row["int"]); want != "" {
			if err == nil {
				t.Errorf("int(%q) = %d, want %s", s, i, want)
			}
		} else {
			var wantN json.Number
			_ = json.Unmarshal(row["int"], &wantN)
			if w, convErr := wantN.Int64(); convErr == nil && (err != nil || i != w) {
				t.Errorf("int(%q) = %d, %v; want %d", s, i, err, w)
			}
		}
	})
}

func TestTimestampsMatchInfluxClient(t *testing.T) {
	// Python accepted a few forms through dateutil's guessing that are deliberately rejected here; they never
	// come from Varken or the APIs it reads.
	unsupported := map[string]bool{}
	readJSONL(t, "timestamps.jsonl", func(line int, row map[string]json.RawMessage) {
		s := str(t, row["s"])
		ns, err := ParseTimestampNS(s)
		if want := pyErr(row["ns"]); want != "" {
			if err == nil {
				t.Errorf("%q parsed to %d, Python raised %s", s, ns, want)
			}
			return
		}
		want := str(t, row["ns"])
		if unsupported[s] {
			return
		}
		w, convErr := strconv.ParseInt(want, 10, 64)
		if convErr != nil {
			if err == nil {
				t.Errorf("%q: got %d, want out-of-range error (Python: %s)", s, ns, want)
			}
			return
		}
		if err != nil || ns != w {
			t.Errorf("%q = %d, %v; want %d", s, ns, err, w)
		}
	})
}

func TestFormatPadMatchesPython(t *testing.T) {
	readJSONL(t, "pad.jsonl", func(line int, row map[string]json.RawMessage) {
		var v Value
		if err := json.Unmarshal(row["v"], &v); err != nil {
			t.Fatal(err)
		}
		got, err := FormatPad(v, 2)
		if want := pyErr(row["pad2"]); want != "" {
			if err == nil {
				t.Errorf("format(%s, '0>2') = %q, want %s", v.Repr(), got, want)
			}
			return
		}
		if want := str(t, row["pad2"]); err != nil || got != want {
			t.Errorf("format(%s, '0>2') = %q, %v; want %q", v.Repr(), got, err, want)
		}
	})
}

func TestValueKeepsPythonTypes(t *testing.T) {
	cases := []struct {
		json, str, tag string
		kind           Kind
		truthy         bool
	}{
		{`null`, "None", "", NullKind, false},
		{`true`, "True", "True", BoolKind, true},
		{`false`, "False", "False", BoolKind, false},
		{`0`, "0", "0", IntKind, false},
		{`50`, "50", "50", IntKind, true},
		{`50.0`, "50.0", "50.0", FloatKind, true},
		{`1e16`, "1e+16", "1e+16", FloatKind, true},
		{`0.0`, "0.0", "0.0", FloatKind, false},
		{`-0.0`, "-0.0", "-0.0", FloatKind, false},
		{`""`, "", "", StrKind, false},
		{`"x"`, "x", "x", StrKind, true},
		{`123456789012345678901234567890`, "123456789012345678901234567890", "123456789012345678901234567890", IntKind, true},
		{`[]`, "[]", "[]", ListKind, false},
		{`[1, "a", null, true, {"k": 2.5}]`, `[1, 'a', None, True, {'k': 2.5}]`, `[1, 'a', None, True, {'k': 2.5}]`, ListKind, true},
		{`{}`, "{}", "{}", DictKind, false},
		{`{"it's": "a\"b"}`, `{"it's": 'a"b'}`, `{"it's": 'a"b'}`, DictKind, true},
	}
	for _, c := range cases {
		var v Value
		if err := json.Unmarshal([]byte(c.json), &v); err != nil {
			t.Fatalf("%s: %v", c.json, err)
		}
		if v.Kind() != c.kind || v.String() != c.str || v.TagText() != c.tag || v.Truthy() != c.truthy {
			t.Errorf("%s: kind=%d str=%q tag=%q truthy=%v", c.json, v.Kind(), v.String(), v.TagText(), v.Truthy())
		}
	}
	var missing Value
	if missing.Exists() || missing.String() != "None" || !missing.IsNone() {
		t.Error("zero Value should read as a missing key")
	}
	if got := missing.Get(Int(7)); !got.Equal(Int(7)) {
		t.Error("Get on a missing key should give the default")
	}
	if got := None.Get(Int(7)); !got.Equal(None) {
		t.Error("Get on a null key should give None, like dict.get")
	}
	if got := String("").OrString("x"); got.String() != "x" {
		t.Error("Or should fall back on a falsy value")
	}
}

func TestContainersNeverFail(t *testing.T) {
	type item struct {
		Name Value       `json:"name"`
		Sub  Obj[item]   `json:"sub"`
		Tags List[Value] `json:"tags"`
	}
	type doc struct {
		Items  Objs[item]  `json:"items"`
		Counts Map[Value]  `json:"counts"`
		One    Obj[item]   `json:"one"`
		Series List[Value] `json:"series"`
	}
	body := `{"items": [{"name": "a", "sub": "oops"}, null, 5, {"name": 2, "tags": "x"}],
		"counts": {"z": 1, "a": 2, "z": 3}, "one": [1], "series": [1, null, "x"]}`
	d, err := Decode[doc]([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Items) != 2 || d.Items[0].Name.String() != "a" || d.Items[0].Sub.OK() || d.Items[1].Name.String() != "2" {
		t.Errorf("items = %+v", d.Items)
	}
	if len(d.Items[1].Tags) != 0 {
		t.Errorf("non-array list should be empty, got %v", d.Items[1].Tags)
	}
	if d.Counts.Len() != 2 || d.Counts.Keys[0] != "z" || d.Counts.Values[0].String() != "3" {
		t.Errorf("counts = %+v (want first position, last value)", d.Counts)
	}
	if d.One.OK() {
		t.Error("an array should not decode as an object")
	}
	if len(d.Series) != 3 || d.Series[1].Kind() != NullKind {
		t.Errorf("series = %v", d.Series)
	}
	if v, ok := d.Counts.Lookup("a"); !ok || v.String() != "2" {
		t.Error("lookup")
	}
	var m Map[Value]
	m.Set("b", Int(1))
	m.Set("a", Int(2))
	m.Set("b", Int(3))
	if m.Keys[0] != "b" || m.Values[0].String() != "3" {
		t.Errorf("set = %+v", m)
	}
}

func TestIsoFormat(t *testing.T) {
	cases := map[string]time.Time{
		"2026-11-18T16:30:00+00:00":        time.Date(2026, 11, 18, 16, 30, 0, 0, time.UTC),
		"2026-11-18T10:30:00.123456-06:00": time.Date(2026, 11, 18, 10, 30, 0, 123456789, time.FixedZone("", -6*3600)),
		"2026-11-18T16:30:00+05:30:15":     time.Date(2026, 11, 18, 16, 30, 0, 0, time.FixedZone("", 5*3600+30*60+15)),
	}
	for want, ts := range cases {
		if got := IsoFormat(ts); got != want {
			t.Errorf("IsoFormat = %q, want %q", got, want)
		}
	}
}

func TestReprString(t *testing.T) {
	cases := map[string]string{
		"abc":       `'abc'`,
		"it's":      `"it's"`,
		`a'b"c`:     `'a\'b"c'`,
		"tab\there": `'tab\there'`,
		"\x00\x7f":  `'\x00\x7f'`,
		"\u200b":    `'\u200b'`,
		"é👽":        `'é👽'`,
	}
	for in, want := range cases {
		if got := ReprString(in); got != want {
			t.Errorf("ReprString(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestToIntAndFloatHelpers(t *testing.T) {
	if ToInt(String("inf"), 9) != 9 {
		t.Error("inf should fall back instead of escaping like Python's OverflowError")
	}
	if ToInt(Float(7.9), 0) != 7 || ToInt(Bool(true), 0) != 1 || ToInt(None, 4) != 4 {
		t.Error("ToInt")
	}
	if ToFloatOr(String("2.5"), 0) != 2.5 || ToFloatOr(String(""), 1.5) != 1.5 {
		t.Error("ToFloatOr")
	}
	if n, err := IntExact(String("12")); err != nil || n != 12 {
		t.Error("IntExact str")
	}
	if _, err := IntExact(String("1.5")); err == nil {
		t.Error("int('1.5') should fail")
	}
	if n, err := IntExact(Float(-2.7)); err != nil || n != -2 {
		t.Error("IntExact float")
	}
	big := fromToken(json.Number("123456789012345678901234567890"))
	if f, err := ToFloat(big); err != nil || FloatRepr(f) != "1.2345678901234568e+29" {
		t.Errorf("float(big int) = %v, %v", f, err)
	}
	if _, err := TruncFloat(math.NaN()); err != ErrValue {
		t.Error("int(nan)")
	}
}

func FuzzParseTimestamp(f *testing.F) {
	for _, s := range []string{"2026-11-18T16:30:00Z", "2026-11-18 16:30:00.1234567+05:30", "2026-02-29", "x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ts, err := ParseDateTime(s)
		if err != nil {
			return
		}
		if ts.Nanosecond()%1000 != 0 {
			t.Fatalf("%q kept sub-microsecond precision", s)
		}
	})
}

func FuzzParseFloat(f *testing.F) {
	for _, s := range []string{"1", "1_000.5", " -inf ", "1e400", "०"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v, err := ParseFloat(s)
		if err == nil && !math.IsNaN(v) && !math.IsInf(v, 0) {
			if back, err := ParseFloat(FloatRepr(v)); err != nil || back != v {
				t.Fatalf("%q -> %v does not round-trip through repr", s, v)
			}
		}
	})
}
