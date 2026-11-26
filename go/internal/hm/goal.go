package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Claude Code's `/goal CONDITION` keeps a session working until an evaluator judges the condition met after a turn.
// Everything after `/goal ` is the condition, all lines of it, and a condition over goalMax characters is refused.
const (
	goalMax     = 4000
	kickoffFile = ".hivemill/kickoff.md"
)

func goalLen(s string) int { return len(utf16.Encode([]rune(s))) } // characters as Claude Code counts them

// goalPrompt is the first prompt of a task session: kickoff behind `/goal ` and the condition built from the criteria
// (task_goal), so the session keeps working until they pass. A kickoff too long for a condition goes to kickoffFile in
// the clone and the prompt points to it. Other harnesses have no /goal and get the kickoff as it is.
func (r *Runner) goalPrompt(harness string, run *Run, t *Task, kickoff string) string {
	if harness != Harness || cfgInt(r.Store, "task_goal") != 1 {
		return kickoff
	}
	head := fmt.Sprintf("Every acceptance criterion below passes when run exactly as written in the worktree, the branch is pushed "+
		"and the PR is open, or stop after %d turns.\n", cfgInt(r.Store, "task_goal_turns"))
	var list strings.Builder
	for _, c := range Criteria(t.Acceptance) {
		fmt.Fprintf(&list, "%d. %s\n", c.ID, c.Text)
	}
	cond := head + list.String()
	if full := cond + "\n" + kickoff; goalLen(full) <= goalMax {
		return "/goal " + full
	}
	if err := writeFile(filepath.Join(run.Worktree, kickoffFile), kickoff+"\n", 0o600); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: %s: %v (the session starts without /goal)\n", run.Task, err)
		return kickoff
	}
	pointer := "\nFirst read " + kickoffFile + " in the worktree: it is your kickoff message, with the task id, spec, " +
		"acceptance criteria and branch. Follow it."
	// The owner's workflow request stays in the message itself: the Workflow tool wants it in the user's words.
	if run.WorkflowAgents > 0 {
		pointer += "\n" + workflowRequest(run.WorkflowAgents)
	}
	if goalLen(cond+pointer) > goalMax {
		cond = strings.Replace(head, "criterion below", "criterion in "+kickoffFile, 1)
	}
	return "/goal " + cond + pointer
}

func checkGoalCfg(key, value string) error {
	switch key {
	case "task_goal":
		if value != "0" && value != "1" {
			return fmt.Errorf("task_goal %q refused: use 0 or 1", value)
		}
	case "task_goal_turns":
		if n, err := strconv.Atoi(value); err != nil || n < 1 {
			return fmt.Errorf("task_goal_turns %q refused: use a number of 1 or more", value)
		}
	}
	return nil
}
