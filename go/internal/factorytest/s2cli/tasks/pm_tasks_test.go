package s2tasks

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_pm_tasks.py (24 of 25 cases; PARTIAL for P7a, T-1495).
// not ported: PmTaskTests.test_migrate_adds_follow_up_of_to_old_db - it asserts the Python db() migrate() adds the
// tasks.follow_up_of column to an old database; the Go CLI opens the database read-only and leaves migration to Python
// (go/internal/cli/cmd_res.go:1008), so there is no Go counterpart to drive.

func pmEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	seedTask(e, ft.Task{ID: "T-0001", Role: "pm", Status: "done"}, true)
	e.SeedTask(ft.Task{ID: "T-0002", Status: "done"}) // merged PR task
	seedTask(e, ft.Task{ID: "T-0003", Role: "pm", Status: "todo"}, true)
	e.SeedTask(ft.Task{ID: "T-0004", Status: "in_review"}) // open PR task
	seedTask(e, ft.Task{ID: "T-0009", Goal: "G-002", Role: "pm", Status: "done"}, true)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm", Role: "pm", Goal: "G-001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm2", Role: "pm", Goal: "G-002"})
	for _, a := range [][2]string{{"lead", "lead"}, {"director", "director"}, {"coder", "coder"}, {"qa", "qa"},
		{"reviewer", "reviewer"}, {"judge", "judge"}, {"cleaner", "cleaner"}} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + a[0], Role: a[1], Goal: "G-001"})
	}
	return e
}

func pmStatus(e *ft.Env, tid string) string {
	return val(e, "SELECT status FROM tasks WHERE id=?", tid)
}

// ---- reopen -----------------------------------------------------------------------------------------------------------

func TestPmTasksPMReopensDoneNonPRTaskInOwnGoal(t *testing.T) {
	e := pmEnv(t)
	ok(e, "zz-test-pm", "task", "reopen", "T-0001", "--reason", "acceptance 2 unmet")
	r := sql(e, "SELECT status, attempts FROM tasks WHERE id='T-0001'")[0]
	eqS(t, "status,attempts", r, []string{"todo", "0"})
	eqS(t, "reopen events", events(e, "task", "T-0001", "reopen"), []string{"acceptance 2 unmet"})
}

func TestPmTasksLeadAndDirectorCanReopen(t *testing.T) {
	e := pmEnv(t)
	for _, c := range [][2]string{{"zz-test-lead", "T-0001"}, {"zz-test-director", "T-0009"}} {
		ok(e, c[0], "task", "reopen", c[1], "--reason", "r")
		eq(t, "status of "+c[1], pmStatus(e, c[1]), "todo")
	}
}

func TestPmTasksReopenRequiresReason(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 2, "zz-test-pm", "task", "reopen", "T-0001")
	contains(t, "stderr", p.Stderr, "--reason is required")
	refused(e, 2, "zz-test-pm", "task", "reopen", "T-0001", "--reason", " ")
	eq(t, "status", pmStatus(e, "T-0001"), "done")
}

func TestPmTasksReopenRefusesNonDoneTask(t *testing.T) {
	e := pmEnv(t)
	for _, st := range []string{"todo", "in_progress", "in_review", "cancelled"} {
		exec(e, "UPDATE tasks SET status=? WHERE id='T-0003'", st)
		p := refused(e, 2, "zz-test-pm", "task", "reopen", "T-0003", "--reason", "r")
		contains(t, "stderr", p.Stderr, "T-0003 is "+st+", not done: only done tasks are reopened")
	}
}

func TestPmTasksReopenRefusesTaskWithPRPointsToFollowUp(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 2, "zz-test-lead", "task", "reopen", "T-0002", "--reason", "r")
	contains(t, "stderr", p.Stderr, "merged work is not reopened: file a follow-up with factory task add --follow-up-of T-0002")
	eq(t, "status", pmStatus(e, "T-0002"), "done")
}

func TestPmTasksReopenRefusedWhenGoalDone(t *testing.T) {
	e := pmEnv(t)
	exec(e, "UPDATE goals SET status='done' WHERE id='G-001'")
	p := refused(e, 2, "zz-test-pm", "task", "reopen", "T-0001", "--reason", "r")
	contains(t, "stderr", p.Stderr, "goal G-001 is done; the director must reopen the goal first")
}

