package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/citools/risktier"
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
}

// NewRunner opens the factory DB of the environment.
func NewRunner(e Env, out io.Writer) (*Runner, error) {
	s, err := OpenSQLite(e.DBPath())
	if err != nil {
		return nil, err
	}
	return &Runner{Env: e, Store: s, Tools: RealTools(e), Log: Log{Path: filepath.Join(e.StateDir(), "events.jsonl")}, Out: out,
		Poll: 2 * time.Second}, nil
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

var pathWordRe = regexp.MustCompile(`[\w.\-]+(?:/[\w.\-*]+)+/?`)

// Risk decides the model before any diff exists: a stored tier T3, or a sensitive path (ci/tier_rules.json of the
// main checkout) named in the task text. Unreadable rules count as risky (Opus), never the cheap path.
func Risk(home string, t *Task) (bool, string) {
	if t.Tier == "T3" {
		return true, "task tier T3"
	}
	rules, err := risktier.LoadRules(filepath.Join(home, "ci", "tier_rules.json"))
	if err != nil {
		if t.Project == "factory" {
			return true, "tier rules unreadable"
		}
		return false, ""
	}
	var hits []string
	for _, w := range pathWordRe.FindAllString(t.Title+" "+t.Spec+" "+t.Acceptance, -1) {
		if why, _ := risktier.Reasons(map[string][2]string{}, []string{strings.Trim(w, "/")}, 0, rules); len(why) > 0 {
			hits = append(hits, w)
		}
	}
	if len(hits) > 0 {
		if len(hits) > 3 {
			hits = hits[:3]
		}
		return true, "names " + strings.Join(hits, ", ")
	}
	return false, ""
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
func (r *Runner) Candidates() ([]Task, error) {
	ts, err := r.Store.ReadyTasks(csv(r.Store.Cfg("projects")))
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
	if live, _ := r.Store.OldPipelineLive(id); len(live) > 0 {
		return fmt.Errorf("%w: %s has live agents of the old pipeline (%s); stop them first", ErrRefused, id, strings.Join(live, ", "))
	}
	if t.RepoPath == "" || t.GHRepo == "" {
		return fmt.Errorf("%w: %s: its project has no repo_path or gh_repo", ErrRefused, id)
	}
	risky, why := Risk(r.Env.Home, t)
	model, effort, err := r.ModelFor(risky)
	if err != nil {
		return err
	}
	branch := t.Branch
	if branch == "" {
		branch = "factory/" + strings.ToLower(id)
	}
	wt, err := r.Clone(t, branch)
	if err != nil {
		return err
	}
	// The worker guard (unchanged) lets a pane push only HEAD:factory/<task> for FACTORY_AGENT
	// claude-coder-t-NNNN(-aN); the lane's own suffix -a7 keeps the name apart from old-pipeline coders.
	name := "claude-coder-" + strings.ToLower(id) + "-a7"
	run := &Run{Task: id, Goal: t.Goal, Project: t.Project, Repo: t.GHRepo, Base: t.Base, Worktree: wt, Branch: branch, Agent: name,
		State: Working, Round: 1, Risky: risky, RiskWhy: why, Model: model, Effort: effort, Dispatched: nowISO(),
		Criteria: len(Criteria(t.Acceptance))}
	if err := r.Store.PutRun(run); err != nil {
		return err
	}
	_ = r.Log.Emit("task.dispatched", run, Event{Model: model, Effort: effort, Data: map[string]any{"risky": risky, "why": why}})
	_ = r.Store.TaskStatus(id, "in_progress", "")
	_ = r.Store.Event(r.actor(), "task", id, "run-dispatch", map[string]any{"agent": name, "model": model, "risky": risky})
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
	_, err = r.StartSession(run, Launch{Role: "task", Name: name, Cwd: wt, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", "task.md"), Kickoff: Kickoff(run, t), Env: env})
	if err != nil {
		r.fail(run, "the session did not start: "+err.Error())
		_ = r.Store.PutRun(run)
		return err
	}
	r.printf("RUN %s: %s (%s, effort %s%s) in %s\n", id, name, model, effort, map[bool]string{true: ", risk: " + why}[risky], wt)
	return nil
}

// Kickoff is the first message of a task session: the task itself, so the session needs no lookup.
func Kickoff(run *Run, t *Task) string {
	var b strings.Builder
	fmt.Fprintf(&b, "You are `%s`, the factory task. Your task is %s: %s.\n\nSpec:\n%s\n\nAcceptance criteria (number. proof):\n",
		run.Agent, t.ID, t.Title, strings.TrimSpace(t.Spec))
	for _, c := range Criteria(t.Acceptance) {
		fmt.Fprintf(&b, "%d. %s: %s", c.ID, c.Kind, c.Proof)
		if c.Expect != "" {
			fmt.Fprintf(&b, " => %s", c.Expect)
		}
		b.WriteString("\n")
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
			{wt, "checkout", "-q", "-f", "-B", branch, "origin/" + t.Base},
		}
		for _, s := range steps {
			if rc, _, errs := g(s[0], s[1:]...); rc != 0 {
				_ = os.RemoveAll(wt)
				return "", fmt.Errorf("clone %s: git %s: %s", wt, s[1], firstLine(errs))
			}
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
	hook := "#!/bin/sh\n# factory pre-push public-text guard (G-021 T-0696)\nexec python3 " +
		filepath.Join(r.Env.Home, "ci", "pre_push_public_text.py") + " --home " + r.Env.Home + " \"$@\"\n"
	_ = os.MkdirAll(filepath.Join(gitDir, "hooks"), 0o755)
	return os.WriteFile(filepath.Join(gitDir, "hooks", "pre-push"), []byte(hook), 0o755)
}

func (r *Runner) fail(run *Run, reason string) {
	run.State, run.Outcome, run.FailReason, run.Ended = Failed, "failed", reason, nowISO()
	_ = r.Store.TaskStatus(run.Task, "blocked", "owner")
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-fail", truncate(reason, 900))
	_ = r.Log.Emit("task.failed", run, Event{Data: map[string]any{"reason": truncate(reason, 900)}})
}

// resume sends text to the same warm task session and starts a new round.
func (r *Runner) resume(run *Run, text string) {
	run.State, run.Round, run.GateBlocks = Working, run.Round+1, 0
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
	list, ok := Terminals(r.Tools)
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	for _, x := range runs {
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
	if !startNew {
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
	switch run.State {
	case Working:
		if !alive(list, listed, run.Agent) {
			r.fail(run, "the task session ended before its gate passed")
		}
	case Gated:
		r.gated(run)
	case Judging:
		if j := last(run.Judges); j != "" && !alive(list, listed, j) {
			r.fail(run, "judge "+j+" ended without a verdict")
		}
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
	if v.Verdict == "MERGE" {
		run.State = Merging
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
	r.Close(run.Agent)
	for _, j := range run.Judges {
		r.Close(j)
	}
	run.State, run.Outcome, run.Ended = Cancelled, "cancelled", nowISO()
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
		r.printf("%s %-9s round %d blocks %d judge %d ci %d %s%s\n", x.Task, x.State, x.Round, x.GateBlocksTotal, len(x.Verdicts),
			x.CIRounds, x.PRURL, hold)
	}
	return nil
}
