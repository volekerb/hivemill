package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Run states.
const (
	Working   = "working"
	Gated     = "gated"
	Judging   = "judging"
	Judged    = "judged"
	Merging   = "merging"
	Merged    = "merged"
	Failed    = "failed"
	Reverted  = "reverted"
	Cancelled = "cancelled"
	Held      = "held" // judged MERGE but above the mechanical tier: the owner merges; not a failure, holds no slot
)

// Active reports whether a run still holds a slot.
func Active(state string) bool {
	switch state {
	case Working, Gated, Judging, Judged, Merging:
		return true
	}
	return false
}

// Runner ties the store, the tools and the event log together.
type Runner struct {
	Env   Env
	Store Store
	Tools Tools
	Log   Log
	Out   io.Writer
	Poll  time.Duration
	// ReadyWait bounds the wait for a new session's TUI (0 = 90 s).
	ReadyWait time.Duration
	// RemoteUsage reads the usage file of a session machine (nil = the local reading only).
	RemoteUsage func(machine string) ([]byte, error)
	usageFiles  [][]byte // the remote usage files of this pass
	usageRead   bool     // usageFiles has been read in this pass
}

// NewRunner opens the factory DB of the environment.
func NewRunner(e Env, out io.Writer) (*Runner, error) {
	s, err := OpenSQLite(e.DBPath())
	if err != nil {
		return nil, err
	}
	return &Runner{Env: e, Store: s, Tools: RealTools(e), Out: out, Poll: 2 * time.Second, RemoteUsage: readRemoteUsage,
		Log: Log{Path: filepath.Join(e.StateDir(), "events.jsonl"), Machine: s.Cfg("machine_name")}}, nil
}

func (r *Runner) actor() string {
	if r.Env.Actor != "" {
		return r.Env.Actor
	}
	return "owner"
}

func (r *Runner) printf(f string, a ...any) { fmt.Fprintf(r.Out, f, a...) }

// lock takes the per-task lock file; wait=false returns nil while another process (a gate hook) holds it.
func (r *Runner) lock(task string, wait bool) *os.File {
	dir := filepath.Join(r.Env.StateDir(), "locks")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, task+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if syscall.Flock(int(f.Fd()), how) != nil {
		f.Close()
		return nil
	}
	return f
}

// ModelFor picks the session model: model_task / model_task-judge override, else risk_model or model. Fable is refused.
func (r *Runner) ModelFor(risky bool) (string, string, error) {
	m := r.Store.Cfg("model")
	if risky {
		m = r.Store.Cfg("risk_model")
	}
	if !strings.HasPrefix(m, "claude-") || strings.Contains(m, "fable") {
		return "", "", fmt.Errorf("model %q refused: use a claude-sonnet-* or claude-opus-* id (never Fable)", m)
	}
	eff := r.Store.Cfg("effort")
	switch eff {
	case "low", "medium", "high", "xhigh", "max":
	default:
		return "", "", fmt.Errorf("effort %q must be low, medium, high, xhigh or max", eff)
	}
	return m, eff, nil
}

func (r *Runner) activeCount() int {
	runs, _ := r.Store.Runs()
	n := 0
	for _, x := range runs {
		if Active(x.State) {
			n++
		}
	}
	return n
}

// Candidates are ready tasks a plain pass may start (config projects): deps done, spec set, spec gate passing.
func (r *Runner) Candidates() ([]Task, error) { return r.ReadyIn(csv(r.Store.Cfg("projects"))) }

