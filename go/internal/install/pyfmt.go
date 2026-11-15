// Package install is the Go port of plugins/hivemill/scripts/install.py: the non-interactive Hivemill installer.
// Behaviour (flags, step order, dry-run plan text, exit codes, RESULT line) matches the Python reference; the
// Python str semantics that the reference relies on (Unicode whitespace and digits) are reproduced explicitly here.
package install

import (
	"math/big"
	"regexp"
	"strings"
	"unicode"
)

// isPySpace reports whether r is whitespace for Python's str.isspace and the Unicode `\s` of re.
func isPySpace(r rune) bool {
	if r >= 9 && r <= 13 || r >= 0x1c && r <= 0x20 {
		return true
	}
	switch r {
	case 0x85, 0xa0, 0x1680, 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return r >= 0x2000 && r <= 0x200a
}

// pyStrip is str.strip().
func pyStrip(s string) string { return strings.TrimFunc(s, isPySpace) }

// pyFields is str.split() without arguments.
func pyFields(s string) []string { return strings.FieldsFunc(s, isPySpace) }

// pySplitLines is str.splitlines(): it also breaks at \v, \f, \x1c-\x1e, \x85, U+2028 and U+2029.
func pySplitLines(s string) []string {
	var lines []string
	rs := []rune(s)
	var cur []rune
	for i := 0; i < len(rs); i++ {
		switch r := rs[i]; r {
		case '\n', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
			lines = append(lines, string(cur))
			cur = cur[:0]
		case '\r':
			lines = append(lines, string(cur))
			cur = cur[:0]
			if i+1 < len(rs) && rs[i+1] == '\n' {
				i++
			}
		default:
			cur = append(cur, r)
		}
	}
	if len(cur) > 0 {
		lines = append(lines, string(cur))
	}
	return lines
}

// pyDigit returns the decimal value of a Unicode Nd rune (what Python's int() and `\d` accept).
func pyDigit(r rune) (int, bool) {
	if r >= '0' && r <= '9' {
		return int(r - '0'), true
	}
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	for _, rg := range unicode.Nd.R16 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	for _, rg := range unicode.Nd.R32 {
		if r >= rune(rg.Lo) && r <= rune(rg.Hi) {
			return int(r-rune(rg.Lo)) % 10, true
		}
	}
	return 0, false
}

// pyInt parses a string like Python's int(s): surrounding whitespace, one sign, Unicode digits, single underscores
// between digits.
func pyInt(s string) (*big.Int, bool) {
	s = pyStrip(s)
	neg := false
	if strings.HasPrefix(s, "+") || strings.HasPrefix(s, "-") {
		neg = s[0] == '-'
		s = s[1:]
	}
	var sb strings.Builder
	prevDigit := false
	for _, r := range s {
		if r == '_' {
			if !prevDigit {
				return nil, false
			}
			prevDigit = false
			continue
		}
		d, ok := pyDigit(r)
		if !ok {
			return nil, false
		}
		sb.WriteByte(byte('0' + d))
		prevDigit = true
	}
	if sb.Len() == 0 || !prevDigit {
		return nil, false
	}
	n, ok := new(big.Int).SetString(sb.String(), 10)
	if !ok {
		return nil, false
	}
	if neg {
		n.Neg(n)
	}
	return n, true
}

// digitsToInt converts a run of Unicode decimal digits (as matched by \p{Nd}+) to a number.
func digitsToInt(s string) *big.Int {
	var sb strings.Builder
	for _, r := range s {
		d, _ := pyDigit(r)
		sb.WriteByte(byte('0' + d))
	}
	n, ok := new(big.Int).SetString(sb.String(), 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

// spaceClass is the body of a character class holding Python's Unicode `\s`.
const spaceClass = `\t-\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

var redactRes = []struct {
	re   *regexp.Regexp
	repl string
}{
	{regexp.MustCompile(`gh[opsur]_[A-Za-z0-9]{20,}`), "[REDACTED]"},
	{regexp.MustCompile(`gho_[A-Za-z0-9_]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`github_pat_[A-Za-z0-9_]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`sk-ant-[A-Za-z0-9_\-]{10,}`), "[REDACTED]"},
	{regexp.MustCompile(`(?i)authorization:[^\r\n]*`), "[REDACTED]"},
	// Python: (?<=://)[^/@\s]+@ ; RE2 has no lookbehind, so the "://" is kept through a group.
	{regexp.MustCompile(`(://)[^/@` + spaceClass + `]+@`), "$1[REDACTED]"},
}

// redact masks token-shaped strings and URL credentials.
func redact(text string) string {
	for _, r := range redactRes {
		text = r.re.ReplaceAllString(text, r.repl)
	}
	return text
}
