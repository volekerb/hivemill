package main

// Ordered JSON with Python's json module semantics: the stateful herdr stub reads and writes the state file
// byte-for-byte like tests/stubs/herdr (dict order kept, ", " and ": " separators, ensure_ascii, NaN/Infinity
// accepted, ints of any size, floats printed with Python's repr).

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf8"
)

// obj is a JSON object that keeps insertion order. A repeated key keeps its first position and the last value, as a
// Python dict built by json.load does.
type obj struct {
	keys []string
	m    map[string]any
}

func newObj() *obj { return &obj{m: map[string]any{}} }

func (o *obj) set(k string, v any) {
	if _, ok := o.m[k]; !ok {
		o.keys = append(o.keys, k)
	}
	o.m[k] = v
}

func (o *obj) get(k string) (any, bool) { v, ok := o.m[k]; return v, ok }

// obj of key/value pairs given in order: kv("a", 1, "b", 2).
func kv(pairs ...any) *obj {
	o := newObj()
	for i := 0; i+1 < len(pairs); i += 2 {
		o.set(pairs[i].(string), pairs[i+1])
	}
	return o
}

// pyInt is a Python int of any size.
type pyInt struct{ v *big.Int }

// pyFloat is a Python float.
type pyFloat float64

func intOf(n int) pyInt { return pyInt{big.NewInt(int64(n))} }

// decodeError is a JSON syntax error; Python raises json.JSONDecodeError.
type decodeError struct{ msg string }

func (e decodeError) Error() string { return e.msg }

type parser struct {
	s   string
	pos int
}

// parseJSON parses text like Python's json.loads: a single value, optional surrounding JSON whitespace.
func parseJSON(s string) (v any, err error) {
	p := &parser{s: s}
	defer func() {
		if r := recover(); r != nil {
			de, ok := r.(decodeError)
			if !ok {
				panic(r)
			}
			v, err = nil, de
		}
	}()
	p.ws()
	v = p.value(0)
	p.ws()
	if p.pos != len(p.s) {
		p.fail("Extra data")
	}
	return v, nil
}

func (p *parser) fail(what string) {
	panic(decodeError{fmt.Sprintf("%s: char %d", what, p.pos)})
}

func (p *parser) ws() {
	for p.pos < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.pos]) >= 0 {
		p.pos++
	}
}

const maxDepth = 500 // Python's default recursion limit is 1000 frames; a deeper document is refused, not crashed on

func (p *parser) value(depth int) any {
	if depth > maxDepth {
		p.fail("nesting too deep")
	}
	if p.pos >= len(p.s) {
		p.fail("Expecting value")
	}
	switch c := p.s[p.pos]; {
	case c == '{':
		return p.object(depth)
	case c == '[':
		return p.array(depth)
	case c == '"':
		return p.str()
	case strings.HasPrefix(p.s[p.pos:], "true"):
		p.pos += 4
		return true
	case strings.HasPrefix(p.s[p.pos:], "false"):
		p.pos += 5
		return false
	case strings.HasPrefix(p.s[p.pos:], "null"):
		p.pos += 4
		return nil
	case strings.HasPrefix(p.s[p.pos:], "NaN"):
		p.pos += 3
		return pyFloat(math.NaN())
	case strings.HasPrefix(p.s[p.pos:], "Infinity"):
		p.pos += 8
		return pyFloat(math.Inf(1))
	case strings.HasPrefix(p.s[p.pos:], "-Infinity"):
		p.pos += 9
		return pyFloat(math.Inf(-1))
	case c == '-' || (c >= '0' && c <= '9'):
		return p.number()
	}
	p.fail("Expecting value")
	return nil
}

