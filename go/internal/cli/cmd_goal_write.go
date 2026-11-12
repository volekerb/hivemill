package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// `factory goal new|edit|status|audit|signoff`: the Go port of the cmd_goal branches of the same names in
// bin/factory_cli.py (goal_status, goal_audit, goal_signoff, open_tasks). Every other goal action runs elsewhere.
// Argv rules (S2 R1): the flags are exactly those of Python's one `goal` argparse parser; anything argparse would
// treat differently (abbreviation, repeat, a dash-led value, an unknown flag, positionals after a flag) delegates.

func init() {
	for _, a := range []string{"new", "edit", "status", "audit", "signoff"} {
		register("goal", a, handler{parse: gwParseFor(a), run: gwRun})
	}
}

// gwFlags maps each flag of the goal parser to its kind: b store_true, s string, i int.
var gwFlags = map[string]byte{
	"--org": 's', "--ready": 'b', "--gaps": 'b', "--full": 'b', "--report": 's', "--audit": 'i', "--reason": 's',
	"--title": 's', "--project": 's', "--intent": 's', "--success": 's', "--priority": 'i', "--requested-by": 's',
	"--note": 's', "--token-limit": 'i', "--all": 'b',
}

var gwNegNumRE = regexp.MustCompile(`^-[0-9]+$|^-[0-9]*\.[0-9]+$`)

var gwIntRE = regexp.MustCompile(`^[+-]?[0-9]+$`)

// gwArgs is a parsed goal argv: positionals (id, status) and the flags that were given.
type gwArgs struct {
	action string
	pos    []string
	str    map[string]string
	num    map[string]int64
	bools  map[string]bool
}

func (a *gwArgs) s(flag string) *string {
	if v, ok := a.str[flag]; ok {
		return &v
	}
	return nil
}

func (a *gwArgs) n(flag string) (int64, bool) {
	v, ok := a.num[flag]
	return v, ok
}

// gwDashValue: argparse takes a dash-led argument as a value only when it is "-" or looks like a negative number.
func gwDashValue(v string) bool {
	return !strings.HasPrefix(v, "-") || v == "-" || gwNegNumRE.MatchString(v)
}

// gwSplit parses the arguments after "goal <action>"; ok=false delegates.
func gwSplit(rest []string, maxPos int) (gwArgs, bool) {
	a := gwArgs{str: map[string]string{}, num: map[string]int64{}, bools: map[string]bool{}}
	seenFlag := false
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		arg := rest[i]
		if !utf8.ValidString(arg) {
			return a, false
		}
		if !strings.HasPrefix(arg, "-") || arg == "-" || gwNegNumRE.MatchString(arg) {
			if seenFlag || len(a.pos) >= maxPos {
				return a, false // argparse would consume positionals early or call the extra one unrecognised
			}
			a.pos = append(a.pos, arg)
			continue
		}
		name, val, hasVal := arg, "", false
		if strings.HasPrefix(arg, "--") {
			if k := strings.Index(arg, "="); k >= 0 {
				name, val, hasVal = arg[:k], arg[k+1:], true
			}
		}
		kind, known := gwFlags[name]
		if !known || seen[name] {
			return a, false
		}
		seen[name], seenFlag = true, true
		if kind == 'b' {
			if hasVal {
				return a, false
			}
			a.bools[name] = true
			continue
		}
		if !hasVal {
			if i+1 >= len(rest) || !gwDashValue(rest[i+1]) || !utf8.ValidString(rest[i+1]) {
				return a, false
			}
			i++
			val = rest[i]
		}
		if kind == 'i' {
			if !gwIntRE.MatchString(val) {
				return a, false
			}
			n, err := strconv.ParseInt(val, 10, 64)
			if err != nil {
				return a, false
			}
			a.num[name] = n
			continue
		}
		a.str[name] = val
	}
	return a, true
}

func gwParseFor(action string) func(rest []string) (any, bool) {
	return func(rest []string) (any, bool) {
		a, ok := gwSplit(rest, 2)
		if !ok {
			return nil, false
		}
		a.action = action
		if action != "new" && (len(a.pos) == 0 || a.pos[0] == "") {
			return nil, false // Python would run with id None (or "") and crash or print "None updated"
		}
		return a, true
	}
}

