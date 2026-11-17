// Package spawn is the pure half of the Go port of bin/factory_cli.py spawn() / cmd_spawn(): Plan decides what a
// spawn would do (names, org, model, env, refusals, queueing through go/internal/gate) and Brief / SettingsText
// render the role prompt and settings JSON byte-equal to Python. It starts no process, creates no worktree and
// writes no DB row: the caller (P1h) reads the facts, calls Plan, and performs the plan.
package spawn

import (
	"fmt"
	"os"
	"strings"
	"unicode"
	"unicode/utf8"
)

// pyIsSpace is str.isspace for one rune (Python's whitespace set).
func pyIsSpace(r rune) bool {
	switch r {
	case ' ', '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyLower is str.lower() as far as an ASCII-only result can tell: Go's simple mapping equals Python's full mapping
// except U+0130 (Python: "i" + U+0307, Go: "i").
func pyLower(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == 0x130 {
			b.WriteString("i̇")
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// Slug is slug(s, n=32) of the Python CLI: lower-case, every run outside [a-z0-9-] becomes "-", trimmed; a result that
// is empty or does not start with a letter gets the "a-" prefix; cut to 32 and right-trimmed of "-".
func Slug(s string) string {
	var b strings.Builder
	gap := false
	for _, r := range pyLower(s) {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			if gap {
				b.WriteByte('-')
				gap = false
			}
			b.WriteRune(r)
		} else {
			gap = true
		}
	}
	if gap {
		b.WriteByte('-')
	}
	out := strings.Trim(b.String(), "-")
	if out == "" || !(out[0] >= 'a' && out[0] <= 'z') {
		out = "a-" + out
	}
	if len(out) > 32 {
		out = out[:32]
	}
	return strings.TrimRight(out, "-")
}

// jsonEscape is json.dumps(v)[1:-1] of Python (ensure_ascii): the text that sits between the quotes. A byte that is not
// valid UTF-8 is what os.fsdecode gives Python, a lone surrogate U+DC80..U+DCFF, written as \udcXX.
func jsonEscape(v string) string {
	var b strings.Builder
	for i := 0; i < len(v); {
		r, n := utf8.DecodeRuneInString(v[i:])
		if r == utf8.RuneError && n == 1 {
			fmt.Fprintf(&b, "\\u%04x", 0xdc00+int(v[i]))
			i++
			continue
		}
		i += n
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
		case r < 0x20:
			fmt.Fprintf(&b, "\\u%04x", r)
		case r < 0x7f: // Python escapes everything outside " "..."~", DEL included
			b.WriteRune(r)
		case r < 0x10000:
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			r -= 0x10000
			fmt.Fprintf(&b, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
		}
	}
	return b.String()
}

// DecodeText is Path.read_text() on the bytes: UTF-8 (an invalid byte is an error, Python raises
// UnicodeDecodeError) with universal newlines (\r\n and a lone \r read as \n).
func DecodeText(b []byte) (string, error) {
	if !utf8.Valid(b) {
		return "", fmt.Errorf("not valid UTF-8")
	}
	s := string(b)
	if strings.ContainsRune(s, '\r') {
		s = strings.ReplaceAll(s, "\r\n", "\n")
		s = strings.ReplaceAll(s, "\r", "\n")
	}
	return s, nil
}

// ReadText reads a file the way Path.read_text() does. The error names the path.
func ReadText(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	s, err := DecodeText(b)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return s, nil
}

// pyList is repr() of a list of str, for the few Python messages that print one.
func pyList(l []string) string {
	q := make([]string, len(l))
	for i, s := range l {
		q[i] = pyRepr(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// csv is _csv(): split on commas, strip, drop empties.
// InCSV reports whether role is in the comma list v, entries stripped as Python's csv() strips them.
func InCSV(v, role string) bool { return inList(csv(v), role) }

func csv(v string) []string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = pyStrip(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// pyRepr is repr() of a str for the text Python messages carry: single quotes (double quotes when the text holds a
// single quote and no double quote), backslash escapes for the quote, the backslash, \n \r \t and other controls.
func pyRepr(s string) string {
	q := '\''
	if strings.ContainsRune(s, '\'') && !strings.ContainsRune(s, '"') {
		q = '"'
	}
	var b strings.Builder
	b.WriteRune(q)
	for _, r := range s {
		switch {
		case r == q || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteRune(q)
	return b.String()
}
