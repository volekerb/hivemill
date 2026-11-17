package s2tasks

import (
	"fmt"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_compact_show.py (6 of 6 cases). Complete.

func csPlan() string {
	var l []string
	for i := 0; i < 7; i++ {
		l = append(l, fmt.Sprintf("plan-body-line-%d", i))
	}
	return strings.Join(l, "\n")
}

func csEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	exec(e, "UPDATE goals SET intent='zz-intent full', success_criteria='zz-sc full' WHERE id='G-001'")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	for _, a := range [][2]string{{"zz-test-coder", "coder"}, {"zz-test-qa", "qa"}, {"zz-test-lead", "lead"}, {"zz-test-director", "director"}} {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001", Team: "g-001-a"})
	}
	e.SeedTask(ft.Task{ID: "T-0001", Status: "in_progress", Spec: "zz-spec full", Acceptance: "1. zz-acc full"})
	e.SeedTask(ft.Task{ID: "T-0002", Status: "todo"})
	exec(e, "UPDATE tasks SET title='zz-other-task-title' WHERE id='T-0002'")
	for i := 0; i < 2; i++ {
		exec(e, "INSERT INTO verdicts(task_id,role,verdict,summary,author,created) VALUES('T-0001','qa','pass',?,'q',?)",
			fmt.Sprintf("verdict-%d", i), fmt.Sprintf("2026-01-01T00:0%d:00", i))
	}
	type ev struct{ act, det string }
	var evs []ev
	for i := 0; i < 2; i++ {
		evs = append(evs, ev{"note", fmt.Sprintf("note-%d", i)})
	}
	for i := 0; i < 12; i++ {
		evs = append(evs, ev{"update", fmt.Sprintf("other-%d", i)})
	}
	for i, x := range evs {
		exec(e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
			fmt.Sprintf("2026-01-02T00:%02d:00", i), "x", "task", "T-0001", x.act, x.det)
	}
	exec(e, "INSERT INTO plans(goal_id,version,body,author,created) VALUES('G-001',3,?,'pm-x','2026-01-03')", csPlan())
	exec(e, "INSERT INTO audits(goal_id,version,verdict,body,author,created) VALUES('G-001',2,'ready','audit-body','pm-x','2026-01-04')")
	return e
}

// csOthers lists which of other-0..other-11 occur as a whole line of out.
func csOthers(out string) []int {
	var got []int
	for i := 0; i < 12; i++ {
		if strings.Contains(out+"\n", fmt.Sprintf("other-%d\n", i)) {
			got = append(got, i)
		}
	}
	return got
}

func intsEq(t *testing.T, what string, got, want []int) {
	t.Helper()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func TestCompactShowTaskShowCompactForWorker(t *testing.T) {
	e := csEnv(t)
	out := ok(e, "zz-test-coder", "task", "show", "T-0001").Stdout
	for _, w := range []string{"zz-spec full", "1. zz-acc full", "verdict-0", "verdict-1", "note-0", "note-1"} {
		contains(t, "stdout", out, w)
	}
	intsEq(t, "events shown", csOthers(out), []int{7, 8, 9, 10, 11})
	contains(t, "stdout", out, "(7 earlier events: factory task show T-0001 --full)")
}

func TestCompactShowTaskShowFullAndHQIdentical(t *testing.T) {
	e := csEnv(t)
	full := ok(e, "zz-test-coder", "task", "show", "T-0001", "--full").Stdout
	eq(t, "lead compact", ok(e, "zz-test-lead", "task", "show", "T-0001").Stdout, full)
	eq(t, "lead full", ok(e, "zz-test-lead", "task", "show", "T-0001", "--full").Stdout, full)
	intsEq(t, "events shown", csOthers(full), []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11})
	notContains(t, "full", full, "earlier events")
}

func TestCompactShowTaskShowFewEventsNoPointer(t *testing.T) {
	e := csEnv(t)
	exec(e, "DELETE FROM events WHERE action='update' AND detail IN ('other-0','other-1')")
	contains(t, "stdout", ok(e, "zz-test-coder", "task", "show", "T-0001").Stdout, "(5 earlier events")
	exec(e, "DELETE FROM events WHERE action='update' AND CAST(substr(detail,7) AS INT) < 7")
	notContains(t, "stdout", ok(e, "zz-test-coder", "task", "show", "T-0001").Stdout, "earlier events")
}

func TestCompactShowGoalShowCompactForWorker(t *testing.T) {
	e := csEnv(t)
	out := ok(e, "zz-test-qa", "goal", "show", "G-001").Stdout
	for _, w := range []string{"zz-intent full", "zz-sc full",
		"plan v3 by pm-x 2026-01-03, 7 lines: factory goal show G-001 --full", "audit v2 ready",
		"tasks: 2 (todo 1, in_progress 1, in_review 0, done 0, cancelled 0)"} {
		contains(t, "stdout", out, w)
	}
	for _, absent := range []string{"plan-body-line-0", "audit-body", "## teams", "lead_agent", "zz-other-task-title"} {
		notContains(t, "stdout", out, absent)
	}
}

func TestCompactShowGoalShowCompactCountsOtherStatuses(t *testing.T) {
	e := csEnv(t)
	exec(e, "UPDATE tasks SET status='blocked' WHERE id='T-0002'")
	out := ok(e, "zz-test-qa", "goal", "show", "G-001").Stdout
	contains(t, "stdout", out, "tasks: 2 (todo 0, in_progress 1, in_review 0, done 0, cancelled 0, blocked 1)")
}

func TestCompactShowGoalShowFullAndHQIdentical(t *testing.T) {
	e := csEnv(t)
	full := ok(e, "zz-test-qa", "goal", "show", "G-001", "--full").Stdout
	eq(t, "director compact", ok(e, "zz-test-director", "goal", "show", "G-001").Stdout, full)
	for _, w := range []string{"plan-body-line-6", "audit-body", "## teams", "zz-other-task-title"} {
		contains(t, "full", full, w)
	}
}
