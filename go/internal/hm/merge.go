package hm

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"factory/go/internal/citools/risktier"
)

// MergeInput is everything the merge decision reads. Merge decides from it alone (fixtures: testdata/merge_cases.json).
type MergeInput struct {
	PRState        string   `json:"pr_state"` // OPEN | MERGED | CLOSED
	Draft          bool     `json:"draft"`
	Mergeable      string   `json:"mergeable"` // MERGEABLE | CONFLICTING | UNKNOWN
	Head           string   `json:"head"`
	LaneVerdict    string   `json:"lane_verdict"` // newest task-judge verdict on Head: pass | fail | ""
	CI             string   `json:"ci"`           // green | red | pending
	MissingChecks  []string `json:"missing_checks"`
	Tier           string   `json:"tier"` // T0..T3, "" = cannot classify
	Behind         bool     `json:"behind"`
	MergeClean     bool     `json:"merge_clean"`
	BehindOK       bool     `json:"behind_ok"` // stale_policy behind_ok holds: merge behind main without a refresh (docs/stale-policy.md)
	Frozen         bool     `json:"frozen"`
	RedMain        bool     `json:"red_main"`
	FixTask        bool     `json:"fix_task"` // this task is the named red-main fix task
	MainRun        string   `json:"main_run"` // newest main push run for main's head: green | red | pending | none | unknown
	IdentityOK     bool     `json:"identity_ok"`
	PublicTextOK   bool     `json:"public_text_ok"`
	PublicTextNote string   `json:"public_text_note,omitempty"`
	Train          bool     `json:"train,omitempty"` // this merge may go out while main's push run is pending
	Files          []string `json:"files,omitempty"` // the files the PR changes, for the merge train
}

// MergeDecision is what to do now.
type MergeDecision struct {
	Action string `json:"action"` // merge | done | hold | refresh | resume | rejudge | block
	Why    string `json:"why"`
}

// MaxMechTier is the highest risk tier the runner merges without the owner.
const MaxMechTier = "T2"

// Decide reproduces the safety checks of the old mechanical merger: open, not draft, not frozen, a judge MERGE on this
// exact head, no conflict, fresh against main, green and complete checks, tier at most T2, main not red (unless this is
// the fix task), main's own push run finished green (or the merge train allows a pending one), noreply identity and clean
// public text.
func Decide(in MergeInput) MergeDecision {
	switch {
	case in.PRState == "MERGED":
		return MergeDecision{"done", "the PR is merged"}
	case in.PRState != "OPEN":
		return MergeDecision{"block", "the PR is " + strings.ToLower(in.PRState)}
	case in.Draft:
		return MergeDecision{"block", "the PR is a draft"}
	case in.Frozen:
		return MergeDecision{"block", "the repo is frozen (frozen_repos)"}
	case in.LaneVerdict == "fail":
		return MergeDecision{"block", "the judge rejected this head"}
	case in.LaneVerdict != "pass":
		return MergeDecision{"rejudge", fmt.Sprintf("no judge MERGE on head %.8s (the head moved after the verdict)", in.Head)}
	case in.Mergeable == "CONFLICTING" || (in.Behind && !in.MergeClean):
		return MergeDecision{"resume", "the branch conflicts with the base branch"}
	case in.Behind && !in.BehindOK:
		return MergeDecision{"refresh", "the head does not contain the base branch; a merge of it is clean"}
	case in.CI == "red":
		return MergeDecision{"resume", ciRed}
	case in.CI != "green":
		return MergeDecision{"hold", ciPending}
	case len(in.MissingChecks) > 0:
		return MergeDecision{"hold", "required checks missing: " + strings.Join(in.MissingChecks, ", ")}
	case in.Tier == "":
		return MergeDecision{"block", "the risk tier cannot be decided; the owner merges"}
	case tierAbove(in.Tier, MaxMechTier):
		return MergeDecision{"block", "tier " + in.Tier + " is above " + MaxMechTier + "; the owner merges"}
	case in.RedMain && !in.FixTask:
		return MergeDecision{"hold", "main is red; merges wait for a green push run"}
	case (in.MainRun == "pending" && !in.Train) || in.MainRun == "unknown":
		return MergeDecision{"hold", "main's push run for its head is " + in.MainRun}
	case in.MainRun == "red" && !in.FixTask:
		return MergeDecision{"hold", "main's push run for its head failed"}
	case !in.IdentityOK:
		return MergeDecision{"block", "a commit is not authored and committed with the configured noreply_email"}
	case !in.PublicTextOK:
		return MergeDecision{"block", "public text: " + in.PublicTextNote}
	}
	return MergeDecision{"merge", "all checks pass"}
}

