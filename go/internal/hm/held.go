package hm

import (
	"fmt"
	"strings"
)

// A held run moves on without the owner's hand in two cases: the owner answered its question, or its PR conflicts with
// the base.

// approves reports an answer that approves the held PR: it starts with "approved" (the director's and the owner's form).
func approves(answer string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(answer)), "approved")
}

// heldAnswer is the latest answer to the question of the run's held round, nil when there is none.
func heldAnswer(s *SQLite, run *Run) (*Ask, error) {
	asks, err := s.Asks("answered")
	if err != nil {
		return nil, err
	}
	for i := len(asks) - 1; i >= 0; i-- {
		if asks[i].Ref == heldRef(run) {
			return &asks[i], nil
		}
	}
	return nil, nil
}

// actHeld is called with the task lock held, after checkHeld; pr is what that read. An approval moves the run to
// merging, where the approved head needs no second owner decision. Any other answer, and a conflict, resume the task
// session: its new head goes through the gate, the judge and the merge limits again. Whatever cannot be done now (an
// unreadable PR, no terminal list, an unreachable machine) waits for a later pass.
func (r *Runner) actHeld(run *Run, pr *PR, listed bool) {
	if !heldForOwner(run) || (run.PRURL != "" && (pr == nil || pr.State != "OPEN")) {
		return
	}
	if t, err := r.Store.Task(run.Task); err != nil || t.Status == "cancelled" {
		return
	}
	s, err := r.sq()
	if err != nil {
		return
	}
	ask, err := heldAnswer(s, run)
	if err != nil || (ask == nil && (pr == nil || pr.Mergeable != "CONFLICTING")) {
		return
	}
	approved := ask != nil && pr != nil && approves(ask.Answer) // with no PR there is nothing to merge
	if !approved {
		if !listed || (run.Machine != "" && machineDown(r.Tools, run.Machine)) {
			return
		}
		if err := r.remakeWorktree(run); err != nil {
			r.printf("%s: held: %v\n", run.Task, err)
			return
		}
	}
	// one answer acts once: a run held again in the same round gets a new question
	if ask != nil {
		if err := s.RetireAskRef(heldRef(run)); err != nil {
			r.printf("%s: held: %v\n", run.Task, err)
			return
		}
	}
	run.Outcome, run.FailReason, run.Ended, run.MergeHold = "", "", "", ""
	run.IdleSince, run.IdleNudges, run.Resumes = "", 0, 0
	switch {
	case approved:
		run.State, run.Approved = Merging, or(run.JudgeHead, run.Head)
		_ = r.Store.TaskStatus(run.Task, "in_progress", "")
	case ask != nil:
		run.History = append(run.History, fmt.Sprintf("round %d: held, answered: %s", run.Round, truncate(ask.Answer, 300)))
		r.resumeAs(run, "answer", "Your PR was held for the owner, who answered:\n"+ask.Answer+"\nDo what the answer says, "+
			r.pushAndStop(run.Project)+".")
	default:
		run.History = append(run.History, fmt.Sprintf("round %d: held, the PR conflicts with the base", run.Round))
		r.resume(run, "Your held PR conflicts with the base branch: merge origin/"+run.Base+
			" and resolve, keep the approved changes (`git fetch origin && git merge origin/"+run.Base+"`), "+r.pushAndStop(run.Project)+".")
	}
	if ask != nil {
		_ = r.Log.Emit("ask.applied", run, Event{Data: map[string]any{"id": ask.ID, "state": run.State, "pr": run.PRURL}})
	}
}

// remakeWorktree brings back a clone-mode worktree that was removed while the run was held.
func (r *Runner) remakeWorktree(run *Run) error {
	if !run.cloned() || !run.RemoteDone {
		return nil
	}
	t, err := r.Store.Task(run.Task)
	if err != nil {
		return err
	}
	wt, err := r.remoteWorktree(run.Machine, run.RemoteHome, t, run.Branch)
	if err != nil {
		return fmt.Errorf("cannot remake the worktree on %s: %w", run.Machine, err)
	}
	run.Worktree, run.RemoteDone = wt, false
	return nil
}
