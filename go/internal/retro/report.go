// Package retro is the Go port of retro/report.py and retro/schedule.py: the daily rejection report (packet, filed
// report, digest section) and the tick job that starts the retro agent from retro_hour.
package retro

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"factory/go/internal/scout"
)

const (
	Heading     = "## Retro"
	WindowHours = 24
	// MaxEvidence and the fix text bounds are the caps validate_file enforces.
	MaxEvidence = 8
	MaxFixText  = 400
)

// FixTypes is FIX_TYPES.
var FixTypes = []string{"check", "template", "prompt_rule"}

// Cause is one {cause, count} entry.
type Cause struct {
	Cause string
	Count int64
}

func (c Cause) omap() scout.OMap {
	return scout.OMap{{K: "cause", V: c.Cause}, {K: "count", V: c.Count}}
}

// Incident is one rejection incident of a window.
type Incident struct {
	Cause, Kind, TaskID, PRURL string
	VerdictID                  *int64
}

// Packet is what `factory retro packet` prints.
type Packet struct {
	WindowStart, WindowEnd string
	Causes                 []Cause
	Incidents              []Incident
}

// Isoformat is datetime.isoformat(): microseconds only when non-zero, offset always as +HH:MM.
func Isoformat(t time.Time) string {
	s := t.Format("2006-01-02T15:04:05")
	if us := t.Nanosecond() / 1000; us != 0 {
		s += fmt.Sprintf(".%06d", us)
	}
	return s + t.Format("-07:00")
}

// WindowBounds is (end - hours, end).
func WindowBounds(end time.Time, hours int) (time.Time, time.Time) {
	return end.Add(-time.Duration(hours) * time.Hour), end
}

