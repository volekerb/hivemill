package hm

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The Go port of judge_ci_gate=none (docs/judge-ci-gate.md, "Go runner"): the project has no PR checks to read, so its
// gate_cmd on the current PR head is the CI result.

// LocalCI is the last gate_cmd result of a run on a project with ci_gate none. It counts only for its own head.
type LocalCI struct {
	Head string `json:"head"`
	RC   int    `json:"rc"`
	Tail string `json:"tail"`
	// Private: the public-text guard did not pass the whole log, so no part of it goes on the PR.
	Private bool `json:"private,omitempty"`
}

// ciPending is Decide's hold reason for a head without a CI result.
const ciPending = "CI is pending on the head"

// ciRed is Decide's resume reason for a head with a red CI result.
const ciRed = "CI is red on the head"

// ciMode: the project's ci_gate, else the factory's judge_ci_gate, is "none". Any other value keeps the PR checks. An
// error means the project's override could not be read: whoever decides on CI or a merge holds then.
func (r *Runner) ciMode(project string) (bool, error) {
	v := ""
	if s, ok := r.Store.(*SQLite); ok {
		err := s.db.QueryRow(`SELECT ci_gate FROM projects WHERE id=?`, project).Scan(&v)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("cannot read ci_gate of project %s: %w", project, err)
		}
	}
	if strings.TrimSpace(v) == "" {
		v = r.Store.FactoryCfg("judge_ci_gate")
	}
	return strings.EqualFold(strings.TrimSpace(v), "none"), nil
}

// ciNone is ciMode for wording and extras, where an unreadable override reads as pr.
func (r *Runner) ciNone(project string) bool {
	none, _ := r.ciMode(project)
	return none
}

// verdict is the CI verdict of one head in the words of CIVerdict.
func (c *LocalCI) verdict(head string) (string, []string) {
	switch {
	case c == nil || c.Head != head:
		return "pending", nil
	case c.RC == 0:
		return "green", nil
	}
	return "red", []string{fmt.Sprintf("gate_cmd exit %d", c.RC)}
}

// localCI makes sure gate_cmd has a result on head: it keeps the recorded one, else fetches the branch and runs it.
func (r *Runner) localCI(run *Run, head string) error {
	if run.LocalCI != nil && run.LocalCI.Head == head {
		return nil
	}
	cmd, limit := r.projectGate(run.Project)
	if strings.TrimSpace(cmd) == "" {
		return errors.New("ci_gate none and the project has no gate_cmd: never merged untested")
	}
	r.Tools.Git(run.Worktree, "fetch", "-q", "origin", run.Branch)
	logdir := r.logDir(run)
	_ = os.MkdirAll(logdir, 0o700)
	stop := r.keepAlive(time.Minute)
	defer stop()
	if _, _, err := r.recordLocalCI(run, head, cmd, limit, logdir); err != nil {
		return err
	}
	_ = r.Log.Emit("ci_gate.gate_cmd", run, Event{Data: map[string]any{"head": head, "exit": run.LocalCI.RC}})
	return nil
}

// keepAlive pings the watchdog and the heartbeat while gate_cmd runs inside a pass: gate_cmd may take longer than both
// allow, and its own time limit bounds it.
func (r *Runner) keepAlive(every time.Duration) (stop func()) {
	done, ended := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(ended)
		t := time.NewTicker(every)
		defer t.Stop()
		for {
			sdNotify("WATCHDOG=1")
			if r.inDaemon { // a command run by hand must not make a dead daemon look healthy
				_ = r.Heartbeat(time.Now())
			}
			select {
			case <-done:
				return
			case <-t.C:
			}
		}
	}()
	return func() { close(done); <-ended }
}

// recordLocalCI runs gate_cmd and keeps its result for head. As in the old chain (docs/local-gate.md) it runs in a fresh
// detached worktree of exactly that commit, removed afterwards: nothing uncommitted or untracked in the task clone can
// make it pass, and the clone itself is never touched. An error means gate_cmd did not run and nothing is recorded.
func (r *Runner) recordLocalCI(run *Run, head, cmd string, limit time.Duration, logdir string) (bool, string, error) {
	clone, parent := run.Worktree, filepath.Join(r.Env.StateDir(), "gate")
	_ = os.MkdirAll(parent, 0o700)
	// one directory per call: no other gate can remove it under this one
	dir, err := os.MkdirTemp(parent, run.Task+"-")
	if err != nil {
		return false, "", fmt.Errorf("gate_cmd: %w", err)
	}
	defer func() {
		r.Tools.Git(clone, "worktree", "remove", "--force", dir)
		_ = os.RemoveAll(dir)
		r.Tools.Git(clone, "worktree", "prune") // and what an interrupted call left behind
	}()
	if rc, _, errs := r.Tools.Git(clone, "worktree", "add", "-q", "--detach", dir, head); rc != 0 {
		return false, "", fmt.Errorf("gate_cmd: cannot check out head %.12s: %s", head, firstLine(errs))
	}
	if _, at, _ := r.Tools.Git(dir, "rev-parse", "HEAD"); at != head {
		return false, "", fmt.Errorf("gate_cmd: the checkout is at %.12s, not at the head %.12s", at, head)
	}
	rc, text, note := r.execProjectGate(dir, cmd, limit, logdir)
	// the checkout's name differs on every call: an unchanged failure must read the same, or it looks new each time
	text, note = strings.ReplaceAll(text, dir, "<checkout>"), strings.ReplaceAll(note, dir, "<checkout>")
	// the whole log is checked: the tail alone may have lost the line that marks it as private
	private := true
	if args, why := r.guardArgs(); why == "" {
		refused, _ := r.guardText(args, r.redactHomes(text))
		private = refused != 0
	}
	run.LocalCI = &LocalCI{Head: head, RC: rc, Tail: r.commentTail(text), Private: private}
	return rc == 0, note, nil
}

