package data

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Num is a JSON number that remembers whether Python made it an int or a float (1 versus 1.0), which
// encoding/json cannot tell apart.
type Num struct {
	V       float64
	IsFloat bool
}

// Int is an integer Num; Float a float Num.
func Int(n int64) Num       { return Num{V: float64(n)} }
func Float(f float64) Num   { return Num{V: f, IsFloat: true} }
func NumP(n Num) *Num       { return &n }
func FloatP(f float64) *Num { return NumP(Float(f)) }

// String is Python's str() of the number.
func (n Num) String() string {
	if !n.IsFloat {
		return strconv.FormatInt(int64(n.V), 10)
	}
	return pyFloat(n.V)
}

// MarshalJSON writes the number the way Python's json.dumps does.
func (n Num) MarshalJSON() ([]byte, error) { return []byte(n.String()), nil }

func pyFloat(v float64) string {
	a := v
	if a < 0 {
		a = -a
	}
	if a != 0 && (a < 1e-4 || a >= 1e16) {
		return strconv.FormatFloat(v, 'e', -1, 64)
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	if !strings.ContainsAny(s, ".") {
		s += ".0"
	}
	return s
}

// OMap is a JSON object that keeps insertion order, like a Python dict.
type OMap[V any] struct {
	Keys []string
	M    map[string]V
}

// NewOMap returns an empty OMap.
func NewOMap[V any]() *OMap[V] { return &OMap[V]{M: map[string]V{}} }

// Set stores v under k; an existing key keeps its position (Python dict semantics).
func (o *OMap[V]) Set(k string, v V) {
	if _, ok := o.M[k]; !ok {
		o.Keys = append(o.Keys, k)
	}
	o.M[k] = v
}

// Get returns the value for k.
func (o *OMap[V]) Get(k string) (V, bool) {
	if o == nil {
		var z V
		return z, false
	}
	v, ok := o.M[k]
	return v, ok
}

// Len is the number of keys.
func (o *OMap[V]) Len() int {
	if o == nil {
		return 0
	}
	return len(o.Keys)
}

// MarshalJSON writes the keys in insertion order. A nil map is {}.
func (o *OMap[V]) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	if o != nil {
		for i, k := range o.Keys {
			if i > 0 {
				b.WriteByte(',')
			}
			kb, err := marshal(k)
			if err != nil {
				return nil, err
			}
			b.Write(kb)
			b.WriteByte(':')
			vb, err := marshal(o.M[k])
			if err != nil {
				return nil, err
			}
			b.Write(vb)
		}
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// kv is one key/value pair for ordered, hand-built objects.
type kv struct {
	k string
	v any
}

func ordered(pairs ...kv) ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte(',')
		}
		kb, _ := marshal(p.k)
		b.Write(kb)
		b.WriteByte(':')
		vb, err := marshal(p.v)
		if err != nil {
			return nil, err
		}
		b.Write(vb)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// marshal is json.Marshal without HTML escaping (Python does not escape <, > or &).
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Dumps is json.dumps(v) with the default arguments (ensure_ascii, ", " and ": " separators).
func Dumps(v any) ([]byte, error) {
	raw, err := marshal(v)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(raw); {
		c := raw[i]
		if inStr {
			switch {
			case esc:
				esc = false
				out.WriteByte(c)
				i++
			case c == '\\':
				esc = true
				out.WriteByte(c)
				i++
			case c == '"':
				inStr = false
				out.WriteByte(c)
				i++
			case c >= utf8.RuneSelf:
				r, n := utf8.DecodeRune(raw[i:])
				out.WriteString(asciiEscape(r))
				i += n
			default:
				out.WriteByte(c)
				i++
			}
			continue
		}
		out.WriteByte(c)
		switch c {
		case '"':
			inStr = true
		case ',', ':':
			out.WriteByte(' ')
		}
		i++
	}
	return out.Bytes(), nil
}

// DumpsIndent1 is json.dumps(v, indent=1).
func DumpsIndent1(v any) ([]byte, error) {
	raw, err := marshal(v)
	if err != nil {
		return nil, err
	}
	var ind bytes.Buffer
	if err := json.Indent(&ind, raw, "", " "); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	b := ind.Bytes()
	for i := 0; i < len(b); {
		if b[i] < utf8.RuneSelf {
			out.WriteByte(b[i])
			i++
			continue
		}
		r, n := utf8.DecodeRune(b[i:])
		out.WriteString(asciiEscape(r))
		i += n
	}
	return out.Bytes(), nil
}

func asciiEscape(r rune) string {
	if r >= 0x10000 {
		r -= 0x10000
		return fmt.Sprintf("\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	}
	return fmt.Sprintf("\\u%04x", r)
}