func tierAbove(t, max string) bool { return t > max } // T0 < T1 < T2 < T3 sorts as text

// gatherMerge reads the PR, git and GitHub state for Decide.
func (r *Runner) gatherMerge(run *Run) (MergeInput, *PR, error) {
	in := MergeInput{}
	pr, err := r.prView(run)
	if err != nil {
		return in, nil, err
	}
	in.PRState, in.Draft, in.Mergeable, in.Head = pr.State, pr.IsDraft, pr.Mergeable, pr.HeadRefOid
	if pr.State != "OPEN" {
		return in, pr, nil
	}
	in.LaneVerdict, _, _ = r.Store.LaneVerdict(run.Task, pr.HeadRefOid)
	none, err := r.ciMode(run.Project)
	if err != nil {
		return in, nil, err
	}
	if none {
		in.CI, _ = run.LocalCI.verdict(pr.HeadRefOid) // read only: Merge runs gate_cmd, the dry run never does
	} else {
		in.CI, _ = CIVerdict(pr.Checks)
		_, in.MissingChecks = r.missingChecks(run.Task, pr.Checks) // required checks are PR checks
	}
	t, _ := r.Store.Task(run.Task)
	slug := strings.ToLower(run.Repo)
	for _, f := range csv(r.Store.FactoryCfg("frozen_repos")) {
		in.Frozen = in.Frozen || strings.EqualFold(f, slug)
	}
	if st, red := r.Store.RedMain(slug); red && r.Store.FactoryCfg("red_main_freeze") == "1" {
		in.RedMain = true
		in.FixTask = fmt.Sprint(st["task"]) == run.Task
	}
	wt, base := run.Worktree, run.Base
	r.Tools.Git(wt, "fetch", "-q", "origin", base)
	_, local, _ := r.Tools.Git(wt, "rev-parse", "HEAD")
	if local != pr.HeadRefOid {
		r.Tools.Git(wt, "fetch", "-q", "origin", run.Branch)
		r.Tools.Git(wt, "checkout", "-q", "-B", run.Branch, pr.HeadRefOid)
	}
	if rc, _, _ := r.Tools.Git(wt, "merge-base", "--is-ancestor", "origin/"+base, pr.HeadRefOid); rc != 0 {
		in.Behind = true
		rc, _, _ := r.Tools.Git(wt, "merge-tree", "--write-tree", "origin/"+base, pr.HeadRefOid)
		in.MergeClean = rc == 0
		if in.MergeClean && pr.Mergeable == "MERGEABLE" {
			in.BehindOK = r.behindOK(run.Repo, wt, base, pr.HeadRefOid)
		}
	}
	if t != nil {
		in.Tier = r.tierOf(t.Project, wt, base, pr.HeadRefOid)
	}
	if rc, out, _ := r.Tools.Git(wt, "diff", "--name-only", "--no-renames", "origin/"+base+"..."+pr.HeadRefOid); rc == 0 {
		in.Files = strings.Fields(out)
	}
	_, mainHead, _ := r.Tools.Git(wt, "rev-parse", "origin/"+base)
	in.MainRun, in.Train = r.trainState(run.Repo, base, strings.TrimSpace(mainHead), r.mainRun(slug, wt, base), in.Files)
	in.IdentityOK = true
	_, logs, _ := r.Tools.Git(wt, "log", "--format=%ae%x09%ce", "origin/"+base+".."+pr.HeadRefOid)
	for _, ln := range strings.Split(logs, "\n") {
		if f := strings.Split(ln, "\t"); len(f) == 2 && (!r.isNoreply(f[0]) || !r.isNoreply(f[1])) {
			in.IdentityOK = false
		}
	}
	in.PublicTextOK, in.PublicTextNote = r.publicText(wt, base, pr)
	return in, pr, nil
}

func (r *Runner) projectRequired(project string) string {
	if s, ok := r.Store.(*SQLite); ok {
		var v string
		_ = s.db.QueryRow(`SELECT required_checks FROM projects WHERE id=?`, project).Scan(&v)
		return v
	}
	return ""
}

