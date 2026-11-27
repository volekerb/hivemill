package hm

// Upstream filing (docs/option-b-runner.md#filing-upstream): a standalone runner has no row for the factory's backlog,
// which lives in another runner's database. Its planner files and follows factory tasks there over ssh.

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"factory/go/internal/shlex"
)

var (
	upstreamTargetRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._@-]*$`) // never an ssh option
	upstreamFiledRe  = regexp.MustCompile(`(?m)^(T-\d+) todo in (G-\d+)$`)
	upstreamGoalRe   = regexp.MustCompile(`^G-\d+$`)
)

const upstreamTimeout = 60 * time.Second

const upstreamFiledEvent = "upstream.filed"

// noTaskRow: an event that names a task without being part of a run of it, so the report and obs make no row for it.
func noTaskRow(event string) bool { return event == repointedEvent || event == upstreamFiledEvent }

// UpstreamTask is a task to file on the factory runner.
type UpstreamTask struct {
	Title, Spec, Acceptance string
	Priority                int // under 0: the factory runner's default
}

// upstreamTarget is config upstream, TARGET:/FACTORY_HOME.
func (r *Runner) upstreamTarget() (target, home string, err error) {
	v := strings.TrimSpace(r.Store.Cfg("upstream"))
	if v == "" {
		return "", "", fmt.Errorf("%w: config upstream is not set (`hivemill config upstream TARGET:/FACTORY_HOME`)", ErrRefused)
	}
	target, home, _ = strings.Cut(v, ":")
	if !upstreamTargetRe.MatchString(target) || !path.IsAbs(home) {
		return "", "", fmt.Errorf("%w: config upstream %q: want an ssh target, ':' and the absolute FACTORY_HOME of the factory runner",
			ErrRefused, v)
	}
	return target, home, nil
}

// upstreamRun runs one hivemill command on the factory runner. A refusal there is a refusal here, and so is a usage
// error (2) or a missing hivemill (127): in both nothing ran.
func (r *Runner) upstreamRun(target, home, stdin string, args ...string) (string, error) {
	if r.SSH == nil {
		return "", fmt.Errorf("no ssh transport")
	}
	script := "FACTORY_HOME=" + shlex.Quote(home) + "; export FACTORY_HOME; hivemill " + quoteAll(args)
	rc, out, errs := r.SSH(target, stdin, upstreamTimeout, sshCommand(script))
	if rc == 0 {
		return out, nil
	}
	err := fmt.Errorf("ssh %s: exit %d: %s", target, rc, strings.TrimSpace(errs+"\n"+out))
	if rc == 2 || rc == 3 || rc == 127 {
		err = fmt.Errorf("%w: %w", ErrRefused, err)
	}
	return out, err
}

// UpstreamAdd lints t here, files it under config upstream_goal on the factory runner and returns the new task id.
func (r *Runner) UpstreamAdd(t UpstreamTask) (string, error) {
	target, home, err := r.upstreamTarget()
	if err != nil {
		return "", err
	}
	goal := strings.ToUpper(strings.TrimSpace(r.Store.Cfg("upstream_goal")))
	if !upstreamGoalRe.MatchString(goal) {
		return "", fmt.Errorf("%w: config upstream_goal %q: set it to the factory runner's goal for these tasks (G-123)", ErrRefused, goal)
	}
	title := strings.TrimSpace(t.Title)
	if title == "" || strings.ContainsAny(title, "\r\n") || strings.TrimSpace(t.Spec) == "" {
		return "", fmt.Errorf("%w: a task needs a one-line title and a spec", ErrRefused)
	}
	if probs := FilingProblems(t.Acceptance); len(probs) > 0 {
		return "", fmt.Errorf("%w: the criteria fail the spec gate:\n  - %s", ErrRefused, strings.Join(probs, "\n  - "))
	}
	// the spec goes over stdin: it is the long text, and one argument is limited
	args := []string{"task", "add", "--goal=" + goal, "--title=" + title, "--spec-file=-", "--acceptance=" + t.Acceptance,
		"--upstream-from=" + MachineName(r.Store.Cfg("machine_name"))}
	if t.Priority >= 0 {
		args = append(args, "--priority="+strconv.Itoa(t.Priority))
	}
	out, err := r.upstreamRun(target, home, t.Spec, args...)
	m := upstreamFiledRe.FindAllStringSubmatch(out, -1)
	if len(m) == 0 {
		if err == nil {
			err = fmt.Errorf("ssh %s: no new task id in the answer: %s", target, strings.TrimSpace(out))
		}
		if errors.Is(err, ErrRefused) {
			return "", err
		}
		return "", fmt.Errorf("%w\n(the task may have been filed: check the factory runner before you file it again)", err)
	}
	// a filed id stands even when ssh failed afterwards: reporting a failure would file it twice
	id, g := m[len(m)-1][1], m[len(m)-1][2]
	if err := r.Log.Emit(upstreamFiledEvent, nil, Event{Data: map[string]any{
		"by": r.actor(), "upstream": target, "upstream_task": id, "upstream_goal": g, "title": title}}); err != nil {
		r.printf("hivemill: %s is filed, but the event was not logged: %v\n", id, err)
	}
	return id, nil
}

// UpstreamStatus is the state of a task on the factory runner: its `task show` without the spec and criteria.
func (r *Runner) UpstreamStatus(id string) (string, error) {
	target, home, err := r.upstreamTarget()
	if err != nil {
		return "", err
	}
	out, err := r.upstreamRun(target, home, "", "task", "show", id)
	if err != nil {
		return "", err
	}
	head, _, ok := strings.Cut(out, "\n\nSpec:\n")
	if !ok || !strings.HasPrefix(head, id+" ") {
		return "", fmt.Errorf("ssh %s: not a `task show` of %s: %s", target, id, firstLine(out))
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := lines[len(lines)-1]; strings.HasPrefix(last, "run: ") {
		head += "\n" + last
	}
	return head + "\n", nil
}