func (p *parser) object(depth int) any {
	p.pos++ // {
	o := newObj()
	p.ws()
	if p.pos < len(p.s) && p.s[p.pos] == '}' {
		p.pos++
		return o
	}
	for {
		p.ws()
		if p.pos >= len(p.s) || p.s[p.pos] != '"' {
			p.fail("Expecting property name enclosed in double quotes")
		}
		k := p.str()
		p.ws()
		if p.pos >= len(p.s) || p.s[p.pos] != ':' {
			p.fail("Expecting ':' delimiter")
		}
		p.pos++
		p.ws()
		o.set(k, p.value(depth+1))
		p.ws()
		if p.pos >= len(p.s) {
			p.fail("Expecting ',' delimiter")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			return o
		default:
			p.fail("Expecting ',' delimiter")
		}
	}
}

func (p *parser) array(depth int) any {
	p.pos++ // [
	a := []any{}
	p.ws()
	if p.pos < len(p.s) && p.s[p.pos] == ']' {
		p.pos++
		return a
	}
	for {
		p.ws()
		a = append(a, p.value(depth+1))
		p.ws()
		if p.pos >= len(p.s) {
			p.fail("Expecting ',' delimiter")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case ']':
			p.pos++
			return a
		default:
			p.fail("Expecting ',' delimiter")
		}
	}
}

// str parses a string. A lone surrogate escape is kept as its three-byte WTF-8 form so it dumps back unchanged.
func (p *parser) str() string {
	p.pos++ // opening quote
	var b strings.Builder
	for {
		if p.pos >= len(p.s) {
			p.fail("Unterminated string starting at")
		}
		c := p.s[p.pos]
		switch {
		case c == '"':
			p.pos++
			return b.String()
		case c < 0x20:
			p.fail("Invalid control character at")
		case c == '\\':
			p.pos++
			if p.pos >= len(p.s) {
				p.fail("Unterminated string starting at")
			}
			e := p.s[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				r := p.hex4()
				if r >= 0xd800 && r < 0xdc00 && strings.HasPrefix(p.s[p.pos:], `\u`) {
					save := p.pos
					p.pos += 2
					lo := p.hex4()
					if lo >= 0xdc00 && lo < 0xe000 {
						r = 0x10000 + (r-0xd800)<<10 + (lo - 0xdc00)
					} else {
						p.pos = save
					}
				}
				writeWTF8(&b, r)
			default:
				p.fail("Invalid \\escape")
			}
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
}

func (p *parser) hex4() rune {
	if p.pos+4 > len(p.s) {
		p.fail("Invalid \\uXXXX escape")
	}
	n, err := strconv.ParseUint(p.s[p.pos:p.pos+4], 16, 32)
	if err != nil || strings.ContainsAny(p.s[p.pos:p.pos+4], "+-_ ") {
		p.fail("Invalid \\uXXXX escape")
	}
	p.pos += 4
	return rune(n)
}

// writeWTF8 writes r; a surrogate (not valid UTF-8) is written as its generalized three-byte form.
func writeWTF8(b *strings.Builder, r rune) {
	if r >= 0xd800 && r < 0xe000 {
		b.WriteByte(0xe0 | byte(r>>12))
		b.WriteByte(0x80 | byte(r>>6)&0x3f)
		b.WriteByte(0x80 | byte(r)&0x3f)
		return
	}
	b.WriteRune(r)
}

func (p *parser) number() any {
	start := p.pos
	if p.s[p.pos] == '-' {
		p.pos++
	}
	digits := func() int {
		n := 0
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
			n++
		}
		return n
	}
	if p.pos < len(p.s) && p.s[p.pos] == '0' {
		p.pos++
	} else if digits() == 0 {
		p.fail("Expecting value")
	}
	isFloat := false
	if p.pos+1 < len(p.s) && p.s[p.pos] == '.' && p.s[p.pos+1] >= '0' && p.s[p.pos+1] <= '9' {
		isFloat = true
		p.pos++
		digits()
	}
	if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		q := p.pos + 1
		if q < len(p.s) && (p.s[q] == '+' || p.s[q] == '-') {
			q++
		}
		if q < len(p.s) && p.s[q] >= '0' && p.s[q] <= '9' {
			isFloat = true
			p.pos = q
			digits()
		}
	}
	text := p.s[start:p.pos]
	if isFloat {
		f, err := strconv.ParseFloat(text, 64)
		if err != nil && !math.IsInf(f, 0) { // out of range is inf in Python too
			p.fail("Expecting value")
		}
		return pyFloat(f)
	}
	n, ok := new(big.Int).SetString(text, 10)
	if !ok {
		p.fail("Expecting value")
	}
	return pyInt{n}
}

