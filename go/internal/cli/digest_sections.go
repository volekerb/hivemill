package cli

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Sections of the factory-wide digest (G-041 S2t): scout/report.py build_skill_scout_section, retro/report.py
// build_digest_section and the Lessons and HQ blocks of build_digest. Every input this port cannot reproduce byte for
// byte returns errDelegate so the command runs in Python.

// Headings and constants copied from scout/report.py and retro/report.py (tests read the Python literals).
const (
	dgaScoutHeading = "## Skill scout"
	dgaScoutFooter  = "Adoption of any third-party skill needs owner approval through the boss."
	dgaRetroHeading = "## Retro"
	dgaMaxFinds     = 5
	dgaControlKey   = "local:skilleval-control" // scoutdb.CONTROL_KEY
	dgaMaxExact     = 1 << 52                   // integers beyond this are not exact as float64
)

var dgaTrialEnded = []string{"passed", "failed", "timeout", "over-budget", "error"}

var dgaRank = map[string]int{"adopt": 0, "watch": 1, "skip": 2}

// dgaText is a text column read as `any`: ok=false for a type Python would print differently (a blob or a float).
func dgaText(v any) (s string, null bool, err error) {
	switch x := v.(type) {
	case nil:
		return "", true, nil
	case string:
		return x, false, nil
	case int64:
		return strconv.FormatInt(x, 10), false, nil
	}
	return "", false, errDelegate
}

// dgaTextS is dgaText for a column that is not NULL-able in the schema (NULL reads as "").
func dgaTextS(v any) (string, error) {
	s, _, err := dgaText(v)
	return s, err
}

// dgaNum is a numeric column: nil, int64 or finite float64 only.
func dgaNum(v any) (f float64, null bool, err error) {
	switch x := v.(type) {
	case nil:
		return 0, true, nil
	case int64:
		if x > dgaMaxExact || x < -dgaMaxExact {
			return 0, false, errDelegate
		}
		return float64(x), false, nil
	case float64:
		if math.IsInf(x, 0) || math.IsNaN(x) || math.Abs(x) >= 1<<53 {
			return 0, false, errDelegate
		}
		return x, false, nil
	}
	return 0, false, errDelegate
}

// dgaLine is scout.report._line for a text: whitespace collapsed, "-" when empty.
func dgaLine(s string) string {
	out := strings.Join(strings.FieldsFunc(s, pyIsSpace), " ")
	if out == "" {
		return "-"
	}
	return out
}

// dgaLineNull is _line(c[col]) for a nullable column: None prints "-".
func dgaLineNull(v any) (string, error) {
	s, null, err := dgaText(v)
	if err != nil || null {
		return "-", err
	}
	return dgaLine(s), nil
}

type dgaCand struct {
	id, key, url, repo, licence, lastCommit, risk, riskDetail string
	hasRisk                                                   bool
	stars                                                     string // "-" when NULL
	spd                                                       float64
	hasSPD                                                    bool
	licenceNull, lastCommitNull                               bool
}

type dgaTriage struct{ rec, rationale, purpose, roles, overlap string }

type dgaFind struct {
	c dgaCand
	t dgaTriage
}

func dgaLoadCand(db *sql.DB, id string) (dgaCand, bool, error) {
	var c dgaCand
	var key, url, repo, lic, last, risk, detail, stars, spd any
	var cid any
	err := db.QueryRow("SELECT id, key, url, repo, licence, last_commit, stars, stars_per_day, install_risk, risk_detail "+
		"FROM scout_candidates WHERE id=?", id).Scan(&cid, &key, &url, &repo, &lic, &last, &stars, &spd, &risk, &detail)
	if err == sql.ErrNoRows {
		return c, false, nil
	}
	if err != nil {
		return c, false, errDelegate
	}
	var e error
	set := func(dst *string, v any) {
		if e == nil {
			*dst, e = dgaTextS(v)
		}
	}
	set(&c.id, cid)
	set(&c.key, key)
	set(&c.url, url)
	set(&c.repo, repo)
	set(&c.riskDetail, detail)
	if e == nil {
		c.licence, c.licenceNull, e = dgaText(lic)
	}
	if e == nil {
		c.lastCommit, c.lastCommitNull, e = dgaText(last)
	}
	if e == nil {
		var null bool
		c.risk, null, e = dgaText(risk)
		c.hasRisk = !null
	}
	if e != nil {
		return c, false, e
	}
	switch x := stars.(type) {
	case nil:
		c.stars = "-"
	case int64:
		c.stars = strconv.FormatInt(x, 10)
	default:
		return c, false, errDelegate
	}
	f, null, err := dgaNum(spd)
	if err != nil {
		return c, false, err
	}
	c.spd, c.hasSPD = f, !null
	return c, true, nil
}

