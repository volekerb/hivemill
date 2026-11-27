package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/shlex"
)

// A large task has at least this many acceptance criteria or scope paths, or is T3.
const (
	largeCriteria   = 4
	largeScopePaths = 6
)

// scopePaths counts the paths a task's declared scope lists: the FILES: entries, or the alternatives of its allow-list
// regexes, whichever names more (a spec often declares the same paths both ways).
func scopePaths(t *Task) int {
	s := DeclaredScope(t)
	files, allow := map[string]bool{}, map[string]bool{}
	for _, f := range s.Files {
		files[f] = true
	}
	for _, re := range s.Allow {
		for _, p := range alternatives(re.String()) {
			allow[p] = true
		}
	}
	return max(len(files), len(allow))
}

// alternatives splits a regex source at every | outside a character class, without the anchors and groups around each
// part: the paths an allow-list names, anchored or not.
func alternatives(re string) []string {
	var out []string
	start, class := 0, false
	add := func(end int) {
		p := strings.ReplaceAll(re[start:end], "(?:", "")
		if p = strings.Trim(p, "^$()"); p != "" {
			out = append(out, p)
		}
	}
	for i := 0; i < len(re); i++ {
		switch c := re[i]; {
		case c == '\\':
			i++
		case class:
			class = c != ']'
		case c == '[':
			class = true
		case c == '|':
			add(i)
			start = i + 1
		}
	}
	add(len(re))
	return out
}

// workflowBusy reports whether a subagent or workflow agent of the run's task sessions wrote its transcript since t: a
// session waiting for its workflow reads idle.
func (r *Runner) workflowBusy(run *Run, t time.Time) bool {
	sess, _ := r.Store.Sessions(run.Task)
	for _, s := range sess {
		if s.Role != "task" {
			continue
		}
		if run.cloned() { // the transcripts are on the session's machine
			rc, _, _ := r.rsh(run.Machine, "", 30*time.Second, fmt.Sprintf(
				`sid=%s; find "$HOME"/.claude/projects/*/"$sid"/subagents -name '*.jsonl' -mmin -%d 2>/dev/null | grep -q .`,
				shlex.Quote(s.SID), max(int(time.Since(t).Minutes()+0.5), 1)))
			if rc == 0 {
				return true
			}
			continue
		}
		dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "*", s.SID, "subagents")
		plain, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
		flows, _ := filepath.Glob(filepath.Join(dir, "workflows", "*", "*.jsonl"))
		for _, f := range append(plain, flows...) {
			if fi, err := os.Stat(f); err == nil && fi.ModTime().After(t) {
				return true
			}
		}
	}
	return false
}

// LargeTask is the rule of task_workflows=large.
func LargeTask(t *Task) bool {
	return t.Tier == "T3" || len(Criteria(t.Acceptance)) >= largeCriteria || scopePaths(t) >= largeScopePaths
}

// workflowAgents is the agent cap of the workflow request a new run of t carries; 0 = no request (task_workflows).
func workflowAgents(s Store, t *Task) int {
	if mode := s.Cfg("task_workflows"); mode == "all" || mode == "large" && LargeTask(t) {
		return max(cfgInt(s, "task_workflow_agents"), 1)
	}
	return 0
}

// workflowRequest is the owner's request for a workflow. The Workflow tool runs only when the user asks for it in their
// own words, and the runner's prompt is the owner's configured instruction.
func workflowRequest(agents int) string {
	return fmt.Sprintf("The owner asks you to use a workflow (the Workflow tool) for this task when the work splits into "+
		"independent parts. For example: implement the parts in parallel, then review, then verify each acceptance criterion. "+
		"Use at most %d agents.", agents)
}

func checkWorkflowCfg(key, value string) error {
	switch key {
	case "task_workflows":
		if value != "off" && value != "large" && value != "all" {
			return fmt.Errorf("task_workflows %q refused: use off, large or all", value)
		}
	case "task_workflow_agents":
		if n, err := strconv.Atoi(strings.TrimSpace(value)); err != nil || n < 1 {
			return fmt.Errorf("task_workflow_agents %q refused: use a number of 1 or more", value)
		}
	}
	return nil
}
