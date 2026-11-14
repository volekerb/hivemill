package cli

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// `factory goal close|org`, `factory team new --no-lead|close|reopen`, `factory project add|update`: the Go ports of
// goal_close, goal_move_org, cmd_team and cmd_project in bin/factory_cli.py. Everything else (team new without
// --no-lead, team wake, team list, project list, project update --repo) runs elsewhere. Argv rules (S2 R1): anything
// argparse would treat differently (abbreviation, repeat, a dash-led value, an unknown flag, a positional after a
// flag, -h, `--`) delegates to Python before any database access.

func init() {
	register("goal", "close", handler{parse: gwParseFor("close"), run: lcGoalClose})
	register("goal", "org", handler{parse: gwParseFor("org"), run: lcGoalOrg})
	register("team", "new", handler{parse: lcParseTeam("new"), run: lcTeamRun})
	register("team", "close", handler{parse: lcParseTeam("close"), run: lcTeamRun})
	register("team", "reopen", handler{parse: lcParseTeam("reopen"), run: lcTeamRun})
	register("project", "add", handler{parse: lcParseProject("add"), run: lcProjectRun})
	register("project", "update", handler{parse: lcParseProject("update"), run: lcProjectRun})
}

// lcSplit is gwSplit for another parser's flag table: kinds b store_true, s string, i int.
func lcSplit(rest []string, maxPos int, flags map[string]byte) (gwArgs, bool) {
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
				return a, false
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
		kind, known := flags[name]
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

// ───────────────────────────── goal close ─────────────────────────────

// lcCloseRefused is close_refused: one "close-refused" event, then exit 4.
func lcCloseRefused(c *ctx, gid, why string) error {
	if err := event(c, "goal", gid, "close-refused", strp(why)); err != nil {
		return err
	}
	return dieErr(4, why)
}

func lcGoalClose(c *ctx, p any) (int, error) {
	a := p.(gwArgs)
	return 0, lcGoalCloseRun(c, a)
}

func lcGoalCloseRun(c *ctx, a gwArgs) error {
	role, err := actorRole(c)
	if err != nil {
		return err
	}
	if role != "owner" && role != "pm" && role != "director" {
		return dieErr(3, fmt.Sprintf("role '%s' may not close goals; allowed: pm (own goal), director", role))
	}
	gid := a.pos[0]
	var status string
	if err := c.db.QueryRow("SELECT status FROM goals WHERE id=?", gid).Scan(&status); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(1, "no goal "+gid)
		}
		return err
	}
	if err := pmOwnGoal(c, gid, "the close"); err != nil {
		return err
	}
	reason := a.s("--reason")
	if reason == nil || pyStrip(*reason) == "" {
		return dieErr(2, "--reason is required")
	}
	if status == "done" || status == "cancelled" {
		return lcCloseRefused(c, gid, fmt.Sprintf("goal %s is already %s", gid, status))
	}
	// latest_audit: the newest audit and its newest sign-off.
	var auID, auVer int64
	var auVerdict string
	var auTask sql.NullString
	haveAu := true
	err = c.db.QueryRow("SELECT id,version,verdict,task_id FROM audits WHERE goal_id=? ORDER BY version DESC LIMIT 1", gid).
		Scan(&auID, &auVer, &auVerdict, &auTask)
	if errors.Is(err, sql.ErrNoRows) {
		haveAu = false
	} else if err != nil {
		return err
	}
	var exempt *string
	if haveAu && auTask.Valid {
		exempt = &auTask.String
	}
	op, err := gwOpenTasks(c, gid, exempt)
	if err != nil {
		return err
	}
	if len(op) > 0 {
		return lcCloseRefused(c, gid, "open tasks: "+strings.Join(op, ", "))
	}
	if !haveAu {
		return lcCloseRefused(c, gid, fmt.Sprintf("no audit for %s: run factory goal audit", gid))
	}
	if auVerdict != "ready" {
		return lcCloseRefused(c, gid, fmt.Sprintf("latest audit v%d reports gaps", auVer))
	}
	var soID int64
	var soSnap string
	var soAuthor sql.NullString
	err = c.db.QueryRow("SELECT id,snapshot,author FROM signoffs WHERE audit_id=? ORDER BY id DESC LIMIT 1", auID).
		Scan(&soID, &soSnap, &soAuthor)
	if errors.Is(err, sql.ErrNoRows) {
		return lcCloseRefused(c, gid, fmt.Sprintf("no director sign-off for audit v%d (factory goal signoff %s --audit %d)", auVer, gid, auVer))
	}
	if err != nil {
		return err
	}
	if !soAuthor.Valid {
		return errDelegate // Python formats None
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(soSnap), &snap); err != nil {
		return errDelegate // Python's json.loads accepts or crashes on shapes Go does not decide
	}
	rows, err := c.query("SELECT id,status FROM tasks WHERE goal_id=? ORDER BY id", gid)
	if err != nil {
		return err
	}
	var diffs []string
	for rows.Next() {
		var id, st string
		if err := rows.Scan(&id, &st); err != nil {
			rows.Close()
			return err
		}
		old, present := snap[id]
		if !present || old == nil {
			diffs = append(diffs, id+" is new")
			continue
		}
		os, isStr := old.(string)
		if !isStr {
			rows.Close()
			return errDelegate // Python would print the other type's repr
		}
		if st != os && !(exempt != nil && id == *exempt && st == "done") {
			diffs = append(diffs, fmt.Sprintf("%s changed after sign-off of audit v%d (%s -> %s)", id, auVer, os, st))
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(diffs) > 0 {
		return lcCloseRefused(c, gid, strings.Join(diffs, "; ")+": re-audit and get a new sign-off")
	}
	if _, err := c.exec("UPDATE goals SET status='done', updated=? WHERE id=?", eventTS(c), gid); err != nil {
		return err
	}
	if err := event(c, "goal", gid, "goal close", strp(fmt.Sprintf("audit %d (v%d), signoff %d: %s", auID, auVer, soID, *reason))); err != nil {
		return err
	}
	if err := event(c, "goal", gid, "status", strp("done: "+*reason)); err != nil {
		return err
	}
	body := fmt.Sprintf("%s closed by %s: %s\naudit v%d (id %d), signoff id %d by %s", gid, c.who, *reason, auVer, auID, soID, soAuthor.String)
	rc, err := c.query("SELECT lead_agent FROM teams WHERE goal_id=? AND lead_agent IS NOT NULL", gid)
	if err != nil {
		return err
	}
	rcpts := []string{"director"}
	for rc.Next() {
		var l string
		if err := rc.Scan(&l); err != nil {
			rc.Close()
			return err
		}
		rcpts = append(rcpts, l)
	}
	if err := rc.Err(); err != nil {
		rc.Close()
		return err
	}
	rc.Close()
	seen := map[string]bool{}
	for _, r := range rcpts {
		if seen[r] {
			continue
		}
		seen[r] = true
		if _, _, err := msgSend(c, r, gid+" closed", strp(body), "report", strp(gid), nil, ""); err != nil {
			return err
		}
	}
	gwPrintf(c, "%s → done\n", gid)
	return nil
}

// ───────────────────────────── goal org ─────────────────────────────

func lcGoalOrg(c *ctx, p any) (int, error) {
	return 0, lcGoalOrgRun(c, p.(gwArgs))
}

func lcGoalOrgRun(c *ctx, a gwArgs) error {
	if _, err := requireRole(c, "boss", "director"); err != nil {
		return err
	}
	id := a.pos[0]
	if len(a.pos) < 2 || a.pos[1] == "" {
		return dieErr(2, "usage: factory goal org G-nnn <org>  (orgs: "+strings.Join(orgNames, ", ")+")")
	}
	nw, ok := orgCheck(a.pos[1])
	if !ok {
		r, err := gwPyReprSimple(nw)
		if err != nil {
			return err
		}
		return dieErr(2, fmt.Sprintf("unknown org %s; known: %s", r, strings.Join(orgNames, ", ")))
	}
	var old sql.NullString
	if err := c.db.QueryRow("SELECT org FROM goals WHERE id=?", id).Scan(&old); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return dieErr(2, "no goal "+id)
		}
		return err
	}
	if !old.Valid {
		return errDelegate // Python formats None
	}
	if old.String == nw {
		gwPrintf(c, "%s already in the %s org\n", id, nw)
		return nil
	}
	if _, err := c.exec("UPDATE goals SET org=?, updated=? WHERE id=?", nw, eventTS(c), id); err != nil {
		return err
	}
	if err := event(c, "goal", id, "org", strp(old.String+" -> "+nw)); err != nil {
		return err
	}
	lr, err := c.query("SELECT name FROM agents WHERE goal_id=? AND status IN ('live','starting','queued')", id)
	if err != nil {
		return err
	}
	var live []string
	for lr.Next() {
		var n string
		if err := lr.Scan(&n); err != nil {
			lr.Close()
			return err
		}
		live = append(live, n)
	}
	if err := lr.Err(); err != nil {
		lr.Close()
		return err
	}
	lr.Close()
	body := fmt.Sprintf("%s now belongs to the %s org. New spawns for it use the %s runtime, models and name prefix.", id, nw, nw)
	if len(live) > 0 {
		body += fmt.Sprintf(" Still running under the old org: %s (finish or stop them).", strings.Join(live, ", "))
	}
	// Python iterates a set {old, new}: its order depends on the hash seed. Go sends old first, then new.
	for _, o := range []string{old.String, nw} {
		if _, _, err := msgSend(c, "director", fmt.Sprintf("%s moved %s -> %s", id, old.String, nw), strp(body), "decision", strp(id), nil, o); err != nil {
			return err
		}
	}
	gwPrintf(c, "%s: %s -> %s\n", id, old.String, nw)
	return nil
}

