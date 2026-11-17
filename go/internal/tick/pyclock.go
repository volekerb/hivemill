package tick

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// pyClockNow is digest_clock().timestamp(): $FACTORY_NOW when set and non-empty (Python datetime.fromisoformat, 3.11
// rules; a naive time is in local, nil = the process zone), else the real clock. It implements FACTORY_NOW only; stored now() timestamps come from
// the S2 clock (FACTORY_TEST_NOW) and there is no second now() here. An invalid value fails with Python's
// FactoryNowError text.
func pyClockNow(getenv func(string) string, real func() time.Time, local *time.Location) (float64, error) {
	if local == nil { // nil: the process's own zone (what astimezone() uses for a naive time)
		local = real().Location()
	}
	fixed := getenv("FACTORY_NOW")
	if fixed == "" {
		return epochSeconds(real()), nil
	}
	t, ok := pyFromISO(fixed, local)
	if !ok {
		return 0, fmt.Errorf("FACTORY_NOW must be an ISO timestamp, got %s", fixed)
	}
	return epochSeconds(t), nil
}

// epochSeconds is datetime.timestamp(). UnixNano is undefined before 1678 and after 2262 (it wraps), and
// Python accepts those years, so the seconds and the nanosecond offset are added separately.
func epochSeconds(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return s != ""
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// pyFromISO is datetime.fromisoformat (Python 3.11+): date YYYY-MM-DD, YYYYMMDD, YYYY-Www[-D], YYYYWww[D]; any single
// separator character; time HH[:MM[:SS[.f]]] or HHMM[SS[.f]]; optional Z or +-HH[:MM[:SS[.f]]] offset.
func pyFromISO(s string, local *time.Location) (time.Time, bool) {
	for _, r := range s {
		if r > 127 { // non-ASCII: Python decodes digits oddly; treat as invalid
			return time.Time{}, false
		}
	}
	date, rest := s, ""
	var y, mo, d int
	n, ok := pyDateLen(s)
	if !ok {
		return time.Time{}, false
	}
	date = s[:n]
	if len(s) > n {
		if len(s) == n+1 { // a separator with no time
			return time.Time{}, false
		}
		rest = s[n+1:]
	}
	if y, mo, d, ok = pyParseDate(date); !ok {
		return time.Time{}, false
	}
	hh, mi, ss, ns := 0, 0, 0, 0
	loc := local
	if rest != "" {
		tm, tz, hasTZ := rest, "", false
		if i := strings.IndexAny(rest, "Z+-"); i >= 0 {
			tm, tz, hasTZ = rest[:i], rest[i:], true
		}
		if hh, mi, ss, ns, ok = pyParseTime(tm); !ok {
			return time.Time{}, false
		}
		if hasTZ {
			off, ok := pyParseTZ(tz)
			if !ok {
				return time.Time{}, false
			}
			loc = time.FixedZone("", off)
		}
	}
	if mo < 1 || mo > 12 || d < 1 || y < 1 {
		return time.Time{}, false
	}
	t := time.Date(y, time.Month(mo), d, hh, mi, ss, ns, loc)
	if loc == local { // a naive time that occurs twice (DST end): Python's fold=0 is the first one
		if u := t.Add(-time.Hour); u.Hour() == t.Hour() && u.Minute() == t.Minute() && u.Day() == t.Day() {
			t = u
		}
	}
	if t.Day() != d || int(t.Month()) != mo { // 2026-02-30
		return time.Time{}, false
	}
	return t, true
}

// pyDateLen finds the length of the date part (10 extended, 8 basic, 8 or 10 week forms, 7 basic week).
func pyDateLen(s string) (int, bool) {
	switch {
	case len(s) >= 10 && s[4] == '-' && s[7] == '-' && allDigits(s[:4]+s[5:7]+s[8:10]):
		return 10, true
	case len(s) >= 10 && s[4] == '-' && s[5] == 'W' && s[8] == '-' && allDigits(s[:4]+s[6:8]+s[9:10]):
		return 10, true
	case len(s) >= 8 && s[4] == '-' && s[5] == 'W' && allDigits(s[:4]+s[6:8]) && (len(s) == 8 || !allDigits(s[8:9])):
		return 8, true
	case len(s) >= 8 && allDigits(s[:8]):
		return 8, true
	case len(s) >= 8 && s[4] == 'W' && allDigits(s[:4]+s[5:8]):
		return 8, true
	case len(s) >= 7 && s[4] == 'W' && allDigits(s[:4]+s[5:7]):
		return 7, true
	}
	return 0, false
}

func pyParseDate(d string) (y, m, day int, ok bool) {
	switch {
	case len(d) == 10 && d[4] == '-' && d[5] == 'W', len(d) == 8 && d[4] == 'W', len(d) == 8 && d[4] == '-' && d[5] == 'W', len(d) == 7:
		d2 := strings.ReplaceAll(d, "-", "")
		y, w, wd := atoi(d2[:4]), atoi(d2[5:7]), 1
		if len(d2) == 8 {
			wd = atoi(d2[7:8])
		}
		if w < 1 || w > 53 || wd < 1 || wd > 7 || y < 1 {
			return 0, 0, 0, false
		}
		jan4 := time.Date(y, 1, 4, 0, 0, 0, 0, time.UTC)
		off := (int(jan4.Weekday()) + 6) % 7 // Monday=0
		t := jan4.AddDate(0, 0, -off+(w-1)*7+(wd-1))
		if w == 53 { // Python rejects a week that the year does not have
			if yy, ww := t.ISOWeek(); yy != y || ww != 53 {
				return 0, 0, 0, false
			}
		}
		return t.Year(), int(t.Month()), t.Day(), true
	case len(d) == 10:
		return atoi(d[:4]), atoi(d[5:7]), atoi(d[8:10]), true
	case len(d) == 8:
		return atoi(d[:4]), atoi(d[4:6]), atoi(d[6:8]), true
	}
	return 0, 0, 0, false
}

func pyParseTime(t string) (hh, mi, ss, ns int, ok bool) {
	frac := ""
	if i := strings.IndexAny(t, ".,"); i >= 0 {
		t, frac = t[:i], t[i+1:]
		if !allDigits(frac) {
			return 0, 0, 0, 0, false
		}
	}
	var parts []string
	switch {
	case strings.Contains(t, ":"):
		parts = strings.Split(t, ":")
	case len(t) == 2 || len(t) == 4 || len(t) == 6:
		for i := 0; i < len(t); i += 2 {
			parts = append(parts, t[i:i+2])
		}
	default:
		return 0, 0, 0, 0, false
	}
	if len(parts) > 3 || (frac != "" && len(parts) != 3) {
		return 0, 0, 0, 0, false
	}
	vals := []int{0, 0, 0}
	for i, p := range parts {
		if len(p) != 2 || !allDigits(p) {
			return 0, 0, 0, 0, false
		}
		vals[i] = atoi(p)
	}
	if vals[0] > 23 || vals[1] > 59 || vals[2] > 59 {
		return 0, 0, 0, 0, false
	}
	if len(frac) > 6 {
		frac = frac[:6]
	}
	if frac != "" {
		ns = atoi(frac+strings.Repeat("0", 6-len(frac))) * 1000
	}
	return vals[0], vals[1], vals[2], ns, true
}

// pyParseTZ is the offset suffix: Z, or +-HH[:MM[:SS[.f]]] / +-HHMM[SS]; returns seconds east of UTC.
func pyParseTZ(z string) (int, bool) {
	if z == "Z" {
		return 0, true
	}
	if z == "" || (z[0] != '+' && z[0] != '-') {
		return 0, false
	}
	sign := 1
	if z[0] == '-' {
		sign = -1
	}
	body := z[1:]
	frac := ""
	if i := strings.IndexAny(body, ".,"); i >= 0 {
		body, frac = body[:i], body[i+1:]
		if !allDigits(frac) {
			return 0, false
		}
	}
	var parts []string
	switch {
	case strings.Contains(body, ":"):
		parts = strings.Split(body, ":")
	case len(body) == 2 || len(body) == 4 || len(body) == 6:
		for i := 0; i < len(body); i += 2 {
			parts = append(parts, body[i:i+2])
		}
	default:
		return 0, false
	}
	if len(parts) > 3 || (frac != "" && len(parts) != 3) {
		return 0, false
	}
	v := []int{0, 0, 0}
	for i, p := range parts {
		if len(p) != 2 || !allDigits(p) {
			return 0, false
		}
		v[i] = atoi(p)
	}
	if v[0] > 23 || v[1] > 59 || v[2] > 59 {
		return 0, false
	}
	return sign * (v[0]*3600 + v[1]*60 + v[2]), true // sub-second offsets are below the clock's resolution here
}

// Python json.loads + float() semantics for the one field the prune unit reads (config value -> "at"). A cache value
// is stale when Python's `float(json.loads(v)["at"])` raises (ValueError, KeyError, TypeError, OverflowError) or gives a
// non-finite number, so only the finite float matters: pyFloatAt reports it, or ok=false for "stale because unreadable".

type pyKind int

const (
	pyNull pyKind = iota
	pyBool
	pyInt // integer literal (no fraction or exponent): float(int) can overflow
	pyNum // float literal, NaN, Infinity, -Infinity
	pyStr
	pyList
	pyObj
)

type pyVal struct {
	kind pyKind
	b    bool
	f    float64 // pyNum value; pyInt value (±Inf when beyond float range)
	s    string
	obj  map[string]pyVal // later duplicate keys win, as in Python
	list []pyVal          // pyList elements
}

type pyParser struct {
	s string
	i int
}

// pyParse is json.loads (strict): NaN, Infinity and -Infinity accepted, whitespace = space, \t, \n, \r.
func pyParse(s string) (pyVal, bool) {
	p := &pyParser{s: s}
	p.ws()
	v, ok := p.value()
	if !ok {
		return pyVal{}, false
	}
	p.ws()
	return v, p.i == len(p.s)
}

func (p *pyParser) ws() {
	for p.i < len(p.s) && strings.IndexByte(" \t\n\r", p.s[p.i]) >= 0 {
		p.i++
	}
}

func (p *pyParser) lit(w string) bool {
	if strings.HasPrefix(p.s[p.i:], w) {
		p.i += len(w)
		return true
	}
	return false
}

func (p *pyParser) value() (pyVal, bool) {
	if p.i >= len(p.s) {
		return pyVal{}, false
	}
	switch c := p.s[p.i]; {
	case c == '{':
		return p.object()
	case c == '[':
		return p.list()
	case c == '"':
		s, ok := p.str()
		return pyVal{kind: pyStr, s: s}, ok
	case p.lit("null"):
		return pyVal{kind: pyNull}, true
	case p.lit("true"):
		return pyVal{kind: pyBool, b: true}, true
	case p.lit("false"):
		return pyVal{kind: pyBool}, true
	case p.lit("NaN"):
		return pyVal{kind: pyNum, f: math.NaN()}, true
	case p.lit("Infinity"):
		return pyVal{kind: pyNum, f: math.Inf(1)}, true
	case p.lit("-Infinity"):
		return pyVal{kind: pyNum, f: math.Inf(-1)}, true
	}
	return p.number()
}

func (p *pyParser) number() (pyVal, bool) {
	start := p.i
	if p.i < len(p.s) && p.s[p.i] == '-' {
		p.i++
	}
	digits := func() int {
		n := 0
		for p.i < len(p.s) && p.s[p.i] >= '0' && p.s[p.i] <= '9' {
			p.i++
			n++
		}
		return n
	}
	switch {
	case p.i < len(p.s) && p.s[p.i] == '0':
		p.i++
	case p.i < len(p.s) && p.s[p.i] >= '1' && p.s[p.i] <= '9':
		digits()
	default:
		return pyVal{}, false
	}
	isInt := true
	if p.i+1 < len(p.s) && p.s[p.i] == '.' && p.s[p.i+1] >= '0' && p.s[p.i+1] <= '9' {
		p.i++
		digits()
		isInt = false
	}
	if p.i < len(p.s) && (p.s[p.i] == 'e' || p.s[p.i] == 'E') {
		j := p.i + 1
		if j < len(p.s) && (p.s[j] == '+' || p.s[j] == '-') {
			j++
		}
		if j < len(p.s) && p.s[j] >= '0' && p.s[j] <= '9' {
			p.i = j
			digits()
			isInt = false
		}
	}
	f, err := strconv.ParseFloat(p.s[start:p.i], 64) // a range error still returns ±Inf, which is stale either way
	if err != nil && !math.IsInf(f, 0) {
		return pyVal{}, false
	}
	if isInt {
		return pyVal{kind: pyInt, f: f}, true
	}
	return pyVal{kind: pyNum, f: f}, true
}

func (p *pyParser) str() (string, bool) {
	p.i++ // opening quote
	var sb strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		switch {
		case c == '"':
			p.i++
			return sb.String(), true
		case c < 0x20: // strict mode: no raw control characters
			return "", false
		case c == '\\':
			if p.i+1 >= len(p.s) {
				return "", false
			}
			e := p.s[p.i+1]
			p.i += 2
			switch e {
			case '"', '\\', '/':
				sb.WriteByte(e)
			case 'b':
				sb.WriteByte('\b')
			case 'f':
				sb.WriteByte('\f')
			case 'n':
				sb.WriteByte('\n')
			case 'r':
				sb.WriteByte('\r')
			case 't':
				sb.WriteByte('\t')
			case 'u':
				if p.i+4 > len(p.s) {
					return "", false
				}
				n, err := strconv.ParseUint(p.s[p.i:p.i+4], 16, 32)
				if err != nil || strings.ContainsAny(p.s[p.i:p.i+4], "+-_") {
					return "", false
				}
				p.i += 4
				sb.WriteRune(rune(n))
			default:
				return "", false
			}
		default:
			sb.WriteByte(c)
			p.i++
		}
	}
	return "", false
}