// dgaFinds is _finds: the newest triage per candidate inside the window, best first.
func dgaFinds(db *sql.DB, w dgWindow) ([]dgaFind, error) {
	rs, err := db.Query("SELECT candidate_id, recommendation, rationale, purpose, target_roles, overlap, created FROM scout_triage ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	by := map[string]dgaTriage{}
	for rs.Next() {
		var a, b, c, d, e, f, g any
		if err := rs.Scan(&a, &b, &c, &d, &e, &f, &g); err != nil {
			rs.Close()
			return nil, errDelegate
		}
		created, err := dgaTextS(g)
		if err != nil {
			rs.Close()
			return nil, err
		}
		_, in, err := w.in(created)
		if err != nil {
			rs.Close()
			return nil, err
		}
		if !in {
			continue
		}
		var t dgaTriage
		var cid string
		for _, p := range []struct {
			dst *string
			v   any
		}{{&cid, a}, {&t.rec, b}, {&t.rationale, c}, {&t.purpose, d}, {&t.roles, e}, {&t.overlap, f}} {
			if *p.dst, err = dgaTextS(p.v); err != nil {
				rs.Close()
				return nil, err
			}
		}
		by[cid] = t
	}
	rs.Close()
	if rs.Err() != nil {
		return nil, errDelegate
	}
	var out []dgaFind
	for cid, t := range by {
		c, ok, err := dgaLoadCand(db, cid)
		if err != nil {
			return nil, err
		}
		if ok && c.key != dgaControlKey {
			out = append(out, dgaFind{c, t})
		}
	}
	rank := func(f dgaFind) int {
		if r, ok := dgaRank[f.t.rec]; ok {
			return r
		}
		return 9
	}
	spdKey := func(f dgaFind) float64 { // -(stars_per_day), None = -(-inf)
		if !f.c.hasSPD {
			return math.Inf(1)
		}
		return -f.c.spd
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		if ka, kb := spdKey(a), spdKey(b); ka != kb {
			return ka < kb
		}
		return a.c.id < b.c.id
	})
	return out, nil
}

func dgaLink(c dgaCand) string {
	if c.url != "" {
		return c.url
	}
	if strings.HasPrefix(c.repo, "http") {
		return c.repo
	}
	if c.repo != "" {
		return "https://github.com/" + c.repo
	}
	return c.key
}

func dgaFindLines(c dgaCand, t dgaTriage) []string {
	spd := "-"
	if c.hasSPD {
		spd = fmt.Sprintf("%+.1f/day", c.spd)
	}
	lic, last := "-", "-"
	if !c.licenceNull {
		lic = dgaLine(c.licence)
	}
	if !c.lastCommitNull {
		last = dgaLine(c.lastCommit)
	}
	risk := "unknown"
	if c.hasRisk && c.risk != "" {
		risk = c.risk
	}
	return []string{
		fmt.Sprintf("- **%s** %s — REC **%s**: %s", c.id, dgaLink(c), t.rec, dgaLine(t.rationale)),
		fmt.Sprintf("  - ★ %s (%s) · licence %s · last commit %s", c.stars, spd, lic, last),
		"  - purpose: " + dgaLine(t.purpose),
		fmt.Sprintf("  - target roles: %s · overlap: %s", dgaLine(t.roles), dgaLine(t.overlap)),
		"  - install risk: " + dgaLine(risk),
	}
}

