package s2tasks

import (
	"fmt"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_goal_close.py (25 of 25 cases). Complete. G-004 B3: goal close gate and goal status gating.

func gcEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "done"})
	e.SeedTask(ft.Task{ID: "T-0002", Status: "cancelled"})
	seedTask(e, ft.Task{ID: "T-0003", Role: "pm", Status: "in_progress"}, true) // the audit pm's own task
	seedTask(e, ft.Task{ID: "T-0009", Goal: "G-002", Role: "pm", Status: "in_progress"}, true)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm", Role: "pm", Goal: "G-001", Task: "T-0003"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm2", Role: "pm", Goal: "G-002", Task: "T-0009"})
	for _, a := range [][2]string{{"lead", "lead"}, {"director", "director"}, {"boss", "boss"}, {"coder", "coder"},
		{"qa", "qa"}, {"reviewer", "reviewer"}, {"judge", "judge"}, {"cleaner", "cleaner"}} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + a[0], Role: a[1], Goal: "G-001"})
	}
	return e
}

// gcSigned is audit v1 (ready, recorded by the pm) + director sign-off; T-0003 is the audit task.
func gcSigned(e *ft.Env, auditTaskDone bool) {
	e.T.Helper()
	if auditTaskDone {
		exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	}
	okS(e, "zz-test-pm", "ready", "goal", "audit", "G-001", "--ready", "--report", "-")
	ok(e, "zz-test-director", "goal", "signoff", "G-001", "--audit", "1", "--reason", "ok")
}

func gcClose(e *ft.Env, agent string) ft.Result {
	return run(e, agent, "goal", "close", "G-001", "--reason", "audit v1 ready")
}

func gcStatus(e *ft.Env, gid string) string {
	return val(e, "SELECT status FROM goals WHERE id=?", gid)
}

func gcEvents(e *ft.Env, action string) [][]string {
	return sql(e, "SELECT actor, detail FROM events WHERE entity='goal' AND entity_id='G-001' AND action=? ORDER BY id", action)
}

func gcRefusedClose(t *testing.T, e *ft.Env, needles ...string) ft.Result {
	t.Helper()
	p := gcClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 4)
	for _, n := range needles {
		contains(t, "stderr", p.Stderr, n)
	}
	eq(t, "status", gcStatus(e, "G-001"), "active")
	eq(t, "close-refused events", len(gcEvents(e, "close-refused")), 1)
	return p
}

func TestGoalClosePMClosesOwnGoalAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	p := ok(e, "zz-test-pm", "goal", "close", "G-001", "--reason", "done")
	contains(t, "stdout", p.Stdout, "G-001 → done")
	eq(t, "status", gcStatus(e, "G-001"), "done")
	ev := gcEvents(e, "goal close")
	eq(t, "close events", len(ev), 1)
	so := val(e, "SELECT id FROM signoffs")
	contains(t, "close event", ev[0][1], fmt.Sprintf("signoff %s", so))
	contains(t, "close event", ev[0][1], "audit 1")
	eq(t, "status events", len(gcEvents(e, "status")), 1)
	var got []string
	for _, m := range sql(e, "SELECT recipient, kind FROM messages WHERE subject='G-001 closed' ORDER BY recipient") {
		got = append(got, m[0]+"/"+m[1])
	}
	eqS(t, "messages", got, []string{"zz-test-director/report", "zz-test-lead/report"})
}

func TestGoalCloseDirectorClosesGoalAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	ok(e, "zz-test-director", "goal", "close", "G-001", "--reason", "done")
	eq(t, "status", gcStatus(e, "G-001"), "done")
}

func TestGoalCloseDirectorClosesAfterAuditPMDonePostSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'") // the audit pm ran factory done
	ok(e, "zz-test-director", "goal", "close", "G-001", "--reason", "done")
	eq(t, "status", gcStatus(e, "G-001"), "done")
}

