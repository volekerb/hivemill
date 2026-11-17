package s2tasks

import (
	"encoding/json"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_update.py (18 of 18 cases). Complete.

func tuEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	for _, id := range []string{"T-0001", "T-0002", "T-0003"} {
		seedTask(e, ft.Task{ID: id, Status: "todo"}, true)
	}
	seedTask(e, ft.Task{ID: "T-0009", Goal: "G-002", Status: "todo"}, true)
	return e
}

func tuDeps(t *testing.T, e *ft.Env, tid string) []string {
	t.Helper()
	deps := []string{}
	if err := json.Unmarshal([]byte(val(e, "SELECT depends_on FROM tasks WHERE id=?", tid)), &deps); err != nil {
		t.Fatalf("depends_on of %s is not JSON: %v", tid, err)
	}
	return deps
}

func tuTitle(e *ft.Env, tid string) string { return val(e, "SELECT title FROM tasks WHERE id=?", tid) }

func tuUpdateEvents(e *ft.Env, tid string) []string { return events(e, "task", tid, "update") }

func TestUpdateCmdAfterSetsDeps(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001,T-0002")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0001", "T-0002"})
	ok(e, "", "task", "update", "T-0003", "--after", "T-0002") // replaces, not appends
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0002"})
	contains(t, "task show", ok(e, "", "task", "show", "T-0003").Stdout, `depends_on: ["T-0002"]`)
	found := false
	for _, d := range tuUpdateEvents(e, "T-0003") {
		found = found || strings.Contains(d, `depends_on=["T-0002"]`)
	}
	if !found {
		t.Fatalf("no update event with depends_on=[\"T-0002\"]: %q", tuUpdateEvents(e, "T-0003"))
	}
}

func TestUpdateCmdAfterUnknownIDRejected(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001")
	for _, bad := range []string{"T-ZZZ", "T-0001,T-ZZZ", "T-0003", "T-0009"} { // unknown, partly unknown, self, other goal
		p := refused(e, 2, "", "task", "update", "T-0003", "--after", bad, "--title", "changed")
		contains(t, "stderr", p.Stderr, "factory:")
		eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0001"})
	}
	eq(t, "title", tuTitle(e, "T-0003"), "zz-test task")
}

func TestUpdateCmdAfterEmptyClears(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001,T-0002")
	ok(e, "", "task", "update", "T-0003", "--after", "")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{})
}

func TestUpdateCmdWithoutAfterKeepsDeps(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001")
	ok(e, "", "task", "update", "T-0003", "--title", "new")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0001"})
}

func TestUpdateCmdAfterRejectsDirectCycle(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0001", "--after", "T-0002")
	p := refused(e, 2, "", "task", "update", "T-0002", "--after", "T-0001", "--title", "changed")
	contains(t, "stderr", p.Stderr, "dependency cycle: T-0002 -> T-0001 -> T-0002")
	eqS(t, "deps", tuDeps(t, e, "T-0002"), []string{})
	eq(t, "title", tuTitle(e, "T-0002"), "zz-test task")
}

func TestUpdateCmdAfterRejectsIndirectCycle(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0001", "--after", "T-0002")
	ok(e, "", "task", "update", "T-0002", "--after", "T-0003")
	p := refused(e, 2, "", "task", "update", "T-0003", "--after", "T-0001")
	contains(t, "stderr", p.Stderr, "dependency cycle: T-0003 -> T-0001 -> T-0002 -> T-0003")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{})
}

func TestUpdateCmdAfterAllowsDiamond(t *testing.T) {
	e := tuEnv(t)
	seedTask(e, ft.Task{ID: "T-0004", Status: "todo"}, true)
	// A=T-0001 -> B=T-0002, C=T-0003 -> D=T-0004
	ok(e, "", "task", "update", "T-0001", "--after", "T-0002,T-0003")
	ok(e, "", "task", "update", "T-0002", "--after", "T-0004")
	ok(e, "", "task", "update", "T-0003", "--after", "T-0004")
	// re-set D's deps and A's deps: D is reached via both B and C, no false positive
	ok(e, "", "task", "update", "T-0004", "--after", "")
	ok(e, "", "task", "update", "T-0001", "--after", "T-0002,T-0003,T-0004")
	eqS(t, "deps T-0001", tuDeps(t, e, "T-0001"), []string{"T-0002", "T-0003", "T-0004"})
	eqS(t, "deps T-0003", tuDeps(t, e, "T-0003"), []string{"T-0004"})
}

func TestUpdateCmdNoteOnlyKeepsDepsAndCleanDetail(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001")
	ok(e, "", "task", "update", "T-0003", "--note", "hi")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0001"})
	ev := tuUpdateEvents(e, "T-0003")
	eq(t, "last detail", ev[len(ev)-1], "hi")
}

func tuCount(e *ft.Env) string { return val(e, "SELECT COUNT(*) FROM tasks") }

func TestUpdateCmdTaskAddAfterUnknownIDRejected(t *testing.T) {
	e := tuEnv(t)
	n := tuCount(e)
	for _, bad := range []string{"T-ZZZ", "T-0001,T-ZZZ", "T-0009"} { // unknown, partly unknown, other goal
		p := refused(e, 2, "", "task", "add", "--goal", "G-001", "--title", "x", "--after", bad)
		contains(t, "stderr", p.Stderr, "factory:")
	}
	eq(t, "task count", tuCount(e), n)
}

