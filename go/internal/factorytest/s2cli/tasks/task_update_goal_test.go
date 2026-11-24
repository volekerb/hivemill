package s2tasks

import (
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_update_goal.py (6 of 6 cases). Complete.
// T-0552: `factory task update --goal` really moves the task (director/owner) or refuses; never a silent no-op.

func tugEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "")
	seedTask(e, ft.Task{ID: "T-0001", Goal: "G-001", Status: "todo", Team: "g-001-a"}, true)
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	return e
}

func tugGoalOf(e *ft.Env) (goal, team string) {
	r := sql(e, "SELECT goal_id, COALESCE(team_id,'NULL') FROM tasks WHERE id='T-0001'")[0]
	return r[0], r[1]
}

func TestUpdGoalADirectorMovesTaskAndEventRecordsOldNew(t *testing.T) {
	e := tugEnv(t)
	p := ok(e, "zz-director", "task", "update", "T-0001", "--goal", "G-002")
	contains(t, "stdout", p.Stdout, "T-0001 updated")
	g, tm := tugGoalOf(e)
	eq(t, "goal", g, "G-002")
	eq(t, "team", tm, "NULL")
	ev := events(e, "task", "T-0001", "update")
	contains(t, "last update event", ev[len(ev)-1], "goal=G-001 -> G-002")
}

func TestUpdGoalBLeadRefusedAndUnchanged(t *testing.T) {
	e := tugEnv(t)
	p := refused(e, 3, "zz-lead", "task", "update", "T-0001", "--goal", "G-002")
	notContains(t, "stdout", p.Stdout, "updated")
	g, tm := tugGoalOf(e)
	eq(t, "goal", g, "G-001")
	eq(t, "team", tm, "g-001-a")
}

func TestUpdGoalCUnknownGoalSameGoalAndActiveTaskRefused(t *testing.T) {
	e := tugEnv(t)
	refused(e, 2, "zz-director", "task", "update", "T-0001", "--goal", "G-999")
	refused(e, 2, "zz-director", "task", "update", "T-0001", "--goal", "G-001")
	exec(e, "UPDATE tasks SET status='in_progress' WHERE id='T-0001'")
	refused(e, 2, "zz-director", "task", "update", "T-0001", "--goal", "G-002")
	g, _ := tugGoalOf(e)
	eq(t, "goal", g, "G-001")
}

func TestUpdGoalDLiveAgentRefused(t *testing.T) {
	e := tugEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-coder", Role: "coder", Goal: "G-001", Task: "T-0001"})
	p := refused(e, 2, "zz-director", "task", "update", "T-0001", "--goal", "G-002")
	contains(t, "stderr", p.Stderr, "zz-coder")
	g, _ := tugGoalOf(e)
	eq(t, "goal", g, "G-001")
}

func TestUpdGoalEDependencyLinksRefused(t *testing.T) {
	e := tugEnv(t)
	seedTask(e, ft.Task{ID: "T-0002", Goal: "G-001", Status: "todo"}, true)
	exec(e, `UPDATE tasks SET depends_on='["T-0001"]' WHERE id='T-0002'`)
	refused(e, 2, "zz-director", "task", "update", "T-0001", "--goal", "G-002")
	refused(e, 2, "zz-director", "task", "update", "T-0002", "--goal", "G-002")
}

func TestUpdGoalFEmptyUpdateIsAnErrorNotUpdated(t *testing.T) {
	e := tugEnv(t)
	p := refused(e, 2, "zz-director", "task", "update", "T-0001")
	notContains(t, "stdout", p.Stdout, "updated")
	eq(t, "update events", val(e, "SELECT COUNT(*) FROM events WHERE entity_id='T-0001' AND action='update'"), "0")
}
