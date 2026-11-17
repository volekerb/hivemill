package s2tasks

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_team_update.py (9 of 9 cases). Complete.

func ttuEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "")
	e.SeedTeam("g-001-b", "G-001", "")
	e.SeedTeam("g-002-a", "G-002", "")
	seedTask(e, ft.Task{ID: "T-0001", Status: "todo", Team: "g-001-a"}, true)
	e.SeedAgent(ft.AgentRow{Name: "lead-g-001-a", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "coder-t-0001", Role: "coder", Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	return e
}

func ttuTeamOf(e *ft.Env, tid string) string {
	return val(e, "SELECT COALESCE(team_id,'NULL') FROM tasks WHERE id=?", tid)
}

func ttuSeedGoalBTask(e *ft.Env) {
	seedTask(e, ft.Task{ID: "T-0002", Goal: "G-002", Status: "todo", Team: "g-002-a"}, true)
	e.SeedTeam("g-002-b", "G-002", "")
}

func TestUpdTeamValidTeamChangesTeamIDAndLogsHistory(t *testing.T) {
	e := ttuEnv(t)
	for _, agent := range []string{"lead-g-001-a", "director"} {
		exec(e, "UPDATE tasks SET team_id='g-001-a' WHERE id='T-0001'")
		p := ok(e, agent, "task", "update", "T-0001", "--team", "g-001-b")
		contains(t, "stdout", p.Stdout, "T-0001 updated")
		eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-b")
		if h := events(e, "task", "T-0001", "update"); !strings.Contains(strings.Join(h, "\n"), "team=g-001-b") {
			t.Fatalf("history %q lacks team=g-001-b", h)
		}
	}
}

func TestUpdTeamUnknownTeamRefused(t *testing.T) {
	e := ttuEnv(t)
	p := refused(e, 2, "lead-g-001-a", "task", "update", "T-0001", "--team", "g-001-zz")
	contains(t, "stderr", p.Stderr, "g-001-zz")
	notContains(t, "stdout", p.Stdout, "updated")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-a")
}

func TestUpdTeamTeamOfAnotherGoalRefused(t *testing.T) {
	e := ttuEnv(t)
	p := refused(e, 2, "director", "task", "update", "T-0001", "--team", "g-002-a")
	contains(t, "stderr", p.Stderr, "g-002-a")
	notContains(t, "stdout", p.Stdout, "updated")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-a")
}

func TestUpdTeamWorkerRefused(t *testing.T) {
	e := ttuEnv(t)
	p := refused(e, 3, "coder-t-0001", "task", "update", "T-0001", "--team", "g-001-b")
	contains(t, "stderr", p.Stderr, "--team")
	notContains(t, "stdout", p.Stdout, "updated")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-a")
}

func TestUpdTeamUpdateWithoutTeamKeepsTeam(t *testing.T) {
	e := ttuEnv(t)
	ok(e, "lead-g-001-a", "task", "update", "T-0001", "--title", "new")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-a")
}

func TestUpdTeamLeadOfAnotherGoalRefused(t *testing.T) {
	e := ttuEnv(t)
	ttuSeedGoalBTask(e)
	p := refused(e, 3, "lead-g-001-a", "task", "update", "T-0002", "--team", "g-002-b")
	contains(t, "stderr", p.Stderr, "--team on T-0002")
	notContains(t, "stdout", p.Stdout, "updated")
	eq(t, "team", ttuTeamOf(e, "T-0002"), "g-002-a")
}

func TestUpdTeamLeadWithoutGoalRefused(t *testing.T) {
	e := ttuEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "lead-nogoal", Role: "lead"})
	p := refused(e, 3, "lead-nogoal", "task", "update", "T-0001", "--team", "g-001-b")
	contains(t, "stderr", p.Stderr, "--team on T-0001")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-a")
}

func TestUpdTeamLeadOwnGoalAllowed(t *testing.T) {
	e := ttuEnv(t)
	ok(e, "lead-g-001-a", "task", "update", "T-0001", "--team", "g-001-b")
	eq(t, "team", ttuTeamOf(e, "T-0001"), "g-001-b")
}

func TestUpdTeamDirectorAndBossAnyGoalAllowed(t *testing.T) {
	e := ttuEnv(t)
	ttuSeedGoalBTask(e)
	e.SeedAgent(ft.AgentRow{Name: "boss", Role: "boss"})
	for _, c := range [][3]string{{"director", "T-0002", "g-002-b"}, {"boss", "T-0001", "g-001-b"}} {
		ok(e, c[0], "task", "update", c[1], "--team", c[2])
		eq(t, "team of "+c[1], ttuTeamOf(e, c[1]), c[2])
	}
}