// ───────────────────────────── team ─────────────────────────────

var lcTeamFlags = map[string]byte{"--goal": 's', "--scope": 's', "--no-lead": 'b'}

func lcParseTeam(action string) func(rest []string) (any, bool) {
	return func(rest []string) (any, bool) {
		a, ok := lcSplit(rest, 1, lcTeamFlags)
		if !ok {
			return nil, false
		}
		a.action = action
		switch action {
		case "new":
			if !a.bools["--no-lead"] {
				return nil, false // spawns a lead: Python
			}
			if g := a.s("--goal"); g == nil || *g == "" || !lcASCII(*g) {
				return nil, false // Python crashes on a missing goal; a.goal.lower() of non-ASCII is not ours
			}
		default:
			if len(a.pos) == 0 || a.pos[0] == "" {
				return nil, false
			}
		}
		return a, true
	}
}

func lcASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func lcTeamRun(c *ctx, p any) (int, error) {
	a := p.(gwArgs)
	if err := gwRefuseWorker(c); err != nil { // refuse_worker, before require_role and any write
		return 0, err
	}
	if _, err := requireRole(c, "director"); err != nil {
		return 0, err
	}
	switch a.action {
	case "new":
		return 0, lcTeamNew(c, a)
	case "close":
		return 0, lcTeamClose(c, a)
	}
	return 0, lcTeamReopen(c, a)
}