type dgaTrial struct {
	cid, task, cond, status string
	passed, invoked         bool
	invokedNull             bool
	wall, tokens            float64
	hasWall, hasTokens      bool
}

func dgaNumTruthy(v any) (truthy, null bool, err error) {
	switch x := v.(type) {
	case nil:
		return false, true, nil
	case int64:
		return x != 0, false, nil
	case float64:
		return x != 0, false, nil
	}
	return false, false, errDelegate
}

func dgaTrials(db *sql.DB, w dgWindow) ([]dgaTrial, error) {
	q := "SELECT candidate_id, task, condition, status, ended, passed, wall_s, tokens_in, tokens_out, cache_write, skill_invoked " +
		"FROM skill_trials WHERE status IN (?,?,?,?,?) AND candidate_id NOT IN (SELECT id FROM scout_candidates WHERE key=?) ORDER BY id"
	args := []any{}
	for _, s := range dgaTrialEnded {
		args = append(args, s)
	}
	rs, err := db.Query(q, append(args, dgaControlKey)...)
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	var out []dgaTrial
	for rs.Next() {
		var v [11]any
		if err := rs.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7], &v[8], &v[9], &v[10]); err != nil {
			return nil, errDelegate
		}
		ended, null, err := dgaText(v[4])
		if err != nil {
			return nil, err
		}
		if null || ended == "" {
			continue
		}
		_, in, err := w.in(ended)
		if err != nil {
			return nil, err
		}
		if !in {
			continue
		}
		var t dgaTrial
		for _, p := range []struct {
			dst *string
			v   any
		}{{&t.cid, v[0]}, {&t.task, v[1]}, {&t.cond, v[2]}, {&t.status, v[3]}} {
			if *p.dst, err = dgaTextS(p.v); err != nil {
				return nil, err
			}
		}
		if t.passed, _, err = dgaNumTruthy(v[5]); err != nil {
			return nil, err
		}
		if t.invoked, t.invokedNull, err = dgaNumTruthy(v[10]); err != nil {
			return nil, err
		}
		f, null, err := dgaNum(v[6])
		if err != nil {
			return nil, err
		}
		t.wall, t.hasWall = f, !null
		sum, any := 0.0, false
		for _, p := range v[7:10] { // tokens_in, tokens_out, cache_write
			f, null, err := dgaNum(p)
			if err != nil {
				return nil, err
			}
			if !null {
				any = true
				sum += f
			}
		}
		if sum >= dgaMaxExact {
			return nil, errDelegate
		}
		t.tokens, t.hasTokens = sum, any
		out = append(out, t)
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	return out, nil
}

// dgaNumStr is scout.report._num of a median ("-" for None).
func dgaNumStr(v float64, ok bool) string {
	if !ok {
		return "-"
	}
	if v == math.Trunc(v) {
		return strconv.FormatInt(int64(v), 10)
	}
	return fmt.Sprintf("%.1f", v)
}

