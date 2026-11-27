package hm

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
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
	ObsDial       func(addr string) error
	usageFiles    [][]byte               // the remote usage files of this pass
	usageRead     bool                   // usageFiles has been read in this pass
	inDaemon      bool                   // this process runs the daemon's passes
	stepErrs      int                    // run steps of the last pass that failed
	passSkipped   bool                   // the last pass did not run: another one held the lock
	outMu         sync.Mutex             // guards Out
	stableSums    sync.Map               // path|size|mtime of the stable binary to its checksum
	stableRunning sync.Map               // projects whose stable pass runs
	stableWG      sync.WaitGroup         // the stable passes
	TakeOver      bool                   // the daemon starts over another host's fresh daemon lock (daemonlock.go)
	standby       chan standbyResult     // the standby check running beside the passes (standby.go)
	takenOver     atomic.Pointer[string] // the host that took the daemon lock over; set once, ends this daemon
	loads         map[string]loadReading // CPU load per machine, see load.go
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

func (r *Runner) printf(f string, a ...any) {
	r.outMu.Lock() // stable passes print beside the pass
	defer r.outMu.Unlock()
	fmt.Fprintf(r.Out, f, a...)
}

// lock takes the per-task lock file; wait=false returns nil while another process (a gate hook) holds it.
func (r *Runner) lock(task string, wait bool) *os.File {
	f, _ := r.lockErr(task, wait)
	return f
}

// lockErr is lock with the reason: errLockBusy when another holder has it, else the open or flock error.
func (r *Runner) lockErr(task string, wait bool) (*os.File, error) {
	dir := filepath.Join(r.Env.StateDir(), "locks")
	_ = os.MkdirAll(dir, 0o700)
	f, err := os.OpenFile(filepath.Join(dir, task+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_EX
	if !wait {
		how |= syscall.LOCK_NB
	}
	if err := syscall.Flock(int(f.Fd()), how); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, errLockBusy
		}
		return nil, err
	}
	return f, nil
}

var errLockBusy = errors.New("lock held by another process")

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
	// one dispatch at a time across processes (the edge pass, a stable pass, a start by hand): the checks below hold
	// until the run is saved
	dl := r.lock("dispatch", false) // no wait: a slow start in one channel must not stall the pass of the other
	if dl == nil {
		return fmt.Errorf("%w: another start is in progress; try again", ErrRefused)
	}
	dispatching := true
	release := func() {
		if dispatching {
			dispatching = false
			dl.Close()
		}
	}
	defer release()
	if old, _ := r.Store.Run(id); old != nil && Active(old.State) {
		return fmt.Errorf("%w: %s already has an active run (%s)", ErrRefused, id, old.State)
	}
	if why := r.runCapBlock(); why != "" {
		return fmt.Errorf("%w: %s", ErrRefused, why)
	}
	if r.Draining() {
		return fmt.Errorf("%w: drain mode is on (`hivemill resume` lifts it)", ErrRefused)
	}
	if r.ProjectDraining(t.Project) {
		return fmt.Errorf("%w: project %s is in drain mode (`hivemill resume --project %s` lifts it)", ErrRefused, t.Project, t.Project)
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
		State: Working, Round: 1, Risky: risky, RiskWhy: why, Model: model, Effort: effort, Dispatched: nowISO(), RoundStart: nowISO(),
		Criteria: len(Criteria(t.Acceptance)), CINone: ciNone, Machine: pl.Machine, WorkflowAgents: workflowAgents(r.Store, t),
		Mirror: mirror, RemoteHome: remoteHome}
	if err := r.Store.PutRun(run); err != nil {
		return err
	}
	unlock()
	release()
	_ = r.Log.Emit("task.dispatched", run, Event{Model: model, Effort: effort, Data: map[string]any{"risky": risky, "why": why,
		"machine": machineLabel(pl.Machine), "skipped": pl.Skipped, "workflow": run.WorkflowAgents > 0}})
	_ = r.Store.TaskStatus(id, "in_progress", "")
	_ = r.Store.Event(r.actor(), "task", id, "run-dispatch", map[string]any{"agent": name, "model": model, "risky": risky})
	if err := r.launchTask(run, r.goalPrompt(Harness, run, t, Kickoff(run, t))); err != nil {
		return err
	}
	r.printf("RUN %s: %s (%s, effort %s%s) in %s on %s\n", id, name, model, effort, map[bool]string{true: ", risk: " + why}[risky], run.Worktree,
		machineLabel(pl.Machine))
	return nil
}