func (p *pyParser) list() (pyVal, bool) {
	p.i++
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == ']' {
		p.i++
		return pyVal{kind: pyList}, true
	}
	var elems []pyVal
	for {
		p.ws()
		e, ok := p.value()
		if !ok {
			return pyVal{}, false
		}
		elems = append(elems, e)
		p.ws()
		if p.i >= len(p.s) {
			return pyVal{}, false
		}
		c := p.s[p.i]
		p.i++
		if c == ']' {
			return pyVal{kind: pyList, list: elems}, true
		}
		if c != ',' {
			return pyVal{}, false
		}
	}
}

func (p *pyParser) object() (pyVal, bool) {
	p.i++
	obj := map[string]pyVal{}
	p.ws()
	if p.i < len(p.s) && p.s[p.i] == '}' {
		p.i++
		return pyVal{kind: pyObj, obj: obj}, true
	}
	for {
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != '"' {
			return pyVal{}, false
		}
		k, ok := p.str()
		if !ok {
			return pyVal{}, false
		}
		p.ws()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return pyVal{}, false
		}
		p.i++
		p.ws()
		v, ok := p.value()
		if !ok {
			return pyVal{}, false
		}
		obj[k] = v
		p.ws()
		if p.i >= len(p.s) {
			return pyVal{}, false
		}
		c := p.s[p.i]
		p.i++
		if c == '}' {
			return pyVal{kind: pyObj, obj: obj}, true
		}
		if c != ',' {
			return pyVal{}, false
		}
	}
}

