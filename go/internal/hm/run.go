package hm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"factory/go/internal/guard"
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
	Held      = "held" // the owner decides (a hard limit, judge HOLD, a tier the runner does not merge); not a failure, counts against max_waiting
)

// Active reports whether a run is unfinished and each pass advances it; busy and waiting say which cap it counts against.
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
	// SSH runs command on a clone-mode session machine, with stdin (clonemachine.go).
	SSH func(machine, stdin string, timeout time.Duration, command string) (int, string, string)
	// ObsDial dials the dashboard's address for ObsDown (nil = a TCP connect).
	ObsDial    func(addr string) error
	usageFiles [][]byte               // the remote usage files of this pass
	usageRead  bool                   // usageFiles has been read in this pass
	inDaemon   bool                   // this process runs the daemon's passes
	TakeOver   bool                   // the daemon starts over another host's fresh daemon lock (daemonlock.go)
	standby    chan standbyResult     // the standby check running beside the passes (standby.go)
	takenOver  atomic.Pointer[string] // the host that took the daemon lock over; set once, ends this daemon
	loads      map[string]loadReading // CPU load per machine, see load.go
	poll       *prPoll                // the pass's one read of the open PRs; nil outside a pass
	queueSlow  bool                   // a PR is queued for CI over its budget: no refresh
	// DiskFree is (total, available) bytes of a path's filesystem; nil = statfs. Tests replace it.
	DiskFree func(path string) (uint64, uint64, error)
}

