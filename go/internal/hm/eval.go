package hm

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Eval mode measures the review layers (gate, /code-review in the task session, judge) on a branch that already holds
// a change. It never merges: an eval run never enters the merge state, Merge refuses it, no verdicts row is written, and
// the PR must be a draft. The operator keeps the expected answer outside the factory home.
const (
	Evaluated     = "evaluated"
	EvalFull      = "full"       // a task session reviews the prepared head, then the gate, CI and the judge
	EvalJudgeOnly = "judge-only" // no session: the runner gates the prepared head itself, then a judge reads it
	EvalReview    = "review"     // a task session only runs /code-review on the prepared head and writes its findings
)

// EvalStart starts an eval run of a task whose branch is pushed and has an open draft PR.
func (r *Runner) EvalStart(id, mode string) error {
	if mode != EvalFull && mode != EvalJudgeOnly && mode != EvalReview {
		return fmt.Errorf("%w: eval mode %q: use %s, %s or %s", ErrRefused, mode, EvalFull, EvalJudgeOnly, EvalReview)
	}
	t, err := r.Store.Task(id)
	if err != nil {
		return err
	}
	if probs := LintProblems(t.Acceptance); len(probs) > 0 {
		return fmt.Errorf("%w: %s fails the spec gate: %s", ErrRefused, id, strings.Join(probs, "; "))
	}
	if old, _ := r.Store.Run(id); old != nil && Active(old.State) {
		return fmt.Errorf("%w: %s already has an active run (%s)", ErrRefused, id, old.State)
	}
	if why := r.runCapBlock(); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	if t.RepoPath == "" || t.GHRepo == "" {
		return fmt.Errorf("%w: %s: its project has no repo_path or gh_repo", ErrRefused, id)
	}
	branch := t.Branch
	if branch == "" {
		branch = "factory/" + strings.ToLower(id)
	}
	run := &Run{Task: id, Goal: t.Goal, Project: t.Project, Repo: t.GHRepo, Base: t.Base, Branch: branch,
		Agent: "claude-coder-" + strings.ToLower(id) + "-a7", State: Working, Round: 1, Eval: true, EvalMode: mode,
		Dispatched: nowISO(), RoundStart: nowISO(), Criteria: len(Criteria(t.Acceptance)), Continues: r.continuesFrom(id, branch)}
	if run.CINone, err = r.ciMode(t.Project); err != nil {
		return err
	}
	if cmd, _ := r.projectGate(t.Project); run.CINone && strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("%w: %s: project %s has ci_gate none and no gate_cmd, so nothing would test it", ErrRefused, id, t.Project)
	}
	url := r.findPR(run, t)
	if url == "" {
		return fmt.Errorf("%w: %s: no open PR for %s (push the prepared branch and open a draft PR first)", ErrRefused, id, branch)
	}
	pr, err := PRView(r.Tools, url)
	if err != nil {
		return err
	}
	if !pr.IsDraft || pr.State != "OPEN" {
		return fmt.Errorf("%w: %s: eval needs an open draft PR, %s is %s draft=%v", ErrRefused, id, url, pr.State, pr.IsDraft)
	}
	run.PRURL, run.Head = url, pr.HeadRefOid
	caps := r.ReadCaps(time.Now())
	if why := caps.DispatchBlock(r.Store, t.Project); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	if run.Worktree, err = r.Clone(t, branch); err != nil {
		return err
	}
	// Start from the prepared head and an empty .hivemill/, also when the clone is reused from an earlier eval run.
	if rc, _, errs := r.Tools.Git(run.Worktree, "fetch", "-q", "origin", branch); rc != 0 {
		return fmt.Errorf("fetch %s: %s", branch, firstLine(errs))
	}
	if rc, _, errs := r.Tools.Git(run.Worktree, "checkout", "-q", "-f", "-B", branch, "origin/"+branch); rc != 0 {
		return fmt.Errorf("checkout %s: %s", branch, firstLine(errs))
	}
	_ = os.RemoveAll(filepath.Join(run.Worktree, ".hivemill"))
	_ = os.MkdirAll(filepath.Join(run.Worktree, ".hivemill"), 0o755)
	run.Risky, run.RiskWhy = Risk(r.Env.Home, t, nil)
	if run.Model, run.Effort, err = r.ModelFor(run.Risky); err != nil {
		return err
	}
	if err := r.Store.PutRun(run); err != nil {
		return err
	}
	_ = r.Log.Emit("task.dispatched", run, Event{Model: run.Model, Effort: run.Effort, Data: map[string]any{"eval": true, "mode": mode,
		"head": run.Head, "risky": run.Risky}})
	if mode == EvalJudgeOnly {
		rec := r.EvalGate(run)
		run.LastGate, run.Head, run.Tier, run.State, run.Gated = rec, rec.Head, rec.Tier, Gated, nowISO()
		_ = r.Log.Emit("eval.gate", run, Event{Data: map[string]any{"problems": rec.Problems, "reruns": rec.Reruns, "tier": rec.Tier}})
		r.printf("EVAL %s judge-only: gate %d problems; the judge starts when CI has finished\n", id, len(rec.Problems))
		return r.Store.PutRun(run)
	}
	var kick string
	if mode == EvalReview {
		kick = EvalReviewKickoff(run, t) // no /goal: this session must not change code until the criteria pass
	} else {
		kick = r.goalPrompt(Harness, run, t, EvalKickoff(run, t))
	}
	if err := r.launchTask(run, kick); err != nil {
		return err
	}
	r.printf("EVAL %s %s: %s (%s) in %s\n", id, mode, run.Agent, run.Model, run.Worktree)
	return r.Store.PutRun(run)
}

