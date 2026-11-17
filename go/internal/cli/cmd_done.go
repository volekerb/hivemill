package cli

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// done (G-048 T-1462): cmd_done in bin/factory_cli.py (11789). Every refusal Python makes is made here before the
// first write. The coder and judge paths read git, gh and the CI state (coder_done_findings, prcheck_done_refusal,
// judge_done_refusal); they hand over to Python before anything is read or written, so a stdin summary is not lost.

func init() {
	register("done", "", handler{parse: parseDone, run: runDone})
}

type doneCmd struct {
	summary string
	force   bool
}

// parseDone accepts `done [--force] SUMMARY` with the summary a single positional that does not start with "-"
// (except exactly "-"). Anything argparse could read differently (an abbreviation, a repeated flag, "--", a
// second positional, invalid UTF-8) fails the parse and Python answers.
func parseDone(rest []string) (any, bool) {
	var d doneCmd
	have := false
	for _, w := range rest {
		if !utf8.ValidString(w) {
			return nil, false
		}
		switch {
		case w == "--force":
			if d.force {
				return nil, false
			}
			d.force = true
		case w == "-" || !strings.HasPrefix(w, "-"):
			if have {
				return nil, false
			}
			have, d.summary = true, w
		default:
			return nil, false
		}
	}
	if !have {
		return nil, false
	}
	return d, true
}

// persistentRoles is PERSISTENT_ROLES (bin/factory_cli.py 116): never reaped.
var persistentRoles = map[string]bool{"boss": true, "director": true, "lead": true}

// jsonBy is json.dumps({"by": who}) for the done events.
func jsonBy(who string) string {
	var b strings.Builder
	b.WriteString(`{"by": `)
	dumpString(&b, who)
	b.WriteString("}")
	return b.String()
}