func (r *Runner) tierOf(project, wt, base, head string) string {
	rules, _, err := r.TierRules(project)
	if err != nil {
		return "" // cannot decide: Decide blocks, never merges
	}
	tier, _, err := risktier.ClassifyRange(wt, "origin/"+base, head, rules)
	if err != nil {
		return ""
	}
	return tier
}

// mainRun is the newest tests.yml push run on main for main's current head: green, red, pending, none (no such
// workflow in the repo) or unknown (gh failed).
func (r *Runner) mainRun(slug, wt, base string) string {
	_, head, _ := r.Tools.Git(wt, "rev-parse", "origin/"+base)
	if r.poll != nil {
		b := r.poll.bases[baseKey(slug, base)]
		if b == nil {
			return "unknown"
		}
		return b.mainState(strings.TrimSpace(head))
	}
	rc, out, errs := r.Tools.GH("", "run", "list", "-R", slug, "--workflow", "tests.yml", "--branch", base, "--event", "push",
		"--limit", "10", "--json", "status,conclusion,headSha")
	if rc != 0 {
		if strings.Contains(strings.ToLower(errs), "not found") {
			return "none"
		}
		return "unknown"
	}
	var runs []struct{ Status, Conclusion, HeadSha string }
	if json.Unmarshal([]byte(out), &runs) != nil {
		return "unknown"
	}
	for _, x := range runs {
		if x.HeadSha != head {
			continue
		}
		if x.Status != "completed" {
			return "pending"
		}
		if x.Conclusion == "success" {
			return "green"
		}
		if x.Conclusion == "cancelled" || x.Conclusion == "skipped" {
			continue
		}
		return "red"
	}
	if len(runs) == 0 {
		return "none"
	}
	return "pending"
}

// Merge is one merge attempt of a run in state merging.
func (r *Runner) Merge(run *Run) {
	if run.Eval {
		run.MergeHold = "eval run: never merged"
		r.fail(run, "eval run reached the merge step: refused, eval runs are never merged")
		return
	}
	in, pr, err := r.gatherMerge(run)
	if err != nil {
		run.MergeHold = err.Error()
		return
	}
	d := Decide(in)
	run.MergeHold = d.Action + ": " + d.Why
	run.mergeStage = mergeStageOf(in, d)
	if pr.State == "OPEN" && r.ciNone(run.Project) {
		if d.Why == ciPending {
			// a new head (a refresh, a revert): run gate_cmd now and decide on the next pass from fresh PR data
			if err := r.localCI(run, in.Head); err != nil {
				run.MergeHold = err.Error()
				return
			}
		}
		r.postLocalCI(run)
		if d.Why == ciPending {
			return
		}
	}
	switch d.Action {
	case "done":
		r.finish(run, pr)
	case "hold":
	case "block":
		if OwnerMerges(d.Why) {
			r.hold(run, d.Why)
			return
		}
		r.fail(run, "merge blocked: "+d.Why)
	case "rejudge":
		run.Head, run.State = in.Head, Gated
	case "resume":
		if d.Why == ciRed && r.ciNone(run.Project) && run.LocalCI != nil {
			_, failed := run.LocalCI.verdict(in.Head)
			r.redCI(run, failed)
			return
		}
		if d.Why == ciRed && !r.ciNone(run.Project) {
			if _, failed := ciRows(pr.Checks); r.flakyRerun(run, in.Head, failed) {
				run.MergeHold, run.mergeStage = "re-running failed jobs that passed on "+run.Base+" in the last 24 h", flowStageCI
				return
			}
		}
		run.History = append(run.History, fmt.Sprintf("round %d: %s", run.Round, d.Why))
		r.resume(run, d.Why+". Fix it (`git fetch origin && git merge origin/"+run.Base+"` for a conflict), "+r.pushAndStop(run.Project)+".")
	case "refresh":
		if r.queueSlow {
			run.MergeHold = "refresh: held while a PR waits for a CI runner over its budget"
			return
		}
		r.refresh(run)
	case "merge":
		// kept on the run: a merge confirmed by a later pass joins the train too
		run.Car, run.Rode = &trainCar{Files: in.Files}, in.MainRun == "pending"
		r.mergeNow(run, in.Head)
		if run.State == Merged {
			_ = r.Store.PutRun(run) // before the next run of the pass reads the train
		}
	}
}