// evalBase is the task's kickoff without its steps for opening a PR (an eval has its draft PR), and its last line.
func evalBase(run *Run, t *Task) (string, string) {
	base := *run
	base.CINone = false
	return Kickoff(&base, t), kickoffEnd(&base)
}

// EvalKickoff reads like a resumed task: the change is already on the branch, the review and the rest of the role
// steps are still to do. It does not say the run is an eval.
func EvalKickoff(run *Run, t *Task) string {
	k, end := evalBase(run, t)
	last := "push, wait for CI once and stop"
	if run.CINone {
		last = "push and stop (this project has no PR checks to wait for)"
	}
	return strings.TrimSuffix(k, end) +
		fmt.Sprintf("\nBranch %s, base %s. An earlier session of this task has ended after the implementation: the change is committed "+
			"on the branch and its draft PR %s is open. Do not start over. Continue your role steps from there: check the change "+
			"against the spec and the criteria, run the affected tests, run /code-review, fix the findings, write the .hivemill files, "+
			"%s.", run.Branch, run.Base, run.PRURL, last)
}

// EvalReviewKickoff asks only for the review: /code-review on the committed change, findings written, no code change.
func EvalReviewKickoff(run *Run, t *Task) string {
	k, end := evalBase(run, t)
	return strings.TrimSuffix(k, end) +
		fmt.Sprintf("\nBranch %s, base %s. The change is committed on the branch and its draft PR %s is open. Your only job now is the "+
			"review step: run /code-review on the branch, wait for it, then write .hivemill/findings.json with every finding and "+
			"\"resolution\": \"open\". Do not change, commit or push code. Then stop.", run.Branch, run.Base, run.PRURL)
}