// ReadyIn are the tasks of these projects a pass would start: todo, deps done, spec set, spec gate passing, and never run
// before (a task with any run, failed or cancelled included, is not started again: file a new one).
func (r *Runner) ReadyIn(projects []string) ([]Task, error) {
	ts, err := r.Store.ReadyTasks(projects)
	if err != nil || len(ts) == 0 {
		return nil, err
	}
	done, err := r.Store.DoneTasks()
	if err != nil {
		return nil, err
	}
	var out []Task
	for _, t := range ts {
		var deps []string
		_ = json.Unmarshal([]byte(t.DependsOn), &deps)
		ok := strings.TrimSpace(t.Spec) != "" && len(LintProblems(t.Acceptance)) == 0
		for _, d := range deps {
			ok = ok && done[d]
		}
		if old, _ := r.Store.Run(t.ID); old != nil {
			ok = false
		}
		if ok {
			out = append(out, t)
		}
	}
	return out, nil
}

// ErrRefused is a start the runner refuses (exit 3).
var ErrRefused = errors.New("refused")

// Start creates the clone and launches the task session for one task.
func (r *Runner) Start(id string) error {
	t, err := r.Store.Task(id)
	if err != nil {
		return err
	}
	if t.Status != "todo" && t.Status != "in_progress" {
		return fmt.Errorf("%w: %s is %s; the runner starts todo or in_progress tasks", ErrRefused, id, t.Status)
	}
	if probs := LintProblems(t.Acceptance); len(probs) > 0 {
		return fmt.Errorf("%w: %s fails the spec gate (`hivemill task lint %s`):\n  - %s", ErrRefused, id, id, strings.Join(probs, "\n  - "))
	}
	if old, _ := r.Store.Run(id); old != nil && Active(old.State) {
		return fmt.Errorf("%w: %s already has an active run (%s)", ErrRefused, id, old.State)
	}
	if limit := cfgInt(r.Store, "max_concurrent"); r.activeCount() >= limit {
		return fmt.Errorf("%w: max_concurrent=%d runs are active", ErrRefused, limit)
	}
	if r.Draining() {
		return fmt.Errorf("%w: drain mode is on (`hivemill resume` lifts it)", ErrRefused)
	}
	caps := r.ReadCaps(time.Now())
	if why := caps.DispatchBlock(r.Store, t.Project); why != "" {
		r.budgetEvent(why)
		r.usageAlert(caps, why)
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	if caps.Probe {
		_ = r.Log.Emit("usage.probe", nil, Event{TaskID: id, Data: map[string]any{"why": caps.UsageUnknown(r.Store),
			"note": "one run starts to refresh the stale usage reading; no second run until it is fresh"}})
	} else {
		_ = r.Store.SetMeta("usage_alerted", "")
	}
	if live, _ := r.Store.OldPipelineLive(id); len(live) > 0 {
		return fmt.Errorf("%w: %s has live agents of the old pipeline (%s); stop them first", ErrRefused, id, strings.Join(live, ", "))
	}
	if t.RepoPath == "" || t.GHRepo == "" {
		return fmt.Errorf("%w: %s: its project has no repo_path or gh_repo", ErrRefused, id)
	}
	risky, why := Risk(r.Env.Home, t, nil)
	model, effort, err := r.ModelFor(risky)
	if err != nil {
		return err
	}
	branch := t.Branch
	if branch == "" {
		branch = "factory/" + strings.ToLower(id)
	}
	var pl Placement
	if caps.Probe { // the probe refreshes the local reading, which needs no remote read to be seen
		pl.Skipped = []string{"the usage probe runs only on the local core"}
	} else {
		pl = r.PickMachine()
	}
	wt, err := r.Clone(t, branch)
	if err != nil {
		return err
	}
	if pl.Machine != "" {
		if why := r.cloneOn(pl.Machine, wt); why != "" {
			pl.Skipped, pl.Machine = append(pl.Skipped, pl.Machine+": "+why), ""
		}
	}
	if pl.Machine == "" && placementOn(r.Store) && caps.MemMB >= 0 && caps.MemMB < caps.MemMin {
		// DispatchBlock leaves the local floor to here when placement is on: no worker took it, so it runs here
		return fmt.Errorf("%w: memory: %d MB available, floor %d MB, and no session machine took it (%s)", ErrRefused, caps.MemMB,
			caps.MemMin, strings.Join(pl.Skipped, "; "))
	}
	// A reused clone may hold the review files of an earlier run of this task: they would make the gate treat the new
	// session's first mid-work stop as a gate attempt, and they describe another head.
	for _, f := range []string{"findings.json", "evidence.json"} {
		_ = os.Remove(filepath.Join(wt, ".hivemill", f))
	}
	// The worker guard (unchanged) lets a pane push only HEAD:factory/<task> for FACTORY_AGENT
	// claude-coder-t-NNNN(-aN); the lane's own suffix -a7 keeps the name apart from old-pipeline coders.
	name := "claude-coder-" + strings.ToLower(id) + "-a7"
	run := &Run{Task: id, Goal: t.Goal, Project: t.Project, Repo: t.GHRepo, Base: t.Base, Worktree: wt, Branch: branch, Agent: name,
		State: Working, Round: 1, Risky: risky, RiskWhy: why, Model: model, Effort: effort, Dispatched: nowISO(), RoundStart: nowISO(),
		Criteria: len(Criteria(t.Acceptance)), Machine: pl.Machine, WorkflowAgents: workflowAgents(r.Store, t)}
	if err := r.Store.PutRun(run); err != nil {
		return err
	}
	_ = r.Log.Emit("task.dispatched", run, Event{Model: model, Effort: effort, Data: map[string]any{"risky": risky, "why": why,
		"machine": machineLabel(pl.Machine), "skipped": pl.Skipped, "workflow": run.WorkflowAgents > 0}})
	_ = r.Store.TaskStatus(id, "in_progress", "")
	_ = r.Store.Event(r.actor(), "task", id, "run-dispatch", map[string]any{"agent": name, "model": model, "risky": risky})
	if err := r.launchTask(run, Kickoff(run, t)); err != nil {
		return err
	}
	r.printf("RUN %s: %s (%s, effort %s%s) in %s on %s\n", id, name, model, effort, map[bool]string{true: ", risk: " + why}[risky], wt,
		machineLabel(pl.Machine))
	return nil
}

// launchTask starts the task session of a run in its clone, with the noreply identity and the scrub terms.
func (r *Runner) launchTask(run *Run, kickoff string) error {
	name, wt, model, effort := run.Agent, run.Worktree, run.Model, run.Effort
	env := map[string]string{}
	if email := r.Store.FactoryCfg("noreply_email"); email != "" {
		env["GIT_AUTHOR_EMAIL"], env["GIT_COMMITTER_EMAIL"] = email, email
		env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"] = nameOf(email), nameOf(email)
	}
	if terms := r.Store.FactoryCfg("scrub_terms_file"); terms != "" {
		env["SCRUB_TERMS_FILE"] = terms
	}
	if list, ok := Terminals(r.Tools); ok && alive(list, true, name) {
		r.Close(name) // a session left open by an earlier FAIL; its transcript stays resumable by session id
	}
	_, err := r.StartSession(run, Launch{Role: "task", Name: name, Cwd: wt, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "task.md"), Kickoff: kickoff, Env: env, Machine: run.Machine})
	if err != nil {
		r.fail(run, "the session did not start: "+err.Error())
		_ = r.Store.PutRun(run)
		return err
	}
	return nil
}