// CauseCounts is the per-cause count of the incidents in (start, end], ordered by count desc then cause.
func CauseCounts(db *sql.DB, start, end time.Time) ([]Cause, error) {
	rows, err := db.Query("SELECT cause, COUNT(*) n FROM rejection_incidents "+
		"WHERE julianday(created) > julianday(?) AND julianday(created) <= julianday(?) "+
		"GROUP BY cause ORDER BY n DESC, cause", Isoformat(start), Isoformat(end))
	if err != nil {
		return nil, fmt.Errorf("count rejection causes: %w", err)
	}
	defer rows.Close()
	out := []Cause{}
	for rows.Next() {
		var c Cause
		if err := rows.Scan(&c.Cause, &c.Count); err != nil {
			return nil, fmt.Errorf("scan rejection cause: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rejection causes: %w", err)
	}
	return out, nil
}

// TopCauses keeps the positive counts, sorted by count desc then cause, at most limit.
func TopCauses(counts []Cause, limit int) []Cause {
	items := []Cause{}
	for _, c := range counts {
		if c.Count > 0 {
			items = append(items, c)
		}
	}
	// insertion sort: stable and tiny; Python sorts on (-count, cause) with str comparison (code points)
	for i := 1; i < len(items); i++ {
		for j := i; j > 0 && lessCause(items[j], items[j-1]); j-- {
			items[j], items[j-1] = items[j-1], items[j]
		}
	}
	if len(items) > limit {
		items = items[:limit]
	}
	return items
}

func lessCause(a, b Cause) bool {
	if a.Count != b.Count {
		return a.Count > b.Count
	}
	return codePointLess(a.Cause, b.Cause)
}

// codePointLess is Python's str < str. Go compares UTF-8 bytes, which orders the same as code points.
func codePointLess(a, b string) bool { return a < b }

// IncidentsInWindow lists the incidents of (start, end] oldest first.
func IncidentsInWindow(db *sql.DB, start, end time.Time) ([]Incident, error) {
	rows, err := db.Query("SELECT ri.cause, ri.kind, ri.task_id, coalesce(t.pr_url, '') AS pr_url, ri.verdict_id "+
		"FROM rejection_incidents ri LEFT JOIN tasks t ON t.id = ri.task_id "+
		"WHERE julianday(ri.created) > julianday(?) AND julianday(ri.created) <= julianday(?) "+
		"ORDER BY ri.created, ri.id", Isoformat(start), Isoformat(end))
	if err != nil {
		return nil, fmt.Errorf("list rejection incidents: %w", err)
	}
	defer rows.Close()
	out := []Incident{}
	for rows.Next() {
		var i Incident
		var v sql.NullInt64
		if err := rows.Scan(&i.Cause, &i.Kind, &i.TaskID, &i.PRURL, &v); err != nil {
			return nil, fmt.Errorf("scan rejection incident: %w", err)
		}
		if v.Valid {
			i.VerdictID = &v.Int64
		}
		out = append(out, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read rejection incidents: %w", err)
	}
	return out, nil
}

// BuildPacket is the packet of the 24 h window that ends at clock.
func BuildPacket(db *sql.DB, clock time.Time) (Packet, error) {
	start, end := WindowBounds(clock, WindowHours)
	counts, err := CauseCounts(db, start, end)
	if err != nil {
		return Packet{}, err
	}
	inc, err := IncidentsInWindow(db, start, end)
	if err != nil {
		return Packet{}, err
	}
	return Packet{WindowStart: Isoformat(start), WindowEnd: Isoformat(end), Causes: TopCauses(counts, 5), Incidents: inc}, nil
}

// Text is json.dumps(packet, indent=2).
func (p Packet) Text() string {
	causes := make([]any, len(p.Causes))
	for i, c := range p.Causes {
		causes[i] = c.omap()
	}
	inc := make([]any, len(p.Incidents))
	for i, x := range p.Incidents {
		var v any
		if x.VerdictID != nil {
			v = *x.VerdictID
		}
		inc[i] = scout.OMap{{K: "cause", V: x.Cause}, {K: "kind", V: x.Kind}, {K: "task_id", V: x.TaskID},
			{K: "pr_url", V: x.PRURL}, {K: "verdict_id", V: v}}
	}
	return scout.Dumps(scout.OMap{{K: "window_start", V: p.WindowStart}, {K: "window_end", V: p.WindowEnd},
		{K: "causes", V: causes}, {K: "incidents", V: inc}}, 2, false)
}

// ValueError is a user-facing validation failure (Python ValueError); the CLI maps it to exit 2.
type ValueError struct{ Msg string }

func (e *ValueError) Error() string { return e.Msg }

func verr(format string, a ...any) error { return &ValueError{Msg: fmt.Sprintf(format, a...)} }

// ---- ordered JSON ----

// DecodeOrdered is json.loads with objects as scout.OMap (insertion order, last duplicate wins in place), arrays as
// []any, integers as int64 (float64 when they do not fit) and other numbers as float64.
func DecodeOrdered(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec)
	if err != nil {
		return nil, fmt.Errorf("decode JSON: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("decode JSON: extra data after the first value")
	}
	return v, nil
}

func decodeValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		if t == '[' {
			out := []any{}
			for dec.More() {
				v, err := decodeValue(dec)
				if err != nil {
					return nil, err
				}
				out = append(out, v)
			}
			if _, err := dec.Token(); err != nil {
				return nil, err
			}
			return out, nil
		}
		out := scout.OMap{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return nil, err
			}
			k, ok := kt.(string)
			if !ok {
				return nil, errors.New("object key is not a string")
			}
			v, err := decodeValue(dec)
			if err != nil {
				return nil, err
			}
			replaced := false
			for i := range out {
				if out[i].K == k {
					out[i].V, replaced = v, true
					break
				}
			}
			if !replaced {
				out = append(out, scout.KV{K: k, V: v})
			}
		}
		if _, err := dec.Token(); err != nil {
			return nil, err
		}
		return out, nil
	case json.Number:
		if n, err := strconv.ParseInt(string(t), 10, 64); err == nil {
			return n, nil
		}
		f, err := t.Float64()
		if err != nil {
			return nil, fmt.Errorf("number %s: %w", t, err)
		}
		return f, nil
	default: // string, bool, nil
		return tok, nil
	}
}

func get(o scout.OMap, k string) (any, bool) {
	for _, kv := range o {
		if kv.K == k {
			return kv.V, true
		}
	}
	return nil, false
}

func truthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case int64:
		return x != 0
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case scout.OMap:
		return len(x) > 0
	}
	return true
}