// NewRunner opens the factory DB of the environment.
func NewRunner(e Env, out io.Writer) (*Runner, error) {
	s, err := OpenSQLite(e.DBPath())
	if err != nil {
		return nil, err
	}
	return &Runner{Env: e, Store: s, Tools: RealTools(e), Out: out, Poll: 2 * time.Second, RemoteUsage: readRemoteUsage, SSH: sshTransport,
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

// busy reports whether a run does work: its session writes or judges, or the runner runs its proofs or gate_cmd (the
// gate and the eval gate run while the run is still working). Only these runs count against max_concurrent.
func busy(state string) bool { return state == Working || state == Judging }

// waiting reports whether an unfinished run does no work: it waits for CI (gated), for the merge (judged, merging) or
// for the owner (held). These runs count against max_waiting.
func waiting(x *Run) bool {
	return (Active(x.State) && !busy(x.State)) || (heldForOwner(x) && !x.Eval)
}

// runCounts is the number of busy and of waiting runs.
func (r *Runner) runCounts() (working, waits int) {
	runs, _ := r.Store.Runs()
	for i := range runs {
		switch {
		case busy(runs[i].State):
			working++
		case waiting(&runs[i]):
			waits++
		}
	}
	return working, waits
}

// runCapBlock says why the run caps hold a new run now ("" = they do not).
func (r *Runner) runCapBlock() string {
	if why := r.waitingBlock(); why != "" {
		return why
	}
	working, _ := r.runCounts()
	if limit := cfgInt(r.Store, "max_concurrent"); working >= limit {
		return fmt.Sprintf("max_concurrent=%d runs are working", limit)
	}
	return ""
}

// waitingBlock is the max_waiting part of runCapBlock. Reaching the cap emits one dispatch.paused_waiting event; the
// next one comes only after the count has been seen below the cap again.
func (r *Runner) waitingBlock() string {
	_, waits := r.runCounts()
	if limit := cfgInt(r.Store, "max_waiting"); waits >= limit {
		why := fmt.Sprintf("max_waiting=%d: %d runs wait for CI, a merge or the owner", limit, waits)
		if r.Store.Meta("waiting_paused") == "" {
			_ = r.Log.Emit("dispatch.paused_waiting", nil, Event{Data: map[string]any{"why": why, "waiting": waits, "max_waiting": limit}})
			_ = r.Store.SetMeta("waiting_paused", nowISO())
		}
		return why
	}
	if r.Store.Meta("waiting_paused") != "" {
		_ = r.Store.SetMeta("waiting_paused", "")
	}
	return ""
}

// Candidates are ready tasks a plain pass may start (config projects): deps done, spec set, spec gate passing.
func (r *Runner) Candidates() ([]Task, error) { return r.ReadyIn(csv(r.Store.Cfg("projects"))) }

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
	ciNone, err := r.ciMode(t.Project)
	if err != nil {
		return err
	}
	if cmd, _ := r.projectGate(t.Project); ciNone && strings.TrimSpace(cmd) == "" {
		return fmt.Errorf("%w: %s: project %s has ci_gate none and no gate_cmd, so nothing would test it; set a gate_cmd or ci_gate pr",
			ErrRefused, id, t.Project)
	}
	if old, _ := r.Store.Run(id); old != nil && Active(old.State) {
		return fmt.Errorf("%w: %s already has an active run (%s)", ErrRefused, id, old.State)
	}
	if why := r.runCapBlock(); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
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
	branch, err := r.startBranch(t)
	if err != nil {
		return err
	}
	// held until the run is saved: the removal of an ended run's remote worktree takes it too
	l := r.lock(id, false)
	if l == nil {
		return fmt.Errorf("%w: %s is busy; try again", ErrRefused, id)
	}
	locked := true
	unlock := func() {
		if locked {
			locked = false
			l.Close()
		}
	}
	defer unlock()
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
	mirror, remoteHome := "", ""
	if pl.Machine != "" && r.cloneMode(pl.Machine) { // its own clone there; the one here stays as the mirror
		rhome, err := r.remoteHomeOf(pl.Machine)
		remote := ""
		if err == nil {
			remote, err = r.remoteWorktree(pl.Machine, rhome, t, branch)
		}
		if err != nil {
			pl.Skipped, pl.Machine = append(pl.Skipped, pl.Machine+": "+err.Error()), ""
		} else {
			mirror, remoteHome, wt = wt, rhome, remote
		}
	} else if pl.Machine != "" {
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
	// session's first mid-work stop as a gate attempt, and they describe another head. An old blocked.md would end its /goal.
	for _, f := range []string{"findings.json", "evidence.json", "blocked.md"} {
		if mirror == "" { // remoteWorktree removed them in a remote worktree
			_ = os.Remove(filepath.Join(wt, ".hivemill", f))
		}
	}
	// The worker guard (unchanged) lets a pane push only HEAD:factory/<task> for FACTORY_AGENT
	// claude-coder-t-NNNN(-aN); the lane's own suffix -a7 keeps the name apart from old-pipeline coders.
	name := "claude-coder-" + strings.ToLower(id) + "-a7"
	run := &Run{Task: id, Goal: t.Goal, Project: t.Project, Repo: t.GHRepo, Base: t.Base, Worktree: wt, Branch: branch, Agent: name,
		Continues: r.continuesFrom(id, branch),
		State:     Working, Round: 1, Risky: risky, RiskWhy: why, Model: model, Effort: effort, Dispatched: nowISO(), RoundStart: nowISO(),
		Criteria: len(Criteria(t.Acceptance)), CINone: ciNone, Machine: pl.Machine, WorkflowAgents: workflowAgents(r.Store, t),
		Mirror: mirror, RemoteHome: remoteHome}
	if err := r.saveStarted(run, t); err != nil {
		return err
	}
	unlock()
	_ = r.Log.Emit("task.dispatched", run, Event{Model: model, Effort: effort, Data: map[string]any{"risky": risky, "why": why,
		"machine": machineLabel(pl.Machine), "skipped": pl.Skipped, "workflow": run.WorkflowAgents > 0}})
	_ = r.Store.TaskStatus(id, "in_progress", "")
	_ = r.Store.Event(r.actor(), "task", id, "run-dispatch", map[string]any{"agent": name, "model": model, "risky": risky})
	kickoff := Kickoff(run, t)
	if old := r.continuedTask(run); old != "" {
		kickoff += continuedNote(run, old)
	}
	if err := r.launchTask(run, r.goalPrompt(Harness, run, t, kickoff)); err != nil {
		return err
	}
	r.printf("RUN %s: %s (%s, effort %s%s) in %s on %s\n", id, name, model, effort, map[bool]string{true: ", risk: " + why}[risky], run.Worktree,
		machineLabel(pl.Machine))
	return nil
}

// saveStarted adopts the PR the new run continues and saves the run, under one lock: the sweep must not close that PR
// in between, and of two starts on one branch only the first is saved.
func (r *Runner) saveStarted(run *Run, t *Task) error {
	if l := r.lock(prLock, true); l != nil {
		defer l.Close()
	}
	user, err := r.branchUser(run.Task, run.Repo, run.Branch, "")
	if err != nil {
		return err
	}
	if user != "" {
		return fmt.Errorf("%w: %s: the run of %s works on %s", ErrRefused, run.Task, user, run.Branch)
	}
	if err := r.adoptPR(run, t); err != nil { // before the save: a pass must not see the run without its PR
		return err
	}
	return r.Store.PutRun(run)
}

// taskEnv is the environment of a task session, started or relaunched: the noreply identity, the scrub terms and,
// for a run that continues the branch of an older task, that task for the guard's push pin.
func (r *Runner) taskEnv(run *Run) map[string]string {
	env := map[string]string{}
	if old := r.continuedTask(run); old != "" {
		env[guard.PushTaskVar] = old
	}
	if email := r.Store.FactoryCfg("noreply_email"); email != "" {
		env["GIT_AUTHOR_EMAIL"], env["GIT_COMMITTER_EMAIL"] = email, email
		env["GIT_AUTHOR_NAME"], env["GIT_COMMITTER_NAME"] = nameOf(email), nameOf(email)
	}
	if terms := r.Store.FactoryCfg("scrub_terms_file"); terms != "" {
		env["SCRUB_TERMS_FILE"] = terms
	}
	return env
}

// launchTask starts the task session of a run in its clone, with the noreply identity and the scrub terms.
func (r *Runner) launchTask(run *Run, kickoff string) error {
	name, wt, model, effort := run.Agent, run.Worktree, run.Model, run.Effort
	env := r.taskEnv(run)
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

// kickoffEnd is the last line of a kickoff; without PR checks there is no green CI to stop on.
func kickoffEnd(run *Run) string {
	until := "the PR is open and CI is green"
	if run.CINone {
		until = "the PR is open"
	}
	return fmt.Sprintf("\nBranch %s, base %s. Follow your role steps; stop when %s.", run.Branch, run.Base, until)
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
	b.WriteString(uiSkillNote(t))
	if run.WorkflowAgents > 0 {
		fmt.Fprintf(&b, "\n%s\n", workflowRequest(run.WorkflowAgents))
	}
	if run.CINone {
		b.WriteString("\nThis project has no PR checks (ci_gate none): the factory runs the project's gate command as the CI. " +
			"Skip the CI wait of your role steps (no `gh pr checks`): push, open the PR and stop.\n")
	}
	b.WriteString(kickoffEnd(run))
	return b.String()
}

// Clone makes (or reuses) the task's own clone under <home>/worktrees: objects shared with the project checkout,
// origin set to the project's own origin, branch from origin/<base>, the public-text pre-push hook installed and
// .hivemill/ ignored through the clone's .git/info/exclude.
func (r *Runner) Clone(t *Task, branch string) (string, error) {
	g := r.Tools.Git
	wt := filepath.Join(r.Env.Home, "worktrees", t.Goal+"-"+t.ID)
	if fi, err := os.Stat(filepath.Join(wt, ".git")); err == nil && fi.IsDir() {
		g(wt, "fetch", "-q", "origin") // another run may have pushed to the branch; offline, the local one serves
		if rc, _, errs := g(wt, "checkout", "-q", branch); rc != 0 {
			if rc, _, errs2 := g(wt, "checkout", "-q", "-B", branch, "origin/"+t.Base); rc != 0 {
				return "", fmt.Errorf("clone %s: cannot check out %s: %s %s", wt, branch, errs, errs2)
			}
		}
		g(wt, "merge", "-q", "--ff-only", "origin/"+branch) // best effort: unpushed local commits stay
	} else {
		if _, err := os.Stat(wt); err == nil {
			return "", fmt.Errorf("%w: %s exists but is not a clone; remove it", ErrRefused, wt)
		}
		rc, url, errs := g(t.RepoPath, "remote", "get-url", "origin")
		if rc != 0 || url == "" {
			return "", fmt.Errorf("%s has no origin URL: %s", t.RepoPath, errs)
		}
		cow := false
		if GOOS == "darwin" {
			if err := r.cowClone(t, wt, url); err != nil {
				r.printf("%s: copy-on-write clone: %v (full clone instead)\n", t.ID, err)
			} else {
				cow = true
			}
		}
		steps := [][]string{
			{t.RepoPath, "-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", t.RepoPath, wt},
			{wt, "remote", "set-url", "origin", url},
			{wt, "fetch", "-q", "origin"},
		}
		for _, s := range steps {
			if cow {
				break
			}
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

// cloneTree copies a directory tree copy-on-write (APFS clonefile through cp -c): unchanged files share disk blocks.
var cloneTree = func(src, dst string) error {
	if out, err := exec.Command("cp", "-c", "-R", src, dst).CombinedOutput(); err != nil {
		return fmt.Errorf("cp -c: %v: %s", err, firstLine(string(out)))
	}
	return nil
}

// cowClone makes a task clone on macOS as a copy-on-write copy of one base checkout per project and base branch
// (<state>/base/<project>-<base>, kept at origin/<base>), so a task costs only the files it changes, not the whole
// tree (2026-11-27: 25 clones of one app repo took 49 GB, about 2 GB of tracked files each). The caller checks out the task
// branch; on an error the caller makes a full clone.
func (r *Runner) cowClone(t *Task, wt, url string) error {
	g := r.Tools.Git
	base := filepath.Join(r.Env.StateDir(), "base", t.Project+"-"+t.Base)
	if l := r.lock("base-"+t.Project+"-"+t.Base, true); l != nil {
		defer l.Close()
	}
	if fi, err := os.Stat(filepath.Join(base, ".git")); err != nil || !fi.IsDir() {
		_ = os.RemoveAll(base)
		_ = os.MkdirAll(filepath.Dir(base), 0o755)
		for _, s := range [][]string{
			{t.RepoPath, "-c", "protocol.file.allow=always", "clone", "-q", "--shared", "--no-checkout", t.RepoPath, base},
			{base, "remote", "set-url", "origin", url},
		} {
			if rc, _, errs := g(s[0], s[1:]...); rc != 0 {
				_ = os.RemoveAll(base)
				return fmt.Errorf("base %s: git %s: %s", base, s[1], firstLine(errs))
			}
		}
	}
	for _, s := range [][]string{{"fetch", "-q", "origin"}, {"checkout", "-q", "-f", "--detach", "origin/" + t.Base}} {
		if rc, _, errs := g(base, s...); rc != 0 {
			return fmt.Errorf("base %s: git %s: %s", base, s[0], firstLine(errs))
		}
	}
	if err := cloneTree(base, wt); err != nil {
		_ = os.RemoveAll(wt)
		return err
	}
	// the copy has new inodes, so every index entry looks changed: refresh it from content, which rewrites no file
	_, _, _ = g(wt, "update-index", "-q", "--refresh")
	return nil
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
	run.HookStarted = ""
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
	r.repointRefiles()      // after the save: it reads the run state
	r.closeSessions(run)
}

// resume sends text to the same task session and starts a new round. A session whose pane has closed is relaunched by
// its session id with text as its first message.
func (r *Runner) resume(run *Run, text string) { r.resumeAs(run, "resume", text) }

// resumeAs is resume with the kind a prompt to the warm session is recorded under (a relaunch records its own).
func (r *Runner) resumeAs(run *Run, kind, text string) {
	// The last round's findings describe an older head: keep them as findings-rN.json, so the new round's mid-work stops
	// are deferred until the session writes fresh findings.
	if run.Worktree != "" {
		dir := filepath.Join(run.Worktree, ".hivemill")
		old := fmt.Sprintf("findings-r%d.json", run.Round)
		if err := r.at(run).rename(filepath.Join(dir, "findings.json"), filepath.Join(dir, old)); err != nil {
			r.printf("%s: %v\n", run.Task, err)
			text = "First move .hivemill/findings.json to .hivemill/" + old + " (the factory could not). " + text
		}
	}
	run.State, run.Round, run.GateBlocks, run.BlockStops, run.LastBlock = Working, run.Round+1, 0, 0, ""
	run.RoundStart = nowISO()
	run.LongPct, run.LongResumed, run.LongSendFailed = 0, "", ""
	run.HookStarted = ""
	if run.Head != "" { // sent back for more work: this head must not go to evaluation unchanged (rescueOpenPR)
		run.RescuedHead = run.Head
	}
	_ = r.Store.TaskStatus(run.Task, "in_progress", "")
	send := r.Prompt
	list, listed := Terminals(r.Tools)
	relaunch := listed && !alive(list, true, run.Agent) && !(run.Machine != "" && machineDown(r.Tools, run.Machine)) &&
		r.sessionID(run.Task, run.Agent) != ""
	if relaunch {
		send = func(name, text string) error { return r.resumeSession(run, name, text) }
	}
	if err := send(run.Agent, text); err != nil {
		r.fail(run, "cannot reach the task session: "+err.Error())
		return
	}
	if !relaunch {
		r.recordPrompt(run, "task", kind, text)
	}
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-resume", truncate(text, 300))
}

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Pass reads every open PR with one query (prpoll.go), advances every active run once, checks for a revert, then starts
// ready tasks.
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
	defer r.removeRemoteWorktrees() // after the runs advanced: an ended one gives its worktree back
	list, ok := Terminals(r.Tools)
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	defer r.countGH()() // the gh calls of this pass, for the digest
	r.sweepLocalCI()    // results first: they free job slots, and the runs below read them
	if runs, err = r.Store.Runs(); err != nil {
		return err
	}
	r.poll = r.pollPRs(runs)
	defer func() { r.poll, r.queueSlow = nil, false }()
	r.queueSlow = r.queuedTooLong(runs)
	fifo(runs)
	for _, x := range runs {
		if heldForOwner(&x) && !x.Eval {
			if l := r.lock(x.Task, false); l != nil {
				if run, _ := r.Store.Run(x.Task); run != nil && heldForOwner(run) {
					r.checkHeld(run)
					r.trackFlow(run)
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
	r.repointRefiles() // a cancel by another tool, or a new dependency on an old id
	r.sweepPRs()
	if err := r.reportUnstartable(); err != nil {
		r.printf("unstartable tasks: %v\n", err)
	}
	// every pass records a waiting pause that starts or ends, also one that dispatches nothing below
	paused := r.waitingBlock()
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
	if paused != "" {
		r.printf("dispatch held: %s\n", paused)
		return nil
	}
	cands, err := r.Candidates()
	if err != nil {
		return err
	}
	for _, t := range cands {
		if r.runCapBlock() != "" {
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
	defer func() {
		if r.poll != nil {
			r.trackFlow(run)
		}
		if run.State != Gated && (run.State != Merging || !r.ciNone(run.Project)) {
			r.endCIWait(run) // the run left the CI wait, however it did
		}
		_ = r.Store.PutRun(run)
	}()
	if Active(run.State) && run.State != Merging && r.overBudget(run) {
		return run.State, nil
	}
	if r.reconcile(run, list, listed) {
		return run.State, nil
	}
	if r.poll != nil && (run.State == Gated || run.State == Merging) {
		r.rerunPending(run)
	}
	switch run.State {
	case Working:
		if !listed || !r.workingLimit(run, time.Now()) { // sessions that cannot be listed are never failed on time
			r.idleWatch(run, list)
		}
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
	now := time.Now()
	if !r.ciPollDue(run, now) {
		return
	}
	pr, err := r.prView(run)
	if err != nil {
		r.ciPolled(run, now)
		run.MergeHold = err.Error()
		r.ciOverdue(run, now) // an unreadable PR is a pending one
		return
	}
	if r.poll != nil && pr.HeadRefOid != run.Head && run.Gated >= r.poll.at {
		return // pushed and gated after the poll: the next pass reads the new head
	}
	run.MergeHold = "" // set again below by what still holds the run
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
			"affected tests, %s.", run.Base, run.Base, r.pushAndStop(run.Project)))
	default:
		run.Head, run.MergeHold = pr.HeadRefOid, ""
		r.ciPolled(run, now)
		var verdict string
		var failed []string       // ci_gate none: what failed in gate_cmd
		var rows []map[string]any // PR checks: the failed rows, for their logs
		none, err := r.ciMode(run.Project)
		if err != nil {
			run.MergeHold = err.Error()
			r.ciOverdue(run, now)
			return
		}
		if none {
			if err := r.localCI(run, pr.HeadRefOid); err != nil {
				run.MergeHold = err.Error()
				r.ciOverdue(run, now)
				return
			}
			r.postLocalCI(run)
			verdict, failed = run.LocalCI.verdict(pr.HeadRefOid)
		} else {
			verdict, rows = ciRows(pr.Checks)
			switch {
			case verdict != "green":
				run.CI.Green = 0
			case run.CI.Green == 0:
				run.CI.Green = now.Unix()
			}
			if verdict == "green" && !r.ciSettled(run, pr.Checks, now) {
				verdict = "pending"
			}
		}
		switch verdict {
		case "red":
			if !none && r.flakyRerun(run, pr.HeadRefOid, rows) {
				run.MergeHold = "re-running failed jobs that passed on " + run.Base + " in the last 24 h"
				return
			}
			if run.Machine != "" && machineDown(r.Tools, run.Machine) {
				run.MergeHold = "CI is red; waiting for machine " + run.Machine + " to resume the session"
				r.ciOverdue(run, now)
				return
			}
			if none {
				r.redCI(run, failed)
			} else {
				r.ciRed(run, rows)
			}
		case "green":
			if err := r.StartJudge(run); err != nil {
				r.fail(run, "the judge did not start: "+err.Error())
			}
		default:
			// in a pass, running Actions checks over budget are re-run (trackFlow); the rest alert the owner, and so does
			// a wait of twice the budget, which a series of short jobs can reach without one job running over it
			capped := now.Unix()-run.CI.Since >= 2*int64(cfgInt(r.Store, "ci_timeout_s"))
			if rows, actions := unfinishedActions(pr.Checks); capped || r.poll == nil || run.Eval || none || !actions || !slices.ContainsFunc(rows, onRunner) {
				r.ciOverdue(run, now)
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
	r.resumeAs(run, "judge", "The judge rejected the PR. Fix this, "+r.pushAndStop(run.Project)+":\n- "+strings.Join(v.Reasons, "\n- "))
}

// Stop cancels a run: its terminals close and the task goes back to todo.
func (r *Runner) Stop(task string) error {
	// a gate of this task holds the lock and saves the run when it ends: cancel after it, or the cancel is lost
	l := r.lock(task, false)
	if l == nil {
		r.printf("%s: waiting for a running step of the task (a gate) to end\n", task)
		l = r.lock(task, true)
	}
	if l != nil {
		defer l.Close()
	}
	run, err := r.Store.Run(task)
	if err != nil || run == nil {
		return fmt.Errorf("no run for %s", task)
	}
	if run.State == Merged { // the step waited for has merged it
		return fmt.Errorf("%w: %s is merged already", ErrRefused, task)
	}
	r.closeSessions(run)
	r.endCIWait(run)
	if run.Flow != nil {
		r.enterFlow(run, "", "")
	}
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
		if (x.State == Merging || x.State == Gated) && x.MergeHold != "" {
			hold = " hold: " + truncate(x.MergeHold, 100)
		}
		flow := ""
		if x.Flow != nil && (Active(x.State) || heldForOwner(&x)) {
			flow = " " + x.Flow.String()
		}
		r.printf("%s %-9s round %d blocks %d judge %d ci %d on %s %s%s%s\n", x.Task, x.State, x.Round, x.GateBlocksTotal, len(x.Verdicts),
			x.CIRounds, machineLabel(x.Machine), x.PRURL, flow, hold)
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

// failUnlessRescued fails a stalled or timed out run, unless an open PR takes it to evaluation (rescueOpenPR). The
// task session of a rescued run is closed, so that it does not change the worktree under the judge; a later round
// relaunches it.
func (r *Runner) failUnlessRescued(run *Run, reason string) {
	if !r.rescueOpenPR(run, reason) {
		r.fail(run, reason)
		return
	}
	r.Close(run.Agent)
}

// idleWatch nudges a task session that sits idle with no PR for 15 minutes (it stopped mid-work, e.g. while a
// background review was running); after a second idle spell the run is FAIL "stalled". A running gate hook is not
// idleness: a session whose hook passed its timeout without a result fails as "gate hook timed out" instead.
func (r *Runner) idleWatch(run *Run, list []Terminal) {
	idle := false
	for _, t := range list {
		if t.Name == run.Agent {
			idle = t.Status == "idle" || t.Status == "done"
		}
	}
	if !idle { // a session that works on has left its hook behind; its next stop records a new start
		run.HookStarted = ""
	}
	started, err := time.Parse(time.RFC3339, run.HookStarted)
	if err == nil && time.Since(started) < gateHookTimeout {
		run.IdleSince = ""
		return
	}
	if !idle || r.workflowBusy(run, time.Now().Add(-15*time.Minute)) {
		run.IdleSince = ""
		return
	}
	if err == nil { // the hook passed its timeout without a result
		run.HookStarted, run.IdleSince = "", ""
		r.failUnlessRescued(run, fmt.Sprintf("gate hook timed out: no result after %d seconds", int(gateHookTimeout/time.Second)))
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
		r.failUnlessRescued(run, "stalled: the task session stayed idle without a passing gate after a nudge")
		return
	}
	run.IdleNudges++
	wait := "wait for CI once, then stop"
	if r.ciNone(run.Project) {
		wait = "then stop (this project has no PR checks to wait for)"
	}
	nudge := "You stopped before your task was finished. Continue your role steps from where you are: finish the " +
		"review, write the .hivemill files, push, open the PR, " + wait + "."
	if r.Prompt(run.Agent, nudge) == nil {
		r.recordPrompt(run, "task", "nudge", nudge)
	}
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
