package hm

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// reconcile compares a run with what is really there and repairs it: a lost session is relaunched with
// `claude --resume` (at most resume_max times), a missing clone is recreated from the branch, and a PR merged or closed
// by hand moves the run on. It returns true when it changed the run's course in this pass.
func (r *Runner) reconcile(run *Run, list []Terminal, listed bool) bool {
	var fixed []string
	if run.Worktree != "" && Active(run.State) {
		if _, err := os.Stat(filepath.Join(run.Worktree, ".git")); os.IsNotExist(err) {
			if t, err := r.Store.Task(run.Task); err == nil {
				if wt, err := r.Clone(t, run.Branch); err == nil {
					run.Worktree = wt
					fixed = append(fixed, "clone recreated from "+run.Branch)
				}
			}
		}
	}
	if (run.State == Working || run.State == Judging) && run.PRURL != "" {
		if pr, err := PRView(r.Tools, run.PRURL); err == nil && pr.State != "OPEN" {
			if pr.State == "MERGED" && !run.Eval {
				r.finish(run, pr)
				fixed = append(fixed, "PR merged by hand")
			} else {
				r.fail(run, "the PR was closed by hand")
				fixed = append(fixed, "PR closed by hand")
			}
		}
	}
	name := ""
	switch run.State {
	case Working:
		name = run.Agent
	case Judging:
		name = last(run.Judges)
	}
	// a task session on a worker that hc lists as disconnected is not lost: hc keeps it and lists it again on reconnect
	down := name == run.Agent && run.Machine != "" && machineDown(r.Tools, run.Machine)
	if name != "" && listed && !down && !alive(list, true, name) {
		if run.Resumes >= cfgInt(r.Store, "resume_max") {
			r.fail(run, fmt.Sprintf("session lost: %s is gone after %d resumes", name, run.Resumes))
			fixed = append(fixed, "session lost, run failed")
		} else if err := r.resumeSession(run, name, ""); err != nil {
			fixed = append(fixed, "resume of "+name+" failed: "+firstLine(err.Error()))
		} else {
			run.Resumes++
			fixed = append(fixed, "session "+name+" resumed")
		}
	}
	if len(fixed) == 0 {
		return false
	}
	_ = r.Log.Emit("run.reconciled", run, Event{Data: map[string]any{"repaired": fixed}})
	_ = r.Store.Event(r.actor(), "task", run.Task, "run-reconciled", strings.Join(fixed, "; "))
	return true
}

// sessionID is the id of the last session recorded for the agent ("": none).
func (r *Runner) sessionID(task, name string) (sid string) {
	sess, _ := r.Store.Sessions(task)
	for _, s := range sess {
		if s.Agent == name {
			sid = s.SID
		}
	}
	return sid
}

// resumeSession relaunches a task or judge session in a new hc terminal with the same settings and model. text is its
// first message; empty says that the session was lost.
func (r *Runner) resumeSession(run *Run, name, text string) error {
	sess, _ := r.Store.Sessions(run.Task)
	sid, model, effort := r.sessionID(run.Task, name), run.Model, run.Effort
	for _, s := range sess {
		if s.Agent == name && s.Model != "" {
			model, effort = s.Model, s.Effort
		}
	}
	if sid == "" {
		return fmt.Errorf("no session id recorded for %s", name)
	}
	role, prompt, machine := "task", "task.md", run.Machine // the transcript and the trust entry are there
	kick := "The factory restarted your session after it was lost. Continue your role steps from where you were."
	var env map[string]string
	if strings.Contains(name, "judge") {
		role, prompt, machine = "task-judge", "task-judge.md", "" // the judge stays on the local core
		kick = "The factory restarted your session after it was lost. Finish your verdict with the JSON block."
	} else {
		env = r.taskEnv()
	}
	if text != "" {
		kick = text
	}
	_, err := r.StartSession(run, Launch{Role: role, Name: name, Cwd: run.Worktree, Model: model, Effort: effort,
		PromptFile: filepath.Join(r.Env.Home, "roles", "hivemill", prompt), Kickoff: kick, Env: env, Resume: sid,
		Machine: machine})
	return err
}

// Draining reports drain mode: no new dispatch, active runs finish.
func (r *Runner) Draining() bool { return r.Store.Meta("drain") == "on" }

// SetDrain turns drain mode on or off.
func (r *Runner) SetDrain(on bool) error {
	v := "off"
	if on {
		v = "on"
	}
	return r.Store.SetMeta("drain", v)
}