// pyStr is str(v) for decoded JSON values.
func pyStr(v any) string {
	switch x := v.(type) {
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case string:
		return x
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e16 {
			return strconv.FormatFloat(x, 'f', 1, 64)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return scout.Dumps(v, 0, false)
}

// pyIntOf is int(v) for decoded JSON values: ints, floats (truncated), bools and numeric strings.
func pyIntOf(v any) (int64, bool) {
	switch x := v.(type) {
	case int64:
		return x, true
	case bool:
		if x {
			return 1, true
		}
		return 0, true
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) >= 9.2e18 {
			return 0, false
		}
		return int64(x), true
	case string:
		return PyInt(x)
	}
	return 0, false
}

// PyInt is Python's int(str): optional sign, Unicode decimal digits (category Nd), single underscores between
// digits, surrounding Unicode whitespace.
func PyInt(s string) (int64, bool) {
	s = strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) })
	neg := false
	if s != "" && (s[0] == '+' || s[0] == '-') {
		neg = s[0] == '-'
		s = s[1:]
	}
	if s == "" {
		return 0, false
	}
	var n int64
	prevUnderscore, seen := true, false // a leading underscore is invalid
	for _, r := range s {
		if r == '_' {
			if prevUnderscore {
				return 0, false
			}
			prevUnderscore = true
			continue
		}
		d, ok := digitValue(r)
		if !ok {
			return 0, false
		}
		if n > (math.MaxInt64-int64(d))/10 {
			return 0, false // overflows; Python's int is unbounded but no caller needs it
		}
		n = n*10 + int64(d)
		prevUnderscore, seen = false, true
	}
	if prevUnderscore || !seen {
		return 0, false // a trailing underscore is invalid
	}
	if neg {
		n = -n
	}
	return n, true
}

// digitValue is the decimal value of a Unicode Nd character: Nd characters come in aligned runs of ten.
func digitValue(r rune) (int, bool) {
	if !unicode.Is(unicode.Nd, r) {
		return 0, false
	}
	n := 0
	for c := r - 1; unicode.Is(unicode.Nd, c); c-- {
		n++
	}
	return n % 10, true
}

func parseCauses(body scout.OMap) ([]Cause, error) {
	raw, _ := get(body, "causes")
	list, ok := raw.([]any)
	if !ok {
		return nil, verr("causes must be a list")
	}
	out := []Cause{}
	for _, item := range list {
		o, ok := item.(scout.OMap)
		if !ok {
			return nil, verr("each cause must be {cause, count}")
		}
		cv, hasC := get(o, "cause")
		nv, hasN := get(o, "count")
		if !hasC || !hasN {
			return nil, verr("each cause must be {cause, count}")
		}
		n, ok := pyIntOf(nv)
		if !ok {
			return nil, verr("each cause must be {cause, count}: count is not an integer")
		}
		out = append(out, Cause{Cause: pyStr(cv), Count: n})
	}
	return out, nil
}

func causesEqual(a, b []Cause) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Normalized is the validated body that store_report keeps. Evidence stays as the agent wrote it.
type Normalized struct {
	Causes   []Cause
	Evidence []any
	Fixes    []any
}

func causesJSON(cs []Cause) string {
	a := make([]any, len(cs))
	for i, c := range cs {
		a[i] = c.omap()
	}
	return scout.Dumps(a, 0, false)
}

