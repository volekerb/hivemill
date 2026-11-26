package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const goalHead = "/goal Every acceptance criterion below passes when run exactly as written in the worktree, the branch is " +
	"pushed and the PR is open, or stop after "

func goalFixture(t *testing.T) (*Runner, *SQLite, *Run, *Task) {
	t.Helper()
	r, s := testRunner(t, "")
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(wt, ".hivemill"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := &Run{Task: "T-1", Agent: "a", Branch: "factory/t-1", Base: "main", Worktree: wt}
	task := &Task{ID: "T-1", Title: "t", Spec: "s", Acceptance: "1. one - cmd:true => exit 0\n2. two - cmd:true => exit 0"}
	return r, s, run, task
}

func TestGoalPromptWithTaskGoalOn(t *testing.T) {
	r, s, run, task := goalFixture(t) // task_goal 1 and task_goal_turns 60 are the defaults
	k := Kickoff(run, task)
	got := r.goalPrompt(Harness, run, task, k)
	want := goalHead + "60 turns.\n1. one - cmd:true => exit 0\n2. two - cmd:true => exit 0\n" + goalExits + "\n" + k
	if got != want {
		t.Errorf("prompt:\n%s\nwant:\n%s", got, want)
	}
	if err := s.SetCfg("task_goal_turns", "7"); err != nil {
		t.Fatal(err)
	}
	if got := r.goalPrompt(Harness, run, task, k); !strings.HasPrefix(got, goalHead+"7 turns.\n") {
		t.Errorf("task_goal_turns=7 not in the prompt:\n%s", got)
	}
	if got := r.goalPrompt("codex", run, task, k); got != k {
		t.Errorf("another harness got a prefix:\n%s", got)
	}
}

func TestGoalPromptWithTaskGoalOff(t *testing.T) {
	r, s, run, task := goalFixture(t)
	if err := s.SetCfg("task_goal", "0"); err != nil {
		t.Fatal(err)
	}
	k := Kickoff(run, task)
	if got := r.goalPrompt(Harness, run, task, k); got != k {
		t.Errorf("task_goal=0 changed the prompt:\n%s", got)
	}
}

func TestGoalPromptStaysWithinTheConditionLimit(t *testing.T) {
	r, _, run, task := goalFixture(t)
	run.WorkflowAgents = 6
	long := strings.Repeat("é ", goalMax)
	for name, c := range map[string]struct {
		spec, acceptance, want string
	}{
		"long spec":     {long, task.Acceptance, "1. one - cmd:true => exit 0\n"},
		"long criteria": {"s", "1. " + long + "- cmd:true => exit 0", "criterion in " + kickoffFile},
	} {
		tk := &Task{ID: "T-1", Title: "t", Spec: c.spec, Acceptance: c.acceptance}
		k := Kickoff(run, tk)
		got := r.goalPrompt(Harness, run, tk, k)
		if !strings.HasPrefix(got, "/goal ") || goalLen(strings.TrimPrefix(got, "/goal ")) > goalMax {
			t.Errorf("%s: not a /goal within the limit (%d):\n%.300s", name, goalLen(got), got)
		}
		for _, part := range []string{c.want, goalExits, "First read " + kickoffFile, "Use at most 6 agents."} {
			if !strings.Contains(got, part) {
				t.Errorf("%s: %q not in the prompt", name, part)
			}
		}
		if b, err := os.ReadFile(filepath.Join(run.Worktree, kickoffFile)); err != nil || string(b) != k+"\n" {
			t.Errorf("%s: kickoff file: %v", name, err)
		}
	}
	run.Worktree = filepath.Join(run.Worktree, "missing")
	k := Kickoff(run, &Task{ID: "T-1", Spec: long})
	if got := r.goalPrompt(Harness, run, task, k); got != k {
		t.Errorf("an unwritable kickoff file must leave the full kickoff, got:\n%.200s", got)
	}
}

func TestGoalPromptWritesTheKickoffOnTheCloneMachine(t *testing.T) {
	r, _, run, task := goalFixture(t)
	run.Machine, run.Mirror, run.Worktree = "box", run.Worktree, "/remote/wt"
	var machine, stdin, command string
	r.SSH = func(m, in string, _ time.Duration, c string) (int, string, string) {
		machine, stdin, command = m, in, c
		return 0, "", ""
	}
	k := Kickoff(run, &Task{ID: "T-1", Spec: strings.Repeat("x", goalMax)})
	got := r.goalPrompt(Harness, run, task, k)
	if !strings.Contains(got, "First read "+kickoffFile) {
		t.Errorf("no pointer in the prompt:\n%.300s", got)
	}
	if machine != "box" || stdin != k+"\n" || !strings.Contains(command, "/remote/wt/"+kickoffFile) {
		t.Errorf("kickoff not written over ssh: machine %q, command %q", machine, command)
	}
}

func TestGoalConfigValues(t *testing.T) {
	_, s := testRunner(t, "")
	for key, bad := range map[string]string{"task_goal": "on", "task_goal_turns": "0"} {
		if err := s.SetCfg(key, bad); err == nil {
			t.Errorf("config %s=%s accepted", key, bad)
		}
	}
}
