package hm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/shlex"
)

// A release task has no coding session and no run row: the runner itself runs the project's release_cmd in a clean
// detached worktree of origin/<base>, first with --dry-run, then for real. The task goes todo -> in_progress -> done,
// or blocked on the owner. It is never started again by itself.

// KindRelease is the task kind of a release task ("" is a coding task).
const KindRelease = "release"

const (
	releaseQueued  = "queued"
	releaseRunning = "running"
	releaseDone    = "done"
	releaseFailed  = "failed"

	releaseTimeout   = 2 * time.Hour // each of the two release_cmd calls: an archive, an upload and Apple's processing
	releaseTailLines = 40
)

func (r *Runner) releaseDir(task string) string {
	return filepath.Join(r.Env.StateDir(), "release", task)
}

// ReleaseLog is the redacted output of the task's last release.
func (r *Runner) ReleaseLog(task string) string {
	return filepath.Join(r.releaseDir(task), "release.log")
}

// releasePgid holds the process group of the release_cmd that runs now.
func (r *Runner) releasePgid(task string) string {
	return filepath.Join(r.releaseDir(task), "pgid")
}

// releaseOrphaned is true while the release_cmd of a dead job still has processes: they may still upload. A file older
// than releaseTimeout no longer counts, so a reused process group id cannot hold a release forever.
func (r *Runner) releaseOrphaned(task string) bool {
	fi, err := os.Stat(r.releasePgid(task))
	if err != nil || time.Since(fi.ModTime()) > releaseTimeout {
		return false
	}
	b, err := os.ReadFile(r.releasePgid(task))
	pgid, perr := strconv.Atoi(string(b))
	return err == nil && perr == nil && pgid > 1 && syscall.Kill(-pgid, 0) == nil
}

// HoldRelease takes the task lock of a release task whose release is neither queued nor running, so that a status change
// cannot cross a start. The caller closes the lock after its change.
func (r *Runner) HoldRelease(task string) (io.Closer, error) {
	s, err := r.sq()
	if err != nil {
		return nil, err
	}
	l := r.lock(task, false)
	if l == nil {
		return nil, fmt.Errorf("%w: %s is busy; try again", ErrRefused, task)
	}
	state, _, err := s.Release(task)
	if err == nil && (state == releaseQueued || state == releaseRunning) {
		err = fmt.Errorf("%w: %s has a release %s; wait until it ends", ErrRefused, task, state)
	}
	if err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}

func (r *Runner) dropReleaseWorktree(t *Task) {
	wt := filepath.Join(r.Env.Home, "worktrees", "release-"+t.ID)
	r.Tools.Git(t.RepoPath, "worktree", "remove", "--force", wt)
	_ = os.RemoveAll(wt)
	r.Tools.Git(t.RepoPath, "worktree", "prune")
}

func (r *Runner) releaseLock(task string) string {
	return filepath.Join(r.releaseDir(task), "job.lock")
}