// EvalGate is the deterministic layer on the prepared head without a session: the gate's checks minus the session's own
// duties (findings, review, evidence), and every test:/cmd: proof re-run.
func (r *Runner) EvalGate(run *Run) *GateRecord {
	rec := r.Gate(run)
	var keep []string
	for _, p := range rec.Problems {
		if !strings.HasPrefix(p, "findings:") && !strings.HasPrefix(p, "review:") && !strings.HasPrefix(p, "evidence:") {
			keep = append(keep, p)
		}
	}
	rec.Problems = keep
	if rec.Reruns == nil {
		t, err := r.Store.Task(run.Task)
		if err == nil {
			logdir := filepath.Join(r.Env.StateDir(), "runs", run.Task, "eval-gate")
			_ = os.MkdirAll(logdir, 0o700)
			rec.Reruns = r.RerunProofs(run, Criteria(t.Acceptance), logdir)
			for _, x := range rec.Reruns {
				if x.OK != nil && !*x.OK {
					rec.Problems = append(rec.Problems, fmt.Sprintf("criterion %d (%s): the factory re-ran its proof and it failed: %s", x.ID, x.Kind, x.Note))
				}
			}
		}
	}
	for k := range rec.Checks {
		if k == "findings" || k == "review" || k == "evidence" {
			delete(rec.Checks, k)
		}
	}
	for _, p := range rec.Problems {
		if i := strings.Index(p, ": "); i > 0 && !strings.HasPrefix(p, "criterion ") {
			rec.Checks[p[:i]] = "FAIL " + p[i+2:]
		}
	}
	return rec
}

// evalJudge starts the judge of a judge-only eval run once CI has finished, green or red.
func (r *Runner) evalJudge(run *Run) {
	if err := r.StartJudge(run); err != nil {
		r.fail(run, "the judge did not start: "+err.Error())
	}
}

// evalFinish ends an eval run after its first verdict (or a failure): sessions closed, costs recorded, one eval.done
// event with what each layer said. A full run's PR is labelled eval, closed and its branch deleted; a judge-only run
// leaves the PR open for the full run and empties .hivemill/ so that session never sees the judge packet.
func (r *Runner) evalFinish(run *Run, state, outcome string) {
	run.State, run.Outcome, run.Ended = state, "eval: "+outcome, nowISO()
	// Close the terminals last, after the run is saved: when the task session's own Stop hook fails the run, closing
	// its terminal ends the hook process, and an unsaved state would be resumed by the next pass.
	defer func() {
		_ = r.Store.PutRun(run)
		r.closeSessions(run)
	}()
	total := r.endSessions(run)
	data := map[string]any{"mode": run.EvalMode, "outcome": outcome, "gate_blocks": run.GateBlocksTotal, "ci_rounds": run.CIRounds,
		"findings": run.FindingsTotal, "findings_fixed": run.FindingsFixed, "head": run.Head, "history": run.History, "usd_all_sessions": round2(total)}
	if run.LastGate != nil {
		data["gate_problems"] = run.LastGate.Problems
		data["review_findings"] = run.LastGate.Findings
	}
	if len(run.Verdicts) > 0 {
		v := run.Verdicts[len(run.Verdicts)-1]
		data["verdict"], data["reasons"], data["changes"] = v.Verdict, v.Reasons, v.Changes
		if v.JudgeVerdict != "" {
			data["judge_verdict"] = v.JudgeVerdict
		}
	}
	_ = r.Log.Emit("eval.done", run, Event{Data: data})
	_ = r.Store.TaskStatus(run.Task, "cancelled", "")
	if run.EvalMode == EvalJudgeOnly {
		_ = os.RemoveAll(filepath.Join(run.Worktree, ".hivemill"))
		_ = os.MkdirAll(filepath.Join(run.Worktree, ".hivemill"), 0o755)
		return
	}
	if run.PRURL != "" {
		r.Tools.GH("", "label", "create", "eval", "-R", run.Repo, "--force", "--description", "hivemill eval case: never merged")
		r.Tools.GH("", "pr", "edit", run.PRURL, "--add-label", "eval")
		r.Tools.GH("", "pr", "close", run.PRURL, "--delete-branch", "--comment", "hivemill eval case: closed unmerged by design.")
	}
}
