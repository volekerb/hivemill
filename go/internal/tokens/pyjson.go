package tokens

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// pyKV and pyObj are an ordered JSON object (a Python dict keeps insertion order).
type pyKV struct {
	K string
	V any
}
type pyObj []pyKV

// pyDumps is json.dumps(v, sort_keys=sortKeys) with Python's default separators (", " and ": ") and ensure_ascii.
// Values: nil, bool, string, ints, float64, json.Number (kept as written), []any, map[string]any, pyObj.
func pyDumps(v any, sortKeys bool) string {
	var b strings.Builder
	pyWrite(&b, v, sortKeys)
	return b.String()
}

func pyWrite(b *strings.Builder, v any, sortKeys bool) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case string:
		pyString(b, x)
	case int:
		b.WriteString(strconv.Itoa(x))
	case int64:
		b.WriteString(strconv.FormatInt(x, 10))
	case float64:
		b.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case json.Number:
		b.WriteString(string(x))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyWrite(b, e, sortKeys)
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		kv := make(pyObj, 0, len(keys))
		for _, k := range keys {
			kv = append(kv, pyKV{k, x[k]})
		}
		pyWrite(b, kv, sortKeys)
	case pyObj:
		b.WriteByte('{')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			pyString(b, e.K)
			b.WriteString(": ")
			pyWrite(b, e.V, sortKeys)
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("tokens: pyDumps: unsupported %T", v))
	}
}

func pyString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\f':
			b.WriteString(`\f`)
		case r < 0x20 || (r >= 0x7f && r < 0x10000):
			if r == 0x7f {
				b.WriteRune(r) // DEL is printable ASCII to Python
			} else {
				fmt.Fprintf(b, `\u%04x`, r)
			}
		case r >= 0x10000:
			r -= 0x10000
			fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