// startRelease is Start for a release task: it claims the task and starts the job that RunRelease is.
func (r *Runner) startRelease(t *Task) error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	if r.Draining() {
		return fmt.Errorf("%w: drain mode is on (`hivemill resume` lifts it)", ErrRefused)
	}
	cmd, err := s.ReleaseCmd(t.Project)
	if err != nil {
		return err
	}
	if cmd == "" {
		return fmt.Errorf("%w: %s: project %s has no release_cmd (`hivemill project profile %s --release-cmd CMD`)", ErrRefused,
			t.ID, t.Project, t.Project)
	}
	if t.RepoPath == "" {
		return fmt.Errorf("%w: %s: its project has no repo_path", ErrRefused, t.ID)
	}
	// one release per project: two would take build numbers from each other
	pl := r.lock("release-"+t.Project, false)
	if pl == nil {
		return fmt.Errorf("%w: %s: another release of %s is starting", ErrRefused, t.ID, t.Project)
	}
	defer pl.Close()
	l := r.lock(t.ID, false)
	if l == nil {
		return fmt.Errorf("%w: %s is busy; try again", ErrRefused, t.ID)
	}
	defer l.Close()
	if status, _, err := s.TaskState(t.ID); err != nil {
		return err
	} else if status != "todo" {
		return fmt.Errorf("%w: %s is %s; the runner starts a todo release task", ErrRefused, t.ID, status)
	}
	live, err := s.LiveReleases()
	if err != nil {
		return err
	}
	for _, id := range live {
		if other, err := s.Task(id); err == nil && other.Project == t.Project {
			return fmt.Errorf("%w: %s: %s is releasing %s now", ErrRefused, t.ID, id, t.Project)
		}
	}
	if err := os.MkdirAll(r.releaseDir(t.ID), 0o700); err != nil {
		return err
	}
	// a job that dies before it writes must not be reported with an earlier attempt's log or process group
	for _, f := range []string{r.ReleaseLog(t.ID), r.releasePgid(t.ID)} {
		if err := os.Remove(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := s.SetRelease(t.ID, releaseQueued, ""); err != nil {
		return err
	}
	if err := s.TaskStatus(t.ID, "in_progress", ""); err != nil {
		return err
	}
	r.releaseEvent(t, "release.started", nil)
	r.printf("%s: release of %s started; log %s\n", t.ID, t.Project, r.ReleaseLog(t.ID))
	if r.Tools.Detach == nil { // tests: run it here
		l.Close()
		return r.RunRelease(t.ID)
	}
	exe, err := os.Executable()
	if err == nil {
		env := append(cleanEnv(), "FACTORY_HOME="+r.Env.Home)
		_, err = r.Tools.Detach(r.Env.Home, filepath.Join(r.releaseDir(t.ID), "job.log"), r.releaseLock(t.ID), env,
			exe, "release-run", t.ID)
	}
	if err != nil {
		// the task lock is held here already
		if rerr := r.recordRelease(t, releaseResult{why: "the release job did not start: " + err.Error(), exit: -1}); rerr != nil {
			return errors.Join(err, rerr)
		}
		return fmt.Errorf("%s: the release job did not start: %w", t.ID, err)
	}
	return nil
}

// startReleases starts the release tasks that are ready. They take no session slot and no model usage, so neither the run
// caps nor the usage floors hold them.
func (r *Runner) startReleases() {
	cands, err := r.Candidates()
	if err != nil {
		r.printf("releases: %v\n", err)
		return
	}
	for _, t := range cands {
		if t.Kind != KindRelease {
			continue
		}
		if err := r.Start(t.ID); err != nil {
			r.printf("%s: %v\n", t.ID, err)
		}
	}
}

// sweepReleases fails the releases whose job is gone. It never starts one again: the build may already be uploaded.
func (r *Runner) sweepReleases() {
	s, err := r.sq()
	if err != nil {
		return
	}
	live, err := s.LiveReleases()
	if err != nil {
		r.printf("releases: %v\n", err)
		return
	}
	for _, id := range live {
		l := r.lock(id, false)
		if l == nil {
			continue
		}
		state, _, err := s.Release(id)
		if t, terr := s.Task(id); err == nil && terr == nil && (state == releaseQueued || state == releaseRunning) &&
			!lockHeld(r.releaseLock(id)) && !r.releaseOrphaned(id) {
			r.dropReleaseWorktree(t)
			b, _ := os.ReadFile(r.ReleaseLog(id))
			res := releaseResult{why: "the release job died; check App Store Connect for an uploaded build before a new release",
				exit: -1, tail: lastLines(string(b), releaseTailLines)}
			if err := r.recordRelease(t, res); err != nil {
				r.printf("%s: %v\n", id, err)
			}
		}
		l.Close()
	}
}

// releaseResult is how one release ended: ok with the summary, or why not with the exit code and the last log lines.
type releaseResult struct {
	ok      bool
	exit    int
	why     string
	tail    string
	summary map[string]any
}

// RunRelease is the release job of one queued release task (`hivemill release-run`).
func (r *Runner) RunRelease(id string) error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	t, err := s.Task(id)
	if err != nil {
		return err
	}
	claimed, err := s.ClaimRelease(id)
	if err != nil {
		return err
	}
	if !claimed {
		return fmt.Errorf("%w: %s is not a queued release", ErrRefused, id)
	}
	res := r.release(t)
	if l := r.lock(id, true); l != nil {
		defer l.Close()
	}
	return r.recordRelease(t, res)
}

