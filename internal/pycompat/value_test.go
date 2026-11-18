package pycompat

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
)

func decode(t *testing.T, text string) Value {
	t.Helper()
	var v Value
	if err := json.Unmarshal([]byte(text), &v); err != nil {
		t.Fatalf("%s: %v", text, err)
	}
	return v
}

func TestAccessors(t *testing.T) {
	if s, ok := String("é").AsString(); !ok || s != "é" {
		t.Error("AsString")
	}
	if b, ok := Bool(true).AsBool(); !ok || !b {
		t.Error("AsBool")
	}
	if i, ok := Int(4).AsInt(); !ok || i != 4 {
		t.Error("AsInt")
	}
	if _, ok := decode(t, "123456789012345678901234567890").AsInt(); ok {
		t.Error("a big int does not fit int64")
	}
	if !decode(t, "-123456789012345678901234567890").IsBig() || Int(1).IsBig() {
		t.Error("IsBig")
	}
	if f, ok := Float(1.5).AsFloat(); !ok || f != 1.5 {
		t.Error("AsFloat")
	}
	lens := map[string]int{`"héllo"`: 5, `[1,2]`: 2, `{"a":1}`: 1}
	for text, want := range lens {
		if n, ok := decode(t, text).Len(); !ok || n != want {
			t.Errorf("len(%s) = %d", text, n)
		}
	}
	if _, ok := Int(1).Len(); ok {
		t.Error("len(int)")
	}
	if Int(0).Or(Int(2)).String() != "2" || Int(1).Or(Int(2)).String() != "1" {
		t.Error("Or")
	}
	if String("x").Repr() != "'x'" || Int(1).Repr() != "1" {
		t.Error("Repr")
	}
	if None.Exists() != true || (Value{}).Exists() {
		t.Error("Exists")
	}
}

func TestEqual(t *testing.T) {
	same := [][2]Value{{Bool(true), Bool(true)}, {Int(1), Int(1)}, {Float(math.NaN()), Float(math.NaN())},
		{String("a"), String("a")}, {None, None}, {decode(t, "[1]"), decode(t, "[1]")}}
	for _, p := range same {
		if !p[0].Equal(p[1]) {
			t.Errorf("%v != %v", p[0], p[1])
		}
	}
	diff := [][2]Value{{Int(1), Float(1)}, {Int(1), Bool(true)}, {Bool(true), Bool(false)}, {String("a"), String("b")},
		{Float(1), Float(2)}, {None, Value{}}}
	for _, p := range diff {
		if p[0].Equal(p[1]) {
			t.Errorf("%v == %v", p[0].Repr(), p[1].Repr())
		}
	}
}

func TestMarshalJSON(t *testing.T) {
	values := map[string]Value{"null": {}, "true": Bool(true), "7": Int(7), "50.0": Float(50), `"\"inf\""`: Float(math.Inf(1)),
		`"x"`: String("x"), `[1,2]`: decode(t, "[1, 2]")}
	for want, v := range values {
		got, err := json.Marshal(v)
		if err != nil || (string(got) != want && !(want == `"\"inf\""` && string(got) == `"inf"`)) {
			t.Errorf("Marshal(%s) = %s, %v", v.Repr(), got, err)
		}
	}
}

func TestDecodeEdgeCases(t *testing.T) {
	if v := decode(t, "1e400"); v.Kind() != FloatKind || !math.IsInf(mustFloat(v), 1) {
		t.Errorf("1e400 = %v", v)
	}
	if v := decode(t, ` "a" `); v.String() != "a" {
		t.Error("padded string")
	}
	var v Value
	for _, bad := range []string{`"unterminated`, `[1,`, `{"a":`} {
		if err := v.UnmarshalJSON([]byte(bad)); err == nil {
			t.Errorf("%s should fail", bad)
		}
	}
	if err := v.UnmarshalJSON(nil); err != nil || v.Kind() != NullKind {
		t.Error("empty input reads as null")
	}
	if parseJSONNumber("12x").Kind() != NullKind || parseJSONNumber("1.2.3").Kind() != NullKind {
		t.Error("garbage numbers")
	}
	if fromToken(nil).Kind() != NullKind || fromToken(true).String() != "True" {
		t.Error("fromToken")
	}
	if reprJSON(`[1,`) != `[1,` {
		t.Error("invalid JSON is printed as is")
	}
	if got := decode(t, `{"k": [true, null, 1.5, "it's"]}`).String(); got != `{'k': [True, None, 1.5, "it's"]}` {
		t.Errorf("repr = %s", got)
	}
}

func mustFloat(v Value) float64 { f, _ := v.AsFloat(); return f }