// ---- dump ------------------------------------------------------------------------------------------------------

// dumps is json.dumps(v): separators ", " and ": ", ensure_ascii, dict order kept.
func dumps(v any) string {
	var b strings.Builder
	dump(&b, v)
	return b.String()
}

func dump(b *strings.Builder, v any) {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		if x {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case string:
		b.WriteString(pyStrWTF(x))
	case pyInt:
		b.WriteString(x.v.String())
	case pyFloat:
		b.WriteString(floatRepr(float64(x)))
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteString(", ")
			}
			dump(b, e)
		}
		b.WriteByte(']')
	case *obj:
		b.WriteByte('{')
		for i, k := range x.keys {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteString(pyStrWTF(k))
			b.WriteString(": ")
			dump(b, x.m[k])
		}
		b.WriteByte('}')
	default:
		panic(fmt.Sprintf("stub bug: dump of %T", v))
	}
}

// floatRepr is Python's repr(float) as json.dumps writes it (NaN, Infinity, -Infinity for the special values).
func floatRepr(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	// shortest digits, then Python's layout: scientific when the decimal exponent is < -4 or >= 16
	s := strconv.FormatFloat(f, 'e', -1, 64) // d.ddde±XX
	mant, expS, _ := strings.Cut(s, "e")
	exp, _ := strconv.Atoi(expS)
	neg := strings.HasPrefix(mant, "-")
	mant = strings.TrimPrefix(mant, "-")
	digits := strings.Replace(mant, ".", "", 1)
	var out string
	switch {
	case exp < -4 || exp >= 16:
		m := digits[:1]
		if len(digits) > 1 {
			m += "." + digits[1:]
		}
		sign := "+"
		if exp < 0 {
			sign, exp = "-", -exp
		}
		out = fmt.Sprintf("%se%s%02d", m, sign, exp)
	case exp < 0:
		out = "0." + strings.Repeat("0", -exp-1) + digits
	case len(digits) <= exp+1:
		out = digits + strings.Repeat("0", exp+1-len(digits)) + ".0"
	default:
		out = digits[:exp+1] + "." + digits[exp+1:]
	}
	if neg {
		out = "-" + out
	}
	return out
}

// pyStrWTF is json.dumps of a str held as WTF-8: a surrogate in its three-byte form is written as \udXXX, any
// other invalid byte as the surrogate Python's argv decoding would make of it (\udcXX).
func pyStrWTF(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); {
		if s[i] == 0xed && i+2 < len(s) && s[i+1] >= 0xa0 && s[i+1] <= 0xbf && s[i+2]&0xc0 == 0x80 {
			r := rune(s[i]&0x0f)<<12 | rune(s[i+1]&0x3f)<<6 | rune(s[i+2]&0x3f)
			fmt.Fprintf(&b, `\u%04x`, r)
			i += 3
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			fmt.Fprintf(&b, `\u%04x`, 0xdc00+int(s[i]))
			i++
			continue
		}
		escRune(&b, r)
		i += size
	}
	b.WriteByte('"')
	return b.String()
}

// escRune writes one rune the way Python's json.dumps(ensure_ascii=True) does.
func escRune(b *strings.Builder, r rune) {
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
		fmt.Fprintf(b, `\u%04x`, r)
	case r < 0x7f:
		b.WriteRune(r)
	case r > 0xffff:
		r -= 0x10000
		fmt.Fprintf(b, `\u%04x\u%04x`, 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	default:
		fmt.Fprintf(b, `\u%04x`, r)
	}
}
