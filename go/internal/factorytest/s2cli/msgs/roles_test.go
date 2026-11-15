package s2msgs

import (
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_roles.py (20 cases). judge and spawn paths use NeedPython (command still in Python).

func newRolesEnv(t *testing.T) *ft.Env {
	t.Helper()
	t.Parallel()
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "in_progress", Team: "g-001-a"})
	sqlExec(t, e, "INSERT INTO tasks(id,goal_id,team_id,title,role,status,pr_url,spec,acceptance,created,updated) "+
		"VALUES('T-0002','G-001','g-001-a','zz-test task','pm','in_progress',NULL,'zz-test spec','1. zz-test','t','t')")
	agents := [][2]string{
		{"zz-test-coder", "coder"}, {"zz-test-lead", "lead"}, {"zz-test-boss", "boss"},
		{"zz-test-director", "director"}, {"zz-test-qa", "qa"}, {"zz-test-reviewer", "reviewer"},
		{"zz-test-judge", "judge"}, {"zz-test-pm", "pm"}, {"zz-test-cleaner", "cleaner"},
	}
	for _, a := range agents {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	}
	return e
}

func TestRolesCoderCannotGoalNew(t *testing.T) {
	e := newRolesEnv(t)
	r := failAs(e, "zz-test-coder", 3, "goal", "new", "--title", "zz-test")
	contains(t, "stderr", r.Stderr, "only the boss creates goals")
	if e.Count("SELECT count(*) FROM goals") != 1 {
		t.Fatal("goal was created")
	}
}

func TestRolesCoderCannotSetConfig(t *testing.T) {
	e := newRolesEnv(t)
	failAs(e, "zz-test-coder", 3, "config", "max_live_agents", "99")
	rows := e.SQL("SELECT value FROM config WHERE key='max_live_agents'")
	if len(rows) != 1 || rows[0][0] != "6" {
		t.Fatalf("max_live_agents = %v, want 6", rows)
	}
}

func TestRolesCoderCanReadConfig(t *testing.T) {
	e := newRolesEnv(t)
	okAs(e, "zz-test-coder", "config")
}

func TestRolesDirectorCanSetConfig(t *testing.T) {
	e := newRolesEnv(t)
	okAs(e, "zz-test-director", "config", "max_live_agents", "7")
	rows := e.SQL("SELECT value FROM config WHERE key='max_live_agents'")
	if len(rows) != 1 || rows[0][0] != "7" {
		t.Fatalf("max_live_agents = %v, want 7", rows)
	}
}

func TestRolesLeadCannotSpawnLeadOrDirector(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	for _, role := range []string{"lead", "director", "boss"} {
		failAs(e, "zz-test-lead", 3, "spawn", role, "--name", "zz-test-x")
	}
	if e.Count("SELECT count(*) FROM agents WHERE name='zz-test-x'") != 0 {
		t.Fatal("agent was spawned")
	}
}

func TestRolesLeadCanSpawnWorker(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	p := okAs(e, "zz-test-lead", "spawn", "pm", "--name", "zz-test-pm2")
	contains(t, "stdout", p.Stdout, "SPAWNED zz-test-pm2")
}

func TestRolesBossCanOnlySpawnDirector(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	for _, role := range []string{"coder", "lead", "pm", "judge"} {
		failAs(e, "zz-test-boss", 3, "spawn", role, "--name", "zz-test-x")
	}
	sqlExec(t, e, "UPDATE agents SET status='exited' WHERE name='zz-test-director'")
	p := okAs(e, "zz-test-boss", "spawn", "director", "--name", "zz-test-director2")
	contains(t, "stdout", p.Stdout, "SPAWNED zz-test-director2")
}

func TestRolesWorkersCannotSpawn(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	for _, who := range []string{"zz-test-coder", "zz-test-qa", "zz-test-reviewer", "zz-test-judge", "zz-test-pm"} {
		failAs(e, who, 3, "spawn", "coder", "--name", "zz-test-x")
	}
}

func TestRolesQaReviewerCannotMarkTaskDone(t *testing.T) {
	e := newRolesEnv(t)
	for _, who := range []string{"zz-test-qa", "zz-test-reviewer"} {
		failAs(e, who, 3, "task", "update", "T-0002", "--status", "done")
		failAs(e, who, 3, "task", "update", "T-0001", "--status", "done")
	}
	rows := e.SQL("SELECT status FROM tasks WHERE id='T-0001'")
	if len(rows) != 1 || rows[0][0] != "in_progress" {
		t.Fatalf("T-0001 status = %v, want in_progress", rows)
	}
}

func TestRolesQaCanMarkBlocked(t *testing.T) {
	e := newRolesEnv(t)
	okAs(e, "zz-test-qa", "task", "update", "T-0001", "--status", "blocked")
}

