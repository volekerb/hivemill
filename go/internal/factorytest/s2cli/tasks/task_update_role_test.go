package s2tasks

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_update_role.py (12 of 12 cases). Complete.
// T-0941: `factory task update T --role R` applies the role with checks and a history event; `task add` keeps its
// coder default now that the parser default is gone.

const (
	turT    = "T-0001"
	turLead = "zz-lead"
)

func turEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", turLead)
	e.SeedAgent(ft.AgentRow{Name: turLead, Role: "lead", Goal: "G-001", Team: "g-001-a", Status: "exited"})
	seedTask(e, ft.Task{ID: turT, Role: "coder", Status: "todo", Team: "g-001-a"}, true)
	return e
}

// turRow is the whole tasks row as one string (the Python dict(row) comparison).
func turRow(e *ft.Env) string {
	return strings.Join(sql(e, "SELECT * FROM tasks WHERE id=?", turT)[0], "|")
}

func turUpd(e *ft.Env, agent string, args ...string) ft.Result {
	return run(e, agent, append([]string{"task", "update", turT}, args...)...)
}

func turShow(e *ft.Env) string { return ok(e, "", "task", "show", turT).Stdout }

func TestUpdRoleLeadChangesRoleAndHistoryRecordsIt(t *testing.T) {
	e := turEnv(t)
	p := turUpd(e, turLead, "--role", "researcher")
	eq(t, "exit", p.Exit, 0)
	eq(t, "role", val(e, "SELECT role FROM tasks WHERE id=?", turT), "researcher")
	contains(t, "task show", turShow(e), "role=coder->researcher")
}

func TestUpdRoleSameRoleAloneIsAllowed(t *testing.T) {
	e := turEnv(t)
	eq(t, "exit", turUpd(e, turLead, "--role", "coder").Exit, 0)
	contains(t, "task show", turShow(e), "role=coder->coder")
}

func TestUpdRoleInvalidRoleRefusedRowUnchanged(t *testing.T) {
	e := turEnv(t)
	before := turRow(e)
	p := turUpd(e, turLead, "--role", "bogus", "--title", "new title")
	if p.Exit == 0 {
		t.Fatalf("bogus role accepted: %s", p.Stdout)
	}
	contains(t, "stderr", p.Stderr, "role must be one of")
	eq(t, "row", turRow(e), before)
}

func TestUpdRoleLiveAndQueuedAgentRefuseStoppingAgentDoesNot(t *testing.T) {
	e := turEnv(t)
	for _, status := range []string{"live", "queued"} {
		name := "zz-coder-" + status
		e.SeedAgent(ft.AgentRow{Name: name, Role: "coder", Goal: "G-001", Task: turT, Status: status})
		before := turRow(e)
		p := turUpd(e, turLead, "--role", "qa", "--status", "blocked")
		if p.Exit == 0 {
			t.Fatalf("%s: role change accepted with a %s agent", status, status)
		}
		contains(t, "stderr", p.Stderr, name)
		contains(t, "stderr", p.Stderr, "factory stop")
		eq(t, "row", turRow(e), before)
		exec(e, "UPDATE agents SET stop_requested_at='2026-01-01T00:00:00+00:00' WHERE name=?", name)
		eq(t, status+" stopping exit", turUpd(e, turLead, "--role", "qa").Exit, 0)
		exec(e, "UPDATE tasks SET role='coder' WHERE id=?", turT)
	}
}

func TestUpdRoleWorkerActorRefusedRowUnchanged(t *testing.T) {
	e := turEnv(t)
	for _, role := range []string{"coder", "pm"} {
		e.SeedAgent(ft.AgentRow{Name: "zz-" + role + "-x", Role: role, Goal: "G-001", Status: "live"})
		before := turRow(e)
		if p := turUpd(e, "zz-"+role+"-x", "--role", "qa"); p.Exit == 0 {
			t.Fatalf("%s actor changed the role", role)
		}
		eq(t, "row", turRow(e), before)
	}
}

func TestUpdRoleSandboxRequiredTaskRefusesUnsandboxableRole(t *testing.T) {
	e := turEnv(t)
	exec(e, "UPDATE tasks SET sandbox_required=1 WHERE id=?", turT)
	before := turRow(e)
	p := turUpd(e, turLead, "--role", "pm")
	if p.Exit == 0 {
		t.Fatal("pm accepted for a sandbox-required task")
	}
	contains(t, "stderr", p.Stderr, "cannot be sandboxed")
	eq(t, "row", turRow(e), before)
}