// redactHomes removes the App Store Connect key and the local home paths.
func (r *Runner) redactHomes(text string) string {
	text = ScrubSecrets(text) // before the paths: a rewritten path inside a secret would hide it
	// the factory home first: it is usually inside the user home
	for _, p := range [][2]string{{r.Env.Home, "<factory>"}, {r.Env.UserHome, "~"}} {
		if len(p[0]) > 1 {
			text = strings.ReplaceAll(text, p[0], p[1])
		}
	}
	return text
}

// redCI handles a red CI result on the head: the task session resumes with what failed, up to ci_rounds_max times.
func (r *Runner) redCI(run *Run, failed []string) {
	run.CIRounds++
	_ = r.Log.Emit("ci.red", run, Event{Data: map[string]any{"failed": failed, "ci_round": run.CIRounds}})
	if run.CIRounds > cfgInt(r.Store, "ci_rounds_max") {
		r.fail(run, fmt.Sprintf("CI red %d times: %s", run.CIRounds, strings.Join(failed, ", ")))
		return
	}
	run.History = append(run.History, fmt.Sprintf("round %d: CI red (%s)", run.Round, strings.Join(failed, ", ")))
	if r.ciNone(run.Project) && run.LocalCI != nil {
		r.resumeAs(run, "ci", "The project gate command (this project's CI) failed on your PR head: "+strings.Join(failed, ", ")+
			". Its log ends:\n"+run.LocalCI.Tail+"\nFix it, "+r.pushAndStop(run.Project)+".")
		return
	}
	r.resumeAs(run, "ci", "CI is red on your PR: "+strings.Join(failed, ", ")+". Read the failing log (`gh run view --log-failed`), "+
		"fix it, push, wait for CI once with one blocking command, and stop.")
}

func (r *Runner) logDir(run *Run) string {
	return filepath.Join(r.Env.StateDir(), "runs", run.Task, fmt.Sprintf("round-%d", run.Round))
}

// commentTail is the end of the gate log as it may appear on the PR: bounded, and without the local home paths.
func (r *Runner) commentTail(text string) string {
	// a bare CR also ends a markdown line, and only LF-ended lines get indented into the code block
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	tail := lastLines(r.redactHomes(text), 20)
	if len(tail) > 4000 {
		// cut at a line start: half a line could hide a blocked term from the public-text guard
		var whole bool
		if _, tail, whole = strings.Cut(tail[len(tail)-4001:], "\n"); !whole || strings.TrimSpace(tail) == "" {
			tail = "(the log ends in a line too long to quote: see gate-cmd.log)"
		}
	}
	return strings.ToValidUTF8(tail, "")
}

// postLocalCI puts one PR comment per head and exit code: the code, the head SHA and the log tail. The repo may be
// public, so a tail that the public-text guard refuses is left out. A failed post is tried again by a later pass
// only while this is still the run's latest result.
func (r *Runner) postLocalCI(run *Run) {
	c := run.LocalCI
	if c == nil || run.Eval || run.PRURL == "" {
		return
	}
	key := fmt.Sprintf("%s:%d", c.Head, c.RC)
	if run.CIPosted == key {
		return
	}
	body := fmt.Sprintf("Project gate (no PR checks, ci_gate none): exit %d on head %s.\n", c.RC, c.Head)
	if strings.TrimSpace(c.Tail) != "" {
		args, why := r.guardArgs()
		rc := 1
		if why == "" {
			rc, _ = r.guardText(args, c.Tail)
		}
		if rc == 0 && !c.Private {
			body += "\nLog tail:\n\n    " + strings.ReplaceAll(c.Tail, "\n", "\n    ") + "\n"
		} else {
			body += "\nLog tail left out: the public-text guard did not pass it.\n"
		}
	}
	if rc, _, errs := r.Tools.GH(body, "pr", "comment", run.PRURL, "--body-file", "-"); rc != 0 {
		r.printf("%s: PR comment failed: %s\n", run.Task, firstLine(errs))
		return
	}
	run.CIPosted = key
}

// pushAndStop is how a resumed session ends its round: without PR checks there is no CI to wait for.
func (r *Runner) pushAndStop(project string) string {
	if r.ciNone(project) {
		return "push and stop (this project has no PR checks to wait for)"
	}
	return "push, wait for CI once and stop"
}