// Heartbeat records the end of a good pass.
func (r *Runner) Heartbeat(now time.Time) error {
	return r.Store.SetMeta("heartbeat", strconv.FormatInt(now.Unix(), 10))
}

// Health is (ok, age of the last good pass). No heartbeat yet is not ok.
func (r *Runner) Health(now time.Time) (bool, time.Duration) {
	ts, err := strconv.ParseInt(r.Store.Meta("heartbeat"), 10, 64)
	if err != nil {
		return false, -1
	}
	age := now.Sub(time.Unix(ts, 0))
	return age <= time.Duration(cfgInt(r.Store, "health_max_age_s"))*time.Second, age
}

// HealthAlert raises the dead-man alert, at most once per alert_every_s: one owner alert feed row and one owner alert
// (Alert: the alert log, then alert_cmd or a macOS notification).
func (r *Runner) HealthAlert(now time.Time, age time.Duration) (bool, error) {
	if last, err := strconv.ParseInt(r.Store.Meta("alerted"), 10, 64); err == nil &&
		now.Sub(time.Unix(last, 0)) < time.Duration(cfgInt(r.Store, "alert_every_s"))*time.Second {
		return false, nil
	}
	what := "no hivemill pass has completed yet"
	if age >= 0 {
		what = fmt.Sprintf("the last hivemill pass ended %s ago", age.Round(time.Second))
	}
	link, check := "journalctl --user -u hivemill.service", "Check `systemctl --user status hivemill.service` and "+
		"`journalctl --user -u hivemill.service -n 50`."
	if GOOS == "darwin" {
		log := filepath.Join(r.Env.Home, "logs", "hivemill.daemon.log")
		link, check = log, "Check `launchctl print gui/$(id -u)/hivemill.daemon` and "+log+"."
	}
	if err := r.Store.AlertFeed("hivemill-dead", "hivemill", link); err != nil {
		return false, err
	}
	if _, err := r.Alert("hivemill runner is down", what+". "+check+" Active task sessions keep running in their hc terminals."); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: alert: %v\n", err) // the feed row is open; the mark below still rate-limits
	}
	_ = r.Log.Emit("health.alert", nil, Event{Data: map[string]any{"why": what}})
	return true, r.Store.SetMeta("alerted", strconv.FormatInt(now.Unix(), 10))
}

// sdNotify sends one state line to systemd (Type=notify); a no-op outside systemd.
func sdNotify(state string) {
	sock := os.Getenv("NOTIFY_SOCKET")
	if sock == "" {
		return
	}
	if strings.HasPrefix(sock, "@") {
		sock = "\x00" + sock[1:]
	}
	c, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: sock, Net: "unixgram"})
	if err != nil {
		return
	}
	defer c.Close()
	_, _ = c.Write([]byte(state))
}

// Daemon runs the pass loop in-process until ctx ends (SIGTERM): one pass every pass_interval_s, a heartbeat and a
// watchdog ping after each pass. Only one daemon runs per factory home (its lock).
func (r *Runner) Daemon(ctx context.Context) error {
	l := r.lock("daemon", false)
	if l == nil {
		return fmt.Errorf("%w: another hivemill daemon holds the lock", ErrRefused)
	}
	defer l.Close()
	r.inDaemon = true
	if err := r.WriteGuardExport(time.Now()); err != nil { // at start, even when another process holds the pass lock
		r.printf("guard export: %v\n", err)
	}
	go r.keepGuardExport(ctx) // a pass can take longer than the export's 15-minute age limit
	sdNotify("READY=1")
	for {
		if err := r.Pass(true); err != nil {
			r.printf("pass: %v\n", err)
		} else {
			_ = r.Heartbeat(time.Now())
		}
		r.PlannerPass(time.Now()) // held runs to the owner queue; keep the planner alive and wake it on events
		r.OtelTail(time.Now())    // otel_endpoint set: finished tasks go out as traces (never fails the pass)
		if r.BackupDue(time.Now()) {
			if _, err := r.Backup(time.Now()); err != nil {
				r.printf("backup: %v\n", err)
			}
		}
		sdNotify("WATCHDOG=1")
		select {
		case <-ctx.Done():
			sdNotify("STOPPING=1")
			return nil
		case <-time.After(time.Duration(cfgInt(r.Store, "pass_interval_s")) * time.Second):
		}
	}
}

// DaemonRunning reports whether a daemon holds its lock.
func (r *Runner) DaemonRunning() bool {
	l := r.lock("daemon", false)
	if l == nil {
		return true
	}
	l.Close()
	return false
}
