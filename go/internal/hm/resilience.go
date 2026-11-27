package hm

import (
	"context"
	"errors"
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
		if _, err := os.Stat(filepath.Join(run.coordWT(), ".git")); os.IsNotExist(err) {
			if t, err := r.Store.Task(run.Task); err == nil {
				if wt, err := r.Clone(t, run.Branch); err == nil {
					if run.cloned() {
						run.Mirror = wt
					} else {
						run.Worktree = wt
					}
					fixed = append(fixed, "clone recreated from "+run.Branch)
				}
			}
		}
	}
	if run.cloned() && Active(run.State) && run.State != Merging && r.worktreeLost(run) {
		if t, err := r.Store.Task(run.Task); err == nil {
			if _, err := r.remoteWorktree(run.Machine, run.RemoteHome, t, run.Branch); err != nil {
				r.printf("%s: cannot recreate %s on %s: %v\n", run.Task, run.Worktree, run.Machine, err)
			} else {
				fixed = append(fixed, "worktree on "+run.Machine+" recreated from "+run.Branch)
			}
		}
	}
	if (run.State == Working || run.State == Judging) && run.PRURL != "" {
		if pr, err := r.prView(run); err == nil && pr.State != "OPEN" {
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
	// a session on a worker that hc lists as disconnected is not lost: hc keeps it and lists it again on reconnect. The
	// judge runs on the local core, except for a clone-mode run
	down := run.Machine != "" && (name == run.Agent || run.cloned()) && machineDown(r.Tools, run.Machine)
	if name != "" && listed && !down && !alive(list, true, name) {
		if run.Resumes >= cfgInt(r.Store, "resume_max") {
			r.fail(run, fmt.Sprintf("session lost: %s is gone after %d resumes", name, run.Resumes))
			fixed = append(fixed, "session lost, run failed")
		} else if err := r.resumeSession(run, name, ""); err != nil {
			fixed = append(fixed, "resume of "+name+" failed: "+firstLine(err.Error()))
		} else {
			run.Resumes++
			if name == run.Agent { // the time the session was gone is not its fault: a new message and a new grace
				run.LongResumed, run.LongSendFailed, run.HookStarted = "", "", ""
			}
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
		if run.cloned() {
			machine = run.Machine // except for a clone-mode run: its worktree is only there
		}
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

// ObsDown says why the dashboard does not answer on obs_listen:obs_port ("" = it answers). A wildcard obs_listen is
// dialled on loopback.
func (r *Runner) ObsDown() string {
	host := strings.TrimSpace(r.Store.Cfg("obs_listen"))
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	addr := ObsAddr(host, cfgInt(r.Store, "obs_port"))
	dial := r.ObsDial
	if dial == nil {
		dial = func(a string) error {
			c, err := net.DialTimeout("tcp", a, 2*time.Second)
			if err == nil {
				_ = c.Close()
			}
			return err
		}
	}
	if err := dial(addr); err != nil {
		return fmt.Sprintf("hivemill obs does not answer on %s: %v", addr, err)
	}
	return ""
}

// ObsAlert follows ObsDown: while the dashboard is down it keeps the owner alert feed row open and raises the owner
// alert at most once per alert_every_s (also across a recovery: a flapping obs alerts no more often); it closes the
// row when why is "".
func (r *Runner) ObsAlert(now time.Time, why string) (bool, error) {
	s, err := r.sq()
	if err != nil {
		return false, err
	}
	if why == "" {
		return false, s.CloseAlertKind("hivemill-obs-down")
	}
	link, check := "journalctl --user -u hivemill-obs.service", "Check `systemctl --user status hivemill-obs.service`."
	if GOOS == "darwin" {
		log := filepath.Join(r.Env.Home, "logs", "hivemill.obs.log")
		link, check = log, "Check `launchctl print gui/$(id -u)/hivemill.obs` and "+log+"."
	}
	if err := r.Store.AlertFeed("hivemill-obs-down", "hivemill", link); err != nil {
		return false, err
	}
	if last, err := strconv.ParseInt(r.Store.Meta("obs_alerted"), 10, 64); err == nil &&
		now.Sub(time.Unix(last, 0)) < time.Duration(cfgInt(r.Store, "alert_every_s"))*time.Second {
		return false, nil
	}
	if _, err := r.Alert("hivemill obs is down", why+". "+check); err != nil {
		fmt.Fprintf(os.Stderr, "hivemill: alert: %v\n", err)
	}
	_ = r.Log.Emit("health.alert", nil, Event{Data: map[string]any{"why": why}})
	return true, r.Store.SetMeta("obs_alerted", strconv.FormatInt(now.Unix(), 10))
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
// watchdog ping after each pass. Only one daemon runs per factory home: its flock on this machine and its daemon lock
// (daemonlock.go) across machines.
func (r *Runner) Daemon(ctx context.Context) error {
	l := r.lock("daemon", false)
	if l == nil {
		return fmt.Errorf("%w: another hivemill daemon holds the lock", ErrRefused)
	}
	defer l.Close()
	host, release, err := r.holdDaemonLock(ctx)
	if err != nil {
		return err
	}
	defer release()
	bin, err := os.Executable()
	if err != nil {
		bin = "hivemill"
	}
	r.inDaemon = true
	if err := r.WriteGuardExport(time.Now()); err != nil { // at start, even when another process holds the pass lock
		r.printf("guard export: %v\n", err)
	}
	go r.keepGuardExport(ctx) // a pass can take longer than the export's 15-minute age limit
	cleaned := make(chan struct{})
	cleanCtx, stopClean := context.WithCancel(ctx) // a take-over returns with ctx still live
	go func() { defer close(cleaned); r.keepClean(cleanCtx) }()
	defer func() { stopClean(); <-cleaned }() // the daemon lock is held until a running clean has stopped
	sdNotify("READY=1")
	for {
		if err := r.refreshDaemonLock(host, time.Now()); errors.Is(err, ErrRefused) { // another host took over
			sdNotify("STOPPING=1")
			return err
		} else if err != nil { // a full disk or a brief I/O error does not stop the runner
			r.printf("daemon lock: %v\n", err)
		}
		if err := r.Pass(true); err != nil {
			r.printf("pass: %v\n", err)
		} else {
			_ = r.Heartbeat(time.Now())
		}
		// taken over during the pass: no planner, backup or standby check; the top of the loop ends this daemon
		if errors.Is(r.refreshDaemonLock(host, time.Now()), ErrRefused) {
			continue
		}
		r.PlannerPass(time.Now()) // held runs to the owner queue; keep the planner alive and wake it on events
		r.OtelTail(time.Now())    // otel_endpoint set: finished tasks go out as traces (never fails the pass)
		if r.BackupDue(time.Now()) {
			if _, err := r.Backup(time.Now()); err != nil {
				r.printf("backup: %v\n", err)
			}
		}
		r.StandbyPass(bin, time.Now())
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

// judgeStartMax bounds the passes on which the judge of a clone-mode run may fail to start in one round.
const judgeStartMax = 10

// judgeStartFails counts the passes of this round on which the judge did not start. It is read from the run's history,
// which is stored with the run.
func (run *Run) judgeStartFails() (n int) {
	for _, h := range run.History {
		if strings.HasPrefix(h, fmt.Sprintf("round %d: the judge did not start", run.Round)) {
			n++
		}
	}
	return n
}

// startJudge starts the judge or fails the run. A clone-mode run's judge starts over ssh: while its machine is away the
// run waits, and an error is retried on later passes.
func (r *Runner) startJudge(run *Run) {
	if run.cloned() && machineDown(r.Tools, run.Machine) {
		run.MergeHold = "the judge waits for " + run.Machine + " to reconnect"
		return
	}
	state, n := run.State, len(run.Judges)
	err := r.StartJudge(run)
	if err == nil {
		return
	}
	if !run.cloned() || run.judgeStartFails()+1 >= judgeStartMax {
		r.fail(run, "the judge did not start: "+err.Error())
		return
	}
	if len(run.Judges) > n {
		r.Close(run.Judges[n])
		run.Judges = run.Judges[:n]
	}
	run.State, run.MergeHold = state, "the judge did not start (retried next pass): "+firstLine(err.Error())
	run.History = append(run.History, fmt.Sprintf("round %d: %s", run.Round, run.MergeHold))
	r.printf("%s: %s\n", run.Task, run.MergeHold)
}