func TestGoalCloseNoteOnlyUpdateAfterSignoffDoesNotBlockClose(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET updated='2099-01-01T00:00:00+00:00'")
	ok(e, "zz-test-lead", "task", "update", "T-0001", "--note", "late note")
	ok(e, "zz-test-director", "goal", "close", "G-001", "--reason", "done")
	eq(t, "status", gcStatus(e, "G-001"), "done")
}

func TestGoalCloseWithoutSignoffRefused(t *testing.T) {
	e := gcEnv(t)
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--ready", "--report", "-")
	gcRefusedClose(t, e, "no director sign-off for audit v1 (factory goal signoff G-001 --audit 1)")
}

func TestGoalCloseWithoutAuditRefused(t *testing.T) {
	e := gcEnv(t)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	gcRefusedClose(t, e, "no audit for G-001: run factory goal audit")
}

func TestGoalCloseWithGapsAuditRefused(t *testing.T) {
	e := gcEnv(t)
	okS(e, "zz-test-pm", "gap", "goal", "audit", "G-001", "--gaps", "--report", "-")
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	gcRefusedClose(t, e, "latest audit v1 reports gaps")
}

func TestGoalCloseRefusedWithOpenTasks(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	seedTask(e, ft.Task{ID: "T-0004", Status: "todo"}, true)
	gcRefusedClose(t, e, "open tasks: T-0004(todo)")
}

func TestGoalCloseAllowsAuditTaskStillOpen(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	eq(t, "T-0003 status", val(e, "SELECT status FROM tasks WHERE id='T-0003'"), "in_progress")
	ok(e, "zz-test-pm", "goal", "close", "G-001", "--reason", "done")
}

func TestGoalCloseRefusedWhenTaskStatusChangedAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET status='cancelled' WHERE id='T-0001'") // closed states, but not the snapshot's
	gcRefusedClose(t, e, "T-0001 changed after sign-off of audit v1 (done -> cancelled)", "re-audit and get a new sign-off")
}

func TestGoalCloseRefusedWhenTaskReopenedAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
	ok(e, "zz-test-director", "task", "reopen", "T-0001", "--reason", "x")
	p := gcClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 4)
	contains(t, "stderr", p.Stderr, "open tasks: T-0001(todo)") // check 3 fires before check 6
	eq(t, "close-refused events", len(gcEvents(e, "close-refused")), 1)
}

func TestGoalCloseRefusedWhenNonAuditTaskStatusChangedToOtherDone(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0002'")
	gcRefusedClose(t, e, "T-0002 changed after sign-off of audit v1 (cancelled -> done)")
}

func TestGoalCloseAuditTaskMayOnlyMoveToDone(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET status='cancelled' WHERE id='T-0003'")
	gcRefusedClose(t, e, "T-0003 changed after sign-off of audit v1 (in_progress -> cancelled)")
}

func TestGoalCloseRefusedWhenTaskAddedAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	seedTask(e, ft.Task{ID: "T-0004", Status: "done"}, true) // a follow-up filed and finished
	gcRefusedClose(t, e, "T-0004 is new", "re-audit and get a new sign-off")
}

func TestGoalCloseRefusedWhenNewAuditAfterSignoff(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	okS(e, "zz-test-director", "again", "goal", "audit", "G-001", "--ready", "--report", "-")
	gcRefusedClose(t, e, "no director sign-off for audit v2")
}

func TestGoalCloseRefusedForWrongRole(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	for _, who := range []string{"coder", "qa", "reviewer", "judge", "lead", "boss"} {
		p := gcClose(e, "zz-test-"+who)
		if p.Exit != 3 {
			t.Fatalf("%s: exit %d, want 3: %s", who, p.Exit, p.Stderr)
		}
		contains(t, "stderr", p.Stderr, "role '"+who+"' may not close goals; allowed: pm (own goal), director")
	}
	eq(t, "status", gcStatus(e, "G-001"), "active")
}

