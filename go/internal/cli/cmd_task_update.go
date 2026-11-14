package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// The S2l write command: task update, the forms without external side effects (cmd_task "update" in
// bin/factory_cli.py, 3637-3835). Everything the Python parser accepts that this file does not read, plus the two
// forms that call gh (--merged-externally, a coder's --status in_review), runs in Python before any DB write.

func init() {
	register("task", "update", handler{parse: parseTaskUpdate, run: runTaskUpdate})
}

type taskUpdCmd struct {
	id                                                                                            string
	status, pr, branch, assignee, title, spec, acceptance, team, after, waits, note, reason, goal *string
	sandbox, judgeHold, role                                                                      *string
	priority                                                                                      *int
	holdClear                                                                                     bool
}

// taskUpdAsciiInt is int(s) for a plain run of ASCII digits (at most 9); ok=false for anything else, because Python's
// int() also takes signs, spaces, underscores and non-ASCII digits.
func taskUpdAsciiInt(s string) (int, bool) {
	if s == "" || len(s) > 9 {
		return 0, false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// parseTaskUpdate accepts exact long flags (--flag v or --flag=v) and one positional. Anything argparse could read
// differently (abbreviation, unknown or non-update flag, -h, "--", a repeated flag, a value starting with "-" other
// than "-", a bad choice, a non-ASCII-digit or signed int, extra positionals) fails the parse: Python answers.
func parseTaskUpdate(rest []string) (any, bool) {
	var c taskUpdCmd
	dest := map[string]**string{
		"--status": &c.status, "--pr": &c.pr, "--branch": &c.branch, "--assignee": &c.assignee, "--title": &c.title,
		"--spec": &c.spec, "--acceptance": &c.acceptance, "--team": &c.team, "--after": &c.after, "--waits": &c.waits,
		"--note": &c.note, "--reason": &c.reason, "--goal": &c.goal, "--sandbox-required": &c.sandbox,
		"--judge-hold": &c.judgeHold, "--role": &c.role,
	}
	seen := map[string]bool{}
	haveID := false
	for i := 0; i < len(rest); i++ {
		w := rest[i]
		switch {
		case w == "--judge-hold-clear":
			if c.holdClear {
				return nil, false
			}
			c.holdClear = true
		case strings.HasPrefix(w, "-"):
			name, val, hasEq := strings.Cut(w, "=")
			if !strings.HasPrefix(name, "--") || seen[name] {
				return nil, false
			}
			if !hasEq {
				if i+1 >= len(rest) {
					return nil, false
				}
				i++
				val = rest[i]
			}
			if strings.HasPrefix(val, "-") && val != "-" {
				return nil, false
			}
			seen[name] = true
			if name == "--priority" {
				n, ok := taskUpdAsciiInt(val)
				if !ok {
					return nil, false
				}
				c.priority = &n
				continue
			}
			d, ok := dest[name]
			if !ok {
				return nil, false
			}
			v := val
			*d = &v
		default:
			if haveID {
				return nil, false
			}
			haveID = true
			c.id = w
		}
	}
	if !haveID {
		return nil, false
	}
	if c.waits != nil && *c.waits != "owner" && *c.waits != "internal" {
		return nil, false
	}
	if c.sandbox != nil && *c.sandbox != "0" && *c.sandbox != "1" {
		return nil, false
	}
	return c, true
}

// taskUpdTruthy is Python truthiness of an optional string.
func taskUpdTruthy(p *string) bool { return p != nil && *p != "" }

func taskUpdStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// taskUpdWorkerRoles is sorted(WORKER_ROLES) as SQL arguments.
func taskUpdWorkerRoles() (string, []any) {
	var rs []string
	for r := range workerRoles {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	args := make([]any, len(rs))
	for i, r := range rs {
		args[i] = r
	}
	return strings.TrimSuffix(strings.Repeat("?,", len(rs)), ","), args
}

func taskUpdHigh(role string) bool {
	return role == "owner" || role == "lead" || role == "director" || role == "boss"
}

func runTaskUpdate(c *ctx, p any) (int, error) {
	a := p.(taskUpdCmd)
	var tGoal, tStatus, tRole, tWaits, tDeps string
	var tPR, tHold sql.NullString
	var tSandbox int
	err := c.db.QueryRow("SELECT goal_id,status,role,waits,depends_on,pr_url,sandbox_required FROM tasks WHERE id=?", a.id).
		Scan(&tGoal, &tStatus, &tRole, &tWaits, &tDeps, &tPR, &tSandbox)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dieErr(1, "no task "+a.id)
	}
	if err != nil {
		return 0, err
	}
	if a.holdClear {
		// judge_hold_reason is added by Python's migrate(); a database without it is Python's to open.
		if err := c.db.QueryRow("SELECT judge_hold_reason FROM tasks WHERE id=?", a.id).Scan(&tHold); err != nil {
			return 0, errDelegate
		}
	}
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	hasPR := tPR.String != "" || taskUpdTruthy(a.pr)
	switch role {
	case "reviewer", "judge", "qa", "cleaner":
		if a.status != nil && *a.status != "blocked" {
			return 0, dieErr(3, role+" reports through `factory verdict`/`factory judge`, not task status")
		}
	}
	if role == "pm" && a.status != nil {
		narrow := "pm changes task status only with factory task reopen or --status done on a task without a PR"
		if *a.status != "done" {
			return 0, dieErr(3, narrow)
		}
		if err := pmOwnGoal(c, tGoal, a.id); err != nil {
			return 0, err
		}
		if hasPR {
			return 0, dieErr(3, "a coder task with a PR is only done when the judge merges it")
		}
		if tStatus != "todo" && tStatus != "in_progress" && tStatus != "blocked" {
			return 0, dieErr(3, narrow)
		}
		if tRole == "coder" {
			return 0, dieErr(3, "pm may not close a coder task without a PR: reopen it (factory task reopen T --reason ...) or file a follow-up (factory task add --follow-up-of T ...); only a lead, director or the owner may close it")
		}
		if pyStrip(taskUpdStr(a.note)) == "" {
			return 0, dieErr(2, "pm must give --note with the evidence when closing a task")
		}
	}
	if a.priority != nil {
		if !taskUpdHigh(role) {
			return 0, dieErr(3, fmt.Sprintf("only a lead, director or the owner may set a task's priority (%s may not use --priority)", role))
		}
		if *a.priority < 1 || *a.priority > 3 {
			return 0, dieErr(2, "--priority must be 1, 2 or 3")
		}
	}
	checkSandbox := func(r string) error {
		if !taskSandboxable[r] {
			return dieErr(2, "--sandbox-required: "+r+" cannot be sandboxed; allowed: coder, qa, researcher, reviewer")
		}
		return nil
	}
	if a.role != nil {
		if !taskUpdHigh(role) {
			return 0, dieErr(3, fmt.Sprintf("only a lead, director or the owner may change a task's role (%s may not use --role)", role))
		}
		known := false
		for _, r := range factoryRoles {
			known = known || r == *a.role
		}
		if !known {
			return 0, dieErr(2, "role must be one of "+taskRolesRepr())
		}
		if tRole == "coder" && hasPR && role != "owner" {
			return 0, dieErr(3, "a coder task with a PR is closed only by the judge: only the owner may change its role")
		}
		ph, wargs := taskUpdWorkerRoles()
		var holder string
		err := c.db.QueryRow("SELECT name FROM agents WHERE task_id=? AND status IN ('live','starting','queued') "+
			"AND stop_requested_at IS NULL AND role IN ("+ph+") LIMIT 1", append([]any{a.id}, wargs...)...).Scan(&holder)
		if err == nil {
			return 0, dieErr(2, fmt.Sprintf("%s is held by agent %s; change the role with: factory stop %s --reason ..., then factory task update %s --role %s, then factory spawn %s --task %s",
				a.id, holder, holder, a.id, *a.role, *a.role, a.id))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		if (a.sandbox != nil && *a.sandbox == "1") || (a.sandbox == nil && tSandbox != 0) {
			if err := checkSandbox(*a.role); err != nil {
				return 0, err
			}
		}
	}
	if a.sandbox != nil {
		if !taskUpdHigh(role) {
			return 0, dieErr(3, fmt.Sprintf("only a lead, director or the owner may set a task's sandbox_required (%s may not use --sandbox-required)", role))
		}
		if *a.sandbox == "1" {
			r := tRole
			if taskUpdTruthy(a.role) {
				r = *a.role
			}
			if err := checkSandbox(r); err != nil {
				return 0, err
			}
		}
	}
	if a.judgeHold != nil || a.holdClear {
		if !taskUpdHigh(role) {
			return 0, dieErr(3, fmt.Sprintf("only a lead, director or the owner may set a task's judge hold (%s may not use --judge-hold)", role))
		}
		if a.judgeHold != nil && a.holdClear {
			return 0, dieErr(2, "give --judge-hold or --judge-hold-clear, not both")
		}
		if a.judgeHold != nil && pyStrip(*a.judgeHold) == "" {
			return 0, dieErr(2, "--judge-hold needs a non-empty reason")
		}
	}
	if a.team != nil {
		if !taskUpdHigh(role) {
			return 0, dieErr(3, fmt.Sprintf("only a lead, director or the owner may change a task's team (%s may not use --team)", role))
		}
		if role == "lead" {
			var own sql.NullString
			err := c.db.QueryRow("SELECT goal_id FROM agents WHERE name=?", c.who).Scan(&own)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return 0, err
			}
			if own.String == "" || own.String != tGoal {
				ownS := own.String
				if ownS == "" {
					ownS = "no goal"
				}
				return 0, dieErr(3, fmt.Sprintf("%s leads %s; --team on %s is in %s", c.who, ownS, a.id, tGoal))
			}
		}
		var tg sql.NullString
		err := c.db.QueryRow("SELECT goal_id FROM teams WHERE id=?", *a.team).Scan(&tg)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, dieErr(2, "--team: no team "+*a.team)
		}
		if err != nil {
			return 0, err
		}
		if tg.String != tGoal {
			tgS := "None"
			if tg.Valid {
				tgS = tg.String
			}
			return 0, dieErr(2, fmt.Sprintf("--team: team %s belongs to goal %s, but %s is in goal %s", *a.team, tgS, a.id, tGoal))
		}
	}
	if a.goal != nil {
		if role != "owner" && role != "director" && role != "boss" {
			return 0, dieErr(3, fmt.Sprintf("only the director or the owner may move a task to another goal (%s may not use --goal)", role))
		}
		if *a.goal == tGoal {
			return 0, dieErr(2, fmt.Sprintf("%s is already in goal %s; nothing to move", a.id, *a.goal))
		}
		if a.team != nil {
			return 0, dieErr(2, "--goal clears the task's team; run the --team update as a second call once the task is in the new goal")
		}
		var one int
		err := c.db.QueryRow("SELECT 1 FROM goals WHERE id=?", *a.goal).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return 0, dieErr(2, "--goal: no goal "+*a.goal)
		}
		if err != nil {
			return 0, err
		}
		if tStatus == "in_progress" || tStatus == "in_review" {
			return 0, dieErr(2, fmt.Sprintf("%s is %s; stop its worker and reopen it before moving it to another goal", a.id, tStatus))
		}
		var live string
		err = c.db.QueryRow("SELECT name FROM agents WHERE task_id=? AND status='live' LIMIT 1", a.id).Scan(&live)
		if err == nil {
			return 0, dieErr(2, fmt.Sprintf("%s has a live agent %s; factory stop %s --reason ... before moving the task", a.id, live, live))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
		own, err := taskDepsOf(c, a.id, nil)
		if err != nil {
			return 0, err
		}
		if len(own) > 0 {
			return 0, dieErr(2, fmt.Sprintf("%s depends on %s in %s; clear it first: factory task update %s --after ''", a.id, tDeps, tGoal, a.id))
		}
		rs, err := c.query("SELECT id FROM tasks WHERE goal_id=?", tGoal)
		if err != nil {
			return 0, err
		}
		var ids []string
		for rs.Next() {
			var id string
			if err := rs.Scan(&id); err != nil {
				rs.Close()
				return 0, err
			}
			ids = append(ids, id)
		}
		rs.Close()
		if err := rs.Err(); err != nil {
			return 0, err
		}
		var dependents []string
		for _, id := range ids {
			ds, err := taskDepsOf(c, id, nil)
			if err != nil {
				return 0, err
			}
			for _, d := range ds {
				if d == a.id {
					dependents = append(dependents, id)
					break
				}
			}
		}
		if len(dependents) > 0 {
			return 0, dieErr(2, fmt.Sprintf("%s depend on %s in %s; clear their --after first", strings.Join(dependents, ", "), a.id, tGoal))
		}
	} else if a.status == nil && a.pr == nil && a.branch == nil && a.assignee == nil && a.title == nil && a.spec == nil &&
		a.acceptance == nil && a.team == nil && a.priority == nil && a.after == nil && a.waits == nil && a.note == nil &&
		a.reason == nil && a.sandbox == nil && a.judgeHold == nil && a.role == nil && !a.holdClear {
		return 0, dieErr(2, fmt.Sprintf("nothing to update on %s: give at least one of --status --pr --branch --assignee --title --spec "+
			"--acceptance --team --priority --after --waits --note --goal --sandbox-required --judge-hold "+
			"--judge-hold-clear --role", a.id))
	}
	curStatus := tStatus
	if taskUpdTruthy(a.status) {
		curStatus = *a.status
	}
	if a.waits != nil && curStatus != "blocked" {
		return 0, dieErr(2, "--waits applies to blocked tasks: use it with --status blocked")
	}
	if taskUpdTruthy(a.reason) && taskUpdTruthy(a.note) {
		return 0, dieErr(2, "give --reason or --note, not both (--reason is the blocked reason)")
	}
	note := a.note
	if !taskUpdTruthy(note) {
		note = a.reason
	}
	if role == "coder" && a.status != nil && *a.status == "in_review" {
		return 0, errDelegate // require_acceptance_walk runs gh
	}
	if a.spec != nil || a.acceptance != nil {
		ph, wargs := taskUpdWorkerRoles()
		var live string
		err := c.db.QueryRow("SELECT name FROM agents WHERE task_id=? AND status='live' AND stop_requested_at IS NULL AND role IN ("+ph+") LIMIT 1",
			append([]any{a.id}, wargs...)...).Scan(&live)
		if err == nil {
			return 0, dieErr(2, fmt.Sprintf("%s has a live worker %s; it will not re-read its task. Spec change = factory stop %s --reason ..., "+
				"then factory task update %s --spec/--acceptance, then factory spawn %s --task %s", a.id, live, live, a.id, tRole, a.id))
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return 0, err
		}
	}
	if a.after != nil && (a.spec != nil && *a.spec == "-" || a.acceptance != nil && *a.acceptance == "-") {
		// checkAfter may delegate (malformed depends_on); Python cannot get stdin back once it is read here, so find
		// out first. Its refusals wait for their place in Python's order.
		if err := checkAfter(c, a.id, tGoal, splitAfter(*a.after)); errors.Is(err, errDelegate) {
			return 0, err
		}
	}
	spec, err := readText(c, a.spec, false)
	if err != nil {
		return 0, err
	}
	acceptance, err := readText(c, a.acceptance, false)
	if err != nil {
		return 0, err
	}
	for _, f := range []struct {
		flag string
		raw  *string
		val  *string
	}{{"--spec", a.spec, spec}, {"--acceptance", a.acceptance, acceptance}} {
		if f.raw != nil && *f.raw == "-" && taskUpdStr(f.val) == "" {
			return 0, dieErr(2, fmt.Sprintf("%s - read empty stdin: refusing to blank the field, nothing was changed on %s. "+
				"Pipe the text in (heredoc) or pass it inline; to clear it on purpose use %s \"\"", f.flag, a.id, f.flag))
		}
	}
	sets, args := []string{"updated=?"}, []any{eventTS(c)}
	var deps []string
	haveDeps := false
	if a.after != nil {
		deps = splitAfter(*a.after)
		haveDeps = true
		if err := checkAfter(c, a.id, tGoal, deps); err != nil {
			return 0, err
		}
		sets = append(sets, "depends_on=?")
		args = append(args, taskUpdJSONList(deps))
	}
	if a.status != nil {
		ok := false
		for _, s := range taskStatuses {
			ok = ok || s == *a.status
		}
		if !ok {
			q := make([]string, len(taskStatuses))
			for i, s := range taskStatuses {
				q[i] = "'" + s + "'"
			}
			return 0, dieErr(1, "status must be one of ["+strings.Join(q, ", ")+"]")
		}
		if *a.status == "done" && tRole == "coder" {
			if hasPR {
				if role != "owner" && role != "judge" {
					return 0, dieErr(3, "a coder task with a PR is only done when the judge merges it")
				}
			} else if role != "owner" && role != "judge" && role != "lead" && role != "director" {
				return 0, dieErr(3, "only a lead, director or the owner may close a coder task that has no PR; a task with a PR is closed by the judge via `factory judge`")
			}
		}
	}
	if a.pr != nil && *a.pr == "" && tPR.String != "" && role != "owner" && role != "judge" {
		return 0, dieErr(3, "only the owner or judge may clear the PR of a task")
	}
	// The SET list in Python's column order: status, pr_url, branch, assignee, title, spec, acceptance, team_id,
	// priority, goal_id, role.
	for _, col := range []struct {
		name string
		val  any
		set  bool
	}{
		{"status", taskUpdStr(a.status), a.status != nil}, {"pr_url", taskUpdStr(a.pr), a.pr != nil},
		{"branch", taskUpdStr(a.branch), a.branch != nil}, {"assignee", taskUpdStr(a.assignee), a.assignee != nil},
		{"title", taskUpdStr(a.title), a.title != nil}, {"spec", taskUpdStr(spec), spec != nil},
		{"acceptance", taskUpdStr(acceptance), acceptance != nil}, {"team_id", taskUpdStr(a.team), a.team != nil},
		{"priority", func() any {
			if a.priority == nil {
				return nil
			}
			return *a.priority
		}(), a.priority != nil},
		{"goal_id", taskUpdStr(a.goal), a.goal != nil}, {"role", taskUpdStr(a.role), a.role != nil},
	} {
		if col.set {
			sets = append(sets, col.name+"=?")
			args = append(args, col.val)
		}
	}
	if a.goal != nil {
		sets = append(sets, "team_id=NULL")
	}
	if a.status != nil && *a.status == "in_progress" && tStatus != "in_progress" {
		sets = append(sets, "attempts=attempts+1")
	}
	if a.status != nil && *a.status == "blocked" { // default internal, kept when already blocked (T-0370)
		if a.waits != nil || tStatus != "blocked" {
			w := "internal"
			if taskUpdTruthy(a.waits) {
				w = *a.waits
			}
			sets = append(sets, "waits=?")
			args = append(args, w)
		}
	} else if taskUpdTruthy(a.status) || taskUpdTruthy(a.waits) {
		if a.waits == nil && tStatus == "blocked" {
			sets = append(sets, "waits=?")
			args = append(args, "internal") // leaving blocked clears the owner flag
		} else if a.waits != nil {
			sets = append(sets, "waits=?")
			args = append(args, *a.waits)
		}
	}
	// Reads that can still delegate come before the first write.
	var plan *fileOverlap
	if spec != nil {
		cur := deps
		if !haveDeps {
			if cur, err = taskDepsOf(c, a.id, nil); err != nil {
				return 0, err
			}
		}
		if plan, err = planFileOverlap(c, a.id, tGoal, spec, cur); err != nil {
			return 0, err
		}
	}
	// Python 3825-3860: UPDATE tasks, event update, [UPDATE tasks sandbox, event], [UPDATE tasks judge hold, event],
	// overlap event, [UPDATE goals, event].
	if _, err := c.exec("UPDATE tasks SET "+strings.Join(sets, ", ")+" WHERE id=?", append(args, a.id)...); err != nil {
		return 0, err
	}
	var parts []string
	if taskUpdTruthy(a.status) {
		parts = append(parts, "status="+*a.status)
	}
	if taskUpdTruthy(a.pr) {
		parts = append(parts, "pr="+*a.pr)
	}
	if taskUpdTruthy(a.team) {
		parts = append(parts, "team="+*a.team)
	}
	if a.priority != nil && *a.priority != 0 {
		parts = append(parts, "priority="+strconv.Itoa(*a.priority))
	}
	if a.goal != nil {
		parts = append(parts, fmt.Sprintf("goal=%s -> %s", tGoal, *a.goal))
	}
	if a.role != nil {
		parts = append(parts, fmt.Sprintf("role=%s->%s", tRole, *a.role))
	}
	if a.status != nil && *a.status == "blocked" {
		w := "internal"
		switch {
		case taskUpdTruthy(a.waits):
			w = *a.waits
		case tStatus == "blocked":
			w = tWaits
		}
		parts = append(parts, "waits="+w)
	}
	if haveDeps {
		parts = append(parts, "depends_on="+taskUpdJSONList(deps))
	}
	detail := strings.Join(parts, " ")
	if taskUpdTruthy(note) {
		if detail != "" {
			detail += " — " + *note
		} else {
			detail = *note
		}
	}
	if err := event(c, "task", a.id, "update", &detail); err != nil {
		return 0, err
	}
	if a.sandbox != nil {
		n, _ := strconv.Atoi(*a.sandbox)
		if n != tSandbox {
			if _, err := c.exec("UPDATE tasks SET sandbox_required=? WHERE id=?", n, a.id); err != nil {
				return 0, err
			}
			if err := event(c, "task", a.id, "sandbox-required", a.sandbox); err != nil {
				return 0, err
			}
		}
	}
	if a.judgeHold != nil {
		reason := pyStrip(*a.judgeHold)
		if _, err := c.exec("UPDATE tasks SET judge_hold_reason=? WHERE id=?", reason, a.id); err != nil {
			return 0, err
		}
		if err := event(c, "task", a.id, "judge-hold-set", &reason); err != nil {
			return 0, err
		}
	} else if a.holdClear && pyStrip(tHold.String) != "" {
		if _, err := c.exec("UPDATE tasks SET judge_hold_reason=NULL WHERE id=?", a.id); err != nil {
			return 0, err
		}
		if err := event(c, "task", a.id, "judge-hold-clear", strp("")); err != nil {
			return 0, err
		}
	}
	if plan != nil {
		if err := applyFileOverlap(c, a.id, plan); err != nil {
			return 0, err
		}
	}
	if a.status != nil && *a.status == "in_progress" {
		// goal_auto_active (3206): proposed/planning -> active, one event.
		res, err := c.exec("UPDATE goals SET status='active', updated=? WHERE id=? AND status IN ('proposed','planning')", eventTS(c), tGoal)
		if err != nil {
			return 0, err
		}
		if n, err := res.RowsAffected(); err != nil {
			return 0, err
		} else if n > 0 {
			if err := event(c, "goal", tGoal, "goal-auto-active", strp("task "+a.id+" in_progress (task update)")); err != nil {
				return 0, err
			}
		}
	}
	fmt.Fprintf(c.out, "%s updated\n", a.id)
	return 0, nil
}

// taskUpdJSONList is json.dumps of a list of strings.
func taskUpdJSONList(ids []string) string {
	var b strings.Builder
	b.WriteByte('[')
	for i, d := range ids {
		if i > 0 {
			b.WriteString(", ")
		}
		dumpString(&b, d)
	}
	b.WriteByte(']')
	return b.String()
}
