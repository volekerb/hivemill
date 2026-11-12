package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// verdict (G-041 S2p): cmd_verdict in bin/factory_cli.py (4707) with pr_state (4573), gh_json (4445), gates_refusal
// (4661), parse_gates (4637), combined_name (4678), verdict_start_head (4684), normalize_rejection_cause (4331),
// require_rejection_cause (4341) and record_rejection_incident (4358). Verdict rows feed the judge merge gate, so
// every refusal Python makes is made here, in the same order, before the first write; anything Go does not decide
// the same way (a gh crash, a time Python's fromisoformat reads differently) runs in Python before any write.

func init() {
	register("verdict", "", handler{parse: parseVerdict, run: runVerdict})
}

type verdictCmd struct {
	task, summary string
	passed        bool
	asRole        *string
	sha, cause    *string
}

// verdictRejectionCauses is REJECTION_CAUSES (bin/factory_cli.py 310).
var verdictRejectionCauses = []string{"unclear_spec", "bug", "missing_test", "stale_or_conflicting_branch", "wrong_review", "gate_refusal", "other"}

var verdictCauseHelp = strings.Join(verdictRejectionCauses, ", ")

// verdictGatesFormat is GATES_FORMAT (4630).
const verdictGatesFormat = "Gate line format: `- <name> exit=<code> cmd=<command>`, one per line under a line `## Gates`, " +
	"e.g.\n## Gates\n- unittest exit=0 cmd=python3 -m unittest discover -s tests -v\n" +
	"- compile exit=0 cmd=python3 -m py_compile bin/factory\n" +
	"Gates are commands that must succeed (suite, build, lint); a check whose correct result is a " +
	"non-zero exit belongs in the per-criterion list instead."

// parseVerdict accepts the exact long flags of the Python parser, one positional and exactly one of --pass/--fail.
// Everything argparse could read differently (abbreviation, unknown flag, -h, "--", a repeated flag, both --pass and
// --fail, a value starting with "-" other than "-", a choice outside qa|reviewer, extra positionals, a missing
// required argument, invalid UTF-8, a non-ASCII --cause) fails the parse: Python answers.
func parseVerdict(rest []string) (any, bool) {
	var c verdictCmd
	var havePass, haveFail, haveTask, haveSummary bool
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		if !utf8.ValidString(w) {
			return nil, false
		}
		if w == "--pass" || w == "--fail" {
			if havePass || haveFail {
				return nil, false
			}
			havePass, haveFail = w == "--pass", w == "--fail"
			continue
		}
		if !strings.HasPrefix(w, "-") || w == "-" {
			if haveTask {
				return nil, false
			}
			haveTask, c.task = true, w
			continue
		}
		name, val, hasEq := strings.Cut(w, "=")
		if (name != "--summary" && name != "--as" && name != "--sha" && name != "--cause") || seen[name] {
			return nil, false
		}
		if !hasEq {
			if i+1 >= len(rest) {
				return nil, false
			}
			i++
			val = rest[i]
		}
		if (strings.HasPrefix(val, "-") && val != "-") || !utf8.ValidString(val) {
			return nil, false
		}
		seen[name] = true
		switch name {
		case "--summary":
			haveSummary, c.summary = true, val
		case "--as":
			if val != "qa" && val != "reviewer" {
				return nil, false
			}
			c.asRole = strp(val)
		case "--sha":
			c.sha = strp(val)
		case "--cause":
			for _, r := range val {
				if r >= 0x80 {
					return nil, false
				}
			}
			c.cause = strp(val)
		}
	}
	if !haveTask || !haveSummary || !(havePass || haveFail) {
		return nil, false
	}
	c.passed = havePass
	return c, true
}

// verdictRepr is repr(str): the quote Python picks, escapes for backslash, the quote, \t \n \r and every
// non-printable character (Unicode categories other than L, M, N, P, S and the plain space).
func verdictRepr(s string) string {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, "\"") {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for _, r := range s {
		switch {
		case r == rune(q) || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case unicode.IsPrint(r):
			b.WriteRune(r)
		case r < 0x100:
			fmt.Fprintf(&b, `\x%02x`, r)
		case r < 0x10000:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			fmt.Fprintf(&b, `\U%08x`, r)
		}
	}
	b.WriteByte(q)
	return b.String()
}