// taskEnv is the environment of a task session, started or relaunched: the noreply identity and the scrub terms.
func (r *Runner) taskEnv() map[string]string {
	env := map[string]string{}
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
	env := r.taskEnv()
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

// Pass advances every active run once (one gh read per run; a red CI also reads the failed logs), checks for a revert, then starts ready tasks.
// A project on the stable channel is not run here: the pass starts the stable binary for it (PassProject).
func (r *Runner) Pass(startNew bool) error { return r.pass("", startNew) }

// PassProject is the pass of one project, whatever its channel (`hivemill run --project ID`).
func (r *Runner) PassProject(project string, startNew bool) error {
	if err := r.checkProject(project); err != nil {
		return err
	}
	if err := r.pass(project, startNew); err != nil {
		return err
	}
	switch {
	case r.passSkipped:
		return ErrPassSkipped
	case r.stepErrs > 0:
		return fmt.Errorf("project %s: %d steps of the pass failed", project, r.stepErrs)
	}
	return nil
}

// pass runs the projects of this process, or only the project `only`, which has its own pass lock.
func (r *Runner) pass(only string, startNew bool) error {
	r.stepErrs, r.passSkipped = 0, false
	lockName, mine := "pass", func(p string) bool { return r.Delegate(p) == "" }
	if only != "" && r.stableFor(only) == "" { // an edge project: never beside the full pass
		mine = func(p string) bool { return p == only }
	} else if only != "" {
		lockName, mine = "pass-"+only, func(p string) bool { return p == only }
	}
	// the daemon reuses the Runner: each pass, a skipped one too, reads the machines again
	r.usageFiles, r.usageRead = nil, false
	if only == "" && !r.inDaemon {
		defer r.waitStable() // registered first, so it runs after the pass lock is released
	}
	l, lerr := r.lockErr(lockName, false) // one pass at a time: a timer tick that finds a pass running exits at once
	if lerr != nil && !errors.Is(lerr, errLockBusy) {
		return fmt.Errorf("pass lock %s: %w", lockName, lerr) // not a skip: a broken lock dir must fail the pass
	}
	if l == nil {
		r.passSkipped = true
		r.printf("another pass is running; skipped\n")
		return nil
	}
	defer l.Close()
	if only == "" {
		r.stablePasses(startNew) // first: nothing that fails below holds a stable project back
	}
	if err := r.WriteGuardExport(time.Now()); err != nil { // a standalone home: the guard fails closed on a stale export
		r.printf("hivemill: %v\n", err)
	}
	if only == "" {
		defer r.removeRemoteWorktrees() // after the runs advanced: an ended one gives its worktree back
	}
	list, ok := Terminals(r.Tools)
	runs, err := r.Store.Runs()
	if err != nil {
		return err
	}
	r.sweepLocalCI() // results first: they free job slots, and the runs below read them
	if runs, err = r.Store.Runs(); err != nil {
		return err
	}
	for _, x := range runs {
		if !mine(x.Project) {
			continue
		}
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
				r.stepErrs++
			}
			r.printf("%s: %s\n", x.Task, st)
		}
	}
	if err := r.revertCheck(mine); err != nil {
		r.stepErrs++
		r.printf("revert check: %v\n", err)
	}
	if only == "" {
		r.repointRefiles() // a cancel by another tool, or a new dependency on an old id
		if err := r.reportUnstartable(); err != nil {
			r.stepErrs++
			r.printf("unstartable tasks: %v\n", err)
		}
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
		if !mine(t.Project) || r.ProjectDraining(t.Project) {
			continue
		}
		if err := r.Start(t.ID); err != nil {
			if !errors.Is(err, ErrRefused) {
				r.stepErrs++
			}
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
	pr, err := PRView(r.Tools, run.PRURL)
	if err != nil {
		r.ciPolled(run, now)
		run.MergeHold = err.Error()
		r.ciOverdue(run, now) // an unreadable PR is a pending one
		return
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
			r.ciOverdue(run, now)
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
