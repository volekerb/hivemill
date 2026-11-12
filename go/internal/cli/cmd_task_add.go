package cli

import (
	"database/sql"
	"fmt"
	"regexp"
	"strings"
)

// The S2k write commands: task add, task note and task reopen (cmd_task in bin/factory_cli.py, 3556-3612 and
// 3854-3864). Each answers only the exact argv shapes below; every other form (abbreviated or --x=v flags, -h, a
// flag the Python parser declares but the branch does not read, a flag value that is empty or starts with "-", a
// repeated flag, a positional starting with "-" other than exactly "-", too many positionals) runs in Python.

func init() {
	register("task", "add", handler{parse: parseTaskAdd, run: runTaskAdd})
	register("task", "note", handler{parse: parseTaskNote, run: runTaskNote})
	register("task", "reopen", handler{parse: parseTaskReopen, run: runTaskReopen})
}

type taskAddCmd struct {
	goal, team, title, role, after, spec, acceptance, followUpOf, sandbox string
}

func parseTaskAdd(rest []string) (any, bool) {
	var c taskAddCmd
	ok := taskFlagParse(rest, map[string]*string{"--goal": &c.goal, "--team": &c.team, "--title": &c.title, "--role": &c.role,
		"--after": &c.after, "--spec": &c.spec, "--acceptance": &c.acceptance, "--follow-up-of": &c.followUpOf,
		"--sandbox-required": &c.sandbox})
	// Python dies on a missing goal and crashes on a missing title; a sandbox value outside choices is a usage error.
	if !ok || c.goal == "" || c.title == "" || (c.sandbox != "" && c.sandbox != "0" && c.sandbox != "1") {
		return nil, false
	}
	return c, true
}

// taskSandboxable is workerbox.SANDBOXABLE (scout/workerbox.py).
var taskSandboxable = map[string]bool{"reviewer": true, "qa": true, "coder": true, "researcher": true}