func TestPmTasksReopenRefusedForWrongRole(t *testing.T) {
	e := pmEnv(t)
	for _, who := range []string{"coder", "qa", "reviewer", "judge", "cleaner"} {
		p := refused(e, 3, "zz-test-"+who, "task", "reopen", "T-0001", "--reason", "r")
		contains(t, "stderr", p.Stderr, "role '"+who+"' may not reopen tasks; allowed: pm (own goal), lead, director, owner")
	}
	eq(t, "status", pmStatus(e, "T-0001"), "done")
}

func TestPmTasksPMReopenRefusedForOtherGoal(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 3, "zz-test-pm", "task", "reopen", "T-0009", "--reason", "r")
	contains(t, "stderr", p.Stderr, "zz-test-pm works on G-001; T-0009 is in G-002")
}

// ---- follow-up --------------------------------------------------------------------------------------------------------

func pmAddFollowUp(e *ft.Env, parent, agent string) ft.Result {
	return runO(e, ft.Opts{Agent: agent, Stdin: "spec"}, "task", "add", "--goal", "G-001", "--title", "fix",
		"--follow-up-of", parent, "--spec", "-")
}

func TestPmTasksFollowUpLinksParentAndShowsInTaskShow(t *testing.T) {
	e := pmEnv(t)
	p := pmAddFollowUp(e, "T-0002", "zz-test-pm")
	eq(t, "exit", p.Exit, 0)
	tid := strings.TrimSpace(p.Stdout)
	eq(t, "follow_up_of", val(e, "SELECT follow_up_of FROM tasks WHERE id=?", tid), "T-0002")
	found := false
	for _, d := range events(e, "task", tid, "follow-up") {
		found = found || d == "follow-up of T-0002"
	}
	if !found {
		t.Fatalf("no follow-up event 'follow-up of T-0002': %q", events(e, "task", tid, "follow-up"))
	}
	contains(t, "task show", ok(e, "", "task", "show", tid).Stdout, "follow_up_of: T-0002")
}

func TestPmTasksFollowUpParentMustExistInSameGoalAndBeDone(t *testing.T) {
	e := pmEnv(t)
	for _, c := range [][2]string{
		{"T-9999", "--follow-up-of: no task T-9999 in goal G-001"},
		{"T-0009", "--follow-up-of: no task T-0009 in goal G-001"},
		{"T-0003", "--follow-up-of: T-0003 is todo; follow-ups are filed against done tasks"}} {
		p := pmAddFollowUp(e, c[0], "zz-test-lead")
		eq(t, "exit for "+c[0], p.Exit, 2)
		contains(t, "stderr", p.Stderr, c[1])
	}
	eq(t, "task count", val(e, "SELECT count(*) FROM tasks"), "5")
}

func TestPmTasksFollowUpRefusedForPMOfOtherGoal(t *testing.T) {
	e := pmEnv(t)
	p := pmAddFollowUp(e, "T-0001", "zz-test-pm2")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "zz-test-pm2 works on G-002")
}

func TestPmTasksFollowUpRefusedForCoder(t *testing.T) {
	e := pmEnv(t)
	eq(t, "exit", pmAddFollowUp(e, "T-0001", "zz-test-coder").Exit, 3)
}

// ---- pm status narrowing ----------------------------------------------------------------------------------------------

func TestPmTasksPMMarksNonPRTaskDoneWithNote(t *testing.T) {
	e := pmEnv(t)
	for _, st := range []string{"todo", "in_progress", "blocked"} {
		exec(e, "UPDATE tasks SET status=? WHERE id='T-0003'", st)
		ok(e, "zz-test-pm", "task", "update", "T-0003", "--status", "done", "--note", "evidence")
		eq(t, "status after "+st, pmStatus(e, "T-0003"), "done")
	}
}

func TestPmTasksPMDoneRequiresNote(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 2, "zz-test-pm", "task", "update", "T-0003", "--status", "done")
	contains(t, "stderr", p.Stderr, "pm must give --note with the evidence when closing a task")
	eq(t, "status", pmStatus(e, "T-0003"), "todo")
}

func TestPmTasksPMCannotMarkPRTaskDone(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 3, "zz-test-pm", "task", "update", "T-0004", "--status", "done", "--note", "n")
	contains(t, "stderr", p.Stderr, "a coder task with a PR is only done when the judge merges it")
	eq(t, "status", pmStatus(e, "T-0004"), "in_review")
}