// verdictNormalizeCause is normalize_rejection_cause: "" (blank or absent) means no cause; an unknown one is exit 2.
func verdictNormalizeCause(val *string) (string, error) {
	if val == nil || pyStrip(*val) == "" {
		return "", nil
	}
	var b strings.Builder
	inRun := false
	for _, r := range strings.ToLower(pyStrip(*val)) {
		if pyIsSpace(r) || r == '-' {
			if !inRun {
				b.WriteByte('_')
			}
			inRun = true
			continue
		}
		inRun = false
		b.WriteRune(r)
	}
	if inList(verdictRejectionCauses, b.String()) {
		return b.String(), nil
	}
	return "", dieErr(2, fmt.Sprintf("unknown rejection cause %s; use one of: %s", verdictRepr(*val), verdictCauseHelp))
}

// verdictGate is one well-formed gate line.
type verdictGate struct {
	line       int
	name, exit string
}

func verdictNameChar(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '.' || r == ':' || r == '-'
}

// verdictGateLine matches GATE_RE (4626): "- NAME exit=CODE cmd=CMD". \d is any Unicode decimal digit and \S any
// non-space, as in Python's str patterns.
func verdictGateLine(line string) (name, exit string, ok bool) {
	s, found := strings.CutPrefix(line, "- ")
	if !found {
		return "", "", false
	}
	name, s, found = strings.Cut(s, " ")
	if !found || name == "" {
		return "", "", false
	}
	for _, r := range name {
		if !verdictNameChar(r) {
			return "", "", false
		}
	}
	s, found = strings.CutPrefix(s, "exit=")
	if !found {
		return "", "", false
	}
	exit, s, found = strings.Cut(s, " ")
	if !found {
		return "", "", false
	}
	if exit != "unknown" {
		digits := strings.TrimPrefix(exit, "-")
		if digits == "" {
			return "", "", false
		}
		for _, r := range digits {
			if !unicode.Is(unicode.Nd, r) {
				return "", "", false
			}
		}
	}
	cmd, found := strings.CutPrefix(s, "cmd=")
	if !found || cmd == "" {
		return "", "", false
	}
	if r, _ := utf8.DecodeRuneInString(cmd); pyIsSpace(r) {
		return "", "", false
	}
	return name, exit, true
}

// verdictParseGates is parse_gates: the number of "## Gates" sections, the well-formed gate lines and the errors.
func verdictParseGates(text string) (sections int, gates []verdictGate, errs []string) {
	seen := map[string]bool{}
	inside := false
	for i, raw := range pySplitlines(text) {
		n := i + 1
		line := strings.TrimRightFunc(raw, pyIsSpace)
		if strings.HasPrefix(line, "#") {
			inside = line == "## Gates"
			if inside {
				sections++
			}
			continue
		}
		if !inside || pyStrip(line) == "" {
			continue
		}
		name, exit, ok := verdictGateLine(line)
		switch {
		case !ok:
			errs = append(errs, fmt.Sprintf("line %d malformed: %s", n, verdictRepr(line)))
		case seen[name]:
			errs = append(errs, fmt.Sprintf("duplicate name %s (line %d)", name, n))
		default:
			seen[name] = true
			gates = append(gates, verdictGate{n, name, exit})
		}
	}
	return sections, gates, errs
}

// verdictGatesRefusal is gates_refusal: the reason a PASS summary's Gates section is unacceptable, or "".
func verdictGatesRefusal(text string, required bool) string {
	sections, gates, errs := verdictParseGates(text)
	switch {
	case sections == 0 && !required:
		return ""
	case sections == 0:
		return "missing '## Gates' section"
	case sections > 1:
		return fmt.Sprintf("%d '## Gates' sections (exactly one allowed)", sections)
	case len(errs) > 0:
		return strings.Join(errs, "; ")
	case len(gates) == 0:
		return "'## Gates' section has no gate lines"
	}
	var bad []string
	for _, g := range gates {
		if g.exit != "0" {
			bad = append(bad, fmt.Sprintf("gate %s exit=%s", g.name, g.exit))
		}
	}
	return strings.Join(bad, "; ")
}

var verdictShaRE = regexp.MustCompile(`^[0-9a-f]{7,40}$`)

// verdictParseTime reads a stored timestamp the way every writer of these columns formats it. Anything else makes the
// caller hand over: Python's fromisoformat accepts more shapes, and a naive/aware mix is a crash there.
func verdictParseTime(s string) (time.Time, bool) {
	t, err := time.Parse("2006-01-02T15:04:05Z07:00", s)
	if err != nil {
		return t, false
	}
	if _, off := t.Zone(); off <= -24*3600 || off >= 24*3600 {
		return t, false // fromisoformat rejects offsets of a day or more
	}
	return t, true
}

