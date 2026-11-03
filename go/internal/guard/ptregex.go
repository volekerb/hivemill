package guard

import (
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"
)

// Python's re treats \w \d \s (and their negations) as Unicode classes in a str pattern; RE2 treats them as ASCII.
// pyRegexToRE2 rewrites the shorthands of an allowlist regex to the Unicode classes the rest of the port uses
// (reWord, \p{Nd}, rxSpace) so the same line matches the same paths (T-0887).

var (
	negOnce                     sync.Once
	negWord, negDigit, negSpace string
)

// complementRanges is the RE2 class body of every rune (surrogates excluded) for which in is false.
func complementRanges(in func(rune) bool) string {
	var sb strings.Builder
	start := rune(-1)
	flush := func(end rune) {
		if start < 0 {
			return
		}
		if start == end {
			fmt.Fprintf(&sb, `\x{%x}`, start)
		} else {
			fmt.Fprintf(&sb, `\x{%x}-\x{%x}`, start, end)
		}
		start = -1
	}
	for r := rune(0); r <= utf8.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			flush(r - 1)
			continue
		}
		if in(r) {
			flush(r - 1)
		} else if start < 0 {
			start = r
		}
	}
	flush(utf8.MaxRune)
	return sb.String()
}

func initNeg() {
	negWord = complementRanges(isWord)
	negDigit = complementRanges(isDecimal)
	negSpace = complementRanges(pyIsSpace)
}

// pyRegexToRE2 returns the RE2 source with the same meaning as the Python str pattern p, or an error for a construct
// RE2 cannot express (\b, \B: RE2 has ASCII word boundaries only), so the caller fails closed.
func pyRegexToRE2(p string) (string, error) {
	negOnce.Do(initNeg)
	var sb strings.Builder
	inClass := false
	for i := 0; i < len(p); {
		c := p[i]
		switch {
		case c == '\\' && i+1 < len(p):
			e := p[i+1]
			i += 2
			var pos, neg, outer string
			switch e {
			case 'w':
				pos = `\p{L}\p{N}_`
			case 'W':
				neg, outer = negWord, `[^\p{L}\p{N}_]`
			case 'd':
				pos = `\p{Nd}`
			case 'D':
				neg, outer = negDigit, `\P{Nd}`
			case 's':
				pos = rxSpace
			case 'S':
				neg, outer = negSpace, `[^`+rxSpace+`]`
			case 'b', 'B':
				if !inClass {
					return "", fmt.Errorf(`\%c is not supported (RE2 has no Unicode word boundary); spell the boundary out`, e)
				}
				sb.WriteString(`\` + string(e))
				continue
			default:
				sb.WriteByte('\\')
				if i-1 < len(p) {
					_, n := utf8.DecodeRuneInString(p[i-1:])
					sb.WriteString(p[i-1 : i-1+n])
					i += n - 1
				}
				continue
			}
			switch {
			case pos != "" && inClass:
				sb.WriteString(pos)
			case pos != "":
				sb.WriteString(`[` + pos + `]`)
			case inClass:
				sb.WriteString(neg)
			default:
				sb.WriteString(outer)
			}
		case inClass:
			if c == ']' {
				inClass = false
			}
			sb.WriteByte(c)
			i++
		case c == '[':
			inClass = true
			sb.WriteByte(c)
			i++
			if i < len(p) && p[i] == '^' {
				sb.WriteByte('^')
				i++
			}
			if i < len(p) && p[i] == ']' { // a leading ] is a literal
				sb.WriteString(`\]`)
				i++
			}
		default:
			sb.WriteByte(c)
			i++
		}
	}
	return sb.String(), nil
}

// pyUTF8Error is the text of the UnicodeDecodeError that bytes.decode('utf-8') raises for b, "" when b is valid.
func pyUTF8Error(b []byte) string {
	if utf8.Valid(b) {
		return ""
	}
	for i := 0; i < len(b); {
		if r, n := utf8.DecodeRune(b[i:]); r != utf8.RuneError || n > 1 {
			i += n
			continue
		}
		lead := b[i]
		need, lo, hi := 0, byte(0x80), byte(0xbf)
		switch {
		case lead >= 0xc2 && lead <= 0xdf:
			need = 1
		case lead >= 0xe0 && lead <= 0xef:
			need = 2
			if lead == 0xe0 {
				lo = 0xa0
			} else if lead == 0xed {
				hi = 0x9f
			}
		case lead >= 0xf0 && lead <= 0xf4:
			need = 3
			if lead == 0xf0 {
				lo = 0x90
			} else if lead == 0xf4 {
				hi = 0x8f
			}
		default:
			return fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: invalid start byte", lead, i)
		}
		end, why := i+1, "unexpected end of data"
		for k := 0; k < need; k++ {
			if end >= len(b) {
				break
			}
			if b[end] < lo || b[end] > hi {
				why = "invalid continuation byte"
				break
			}
			lo, hi = 0x80, 0xbf
			end++
			if k == need-1 {
				why = "" // cannot happen: DecodeRune accepted it
			}
		}
		if end-i == 1 {
			return fmt.Sprintf("'utf-8' codec can't decode byte 0x%02x in position %d: %s", lead, i, why)
		}
		return fmt.Sprintf("'utf-8' codec can't decode bytes in position %d-%d: %s", i, end-1, why)
	}
	return ""
}
