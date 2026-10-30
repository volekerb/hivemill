package guard

import (
	"fmt"
	"strings"
)

// PyJSONError is the message of the json.JSONDecodeError that Python 3.13's json.loads(text) raises, or "" when text
// is valid JSON by Python's rules. The hook prints it on a bad payload ("hook error, failing closed: JSONDecodeError:
// ..."), and the differential tests compare stderr text, so the wording and the character position must match.
// Offsets are in characters (runes), as in Python.
func PyJSONError(text string) string {
	s := []rune(text)
	if len(s) > 0 && s[0] == '\ufeff' {
		return pyJSONMsg("Unexpected UTF-8 BOM (decode using utf-8-sig)", s, 0)
	}
	p := &pyJSON{s: s}
	i := p.ws(0)
	end, msg := p.value(i)
	if msg != "" {
		return msg
	}
	if end = p.ws(end); end != len(s) {
		return pyJSONMsg("Extra data", s, end)
	}
	return ""
}

type pyJSON struct{ s []rune }

func pyJSONMsg(what string, s []rune, pos int) string {
	line, last := 1, -1
	for i := 0; i < pos; i++ {
		if s[i] == '\n' {
			line++
			last = i
		}
	}
	return fmt.Sprintf("JSONDecodeError: %s: line %d column %d (char %d)", what, line, pos-last, pos)
}

func (p *pyJSON) at(i int) rune {
	if i < len(p.s) {
		return p.s[i]
	}
	return -1
}

func (p *pyJSON) ws(i int) int {
	for i < len(p.s) && (p.s[i] == ' ' || p.s[i] == '\t' || p.s[i] == '\n' || p.s[i] == '\r') {
		i++
	}
	return i
}

func (p *pyJSON) lit(i int, word string) bool {
	r := []rune(word)
	return i+len(r) <= len(p.s) && string(p.s[i:i+len(r)]) == word
}

func isDigit(r rune) bool { return r >= '0' && r <= '9' }

// value is scan_once: the end offset of the value at i, or the error message.
func (p *pyJSON) value(i int) (int, string) {
	switch c := p.at(i); {
	case c == '"':
		return p.str(i + 1)
	case c == '{':
		return p.object(i + 1)
	case c == '[':
		return p.array(i + 1)
	case c == 'n' && p.lit(i, "null"):
		return i + 4, ""
	case c == 't' && p.lit(i, "true"):
		return i + 4, ""
	case c == 'f' && p.lit(i, "false"):
		return i + 5, ""
	case c == 'N' && p.lit(i, "NaN"):
		return i + 3, ""
	case c == 'I' && p.lit(i, "Infinity"):
		return i + 8, ""
	case c == '-' && p.lit(i, "-Infinity"):
		return i + 9, ""
	case c == '-' || isDigit(c):
		j := i
		if p.at(j) == '-' {
			j++
		}
		switch {
		case p.at(j) == '0':
			j++
		case p.at(j) >= '1' && p.at(j) <= '9':
			for isDigit(p.at(j)) {
				j++
			}
		default:
			return i, pyJSONMsg("Expecting value", p.s, i)
		}
		if p.at(j) == '.' && isDigit(p.at(j+1)) {
			for j++; isDigit(p.at(j)); {
				j++
			}
		}
		if e := p.at(j); e == 'e' || e == 'E' {
			k := j + 1
			if p.at(k) == '+' || p.at(k) == '-' {
				k++
			}
			if isDigit(p.at(k)) {
				for isDigit(p.at(k)) {
					k++
				}
				j = k
			}
		}
		return j, ""
	}
	return i, pyJSONMsg("Expecting value", p.s, i)
}

// str is py_scanstring (strict): i is just after the opening quote.
func (p *pyJSON) str(i int) (int, string) {
	begin := i - 1
	for {
		c := p.at(i)
		switch {
		case c == -1:
			return i, pyJSONMsg("Unterminated string starting at", p.s, begin)
		case c == '"':
			return i + 1, ""
		case c < 0x20:
			return i, pyJSONMsg("Invalid control character at", p.s, i)
		case c != '\\':
			i++
			continue
		}
		e := p.at(i + 1)
		switch {
		case e == -1:
			return i, pyJSONMsg("Unterminated string starting at", p.s, begin)
		case e != 'u':
			if !strings.ContainsRune(`"\/bfnrt`, e) {
				return i, pyJSONMsg(`Invalid \escape`, p.s, i)
			}
			i += 2
			continue
		}
		if !p.hex4(i + 2) {
			return i, pyJSONMsg(`Invalid \uXXXX escape`, p.s, i+1)
		}
		i += 6
		// a high surrogate followed by \u starts a pair whose second half must be well-formed too
		if h := p.hexVal(i - 4); h >= 0xd800 && h <= 0xdbff && p.at(i) == '\\' && p.at(i+1) == 'u' {
			if !p.hex4(i + 2) {
				return i, pyJSONMsg(`Invalid \uXXXX escape`, p.s, i+1)
			}
			if l := p.hexVal(i + 2); l >= 0xdc00 && l <= 0xdfff {
				i += 6
			}
		}
	}
}

func (p *pyJSON) hex4(i int) bool {
	if i+4 > len(p.s) {
		return false
	}
	for _, r := range p.s[i : i+4] {
		if !(isDigit(r) || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

func (p *pyJSON) hexVal(i int) int {
	n := 0
	for _, r := range p.s[i : i+4] {
		n *= 16
		switch {
		case isDigit(r):
			n += int(r - '0')
		case r >= 'a':
			n += int(r-'a') + 10
		default:
			n += int(r-'A') + 10
		}
	}
	return n
}

// object is JSONObject: i is just after the '{'.
func (p *pyJSON) object(i int) (int, string) {
	const noKey = "Expecting property name enclosed in double quotes"
	i = p.ws(i)
	if p.at(i) == '}' {
		return i + 1, ""
	}
	if p.at(i) != '"' {
		return i, pyJSONMsg(noKey, p.s, i)
	}
	for {
		var msg string
		if i, msg = p.str(i + 1); msg != "" {
			return i, msg
		}
		if i = p.ws(i); p.at(i) != ':' {
			return i, pyJSONMsg("Expecting ':' delimiter", p.s, i)
		}
		if i, msg = p.value(p.ws(i + 1)); msg != "" {
			return i, msg
		}
		i = p.ws(i)
		if p.at(i) == '}' {
			return i + 1, ""
		}
		if p.at(i) != ',' {
			return i, pyJSONMsg("Expecting ',' delimiter", p.s, i)
		}
		comma := i
		if i = p.ws(i + 1); p.at(i) == '}' {
			return i, pyJSONMsg("Illegal trailing comma before end of object", p.s, comma)
		}
		if p.at(i) != '"' {
			return i, pyJSONMsg(noKey, p.s, i)
		}
	}
}

// array is JSONArray: i is just after the '['.
func (p *pyJSON) array(i int) (int, string) {
	i = p.ws(i)
	if p.at(i) == ']' {
		return i + 1, ""
	}
	for {
		var msg string
		if i, msg = p.value(i); msg != "" {
			return i, msg
		}
		i = p.ws(i)
		if p.at(i) == ']' {
			return i + 1, ""
		}
		if p.at(i) != ',' {
			return i, pyJSONMsg("Expecting ',' delimiter", p.s, i)
		}
		comma := i
		if i = p.ws(i + 1); p.at(i) == ']' {
			return i, pyJSONMsg("Illegal trailing comma before end of array", p.s, comma)
		}
	}
}