// verdictStartHead is verdict_start_head: the full PR head the tick started agent on, "" for none.
func verdictStartHead(c *ctx, task, agent string) (string, error) {
	var started sql.NullString
	err := c.db.QueryRow("SELECT started FROM agents WHERE name=?", agent).Scan(&started)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	var began time.Time
	haveBegan := false
	if started.Valid && started.String != "" {
		t, ok := verdictParseTime(started.String)
		if !ok {
			return "", errDelegate
		}
		began, haveBegan = t, true
	}
	rows, err := c.query("SELECT ts, detail FROM events WHERE entity='task' AND entity_id=? AND action='review-autospawn' ORDER BY id DESC", task)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	for rows.Next() {
		var ts string
		var detail sql.NullString
		if err := rows.Scan(&ts, &detail); err != nil {
			return "", err
		}
		if !detail.Valid {
			continue // json.loads(None) is a TypeError, which Python catches
		}
		raw := []byte(detail.String)
		if !json.Valid(raw) {
			return "", errDelegate // Python's json also reads NaN, Infinity and the like
		}
		var d map[string]json.RawMessage
		if json.Unmarshal(raw, &d) != nil {
			continue // valid JSON that is not an object
		}
		var who string
		if json.Unmarshal(d["agent"], &who) != nil || who != agent {
			continue
		}
		shaRaw, has := d["sha"]
		if !has {
			continue
		}
		var sha string
		if err := json.Unmarshal(shaRaw, &sha); err != nil {
			if c0 := bytes.TrimSpace(shaRaw); len(c0) > 0 && (c0[0] == '-' || c0[0] >= '0' && c0[0] <= '9') {
				return "", errDelegate // a number: str() may match 7-40 digits and .lower() then crashes in Python
			}
			continue // str() of null, a bool, a list or an object never fullmatches 7-40 lower-case hex
		}
		if !verdictShaRE.MatchString(sha) {
			continue
		}
		if haveBegan {
			t, ok := verdictParseTime(ts)
			if !ok {
				return "", errDelegate
			}
			if t.Before(began) {
				return "", nil
			}
		}
		return sha, nil
	}
	return "", rows.Err()
}

// verdictPRHead is pr_state(url)["headRefOid"] through gh_json. A gh failure ends like Python (exit 1, the message
// on stderr twice); a timeout, a missing gh or unreadable JSON are Python tracebacks, so Go hands over.
func verdictPRHead(c *ctx, url string) (string, error) {
	argv := []string{"pr", "view", url, "--json",
		"number,state,headRefOid,headRefName,baseRefName,mergeable,isDraft,statusCheckRollup,url"}
	out, errText, code, err := c.deps.runCmd("gh", argv, "", c.deps.Environ, 120*time.Second)
	if err != nil {
		return "", errDelegate
	}
	if code != 0 {
		if !utf8.ValidString(errText) {
			return "", errDelegate // Python's text=True crashes on it
		}
		first := ""
		if lines := pySplitlines(pyStrip(errText)); len(lines) > 0 {
			first = lines[0]
		}
		msg := fmt.Sprintf("gh pr view %s: %s", url, first)
		return "", &rawExit{Code: 1, Stderr: "factory: " + msg + "\n" + msg + "\n"}
	}
	if !utf8.ValidString(out) {
		return "", errDelegate
	}
	var pr map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &pr) != nil {
		return "", errDelegate
	}
	var head string
	if raw, ok := pr["headRefOid"]; !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &head) != nil {
		return "", errDelegate
	}
	return head, nil
}

func verdictHead8(s string) string {
	if r := []rune(s); len(r) > 8 {
		return string(r[:8])
	}
	return s
}