// mergeStageOf is the flow state a merge decision leaves the run in.
func mergeStageOf(in MergeInput, d MergeDecision) string {
	switch {
	case d.Why == ciPending || (d.Action == "hold" && len(in.MissingChecks) > 0 && in.CI == "green"):
		return flowStageCI
	case d.Action == "refresh":
		return FlowRefresh
	case d.Action == "hold":
		return FlowMainWait
	}
	return FlowMergeReady
}

// MergeTask is `hivemill merge T`: one merge step under the task lock, so no pass or stop works on the run meanwhile.
func (r *Runner) MergeTask(task string) (*Run, error) {
	l := r.lock(task, false)
	if l == nil {
		return nil, fmt.Errorf("%w: %s: another step of the task is running", ErrRefused, task)
	}
	defer l.Close()
	run, err := r.Store.Run(task)
	if err != nil || run == nil {
		return nil, fmt.Errorf("no run for %s", task)
	}
	if run.State != Merging {
		return nil, fmt.Errorf("%w: %s is %s, not merging", ErrRefused, task, run.State)
	}
	r.Merge(run)
	return run, r.Store.PutRun(run)
}

// OwnerMerges reports whether a merge block only means the owner merges by hand (tier above the mechanical maximum
// or an unclassifiable diff), as opposed to a defect.
func OwnerMerges(why string) bool { return strings.HasSuffix(why, "the owner merges") }

// hold parks a judged run for the owner: the terminals close, the task waits on the owner, and each pass checks the PR
// once so a merge by hand finishes the run.
func (r *Runner) hold(run *Run, why string) {
	run.State, run.Outcome, run.MergeHold = Held, "held for owner", why
	r.Close(run.Agent)
	for _, j := range run.Judges {
		r.Close(j)
	}
	_ = r.Store.TaskStatus(run.Task, "blocked", "owner")
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-held", why)
	_ = r.Log.Emit("run.held", run, Event{Data: map[string]any{"why": why, "pr": run.PRURL}})
}

// heldForOwner: a held run, or a run failed by the older binary on an owner-only merge block.
func heldForOwner(x *Run) bool {
	return x.State == Held || (x.State == Failed && strings.HasPrefix(x.FailReason, "merge blocked: ") && OwnerMerges(x.FailReason))
}

// checkHeld finishes a held run whose PR the owner merged, and fails one whose PR was closed.
func (r *Runner) checkHeld(run *Run) {
	if run.PRURL == "" {
		return
	}
	pr, err := r.prView(run)
	if err != nil {
		return
	}
	switch pr.State {
	case "MERGED":
		_ = r.Log.Emit("run.reconciled", run, Event{Data: map[string]any{"repaired": []string{"held PR merged by the owner"}}})
		r.finish(run, pr)
	case "CLOSED":
		r.fail(run, "the owner closed the held PR")
	}
}

// refresh merges origin/<base> into the branch (a merge commit, never a force-push), pushes it and carries the judge's
// MERGE to the new head: the judged change is the same, and CI must go green again before the merge.
func (r *Runner) refresh(run *Run) {
	wt, base := run.Worktree, run.Base
	email := r.Store.FactoryCfg("noreply_email")
	if rc, _, errs := gitAs(r.Env, email, wt, "merge", "--no-edit", "origin/"+base); rc != 0 {
		r.Tools.Git(wt, "merge", "--abort")
		run.MergeHold = "refresh failed: " + firstLine(errs)
		return
	}
	old := run.Head
	if rc, _, errs := r.Tools.Git(wt, "push", "-q", "origin", "HEAD:"+run.Branch); rc != 0 {
		run.MergeHold = "refresh push failed: " + firstLine(errs)
		return
	}
	_, run.Head, _ = r.Tools.Git(wt, "rev-parse", "HEAD")
	run.JudgeHead = run.Head
	run.Refreshes++
	_ = r.Store.Verdict(run.Task, run.PRURL, run.Head, "pass", fmt.Sprintf("carried from %.12s: clean merge of origin/%s only", old, base), "hivemill")
	_ = r.Log.Emit("pr.refreshed", run, Event{Data: map[string]any{"head": run.Head, "from": old}})
}

func nameOf(email string) string {
	if i, j := strings.Index(email, "+"), strings.Index(email, "@"); i >= 0 && j > i {
		return email[i+1 : j]
	}
	return "hivemill"
}

