package scout

import (
	"database/sql"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Port of scout/report.py: the "## Skill scout" markdown section and the once-per-run message to the director
// (docs/scout-report.md). Read-only queries over scout_candidates / scout_triage / skill_trials / scout_runs,
// plus SendPending, which the tick calls. Messaging and the clock reach this package as ReportAPI.

const (
	Heading      = "## Skill scout"
	Footer       = "Adoption of any third-party skill needs owner approval through the boss."
	MaxFinds     = 5
	DefaultHours = 24
)

var (
	rank       = map[string]int{"adopt": 0, "watch": 1, "skip": 2}
	reported   = []string{"done", "stopped", "failed"} // terminal run statuses that get a report; a skipped run sends nothing
	trialEnded = []string{"passed", "failed", "timeout", "over-budget", "error"}
)

// ReportAPI is what SendPending needs from bin/factory: the clock, messaging and the events log.
type ReportAPI interface {
	Clock() time.Time
	// Send queues a message and returns its id.
	Send(db *sql.DB, to, subject, body, kind, sender string) (int64, error)
	Event(db *sql.DB, entity, entityID, action, detail string) error
}

// TS is ts(): an ISO string to an aware time (naive = local); ok false when unparsable.
func TS(s string) (time.Time, bool) {
	t, err := ParseTS(s)
	return t, err == nil
}

// line is _line: one line of untrusted text, whitespace collapsed so a candidate cannot inject headings or items.
func line(s sql.NullString) string {
	if !s.Valid {
		return "-"
	}
	return lineStr(s.String)
}

func lineStr(s string) string {
	if out := strings.Join(pyFields(s), " "); out != "" {
		return out
	}
	return "-"
}

func link(c Candidate) string {
	if c.URL.String != "" {
		return c.URL.String
	}
	repo := c.Repo.String
	if strings.HasPrefix(repo, "http") {
		return repo
	}
	if repo != "" {
		return "https://github.com/" + repo
	}
	return c.Key
}

func inWin(s sql.NullString, since, until time.Time) bool {
	if !s.Valid || s.String == "" {
		return false
	}
	t, ok := TS(s.String)
	return ok && t.After(since) && !t.After(until)
}

type triageRow struct {
	ID                                                   int64
	CandidateID, Purpose, TargetRoles, Overlap, Rec, Why string
	Created                                              sql.NullString
}

type find struct {
	c Candidate
	t triageRow
}

func finds(db *sql.DB, since, until time.Time) ([]find, error) {
	rows, err := db.Query("SELECT id, candidate_id, purpose, target_roles, overlap, recommendation, rationale, created FROM scout_triage ORDER BY id")
	if err != nil {
		return nil, err
	}
	latest := map[string]triageRow{}
	var order []string
	for rows.Next() {
		var t triageRow
		if err := rows.Scan(&t.ID, &t.CandidateID, &t.Purpose, &t.TargetRoles, &t.Overlap, &t.Rec, &t.Why, &t.Created); err != nil {
			rows.Close()
			return nil, err
		}
		if inWin(t.Created, since, until) {
			if _, seen := latest[t.CandidateID]; !seen {
				order = append(order, t.CandidateID)
			}
			latest[t.CandidateID] = t
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var out []find
	for _, cid := range order {
		c, ok, err := GetCandidate(db, cid)
		if err != nil {
			return nil, err
		}
		if ok && c.Key != ControlKey { // the skilleval positive control is not a find
			out = append(out, find{c, latest[cid]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		ra, rb := rankOf(a.t.Rec), rankOf(b.t.Rec)
		if ra != rb {
			return ra < rb
		}
		sa, sb := spdKey(a.c), spdKey(b.c)
		if sa != sb {
			return sa < sb
		}
		return a.c.ID < b.c.ID
	})
	return out, nil
}

func rankOf(rec string) int {
	if r, ok := rank[rec]; ok {
		return r
	}
	return 9
}

// spdKey is -(stars_per_day, or -inf when NULL): NULL sorts last.
func spdKey(c Candidate) float64 {
	if !c.StarsPerDay.Valid {
		return math.Inf(1)
	}
	return -c.StarsPerDay.Float64
}

func findLines(c Candidate, t triageRow) []string {
	stars, spd := "-", "-"
	if c.Stars.Valid {
		stars = strconv.FormatInt(c.Stars.Int64, 10)
	}
	if c.StarsPerDay.Valid {
		spd = fmt.Sprintf("%+.1f/day", c.StarsPerDay.Float64)
	}
	risk := c.InstallRisk
	if risk.String == "" {
		risk = sql.NullString{String: "unknown", Valid: true}
	}
	return []string{
		fmt.Sprintf("- **%s** %s — REC **%s**: %s", c.ID, link(c), t.Rec, lineStr(t.Why)),
		fmt.Sprintf("  - ★ %s (%s) · licence %s · last commit %s", stars, spd, line(c.Licence), line(c.LastCommit)),
		"  - purpose: " + lineStr(t.Purpose),
		fmt.Sprintf("  - target roles: %s · overlap: %s", lineStr(t.TargetRoles), lineStr(t.Overlap)),
		"  - install risk: " + line(risk),
	}
}

func median(vals []float64) (float64, bool) {
	if len(vals) == 0 {
		return 0, false
	}
	s := append([]float64{}, vals...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2], true
	}
	return (s[n/2-1] + s[n/2]) / 2, true
}

func num(v float64, ok bool) string {
	switch {
	case !ok:
		return "-"
	case v == math.Trunc(v):
		return strconv.FormatInt(int64(v), 10)
	}
	return fmt.Sprintf("%.1f", v)
}

type trial struct {
	CandidateID, Task, Condition, Status string
	Passed, SkillInvoked                 sql.NullInt64
	WallS                                sql.NullFloat64
	TokensIn, TokensOut, CacheWrite      sql.NullInt64
	Ended                                sql.NullString
}

// cell is the result of the trials of one task and condition: P / F, or the terminal status when it never got a verdict.
func cell(rows []trial) string {
	if len(rows) == 0 {
		return "-"
	}
	out := make([]string, len(rows))
	for i, r := range rows {
		switch {
		case r.Status == "passed" && r.Passed.Int64 != 0:
			out[i] = "P"
		case r.Status == "passed" || r.Status == "failed":
			out[i] = "F"
		default:
			out[i] = r.Status
		}
	}
	return strings.Join(out, "/")
}

func tokens(r trial) (float64, bool) {
	if !r.TokensIn.Valid && !r.TokensOut.Valid && !r.CacheWrite.Valid {
		return 0, false
	}
	return float64(r.TokensIn.Int64 + r.TokensOut.Int64 + r.CacheWrite.Int64), true
}

func medianOf(rows []trial, f func(trial) (float64, bool)) string {
	var vals []float64
	for _, r := range rows {
		if v, ok := f(r); ok {
			vals = append(vals, v)
		}
	}
	m, ok := median(vals)
	return num(m, ok)
}

func evalLines(db *sql.DB, since, until time.Time) ([]string, error) {
	q := "SELECT candidate_id, task, condition, status, passed, skill_invoked, wall_s, tokens_in, tokens_out, cache_write, ended " +
		"FROM skill_trials WHERE status IN (" + strings.TrimSuffix(strings.Repeat("?,", len(trialEnded)), ",") + ") AND candidate_id NOT IN " +
		"(SELECT id FROM scout_candidates WHERE key=?) ORDER BY id"
	args := []any{}
	for _, s := range trialEnded {
		args = append(args, s)
	}
	rows, err := db.Query(q, append(args, ControlKey)...)
	if err != nil {
		return nil, err
	}
	var trials []trial
	for rows.Next() {
		var t trial
		if err := rows.Scan(&t.CandidateID, &t.Task, &t.Condition, &t.Status, &t.Passed, &t.SkillInvoked, &t.WallS,
			&t.TokensIn, &t.TokensOut, &t.CacheWrite, &t.Ended); err != nil {
			rows.Close()
			return nil, err
		}
		if inWin(t.Ended, since, until) {
			trials = append(trials, t)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if len(trials) == 0 {
		return nil, nil
	}
	out := []string{"### Eval results (indicative, n=1 per cell)", ""}
	var cids []string
	for _, t := range trials {
		if !contains(cids, t.CandidateID) {
			cids = append(cids, t.CandidateID)
		}
	}
	for _, cid := range cids {
		key := "-"
		if err := db.QueryRow("SELECT key FROM scout_candidates WHERE id=?", cid).Scan(&key); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
		out = append(out, fmt.Sprintf("**%s** %s", cid, key), "",
			"| task | with | without | tokens with / without | wall s with / without | skill invoked |",
			"|---|---|---|---|---|---|")
		var mine []trial
		var tasks []string
		for _, t := range trials {
			if t.CandidateID == cid {
				mine = append(mine, t)
				if !contains(tasks, t.Task) {
					tasks = append(tasks, t.Task)
				}
			}
		}
		for _, task := range tasks {
			var w, wo []trial
			for _, t := range mine {
				if t.Task == task && t.Condition == "with" {
					w = append(w, t)
				}
				if t.Task == task && t.Condition == "without" {
					wo = append(wo, t)
				}
			}
			inv, any, seen := "-", false, false
			for _, t := range w {
				if t.SkillInvoked.Valid {
					seen = true
					any = any || t.SkillInvoked.Int64 != 0
				}
			}
			if seen {
				inv = "no"
				if any {
					inv = "yes"
				}
			}
			wall := func(t trial) (float64, bool) { return t.WallS.Float64, t.WallS.Valid }
			out = append(out, fmt.Sprintf("| %s | %s | %s | %s / %s | %s / %s | %s |", lineStr(task), cell(w), cell(wo),
				medianOf(w, tokens), medianOf(wo, tokens), medianOf(w, wall), medianOf(wo, wall), inv))
		}
		out = append(out, "")
	}
	return out, nil
}

// BuildSkillScoutSection is build_skill_scout_section: the markdown for the window (since, until].
func BuildSkillScoutSection(db *sql.DB, since, until time.Time) (string, error) {
	fs, err := finds(db, since, until)
	if err != nil {
		return "", err
	}
	evals, err := evalLines(db, since, until)
	if err != nil {
		return "", err
	}
	if len(fs) == 0 && len(evals) == 0 {
		return Heading + "\nNo new finds.\n", nil
	}
	out := []string{Heading, ""}
	if len(fs) > 0 {
		shown := fs[:min(len(fs), MaxFinds)]
		out = append(out, fmt.Sprintf("### Finds (%d of %d triaged)", len(shown), len(fs)), "")
		for _, f := range shown {
			out = append(out, findLines(f.c, f.t)...)
		}
		out = append(out, "")
	} else {
		out = append(out, "No new finds.", "")
	}
	out = append(out, evals...)
	var exe []find
	for _, f := range fs {
		if (f.t.Rec == "adopt" || f.t.Rec == "watch") && orUnknown(f.c.InstallRisk) != "prompt-only" {
			exe = append(exe, f)
		}
	}
	if len(exe) > 0 {
		out = append(out, "### Needs owner decision: executable parts", "")
		for _, f := range exe {
			detail := riskDetailHead(f.c.RiskDetail.String)
			item := fmt.Sprintf("- %s %s — %s, risk flags: %s", f.c.ID, link(f.c), f.t.Rec, lineStr(orUnknown(f.c.InstallRisk)))
			if detail != "" {
				item += fmt.Sprintf(" (%s)", lineStr(detail))
			}
			out = append(out, item)
		}
		out = append(out, "")
	}
	out = append(out, Footer)
	return strings.Join(out, "\n") + "\n", nil
}

func orUnknown(s sql.NullString) string {
	if s.String == "" {
		return "unknown"
	}
	return s.String
}

// riskDetailHead is ", ".join(str(d) for d in json.loads(risk_detail or "[]")[:3]); "" when it is not a JSON list.
func riskDetailHead(raw string) string {
	if raw == "" {
		raw = "[]"
	}
	v, ok := parseJSON(raw)
	if str, isStr := v.(string); ok && isStr { // Python slices a JSON string into characters
		var chars []string
		for _, r := range []rune(str)[:min(len([]rune(str)), 3)] {
			chars = append(chars, string(r))
		}
		return strings.Join(chars, ", ")
	}
	list, isList := v.([]any)
	if !ok || !isList {
		return ""
	}
	var parts []string
	for _, d := range list[:min(len(list), 3)] {
		parts = append(parts, pyStr(d))
	}
	return strings.Join(parts, ", ")
}

// pyStr is str(x) for a decoded JSON value (strings as is; others via their JSON-ish form).
func pyStr(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "None"
	case bool:
		if x {
			return "True"
		}
		return "False"
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1e15 {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'g', -1, 64)
	}
	return pyDumps(v)
}

type run struct {
	ID                     int64
	Slot                   string
	Ended                  sql.NullString
	NewCandidates, Triaged sql.NullInt64
}

// RunWindow is run_window: (since, until] of a finished run, from the end of the previous reported run (else 24 h
// before) to its own end. A run whose ended is unparsable ends `now`, so it is reported once.
func RunWindow(db *sql.DB, r run, now time.Time) (time.Time, time.Time, error) {
	until, ok := TS(r.Ended.String)
	if !ok {
		until = now
	}
	var prevEnded string
	err := db.QueryRow("SELECT ended FROM scout_runs WHERE id<? AND reported_msg IS NOT NULL AND ended IS NOT NULL "+
		"ORDER BY id DESC LIMIT 1", r.ID).Scan(&prevEnded)
	if err != nil && err != sql.ErrNoRows {
		return time.Time{}, time.Time{}, err
	}
	since, sok := TS(prevEnded)
	if err == sql.ErrNoRows || !sok || !since.Before(until) {
		since = until.Add(-DefaultHours * time.Hour)
	}
	return since, until, nil
}

// Subject is subject(): "2026-10-25T09" becomes "skill scout 2026-10-25 09".
func Subject(slot string) string { return "skill scout " + strings.Replace(slot, "T", " ", 1) }

func rollback(db *sql.DB, committed *bool) {
	if !*committed {
		db.Exec("ROLLBACK") // best effort: the connection may already be out of the transaction
	}
}

// SendPending sends the section of every terminal, unreported, non-skipped run to the director, once each. The
// check, the message and reported_msg are one IMMEDIATE transaction, so two ticks cannot both send. Returns the
// message ids.
func SendPending(db *sql.DB, api ReportAPI) ([]int64, error) {
	rows, err := db.Query("SELECT id FROM scout_runs WHERE status IN (?,?,?) AND reported_msg IS NULL AND ended IS NOT NULL ORDER BY id",
		reported[0], reported[1], reported[2])
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	var sent []int64
	for _, rid := range ids {
		mid, did, err := sendOne(db, api, rid)
		if err != nil {
			return sent, err
		}
		if did {
			sent = append(sent, mid)
		}
	}
	return sent, nil
}

func sendOne(db *sql.DB, api ReportAPI, rid int64) (mid int64, did bool, err error) {
	if _, err = db.Exec("BEGIN IMMEDIATE"); err != nil {
		return 0, false, err
	}
	committed := false
	defer rollback(db, &committed)
	var r run
	var slot string
	err = db.QueryRow("SELECT id, slot, ended, new_candidates, triaged FROM scout_runs WHERE id=? AND reported_msg IS NULL", rid).
		Scan(&r.ID, &slot, &r.Ended, &r.NewCandidates, &r.Triaged)
	r.Slot = slot
	if err == nil {
		since, until, werr := RunWindow(db, r, api.Clock())
		if werr != nil {
			return 0, false, werr
		}
		body, berr := BuildSkillScoutSection(db, since, until)
		if berr != nil {
			return 0, false, berr
		}
		if r.NewCandidates.Valid {
			body += fmt.Sprintf("\nThis run: %d new candidates, %d triaged.\n", r.NewCandidates.Int64, r.Triaged.Int64)
		}
		if mid, err = api.Send(db, "director", Subject(slot), body, "report", "tick"); err != nil {
			return 0, false, err
		}
		if _, err = db.Exec("UPDATE scout_runs SET reported_msg=? WHERE id=?", mid, rid); err != nil {
			return 0, false, err
		}
		if err = api.Event(db, "scout", slot, "scout-report", fmt.Sprintf("M%d", mid)); err != nil {
			return 0, false, err
		}
		did = true
	} else if err != sql.ErrNoRows {
		return 0, false, err
	}
	if _, err = db.Exec("COMMIT"); err != nil {
		return 0, false, err
	}
	committed = true
	return mid, did, nil
}
