package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const threeCriteria = "1. a cmd: true => exit 0\n2. b cmd: true => exit 0\n3. c cmd: true => exit 0"

func TestLargeTask(t *testing.T) {
	for name, c := range map[string]struct {
		task Task
		want bool
	}{
		"three criteria":   {Task{Acceptance: threeCriteria}, false},
		"four criteria":    {Task{Acceptance: threeCriteria + "\n4. d cmd: true => exit 0"}, true},
		"T3":               {Task{Tier: "T3"}, true},
		"T2":               {Task{Tier: "T2"}, false},
		"five FILES paths": {Task{Spec: "FILES: a/1.go, a/2.go, a/3.go, a/4.go, a/5.go"}, false},
		"six FILES paths":  {Task{Spec: "FILES: a/1.go, a/2.go, a/3.go, a/4.go, a/5.go, a/6.go"}, true},
		"six allowed paths": {Task{Acceptance: "1. scope cmd: git diff --name-only origin/main | " +
			"grep -vE '^(cmd/|docs/|internal/|roles/|ci/|README\\.md$)' => empty"}, true},
		"two allowed paths": {Task{Acceptance: "1. scope cmd: git diff --name-only origin/main | grep -vE '^(docs/|internal/)' => empty"}, false},
		"three paths declared twice": {Task{Spec: "FILES: docs/, internal/, roles/",
			Acceptance: "1. scope cmd: git diff --name-only origin/main | grep -vE '^(docs/|internal/|roles/)' => empty"}, false},
	} {
		if got := LargeTask(&c.task); got != c.want {
			t.Errorf("%s: LargeTask = %v, want %v (%d scope paths)", name, got, c.want, scopePaths(&c.task))
		}
	}
}

func TestKickoffCarriesTheWorkflowRequestPerMode(t *testing.T) {
	_, s := testRunner(t, "")
	small := &Task{ID: "T-0001", Acceptance: threeCriteria}
	large := &Task{ID: "T-0002", Tier: "T3", Acceptance: threeCriteria}
	for _, c := range []struct {
		mode         string
		small, large bool
	}{{"", false, true}, {"off", false, false}, {"large", false, true}, {"all", true, true}} {
		if c.mode != "" { // "" = the default, large
			if err := s.SetCfg("task_workflows", c.mode); err != nil {
				t.Fatal(err)
			}
		}
		for task, want := range map[*Task]bool{small: c.small, large: c.large} {
			run := &Run{Agent: "claude-coder-x", Branch: "factory/x", Base: "main", WorkflowAgents: workflowAgents(s, task)}
			got := Kickoff(run, task)
			has := strings.Contains(got, "The owner asks you to use a workflow (the Workflow tool) for this task when the work splits into "+
				"independent parts.") && strings.Contains(got, "Use at most 6 agents.")
			if has != want || !want && strings.Contains(got, "Workflow") {
				t.Errorf("mode %q, %s: request in prompt = %v, want %v:\n%s", c.mode, task.ID, has, want, got)
			}
		}
	}
	if err := s.SetCfg("task_workflow_agents", "3"); err != nil {
		t.Fatal(err)
	}
	if got := Kickoff(&Run{WorkflowAgents: workflowAgents(s, large)}, large); !strings.Contains(got, "Use at most 3 agents.") {
		t.Errorf("task_workflow_agents=3 not in the prompt:\n%s", got)
	}
	for key, bad := range map[string]string{"task_workflows": "big", "task_workflow_agents": "0"} {
		if err := s.SetCfg(key, bad); err == nil {
			t.Errorf("config %s=%s accepted", key, bad)
		}
	}
}

func TestWorkflowAgentsCountTowardTheSessionUsage(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".claude", "projects", "p")
	write := func(rel, id string) {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		line := `{"type":"assistant","timestamp":"2026-11-26T10:00:00Z","uuid":"` + id + `","requestId":"` + id + `","message":{"id":"` + id +
			`","model":"claude-sonnet-5-5","usage":{"input_tokens":10,"output_tokens":1000}}}` + "\n"
		if err := os.WriteFile(p, []byte(line), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("s1.jsonl", "main")
	write("s1/subagents/agent-a.jsonl", "plain")
	write("s1/subagents/workflows/wf_1/agent-b.jsonl", "wf1")
	write("s1/subagents/workflows/wf_1/agent-c.jsonl", "wf2")
	write("other/subagents/workflows/wf_2/agent-d.jsonl", "not-ours")
	u := SessionUsages(home, []string{"s1"})["s1"]
	if u.Input != 40 || u.Output != 4000 || u.WorkflowAgents != 2 || u.USD <= 0 {
		t.Errorf("usage = %+v, want the 4 transcripts of s1 with 2 workflow agents", u)
	}
	cost := 0.5
	back, _ := usageOf(Event{CostUSD: &cost, Data: map[string]any{"workflow_agents": float64(2)}})
	if back.WorkflowAgents != 2 || u.data()["workflow_agents"] != 2 {
		t.Errorf("workflow_agents does not round-trip: %+v %v", back, u.data())
	}
}

func TestSummariseComparesTasksWithAndWithoutWorkflows(t *testing.T) {
	h := func(v float64) *float64 { return &v }
	tot := Summarise([]Row{
		{Task: "T-1", dispatched: "x", Outcome: "merged", USD: 3, HoursToMerge: h(1), WorkflowAsked: true, WorkflowAgents: 4,
			Judge: []string{"APPROVE"}},
		{Task: "T-2", dispatched: "x", Outcome: "FAIL: x", USD: 1, WorkflowAsked: true, WorkflowAgents: 2, CIRounds: 2},
		{Task: "T-3", dispatched: "x", Outcome: "merged", USD: 2, HoursToMerge: h(5), WorkflowAsked: true},
		{Task: "T-4", dispatched: "x", Outcome: "merged", USD: 50, Eval: true, WorkflowAgents: 1},
		{Task: "T-5"}, // never dispatched: on neither side
	})
	w, n := tot.WithWorkflow, tot.WithoutWorkflow
	if w.Tasks != 2 || w.Asked != 2 || w.Merged != 1 || *w.PerMergedTask != 4 || *w.RoundsPerTask != 1.5 || *w.MedianHours != 1 {
		t.Errorf("with = %+v", w)
	}
	if n.Tasks != 1 || n.Asked != 1 || n.Merged != 1 || *n.PerMergedTask != 2 || *n.RoundsPerTask != 0 || *n.MedianHours != 5 {
		t.Errorf("without = %+v", n)
	}
}
