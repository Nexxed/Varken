package pycompat

import (
	"encoding/json"
	"reflect"
	"testing"
)

// slowValue is the straightforward decoder the fast paths replace: the reference they must agree with.
func slowValue(data []byte) (Value, error) {
	var v Value
	switch {
	case len(data) > 0 && data[0] == '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return v, err
		}
		return String(s), nil
	case len(data) > 0 && data[0] == '[':
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return v, err
		}
		return Value{kind: ListKind, s: string(data), n: len(items)}, nil
	case len(data) > 0 && data[0] == '{':
		var items map[string]json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return v, err
		}
		return Value{kind: DictKind, s: string(data), n: len(items)}, nil
	}
	err := v.UnmarshalJSON(data)
	return v, err
}

func TestFastStringsMatchDecoder(t *testing.T) {
	for _, text := range []string{`"plain"`, `"é👽"`, `"a\"b"`, `"tab\tand\\"`, `"é"`, `"👽"`,
		`"\ud800"`, "\"bad\xffutf8\"", "\"ctl\x01\"", `""`, `"a"b"`, `"`} {
		var fast Value
		fastErr := fast.UnmarshalJSON([]byte(text))
		slow, slowErr := slowValue([]byte(text))
		if (fastErr == nil) != (slowErr == nil) || (fastErr == nil && !fast.Equal(slow)) {
			t.Errorf("%q: fast %q %v, decoder %q %v", text, fast.String(), fastErr, slow.String(), slowErr)
		}
	}
}

func TestElementCounts(t *testing.T) {
	cases := map[string]int{`[]`: 0, `[ ]`: 0, " [\n] ": 0, `[1]`: 1, `[[]]`: 1, `[{}]`: 1, `[[],[]]`: 2,
		`["a,b", "]", "[", "\""]`: 4, `[{"a": [1, 2, {"b": ","}]}, null]`: 2, `{}`: 0, `{"a": 1, "b": [1, 2]}`: 2,
		`{"a": 1, "a": 2}`: 1, `[["x\\"], 3]`: 2}
	for text, want := range cases {
		n, err := countElements([]byte(text))
		if err != nil || n != want {
			t.Errorf("count(%s) = %d, %v; want %d", text, n, err, want)
		}
	}
	if _, err := countElements([]byte(`[1,`)); err == nil {
		t.Error("malformed array")
	}
}

type fastItem struct {
	A Value       `json:"a"`
	B List[Value] `json:"b"`
}

func TestTopLevelDecodeKeepsRules(t *testing.T) {
	objs, err := Decode[Objs[fastItem]]([]byte(`[{"a": 1}, null, "x", 2, {"a": "y", "b": [1, null]}]`))
	if err != nil || len(objs) != 2 || objs[1].A.String() != "y" || len(objs[1].B) != 2 || objs[1].B[1].Kind() != NullKind {
		t.Errorf("mixed Objs = %+v, %v", objs, err)
	}
	objs, err = Decode[Objs[fastItem]]([]byte(`[{"a": 1}, null]`))
	if err != nil || len(objs) != 1 {
		t.Errorf("fast Objs = %+v, %v", objs, err)
	}
	if objs, err := Decode[Objs[fastItem]]([]byte(`{"a": 1}`)); err != nil || objs != nil {
		t.Errorf("an object is not a list: %v %v", objs, err)
	}
	for _, bad := range []string{`[{"a": 1}`, `<html>`, ``} {
		if _, err := Decode[Objs[fastItem]]([]byte(bad)); err == nil {
			t.Errorf("Objs %q should fail", bad)
		}
		if _, err := Decode[List[Value]]([]byte(bad)); err == nil {
			t.Errorf("List %q should fail", bad)
		}
	}
	list, err := Decode[List[fastItem]]([]byte(`[{"a": 1}, "x", null]`))
	if err != nil || len(list) != 3 || list[0].A.String() != "1" || list[1].A.Exists() {
		t.Errorf("mixed List = %+v, %v", list, err)
	}
	if list, err := Decode[List[Value]]([]byte(`{"a": 1}`)); err != nil || list != nil {
		t.Errorf("an object is not a list: %v %v", list, err)
	}
	if list, err := Decode[List[Value]]([]byte(`[1, null, "x"]`)); err != nil || len(list) != 3 || list[1].Kind() != NullKind {
		t.Errorf("List of values = %v, %v", list, err)
	}
}

// FuzzFastPathsMatchSlow checks that every fast path decodes exactly like the element-by-element decoder.
func FuzzFastPathsMatchSlow(f *testing.F) {
	for _, s := range []string{`"x"`, `[1, "a", null]`, `{"a": [1]}`, `[{"a": 1}, null, 2]`, `[{"a": "é", "b": [true]}]`,
		`[[1,[2]],"]"]`} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if !json.Valid(data) {
			return
		}
		data = []byte(string(data))
		var fast Value
		fastErr := fast.UnmarshalJSON(trim(data))
		slow, slowErr := slowValue(trim(data))
		if (fastErr == nil) != (slowErr == nil) || (fastErr == nil && !fast.Equal(slow)) {
			t.Fatalf("Value %q: fast %v %v, slow %v %v", data, fast.Repr(), fastErr, slow.Repr(), slowErr)
		}
		var objsFast, objsSlow Objs[fastItem]
		_ = objsFast.UnmarshalJSON(data)
		_ = objsSlow.slow(data)
		if !reflect.DeepEqual(objsFast, objsSlow) {
			t.Fatalf("Objs %q: fast %+v, slow %+v", data, objsFast, objsSlow)
		}
		var listFast, listSlow List[Value]
		_ = listFast.UnmarshalJSON(data)
		_ = listSlow.slow(data)
		if !reflect.DeepEqual(listFast, listSlow) {
			t.Fatalf("List %q: fast %v, slow %v", data, listFast, listSlow)
		}
	})
}

func trim(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t' || b[0] == '\n' || b[0] == '\r') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t' || b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