// dgaMedian is _median: None values are dropped; no values is None.
func dgaMedian(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	s := append([]float64(nil), vals...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}

func dgaCell(rows []dgaTrial) string {
	if len(rows) == 0 {
		return "-"
	}
	parts := make([]string, len(rows))
	for i, r := range rows {
		switch {
		case r.status == "passed" && r.passed:
			parts[i] = "P"
		case r.status == "passed" || r.status == "failed":
			parts[i] = "F"
		default:
			parts[i] = r.status
		}
	}
	return strings.Join(parts, "/")
}

func dgaEvalLines(db *sql.DB, w dgWindow) ([]string, error) {
	trials, err := dgaTrials(db, w)
	if err != nil || len(trials) == 0 {
		return nil, err
	}
	out := []string{"### Eval results (indicative, n=1 per cell)", ""}
	var cids []string
	seen := map[string]bool{}
	for _, t := range trials {
		if !seen[t.cid] {
			seen[t.cid] = true
			cids = append(cids, t.cid)
		}
	}
	for _, cid := range cids {
		key := "-"
		var k any
		err := db.QueryRow("SELECT key FROM scout_candidates WHERE id=?", cid).Scan(&k)
		if err != nil && err != sql.ErrNoRows {
			return nil, errDelegate
		}
		if err == nil {
			if key, err = dgaTextS(k); err != nil {
				return nil, err
			}
		}
		out = append(out, fmt.Sprintf("**%s** %s", cid, key), "",
			"| task | with | without | tokens with / without | wall s with / without | skill invoked |",
			"|---|---|---|---|---|---|")
		var mine []dgaTrial
		for _, t := range trials {
			if t.cid == cid {
				mine = append(mine, t)
			}
		}
		var tasks []string
		seenT := map[string]bool{}
		for _, t := range mine {
			if !seenT[t.task] {
				seenT[t.task] = true
				tasks = append(tasks, t.task)
			}
		}
		for _, task := range tasks {
			var wi, wo []dgaTrial
			for _, t := range mine {
				if t.task == task && t.cond == "with" {
					wi = append(wi, t)
				}
				if t.task == task && t.cond == "without" {
					wo = append(wo, t)
				}
			}
			med := func(rows []dgaTrial, tok bool) string {
				var vals []float64
				for _, r := range rows {
					if tok && r.hasTokens {
						vals = append(vals, r.tokens)
					}
					if !tok && r.hasWall {
						vals = append(vals, r.wall)
					}
				}
				return dgaNumStr(dgaMedian(vals))
			}
			inv, anyInv, anyTrue := "-", false, false
			for _, r := range wi {
				if !r.invokedNull {
					anyInv = true
					anyTrue = anyTrue || r.invoked
				}
			}
			if anyInv {
				inv = "no"
				if anyTrue {
					inv = "yes"
				}
			}
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s / %s | %s / %s | %s |", dgaLine(task), dgaCell(wi), dgaCell(wo),
				med(wi, true), med(wo, true), med(wi, false), med(wo, false), inv))
		}
		out = append(out, "")
	}
	return out, nil
}

var dgaIntRE = regexp.MustCompile(`^-?[0-9]+$`)

// dgaJSON is json.loads for a stored value: ok=false is a ValueError. Inputs where Go and Python parse differently
// (NaN, Infinity, lone surrogates) return errDelegate.
func dgaJSON(raw string) (v any, ok bool, err error) {
	low := strings.ToLower(raw)
	if strings.Contains(raw, "NaN") || strings.Contains(raw, "Infinity") || strings.Contains(low, `\ud`) {
		return nil, false, errDelegate
	}
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&v); err != nil {
		return nil, false, nil
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, false, nil
	}
	return v, true, nil
}

// dgaPyStr is str(v) for a decoded JSON value Python prints the way this port can: a string, None, a bool or an integer.
func dgaPyStr(v any) (string, error) {
	switch x := v.(type) {
	case nil:
		return "None", nil
	case string:
		return x, nil
	case bool:
		if x {
			return "True", nil
		}
		return "False", nil
	case json.Number:
		s := x.String()
		if dgaIntRE.MatchString(s) && len(s) <= 18 {
			if s == "-0" {
				return "0", nil
			}
			return s, nil
		}
	}
	return "", errDelegate
}

// dgaTruthy is Python truthiness of a decoded JSON value for the kinds dgaPyStr handles.
func dgaTruthy(v any) (bool, error) {
	switch x := v.(type) {
	case nil:
		return false, nil
	case string:
		return x != "", nil
	case bool:
		return x, nil
	case json.Number:
		if _, err := dgaPyStr(x); err != nil {
			return false, err
		}
		s := x.String()
		return s != "0" && s != "-0", nil
	}
	return false, errDelegate
}

