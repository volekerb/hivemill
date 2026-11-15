package scout

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
)

// A small encoder with the exact output of Python's json.dumps (default separators, ensure_ascii), so files and
// DB values written by Go equal the ones Python writes. Go's encoding/json differs (HTML escapes, key order).

// KV is one key of an ordered object; OMap keeps insertion order like a Python dict.
type KV struct {
	K string
	V any
}
type OMap []KV

// rawJSON is a pre-rendered number literal.
type rawJSON string

// Dumps is json.dumps(v, indent=indent or None, sort_keys=sortKeys). Supported values: nil, bool, ints, float64,
// string, []string, []any, []OMap, map[string]any (always key-sorted) and OMap.
func Dumps(v any, indent int, sortKeys bool) string {
	var sb strings.Builder
	dump(&sb, v, indent, sortKeys, 0)
	return sb.String()
}

func pyDumps(v any) string { return Dumps(v, 0, false) }

func dump(sb *strings.Builder, v any, indent int, sortKeys bool, depth int) {
	nl := func(d int) {
		if indent > 0 {
			sb.WriteString("\n" + strings.Repeat(" ", indent*d))
		}
	}
	sep := ", "
	if indent > 0 {
		sep = ","
	}
	switch x := v.(type) {
	case nil:
		sb.WriteString("null")
	case bool:
		sb.WriteString(strconv.FormatBool(x))
	case int:
		sb.WriteString(strconv.Itoa(x))
	case int64:
		sb.WriteString(strconv.FormatInt(x, 10))
	case float64:
		sb.WriteString(strconv.FormatFloat(x, 'g', -1, 64))
	case rawJSON:
		sb.WriteString(string(x))
	case string:
		writeJSONString(sb, x)
	case []string:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		dump(sb, a, indent, sortKeys, depth)
	case []OMap:
		a := make([]any, len(x))
		for i, s := range x {
			a[i] = s
		}
		dump(sb, a, indent, sortKeys, depth)
	case []any:
		if len(x) == 0 {
			sb.WriteString("[]")
			return
		}
		sb.WriteString("[")
		for i, e := range x {
			if i > 0 {
				sb.WriteString(sep)
			}
			nl(depth + 1)
			dump(sb, e, indent, sortKeys, depth+1)
		}
		nl(depth)
		sb.WriteString("]")
	case map[string]any:
		kvs := make(OMap, 0, len(x))
		for k, e := range x {
			kvs = append(kvs, KV{k, e})
		}
		sort.Slice(kvs, func(i, j int) bool { return kvs[i].K < kvs[j].K })
		dump(sb, kvs, indent, sortKeys, depth)
	case OMap:
		if len(x) == 0 {
			sb.WriteString("{}")
			return
		}
		if sortKeys {
			x = append(OMap{}, x...)
			sort.SliceStable(x, func(i, j int) bool { return x[i].K < x[j].K })
		}
		sb.WriteString("{")
		for i, kv := range x {
			if i > 0 {
				sb.WriteString(sep)
			}
			nl(depth + 1)
			writeJSONString(sb, kv.K)
			sb.WriteString(": ")
			dump(sb, kv.V, indent, sortKeys, depth+1)
		}
		nl(depth)
		sb.WriteString("}")
	default:
		panic(fmt.Sprintf("jsonpy: unsupported type %T", v))
	}
}

func writeJSONString(sb *strings.Builder, s string) {
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
		case r < 0x20:
			fmt.Fprintf(sb, `\u%04x`, r)
		case r < 0x7f || r == 0x7f:
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
