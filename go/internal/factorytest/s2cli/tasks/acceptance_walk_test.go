package s2tasks

import (
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_acceptance_walk.py (6 of 6 cases; see ported_s2cli_tasks.txt). Complete.

const awURL = "https://github.com/o/r/pull/7"

// validPRBody is tests/harness.py VALID_PR_BODY: a body that follows the PR template, with an Acceptance walk heading.
const validPRBody = "## Summary\nWhat changed. \n\n## Why\nT-0001 motivation.\n\n## Changes\n- one change\n\n" +
	"## Test evidence\n`python3 -m unittest` -> OK\n\n## Risk / rollback\nRevert this PR.\n\n" +
	"<details><summary>Acceptance walk</summary>\n\n## Acceptance walk\n1. met - cmd -> out\n</details>\n"

func awEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "lead-g-001-a")
	seedTask(e, ft.Task{ID: "T-0001", Status: "in_progress", Team: "g-001-a"}, true)
	for _, a := range [][2]string{{"coder-t-0001", "coder"}, {"lead-g-001-a", "lead"}} {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	}
	return e
}

func awStatus(e *ft.Env) (status, pr string) {
	r := sql(e, "SELECT status, COALESCE(pr_url,'NULL') FROM tasks WHERE id='T-0001'")[0]
	return r[0], r[1]
}

func awInReview(e *ft.Env, agent string) ft.Result {
	return run(e, agent, "task", "update", "T-0001", "--status", "in_review", "--pr", awURL)
}

func TestAcceptanceWalkRefusedWithoutHeading(t *testing.T) {
	e := awEnv(t)
	e.NeedPython() // a coder --status in_review runs gh in Python (require_acceptance_walk)
	seedPR(e, awURL, "OPEN", ptr("summary\nAcceptance walk\n### Acceptance walk\ninline ## Acceptance walk"), nil)
	p := awInReview(e, "coder-t-0001")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "pre-pr-check")
	contains(t, "stderr", p.Stderr, "## Acceptance walk")
	st, pr := awStatus(e)
	eq(t, "status", st, "in_progress")
	eq(t, "pr_url", pr, "NULL")
}

func TestAcceptanceWalkAcceptedWithHeading(t *testing.T) {
	e := awEnv(t)
	e.NeedPython() // a coder --status in_review runs gh in Python (require_acceptance_walk)
	seedPR(e, awURL, "OPEN", ptr(validPRBody), nil)
	p := awInReview(e, "coder-t-0001")
	eq(t, "exit", p.Exit, 0)
	st, _ := awStatus(e)
	eq(t, "status", st, "in_review")
}

func TestAcceptanceWalkGhFailureRefused(t *testing.T) {
	e := awEnv(t)
	e.NeedPython()                     // a coder --status in_review runs gh in Python (require_acceptance_walk)
	p := awInReview(e, "coder-t-0001") // the stub has no such PR: gh exits 1
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "retry")
	st, _ := awStatus(e)
	eq(t, "status", st, "in_progress")
}

func TestAcceptanceWalkTaskPRFieldUsedWithoutFlag(t *testing.T) {
	e := awEnv(t)
	e.NeedPython() // a coder --status in_review runs gh in Python (require_acceptance_walk)
	exec(e, "UPDATE tasks SET pr_url=? WHERE id='T-0001'", awURL)
	seedPR(e, awURL, "OPEN", ptr("no walk"), nil)
	refused(e, 3, "coder-t-0001", "task", "update", "T-0001", "--status", "in_review")
	seedPR(e, awURL, "OPEN", ptr(validPRBody), nil)
	ok(e, "coder-t-0001", "task", "update", "T-0001", "--status", "in_review")
}

func TestAcceptanceWalkNoPRAtAllRefused(t *testing.T) {
	e := awEnv(t)
	e.NeedPython() // a coder --status in_review runs gh in Python (require_acceptance_walk)
	started := time.Now()
	p := refused(e, 3, "coder-t-0001", "task", "update", "T-0001", "--status", "in_review")
	contains(t, "stderr", p.Stderr, "--pr")
	if n := len(e.Calls("gh")); n != 0 {
		t.Fatalf("no PR to look at: gh must not be called at all, got %d calls", n)
	}
	if d := time.Since(started); d > 5*time.Second { // T-0182: it was reported as hanging; it is one local refusal
		t.Fatalf("refusal took %v, want under 5s", d)
	}
}

func TestAcceptanceWalkNonCoderAndOtherStatusesUnchanged(t *testing.T) {
	e := awEnv(t)
	p := awInReview(e, "lead-g-001-a") // no body, no heading: a lead is not checked
	eq(t, "lead exit", p.Exit, 0)
	st, _ := awStatus(e)
	eq(t, "status", st, "in_review")
	ok(e, "coder-t-0001", "task", "update", "T-0001", "--status", "blocked")
}