// ValidateFile checks a filed report against the incidents of its window and returns (day, normalised body).
// The error is a *ValueError for every user-facing refusal.
func ValidateFile(db *sql.DB, bodyAny any) (string, Normalized, error) {
	body, ok := bodyAny.(scout.OMap)
	if !ok {
		return "", Normalized{}, verr("body must be a JSON object")
	}
	wend, _ := get(body, "window_end")
	if !truthy(wend) {
		return "", Normalized{}, verr("window_end is required")
	}
	ws, isStr := wend.(string)
	var end time.Time
	var tsOK bool
	if isStr {
		end, tsOK = scout.TS(ws)
	}
	if !tsOK {
		return "", Normalized{}, verr("window_end must be an ISO timestamp")
	}
	got, err := parseCauses(body)
	if err != nil {
		return "", Normalized{}, err
	}
	start, _ := WindowBounds(end, WindowHours)
	counts, err := CauseCounts(db, start, end)
	if err != nil {
		return "", Normalized{}, err
	}
	want := TopCauses(counts, 5)
	if !causesEqual(got, want) {
		return "", Normalized{}, verr("causes do not match the incidents in the window")
	}
	evV, _ := get(body, "evidence")
	fxV, _ := get(body, "fixes")
	evidence, ok1 := evV.([]any)
	fixes, ok2 := fxV.([]any)
	if !ok1 || !ok2 {
		return "", Normalized{}, verr("evidence and fixes must be arrays")
	}
	if len(evidence) > MaxEvidence {
		return "", Normalized{}, verr("at most 8 evidence items")
	}
	day := end.Format("2006-01-02")
	if len(want) == 0 {
		if len(evidence) > 0 || len(fixes) > 0 {
			return "", Normalized{}, verr("an empty window requires empty evidence and fixes")
		}
		return day, Normalized{Causes: []Cause{}, Evidence: []any{}, Fixes: []any{}}, nil
	}
	if len(fixes) < 1 || len(fixes) > 2 {
		return "", Normalized{}, verr("fixes must have length 1 or 2 when causes are non-empty")
	}
	allowed := map[string]bool{}
	for _, c := range want {
		allowed[c.Cause] = true
	}
	for i, evAny := range evidence {
		ev, ok := evAny.(scout.OMap)
		if !ok {
			return "", Normalized{}, verr("evidence[%d] must be an object", i)
		}
		for _, k := range []string{"task_id", "pr_url", "cause"} {
			if _, has := get(ev, k); !has {
				return "", Normalized{}, verr("evidence[%d] missing %s", i, k)
			}
		}
		cv, _ := get(ev, "cause")
		cause, isCause := cv.(string)
		if !isCause || !allowed[cause] {
			return "", Normalized{}, verr("evidence[%d] cause not in the recomputed list", i)
		}
		tv, _ := get(ev, "task_id")
		tid, isTid := tv.(string)
		if !isTid {
			return "", Normalized{}, verr("evidence[%d] task %s is not an incident for %s in the window", i, pyStr(tv), cause)
		}
		var one int
		err := db.QueryRow("SELECT 1 FROM rejection_incidents ri WHERE ri.task_id=? AND ri.cause=? "+
			"AND julianday(ri.created) > julianday(?) AND julianday(ri.created) <= julianday(?)",
			tid, cause, Isoformat(start), Isoformat(end)).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return "", Normalized{}, verr("evidence[%d] task %s is not an incident for %s in the window", i, tid, cause)
		}
		if err != nil {
			return "", Normalized{}, fmt.Errorf("check evidence[%d]: %w", i, err)
		}
		var pr sql.NullString
		wantPR := ""
		err = db.QueryRow("SELECT pr_url FROM tasks WHERE id=?", tid).Scan(&pr)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", Normalized{}, fmt.Errorf("read pr_url of %s: %w", tid, err)
		}
		if err == nil {
			wantPR = pr.String
		}
		gotPR := ""
		if pv, _ := get(ev, "pr_url"); truthy(pv) {
			s, isS := pv.(string)
			if !isS {
				return "", Normalized{}, verr("evidence[%d] pr_url must match the task", i)
			}
			gotPR = s
		}
		if gotPR != wantPR {
			return "", Normalized{}, verr("evidence[%d] pr_url must match the task", i)
		}
	}
	normFixes := make([]any, 0, len(fixes))
	for i, fxAny := range fixes {
		fx, ok := fxAny.(scout.OMap)
		if !ok {
			return "", Normalized{}, verr("fixes[%d] must be an object", i)
		}
		for _, k := range []string{"type", "cause", "text"} {
			if _, has := get(fx, k); !has {
				return "", Normalized{}, verr("fixes[%d] missing %s", i, k)
			}
		}
		tv, _ := get(fx, "type")
		typ, isT := tv.(string)
		if !isT || !inList(FixTypes, typ) {
			return "", Normalized{}, verr("fixes[%d] type must be one of ('check', 'template', 'prompt_rule')", i)
		}
		cv, _ := get(fx, "cause")
		cause, isC := cv.(string)
		if !isC || !allowed[cause] {
			return "", Normalized{}, verr("fixes[%d] cause not in the recomputed list", i)
		}
		xv, _ := get(fx, "text")
		text, isX := xv.(string)
		if n := utf8.RuneCountInString(text); !isX || n < 1 || n > MaxFixText {
			return "", Normalized{}, verr("fixes[%d] text must be 1-400 characters", i)
		}
		normFixes = append(normFixes, scout.OMap{{K: "type", V: typ}, {K: "cause", V: cause}, {K: "text", V: text}})
	}
	return day, Normalized{Causes: want, Evidence: evidence, Fixes: normFixes}, nil
}