func gwRun(c *ctx, p any) (int, error) {
	a := p.(gwArgs)
	var err error
	switch a.action {
	case "new":
		err = gwNew(c, a)
	case "edit":
		err = gwEdit(c, a)
	case "status":
		err = gwStatus(c, a)
	case "audit":
		err = gwAudit(c, a)
	case "signoff":
		err = gwSignoff(c, a)
	}
	return 0, err
}

func gwPrintf(c *ctx, format string, args ...any) { fmt.Fprintf(c.out, format, args...) }

// gwPyReprSimple is repr() of a str made of plain printable ASCII; anything else delegates.
func gwPyReprSimple(s string) (string, error) {
	for _, r := range s {
		if r < 0x20 || r >= 0x7f || r == '\'' || r == '"' || r == '\\' {
			return "", errDelegate
		}
	}
	return "'" + s + "'", nil
}

func gwNew(c *ctx, a gwArgs) error {
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	if role != "boss" && role != "owner" {
		return dieErr(3, "only the boss creates goals; ask the boss (factory msg boss ...) or add a task to an open goal")
	}
	project := a.s("--project")
	if project != nil && *project == "" {
		return errDelegate // Python skips the check, then the project_id foreign key crashes it before any write
	}
	if project != nil {
		var one int
		err := c.db.QueryRow("SELECT 1 FROM projects WHERE id=?", *project).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(1, "no project "+*project)
		}
		if err != nil {
			return err
		}
	}
	org := ""
	if o := a.s("--org"); o != nil {
		org = *o
	}
	if org == "" {
		if org, err = agentOrg(c, c.who); err != nil {
			return err
		}
	}
	org, ok := orgCheck(org)
	if !ok {
		r, err := gwPyReprSimple(org)
		if err != nil {
			return err
		}
		return dieErr(2, fmt.Sprintf("unknown org %s; known: %s", r, strings.Join(orgNames, ", ")))
	}
	title := a.s("--title")
	if title == nil {
		return errDelegate // goals.title is NOT NULL: Python crashes; nothing is written or read from stdin yet
	}
	gid, err := nextID(c, "goals", "G", 3)
	if err != nil {
		return err
	}
	intent, err := readText(c, a.s("--intent"), false)
	if err != nil {
		return err
	}
	success, err := readText(c, a.s("--success"), false)
	if err != nil {
		return err
	}
	prio := int64(2)
	if v, ok := a.n("--priority"); ok {
		prio = v
	}
	reqBy := c.who
	if r := a.s("--requested-by"); r != nil && *r != "" {
		reqBy = *r
	}
	_, err = c.exec("INSERT INTO goals(id,project_id,title,intent,success_criteria,status,priority,requested_by,created,updated,org)"+
		" VALUES(?,?,?,?,?,?,?,?,?,?,?)",
		gid, gwNullable(project), *title, gwNullable(intent), gwNullable(success), "proposed", prio, reqBy, eventTS(c), eventTS(c), org)
	if err != nil {
		return err
	}
	if err := event(c, "goal", gid, "new", strp("["+org+"] "+*title)); err != nil {
		return err
	}
	gwPrintf(c, "%s\n", gid)
	return nil
}