func TestConversionEdgeCases(t *testing.T) {
	if f, err := ToFloat(Bool(false)); err != nil || f != 0 {
		t.Error("float(False)")
	}
	if _, err := ToFloat(decode(t, "[1]")); err != ErrType {
		t.Error("float(list)")
	}
	huge := decode(t, "1"+strings.Repeat("0", 309))
	if _, err := ToFloat(huge); err != ErrOverflow {
		t.Errorf("float(10**309) should overflow: %v", err)
	}
	if n, err := TruncFloat(1e30); err != nil || n != math.MaxInt64 {
		t.Error("saturate high")
	}
	if n, err := TruncFloat(-1e30); err != nil || n != math.MinInt64 {
		t.Error("saturate low")
	}
	if n, err := IntExact(Bool(true)); err != nil || n != 1 {
		t.Error("int(True)")
	}
	if n, err := IntExact(Int(9)); err != nil || n != 9 {
		t.Error("int(9)")
	}
	if _, err := IntExact(None); err != ErrType {
		t.Error("int(None)")
	}
	if _, err := ParseInt("99999999999999999999"); err != ErrOverflow {
		t.Error("int overflow")
	}
	if _, err := ParseInt("1_"); err == nil {
		t.Error("trailing underscore")
	}
	if _, err := ParseFloat("++1"); err == nil {
		t.Error("double sign")
	}
	if _, err := ParseFloat("1e"); err == nil {
		t.Error("empty exponent")
	}
	if _, err := ParseFloat("."); err == nil {
		t.Error("lone point")
	}
	if _, err := ParseFloat("1a"); err == nil {
		t.Error("letters")
	}
	if _, err := ParseFloat("１"); err == nil && digitValue('１') != 1 {
		t.Error("fullwidth digit")
	}
	if digitValue(0x1D7D9) != 1 || digitValue('a') != 0 {
		t.Error("digitValue in the 32-bit table")
	}
	if _, ok := asciiDigits("é"); ok {
		t.Error("non-digit non-ASCII")
	}
	if !IsSpace(0x3000) || IsSpace('x') || Strip("  x\t") != "x" {
		t.Error("IsSpace/Strip")
	}
	if RuneLen("héllo") != 5 {
		t.Error("RuneLen")
	}
}

func TestRoundEdgeCases(t *testing.T) {
	cases := []struct {
		x    float64
		n    int
		want string
	}{
		{1234.5, -1, "1230.0"}, {1250, -2, "1200.0"}, {-0.001, 2, "-0.0"}, {0, 2, "0.0"},
		{math.Inf(1), 2, "inf"}, {1e308, 2, "1e+308"},
	}
	for _, c := range cases {
		if got := FloatRepr(Round(c.x, c.n)); got != c.want {
			t.Errorf("round(%v, %d) = %s, want %s", c.x, c.n, got, c.want)
		}
	}
}

func TestFormatPadContainers(t *testing.T) {
	if _, err := FormatPad(decode(t, "[1]"), 2); err == nil {
		t.Error("format(list)")
	}
	if Truncate("abc", 0) != "" || Truncate("abc", 5) != "abc" || Truncate("héllo", 2) != "hé" {
		t.Error("Truncate")
	}
	for kind, name := range map[Kind]string{AbsentKind: "NoneType", ListKind: "list", DictKind: "dict"} {
		if (Value{kind: kind}).typeName() != name {
			t.Error("typeName")
		}
	}
}

func TestReprStringEscapes(t *testing.T) {
	cases := map[string]string{"a\rb": `'a\rb'`, "­": `'\xad'`, "\U000e0001": `'\U000e0001'`,
		"\\": `'\\'`, "á": "'á'"}
	for in, want := range cases {
		if got := ReprString(in); got != want {
			t.Errorf("repr(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestDateTimeEdgeCases(t *testing.T) {
	bad := []string{"2026-1-18", "2026-11-18X10:00", "2026-11-18T1", "2026-11-18T10:0a", "2026-11-18T10:00:0",
		"2026-11-18T10:00:00.", "2026-11-18T10:00:00+5", "2026-11-18T10:00:00+25:00", "2026-11-18T10:00:00 PST",
		"abcd-ef-gh", "2026-13-01"}
	for _, s := range bad {
		if _, err := ParseDateTime(s); err == nil {
			t.Errorf("%q should not parse", s)
		}
	}
	ts, err := ParseDateTime("2026-11-18T10:00:00+0530")
	if err != nil {
		t.Fatal(err)
	}
	if _, off := ts.Zone(); off != 5*3600+30*60 {
		t.Errorf("offset = %d", off)
	}
	if _, err := ParseTimestampNS("9999-01-01T00:00:00Z"); err == nil {
		t.Error("beyond int64 nanoseconds")
	}
	now := time.Unix(100, 500_000_000)
	if SecondsSince(now, 40.2) != 60 || SecondsSince(now, math.NaN()) != 0 {
		t.Error("SecondsSince")
	}
}

func TestContainerEdgeCases(t *testing.T) {
	type item struct {
		A Value `json:"a"`
	}
	var o Obj[item]
	if err := json.Unmarshal([]byte(`{"a": 1}`), &o); err != nil || !o.OK() || o.V().A.String() != "1" {
		t.Error("Obj")
	}
	if err := o.UnmarshalJSON([]byte(`{"a": [}`)); err != nil || o.OK() {
		t.Error("broken object reads as absent")
	}
	if (Obj[item]{}).V().A.Exists() {
		t.Error("zero object")
	}
	var objs Objs[item]
	if err := objs.UnmarshalJSON([]byte(`{}`)); err != nil || objs != nil {
		t.Error("Objs from an object")
	}
	if err := objs.UnmarshalJSON([]byte(`[{"a": "x"}, {"a": [}]`)); err != nil || len(objs) != 0 {
		t.Errorf("malformed array: %v", objs)
	}
	var list List[item]
	if err := list.UnmarshalJSON([]byte(`[1, {"a": 2}]`)); err != nil || len(list) != 2 || list[1].A.String() != "2" {
		t.Errorf("List keeps positions: %v", list)
	}
	var m Map[Value]
	for _, bad := range []string{`[]`, `{"a": }`, `{"a"`, `{"a": 1,`} {
		if err := m.UnmarshalJSON([]byte(bad)); err != nil || m.Len() != 0 {
			t.Errorf("%s: %v", bad, m)
		}
	}
	if _, ok := m.Lookup("x"); ok {
		t.Error("lookup in empty map")
	}
	if _, err := Decode[item]([]byte(`[`)); err == nil {
		t.Error("Decode of invalid JSON")
	}
}