// taskRolesRepr is str(ROLES), the list repr Python prints.
func taskRolesRepr() string {
	q := make([]string, len(factoryRoles))
	for i, r := range factoryRoles {
		q[i] = "'" + r + "'"
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// taskSCTagRE is SC_TAG_RE with Python's \d and \s spelled out for RE2.
var taskSCTagRE = regexp.MustCompile(`\[(SC\p{Nd}+(?:[\t-\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]*,[\t-\r\x1c-\x20\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}]*SC\p{Nd}+)*)\]`)

func optStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func runTaskAdd(c *ctx, p any) (int, error) {
	a := p.(taskAddCmd)
	if _, err := requireRole(c, "pm", "lead", "director"); err != nil {
		return 0, err
	}
	var goalID string
	var criteria sql.NullString
	err := c.db.QueryRow("SELECT id,success_criteria FROM goals WHERE id=?", a.goal).Scan(&goalID, &criteria)
	if err == sql.ErrNoRows {
		return 0, dieErr(1, "no goal "+a.goal)
	}
	if err != nil {
		return 0, err
	}
	if a.followUpOf != "" {
		if err := pmOwnGoal(c, goalID, "the follow-up"); err != nil {
			return 0, err
		}
		var status string
		err := c.db.QueryRow("SELECT status FROM tasks WHERE id=? AND goal_id=?", a.followUpOf, goalID).Scan(&status)
		if err == sql.ErrNoRows {
			return 0, dieErr(2, fmt.Sprintf("--follow-up-of: no task %s in goal %s", a.followUpOf, goalID))
		}
		if err != nil {
			return 0, err
		}
		if status != "done" {
			return 0, dieErr(2, fmt.Sprintf("--follow-up-of: %s is %s; follow-ups are filed against done tasks", a.followUpOf, status))
		}
	}
	newRole := a.role
	if newRole == "" {
		newRole = "coder"
	}
	known := false
	for _, r := range factoryRoles {
		known = known || r == newRole
	}
	if !known {
		return 0, dieErr(1, "role must be one of "+taskRolesRepr())
	}
	if a.sandbox == "1" && !taskSandboxable[newRole] {
		return 0, dieErr(2, "--sandbox-required: "+newRole+" cannot be sandboxed; allowed: coder, qa, researcher, reviewer")
	}
	deps := splitAfter(a.after)
	tid, err := nextID(c, "tasks", "T", 4)
	if err != nil {
		return 0, err
	}
	if err := checkAfter(c, tid, goalID, deps); err != nil {
		return 0, err
	}
	var specArg, accArg *string
	if a.spec != "" {
		if specArg, err = readText(c, &a.spec, false); err != nil {
			return 0, err
		}
	}
	if a.acceptance != "" {
		if accArg, err = readText(c, &a.acceptance, false); err != nil {
			return 0, err
		}
	}
	plan, err := planFileOverlap(c, tid, goalID, specArg, deps) // reads only; may delegate
	if err != nil {
		return 0, err
	}
	var specV, accV any
	if specArg != nil {
		specV = *specArg
	}
	if accArg != nil {
		accV = *accArg
	}
	var depsJSON strings.Builder
	depsJSON.WriteByte('[')
	for i, d := range deps {
		if i > 0 {
			depsJSON.WriteString(", ")
		}
		dumpString(&depsJSON, d)
	}
	depsJSON.WriteByte(']')
	// Python 3576-3590: INSERT tasks, event add, [UPDATE sandbox, event], [event follow-up], overlap event.
	if _, err := c.exec("INSERT INTO tasks(id,goal_id,team_id,title,spec,acceptance,role,depends_on,created,updated,follow_up_of) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?)", tid, goalID, optStr(a.team), a.title, specV, accV, newRole, depsJSON.String(),
		eventTS(c), eventTS(c), optStr(a.followUpOf)); err != nil {
		return 0, err
	}
	if err := event(c, "task", tid, "add", &a.title); err != nil {
		return 0, err
	}
	if a.sandbox == "1" {
		if _, err := c.exec("UPDATE tasks SET sandbox_required=1 WHERE id=?", tid); err != nil {
			return 0, err
		}
		if err := event(c, "task", tid, "sandbox-required", strp("1")); err != nil {
			return 0, err
		}
	}
	if a.followUpOf != "" {
		if err := event(c, "task", tid, "follow-up", strp("follow-up of "+a.followUpOf)); err != nil {
			return 0, err
		}
	}
	if err := applyFileOverlap(c, tid, plan); err != nil {
		return 0, err
	}
	if pyStrip(criteria.String) != "" && !taskSCTagRE.MatchString(a.title) {
		fmt.Fprintf(c.errb, "warning: %s title has no [SCn] tag but %s has success criteria; the goal page shows "+
			"them untracked. Retitle: factory task update %s --title '<title> [SCn]'\n", tid, goalID, tid)
	}
	fmt.Fprintln(c.out, tid)
	return 0, nil
}

type taskNoteCmd struct{ id, text, extra *string }

// taskPositionals splits the words after the action into at most max positionals; a word starting with "-" other
// than exactly "-" could be an option to argparse, so the parse fails.
func taskPositionals(rest []string, max int) ([]*string, bool) {
	if len(rest) > max {
		return nil, false
	}
	out := make([]*string, max)
	for i := range rest {
		if strings.HasPrefix(rest[i], "-") && rest[i] != "-" {
			return nil, false
		}
		out[i] = &rest[i]
	}
	return out, true
}

func parseTaskNote(rest []string) (any, bool) {
	ps, ok := taskPositionals(rest, 3)
	if !ok {
		return nil, false
	}
	return taskNoteCmd{ps[0], ps[1], ps[2]}, true
}

// taskPyRepr is repr() of a str made of printable ASCII, \n, \r, \t; ok=false for anything else (Python escapes
// or keeps non-ASCII by rules Go does not reproduce).
func taskPyRepr(s string) (string, bool) {
	q := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = '"'
	}
	var b strings.Builder
	b.WriteByte(q)
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case ch == q || ch == '\\':
			b.WriteByte('\\')
			b.WriteByte(ch)
		case ch == '\n':
			b.WriteString(`\n`)
		case ch == '\r':
			b.WriteString(`\r`)
		case ch == '\t':
			b.WriteString(`\t`)
		case ch >= 0x20 && ch < 0x7f:
			b.WriteByte(ch)
		default:
			return "", false
		}
	}
	b.WriteByte(q)
	return b.String(), true
}