// mergeNow merges under the merge lock with --match-head-commit, so a push after the checks cannot slip in.
func (r *Runner) mergeNow(run *Run, sha string) {
	l := r.lock("merge", true)
	if l != nil {
		defer l.Close()
	}
	rc, _, errs := r.Tools.GH("", "pr", "merge", run.PRURL, "--squash", "--match-head-commit", sha)
	if rc != 0 {
		run.Car = nil
		run.MergeHold = "gh pr merge failed: " + firstLine(errs)
		return
	}
	pr, err := PRView(r.Tools, run.PRURL)
	if err != nil || pr.State != "MERGED" {
		run.MergeHold = "merge not confirmed yet"
		return
	}
	r.finish(run, pr)
}

func (r *Runner) finish(run *Run, pr *PR) {
	if pr.MergeCommit != nil {
		run.MergeSHA = pr.MergeCommit.Oid
	}
	if run.Car != nil {
		r.joinTrain(run, run.Car.Files, run.Rode)
		run.Car = nil
	}
	run.State, run.Outcome, run.Merged, run.Ended, run.MergeHold = Merged, "merged", or(pr.MergedAt, nowISO()), nowISO(), ""
	_ = r.Store.TaskStatus(run.Task, "done", "")
	_ = r.Store.Event(r.actor(), "task", run.Task, "merged", fmt.Sprintf("@%.8s by=hivemill: task-judge MERGE, mechanical merge", run.MergeSHA))
	_ = r.Log.Emit("pr.merged", run, Event{Data: map[string]any{"merge_sha": run.MergeSHA, "pr": run.PRURL, "merged_at": run.Merged}})
	r.checkMergeCommit(run)
	r.closeSessions(run)
	r.endSessions(run)
	r.GoalCheck(run)
}

// GoalCheck runs the goal's cmd: checks (its success criteria, same proof format) on merged main when this was the
// goal's last open task; a goal without one is recorded as skipped.
func (r *Runner) GoalCheck(run *Run) {
	open, crit, err := r.Store.GoalOpenTasks(run.Goal)
	if err != nil || open > 0 {
		return
	}
	var checks []Criterion
	for _, c := range Criteria(crit) {
		if c.Kind == "cmd" {
			checks = append(checks, c)
		}
	}
	if len(checks) == 0 {
		run.GoalCheck = "skipped: the goal defines no cmd: check"
		_ = r.Log.Emit("goal.checked", run, Event{Data: map[string]any{"result": "skipped", "why": "no cmd: check in the goal's success criteria"}})
		return
	}
	r.Tools.Git(run.Worktree, "fetch", "-q", "origin", run.Base)
	r.Tools.Git(run.Worktree, "checkout", "-q", "--detach", "origin/"+run.Base)
	dir := filepath.Join(r.Env.StateDir(), "runs", run.Task)
	_ = os.MkdirAll(dir, 0o700)
	res := r.RerunProofs(run, checks, dir)
	ok := true
	for _, x := range res {
		ok = ok && x.OK != nil && *x.OK
	}
	run.GoalCheck = map[bool]string{true: "pass", false: "fail"}[ok]
	_ = r.Log.Emit("goal.checked", run, Event{Data: map[string]any{"result": run.GoalCheck, "checks": res}})
}

// RevertCheck (auto_revert=on): a runner merge that is the head of a failed main push run, and the newest runner merge
// in its repo, is reverted through a PR named the red-main fix task. Anything else stays with the bisect task the
// factory's red-main monitor files.
func (r *Runner) RevertCheck() error {
	if r.Store.Cfg("auto_revert") != "on" {
		return nil
	}
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	for i := range runs {
		x := &runs[i]
		if x.State != Merged || x.MergeSHA == "" || x.RevertedBy != "" {
			continue
		}
		st, red := r.Store.RedMain(strings.ToLower(x.Repo))
		if !red || r.culprit(x.Repo, x.Base, fmt.Sprint(st["sha"])) != x.MergeSHA {
			continue
		}
		newer := false
		for _, y := range runs {
			newer = newer || (y.Repo == x.Repo && y.State == Merged && y.Merged > x.Merged && !y.Rode) // the train shares no file
		}
		if newer {
			continue
		}
		id, err := r.openRevert(x, st)
		if err != nil {
			return err
		}
		x.State, x.Outcome, x.RevertedBy = Reverted, "reverted", id
		_ = r.Store.PutRun(x)
		_ = r.Log.Emit("pr.reverted", x, Event{Data: map[string]any{"revert_task": id, "merge_sha": x.MergeSHA}})
	}
	return nil
}