// dgaRiskDetail is `", ".join(str(d) for d in json.loads(c["risk_detail"] or "[]")[:3])`; ValueError and TypeError give "".
func dgaRiskDetail(raw string) (string, error) {
	if raw == "" {
		raw = "[]"
	}
	v, ok, err := dgaJSON(raw)
	if err != nil || !ok {
		return "", err
	}
	switch x := v.(type) {
	case []any:
		if len(x) > 3 {
			x = x[:3]
		}
		parts := make([]string, len(x))
		for i, d := range x {
			s, err := dgaPyStr(d)
			if err != nil {
				return "", err
			}
			parts[i] = s
		}
		return strings.Join(parts, ", "), nil
	case string:
		r := []rune(x)
		if len(r) > 3 {
			r = r[:3]
		}
		parts := make([]string, len(r))
		for i, ch := range r {
			parts[i] = string(ch)
		}
		return strings.Join(parts, ", "), nil
	case map[string]any:
		return "", errDelegate // dict[:3] raises KeyError, which build_skill_scout_section does not catch
	}
	return "", nil // None, bool, number: slicing raises TypeError, which it does
}

// dgaScoutSection is build_skill_scout_section(con, start, end) (it ends with a newline).
func dgaScoutSection(db *sql.DB, w dgWindow) (string, error) {
	finds, err := dgaFinds(db, w)
	if err != nil {
		return "", err
	}
	evals, err := dgaEvalLines(db, w)
	if err != nil {
		return "", err
	}
	if len(finds) == 0 && len(evals) == 0 {
		return dgaScoutHeading + "\nNo new finds.\n", nil
	}
	out := []string{dgaScoutHeading, ""}
	if len(finds) > 0 {
		shown := finds
		if len(shown) > dgaMaxFinds {
			shown = shown[:dgaMaxFinds]
		}
		out = append(out, fmt.Sprintf("### Finds (%d of %d triaged)", len(shown), len(finds)), "")
		for _, f := range shown {
			out = append(out, dgaFindLines(f.c, f.t)...)
		}
		out = append(out, "")
	} else {
		out = append(out, "No new finds.", "")
	}
	out = append(out, evals...)
	var exe []dgaFind
	for _, f := range finds {
		risk := f.c.risk
		if !f.c.hasRisk || risk == "" {
			risk = "unknown"
		}
		if (f.t.rec == "adopt" || f.t.rec == "watch") && risk != "prompt-only" {
			exe = append(exe, f)
		}
	}
	if len(exe) > 0 {
		out = append(out, "### Needs owner decision: executable parts", "")
		for _, f := range exe {
			detail, err := dgaRiskDetail(f.c.riskDetail)
			if err != nil {
				return "", err
			}
			risk := f.c.risk
			if !f.c.hasRisk || risk == "" {
				risk = "unknown"
			}
			line := fmt.Sprintf("- %s %s — %s, risk flags: %s", f.c.id, dgaLink(f.c), f.t.rec, dgaLine(risk))
			if detail != "" {
				line += " (" + dgaLine(detail) + ")"
			}
			out = append(out, line)
		}
		out = append(out, "")
	}
	out = append(out, dgaScoutFooter)
	return strings.Join(out, "\n") + "\n", nil
}

