package merge

import (
	"fmt"
	"strings"
	"unicode"
)

// pyLower is str.lower() for the cases that differ from strings.ToLower: U+0130 becomes "i" + U+0307.
func pyLower(s string) string {
	return strings.ToLower(strings.ReplaceAll(s, "İ", "i̇"))
}

// pyIsSpace is str.isspace for one character (see cli.pyIsSpace): Python also counts U+001C..U+001F.
func pyIsSpace(r rune) bool {
	return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f)
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyUpper is str.upper() for the check states compared against (ASCII words); any other letter uses ToUpper.
func pyUpper(s string) string { return strings.ToUpper(s) }

// PyRepr is repr(str).
func PyRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// pyScalar is str(v) of a decoded JSON value for the scalars Python prints the same way; ok=false for lists, objects
// and floats (their Python text is not reproduced here).
func pyScalar(v any) (string, bool) {
	switch x := v.(type) {
	case nil:
		return "None", true
	case string:
		return x, true
	case bool:
		if x {
			return "True", true
		}
		return "False", true
	case float64:
		if x == float64(int64(x)) && x > -1e15 && x < 1e15 {
			return fmt.Sprintf("%d", int64(x)), true
		}
	}
	return "", false
}

// truthy is Python truthiness of a decoded JSON scalar; ok=false for containers.
func truthy(v any) (res, ok bool) {
	switch x := v.(type) {
	case nil:
		return false, true
	case string:
		return x != "", true
	case bool:
		return x, true
	case float64:
		return x != 0, true
	}
	return false, false
}