// recordRelease writes the outcome; the caller holds the task lock. A failure asks the owner and gives the planner a work
// item. The status changes only while the task is still in_progress: an owner's change stands.
func (r *Runner) recordRelease(t *Task, res releaseResult) error {
	s, err := r.sq()
	if err != nil {
		return err
	}
	result := ""
	if res.summary != nil {
		b, err := json.Marshal(res.summary)
		if err != nil {
			return err
		}
		result = redactRelease(string(b))
	}
	state, status, waits, event := releaseDone, "done", "", "release.done"
	if !res.ok {
		state, status, waits, event = releaseFailed, "blocked", "owner", "release.failed"
	}
	if cur, _, err := s.TaskState(t.ID); err == nil && cur == "in_progress" {
		if err := s.TaskStatus(t.ID, status, waits); err != nil {
			return err
		}
	}
	// The release state is written last: until then it is live, and sweepReleases repairs a record that stopped half way.
	if res.ok {
		r.releaseEvent(t, event, map[string]any{"build": res.summary["build"]})
		r.printf("%s: release done: %s\n", t.ID, result)
		return s.SetRelease(t.ID, state, result)
	}
	why := redactRelease(res.why)
	r.releaseEvent(t, event, map[string]any{"why": why, "exit": res.exit})
	r.printf("%s: release failed: %s\n", t.ID, why)
	exit := ""
	if res.exit >= 0 {
		exit = fmt.Sprintf(" (exit %d)", res.exit)
	}
	q := fmt.Sprintf("%s: the release of %s failed%s: %s\nLog: %s", t.ID, t.Project, exit, why, r.ReleaseLog(t.ID))
	if res.tail != "" {
		q += fmt.Sprintf("\nLast %d lines:\n%s", releaseTailLines, res.tail)
	}
	askID, added, err := s.AddAsk(Ask{Project: t.Project, Goal: t.Goal, Task: t.ID, Question: q, AskedBy: "runner"})
	if err != nil {
		return err
	}
	if added {
		_ = r.Log.Emit("ask.added", nil, Event{Project: t.Project, Data: map[string]any{"id": askID, "by": "runner", "task": t.ID}})
	}
	// the kind of the release.failed wake row, so one failure is one work item
	if err := s.EnqueuePlannerWork(event, t.ID); err != nil {
		return err
	}
	return s.SetRelease(t.ID, state, result)
}

func (r *Runner) releaseEvent(t *Task, name string, data map[string]any) {
	_ = r.Log.Emit(name, nil, Event{TaskID: t.ID, GoalID: t.Goal, Project: t.Project, Data: data})
	_ = r.Store.Event(r.actor(), "task", t.ID, name, data)
}