func TestRolesCoderCannotMarkCoderTaskDone(t *testing.T) {
	e := newRolesEnv(t)
	r := failAs(e, "zz-test-coder", 3, "task", "update", "T-0001", "--status", "done")
	contains(t, "stderr", r.Stderr, "judge merges")
	rows := e.SQL("SELECT status FROM tasks WHERE id='T-0001'")
	if len(rows) != 1 || rows[0][0] != "in_progress" {
		t.Fatalf("status = %v", rows)
	}
}

func TestRolesCoderCanMarkInReview(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	seedPRViewURL(t, e, "https://x/pull/9", prStubOpts{body: validPRBody()})
	okAs(e, "zz-test-coder", "task", "update", "T-0001", "--status", "in_review", "--pr", "https://x/pull/9")
	rows := e.SQL("SELECT status, pr_url FROM tasks WHERE id='T-0001'")
	if len(rows) != 1 || rows[0][0] != "in_review" || rows[0][1] != "https://x/pull/9" {
		t.Fatalf("task = %v", rows)
	}
}

func TestRolesOwnerCanMarkCoderTaskDone(t *testing.T) {
	e := newRolesEnv(t)
	okAs(e, "owner", "task", "update", "T-0001", "--status", "done")
}

func TestRolesWorkerCannotSendEscalation(t *testing.T) {
	e := newRolesEnv(t)
	for _, who := range []string{"zz-test-coder", "zz-test-qa", "zz-test-reviewer", "zz-test-pm", "zz-test-judge"} {
		failAs(e, who, 3, "msg", "director", "help", "x", "--kind", "escalation")
	}
	if e.Count("SELECT count(*) FROM messages") != 0 {
		t.Fatal("message was sent")
	}
}

func TestRolesLeadCanSendEscalation(t *testing.T) {
	e := newRolesEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director-live", Role: "director"})
	okAs(e, "zz-test-lead", "msg", "director", "help", "x", "--kind", "escalation")
}

func TestRolesUnknownAgentRefused(t *testing.T) {
	e := newRolesEnv(t)
	cases := [][]string{
		{"goal", "new", "--title", "zz-test"},
		{"config", "git_push", "1"},
		{"spawn", "coder", "--name", "zz-test-x"},
		{"plan", "G-001", "body"},
		{"task", "add", "--goal", "G-001", "--title", "t"},
		{"verdict", "T-0001", "--pass", "--summary", "s"},
		{"judge", "T-0001", "--merge", "--reason", "r"},
		{"eval", "g-001-a", "--score", "3"},
	}
	for i, args := range cases {
		if args[0] == "spawn" || args[0] == "judge" || args[0] == "eval" {
			e.NeedPython()
		}
		if args[0] == "verdict" {
			seedPRView(t, e, prStubOpts{})
		}
		failAs(e, "zz-test-ghost", 3, args...)
		if i == 0 && e.Count("SELECT count(*) FROM goals") != 1 {
			t.Fatal("goal count changed on first refusal")
		}
	}
	if e.Count("SELECT count(*) FROM goals") != 1 {
		t.Fatal("extra goal")
	}
}

func TestRolesUnknownAgentCannotEscalate(t *testing.T) {
	e := newRolesEnv(t)
	failAs(e, "zz-test-ghost", 3, "msg", "director", "x", "y", "--kind", "escalation")
}

func TestRolesCoderCannotVerdict(t *testing.T) {
	e := newRolesEnv(t)
	seedPRView(t, e, prStubOpts{})
	failAs(e, "zz-test-coder", 3, "verdict", "T-0001", "--pass", "--summary", "lgtm")
	if e.Count("SELECT count(*) FROM verdicts") != 0 {
		t.Fatal("verdict recorded")
	}
}

func TestRolesReviewerCannotJudge(t *testing.T) {
	e := newRolesEnv(t)
	e.NeedPython()
	seedPRView(t, e, prStubOpts{})
	for _, who := range []string{"zz-test-reviewer", "zz-test-qa", "zz-test-coder", "zz-test-lead"} {
		failAs(e, who, 3, "judge", "T-0001", "--merge", "--reason", "r")
	}
	if ghPRMergeCalls(e) != 0 {
		t.Fatal("gh pr merge was called")
	}
}

func TestRolesPmCanPlanCoderAndQaCannot(t *testing.T) {
	e := newRolesEnv(t)
	okAs(e, "zz-test-pm", "plan", "G-001", "a plan")
	failAs(e, "zz-test-coder", 3, "plan", "G-001", "a plan")
	failAs(e, "zz-test-qa", 3, "plan", "G-001", "a plan")
}