func gwNullable(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func gwEdit(c *ctx, a gwArgs) error {
	if _, err := requireRole(c, "boss", "director"); err != nil {
		return err
	}
	id := a.pos[0]
	tl, hasTL := a.n("--token-limit")
	if hasTL && tl < 0 {
		return dieErr(2, "--token-limit must be a positive number of tokens, or 0 to clear the limit")
	}
	type set struct {
		col string
		v   any
	}
	var sets []set
	if t := a.s("--title"); t != nil {
		sets = append(sets, set{"title", *t})
	}
	intent, err := readText(c, a.s("--intent"), false)
	if err != nil {
		return err
	}
	success, err := readText(c, a.s("--success"), false)
	if err != nil {
		return err
	}
	if intent != nil {
		sets = append(sets, set{"intent", *intent})
	}
	if success != nil {
		sets = append(sets, set{"success_criteria", *success})
	}
	if p, ok := a.n("--priority"); ok {
		sets = append(sets, set{"priority", p})
	}
	if hasTL && tl != 0 {
		sets = append(sets, set{"token_limit", tl})
	}
	if len(sets) == 0 && !hasTL {
		return dieErr(1, "nothing to change")
	}
	if hasTL && tl == 0 { // 0 clears: NULL needs its own clause
		if _, err := c.exec("UPDATE goals SET token_limit=NULL, updated=? WHERE id=?", eventTS(c), id); err != nil {
			return err
		}
		if err := event(c, "goal", id, "edit", strp("token_limit cleared")); err != nil {
			return err
		}
		if len(sets) == 0 {
			gwPrintf(c, "%s updated\n", id)
			return nil
		}
	}
	var cols []string
	var args []any
	for _, s := range sets {
		cols = append(cols, s.col)
		args = append(args, s.v)
	}
	assign := strings.Join(cols, "=?, ") + "=?"
	args = append(args, eventTS(c), id)
	if _, err := c.exec("UPDATE goals SET "+assign+", updated=? WHERE id=?", args...); err != nil {
		return err
	}
	if err := event(c, "goal", id, "edit", strp(strings.Join(cols, ", "))); err != nil {
		return err
	}
	gwPrintf(c, "%s updated\n", id)
	return nil
}

// gwRefuseWorker is refuse_worker: a worker (or an answer Go cannot give) hands the command to Python before any write.
func gwRefuseWorker(c *ctx) error {
	if role, del := callerIsWorker(c); del || role != "" {
		return errDelegate
	}
	return nil
}

const gwStatusList = "['proposed', 'planning', 'active', 'blocked', 'done', 'cancelled']"

func gwStatus(c *ctx, a gwArgs) error {
	if err := gwRefuseWorker(c); err != nil {
		return err
	}
	id := a.pos[0]
	var status *string
	if len(a.pos) > 1 {
		status = &a.pos[1]
	}
	if status == nil || !inList(goalStatuses, *status) {
		return dieErr(1, "status must be one of "+gwStatusList)
	}
	var cur string
	if err := c.db.QueryRow("SELECT status FROM goals WHERE id=?", id).Scan(&cur); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(1, "no goal "+id)
		}
		return err
	}
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	note := a.s("--note")
	override := false
	if cur == "done" && *status != "done" {
		if role != "owner" && role != "director" {
			return dieErr(3, "only the director reopens a done goal")
		}
		if note == nil || pyStrip(*note) == "" {
			return dieErr(2, "--note is required to reopen a done goal")
		}
	} else {
		if _, err := requireRole(c, "boss", "director", "lead"); err != nil {
			return err
		}
		if *status == "done" {
			if role != "owner" {
				return dieErr(3, fmt.Sprintf("use factory goal close %s: closing a goal needs a director sign-off (factory goal signoff)", id))
			}
			override = true
		}
	}
	if _, err := c.exec("UPDATE goals SET status=?, updated=? WHERE id=?", *status, eventTS(c), id); err != nil {
		return err
	}
	if override {
		if err := event(c, "goal", id, "owner-override-close", note); err != nil {
			return err
		}
	}
	detail := *status
	if note != nil && *note != "" {
		detail += ": " + *note
	}
	if err := event(c, "goal", id, "status", &detail); err != nil {
		return err
	}
	gwPrintf(c, "%s → %s\n", id, *status)
	return nil
}

// gwOpenTasks is open_tasks: "T-1(todo)" for every task of the goal not done or cancelled, except exempt.
func gwOpenTasks(c *ctx, gid string, exempt *string) ([]string, error) {
	rows, err := c.query("SELECT id,status FROM tasks WHERE goal_id=? AND status NOT IN ('done','cancelled') ORDER BY id", gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			return nil, err
		}
		if exempt == nil || id != *exempt {
			out = append(out, id+"("+st+")")
		}
	}
	return out, rows.Err()
}

func gwGoalExists(c *ctx, id string) error {
	var g string
	if err := c.db.QueryRow("SELECT id FROM goals WHERE id=?", id).Scan(&g); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(1, "no goal "+id)
		}
		return err
	}
	return nil
}

