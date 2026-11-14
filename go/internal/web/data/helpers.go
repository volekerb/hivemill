package data

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// IntentMax etc. are the truncation limits of web/queries.py.
	IntentMax  = 600
	SubjectMax = 120
	DetailMax  = 300
	chunk      = 400
)

var endedAgent = []string{"done", "exited", "lost", "stopped"}

var isoRe = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})(?:[T ](\d{2})(?::(\d{2})(?::(\d{2})(?:[.,](\d+))?)?)?(Z|[+-]\d{2}(?::?\d{2}(?::?\d{2})?)?)?)?$`)

// ParseTS is web/queries.py parse_ts: an ISO string to an instant (naive means local time); false for empty or
// unparseable input.
func ParseTS(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	m := isoRe.FindStringSubmatch(s)
	if m == nil {
		return time.Time{}, false
	}
	num := func(i int) int {
		n, _ := strconv.Atoi(m[i])
		return n
	}
	ns := 0
	if f := m[7]; f != "" {
		if len(f) > 6 {
			f = f[:6]
		}
		for len(f) < 6 {
			f += "0"
		}
		n, _ := strconv.Atoi(f)
		ns = n * 1000
	}
	loc := Loc
	if z := m[8]; z != "" {
		if z == "Z" {
			loc = time.UTC
		} else {
			sign := 1
			if z[0] == '-' {
				sign = -1
			}
			d := strings.ReplaceAll(z[1:], ":", "")
			h, _ := strconv.Atoi(d[:2])
			mi, se := 0, 0
			if len(d) >= 4 {
				mi, _ = strconv.Atoi(d[2:4])
			}
			if len(d) >= 6 {
				se, _ = strconv.Atoi(d[4:6])
			}
			if h > 23 || mi > 59 || se > 59 {
				return time.Time{}, false
			}
			loc = time.FixedZone("", sign*(h*3600+mi*60+se))
		}
	}
	y, mo, d, h, mi, se := num(1), num(2), num(3), num(4), num(5), num(6)
	t := time.Date(y, time.Month(mo), d, h, mi, se, ns, loc)
	if t.Year() != y || int(t.Month()) != mo || t.Day() != d || h > 23 || mi > 59 || se > 59 {
		return time.Time{}, false
	}
	return t, true
}

// epoch is queries._epoch: seconds since the epoch (with fraction) of an ISO string.
func epoch(s string) (float64, bool) {
	t, ok := ParseTS(s)
	if !ok {
		return 0, false
	}
	return tsec(t), true
}

func tsec(t time.Time) float64 { return float64(t.Unix()) + float64(t.Nanosecond())/1e9 }

func epochP(s *string) (float64, bool) {
	if s == nil {
		return 0, false
	}
	return epoch(*s)
}

// trunc is queries._t: truncate to n characters with an ellipsis.
func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func truncP(s *string, n int) *string {
	if s == nil {
		return nil
	}
	t := trunc(*s, n)
	return &t
}

func sp(s string) *string { return &s }

// nz returns nil for the empty string (Python's "x or None").
func nz(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func first8(s *string) *string {
	r := []rune(deref(s))
	if len(r) > 8 {
		r = r[:8]
	}
	return nz(string(r))
}

// pyRound is Python's round(x) for one argument: half to even.
func pyRound(x float64) int64 { return int64(math.RoundToEven(x)) }

// pyRoundN is Python's round(x, n): correctly rounded decimal of the exact binary value.
func pyRoundN(x float64, n int) float64 {
	f, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', n, 64), 64)
	return f
}

func nowOr(now time.Time) time.Time {
	if now.IsZero() {
		return time.Now()
	}
	return now
}

func isoSeconds(t time.Time) string { return t.Format("2006-01-02T15:04:05-07:00") }

func localDay(sec float64) string {
	return time.Unix(int64(math.Floor(sec)), 0).In(Loc).Format("2006-01-02")
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func strArgs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// inRows is queries._in: run query (one {in} placeholder) for each chunk of ids and call fn for every row.
func (c *Conn) inRows(query string, ids []string, fn func(rowScanner) error) error {
	for i := 0; i < len(ids); i += chunk {
		end := i + chunk
		if end > len(ids) {
			end = len(ids)
		}
		part := ids[i:end]
		rows, err := c.Query(strings.Replace(query, "{in}", placeholders(len(part)), 1), strArgs(part)...)
		if err != nil {
			return err
		}
		for rows.Next() {
			if err := fn(rows); err != nil {
				rows.Close()
				return err
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}
	return nil
}

type rowScanner interface{ Scan(dest ...any) error }

// each runs a query and calls fn for every row.
func (c *Conn) each(query string, args []any, fn func(rowScanner) error) error {
	rows, err := c.Query(query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := fn(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}

// pyStr is Python's str() of a decoded JSON scalar.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case string:
		return x
	case bool:
		if x {
			return "True"
		}
		return "False"
	case json.Number:
		s := x.String()
		if intRe.MatchString(s) {
			return s
		}
		f, _ := x.Float64()
		return pyFloat(f)
	}
	return fmt.Sprint(v)
}

var intRe = regexp.MustCompile(`^-?\d+$`)

// pyInt is tokens._int: the value if it is a JSON integer, else 0.
func pyInt(v any) int64 {
	if n, ok := v.(json.Number); ok && intRe.MatchString(n.String()) {
		i, err := strconv.ParseInt(n.String(), 10, 64)
		if err == nil {
			return i
		}
	}
	return 0
}

func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r) }

// median is statistics.median of integers: an int for an odd count, a float for an even count; false when empty.
func median(vals []int64) (Num, bool) {
	if len(vals) == 0 {
		return Num{}, false
	}
	s := append([]int64(nil), vals...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	n := len(s)
	if n%2 == 1 {
		return Int(s[n/2]), true
	}
	return Float(float64(s[n/2-1]+s[n/2]) / 2), true
}

func sumFloat(vs []float64) float64 {
	var t float64
	for _, v := range vs {
		t += v
	}
	return t
}

// Loc is the zone for "local" times: naive timestamps and local days. It is the process's local zone; tests pin it.
var Loc = time.Now().Location()
