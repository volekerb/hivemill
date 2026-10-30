// Package guard is the Go port of the worker-guard hook (roles/hooks/worker-guard.py).
// This file holds the Python-semantics helpers the port needs: Python's str.split(),
// \w / \d, os.path.basename and fnmatch.fnmatchcase. They are written out because
// RE2 and the Go standard library differ from Python on exactly these points.
package guard

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// pyIsSpace is str.isspace() / the regex \s of a Python str pattern: unicode.IsSpace plus
// the separators U+001C..U+001F that Python counts as whitespace and Go does not.
func pyIsSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyFields is str.split() with no argument.
func pyFields(s string) []string { return strings.FieldsFunc(s, pyIsSpace) }

// isWord is the regex \w of a Python str pattern: letters, numbers and underscore.
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// isDecimal is the regex \d of a Python str pattern (category Nd).
func isDecimal(r rune) bool { return unicode.Is(unicode.Nd, r) }

// pyBasename is os.path.basename: everything after the last slash.
func pyBasename(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// prevRune is the rune before byte index i, or -1 at the start (a lookbehind sees nothing).
func prevRune(s string, i int) rune {
	if i <= 0 {
		return -1
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r
}

// nextRune is the rune at byte index i, or -1 at the end.
func nextRune(s string, i int) rune {
	if i >= len(s) {
		return -1
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r
}

// wordBefore reports whether the rune before i is a \w character (false at the start).
func wordBefore(s string, i int) bool { r := prevRune(s, i); return r >= 0 && isWord(r) }

// wordAt reports whether the rune at i is a \w character (false at the end).
func wordAt(s string, i int) bool { r := nextRune(s, i); return r >= 0 && isWord(r) }

// boundary is the regex \b at byte index i.
func boundary(s string, i int) bool { return wordBefore(s, i) != wordAt(s, i) }

func set(words ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(words))
	for _, w := range words {
		m[w] = struct{}{}
	}
	return m
}

func in(m map[string]struct{}, k string) bool { _, ok := m[k]; return ok }

// fnmatchCase is fnmatch.fnmatchcase(name, pat) of CPython 3.13: `*` and `?` match any
// character (also "/"), `[seq]` / `[!seq]` classes, no backslash escape.
func fnmatchCase(name, pat string) bool {
	toks := fnTranslate([]rune(pat))
	n := []rune(name)
	// reach[i] = the tokens consumed so far can end with i name runes consumed
	cur := map[int]bool{0: true}
	for _, t := range toks {
		next := map[int]bool{}
		for i := range cur {
			switch t.kind {
			case fnStar:
				for j := i; j <= len(n); j++ {
					next[j] = true
				}
			case fnAny:
				if i < len(n) {
					next[i+1] = true
				}
			case fnClass:
				if i < len(n) && t.matches(n[i]) {
					next[i+1] = true
				}
			case fnLit:
				if i < len(n) && n[i] == t.lit {
					next[i+1] = true
				}
			}
		}
		cur = next
	}
	return cur[len(n)]
}

const (
	fnLit = iota
	fnAny
	fnStar
	fnClass
)

type fnTok struct {
	kind   int
	lit    rune
	neg    bool
	ranges [][2]rune
}

func (t fnTok) matches(r rune) bool {
	hit := false
	for _, rg := range t.ranges {
		if r >= rg[0] && r <= rg[1] {
			hit = true
			break
		}
	}
	return hit != t.neg
}

// fnTranslate follows fnmatch.translate: the class text is rewritten the way CPython does (reversed
// ranges dropped, backslash literal) and then read back with the rules of a regex character class.
func fnTranslate(pat []rune) []fnTok {
	var toks []fnTok
	i, n := 0, len(pat)
	for i < n {
		c := pat[i]
		i++
		switch c {
		case '*':
			toks = append(toks, fnTok{kind: fnStar})
		case '?':
			toks = append(toks, fnTok{kind: fnAny})
		case '[':
			j := i
			if j < n && pat[j] == '!' {
				j++
			}
			if j < n && pat[j] == ']' {
				j++
			}
			for j < n && pat[j] != ']' {
				j++
			}
			if j >= n {
				toks = append(toks, fnTok{kind: fnLit, lit: '['})
				continue
			}
			toks = append(toks, fnClassTok(pat[i:j]))
			i = j + 1
		default:
			toks = append(toks, fnTok{kind: fnLit, lit: c})
		}
	}
	return toks
}

func fnClassTok(stuff []rune) fnTok {
	var s []rune
	if !containsRune(stuff, '-') {
		s = escapeClass(stuff)
	} else {
		var chunks [][]rune
		start, k := 0, 1
		if stuff[0] == '!' {
			k = 2
		}
		for {
			idx := indexRune(stuff, '-', k)
			if idx < 0 {
				break
			}
			chunks = append(chunks, stuff[start:idx])
			start, k = idx+1, idx+3
		}
		if last := stuff[start:]; len(last) > 0 {
			chunks = append(chunks, last)
		} else {
			chunks[len(chunks)-1] = append(append([]rune{}, chunks[len(chunks)-1]...), '-')
		}
		// drop empty (reversed) ranges, as fnmatch.translate does
		for k := len(chunks) - 1; k > 0; k-- {
			a, b := chunks[k-1], chunks[k]
			if len(a) > 0 && len(b) > 0 && a[len(a)-1] > b[0] {
				chunks[k-1] = append(append([]rune{}, a[:len(a)-1]...), b[1:]...)
				chunks = append(chunks[:k], chunks[k+1:]...)
			}
		}
		for ci, ch := range chunks {
			if ci > 0 {
				s = append(s, '-')
			}
			s = append(s, escapeClass(ch)...)
		}
	}
	switch {
	case len(s) == 0:
		return fnTok{kind: fnClass} // "(?!)": matches nothing
	case len(s) == 1 && s[0] == '!':
		return fnTok{kind: fnAny}
	}
	t := fnTok{kind: fnClass}
	if s[0] == '!' {
		t.neg = true
		s = s[1:]
	}
	t.ranges = classRanges(s)
	return t
}

// escapeClass escapes backslash and hyphen the way fnmatch.translate does before the chunks are joined.
func escapeClass(s []rune) []rune {
	var out []rune
	for _, r := range s {
		if r == '\\' || r == '-' {
			out = append(out, '\\')
		}
		out = append(out, r)
	}
	return out
}

func containsRune(s []rune, r rune) bool { return indexRune(s, r, 0) >= 0 }

func indexRune(s []rune, r rune, from int) int {
	for i := from; i < len(s); i++ {
		if s[i] == r {
			return i
		}
	}
	return -1
}

// classRanges reads regex character-class text: `\X` is the literal X, an unescaped '-' between two
// items is a range, everything else is a literal.
func classRanges(s []rune) [][2]rune {
	var out [][2]rune
	item := func(i int) (rune, int) {
		if s[i] == '\\' && i+1 < len(s) {
			return s[i+1], i + 2
		}
		return s[i], i + 1
	}
	for i := 0; i < len(s); {
		lo, ni := item(i)
		if ni+1 < len(s) && s[ni] == '-' {
			hi, nj := item(ni + 1)
			if hi >= lo {
				out = append(out, [2]rune{lo, hi})
			}
			i = nj
			continue
		}
		out = append(out, [2]rune{lo, lo})
		i = ni
	}
	return out
}

// runeLen is the byte length of the rune at i (1 for an invalid byte, so loops always advance).
func runeLen(s string, i int) int { _, n := utf8.DecodeRuneInString(s[i:]); return n }