func TestGoalClosePMCannotCloseOtherGoal(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	eq(t, "exit", gcClose(e, "zz-test-pm2").Exit, 3)
	eq(t, "status", gcStatus(e, "G-001"), "active")
}

func TestGoalCloseRefusedWhenAlreadyDone(t *testing.T) {
	e := gcEnv(t)
	gcSigned(e, false)
	ok(e, "zz-test-director", "goal", "close", "G-001", "--reason", "done")
	p := gcClose(e, "zz-test-director")
	eq(t, "exit", p.Exit, 4)
	contains(t, "stderr", p.Stderr, "goal G-001 is already done")
}

func TestGoalCloseLeadGoalStatusDoneRefused(t *testing.T) {
	e := gcEnv(t)
	p := refused(e, 3, "zz-test-lead", "goal", "status", "G-001", "done")
	contains(t, "stderr", p.Stderr, "use factory goal close G-001")
	eq(t, "status", gcStatus(e, "G-001"), "active")
}

func TestGoalCloseDirectorGoalStatusDoneRefusedPointsToClose(t *testing.T) {
	e := gcEnv(t)
	for _, who := range []string{"director", "boss"} {
		p := refused(e, 3, "zz-test-"+who, "goal", "status", "G-001", "done")
		contains(t, "stderr", p.Stderr, "closing a goal needs a director sign-off (factory goal signoff)")
	}
	eq(t, "status", gcStatus(e, "G-001"), "active")
}

func TestGoalCloseOwnerGoalStatusDoneOverrideLogged(t *testing.T) {
	e := gcEnv(t)
	ok(e, "", "goal", "status", "G-001", "done", "--note", "owner says so")
	eq(t, "status", gcStatus(e, "G-001"), "done")
	eq(t, "override events", len(gcEvents(e, "owner-override-close")), 1)
}

func TestGoalCloseDirectorReopensDoneGoalWithNote(t *testing.T) {
	e := gcEnv(t)
	exec(e, "UPDATE goals SET status='done'")
	ok(e, "zz-test-director", "goal", "status", "G-001", "active", "--note", "reopen")
	eq(t, "status", gcStatus(e, "G-001"), "active")
	exec(e, "UPDATE goals SET status='done'")
	ok(e, "", "goal", "status", "G-001", "active", "--note", "reopen") // owner
	eq(t, "status", gcStatus(e, "G-001"), "active")
}

func TestGoalCloseReopenDoneGoalRefusedForLeadBossPM(t *testing.T) {
	e := gcEnv(t)
	e.NeedPython() // the worker belt of goal status is Python
	exec(e, "UPDATE goals SET status='done'")
	for _, who := range []string{"lead", "boss"} {
		p := refused(e, 3, "zz-test-"+who, "goal", "status", "G-001", "active", "--note", "n")
		contains(t, "stderr", p.Stderr, "only the director reopens a done goal")
	}
	p := refused(e, 3, "zz-test-pm", "goal", "status", "G-001", "active", "--note", "n") // worker belt
	contains(t, "stderr", p.Stderr, "workers may not run `factory goal status`")
	eq(t, "status", gcStatus(e, "G-001"), "done")
}

func TestGoalCloseReopenDoneGoalRequiresNote(t *testing.T) {
	e := gcEnv(t)
	exec(e, "UPDATE goals SET status='done'")
	p := refused(e, 2, "zz-test-director", "goal", "status", "G-001", "active")
	contains(t, "stderr", p.Stderr, "--note is required to reopen a done goal")
	eq(t, "status", gcStatus(e, "G-001"), "done")
}

func TestGoalCloseRefusedEventRecorded(t *testing.T) {
	e := gcEnv(t)
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	gcRefusedClose(t, e, "no audit for G-001")
	ev := gcEvents(e, "close-refused")[0]
	eq(t, "actor", ev[0], "zz-test-director")
	contains(t, "detail", ev[1], "no audit for G-001")
}