// release runs the project's release_cmd in a fresh worktree: dry run, then for real.
func (r *Runner) release(t *Task) releaseResult {
	failed := func(why string, args ...any) releaseResult {
		return releaseResult{why: fmt.Sprintf(why, args...), exit: -1}
	}
	s, err := r.sq()
	if err != nil {
		return failed("%v", err)
	}
	if err := os.MkdirAll(r.releaseDir(t.ID), 0o700); err != nil {
		return failed("%v", err)
	}
	logf, err := os.OpenFile(r.ReleaseLog(t.ID), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return failed("%v", err)
	}
	defer logf.Close()
	var all strings.Builder
	logText := func(text string) {
		text = redactRelease(strings.TrimRight(text, "\n")) + "\n"
		all.WriteString(text)
		_, _ = logf.WriteString(text)
	}
	cmd, err := s.ReleaseCmd(t.Project)
	if err != nil {
		return failed("%v", err)
	}
	argv, err := releaseArgv(cmd, r.Env.UserHome)
	if err != nil {
		return failed("release_cmd: %v", err)
	}
	asks, err := s.Asks("answered")
	if err != nil {
		return failed("%v", err)
	}
	// only answers to this task's questions: an approval given for something else never submits
	asks = slices.DeleteFunc(asks, func(a Ask) bool { return a.Task != t.ID })
	expanded := strings.Join(argv, " ")
	if r.Env.UserHome != "" { // the directory's own name is not part of the command
		expanded = strings.ReplaceAll(expanded, r.Env.UserHome, "~")
	}
	submit := false
	for _, c := range []string{cmd, expanded} {
		action, _ := ReleaseAction(c)
		submit = submit || action || strings.Contains(strings.ToLower(c), "submit")
	}
	// a command that submits does not run at all, not even dry, before the owner has allowed some build
	if submit && !anyReleaseAllowed(asks) {
		return failed("refused: release_cmd names submit; an App Store submit needs the owner's answer \"release build N\"")
	}

	g := r.Tools.Git
	if rc, _, errs := g(t.RepoPath, "fetch", "--quiet", "origin"); rc != 0 {
		logText(errs)
		return releaseResult{why: "git fetch origin failed", exit: rc, tail: lastLines(all.String(), releaseTailLines)}
	}
	wt := filepath.Join(r.Env.Home, "worktrees", "release-"+t.ID)
	r.dropReleaseWorktree(t) // what a job that died left behind
	defer r.dropReleaseWorktree(t)
	if err := os.MkdirAll(filepath.Dir(wt), 0o700); err != nil {
		return failed("%v", err)
	}
	if rc, _, errs := g(t.RepoPath, "worktree", "add", "--quiet", "--detach", wt, "origin/"+t.Base); rc != 0 {
		logText(errs)
		return releaseResult{why: "git worktree add of origin/" + t.Base + " failed", exit: rc,
			tail: lastLines(all.String(), releaseTailLines)}
	}
	external, err := submitExternal(argv, wt)
	if err != nil {
		return failed("refused: %v", err)
	}

	env := cleanEnv()
	step := func(args ...string) (int, map[string]any) {
		full := append(slices.Clone(argv), args...)
		// release_cmd as configured: what its variables expand to may be a secret
		logText("$ " + strings.Join(append([]string{cmd}, args...), " "))
		rc, out, errs := runReleaseCmd(releaseTimeout, wt, env, full, r.releasePgid(t.ID))
		logText(errs)
		logText(out)
		logText(fmt.Sprintf("exit %d", rc))
		return rc, releaseSummary(out) // the summary is on stdout; stderr is the log
	}
	stepFailed := func(name string, rc int, sum map[string]any) releaseResult {
		why := name + " failed"
		if e, _ := sum["error"].(string); e != "" {
			why += ": " + e
		} else if sum == nil {
			why += ": no JSON summary line"
		}
		return releaseResult{why: why, exit: rc, tail: lastLines(all.String(), releaseTailLines), summary: sum}
	}
	rc, dry := step("--dry-run")
	if rc != 0 || dry["ok"] != true {
		return stepFailed("the dry run", rc, dry)
	}
	approved := "" // the build number the owner allowed to submit; the real run may cut no other
	if submit || external {
		build := ""
		if n, ok := dry["build"].(float64); ok {
			build = fmt.Sprintf("%.0f", n)
		}
		if !ReleaseAllowed(asks, build) {
			what := "the config sets submitExternal true"
			if submit {
				what = "release_cmd names submit"
			}
			if build == "" {
				return failed("refused: %s and the dry run names no build number", what)
			}
			return failed("refused: %s; a submit of build %s needs the owner's answer \"release build %s\"", what, build, build)
		}
		approved = build
		env = append(env, "HIVEMILL_RELEASE_BUILD="+approved)
	}
	rc, sum := step()
	if rc != 0 || sum["ok"] != true || sum["build"] == nil {
		return stepFailed("the release", rc, sum)
	}
	if n, _ := sum["build"].(float64); approved != "" && fmt.Sprintf("%.0f", n) != approved {
		return releaseResult{why: fmt.Sprintf("the owner allowed build %s but release_cmd cut build %v; check App Store Connect",
			approved, sum["build"]), exit: rc, tail: lastLines(all.String(), releaseTailLines), summary: sum}
	}
	sum["state"] = "VALID" // release_cmd ends ok only once App Store Connect has processed the build
	return releaseResult{ok: true, summary: sum}
}

// runReleaseCmd runs release_cmd in its own process group, so a timeout also kills the build and upload it started.
func runReleaseCmd(timeout time.Duration, dir string, env, argv []string, pgidFile string) (int, string, string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 30 * time.Second
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Start()
	if err == nil {
		if werr := os.WriteFile(pgidFile, []byte(strconv.Itoa(cmd.Process.Pid)), 0o600); werr != nil {
			_ = cmd.Cancel() // without the file a dead job's processes could not be seen: do not go on
			err = werr
		}
		if werr := cmd.Wait(); err == nil {
			err = werr
		}
		_ = os.Remove(pgidFile)
	}
	if ctx.Err() == context.DeadlineExceeded {
		return 124, out.String(), errb.String() + fmt.Sprintf("\ntimed out after %s", timeout)
	}
	var ee *exec.ExitError
	switch {
	case errors.As(err, &ee):
		return ee.ExitCode(), out.String(), errb.String()
	case err != nil && !errors.Is(err, exec.ErrWaitDelay): // ErrWaitDelay: it exited 0, a child kept a pipe open
		return 127, out.String(), errb.String() + "\n" + err.Error()
	}
	return 0, out.String(), errb.String()
}

