package tick

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// Python's json.dumps (default separators ", " and ": ", ensure_ascii) for the event details the reap units write. Go's
// encoding/json sorts map keys and escapes differently, so a detail built with it would not be byte-equal to Python's.

// kv is one key/value of an ordered object (a Python dict keeps insertion order).
type kv struct {
	K string
	V any
}

// ordered is a Python dict in insertion order.
type ordered []kv

// pyDumps is json.dumps(v). v may be nil, bool, int, int64, float64, string, []any or ordered. Anything else (a Go map
// has no insertion order) is an error, never a guess.
func pyDumps(v any) (string, error) {
	var sb strings.Builder
	if err := pyDump(&sb, v); err != nil {
		return "", err
	}
	return sb.String(), nil
}

func pyDump(sb *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		if x {
			sb.WriteString("true")
		} else {
			sb.WriteString("false")
		}
	case int:
		sb.WriteString(strconv.Itoa(x))
	case int64:
		sb.WriteString(strconv.FormatInt(x, 10))
	case float64:
		sb.WriteString(pyFloatRepr(x))
	case string:
		pyDumpString(sb, x)
	case []any:
		sb.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				sb.WriteString(", ")
			}
			if err := pyDump(sb, e); err != nil {
				return err
			}
		}
		sb.WriteByte(']')
	case ordered:
		sb.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				sb.WriteString(", ")
			}
			pyDumpString(sb, e.K)
			sb.WriteString(": ")
			if err := pyDump(sb, e.V); err != nil {
				return err
			}
		}
		sb.WriteByte('}')
	default:
		return fmt.Errorf("pyDumps: unsupported type %T (use ordered for an object)", v)
	}
	return nil
}

// pyDumpString writes a JSON string the way json.dumps does with ensure_ascii: ", \, \n \r \t \b \f and every other
// control character below U+0020 escaped, every character outside space..~ (so U+007F too) as \uXXXX (a surrogate pair above U+FFFF). Invalid UTF-8 becomes U+FFFD, as Python's surrogateescape-free decode would never hand us anything else.
func pyDumpString(sb *strings.Builder, s string) {
	sb.WriteByte('"')
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		i += n
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
		case r < 0x20:
			fmt.Fprintf(sb, `\u%04x`, r)
		case r < 0x7f:
			sb.WriteRune(r)
		case r > 0xffff:
			a, b := utf16.EncodeRune(r)
			fmt.Fprintf(sb, `\u%04x\u%04x`, a, b)
		default:
			fmt.Fprintf(sb, `\u%04x`, r)
		}
	}
	sb.WriteByte('"')
}

// pyFloatRepr is repr(float): the shortest round-trip digits, fixed notation for a decimal exponent in [-4, 16), else
// d.ddde+XX with at least two exponent digits; an integral value keeps ".0". NaN and the infinities are json.dumps's
// NaN, Infinity and -Infinity.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d[.ddd]e±XX
	i := strings.IndexByte(e, 'e')
	exp, _ := strconv.Atoi(e[i+1:]) // FormatFloat always writes a valid exponent
	if exp >= -4 && exp < 16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.ContainsAny(s, ".") {
			s += ".0"
		}
		return s
	}
	sign := "+"
	if exp < 0 {
		sign, exp = "-", -exp
	}
	return fmt.Sprintf("%s%s%02d", e[:i+1], sign, exp)
}

// spawnArgsMap decodes agents.spawn_args. ok is false for text that is not a JSON object (Python: ValueError, or a
// non-dict whose .get would fail; the callers treat both as "no spawn args").
func spawnArgsMap(raw string) (map[string]any, bool) {
	if raw == "" {
		return map[string]any{}, true
	}
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return nil, false
	}
	m, ok := v.(map[string]any)
	return m, ok
}