// Kickoff is the first message of a task session: the task itself, so the session needs no lookup.
func Kickoff(run *Run, t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are `%s`, the factory task. Your task is %s: %s.\n\nSpec:\n%s\n\nAcceptance criteria (number. criterion, then its proof):\n",
		run.Agent, t.ID, t.Title, strings.TrimSpace(t.Spec))
	for _, c := range Criteria(t.Acceptance) {
		// The whole criterion text, not only its proof: the text says what the proof must show (fixtures, exact strings).
		fmt.Fprintf(&b, "%d. %s\n", c.ID, c.Text)
	}
	if run.WorkflowAgents > 0 {
		fmt.Fprintf(&b, "\n%s\n", workflowRequest(run.WorkflowAgents))
	}
	fmt.Fprintf(&b, "\nBranch %s, base %s. Follow your role steps; stop when the PR is open and CI is green.", run.Branch, run.Base)
	return b.String()
}

// Clone makes (or reuses) the task's own clone under <home>/worktrees: objects shared with the project checkout,
// origin set to the project's own origin, branch from origin/<base>, the public-text pre-push hook installed and
// .hivemill/ ignored through the clone's .git/info/exclude.
func (r *Runner) Clone(t *Task, branch string) (string, error) {
	g := r.Tools.Git
	wt := filepath.Join(r.Env.Home, "worktrees", t.Goal+"-"+t.ID)
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err == nil && fi.IsDir() {
		if rc, _, errs := g(wt, "checkout", "-q", branch); rc != 0 {
			if rc, _, errs2 := g(wt, "checkout", "-q", "-B", branch, "origin/"+t.Base); rc != 0 {
				return "", fmt.Errorf("clone %s: cannot check out %s: %s %s", wt, branch, errs, errs2)
			}
		}
	} else {
		if _, err := os.Stat(wt); err == nil {
			return "", fmt.Errorf("%w: %s exists but is not a clone; remove it", ErrRefused, wt)
		}
		rc, url, errs := g(t.RepoPath, "remote", "get-url", "origin")
		if rc != 0 || url == "" {
			return "", fmt.Errorf("%s has no origin URL: %s", t.RepoPath, errs)
		}
		steps := [][]string{
			{t.RepoPath, "-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", t.RepoPath, wt},
			{wt, "remote", "set-url", "origin", url},
			{wt, "fetch", "-q", "origin"},
		}
		for _, s := range steps {
			if rc, _, errs := g(s[0], s[1:]...); rc != 0 {
				_ = os.RemoveAll(wt)
				return "", fmt.Errorf("clone %s: git %s: %s", wt, s[1], firstLine(errs))
			}
		}
		start := "origin/" + t.Base // a recreated clone continues the pushed branch, never restarts it
		if rc, _, _ := g(wt, "rev-parse", "--verify", "-q", "refs/remotes/origin/"+branch); rc == 0 {
			start = "origin/" + branch
		}
		if rc, _, errs := g(wt, "checkout", "-q", "-f", "-B", branch, start); rc != 0 {
			_ = os.RemoveAll(wt)
			return "", fmt.Errorf("clone %s: git checkout: %s", wt, firstLine(errs))
		}
	}
	if err := r.prepareClone(wt); err != nil {
		return "", err
	}
	if err := TrustClone(r.Env, wt); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: trust %s: %v (the trust dialog may show)\n", wt, err)
	}
	return wt, r.Store.TaskClone(t.ID, wt, branch)
}

