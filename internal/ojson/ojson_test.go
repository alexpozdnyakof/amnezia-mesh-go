package ojson

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestMarshalMatchesCPythonEscaping(t *testing.T) {
	o := New()
	o.Set("quote", `"`)
	o.Set("backslash", `\`)
	o.Set("short", "\b\f\n\r\t")
	o.Set("vtab", "\x0b")
	o.Set("del", "\x7f")
	o.Set("bmp", "é")    // → \u00e9
	o.Set("astral", "😀") // → surrogate pair
	o.Set("ascii", "safe-123 ~")
	var buf bytes.Buffer
	o.Marshal(&buf)
	// CPython json.dumps(ensure_ascii=True): é → 6-char escape, astral → pair.
	want := "{\"quote\":\"\\\"\",\"backslash\":\"\\\\\",\"short\":\"\\b\\f\\n\\r\\t\"," +
		"\"vtab\":\"\\u000b\",\"del\":\"\\u007f\",\"bmp\":\"\\u00e9\",\"astral\":\"\\ud83d\\ude00\"," +
		"\"ascii\":\"safe-123 ~\"}"
	if buf.String() != want {
		t.Fatalf("marshal mismatch:\n got: %s\nwant: %s", buf.String(), want)
	}
}

func TestParseMarshalRoundtripPreservesSemantics(t *testing.T) {
	inner := `{"port":443,"name":"hüb","list":[1,"x",true,null],"obj":{"a":1}}`
	v, err := Parse([]byte(inner))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	marshal(v, &buf, ",", ":")
	again, err := Parse(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !Equal(v, again) {
		t.Fatalf("roundtrip changed semantics:\n first: %v\nsecond: %v", v, again)
	}
	if n := v.(*Obj).Get("port"); n != json.Number("443") {
		t.Fatalf("number token not preserved verbatim: %v (%T)", n, n)
	}
}

func TestEqualIgnoresKeyOrder(t *testing.T) {
	a := New()
	a.Set("x", "1")
	a.Set("y", "2")
	b := New()
	b.Set("y", "2")
	b.Set("x", "1")
	if !Equal(a, b) {
		t.Fatal("Equal must ignore object key order")
	}
	b.Set("x", "3")
	if Equal(a, b) {
		t.Fatal("Equal must compare values")
	}
}

func TestParseRejectsTrailingData(t *testing.T) {
	if _, err := Parse([]byte(`{"a":1} {"b":2}`)); err == nil {
		t.Fatal("trailing data must be rejected")
	}
}