func (r *Runner) openRevert(x *Run, st map[string]any) (string, error) {
	title := fmt.Sprintf("Revert %s: main red after its merge", x.Task)
	if old, _ := r.Store.TaskByTitle(x.Goal, title); old != "" {
		return old, nil // idempotent: a pass that crashed after filing the revert task does not file a second one
	}
	id, err := r.Store.NewTask(x.Goal, title, fmt.Sprintf("Mechanical revert of %.12s (%s): main's push run %v failed on it.", x.MergeSHA, x.PRURL, st["run"]),
		"1. main is green again - manual: the red-main monitor clears it")
	if err != nil {
		return "", err
	}
	t, err := r.Store.Task(id)
	if err != nil {
		return "", err
	}
	branch := "factory/" + strings.ToLower(id)
	wt, err := r.Clone(t, branch)
	if err != nil {
		return "", err
	}
	email := r.Store.FactoryCfg("noreply_email")
	if rc, _, errs := gitAs(r.Env, email, wt, "revert", "--no-edit", x.MergeSHA); rc != 0 {
		return "", fmt.Errorf("git revert %.12s: %s", x.MergeSHA, firstLine(errs))
	}
	if rc, _, errs := r.Tools.Git(wt, "push", "-q", "origin", "HEAD:"+branch); rc != 0 {
		return "", fmt.Errorf("push %s: %s", branch, firstLine(errs))
	}
	body := fmt.Sprintf("## Summary\nReverts %s (%s): main's push run %v failed on its merge commit.\n\n## Why\n%s: mechanical revert by "+
		"`hivemill` (auto_revert=on).\n\n## Changes\n- `git revert %.12s`\n\n## Test evidence\n`git revert --no-edit` -> exit 0; CI on this "+
		"PR decides.\n\n## Risk / rollback\nRe-land the original PR after a fix.\n\n<details><summary>Acceptance walk</summary>\n\n"+
		"## Acceptance walk\n1. main green again - manual\n</details>\n", x.Task, x.PRURL, st["run"], id, x.MergeSHA)
	rc, out, errs := r.Tools.GH(body, "pr", "create", "-R", x.Repo, "--head", branch, "--base", x.Base, "--title",
		truncate(id+": "+title, 72), "--body-file", "-")
	if rc != 0 {
		return "", fmt.Errorf("gh pr create: %s", firstLine(errs))
	}
	url := strings.TrimSpace(out[strings.LastIndex(strings.TrimSpace(out), "\n")+1:])
	_, head, _ := r.Tools.Git(wt, "rev-parse", "HEAD")
	_ = r.Store.TaskPR(id, url, "")
	_ = r.Store.TaskStatus(id, "in_review", "")
	_ = r.Store.SetRedMainFix(strings.ToLower(x.Repo), id)
	marker := "claude-task-judge-" + strings.ToLower(id) + "-revert" // the runner_managed marker the old pipeline respects
	_ = r.Store.AgentLive(marker, "task-judge", id, x.Goal, "", "", "", wt, `{"lane":"option-b","kind":"revert"}`)
	_ = r.Store.AgentEnded(marker)
	_ = r.Store.Verdict(id, url, head, "pass", "mechanical revert of a red-main merge", "hivemill")
	rev := &Run{Task: id, Goal: x.Goal, Project: x.Project, Repo: x.Repo, Base: x.Base, Worktree: wt, Branch: branch, State: Merging,
		Round: 1, Kind: "revert", PRURL: url, Head: head, JudgeHead: head, Dispatched: nowISO(),
		Verdicts: []Verdict{{Verdict: "MERGE", Reasons: []string{"mechanical revert of a red-main merge"}, TS: nowISO()}}}
	_ = r.Store.Event(r.actor(), "redmain", strings.ToLower(x.Repo), "red-main-fix-task", "-> "+id+" (hivemill revert)")
	return id, r.Store.PutRun(rev)
}

// MergeDecision reads the merge input of a run and decides, without acting (`hivemill merge T --dry-run`).
func (r *Runner) MergeDecision(run *Run) (MergeInput, MergeDecision, error) {
	in, _, err := r.gatherMerge(run)
	if err != nil {
		return in, MergeDecision{}, err
	}
	return in, Decide(in), nil
}