func runDone(c *ctx, parsed any) (int, error) {
	a := parsed.(doneCmd)
	var name, role, status string
	var taskID, goalID, teamID sql.NullString
	err := c.db.QueryRow("SELECT name, role, status, task_id, goal_id, team_id FROM agents WHERE name=?", c.who).
		Scan(&name, &role, &status, &taskID, &goalID, &teamID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, dieErr(1, "not a factory agent")
	}
	if err != nil {
		return 0, fmt.Errorf("read agent %s: %w", c.who, err)
	}
	me := c.who
	if status != "live" {
		return 0, dieErr(2, fmt.Sprintf("%s is '%s', not live: nothing to finish (no second done/report)", me, status))
	}
	if (role == "coder" || role == "judge") && taskID.String != "" {
		return 0, errDelegate // git, gh and CI gates stay in Python until their helpers are ported
	}
	sp, err := readText(c, &a.summary, false)
	if err != nil {
		return 0, err
	}
	summary := *sp
	persistent := persistentRoles[role]

	type msgRow struct {
		id                             int64
		kind, sender, created, subject string
		goal, task, body               sql.NullString
	}
	var unread []msgRow
	if !persistent {
		rows, err := c.query("SELECT id, kind, sender, created, subject, goal_id, task_id, body FROM messages "+
			"WHERE recipient=? AND read_at IS NULL AND kind IN ('decision','request') ORDER BY id", me)
		if err != nil {
			return 0, fmt.Errorf("read unread messages: %w", err)
		}
		for rows.Next() {
			var m msgRow
			if err := rows.Scan(&m.id, &m.kind, &m.sender, &m.created, &m.subject, &m.goal, &m.task, &m.body); err != nil {
				rows.Close()
				return 0, fmt.Errorf("scan unread message: %w", err)
			}
			unread = append(unread, m)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return 0, fmt.Errorf("read unread messages: %w", err)
		}
		if err := rows.Close(); err != nil {
			return 0, fmt.Errorf("close unread messages: %w", err)
		}
	}
	markRead := func() error {
		if len(unread) == 0 {
			return nil
		}
		args := []any{eventTS(c)}
		ph := make([]string, len(unread))
		for i, m := range unread {
			ph[i] = "?"
			args = append(args, m.id)
		}
		_, err := c.exec("UPDATE messages SET read_at=? WHERE id IN ("+strings.Join(ph, ",")+")", args...)
		return err
	}
	if len(unread) > 0 && !a.force {
		for _, m := range unread {
			line := fmt.Sprintf("── M%d [%s] from %s @ %s", m.id, m.kind, m.sender, m.created)
			if m.goal.String != "" {
				line += " goal=" + m.goal.String
			}
			if m.task.String != "" {
				line += " task=" + m.task.String
			}
			fmt.Fprintln(c.out, line)
			fmt.Fprintf(c.out, "   %s\n", m.subject)
			if m.body.String != "" {
				fmt.Fprintln(c.out, pyIndent(m.body.String, "   "))
			}
		}
		if err := markRead(); err != nil {
			return 0, fmt.Errorf("mark messages read: %w", err)
		}
		return 0, dieErr(3, "unread orders: read and act on them, then run factory done again")
	}

	qaBlocked := false
	if role == "qa" && taskID.String != "" && strings.HasPrefix(strings.TrimLeftFunc(summary, pyIsSpace), "BLOCKED") {
		var qrole, qstatus string
		var qpr sql.NullString
		err := c.db.QueryRow("SELECT role, pr_url, status FROM tasks WHERE id=?", taskID.String).Scan(&qrole, &qpr, &qstatus)
		found := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("read task %s: %w", taskID.String, err)
		}
		if found && qrole == "qa" && inList([]string{"todo", "in_progress", "in_review"}, qstatus) && pyStrip(qpr.String) == "" {
			var one int
			err := c.db.QueryRow("SELECT 1 FROM verdicts WHERE task_id=? AND role='qa'", taskID.String).Scan(&one)
			if errors.Is(err, sql.ErrNoRows) {
				qaBlocked = true
			} else if err != nil {
				return 0, fmt.Errorf("read verdicts of %s: %w", taskID.String, err)
			}
		}
		if qaBlocked {
			summary += fmt.Sprintf("\n\nNOTE: %s is NOT closed: a QA task with no PR ended BLOCKED with no verdict, "+
				"so it is back to todo. Fix the blocker (worktree or cwd) and respawn, or cancel it.", taskID.String)
		}
	}

	newStatus := "done"
	if persistent {
		newStatus = "live"
	}
	res, err := c.exec("UPDATE agents SET status=?, summary=?, ended=?, stop_requested_at=NULL, stop_attempt_at=NULL, "+
		"stop_failed_kind=NULL WHERE name=? AND status='live'", newStatus, summary, eventTS(c), me)
	if err != nil {
		return 0, fmt.Errorf("finish agent %s: %w", me, err)
	}
	if n, err := res.RowsAffected(); err != nil || n != 1 {
		return 0, dieErr(2, fmt.Sprintf("%s is no longer live: nothing to finish (no second done/report)", me))
	}
	to := ""
	if teamID.String != "" {
		var lead sql.NullString
		err := c.db.QueryRow("SELECT lead_agent FROM teams WHERE id=?", teamID.String).Scan(&lead)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("read team %s: %w", teamID.String, err)
		}
		to = lead.String
	}
	if to == "" {
		to = "director"
	}
	if to != me {
		if _, _, err := msgSend(c, to, pyStrip(me+" finished "+taskID.String), &summary, "report", nullPtr(goalID), nullPtr(taskID), ""); err != nil {
			return 0, err
		}
	}
	if taskID.String != "" && !persistent && role != "coder" {
		st := "done"
		if qaBlocked {
			st = "todo"
		}
		res, err := c.exec("UPDATE tasks SET status=?, updated=? WHERE id=? AND role=? AND status IN ('todo','in_progress','in_review')",
			st, eventTS(c), taskID.String, role)
		if err != nil {
			return 0, fmt.Errorf("close task %s: %w", taskID.String, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("close task %s: %w", taskID.String, err)
		}
		switch {
		case n > 0 && qaBlocked:
			err = event(c, "task", taskID.String, "done-blocked-reopened", strp(jsonBy(me)))
		case n > 0:
			err = event(c, "task", taskID.String, "done", strp(jsonBy(me)))
		default:
			var cur string
			qerr := c.db.QueryRow("SELECT status FROM tasks WHERE id=? AND role=?", taskID.String, role).Scan(&cur)
			if qerr == nil { // no matching task row (e.g. reviewer on a coder task): the agent 'done' event is the record
				var b strings.Builder
				b.WriteString(`{"by": `)
				dumpString(&b, me)
				b.WriteString(`, "status": `)
				dumpString(&b, cur)
				b.WriteString("}")
				err = event(c, "task", taskID.String, "done-ignored", strp(b.String()))
			} else if !errors.Is(qerr, sql.ErrNoRows) {
				err = fmt.Errorf("read task %s: %w", taskID.String, qerr)
			}
		}
		if err != nil {
			return 0, fmt.Errorf("record done of %s: %w", taskID.String, err)
		}
	}
	if err := event(c, "agent", me, "done", &summary); err != nil {
		return 0, fmt.Errorf("record done: %w", err)
	}
	if len(unread) > 0 {
		if err := markRead(); err != nil {
			return 0, fmt.Errorf("mark messages read: %w", err)
		}
		ids := make([]string, len(unread))
		for i, m := range unread {
			ids[i] = fmt.Sprintf("M%d", m.id)
		}
		list := strings.Join(ids, ", ")
		if err := event(c, "agent", me, "done-forced", strp("unread decision/request messages: "+list)); err != nil {
			return 0, fmt.Errorf("record forced done: %w", err)
		}
		if to != me {
			if _, _, err := msgSend(c, to, me+" closed with --force; unread orders: "+list, nil, "report", nullPtr(goalID), nullPtr(taskID), ""); err != nil {
				return 0, err
			}
		}
	}
	if persistent {
		fmt.Fprintf(c.out, "summary sent to %s\n", to)
	} else {
		fmt.Fprintf(c.out, "done; summary sent to %s. Your pane will be closed shortly — stop working now.\n", to)
	}
	return 0, nil
}