func lcTeamNew(c *ctx, a gwArgs) error {
	goal := *a.s("--goal")
	var one int
	if err := c.db.QueryRow("SELECT 1 FROM goals WHERE id=?", goal).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errDelegate // the foreign key makes Python crash before any write
		}
		return err
	}
	var n int
	if err := c.db.QueryRow("SELECT count(*) FROM teams WHERE goal_id=?", goal).Scan(&n); err != nil {
		return err
	}
	tid := strings.ToLower(goal) + "-" + string(rune('a'+n))
	if err := c.db.QueryRow("SELECT 1 FROM teams WHERE id=?", tid).Scan(&one); err == nil {
		return errDelegate // primary key clash: Python crashes
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	scope, err := readText(c, a.s("--scope"), false)
	if err != nil {
		return err
	}
	if _, err := c.exec("INSERT INTO teams(id,goal_id,scope,status,created) VALUES(?,?,?,?,?)",
		tid, goal, gwNullable(scope), "active", eventTS(c)); err != nil {
		return err
	}
	if err := event(c, "team", tid, "new", a.s("--scope")); err != nil {
		return err
	}
	if err := lcGoalAutoActive(c, goal, fmt.Sprintf("team %s staffed", tid)); err != nil {
		return err
	}
	gwPrintf(c, "%s\n", tid)
	return nil
}