func TestPmTasksPMCannotSetOtherStatuses(t *testing.T) {
	e := pmEnv(t)
	for _, st := range []string{"in_progress", "in_review", "blocked", "cancelled", "todo"} {
		p := refused(e, 3, "zz-test-pm", "task", "update", "T-0003", "--status", st, "--note", "n")
		contains(t, "stderr", p.Stderr, "pm changes task status only with factory task reopen or --status done")
	}
	eq(t, "status", pmStatus(e, "T-0003"), "todo")
	eq(t, "attempts", val(e, "SELECT attempts FROM tasks WHERE id='T-0003'"), "0")
}

func TestPmTasksPMCannotUpdateStatusInOtherGoal(t *testing.T) {
	e := pmEnv(t)
	exec(e, "UPDATE tasks SET status='todo' WHERE id='T-0009'")
	p := refused(e, 3, "zz-test-pm", "task", "update", "T-0009", "--status", "done", "--note", "n")
	contains(t, "stderr", p.Stderr, "zz-test-pm works on G-001; T-0009 is in G-002")
}

func TestPmTasksLeadStatusUpdatesUnchanged(t *testing.T) {
	e := pmEnv(t)
	ok(e, "zz-test-lead", "task", "update", "T-0003", "--status", "in_progress")
	eq(t, "status", pmStatus(e, "T-0003"), "in_progress")
}

const pmMsg = "pm may not close a coder task without a PR: reopen it (factory task reopen T --reason ...) or file a " +
	"follow-up (factory task add --follow-up-of T ...); only a lead, director or the owner may close it"

func TestPmTasksPMCloseCoderTaskWithoutPRRefusedPointsToReopenOrFollowUp(t *testing.T) {
	e := pmEnv(t)
	seedTask(e, ft.Task{ID: "T-0010", Status: "todo"}, true)
	p := refused(e, 3, "zz-test-pm", "task", "update", "T-0010", "--status", "done", "--note", "evidence")
	contains(t, "stderr", p.Stderr, pmMsg)
	eq(t, "status", pmStatus(e, "T-0010"), "todo")
	eqS(t, "pm-closed events", events(e, "task", "T-0010", "pm-closed-coder-task-no-pr"), nil)
}

func TestPmTasksPMCloseCoderTaskWithoutPRRefusedEvenWithNote(t *testing.T) {
	e := pmEnv(t)
	seedTask(e, ft.Task{ID: "T-0010", Status: "todo"}, true)
	for _, extra := range [][]string{nil, {"--note", "n"}} {
		args := append([]string{"task", "update", "T-0010", "--status", "done"}, extra...)
		p := refused(e, 3, "zz-test-pm", args...)
		contains(t, "stderr", p.Stderr, pmMsg)
	}
	eq(t, "status", pmStatus(e, "T-0010"), "todo")
}

func TestPmTasksLeadDirectorOwnerCanStillCloseCoderTaskWithoutPR(t *testing.T) {
	e := pmEnv(t)
	for i, who := range []string{"zz-test-lead", "zz-test-director", ""} {
		tid := "T-001" + string(rune('0'+i))
		seedTask(e, ft.Task{ID: tid, Status: "todo"}, true)
		ok(e, who, "task", "update", tid, "--status", "done")
		eq(t, "status of "+tid, pmStatus(e, tid), "done")
	}
}

func TestPmTasksPMCloseCoderTaskInOtherGoalRefused(t *testing.T) {
	e := pmEnv(t)
	seedTask(e, ft.Task{ID: "T-0010", Goal: "G-002", Status: "todo"}, true)
	refused(e, 3, "zz-test-pm", "task", "update", "T-0010", "--status", "done", "--note", "n")
	eq(t, "status", pmStatus(e, "T-0010"), "todo")
}

func TestPmTasksPMCloseCoderTaskWithPRStillRefused(t *testing.T) {
	e := pmEnv(t)
	p := refused(e, 3, "zz-test-pm", "task", "update", "T-0004", "--status", "done", "--note", "n")
	contains(t, "stderr", p.Stderr, "a coder task with a PR is only done when the judge merges it")
	eqS(t, "pm-closed events", events(e, "task", "T-0004", "pm-closed-coder-task-no-pr"), nil)
}

func TestPmTasksPMFactoryDoneStillClosesOwnTask(t *testing.T) {
	e := pmEnv(t)
	exec(e, "UPDATE tasks SET status='in_progress' WHERE id='T-0003'")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm3", Role: "pm", Goal: "G-001", Task: "T-0003"})
	ok(e, "zz-test-pm3", "done", "finished")
	eq(t, "status", pmStatus(e, "T-0003"), "done")
}