// dgaRetroSection is retro.report.build_digest_section(con, start, end) (it ends with a newline).
func dgaRetroSection(db *sql.DB, w dgWindow) (string, error) {
	nothing := dgaRetroHeading + "\n_nothing_\n"
	rs, err := db.Query("SELECT causes, evidence, fixes, created FROM retro_reports ORDER BY day")
	if err != nil {
		return "", errDelegate
	}
	var last [3]string
	found := false
	for rs.Next() {
		var a, b, c, d any
		if err := rs.Scan(&a, &b, &c, &d); err != nil {
			rs.Close()
			return "", errDelegate
		}
		created, err := dgaTextS(d)
		if err == nil {
			_, in, werr := w.in(created)
			if werr != nil {
				err = werr
			} else if in {
				var s [3]string
				for i, p := range []any{a, b, c} {
					if s[i], err = dgaTextS(p); err != nil {
						break
					}
				}
				last, found = s, found || err == nil
			}
		}
		if err != nil {
			rs.Close()
			return "", err
		}
	}
	rs.Close()
	if rs.Err() != nil {
		return "", errDelegate
	}
	if !found {
		return nothing, nil
	}
	var parsed [3]any
	for i, raw := range last {
		if raw == "" {
			raw = "[]"
		}
		v, ok, err := dgaJSON(raw)
		if err != nil {
			return "", err
		}
		if !ok {
			return nothing, nil
		}
		parsed[i] = v
	}
	out := []string{dgaRetroHeading, ""}
	items := func(i int) ([]map[string]any, error) {
		list, ok := parsed[i].([]any)
		if !ok {
			return nil, errDelegate // Python iterates something else and raises
		}
		var ms []map[string]any
		for _, x := range list {
			m, ok := x.(map[string]any)
			if !ok {
				return nil, errDelegate
			}
			ms = append(ms, m)
		}
		return ms, nil
	}
	causes, err := items(0)
	if err != nil {
		return "", err
	}
	evidence, err := items(1)
	if err != nil {
		return "", err
	}
	fixes, err := items(2)
	if err != nil {
		return "", err
	}
	get := func(m map[string]any, k string, def string) (string, error) {
		v, ok := m[k]
		if !ok {
			return def, nil
		}
		return dgaPyStr(v)
	}
	for _, c := range causes {
		cause, ok1 := c["cause"]
		count, ok2 := c["count"]
		if !ok1 || !ok2 {
			return "", errDelegate // KeyError
		}
		cs, err := dgaPyStr(cause)
		if err != nil {
			return "", err
		}
		ns, err := dgaPyStr(count)
		if err != nil {
			return "", err
		}
		out = append(out, fmt.Sprintf("- %s: %s", cs, ns))
	}
	for _, ev := range evidence {
		tid, err := get(ev, "task_id", "")
		if err != nil {
			return "", err
		}
		url := ""
		if v, ok := ev["pr_url"]; ok {
			t, err := dgaTruthy(v)
			if err != nil {
				return "", err
			}
			if t {
				if url, err = dgaPyStr(v); err != nil {
					return "", err
				}
			}
		}
		cause, err := get(ev, "cause", "")
		if err != nil {
			return "", err
		}
		out = append(out, fmt.Sprintf("- %s %s (%s)", tid, url, cause))
	}
	for _, fx := range fixes {
		typ, err := get(fx, "type", "")
		if err != nil {
			return "", err
		}
		text, err := get(fx, "text", "")
		if err != nil {
			return "", err
		}
		out = append(out, fmt.Sprintf("- %s — %s", typ, text))
	}
	return strings.Join(out, "\n") + "\n", nil
}