// lcGoalAutoActive is goal_auto_active: proposed/planning -> active, one event; any other status is left alone.
func lcGoalAutoActive(c *ctx, goal, trigger string) error {
	if goal == "" {
		return nil
	}
	res, err := c.exec("UPDATE goals SET status='active', updated=? WHERE id=? AND status IN ('proposed','planning')", eventTS(c), goal)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil {
		return err
	} else if n > 0 {
		return event(c, "goal", goal, "goal-auto-active", strp(trigger))
	}
	return nil
}

func lcTeamClose(c *ctx, a gwArgs) error {
	id := a.pos[0]
	if _, err := c.exec("UPDATE teams SET status='closed', parked_at=NULL WHERE id=?", id); err != nil {
		return err
	}
	var lead sql.NullString
	err := c.db.QueryRow("SELECT lead_agent FROM teams WHERE id=?", id).Scan(&lead)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if lead.Valid && lead.String != "" {
		if _, err := c.exec("UPDATE agents SET status='done', summary='team closed' WHERE name=? AND status='live'", lead.String); err != nil {
			return err
		}
	}
	if err := event(c, "team", id, "close", nil); err != nil {
		return err
	}
	gwPrintf(c, "%s closed\n", id)
	return nil
}

func lcTeamReopen(c *ctx, a gwArgs) error {
	id := a.pos[0]
	var status, goalID, gstatus string
	err := c.db.QueryRow("SELECT t.status, t.goal_id, g.status FROM teams t JOIN goals g ON g.id=t.goal_id WHERE t.id=?", id).
		Scan(&status, &goalID, &gstatus)
	if errors.Is(err, sql.ErrNoRows) {
		return dieErr(2, fmt.Sprintf("no team %s; usage: factory team reopen <team-id>", id))
	}
	if err != nil {
		return err
	}
	if status != "closed" {
		return dieErr(3, fmt.Sprintf("%s is %s, not closed; only a closed team can be reopened", id, status))
	}
	if gstatus == "done" || gstatus == "cancelled" {
		return dieErr(3, fmt.Sprintf("goal %s is %s; a team of a finished goal is not reopened", goalID, gstatus))
	}
	ts := eventTS(c)
	note := fmt.Sprintf("reopened from closed on %s; read your inbox and factory goal show %s", ts[:10], goalID)
	if _, err := c.exec("UPDATE teams SET status='active', parked_at=?, resume_note=? WHERE id=?", ts, note, id); err != nil {
		return err
	}
	if err := event(c, "team", id, "reopen", nil); err != nil {
		return err
	}
	gwPrintf(c, "%s reopened as parked\n", id)
	return nil
}

// ───────────────────────────── project ─────────────────────────────

var lcProjectFlags = map[string]byte{
	"--name": 's', "--repo": 's', "--gh": 's', "--base": 's', "--no-auto-merge": 'b', "--notes": 's',
	"--required-checks": 's', "--gate-cmd": 's', "--gate-count-cmd": 's', "--gate-timeout-s": 'i',
}

