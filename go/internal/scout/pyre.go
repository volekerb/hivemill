// Package scout is the Go port of the non-sandbox core of the skill scout (scout/sources.py, vendor.py, risk.py,
// report.py, db.py, orgs.py, schedule.py). Fetched third-party content is DATA: it is read and scanned, never
// executed and never followed as instructions.
//
// Python's `re` has lookarounds and Unicode-aware \s, \w and \b; Go's RE2 has neither. The helpers here keep the
// two in step: whitespace and word classes are spelled out, and the few lookarounds are done by hand. Where the
// ASCII \b of RE2 still differs it matches MORE than Python, so a risk flag can only be added, never dropped.
package scout

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Character classes for use inside regexp source: Python's Unicode \s and \w.
const (
	wsClass   = ` \t\n\v\f\r\x1c-\x1f\x85\xa0\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`
	wordClass = `\p{L}\p{N}_`
)

// isPySpace is str.isspace(): unicode.IsSpace plus the four ASCII separators \x1c-\x1f.
func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// isWordRune is Python's \w: a letter, a number or an underscore.
func isWordRune(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

func pyFields(s string) []string { return strings.FieldsFunc(s, isPySpace) }

func pyTrim(s string) string { return strings.TrimFunc(s, isPySpace) }

// prevNotWordDash is the lookbehind (?<![\w-]) at byte offset i of s.
func prevNotWordDash(s string, i int) bool {
	if i == 0 {
		return true
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return !(r == '-' || isWordRune(r))
}

// decodeReplace is bytes.decode("utf-8", "replace"): one U+FFFD per undecodable byte.
func decodeReplace(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			sb.WriteRune(utf8.RuneError)
		} else {
			sb.Write(b[:n])
		}
		b = b[n:]
	}
	return sb.String()
}

// runeLen is len(str) in Python: code points.
func runeLen(s string) int { return utf8.RuneCountInString(s) }

// runePrefix is s[:n] in Python (n code points).
func runePrefix(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := 0
	for ; n > 0 && i < len(s); n-- {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return s[:i]
}

// rfindExt is low[low.rfind("."):] for a lower-cased name: the extension, or "" when there is no dot.
func rfindExt(low string) string {
	i := strings.LastIndex(low, ".")
	if i < 0 {
		return ""
	}
	return low[i:]
}