// dgaLessons is the "## Lessons" block of build_digest (empty when there is no lesson).
func dgaLessons(db *sql.DB, w dgWindow) ([]string, error) {
	var one int
	err := db.QueryRow("SELECT 1 FROM lessons LIMIT 1").Scan(&one)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, errDelegate
	}
	rs, err := db.Query("SELECT id, title, roles, status, created, applied_at, verified_at, evidence FROM lessons ORDER BY id")
	if err != nil {
		return nil, errDelegate
	}
	type lesson struct {
		id, title, roles, status, created, applied, verified, evidence string
		hasApplied, hasVerified                                        bool
	}
	var ls []lesson
	for rs.Next() {
		var v [8]any
		if err := rs.Scan(&v[0], &v[1], &v[2], &v[3], &v[4], &v[5], &v[6], &v[7]); err != nil {
			rs.Close()
			return nil, errDelegate
		}
		var l lesson
		var e1, e2 error
		var n1, n2 bool
		for _, p := range []struct {
			dst *string
			v   any
		}{{&l.id, v[0]}, {&l.title, v[1]}, {&l.roles, v[2]}, {&l.status, v[3]}, {&l.created, v[4]}, {&l.evidence, v[7]}} {
			if *p.dst, e1 = dgaTextS(p.v); e1 != nil {
				rs.Close()
				return nil, e1
			}
		}
		l.applied, n1, e1 = dgaText(v[5])
		l.verified, n2, e2 = dgaText(v[6])
		if e1 != nil || e2 != nil {
			rs.Close()
			return nil, errDelegate
		}
		l.hasApplied, l.hasVerified = !n1, !n2
		ls = append(ls, l)
	}
	rs.Close()
	if rs.Err() != nil {
		return nil, errDelegate
	}
	in := func(s string, has bool) (bool, error) {
		if !has {
			return false, nil
		}
		_, ok, err := w.in(s)
		return ok, err
	}
	prs := func(id string) (string, error) {
		r, err := db.Query("SELECT pr_url FROM lesson_links WHERE lesson_id=? AND pr_url IS NOT NULL ORDER BY id", id)
		if err != nil {
			return "", errDelegate
		}
		defer r.Close()
		var urls []string
		seen := map[string]bool{}
		for r.Next() {
			var u any
			if err := r.Scan(&u); err != nil {
				return "", errDelegate
			}
			s, err := dgaTextS(u)
			if err != nil {
				return "", err
			}
			if !seen[s] {
				seen[s] = true
				urls = append(urls, s)
			}
		}
		if r.Err() != nil {
			return "", errDelegate
		}
		return strings.Join(urls, ", "), nil
	}
	var newL, app, ver []string
	nOpen, nApplied := 0, 0
	for _, l := range ls {
		if l.status == "open" {
			nOpen++
		}
		if l.status == "applied" {
			nApplied++
		}
		if ok, err := in(l.created, true); err != nil {
			return nil, err
		} else if ok {
			newL = append(newL, fmt.Sprintf("- %s [%s] %s (%s)", l.id, l.roles, l.title, l.status))
		}
		if ok, err := in(l.applied, l.hasApplied); err != nil {
			return nil, err
		} else if ok {
			p, err := prs(l.id)
			if err != nil {
				return nil, err
			}
			line := fmt.Sprintf("- %s %s", l.id, l.title)
			if p != "" {
				line += " — " + p
			}
			app = append(app, line)
		}
		if ok, err := in(l.verified, l.hasVerified); err != nil {
			return nil, err
		} else if ok {
			ev := pyTrunc(strings.Join(strings.FieldsFunc(l.evidence, pyIsSpace), " "), 160)
			ver = append(ver, fmt.Sprintf("- %s %s — %s", l.id, l.title, ev))
		}
	}
	or := func(l []string) []string {
		if len(l) == 0 {
			return []string{"_nothing_"}
		}
		return l
	}
	out := []string{"## Lessons", "", "### New"}
	out = append(out, or(newL)...)
	out = append(out, "", "### Applied")
	out = append(out, or(app)...)
	out = append(out, "", "### Verified")
	out = append(out, or(ver)...)
	out = append(out, "", fmt.Sprintf("Open: %d · applied, awaiting verification: %d", nOpen, nApplied), "")
	return out, nil
}

// dgaHQ is the "## HQ" block: live agents without role flags that belong to no goal (empty when there are none).
func dgaHQ(db *sql.DB) ([]string, error) {
	rs, err := db.Query("SELECT name, flagless_since FROM agents WHERE flagless=1 AND status='live' AND goal_id IS NULL ORDER BY name")
	if err != nil {
		return nil, errDelegate
	}
	defer rs.Close()
	var lines []string
	for rs.Next() {
		var n string
		var since sql.NullString
		if err := rs.Scan(&n, &since); err != nil {
			return nil, errDelegate
		}
		lines = append(lines, fmt.Sprintf("- agent %s running without role flags since %s", n, dgStr(since)))
	}
	if rs.Err() != nil {
		return nil, errDelegate
	}
	if len(lines) == 0 {
		return nil, nil
	}
	return append(append([]string{"## HQ", ""}, lines...), ""), nil
}