func TestUpdRoleRoleAndSandboxFlagInOneCallUseTheEndState(t *testing.T) {
	e := turEnv(t)
	before := turRow(e)
	if p := turUpd(e, turLead, "--role", "pm", "--sandbox-required", "1"); p.Exit == 0 {
		t.Fatal("pm + --sandbox-required 1 accepted")
	}
	eq(t, "row", turRow(e), before)
	exec(e, "UPDATE tasks SET sandbox_required=1 WHERE id=?", turT)
	eq(t, "exit", turUpd(e, turLead, "--role", "pm", "--sandbox-required", "0").Exit, 0)
	r := sql(e, "SELECT role, sandbox_required FROM tasks WHERE id=?", turT)[0]
	eqS(t, "role,sandbox_required", r, []string{"pm", "0"})
}

func TestUpdRoleUpdateWithoutRoleKeepsNonCoderRole(t *testing.T) {
	e := turEnv(t)
	exec(e, "UPDATE tasks SET role='qa' WHERE id=?", turT)
	eq(t, "exit", turUpd(e, turLead, "--title", "x").Exit, 0)
	r := sql(e, "SELECT role, title FROM tasks WHERE id=?", turT)[0]
	eqS(t, "role,title", r, []string{"qa", "x"})
	parts := strings.Split(turShow(e), "## history")
	notContains(t, "history", parts[len(parts)-1], "role=")
}

func TestUpdRoleCoderTaskWithPRRoleChangeRefusedExceptOwner(t *testing.T) {
	e := turEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director", Goal: "G-001", Status: "exited"})
	exec(e, "UPDATE tasks SET pr_url='https://github.com/x/y/pull/1' WHERE id=?", turT)
	before := turRow(e)
	for _, agent := range []string{turLead, "zz-director"} {
		p := turUpd(e, agent, "--role", "qa")
		eq(t, "exit", p.Exit, 3)
		contains(t, "stderr", p.Stderr, "judge")
		eq(t, "row", turRow(e), before)
	}
	// --pr in the same call on a coder task without a PR
	exec(e, "UPDATE tasks SET pr_url=NULL WHERE id=?", turT)
	before = turRow(e)
	eq(t, "with --pr", turUpd(e, turLead, "--role", "qa", "--pr", "https://github.com/x/y/pull/2").Exit, 3)
	eq(t, "row", turRow(e), before)
	// coder task without a PR: lead may
	eq(t, "lead", turUpd(e, turLead, "--role", "qa").Exit, 0)
	// owner may on a coder task with a PR
	exec(e, "UPDATE tasks SET role='coder', pr_url='https://github.com/x/y/pull/1' WHERE id=?", turT)
	eq(t, "owner", turUpd(e, "", "--role", "qa").Exit, 0)
	eq(t, "role", val(e, "SELECT role FROM tasks WHERE id=?", turT), "qa")
}

func TestUpdRoleMergedExternallyWithRoleRefused(t *testing.T) {
	e := turEnv(t)
	e.NeedPython() // --merged-externally is Python
	exec(e, "UPDATE tasks SET pr_url='https://github.com/x/y/pull/1' WHERE id=?", turT)
	before := turRow(e)
	p := turUpd(e, "", "--role", "qa", "--status", "done", "--merged-externally", "--reason", "x")
	if p.Exit == 0 {
		t.Fatal("--merged-externally with --role accepted")
	}
	contains(t, "stderr", p.Stderr, "--role")
	eq(t, "row", turRow(e), before)
}

func TestUpdRoleStartingAgentRefusesRoleChange(t *testing.T) {
	e := turEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-coder-starting", Role: "coder", Goal: "G-001", Task: turT, Status: "starting"})
	before := turRow(e)
	p := turUpd(e, turLead, "--role", "qa")
	if p.Exit == 0 {
		t.Fatal("role change accepted with a starting agent")
	}
	contains(t, "stderr", p.Stderr, "zz-coder-starting")
	eq(t, "row", turRow(e), before)
}

func TestUpdRoleTaskAddDefaultExplicitAndBogusRole(t *testing.T) {
	e := turEnv(t)
	add := func(extra ...string) ft.Result {
		return run(e, turLead, append([]string{"task", "add", "--goal", "G-001", "--title", "t", "--spec", "s"}, extra...)...)
	}
	p := add()
	eq(t, "default exit", p.Exit, 0)
	q := add("--role", "qa")
	eq(t, "qa exit", q.Exit, 0)
	roles := col(e, "SELECT role FROM tasks WHERE id IN (?,?) ORDER BY role", strings.TrimSpace(p.Stdout), strings.TrimSpace(q.Stdout))
	eqS(t, "roles", roles, []string{"coder", "qa"})
	n := len(sql(e, "SELECT id FROM tasks"))
	bad := add("--role", "bogus")
	if bad.Exit == 0 {
		t.Fatal("bogus role accepted by task add")
	}
	contains(t, "stderr", bad.Stderr, "role must be one of")
	eq(t, "task count", len(sql(e, "SELECT id FROM tasks")), n)
}
