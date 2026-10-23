// Package ojson is a minimal order-preserving JSON codec whose compact
// output is byte-compatible with CPython's
// json.dumps(obj, separators=(",", ":"), ensure_ascii=True): object keys
// keep insertion order (like Python dicts) and every non-printable-ASCII
// rune is \u-escaped (astral runes as surrogate pairs).
//
// It exists because encoding/json sorts map keys and never \u-escapes
// non-ASCII, both of which break byte parity with the Python reference.
package ojson

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"unicode/utf16"
)

// Obj is an insertion-ordered JSON object.
type Obj struct {
	keys []string
	vals map[string]any
}

// New returns an empty object.
func New() *Obj {
	return &Obj{vals: map[string]any{}}
}

// Set inserts or replaces a value. A re-set key keeps its original
// position, matching Python dict assignment (last value wins).
func (o *Obj) Set(k string, v any) {
	if _, ok := o.vals[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.vals[k] = v
}

// Get returns the value for k, or nil if absent.
func (o *Obj) Get(k string) any {
	return o.vals[k]
}

// Keys returns the insertion-ordered keys.
func (o *Obj) Keys() []string {
	return o.keys
}

// Marshal appends the compact JSON representation to buf
// (Python json.dumps with separators=(",", ":")).
func (o *Obj) Marshal(buf *bytes.Buffer) {
	marshal(o, buf, ",", ":")
}

// MarshalSpaced appends the JSON representation with Python's default
// json.dumps separators (", ", ": ") — used for the nested last_config
// string, which the reference builds without a separators argument.
func (o *Obj) MarshalSpaced(buf *bytes.Buffer) {
	marshal(o, buf, ", ", ": ")
}

func marshal(v any, buf *bytes.Buffer, itemSep, keySep string) {
	switch t := v.(type) {
	case nil:
		buf.WriteString("null")
	case bool:
		if t {
			buf.WriteString("true")
		} else {
			buf.WriteString("false")
		}
	case string:
		writePyString(buf, t)
	case json.Number:
		buf.WriteString(t.String()) // verbatim token, like Python int/float repr
	case int:
		fmt.Fprintf(buf, "%d", t)
	case []any:
		buf.WriteByte('[')
		for i, e := range t {
			if i > 0 {
				buf.WriteString(itemSep)
			}
			marshal(e, buf, itemSep, keySep)
		}
		buf.WriteByte(']')
	case *Obj:
		buf.WriteByte('{')
		for i, k := range t.keys {
			if i > 0 {
				buf.WriteString(itemSep)
			}
			writePyString(buf, k)
			buf.WriteString(keySep)
			marshal(t.vals[k], buf, itemSep, keySep)
		}
		buf.WriteByte('}')
	default:
		panic(fmt.Sprintf("ojson: unsupported value type %T", v))
	}
}

// writePyString writes s as a JSON string matching CPython's
// json.dumps(..., ensure_ascii=True).
func writePyString(buf *bytes.Buffer, s string) {
	buf.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			buf.WriteString(`\"`)
		case '\\':
			buf.WriteString(`\\`)
		case '\b':
			buf.WriteString(`\b`)
		case '\f':
			buf.WriteString(`\f`)
		case '\n':
			buf.WriteString(`\n`)
		case '\r':
			buf.WriteString(`\r`)
		case '\t':
			buf.WriteString(`\t`)
		default:
			switch {
			case r >= 0x20 && r <= 0x7e:
				buf.WriteRune(r)
			case r <= 0xffff:
				fmt.Fprintf(buf, `\u%04x`, r)
			default:
				r1, r2 := utf16.EncodeRune(r)
				fmt.Fprintf(buf, `\u%04x\u%04x`, r1, r2)
			}
		}
	}
	buf.WriteByte('"')
}

// Parse decodes JSON (compact or pretty) into order-preserving values:
// objects become *Obj, numbers json.Number (verbatim tokens). Extra data
// after the top-level value is an error, like Python's json.loads.
func Parse(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := parseValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("ojson: trailing data after JSON value")
	}
	return v, nil
}

// ParseObj parses data that must be a JSON object.
func ParseObj(data []byte) (*Obj, error) {
	v, err := Parse(data)
	if err != nil {
		return nil, err
	}
	o, ok := v.(*Obj)
	if !ok {
		return nil, fmt.Errorf("ojson: not a JSON object")
	}
	return o, nil
}

func parseValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			o := New()
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := ktok.(string)
				if !ok {
					return nil, fmt.Errorf("ojson: non-string object key")
				}
				val, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				o.Set(key, val)
			}
			if _, err := dec.Token(); err != nil { // closing '}'
				return nil, err
			}
			return o, nil
		case '[':
			var arr []any
			for dec.More() {
				v, err := parseValue(dec)
				if err != nil {
					return nil, err
				}
				arr = append(arr, v)
			}
			if _, err := dec.Token(); err != nil { // closing ']'
				return nil, err
			}
			return arr, nil
		}
		return nil, fmt.Errorf("ojson: unexpected delimiter %q", t)
	case json.Number:
		return t, nil
	case string:
		return t, nil
	case bool:
		return t, nil
	case nil:
		return nil, nil
	}
	return nil, fmt.Errorf("ojson: unexpected token %T", tok)
}

// Equal reports deep equality ignoring object key order, like Python's
// dict == on json.loads results.
func Equal(a, b any) bool {
	switch ta := a.(type) {
	case *Obj:
		tb, ok := b.(*Obj)
		if !ok || len(ta.keys) != len(tb.keys) {
			return false
		}
		for _, k := range ta.keys {
			bv, ok := tb.vals[k]
			if !ok || !Equal(ta.vals[k], bv) {
				return false
			}
		}
		return true
	case []any:
		tb, ok := b.([]any)
		if !ok || len(ta) != len(tb) {
			return false
		}
		for i := range ta {
			if !Equal(ta[i], tb[i]) {
				return false
			}
		}
		return true
	case json.Number:
		tb, ok := b.(json.Number)
		return ok && ta == tb
	case string:
		tb, ok := b.(string)
		return ok && ta == tb
	case bool:
		tb, ok := b.(bool)
		return ok && ta == tb
	case nil:
		return b == nil
	}
	return false
}