func TestUpdateCmdTaskAddForwardRefCycleImpossible(t *testing.T) {
	e := tuEnv(t)
	// reviewer repro: add --after T-0005 -> T-0004; add --after T-0004 -> T-0005
	n := tuCount(e)
	p := refused(e, 2, "", "task", "add", "--goal", "G-001", "--title", "first", "--after", "T-0005")
	contains(t, "stderr", p.Stderr, "no task T-0005")
	eq(t, "task count", tuCount(e), n)
	ok(e, "", "task", "add", "--goal", "G-001", "--title", "ok", "--after", "T-0001,T-0002")
	id := val(e, "SELECT id FROM tasks ORDER BY id DESC LIMIT 1")
	eqS(t, "deps of the new task", tuDeps(t, e, id), []string{"T-0001", "T-0002"})
}

func TestUpdateCmdAfterDedupesIDsAndCleanEventDetail(t *testing.T) {
	e := tuEnv(t)
	ok(e, "", "task", "update", "T-0003", "--after", "T-0001, T-0002,T-0001")
	eqS(t, "deps", tuDeps(t, e, "T-0003"), []string{"T-0001", "T-0002"})
	ev := tuUpdateEvents(e, "T-0003")
	eq(t, "last detail", ev[len(ev)-1], `depends_on=["T-0001", "T-0002"]`)
}

// ---- CoderDoneGuardTests ----------------------------------------------------------------------------------------------

const cdURL = "https://github.com/o/r/pull/7"

func cdEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	seedTask(e, ft.Task{ID: "T-0001", Status: "in_progress"}, true)
	e.SeedTask(ft.Task{ID: "T-0002", Status: "in_review", PRURL: cdURL})
	for _, a := range [][2]string{{"lead-g-001-a", "lead"}, {"coder-t-0001", "coder"}, {"judge-1", "judge"}} {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001"})
	}
	return e
}

func cdRow(e *ft.Env, tid string) (status, pr string) {
	r := sql(e, "SELECT status, COALESCE(pr_url,'NULL') FROM tasks WHERE id=?", tid)[0]
	return r[0], r[1]
}

func TestUpdateCmdLeadClosesCoderTaskWithoutPR(t *testing.T) {
	e := cdEnv(t)
	ok(e, "lead-g-001-a", "task", "update", "T-0001", "--status", "done")
	st, _ := cdRow(e, "T-0001")
	eq(t, "status", st, "done")
}

func TestUpdateCmdLeadCannotCloseCoderTaskWithPR(t *testing.T) {
	e := cdEnv(t)
	p := refused(e, 3, "lead-g-001-a", "task", "update", "T-0002", "--status", "done")
	contains(t, "stderr", p.Stderr, "only done when the judge merges")
	st, _ := cdRow(e, "T-0002")
	eq(t, "status", st, "in_review")
}

func TestUpdateCmdLeadCannotSetDoneAndPRInOneCall(t *testing.T) {
	e := cdEnv(t)
	refused(e, 3, "lead-g-001-a", "task", "update", "T-0001", "--status", "done", "--pr", cdURL)
	st, pr := cdRow(e, "T-0001")
	eq(t, "status", st, "in_progress")
	eq(t, "pr_url", pr, "NULL")
}

func TestUpdateCmdDirectorClosesCoderTaskWithoutPR(t *testing.T) {
	e := cdEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "director-1", Role: "director"})
	ok(e, "director-1", "task", "update", "T-0001", "--status", "done")
	st, _ := cdRow(e, "T-0001")
	eq(t, "status", st, "done")
}

func TestUpdateCmdCoderCannotCloseOwnTaskWithoutPR(t *testing.T) {
	e := cdEnv(t)
	p := refused(e, 3, "coder-t-0001", "task", "update", "T-0001", "--status", "done")
	contains(t, "stderr", p.Stderr, "no PR")
	contains(t, "stderr", p.Stderr, "factory judge")
	st, _ := cdRow(e, "T-0001")
	eq(t, "status", st, "in_progress")
}

func TestUpdateCmdClearingPROnlyOwnerOrJudge(t *testing.T) {
	e := cdEnv(t)
	refused(e, 3, "lead-g-001-a", "task", "update", "T-0002", "--pr", "")
	refused(e, 3, "lead-g-001-a", "task", "update", "T-0002", "--status", "done", "--pr", "")
	_, pr := cdRow(e, "T-0002")
	eq(t, "pr_url", pr, cdURL)
	ok(e, "judge-1", "task", "update", "T-0002", "--pr", "")
	if _, pr = cdRow(e, "T-0002"); pr != "NULL" && pr != "" {
		t.Fatalf("pr_url = %q after the judge cleared it", pr)
	}
	exec(e, "UPDATE tasks SET pr_url=? WHERE id='T-0002'", cdURL)
	ok(e, "", "task", "update", "T-0002", "--pr", "") // owner
	if _, pr = cdRow(e, "T-0002"); pr != "NULL" && pr != "" {
		t.Fatalf("pr_url = %q after the owner cleared it", pr)
	}
}

func TestUpdateCmdOwnerSetsDoneWithPR(t *testing.T) {
	e := cdEnv(t)
	// judge never reaches this guard: it is refused any task status earlier (merges go through `factory judge`)
	ok(e, "", "task", "update", "T-0002", "--status", "done")
	st, _ := cdRow(e, "T-0002")
	eq(t, "status", st, "done")
	p := refused(e, 3, "judge-1", "task", "update", "T-0001", "--status", "done")
	contains(t, "stderr", p.Stderr, "factory judge")
}
