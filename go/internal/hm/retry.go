package hm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Retry in place (owner 2026-11-27): a failed task is fixed on its own branch and its own PR, never filed again as a
// new task. Cancel-and-re-file left one open PR per attempt (32 orphans on 2026-11-27) and paid for the same work
// twice. `hivemill task retry` sets the failed task back to todo; the next pass starts a fresh session on the pushed
// branch, which updates the open PR. retry_max bounds the attempts; after that the owner decides.

// retryMark is the meta row of a task's retries: N attempts so far, Of = the Dispatched time of the failed run that the
// pending retry replaces (empty once that run is replaced), and the planner's note for the next session.
type retryMark struct {
	N    int    `json:"n"`
	Of   string `json:"of,omitempty"`
	Note string `json:"note,omitempty"`
}

func retryKey(id string) string { return "retry:" + id }

func (r *Runner) retryMark(id string) retryMark {
	var m retryMark
	_ = json.Unmarshal([]byte(r.Store.Meta(retryKey(id))), &m)
	return m
}

// retryPending: the failed run old of task id was retried and no new run has replaced it yet.
func (r *Runner) retryPending(id string, old *Run) bool {
	if old == nil || old.State != Failed {
		return false
	}
	m := r.retryMark(id)
	return m.Of != "" && m.Of == old.Dispatched
}

func taskBranch(t *Task) string {
	if t.Branch != "" {
		return t.Branch
	}
	return "factory/" + strings.ToLower(t.ID)
}

// TaskPR is the open PR of the task's branch ("": none, or gh failed).
func (r *Runner) TaskPR(t *Task) string {
	rc, out, _ := r.Tools.GH("", "pr", "list", "-R", t.GHRepo, "--head", taskBranch(t), "--state", "open", "--json", "url", "-q", ".[0].url")
	if rc != 0 {
		return ""
	}
	return strings.TrimSpace(out)
}

// MayHavePR: the task ran or recorded a PR, so a gh lookup can find one (a task that never ran has none).
func (r *Runner) MayHavePR(t *Task) bool {
	run, _ := r.Store.Run(t.ID)
	return run != nil || t.PRURL != ""
}

// Retry sets a failed task back to todo so the next pass starts it again on its branch and PR. note goes into the new
// session's kickoff. It refuses a task that never ran, an active, merged or held run, a PR the owner closed, a task
// past retry_max retries, and criteria that fail the spec gate.
func (r *Runner) Retry(id, note string) (string, error) {
	t, err := r.Store.Task(id)
	if err != nil {
		return "", err
	}
	old, _ := r.Store.Run(id)
	switch {
	case old == nil:
		return "", fmt.Errorf("%w: %s has never run: a todo task starts on its own", ErrRefused, id)
	case Active(old.State):
		return "", fmt.Errorf("%w: %s has an active run (%s)", ErrRefused, id, old.State)
	case heldForOwner(old):
		return "", fmt.Errorf("%w: %s is held for the owner: the owner merges or closes its PR", ErrRefused, id)
	case old.State != Failed:
		return "", fmt.Errorf("%w: %s's run is %s; only a failed run is retried", ErrRefused, id, old.State)
	case strings.HasPrefix(old.FailReason, "the owner closed"):
		return "", fmt.Errorf("%w: the owner closed %s's PR: ask the owner before working on it again", ErrRefused, id)
	case t.Status == "done" || t.Status == "cancelled":
		return "", fmt.Errorf("%w: %s is %s", ErrRefused, id, t.Status)
	}
	if probs := LintProblems(t.Acceptance); len(probs) > 0 {
		return "", fmt.Errorf("%w: %s fails the spec gate (`hivemill task lint %s`): fix it with `hivemill task update` first",
			ErrRefused, id, id)
	}
	m := r.retryMark(id)
	if m.Of == old.Dispatched {
		if note != "" && note != m.Note {
			m.Note = note
			if err := r.saveRetry(id, m); err != nil {
				return "", err
			}
		}
		return fmt.Sprintf("%s already retried (attempt %d); it starts on the next pass\n", id, m.N+1), nil
	}
	max := cfgInt(r.Store, "retry_max")
	if m.N >= max {
		return "", fmt.Errorf("%w: %s was retried %d times (retry_max %d): ask the owner (`hivemill ask add --task %s ...`)",
			ErrRefused, id, m.N, max, id)
	}
	m = retryMark{N: m.N + 1, Of: old.Dispatched, Note: strings.TrimSpace(note)}
	if err := r.saveRetry(id, m); err != nil {
		return "", err
	}
	if err := r.Store.TaskStatus(id, "todo", ""); err != nil {
		return "", err
	}
	_ = r.Log.Emit("task.retried", old, Event{Data: map[string]any{"attempt": m.N + 1, "failed": truncate(old.FailReason, 300),
		"note": truncate(m.Note, 300)}})
	return fmt.Sprintf("%s retried: attempt %d of %d starts on the next pass, on %s and its open PR\n", id, m.N+1, max+1,
		taskBranch(t)), nil
}

func (r *Runner) saveRetry(id string, m retryMark) error {
	b, _ := json.Marshal(m)
	return r.Store.SetMeta(retryKey(id), string(b))
}

// retryBrief is the part of a retried run's kickoff that says it continues the failed attempt.
func retryBrief(attempt int, old *Run, branch, pr, note string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n\nThis is attempt %d of this task: a retry in place. The previous attempt failed: %s\n", attempt,
		truncate(old.FailReason, 600))
	at := "Branch " + branch + " already holds that attempt's commits"
	if pr != "" {
		at += " and its PR is " + pr
	}
	b.WriteString(at + ". Continue from them: read the PR and the failure, fix what failed and push to the same branch, " +
		"which updates the PR. Do not start over and never open a second PR.\n")
	if note != "" {
		fmt.Fprintf(&b, "Planner note: %s\n", note)
	}
	return b.String()
}

// CloseTaskPR closes the open PR of a cancelled task with a comment, keeping the branch ("" = none was open).
func (r *Runner) CloseTaskPR(t *Task) (string, error) {
	pr := r.TaskPR(t)
	if pr == "" {
		return "", nil
	}
	rc, _, errs := r.Tools.GH("", "pr", "close", pr, "-R", t.GHRepo, "-c",
		"Closing: task "+t.ID+" was cancelled in hivemill, so no run will merge this PR. The branch is kept.")
	if rc != 0 {
		return pr, fmt.Errorf("gh pr close %s exited %d: %s", pr, rc, strings.TrimSpace(errs))
	}
	return pr, nil
}