func lcParseProject(action string) func(rest []string) (any, bool) {
	return func(rest []string) (any, bool) {
		a, ok := lcSplit(rest, 1, lcProjectFlags)
		if !ok {
			return nil, false
		}
		a.action = action
		if len(a.pos) == 0 {
			return nil, false // Python slugs / looks up None
		}
		if action == "update" && a.s("--repo") != nil {
			return nil, false // git rev-parse of the repo: Python
		}
		if action == "add" && a.pos[0] == "" {
			return nil, false
		}
		return a, true
	}
}

// lcNormChecks is norm_checks.
func lcNormChecks(v string) string {
	var out []string
	for _, x := range strings.Split(v, ",") {
		if x = pyStrip(x); x != "" {
			out = append(out, x)
		}
	}
	return strings.Join(out, ",")
}

var lcGHRepoRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`) // fullmatch; Go's $ is end of text only

// lcExpandUser is str(Path(p).expanduser()); a "~user" form delegates.
func lcExpandUser(c *ctx, p string) (string, error) {
	if strings.HasPrefix(p, "//") && !strings.HasPrefix(p, "///") {
		return "", errDelegate
	}
	n := pyPathNorm(p)
	if !strings.HasPrefix(n, "~") {
		return n, nil
	}
	first, rest := n, ""
	if k := strings.Index(n, "/"); k >= 0 {
		first, rest = n[:k], n[k:]
	}
	if first != "~" {
		return "", errDelegate
	}
	home := c.env.Getenv("HOME")
	if home == "" {
		return "", errDelegate
	}
	res := strings.TrimRight(home, "/") + rest
	if res == "" {
		res = "/"
	}
	return pyPathNorm(res), nil
}

func lcProjectRun(c *ctx, p any) (int, error) {
	a := p.(gwArgs)
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	if role == "retro" {
		return 0, dieErr(3, fmt.Sprintf("role 'retro' (%s) may not run `factory project`; ask your lead", c.who))
	}
	if a.action == "add" {
		return 0, lcProjectAdd(c, a)
	}
	return 0, lcProjectUpdate(c, a)
}

func lcProjectAdd(c *ctx, a gwArgs) error {
	if _, err := requireRole(c, "boss", "director"); err != nil {
		return err
	}
	pid := slug(a.pos[0], 24)
	var one int
	if err := c.db.QueryRow("SELECT 1 FROM projects WHERE id=?", pid).Scan(&one); err == nil {
		return errDelegate // primary key clash: Python crashes
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	name := pid
	if n := a.s("--name"); n != nil && *n != "" {
		name = *n
	}
	var repo any
	if r := a.s("--repo"); r != nil {
		if *r == "" {
			repo = ""
		} else {
			e, err := lcExpandUser(c, *r)
			if err != nil {
				return err
			}
			repo = e
		}
	}
	base := "main"
	if b := a.s("--base"); b != nil {
		base = *b
	}
	auto := 1
	if a.bools["--no-auto-merge"] {
		auto = 0
	}
	notes, err := readText(c, a.s("--notes"), false)
	if err != nil {
		return err
	}
	checks := ""
	if r := a.s("--required-checks"); r != nil {
		checks = lcNormChecks(*r)
	}
	if _, err := c.exec("INSERT INTO projects(id,name,repo_path,gh_repo,base_branch,auto_merge,notes,created,required_checks) "+
		"VALUES(?,?,?,?,?,?,?,?,?)", pid, name, repo, gwNullable(a.s("--gh")), base, auto, gwNullable(notes), eventTS(c), checks); err != nil {
		return err
	}
	if err := event(c, "project", pid, "add", nil); err != nil {
		return err
	}
	gwPrintf(c, "%s\n", pid)
	return nil
}

func lcProjectUpdate(c *ctx, a gwArgs) error {
	if _, err := requireRole(c, "boss", "director"); err != nil {
		return err
	}
	id := a.pos[0]
	var one int
	if err := c.db.QueryRow("SELECT 1 FROM projects WHERE id=?", id).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			r, e := gwPyReprSimple(id)
			if e != nil {
				return e
			}
			return dieErr(1, fmt.Sprintf("no project %s: see `factory project list`", r))
		}
		return err
	}
	gateCmd, gateCount := a.s("--gate-cmd"), a.s("--gate-count-cmd")
	timeout, hasTimeout := a.n("--gate-timeout-s")
	gh := a.s("--gh")
	checks := a.s("--required-checks")
	hasGate := gateCmd != nil || gateCount != nil || hasTimeout
	if checks == nil && !hasGate && gh == nil {
		return dieErr(1, "nothing to update: pass --required-checks \"a,b\" (empty string = use the global config list) "+
			"or --gate-cmd / --gate-count-cmd / --gate-timeout-s / --gh OWNER/NAME / --repo PATH")
	}
	if hasGate || gh != nil {
		if _, err := requireRole(c, "director"); err != nil { // gate command and repo location steer the host
			return err
		}
		if hasTimeout && timeout < 1 {
			return dieErr(2, "--gate-timeout-s must be a positive number of seconds")
		}
	}
	if gh != nil && !lcGHRepoRE.MatchString(*gh) {
		r, err := gwPyReprSimple(*gh)
		if err != nil {
			return err
		}
		return dieErr(2, fmt.Sprintf("--gh must be OWNER/NAME (letters, digits, . _ -), got %s", r))
	}
	// Every repr is computed before the first write: a form Go cannot spell delegates with nothing written.
	var checksNorm, checksDetail string
	if checks != nil {
		checksNorm = lcNormChecks(*checks)
		r, err := gwPyReprSimple(checksNorm)
		if err != nil {
			return err
		}
		checksDetail = "required_checks=" + r
	}
	type upd struct {
		col, detail string
		v           any
	}
	var gates []upd
	for _, g := range []struct {
		col string
		s   *string
	}{{"gate_cmd", gateCmd}, {"gate_count_cmd", gateCount}} {
		if g.s == nil {
			continue
		}
		r, err := gwPyReprSimple(*g.s)
		if err != nil {
			return err
		}
		gates = append(gates, upd{g.col, g.col + "=" + r, *g.s})
	}
	if hasTimeout {
		gates = append(gates, upd{"gate_timeout_s", fmt.Sprintf("gate_timeout_s=%d", timeout), timeout})
	}
	var ghDetail string
	if gh != nil {
		var old sql.NullString
		if err := c.db.QueryRow("SELECT gh_repo FROM projects WHERE id=?", id).Scan(&old); err != nil {
			return err
		}
		oldRepr := "None"
		if old.Valid {
			r, err := gwPyReprSimple(old.String)
			if err != nil {
				return err
			}
			oldRepr = r
		}
		ghDetail = fmt.Sprintf("gh_repo: %s -> '%s'", oldRepr, *gh)
	}
	if checks != nil {
		if _, err := c.exec("UPDATE projects SET required_checks=? WHERE id=?", checksNorm, id); err != nil {
			return err
		}
		if err := event(c, "project", id, "update", strp(checksDetail)); err != nil {
			return err
		}
	}
	for _, g := range gates {
		if _, err := c.exec("UPDATE projects SET "+g.col+"=? WHERE id=?", g.v, id); err != nil {
			return err
		}
		if err := event(c, "project", id, "update", strp(g.detail)); err != nil {
			return err
		}
	}
	if gh != nil {
		if _, err := c.exec("UPDATE projects SET gh_repo=? WHERE id=?", *gh, id); err != nil {
			return err
		}
		if err := event(c, "project", id, "update", strp(ghDetail)); err != nil {
			return err
		}
	}
	gwPrintf(c, "%s\n", id)
	return nil
}