func (r *Runner) prepareClone(wt string) error {
	gitDir := filepath.Join(wt, ".git")
	ex := filepath.Join(gitDir, "info", "exclude")
	_ = os.MkdirAll(filepath.Dir(ex), 0o755)
	b, _ := os.ReadFile(ex)
	if !strings.Contains(string(b), "/.hivemill/") {
		if err := os.WriteFile(ex, append(b, []byte("\n/.hivemill/\n")...), 0o644); err != nil {
			return err
		}
	}
	_ = os.MkdirAll(filepath.Join(wt, ".hivemill"), 0o755)
	hook, err := PrePushHook(r.Env.Home)
	if err != nil {
		return err
	}
	_ = os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755)
	return os.WriteFile(filepath.Join(gitDir, "hooks", "pre-push"), []byte(hook), 0o755)
}

func (r *Runner) fail(run *Run, reason string) {
	run.State, run.Outcome, run.FailReason, run.Ended = Failed, "failed", reason, nowISO()
	_ = r.Store.TaskStatus(run.Task, "blocked", "owner")
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-fail", truncate(reason, 900))
	if !run.Eval {
		r.endSessions(run) // the $ and tokens of a failed run reach the event log too (evalFinish writes its own)
	}
	_ = r.Log.Emit("task.failed", run, Event{Data: map[string]any{"reason": truncate(reason, 900)}})
	if run.Eval {
		r.evalFinish(run, Failed, "failed: "+truncate(reason, 300))
		return
	}
	_ = r.Store.PutRun(run) // before the close: an unsaved state would be resumed by the next pass
	r.closeSessions(run)
}