func inList(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

// StoreReport upserts the report of one day.
func StoreReport(db *sql.DB, day string, n Normalized, author, created string) error {
	_, err := db.Exec("INSERT INTO retro_reports(day, causes, evidence, fixes, author, created) VALUES(?,?,?,?,?,?) "+
		"ON CONFLICT(day) DO UPDATE SET causes=excluded.causes, evidence=excluded.evidence, "+
		"fixes=excluded.fixes, author=excluded.author, created=excluded.created",
		day, causesJSON(n.Causes), scout.Dumps(n.Evidence, 0, false), scout.Dumps(n.Fixes, 0, false), author, created)
	if err != nil {
		return fmt.Errorf("store retro report %s: %w", day, err)
	}
	return nil
}

// Report is one retro_reports row.
type Report struct {
	Day, Causes, Evidence, Fixes, Author, Created string
}

// LoadReport reads the report of one day; ok=false when none.
func LoadReport(db *sql.DB, day string) (Report, bool, error) {
	var r Report
	err := db.QueryRow("SELECT day, causes, evidence, fixes, author, created FROM retro_reports WHERE day=?", day).
		Scan(&r.Day, &r.Causes, &r.Evidence, &r.Fixes, &r.Author, &r.Created)
	if errors.Is(err, sql.ErrNoRows) {
		return Report{}, false, nil
	}
	if err != nil {
		return Report{}, false, fmt.Errorf("load retro report %s: %w", day, err)
	}
	return r, true, nil
}

// ShowText is the `factory retro show` output: json.dumps of the report with its JSON columns parsed, indent 2.
func (r Report) ShowText() (string, error) {
	parts := [3]any{}
	for i, s := range []string{r.Causes, r.Evidence, r.Fixes} {
		v, err := DecodeOrdered([]byte(s))
		if err != nil {
			return "", fmt.Errorf("retro report %s has invalid JSON stored: %w", r.Day, err)
		}
		parts[i] = v
	}
	return scout.Dumps(scout.OMap{{K: "day", V: r.Day}, {K: "causes", V: parts[0]}, {K: "evidence", V: parts[1]},
		{K: "fixes", V: parts[2]}, {K: "author", V: r.Author}, {K: "created", V: r.Created}}, 2, false), nil
}

// BuildDigestSection is the Markdown retro section for (start, end].
func BuildDigestSection(db *sql.DB, start, end time.Time) (string, error) {
	nothing := Heading + "\n_nothing_\n"
	rows, err := db.Query("SELECT day, causes, evidence, fixes, author, created FROM retro_reports ORDER BY day")
	if err != nil {
		return "", fmt.Errorf("list retro reports: %w", err)
	}
	var last *Report
	for rows.Next() {
		var r Report
		if err := rows.Scan(&r.Day, &r.Causes, &r.Evidence, &r.Fixes, &r.Author, &r.Created); err != nil {
			rows.Close()
			return "", fmt.Errorf("scan retro report: %w", err)
		}
		if d, ok := scout.TS(r.Created); ok && d.After(start) && !d.After(end) {
			rr := r
			last = &rr
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return "", fmt.Errorf("read retro reports: %w", err)
	}
	if err := rows.Close(); err != nil {
		return "", fmt.Errorf("close retro reports: %w", err)
	}
	if last == nil {
		return nothing, nil
	}
	dec := func(s string) []any {
		if s == "" {
			s = "[]"
		}
		v, err := DecodeOrdered([]byte(s))
		if err != nil {
			return nil
		}
		l, _ := v.([]any)
		return l
	}
	for _, s := range []string{last.Causes, last.Evidence, last.Fixes} {
		if s == "" {
			s = "[]"
		}
		if _, err := DecodeOrdered([]byte(s)); err != nil {
			return nothing, nil // unreadable stored JSON: the digest says nothing rather than failing
		}
	}
	causes, evidence, fixes := dec(last.Causes), dec(last.Evidence), dec(last.Fixes)
	out := []string{Heading, ""}
	field := func(v any, k string) any {
		if o, ok := v.(scout.OMap); ok {
			x, _ := get(o, k)
			return x
		}
		return nil
	}
	for _, c := range causes {
		out = append(out, fmt.Sprintf("- %s: %s", pyStr(field(c, "cause")), pyStr(field(c, "count"))))
	}
	for _, ev := range evidence {
		tid, url, cause := "", "", ""
		if v := field(ev, "task_id"); v != nil {
			tid = pyStr(v)
		}
		if v := field(ev, "pr_url"); truthy(v) {
			url = pyStr(v)
		}
		if v := field(ev, "cause"); v != nil {
			cause = pyStr(v)
		}
		out = append(out, fmt.Sprintf("- %s %s (%s)", tid, url, cause))
	}
	for _, fx := range fixes {
		t, x := "", ""
		if v := field(fx, "type"); v != nil {
			t = pyStr(v)
		}
		if v := field(fx, "text"); v != nil {
			x = pyStr(v)
		}
		out = append(out, fmt.Sprintf("- %s — %s", t, x))
	}
	return strings.Join(out, "\n") + "\n", nil
}