// runVerdict is cmd_verdict (bin/factory_cli.py 4707-4755). Statement order: INSERT verdicts (4745), [INSERT
// rejection_incidents (4748), FAIL only], INSERT events (4750).
func runVerdict(c *ctx, parsed any) (int, error) {
	a := parsed.(verdictCmd)
	role, err := requireRole(c, "qa", "reviewer")
	if err != nil {
		return 0, err
	}
	if role == "owner" {
		if a.asRole == nil {
			return 0, dieErr(1, "owner must pass --as qa|reviewer")
		}
		role = *a.asRole
	} else if role == "reviewer" && a.asRole != nil && *a.asRole == "qa" &&
		orgStripPrefix(c.who) == orgStripPrefix(slug("combined-"+a.task, 32)) {
		role = "qa" // the combined T0/T1 agent of this task records the QA verdict too
	}
	var prURL sql.NullString
	if err := c.db.QueryRow("SELECT pr_url FROM tasks WHERE id=?", a.task).Scan(&prURL); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, dieErr(1, "no task "+a.task)
		}
		return 0, err
	}
	if !prURL.Valid || prURL.String == "" {
		return 0, dieErr(1, a.task+" has no PR yet")
	}
	sp, err := readText(c, strp(a.summary), false)
	if err != nil {
		return 0, err
	}
	summary := *sp
	if a.passed {
		if why := verdictGatesRefusal(summary, role == "qa"); why != "" {
			return 0, dieErr(5, fmt.Sprintf("%s PASS refused, nothing recorded: %s\n%s", role, why, verdictGatesFormat))
		}
	}
	head, err := verdictPRHead(c, prURL.String)
	if err != nil {
		return 0, err
	}
	sha := head
	checked := ""
	if a.sha != nil {
		checked = strings.ToLower(*a.sha)
	}
	if checked != "" && !verdictShaRE.MatchString(checked) {
		return 0, dieErr(5, fmt.Sprintf("--sha must be 7-40 hex characters of the commit you checked, got %s", verdictRepr(*a.sha)))
	}
	startedAt, err := verdictStartHead(c, a.task, c.who)
	if err != nil {
		return 0, err
	}
	if startedAt != "" && checked == "" && !a.passed {
		checked = startedAt // a FAIL with no --sha is bound to the head this worker was started on
	}
	if a.passed && startedAt != "" && checked != "" && !strings.HasPrefix(startedAt, checked) && !strings.HasPrefix(checked, startedAt) {
		return 0, dieErr(5, fmt.Sprintf("%s PASS refused, nothing recorded: the tick started you on head %s, you named %s. The PR head moved "+
			"after you started, so this worker's check is not of that head: record a FAIL/finding at the head you tested, or let the tick start a fresh round",
			role, startedAt, checked))
	}
	switch {
	case a.passed && checked == "":
		return 0, dieErr(5, fmt.Sprintf("%s PASS refused, nothing recorded: pass --sha <the commit you checked> "+
			"(current PR head is %s; run `git -C <worktree> rev-parse HEAD` on what you tested)", role, head))
	case a.passed && !strings.HasPrefix(head, checked):
		return 0, dieErr(5, fmt.Sprintf("%s PASS refused, nothing recorded: you checked %s but the PR head is now %s. "+
			"The head moved after your check: re-check the new head and pass --sha %s "+
			"(or rely on carry-over at judge time if the move was a pure main merge)", role, checked, head, verdictHead8(head)))
	case a.passed, checked == "", strings.HasPrefix(head, checked):
		// a PASS at the head, or a FAIL with no --sha or a prefix of the head: stored as the full head sha
	default:
		fmt.Fprintf(c.errb, "WARNING: --sha %s is not the current PR head %s; recording the FAIL at %s\n", checked, head, checked)
		sha = checked
	}
	v := "pass"
	cause := ""
	if !a.passed {
		v = "fail"
		if cause, err = verdictNormalizeCause(a.cause); err != nil {
			return 0, err
		}
		if cause == "" {
			return 0, dieErr(2, fmt.Sprintf("rejection refused, nothing recorded: pass --cause (%s)", verdictCauseHelp))
		}
	}
	res, err := c.exec("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)",
		a.task, prURL.String, sha, role, v, summary, c.who, eventTS(c))
	if err != nil {
		return 0, err
	}
	if !a.passed {
		vid, err := res.LastInsertId()
		if err != nil {
			return 0, err
		}
		if _, err := c.exec("INSERT INTO rejection_incidents(cause,kind,task_id,verdict_id,event_id,created) VALUES(?,?,?,?,?,?)",
			cause, role, a.task, vid, nil, eventTS(c)); err != nil {
			return 0, err
		}
	}
	if err := event(c, "task", a.task, role+"-"+v, strp("@"+verdictHead8(sha))); err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "%s %s %s @ %s\n", a.task, role, strings.ToUpper(v), verdictHead8(sha))
	return 0, nil
}
