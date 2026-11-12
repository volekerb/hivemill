package cli

import (
	"math"
	"strconv"
	"strings"
)

// Python string semantics the read commands depend on. Go's strings.Fields, TrimSpace and Split differ from
// str.splitlines, str.strip and textwrap.indent in which characters count as breaks or whitespace.

// pyIsSpace is str.isspace for one character: bidi type WS, B or S, or category Zs.
func pyIsSpace(r rune) bool {
	switch r {
	case '\t', '\n', '\v', '\f', '\r', 0x1c, 0x1d, 0x1e, 0x1f, ' ', 0x85, 0xa0, 0x1680,
		0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// pyStrip is str.strip() with no argument.
func pyStrip(s string) string { return strings.TrimFunc(s, pyIsSpace) }

// pyLineBreak is true for every character str.splitlines splits on (\r\n is handled by the caller).
func pyLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// pySplitlinesKeep is str.splitlines(True): each line keeps its break; a final break adds no empty line.
func pySplitlinesKeep(s string) []string {
	var lines []string
	start := 0
	for i, r := range s {
		if !pyLineBreak(r) {
			continue
		}
		end := i + len(string(r))
		if r == '\r' && strings.HasPrefix(s[end:], "\n") {
			end++
		}
		if end <= start { // the \n of a \r\n already consumed
			continue
		}
		lines = append(lines, s[start:end])
		start = end
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

// pySplitlines is str.splitlines(): the lines without their breaks.
func pySplitlines(s string) []string {
	keep := pySplitlinesKeep(s)
	out := make([]string, len(keep))
	for i, l := range keep {
		out[i] = strings.TrimRightFunc(l, pyLineBreak)
	}
	return out
}

// pyIndent is textwrap.indent(text, prefix): prefix every line whose strip() is non-empty; line ends are kept.
func pyIndent(text, prefix string) string {
	var b strings.Builder
	for _, l := range pySplitlinesKeep(text) {
		if pyStrip(l) != "" {
			b.WriteString(prefix)
		}
		b.WriteString(l)
	}
	return b.String()
}

// pyTrunc is s[:n] on code points (a negative n drops that many from the end, like a Python slice).
func pyTrunc(s string, n int) string {
	r := []rune(s)
	if n < 0 {
		n += len(r)
		if n < 0 {
			n = 0
		}
	}
	if n >= len(r) {
		return s
	}
	return string(r[:n])
}

// cell is `"" if v is None else str(v)` for a value read from SQLite.
func cell(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "", nil
	case string:
		return x, nil
	case []byte:
		return string(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	}
	return "", errDelegate // a float or blob: Python's str() is not reproduced here
}

// pyFloatRepr is Python's repr(float): the shortest string that round-trips, fixed notation for 1e-4 <= |f| < 1e16,
// otherwise the exponent form with a signed, at least two-digit exponent ("1e-05", "1e+16"); integral values keep
// ".0" and the specials are inf, -inf, nan and -0.0.
func pyFloatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	e := strconv.FormatFloat(f, 'e', -1, 64) // d[.ddd]e±XX, the exponent already has two digits
	exp, err := strconv.Atoi(e[strings.IndexByte(e, 'e')+1:])
	if err != nil {
		return e // unreachable: 'e' format always carries an exponent
	}
	if f == 0 || (exp >= -4 && exp < 16) {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	return e
}