// pyFloatStrip is the whitespace float(str) skips around the number: Unicode spaces (not the \x1c-\x1f separators).
func pyFloatStrip(s string) string { return strings.TrimFunc(s, unicode.IsSpace) }

// pyASCIIDigits maps every Unicode decimal digit (category Nd) to its ASCII digit, as float(str) reads them.
func pyASCIIDigits(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r > 127 && unicode.Is(unicode.Nd, r) {
			z := r
			for unicode.Is(unicode.Nd, z-1) { // digit runs start at a multiple of ten
				z--
			}
			r = '0' + (r-z)%10
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// pyFloatStr is float(str) for the finite results (non-finite and invalid both come back ok=false: stale either way).
func pyFloatStr(s string) (float64, bool) {
	s = pyASCIIDigits(pyFloatStrip(s))
	body := strings.TrimLeft(s, "+-")
	if len(s)-len(body) > 1 || body == "" {
		return 0, false
	}
	// digit groups: digits, with single underscores only between digits
	group := func(x string) bool {
		if x == "" || x[0] == '_' || x[len(x)-1] == '_' || strings.Contains(x, "__") {
			return false
		}
		return strings.Trim(x, "0123456789_") == ""
	}
	mant, exp, hasExp := strings.Cut(strings.ToLower(body), "e")
	if hasExp {
		e := strings.TrimLeft(exp, "+-")
		if len(exp)-len(e) > 1 || !group(e) {
			return 0, false
		}
	}
	ip, fp, hasDot := strings.Cut(mant, ".")
	okMant := false
	switch {
	case !hasDot:
		okMant = group(ip)
	case ip == "":
		okMant = group(fp)
	case fp == "":
		okMant = group(ip)
	default:
		okMant = group(ip) && group(fp)
	}
	if !okMant {
		return 0, false
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(s, "_", ""), 64)
	if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
		return 0, false
	}
	return f, true
}

// pyFloatAt is `float(json.loads(value)["at"])`: the finite float, or ok=false when Python raises or the number is
// NaN or infinite (the prune unit treats both as stale).
func pyFloatAt(value string) (float64, bool) {
	v, ok := pyParse(value)
	if !ok || v.kind != pyObj {
		return 0, false
	}
	at, ok := v.obj["at"]
	if !ok {
		return 0, false
	}
	var f float64
	switch at.kind {
	case pyBool:
		if at.b {
			f = 1
		}
	case pyInt, pyNum:
		f = at.f
	case pyStr:
		f, ok = pyFloatStr(at.s)
		if !ok {
			return 0, false
		}
	default: // null, list, object: float() raises TypeError
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}