func runTaskNote(c *ctx, p any) (int, error) {
	a := p.(taskNoteCmd)
	// task_note_refuse_id, 3540-3550
	if a.extra != nil {
		return 0, dieErr(2, "task note takes one task id and note text; extra arguments were given. Nothing was written.")
	}
	idRaw := ""
	if a.id != nil {
		idRaw = *a.id
	}
	tid := pyStrip(idRaw)
	if tid == "" || !taskIDRe.MatchString(tid) {
		repr := "None"
		if a.id != nil {
			r, ok := taskPyRepr(*a.id)
			if !ok {
				return 0, errDelegate
			}
			repr = r
		}
		return 0, dieErr(2, fmt.Sprintf("task note id is not one task id (%s). Nothing was written.", repr))
	}
	if a.text != nil && taskIDRe.MatchString(pyStrip(*a.text)) {
		return 0, dieErr(2, "task note takes one task id and note text; a second task id was given. Nothing was written.")
	}
	var one int
	err := c.db.QueryRow("SELECT 1 FROM tasks WHERE id=?", tid).Scan(&one)
	if err == sql.ErrNoRows {
		return 0, dieErr(2, "no task "+tid+". Nothing was written.")
	}
	if err != nil {
		return 0, err
	}
	text, err := readText(c, a.text, false)
	if err != nil {
		return 0, err
	}
	if text == nil || pyStrip(*text) == "" {
		return 0, dieErr(2, "task note text is empty; pass the note as an argument or on stdin with '-'. Nothing was written.")
	}
	if err := event(c, "task", tid, "note", text); err != nil {
		return 0, err
	}
	fmt.Fprintln(c.out, "noted")
	return 0, nil
}

type taskReopenCmd struct{ id, reason string }

func parseTaskReopen(rest []string) (any, bool) {
	var c taskReopenCmd
	var pos []string
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--reason" {
			if c.reason != "" || i+1 >= len(rest) || rest[i+1] == "" || (rest[i+1][0] == '-' && rest[i+1] != "-") {
				return nil, false
			}
			i++
			c.reason = rest[i]
		} else if strings.HasPrefix(rest[i], "-") {
			return nil, false
		} else {
			pos = append(pos, rest[i])
		}
	}
	if len(pos) != 1 || pos[0] == "" {
		return nil, false
	}
	c.id = pos[0]
	return c, true
}

func runTaskReopen(c *ctx, p any) (int, error) {
	a := p.(taskReopenCmd)
	role, err := actorRole(c)
	if err != nil {
		return 0, err
	}
	switch role {
	case "owner", "pm", "lead", "director":
	default:
		return 0, dieErr(3, fmt.Sprintf("role '%s' may not reopen tasks; allowed: pm (own goal), lead, director, owner", role))
	}
	var goalID, status string
	var prURL sql.NullString
	err = c.db.QueryRow("SELECT goal_id,status,pr_url FROM tasks WHERE id=?", a.id).Scan(&goalID, &status, &prURL)
	if err == sql.ErrNoRows {
		return 0, dieErr(1, "no task "+a.id)
	}
	if err != nil {
		return 0, err
	}
	if err := pmOwnGoal(c, goalID, a.id); err != nil {
		return 0, err
	}
	if pyStrip(a.reason) == "" {
		return 0, dieErr(2, "--reason is required")
	}
	if status != "done" {
		return 0, dieErr(2, fmt.Sprintf("%s is %s, not done: only done tasks are reopened", a.id, status))
	}
	if prURL.String != "" {
		return 0, dieErr(2, fmt.Sprintf("%s has a PR (%s); merged work is not reopened: file a follow-up with factory task add --follow-up-of %s",
			a.id, prURL.String, a.id))
	}
	var gs sql.NullString
	if err := c.db.QueryRow("SELECT status FROM goals WHERE id=?", goalID).Scan(&gs); err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	if gs.Valid && gs.String == "done" {
		return 0, dieErr(2, fmt.Sprintf("goal %s is done; the director must reopen the goal first", goalID))
	}
	if _, err := c.exec("UPDATE tasks SET status='todo', updated=? WHERE id=?", eventTS(c), a.id); err != nil {
		return 0, err
	}
	if err := event(c, "task", a.id, "reopen", &a.reason); err != nil {
		return 0, err
	}
	fmt.Fprintf(c.out, "%s reopened\n", a.id)
	return 0, nil
}

// taskFlagParse walks long value flags only. Unlike flagParser a value may be exactly "-" (stdin for --spec); an empty
// value, one starting with "-", an unknown or repeated flag, or a missing value fails the parse.
func taskFlagParse(rest []string, vals map[string]*string) bool {
	seen := map[string]bool{}
	for i := 0; i < len(rest); i++ {
		v, ok := vals[rest[i]]
		if !ok || seen[rest[i]] || i+1 >= len(rest) || rest[i+1] == "" || (rest[i+1][0] == '-' && rest[i+1] != "-") {
			return false
		}
		seen[rest[i]] = true
		i++
		*v = rest[i]
	}
	return true
}
