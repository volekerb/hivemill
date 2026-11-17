package views

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// numStrip is the whitespace int() and float() skip around a number: the ASCII " \t\n\v\f\r" and the non-ASCII Unicode
// spaces (NBSP, U+2000 range, ...), but not U+001C..U+001F, which str.strip() would remove (int("7\x1c") raises).
func numStrip(s string) string {
	return strings.TrimFunc(s, func(r rune) bool {
		if r < 0x80 {
			return r == ' ' || (r >= '\t' && r <= '\r')
		}
		return pyIsSpace(r)
	})
}

// pyIntText is Python's int(s) for a str: surrounding whitespace (numStrip), an optional sign, then Unicode decimal
// digits (\p{Nd}) with single underscores between digits. ok is false where Python raises ValueError. Values that do
// not fit an int64 are not ok (Python would hold them; no caller here needs them).
func pyIntText(s string) (int64, bool) {
	t := numStrip(s)
	neg := false
	if t != "" && (t[0] == '+' || t[0] == '-') {
		neg = t[0] == '-'
		t = t[1:]
	}
	if t == "" {
		return 0, false
	}
	var n int64
	prevDigit := false
	for _, r := range t {
		if r == '_' {
			if !prevDigit {
				return 0, false
			}
			prevDigit = false
			continue
		}
		if !unicode.Is(unicode.Nd, r) {
			return 0, false
		}
		d := int64(digitValue(r))
		if n > (math.MaxInt64-d)/10 {
			return 0, false
		}
		n = n*10 + d
		prevDigit = true
	}
	if !prevDigit { // trailing underscore
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

var floatTextRe = regexp.MustCompile(`(?i)^[+-]?(([0-9]+\.?[0-9]*|\.[0-9]+)(e[+-]?[0-9]+)?|inf|infinity|nan)\z`)

// pyFloatText is Python's float(s) for a str: surrounding whitespace (numStrip), Unicode decimal digits, single
// underscores between digits, and the inf / nan spellings; ok is false where Python raises ValueError (hex floats, which
// Go would accept, included).
func pyFloatText(s string) (float64, bool) {
	t := numStrip(s)
	if t == "" {
		return 0, false
	}
	var b strings.Builder
	prev := ' '
	for _, r := range []rune(t) {
		switch {
		case r == '_':
			if !(prev >= '0' && prev <= '9') {
				return 0, false
			}
			prev = '_'
			continue
		case unicode.Is(unicode.Nd, r):
			r = rune('0' + digitValue(r))
		case r > unicode.MaxASCII:
			return 0, false
		}
		if prev == '_' && !(r >= '0' && r <= '9') {
			return 0, false // an underscore must sit between two digits
		}
		b.WriteRune(r)
		prev = r
	}
	if prev == '_' {
		return 0, false
	}
	t = b.String()
	if !floatTextRe.MatchString(t) {
		return 0, false
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		if ne, ok := err.(*strconv.NumError); ok && ne.Err == strconv.ErrRange {
			return f, true // Python float("1e999") is inf
		}
		return 0, false
	}
	return f, true
}

// pyFields is str.split() with no argument: runs of Python whitespace separate the fields.
func pyFields(s string) []string { return strings.FieldsFunc(s, pyIsSpace) }
