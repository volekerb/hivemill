package changelog

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Python's re module is Unicode-aware for str patterns (\s, \d, \w, \b); Go's RE2 classes are ASCII only. These
// helpers give the Go port Python's behaviour on non-ASCII commit subjects and notes.

// pyWS is Python's \s for str: every character str.isspace accepts.
const pyWS = `\t\n\v\f\r \x{1c}-\x{1f}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

var (
	prSuffix = regexp.MustCompile(`[` + pyWS + `]*\(#\p{Nd}+\)$`)
	// datePat has no \b: dateRe matches the shape and replaceDates checks the two word boundaries Python's \b adds.
	dateRe = regexp.MustCompile(`^[` + pyWS + `]*(\()?(\p{Nd}{4}-\p{Nd}{2}-\p{Nd}{2})(\))?`)
	// upgradeLine is Python's ^### Upgrade\s*$ with re.M: \s* may cross newlines but $ then still matches before the first one.
	upgradeLine = regexp.MustCompile(`(?m)^### Upgrade[` + strings.ReplaceAll(pyWS, `\n`, "") + `]*$`)
)

func isPySpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyStrip is Python's str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// isWord is Python's \w for str: alphanumeric or underscore.
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

func wordBefore(s string, i int) bool {
	r, n := utf8.DecodeLastRuneInString(s[:i])
	return n > 0 && isWord(r)
}

func wordAt(s string, i int) bool {
	r, n := utf8.DecodeRuneInString(s[i:])
	return n > 0 && isWord(r)
}

// matchWords reports whether re (a pattern starting and ending with a word character) matches s at a word
// boundary on the left, and on the right too when rightBound is set: Python's \b(...) and \b(...)\b.
func matchWords(re *regexp.Regexp, s string, rightBound bool) bool {
	for i := 0; i <= len(s); {
		loc := re.FindStringIndex(s[i:])
		if loc == nil {
			return false
		}
		start, end := i+loc[0], i+loc[1]
		if !wordBefore(s, start) && (!rightBound || !wordAt(s, end)) {
			return true
		}
		_, n := utf8.DecodeRuneInString(s[start:])
		i = start + max(n, 1)
	}
	return false
}

// replaceDates is DATE.sub("", s) for DATE = \s*\(?\b\d{4}-\d{2}-\d{2}\b\)?.
func replaceDates(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if m := dateRe.FindStringSubmatchIndex(s[i:]); m != nil {
			digitsStart, digitsEnd := i+m[4], i+m[5]
			// \b before the digits: always true after "(" or whitespace; at i itself it depends on the previous character.
			leftOK := m[2] >= 0 || digitsStart > i || !wordBefore(s, digitsStart)
			if leftOK && !wordAt(s, digitsEnd) {
				i += m[1]
				continue
			}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && n <= 1 {
			b.WriteByte(s[i])
			i++
			continue
		}
		b.WriteString(s[i : i+n])
		i += n
	}
	return b.String()
}