// resume sends text to the same warm task session and starts a new round.
func (r *Runner) resume(run *Run, text string) {
	// The last round's findings describe an older head: keep them as findings-rN.json, so the new round's mid-work stops
	// are deferred until the session writes fresh findings.
	if run.Worktree != "" {
		dir := filepath.Join(run.Worktree, ".hivemill")
		_ = os.Rename(filepath.Join(dir, "findings.json"), filepath.Join(dir, fmt.Sprintf("findings-r%d.json", run.Round)))
	}
	run.State, run.Round, run.GateBlocks, run.BlockStops, run.LastBlock = Working, run.Round+1, 0, 0, ""
	run.RoundStart = nowISO()
	_ = r.Store.TaskStatus(run.Task, "in_progress", "")
	if err := r.Prompt(run.Agent, text); err != nil {
		r.fail(run, "cannot reach the task session: "+err.Error())
		return
	}
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-resume", truncate(text, 300))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Pass advances every active run once (at most one gh read per run), checks for a revert, then starts ready tasks.
func (r *Runner) Pass(startNew bool) error {
	// the daemon reuses the Runner: each pass, a skipped one too, reads the machines again
	r.usageFiles, r.usageRead = nil, false
	l := r.lock("pass", false) // one pass at a time: a timer tick that finds a pass running exits at once
	if l == nil {
		r.printf("another pass is running; skipped\n")
		return nil
	}
	defer l.Close()
	if err := r.WriteGuardExport(time.Now()); err != nil { // a standalone home: the guard fails closed on a stale export
		r.printf("hivemill: %v\n", err)
	}
	list, ok := Terminals(r.Tools)
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	for _, x := range runs {
		if heldForOwner(&x) && !x.Eval {
			if l := r.lock(x.Task, false); l != nil {
				if run, _ := r.Store.Run(x.Task); run != nil && heldForOwner(run) {
					r.checkHeld(run)
					_ = r.Store.PutRun(run)
				}
				l.Close()
			}
		}
		if Active(x.State) {
			st, err := r.Advance(x.Task, list, ok)
			if err != nil {
				st = "error: " + firstLine(err.Error())
			}
			r.printf("%s: %s\n", x.Task, st)
		}
	}
	if err := r.RevertCheck(); err != nil {
		r.printf("revert check: %v\n", err)
	}
	if !startNew || r.Draining() {
		return nil
	}
	pre := r.ReadCaps(time.Now())
	if why := pre.DispatchBlock(r.Store, ""); why != "" {
		r.budgetEvent(why)
		r.usageAlert(pre, why)
		r.printf("dispatch held: %s\n", why)
		return nil
	}
	cands, err := r.Candidates()
	if err != nil {
		return err
	}
	for _, t := range cands {
		if r.activeCount() >= cfgInt(r.Store, "max_concurrent") {
			break
		}
		if err := r.Start(t.ID); err != nil {
			r.printf("%s: %v\n", t.ID, err)
		}
	}
	return nil
}

// Advance runs one step of one run. It returns the state after the step.
func (r *Runner) Advance(task string, list []Terminal, listed bool) (string, error) {
	l := r.lock(task, false)
	if l == nil {
		return "busy (a gate is running)", nil
	}
	defer l.Close()
	run, err := r.Store.Run(task)
	if err != nil || run == nil {
		return "", err
	}
	defer func() { _ = r.Store.PutRun(run) }()
	if Active(run.State) && run.State != Merging && r.overBudget(run) {
		return run.State, nil
	}
	if r.reconcile(run, list, listed) {
		return run.State, nil
	}
	switch run.State {
	case Working:
		r.idleWatch(run, list)
	case Gated:
		r.gated(run)
	case Judging:
	case Judged:
		r.judged(run)
	}
	if run.State == Merging {
		r.Merge(run)
	}
	return run.State, nil
}

func last(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	return xs[len(xs)-1]
}

func (r *Runner) gated(run *Run) {
	pr, err := PRView(r.Tools, run.PRURL)
	if err != nil {
		run.MergeHold = err.Error()
		return
	}
	switch {
	case pr.State == "MERGED" && run.Eval:
		r.fail(run, "eval PR was merged outside the runner: eval runs are never merged")
	case pr.State == "MERGED":
		run.State = Merging
	case pr.State == "CLOSED":
		r.fail(run, "the PR was closed")
	case pr.Mergeable == "CONFLICTING":
		run.History = append(run.History, fmt.Sprintf("round %d: the PR conflicts with %s", run.Round, run.Base))
		r.resume(run, fmt.Sprintf("Your PR conflicts with %s. Run `git fetch origin && git merge origin/%s`, resolve, re-run the "+
			"affected tests, push, wait for CI once and stop.", run.Base, run.Base))
	default:
		if pr.HeadRefOid != run.Head {
			run.Head = pr.HeadRefOid
		}
		verdict, failed := CIVerdict(pr.Checks)
		switch verdict {
		case "red":
			run.CIRounds++
			_ = r.Log.Emit("ci.red", run, Event{Data: map[string]any{"failed": failed, "ci_round": run.CIRounds}})
			if run.CIRounds > cfgInt(r.Store, "ci_rounds_max") {
				r.fail(run, fmt.Sprintf("CI red %d times: %s", run.CIRounds, strings.Join(failed, ", ")))
				return
			}
			run.History = append(run.History, fmt.Sprintf("round %d: CI red (%s)", run.Round, strings.Join(failed, ", ")))
			r.resume(run, "CI is red on your PR: "+strings.Join(failed, ", ")+". Read the failing log (`gh run view --log-failed`), "+
				"fix it, push, wait for CI once with one blocking command, and stop.")
		case "green":
			if err := r.StartJudge(run); err != nil {
				r.fail(run, "the judge did not start: "+err.Error())
			}
		}
	}
}

func (r *Runner) judged(run *Run) {
	v := run.Verdicts[len(run.Verdicts)-1]
	for _, j := range run.Judges {
		r.Close(j)
	}
	if run.Eval {
		r.evalFinish(run, Evaluated, "judge "+v.Verdict)
		return
	}
	if v.Verdict == VerdictMerge {
		run.State = Merging
		return
	}
	if v.Verdict == VerdictHold {
		run.History = append(run.History, fmt.Sprintf("round %d: judge HOLD: %s", run.Round, truncate(strings.Join(v.Reasons, "; "), 300)))
		r.hold(run, "judge HOLD: "+truncate(strings.Join(v.Reasons, "; "), 500))
		return
	}
	run.JudgeRounds++
	reasons := strings.Join(v.Reasons, "; ")
	run.History = append(run.History, fmt.Sprintf("round %d: judge REJECT: %s", run.Round, truncate(reasons, 300)))
	if run.JudgeRounds >= cfgInt(r.Store, "judge_rounds_max") || v.Invalid {
		r.fail(run, fmt.Sprintf("judge REJECT x%d: %s", run.JudgeRounds, reasons))
		return
	}
	r.resume(run, "The judge rejected the PR. Fix this, push, wait for CI once and stop:\n- "+strings.Join(v.Reasons, "\n- "))
}

// Stop cancels a run: its terminals close and the task goes back to todo.
func (r *Runner) Stop(task string) error {
	run, err := r.Store.Run(task)
	if err != nil || run == nil {
		return fmt.Errorf("no run for %s", task)
	}
	r.closeSessions(run)
	run.State, run.Outcome, run.Ended = Cancelled, "cancelled", nowISO()
	r.endSessions(run)
	_ = r.Log.Emit("task.cancelled", run, Event{})
	_ = r.Store.TaskStatus(task, "todo", "")
	return r.Store.PutRun(run)
}

// Status prints one line per run.
func (r *Runner) Status() error {
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	for _, x := range runs {
		hold := ""
		if x.State == Merging && x.MergeHold != "" {
			hold = " hold: " + truncate(x.MergeHold, 100)
		}
		r.printf("%s %-9s round %d blocks %d judge %d ci %d on %s %s%s\n", x.Task, x.State, x.Round, x.GateBlocksTotal, len(x.Verdicts),
			x.CIRounds, machineLabel(x.Machine), x.PRURL, hold)
	}
	return nil
}

// budgetEvent emits budget.reached once per reason and day.
func (r *Runner) budgetEvent(why string) {
	kind := "budget.reached"
	switch {
	case strings.HasPrefix(why, "budget:"):
	case strings.HasPrefix(why, "usage unknown:"):
		kind = "usage.unknown"
	default:
		return
	}
	dir := filepath.Join(r.Env.StateDir(), "locks")
	_ = os.MkdirAll(dir, 0o700)
	mark := filepath.Join(dir, "budget-"+time.Now().Format("2006-01-02"))
	if b, _ := os.ReadFile(mark); strings.Contains(string(b), why[:min(len(why), 32)]) {
		return
	}
	f, err := os.OpenFile(mark, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err == nil {
		fmt.Fprintln(f, why)
		f.Close()
	}
	_ = r.Log.Emit(kind, nil, Event{Data: map[string]any{"why": why}})
}

// idleWatch nudges a task session that sits idle with no PR for 15 minutes (it stopped mid-work, e.g. while a
// background review was running); after a second idle spell the run is FAIL "stalled".
func (r *Runner) idleWatch(run *Run, list []Terminal) {
	idle := false
	for _, t := range list {
		if t.Name == run.Agent {
			idle = t.Status == "idle" || t.Status == "done"
		}
	}
	if !idle || r.workflowBusy(run, time.Now().Add(-15*time.Minute)) {
		run.IdleSince = ""
		return
	}
	if run.IdleSince == "" {
		run.IdleSince = nowISO()
		return
	}
	since, err := time.Parse(time.RFC3339, run.IdleSince)
	if err != nil || time.Since(since) < 15*time.Minute {
		return
	}
	run.IdleSince = ""
	if run.IdleNudges >= 1 {
		r.fail(run, "stalled: the task session stayed idle without a passing gate after a nudge")
		return
	}
	run.IdleNudges++
	_ = r.Prompt(run.Agent, "You stopped before your task was finished. Continue your role steps from where you are: finish the "+
		"review, write the .hivemill files, push, open the PR, wait for CI once, then stop.")
	_ = r.Log.Emit("session.nudged", run, Event{Role: "task"})
}

// usageAlert tells the owner once (until the gate opens again) that dispatch is held on usage and no probe can help:
// no reading has ever existed, or the last reading was over a cap.
func (r *Runner) usageAlert(c Caps, why string) {
	if !c.UsageHopeless(r.Store) || r.Store.Meta("usage_alerted") != "" {
		return
	}
	_ = r.Store.AlertFeed("hivemill-usage", "dispatch-held", "hivemill report")
	_ = r.Log.Emit("usage.alert", nil, Event{Data: map[string]any{"why": why}})
	_ = r.Store.SetMeta("usage_alerted", truncate(why, 200))
}
