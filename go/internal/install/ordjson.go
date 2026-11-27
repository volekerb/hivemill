package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
)

// OMap is a JSON object that keeps its key order, like a Python dict: the Herdr app layout and dashboard files are
// rewritten with json.dumps(..., indent=2), so the order of existing keys must survive a round trip.
type OMap struct {
	Keys []string
	Vals map[string]any
}

// NewOMap returns an empty object.
func NewOMap() *OMap { return &OMap{Vals: map[string]any{}} }

// Get returns the value of key.
func (m *OMap) Get(k string) (any, bool) { v, ok := m.Vals[k]; return v, ok }

// Set stores v; a new key goes last, an existing key keeps its place (Python dict semantics).
func (m *OMap) Set(k string, v any) {
	if _, ok := m.Vals[k]; !ok {
		m.Keys = append(m.Keys, k)
	}
	m.Vals[k] = v
}

// Del removes key, if present.
func (m *OMap) Del(k string) {
	if _, ok := m.Vals[k]; !ok {
		return
	}
	delete(m.Vals, k)
	for i, key := range m.Keys {
		if key == k {
			m.Keys = append(m.Keys[:i:i], m.Keys[i+1:]...)
			break
		}
	}
}

// Num is a JSON number kept as its source text.
type Num string

const maxJSONDepth = 500

// decodeOrdered parses one JSON document. Objects become *OMap, arrays []any, numbers Num.
func decodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.UseNumber()
	v, err := decodeValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("extra data after the JSON value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, errors.New("JSON nested too deeply")
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			m := NewOMap()
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("invalid JSON: %w", err)
				}
				k, ok := kt.(string)
				if !ok {
					return nil, errors.New("invalid JSON: object key is not a string")
				}
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				m.Set(k, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return m, nil
		case '[':
			list := []any{}
			for dec.More() {
				v, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				list = append(list, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %w", err)
			}
			return list, nil
		}
		return nil, errors.New("invalid JSON: unexpected delimiter")
	case json.Number:
		return Num(t), nil
	default:
		return tok, nil // string, bool, nil
	}
}

// encodeOrdered is json.dumps(v, indent=2): ensure_ascii, ", " free separators, Python float repr.
func encodeOrdered(v any) (string, error) {
	var sb strings.Builder
	if err := encodeValue(&sb, v, 0); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func encodeValue(sb *strings.Builder, v any, level int) error {
	pad := func(n int) string { return strings.Repeat("  ", n) }
	switch t := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(t))
	case string:
		encodeString(sb, t)
	case Num:
		s, err := pyNumber(string(t))
		if err != nil {
			return err
		}
		sb.WriteString(s)
	case *OMap:
		if len(t.Keys) == 0 {
			sb.WriteString("{}")
			return nil
		}
		sb.WriteString("{\n")
		for i, k := range t.Keys {
			sb.WriteString(pad(level + 1))
			encodeString(sb, k)
			sb.WriteString(": ")
			if err := encodeValue(sb, t.Vals[k], level+1); err != nil {
				return err
			}
			if i < len(t.Keys)-1 {
				sb.WriteString(",")
			}
			sb.WriteString("\n")
		}
		sb.WriteString(pad(level) + "}")
	case []any:
		if len(t) == 0 {
			sb.WriteString("[]")
			return nil
		}
		sb.WriteString("[\n")
		for i, e := range t {
			sb.WriteString(pad(level + 1))
			if err := encodeValue(sb, e, level+1); err != nil {
				return err
			}
			if i < len(t)-1 {
				sb.WriteString(",")
			}
			sb.WriteString("\n")
		}
		sb.WriteString(pad(level) + "]")
	default:
		return fmt.Errorf("cannot encode %T as JSON", v)
	}
	return nil
}

func encodeString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			sb.WriteString(`\"`)
		case r == '\\':
			sb.WriteString(`\\`)
		case r == '\n':
			sb.WriteString(`\n`)
		case r == '\r':
			sb.WriteString(`\r`)
		case r == '\t':
			sb.WriteString(`\t`)
		case r == '\b':
			sb.WriteString(`\b`)
		case r == '\f':
			sb.WriteString(`\f`)
		case r >= 0x20 && r <= 0x7e:
			sb.WriteRune(r)
		case r > 0xffff:
			hi, lo := utf16.EncodeRune(r)
			fmt.Fprintf(sb, `\u%04x\u%04x`, hi, lo)
		default:
			fmt.Fprintf(sb, `\u%04x`, r)
		}
	}
	sb.WriteByte('"')
}

// pyNumber renders a JSON number the way Python would after json.loads and json.dumps.
func pyNumber(s string) (string, error) {
	if !strings.ContainsAny(s, ".eE") {
		if strings.Trim(s, "-0") == "" {
			return "0", nil
		}
		return s, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return "", fmt.Errorf("bad JSON number %q: %w", s, err)
	}
	switch {
	case math.IsInf(f, 1):
		return "Infinity", nil
	case math.IsInf(f, -1):
		return "-Infinity", nil
	case f == 0:
		if math.Signbit(f) {
			return "-0.0", nil
		}
		return "0.0", nil
	}
	e := strconv.FormatFloat(f, 'e', -1, 64)
	exp, err := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if err != nil {
		return "", fmt.Errorf("bad float exponent in %q: %w", e, err)
	}
	if exp < -4 || exp >= 16 {
		return e, nil
	}
	out := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(out, ".") {
		out += ".0"
	}
	return out, nil
}

// truthy mirrors Python truthiness for decoded JSON values.
func truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		return t != ""
	case Num:
		f, err := strconv.ParseFloat(string(t), 64)
		return err != nil || f != 0
	case *OMap:
		return len(t.Keys) > 0
	case []any:
		return len(t) > 0
	}
	return true
}

// copyValue deep-copies a decoded JSON value.
func copyValue(v any) any {
	switch t := v.(type) {
	case *OMap:
		c := NewOMap()
		for _, k := range t.Keys {
			c.Set(k, copyValue(t.Vals[k]))
		}
		return c
	case []any:
		c := make([]any, len(t))
		for i, e := range t {
			c[i] = copyValue(e)
		}
		return c
	}
	return v
}