func gwAudit(c *ctx, a gwArgs) error {
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	if role != "owner" && role != "pm" && role != "director" {
		return dieErr(3, fmt.Sprintf("role '%s' may not record a goal audit; allowed: pm (own goal), director", role))
	}
	gid := a.pos[0]
	if err := gwGoalExists(c, gid); err != nil {
		return err
	}
	if err := pmOwnGoal(c, gid, "the audit"); err != nil {
		return err
	}
	ready, gaps := a.bools["--ready"], a.bools["--gaps"]
	if ready == gaps {
		return dieErr(2, "goal audit needs exactly one of --ready or --gaps")
	}
	body, err := readText(c, a.s("--report"), false)
	if err != nil {
		return err
	}
	if body == nil || pyStrip(*body) == "" {
		return dieErr(2, "--report is required (use --report -)")
	}
	verdict := "gaps"
	if ready {
		verdict = "ready"
	}
	var own *string
	if role == "pm" {
		var t sql.NullString
		if err := c.db.QueryRow("SELECT task_id FROM agents WHERE name=?", c.who).Scan(&t); err != nil {
			return err
		}
		if t.Valid {
			own = &t.String
		}
	}
	if verdict == "ready" {
		op, err := gwOpenTasks(c, gid, own)
		if err != nil {
			return err
		}
		if len(op) > 0 {
			return dieErr(2, "audit --ready refused: open tasks "+strings.Join(op, ", "))
		}
	}
	var mv sql.NullInt64
	if err := c.db.QueryRow("SELECT max(version) FROM audits WHERE goal_id=?", gid).Scan(&mv); err != nil {
		return err
	}
	v := mv.Int64 + 1
	if _, err := c.exec("INSERT INTO audits(goal_id,version,verdict,body,author,task_id,created) VALUES(?,?,?,?,?,?,?)",
		gid, v, verdict, *body, c.who, gwNullable(own), eventTS(c)); err != nil {
		return err
	}
	if err := event(c, "goal", gid, fmt.Sprintf("audit v%d", v), strp(verdict)); err != nil {
		return err
	}
	gwPrintf(c, "%s audit v%d (%s)\n", gid, v, verdict)
	return nil
}

func gwSignoff(c *ctx, a gwArgs) error {
	if err := gwRefuseWorker(c); err != nil {
		return err
	}
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	if role != "owner" && role != "director" {
		return dieErr(3, "only the director signs off a goal close")
	}
	gid := a.pos[0]
	if err := gwGoalExists(c, gid); err != nil {
		return err
	}
	var auID, ver int64
	var verdict string
	var task sql.NullString
	err = c.db.QueryRow("SELECT id,version,verdict,task_id FROM audits WHERE goal_id=? ORDER BY version DESC LIMIT 1", gid).
		Scan(&auID, &ver, &verdict, &task)
	if errors.Is(err, sql.ErrNoRows) {
		return dieErr(2, fmt.Sprintf("no audit for %s: run factory goal audit", gid))
	}
	if err != nil {
		return err
	}
	want, ok := a.n("--audit")
	if !ok {
		return dieErr(2, "--audit N is required")
	}
	reason := a.s("--reason")
	if reason == nil || pyStrip(*reason) == "" {
		return dieErr(2, "--reason is required")
	}
	if want != ver {
		return dieErr(2, fmt.Sprintf("audit v%d is not the latest audit of %s (latest: v%d)", want, gid, ver))
	}
	if verdict != "ready" {
		return dieErr(2, fmt.Sprintf("audit v%d reports gaps; nothing to sign off", want))
	}
	var exempt *string
	if task.Valid {
		exempt = &task.String
	}
	op, err := gwOpenTasks(c, gid, exempt)
	if err != nil {
		return err
	}
	if len(op) > 0 {
		return dieErr(2, "signoff refused: open tasks "+strings.Join(op, ", "))
	}
	rows, err := c.query("SELECT id,status FROM tasks WHERE goal_id=? ORDER BY id", gid)
	if err != nil {
		return err
	}
	var parts []string
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			rows.Close()
			return err
		}
		var b1, b2 strings.Builder
		dumpString(&b1, id)
		dumpString(&b2, st)
		parts = append(parts, b1.String()+": "+b2.String())
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	snap := "{" + strings.Join(parts, ", ") + "}" // json.dumps(dict): ", " and ": ", insertion order, ensure_ascii
	if _, err := c.exec("INSERT INTO signoffs(goal_id,audit_id,snapshot,reason,author,created) VALUES(?,?,?,?,?,?)",
		gid, auID, snap, *reason, c.who, eventTS(c)); err != nil {
		return err
	}
	if err := event(c, "goal", gid, fmt.Sprintf("signoff v%d", ver), reason); err != nil {
		return err
	}
	gwPrintf(c, "%s audit v%d signed off\n", gid, ver)
	return nil
}