// CheckReleaseCmd says why cmd cannot be a release_cmd.
func CheckReleaseCmd(cmd string) error {
	_, err := releaseArgv(cmd, "/")
	return err
}

// releaseArgv splits release_cmd; it runs without a shell, with a leading ~/ and variables expanded.
func releaseArgv(cmd, userHome string) ([]string, error) {
	if strings.ContainsAny(cmd, ";&|<>`\n") || strings.Contains(cmd, "$(") {
		return nil, errors.New("it runs without a shell: no operators, redirects or substitutions")
	}
	argv, err := shlex.Split(cmd)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, errors.New("empty")
	}
	for i, a := range argv {
		a = os.Expand(a, func(k string) string {
			if k == "HOME" {
				return userHome
			}
			return os.Getenv(k)
		})
		flag := ""
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			flag, a = "--config=", v
		}
		if rest, ok := strings.CutPrefix(a, "~/"); ok {
			a = filepath.Join(userHome, rest)
		}
		a = flag + a
		argv[i] = a
	}
	return argv, nil
}

// submitExternal reads the release config the command will use (--config, else .hivemill/ios-release.json of the
// worktree). A missing default file is false; a missing --config file, or one that cannot be read as JSON, is an error,
// since nobody can tell what it asks.
func submitExternal(argv []string, wt string) (bool, error) {
	paths := []string{}
	for i, a := range argv {
		if v, ok := strings.CutPrefix(a, "--config="); ok {
			paths = append(paths, v)
		} else if a == "--config" && i+1 < len(argv) {
			paths = append(paths, argv[i+1])
		}
	}
	explicit := len(paths) > 0
	if !explicit {
		paths = []string{filepath.Join(".hivemill", "ios-release.json")}
	}
	for _, p := range paths {
		if !filepath.IsAbs(p) {
			p = filepath.Join(wt, p)
		}
		b, err := os.ReadFile(p)
		if !explicit && errors.Is(err, fs.ErrNotExist) {
			continue
		}
		var c map[string]any
		if err == nil {
			err = json.Unmarshal(b, &c)
		}
		if err != nil {
			return false, fmt.Errorf("the release config %s cannot be read: %v", p, err)
		}
		if v, ok := c["submitExternal"]; ok && v != false {
			return true, nil
		}
	}
	return false, nil
}

// anyReleaseAllowed is true when the owner has answered "release build N" for some N.
func anyReleaseAllowed(asks []Ask) bool {
	for _, a := range asks {
		for _, m := range releaseBuildRe.FindAllStringSubmatch(a.Answer, -1) {
			if ReleaseAllowed([]Ask{a}, m[1]) {
				return true
			}
		}
	}
	return false
}

// releaseSummary is the last line of the output that is a JSON object with "ok" (nil without one).
func releaseSummary(out string) map[string]any {
	lines := strings.Split(out, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var m map[string]any
		if json.Unmarshal([]byte(strings.TrimSpace(lines[i])), &m) == nil {
			if _, ok := m["ok"]; ok {
				return m
			}
		}
	}
	return nil
}

// The rules of the release tool's redact.mjs.
var (
	redactPEMRe = regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----|` +
		`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*$`)
	redactJWTRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
)

const redactMask = "[REDACTED]"

// redactRelease masks private-key blocks, JWTs, and the credential values of this environment with their base names.
func redactRelease(text string) string {
	text = redactJWTRe.ReplaceAllString(redactPEMRe.ReplaceAllString(text, redactMask), redactMask)
	values := strings.Split(os.Getenv("HIVEMILL_REDACT_VALUES"), "\n")
	values = append(values, os.Getenv("ASC_KEY_ID"), os.Getenv("ASC_ISSUER_ID"), os.Getenv("ASC_KEY_PATH"))
	for _, v := range values {
		if strings.Contains(v, "/") {
			values = append(values, filepath.Base(v))
		}
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) }) // a path wins over its base name
	for _, v := range values {
		if v != "" && v != "/" {
			text = strings.ReplaceAll(text, v, redactMask)
		}
	}
	return text
}