// checkMergeCommit verifies, once the merge commit has landed, that its author is the configured noreply_email and
// its committer is that address or GitHub's own web-flow address (GitHub commits squash merges). A violation cannot be
// undone by the runner: it records an event and opens an owner alert.
func (r *Runner) checkMergeCommit(run *Run) {
	if run.MergeSHA == "" {
		return
	}
	rc, out, errs := r.Tools.GH("", "api", "repos/"+run.Repo+"/commits/"+run.MergeSHA, "--jq", `.commit.author.email + " " + .commit.committer.email`)
	why := ""
	if f := strings.Fields(out); rc != 0 || len(f) != 2 {
		why = "cannot read the merge commit: " + firstLine(errs)
	} else if !r.isNoreply(f[0]) || !(r.isNoreply(f[1]) || f[1] == "noreply@github.com") {
		why = fmt.Sprintf("merge commit %.9s has author %s, committer %s", run.MergeSHA, f[0], f[1])
	}
	if why == "" {
		return
	}
	_ = r.Store.Event(r.actor(), "task", run.Task, "noreply-violation", why)
	_ = r.Store.AlertFeed("hivemill-noreply", run.Task, run.PRURL)
	_ = r.Log.Emit("pr.noreply_violation", run, Event{Data: map[string]any{"why": why}})
}

// behindOK is the Go port of the old chain's stale_policy=behind_ok (T-1676, docs/stale-policy.md): a clean, MERGEABLE
// head that is behind main merges without a refresh when GitHub's protection of the base is not strict, no workflow
// file changed on main since the merge-base, and no file changed on main since the merge-base is also changed by the PR.
// A refresh pushes a merge commit and restarts the PR's CI, so with several PRs waiting every merge restarted all of
// them (2026-11-26: PRs sat 2 h in merging behind a full Actions queue). Every doubt answers false (refresh as before).
func (r *Runner) behindOK(repo, wt, base, head string) bool {
	if strings.EqualFold(r.Store.FactoryCfg("stale_policy"), "strict") {
		return false
	}
	if !r.protectionNotStrict(repo, base) {
		return false
	}
	rc, mb, _ := r.Tools.Git(wt, "merge-base", "origin/"+base, head)
	mb = strings.TrimSpace(mb)
	if rc != 0 || mb == "" {
		return false
	}
	rc1, mainFiles, _ := r.Tools.Git(wt, "diff", "--name-only", "--no-renames", mb, "origin/"+base)
	rc2, prFiles, _ := r.Tools.Git(wt, "diff", "--name-only", "--no-renames", mb, head)
	if rc1 != 0 || rc2 != 0 {
		return false
	}
	return behindSafe(strings.Fields(mainFiles), strings.Fields(prFiles))
}

// behindSafe: no workflow file changed on main and no file changed on both sides.
func behindSafe(mainFiles, prFiles []string) bool {
	pr := map[string]bool{}
	for _, f := range prFiles {
		pr[f] = true
	}
	for _, f := range mainFiles {
		if strings.HasPrefix(f, ".github/workflows/") || pr[f] {
			return false
		}
	}
	return true
}

// protectionNotStrict reads required_status_checks.strict of the base branch, cached 10 minutes; unknown = strict.
func (r *Runner) protectionNotStrict(repo, base string) bool {
	if r.poll != nil {
		b := r.poll.bases[baseKey(repo, base)]
		return b != nil && b.NotStrict
	}
	key := "stale_strict:" + strings.ToLower(repo) + ":" + base
	if v := r.Store.Meta(key); v != "" {
		if f := strings.SplitN(v, " ", 2); len(f) == 2 {
			if t, err := time.Parse(time.RFC3339, f[1]); err == nil && time.Since(t) < 10*time.Minute {
				return f[0] == "false"
			}
		}
	}
	rc, out, _ := r.Tools.GH("", "api", "repos/"+repo+"/branches/"+base+"/protection", "--jq", ".required_status_checks.strict")
	v := strings.TrimSpace(out)
	if rc != 0 || (v != "false" && v != "true") {
		return false
	}
	_ = r.Store.SetMeta(key, v+" "+time.Now().UTC().Format(time.RFC3339))
	return v == "false"
}
