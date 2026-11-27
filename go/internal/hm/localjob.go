package hm

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"factory/go/internal/shlex"
)

// Local CI jobs: a ci_gate none gate_cmd runs as a background job, one per (run, head), never inside a daemon pass. The
// job is detached (its own session), so a daemon restart leaves it running; its output and exit code are files, which any
// later process reads. state/hivemill/localci/ holds one record per job; at most local_ci_jobs run per machine.

// errLocalCIRunning: the head has a job without a result yet.
var errLocalCIRunning = errors.New("local CI")

// errLocalCIMachine: the job's machine could not be reached, so nothing is known about it.
var errLocalCIMachine = errors.New("local CI machine unreachable")

type ciJob struct {
	Task, Head string
	// where gate_cmd runs: Machine "" is this machine; Clone and Checkout are paths there
	Machine, RemoteHome string
	Clone, Checkout     string
	// Dir holds gate-cmd.log, result and lock here; RDir holds log and rc on Machine
	Dir, RDir string
	Cmd       string
	LimitS    int
	// the queue: judged runs first, in the order they were judged, then the others in the order their gate passed
	Rank            int
	Order           time.Time
	Queued, Started time.Time
	PID, Restarts   int
	Failed          string `json:",omitempty"`
	// StartFailed: Failed says why the job could not start; the next request reads it and forgets the job
	StartFailed bool `json:",omitempty"`
	// Dropped: no longer wanted, but not yet confirmed ended (its machine was unreachable)
	Dropped bool `json:",omitempty"`
	// TimedOut: killed at its limit; exit 124 is still to be written if the result file is missing
	TimedOut bool `json:",omitempty"`
	// StartLost: the machine stopped answering during a start, so a process may run there that no pid here names
	StartLost bool `json:",omitempty"`
}

const (
	jobQueued  = "queued"
	jobRunning = "running"
	jobDone    = "done"
	jobGone    = "gone"
	jobFailed  = "failed"
)

// ciWrapper runs gate_cmd ($1) and then puts its exit code in $2. The lock on fd 3 is the wrapper's alone: gate_cmd and
// whatever it leaves behind do not hold it.
const ciWrapper = `echo $$ >"${2%/*}/pid"; bash -c "$1" 3>&-; rc=$?; echo "$rc" >"$2.tmp" && mv -f "$2.tmp" "$2"`

// detach starts argv in its own session with its output in logfile. The process holds an flock on lockfile for as long
// as it lives, which is how any later process tells that it still runs (a pid alone may be reused).
func detach(dir, logfile, lockfile string, env []string, argv ...string) (int, error) {
	lock, err := os.OpenFile(lockfile, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	// a job just killed holds the lock until the kernel has ended it
	for end := time.Now().Add(5 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		} else if time.Now().After(end) {
			return 0, fmt.Errorf("lock %s: %w", lockfile, err)
		}
	}
	out, err := os.OpenFile(logfile, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer out.Close()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, out, out
	cmd.ExtraFiles = []*os.File{lock}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	go func() { _ = cmd.Wait() }() // reap it when this process outlives it
	return cmd.Process.Pid, nil
}

// lockHeld reports whether a live process holds the flock of file.
func lockHeld(file string) bool {
	f, err := os.OpenFile(file, os.O_RDWR, 0o600)
	if err != nil {
		return false
	}
	defer f.Close()
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) != nil
}

func (j *ciJob) key() string { return j.Task + "-" + fmt.Sprintf("%.12s", j.Head) }

func (r *Runner) ciJobDir() string { return filepath.Join(r.Env.StateDir(), "localci") }

func (r *Runner) saveCIJob(j *ciJob) error {
	_ = os.MkdirAll(r.ciJobDir(), 0o700)
	b, err := json.MarshalIndent(j, "", " ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(r.ciJobDir(), j.key()+".json"), string(b), 0o600)
}

func (r *Runner) ciJobs() []*ciJob {
	files, _ := filepath.Glob(filepath.Join(r.ciJobDir(), "*.json"))
	var out []*ciJob
	for _, f := range files {
		b, err := os.ReadFile(f)
		j := &ciJob{}
		if err == nil && json.Unmarshal(b, j) == nil && j.Task != "" {
			out = append(out, j)
		}
	}
	return out
}

func (r *Runner) jobPlace(j *ciJob, lost *bool) place {
	return place{r: r, machine: j.Machine, lost: lost}
}

// ciJobState reads what the job's files and process say.
func (r *Runner) ciJobState(j *ciJob) (string, int, error) {
	switch {
	case j.Failed != "":
		return jobFailed, 0, nil
	case j.Started.IsZero():
		return jobQueued, 0, nil
	case j.Machine != "":
		return r.remoteJobState(j)
	}
	result := filepath.Join(j.Dir, "result")
	read := func() (int, bool) {
		b, err := os.ReadFile(result)
		if err != nil {
			return 0, false
		}
		rc, err := strconv.Atoi(strings.TrimSpace(string(b)))
		return rc, err == nil
	}
	if rc, ok := read(); ok {
		return jobDone, rc, nil
	}
	if lockHeld(filepath.Join(j.Dir, "lock")) {
		return jobRunning, 0, nil
	}
	if rc, ok := read(); ok { // it ended between the two looks
		return jobDone, rc, nil
	}
	return jobGone, 0, nil
}

func (r *Runner) remoteJobState(j *ciJob) (string, int, error) {
	rc := shlex.Quote(path.Join(j.RDir, "rc"))
	// the pid counts only while its command line still names this job's result file
	pid := strconv.Itoa(j.PID)
	if j.PID <= 1 { // started by a process that died before it recorded the pid
		pid = fmt.Sprintf(`"$(cat %s 2>/dev/null)"`, shlex.Quote(path.Join(j.RDir, "pid")))
	}
	script := fmt.Sprintf(`if [ -f %[1]s ]; then echo "done $(cat %[1]s)"; elif ps -ww -p %[2]s -o args= 2>/dev/null | grep -qF -- %[1]s; `+
		`then echo running; elif [ -f %[1]s ]; then echo "done $(cat %[1]s)"; else echo gone; fi`, rc, pid)
	out, err := r.rshOK(j.Machine, "", 30*time.Second, script)
	if err != nil {
		return "", 0, fmt.Errorf("%w: %w", errLocalCIMachine, err)
	}
	f := strings.Fields(out)
	switch {
	case len(f) == 2 && f[0] == jobDone:
		if n, err := strconv.Atoi(f[1]); err == nil {
			return jobDone, n, nil
		}
	case len(f) == 1 && (f[0] == jobRunning || f[0] == jobGone):
		return f[0], 0, nil
	}
	return "", 0, fmt.Errorf("%w: %s: unreadable job state %q", errLocalCIMachine, j.Machine, firstLine(out))
}

// startCIJob checks out exactly the head in a fresh detached worktree and starts gate_cmd there (docs/local-gate.md):
// nothing uncommitted or untracked in the task clone can make it pass, and the clone itself is never touched.
func (r *Runner) startCIJob(j *ciJob) error {
	lost, timedOut := false, false
	p := r.jobPlace(j, &lost)
	fail := func(format string, a ...any) error {
		// a command that timed out on another machine says as little about the head as a lost connection
		lost = lost || (j.Machine != "" && timedOut)
		r.removeCICheckout(j)
		if lost {
			return fmt.Errorf("%w: "+format, append([]any{errLocalCIMachine}, a...)...)
		}
		return fmt.Errorf("gate_cmd: "+format, a...)
	}
	r.removeCICheckout(j) // what an earlier attempt left
	if j.Machine == "" {
		parent := filepath.Join(r.Env.StateDir(), "gate")
		_ = os.MkdirAll(parent, 0o700)
		dir, err := os.MkdirTemp(parent, j.Task+"-")
		if err != nil {
			return fail("%v", err)
		}
		j.Checkout = dir
	}
	if rc, _, errs := p.git(j.Clone, "worktree", "add", "-q", "--detach", j.Checkout, j.Head); rc != 0 {
		timedOut = rc == 124
		return fail("cannot check out head %.12s: %s", j.Head, firstLine(errs))
	}
	if rc, at, _ := p.git(j.Checkout, "rev-parse", "HEAD"); at != j.Head {
		timedOut = rc == 124
		return fail("the checkout is at %.12s, not at the head %.12s", at, j.Head)
	}
	if err := os.MkdirAll(j.Dir, 0o700); err != nil {
		return fail("%v", err)
	}
	result := filepath.Join(j.Dir, "result")
	_ = os.Remove(result)
	_ = os.Remove(filepath.Join(j.Dir, "pid"))
	// recorded as started before the launch: a daemon that dies in between must not start a second copy beside it
	j.Started, j.PID, j.StartLost = time.Now(), 0, false
	if err := r.saveCIJob(j); err != nil {
		return fail("%v", err)
	}
	switch {
	case j.Machine != "":
		rcFile := path.Join(j.RDir, "rc")
		script := fmt.Sprintf("mkdir -p %[1]s && rm -f %[2]s && cd %[3]s || exit 1\n", shlex.Quote(j.RDir), shlex.Quote(rcFile),
			shlex.Quote(j.Checkout)) + remoteCleanEnv + "\nnohup $(command -v setsid) bash -c " +
			quoteAll([]string{ciWrapper, "hivemill-localci", j.Cmd, rcFile}) + " >" + shlex.Quote(path.Join(j.RDir, "log")) +
			" 2>&1 </dev/null &\necho $! >" + shlex.Quote(path.Join(j.RDir, "pid")) + "\necho $!"
		rc, out, errs := p.rsh("", 60*time.Second, script)
		pid, err := strconv.Atoi(strings.TrimSpace(out))
		if rc != 0 || err != nil {
			timedOut = rc == 124
			return fail("cannot start on %s: exit %d: %s", j.Machine, rc, firstLine(errs))
		}
		j.PID = pid
	case r.Tools.Detach == nil: // no background jobs (tests that replace the tools): the job runs here and now
		rc, _, _ := r.execProjectGate(j.Checkout, j.Cmd, time.Duration(j.LimitS)*time.Second, j.Dir)
		if err := writeFile(result, strconv.Itoa(rc), 0o600); err != nil {
			return fail("%v", err)
		}
	default:
		pid, err := r.Tools.Detach(j.Checkout, filepath.Join(j.Dir, "gate-cmd.log"), filepath.Join(j.Dir, "lock"), jobEnv(),
			"bash", "-c", ciWrapper, "hivemill-localci", j.Cmd, result)
		if err != nil {
			return fail("%v", err)
		}
		j.PID = pid
	}
	return nil
}

// jobPID is the job's process: the recorded one, else (a daemon that died while it started the job) the one the wrapper
// wrote down.
func (r *Runner) jobPID(j *ciJob) int {
	if j.PID > 1 {
		return j.PID
	}
	file := filepath.Join(j.Dir, "pid")
	if j.Machine != "" {
		file = path.Join(j.RDir, "pid")
	}
	b, err := r.jobPlace(j, nil).read(file)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	return n
}

// killCIOrphans ends what still runs in the group of a job whose wrapper died alone (gate_cmd would run on beside its
// replacement). Only while no process has the wrapper's pid: a reused pid is someone else's.
func (r *Runner) killCIOrphans(j *ciJob) error {
	pid := r.jobPID(j)
	if pid <= 1 {
		return nil
	}
	if j.Machine != "" {
		if _, err := r.rshOK(j.Machine, "", 30*time.Second,
			fmt.Sprintf("ps -p %[1]d >/dev/null 2>&1 || kill -KILL -%[1]d 2>/dev/null || true", pid)); err != nil {
			return fmt.Errorf("%w: %w", errLocalCIMachine, err)
		}
		return nil
	}
	// the wrapper's lock is free before its pid is: it has yet to exit and be reaped, so wait for that while the group
	// lasts. A process that is exiting or a zombie has no command line; one that names no result file is someone else's.
	cmdline, result := fmt.Sprintf("/proc/%d/cmdline", pid), filepath.Join(j.Dir, "result")
	for end := time.Now().Add(5 * time.Second); syscall.Kill(-pid, 0) == nil; time.Sleep(10 * time.Millisecond) {
		err := syscall.Kill(pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
		}
		b, _ := os.ReadFile(cmdline)
		if err != nil || time.Now().After(end) || (len(b) > 0 && !strings.Contains(string(b), result)) {
			break
		}
	}
	return nil
}

// jobEnv is cleanEnv without the caller's session identity: a queued job is started by whichever process looks next,
// another task's gate included.
func jobEnv() []string {
	return slices.DeleteFunc(cleanEnv(), func(kv string) bool {
		return strings.HasPrefix(kv, "FACTORY_AGENT=") || strings.HasPrefix(kv, "FACTORY_ROLE=")
	})
}

// killCIJob ends a job that still runs. An error means it may still run: its machine could not be reached.
func (r *Runner) killCIJob(j *ciJob) error {
	st, _, err := r.ciJobState(j)
	if err == nil && st == jobQueued && j.StartLost {
		if _, err := r.rshOK(j.Machine, "", 30*time.Second, orphanKill(j)+"; true"); err != nil {
			return fmt.Errorf("%w: %w", errLocalCIMachine, err)
		}
		return nil
	}
	if err == nil && st == jobGone {
		return r.killCIOrphans(j)
	}
	pid := r.jobPID(j)
	if err != nil || st != jobRunning || pid <= 1 {
		return err
	}
	if j.Machine != "" {
		if _, err := r.rshOK(j.Machine, "", 30*time.Second,
			fmt.Sprintf("kill -KILL -%[1]d 2>/dev/null || kill -KILL %[1]d 2>/dev/null || true", pid)); err != nil {
			return fmt.Errorf("%w: %w", errLocalCIMachine, err)
		}
		return nil
	}
	_ = syscall.Kill(-pid, syscall.SIGKILL)
	return nil
}

// orphanKill is the script that ends the process the pid file on the machine names, while it is still this job's.
func orphanKill(j *ciJob) string {
	return fmt.Sprintf(`p=$(cat %s 2>/dev/null) && ps -ww -p "$p" -o args= 2>/dev/null | grep -qF -- %s && { kill -KILL -"$p" || kill -KILL "$p"; }`,
		shlex.Quote(path.Join(j.RDir, "pid")), shlex.Quote(path.Join(j.RDir, "rc")))
}

func (r *Runner) removeCICheckout(j *ciJob) {
	if j.Checkout == "" {
		return
	}
	p := r.jobPlace(j, nil)
	if j.Machine != "" {
		// a start whose answer was lost may have left a process that no record knows: end it before its files go
		_, _, _ = p.rsh("", 60*time.Second, orphanKill(j))
	}
	p.git(j.Clone, "worktree", "remove", "--force", j.Checkout)
	if j.Machine == "" {
		_ = os.RemoveAll(j.Checkout)
	} else {
		_, _, _ = p.rsh("", 60*time.Second, "rm -rf "+shlex.Quote(j.Checkout)+" "+shlex.Quote(j.RDir))
	}
	p.git(j.Clone, "worktree", "prune") // and what an interrupted call left behind
}

// dropCIJob ends a job and forgets it. A job that cannot be confirmed ended keeps its record, marked, so it still counts
// against the cap and a later look ends it.
func (r *Runner) dropCIJob(j *ciJob) bool {
	if err := r.killCIJob(j); err != nil {
		j.Dropped = true
		_ = r.saveCIJob(j)
		return false
	}
	r.removeCICheckout(j)
	r.forgetCIJob(j)
	return true
}

func (r *Runner) forgetCIJob(j *ciJob) { _ = os.Remove(filepath.Join(r.ciJobDir(), j.key()+".json")) }

// scheduleCIJobs moves every job on: a job past its time limit ends with exit 124, one that is gone without a result is
// queued again once, and queued jobs start while their machine has fewer than local_ci_jobs running. It returns why a
// job could not be looked at or started; a failed start stays on its record until a request reads it. A machine found
// unreachable is not asked again in this look. The caller holds the lock.
func (r *Runner) scheduleCIJobs() map[string]error {
	errs := map[string]error{}
	running := map[string]int{}
	down := map[string]error{}
	var queued []*ciJob
	unknown := func(j *ciJob, err error) {
		running[j.Machine]++ // unknown counts as running
		errs[j.key()] = err
		if j.Machine != "" {
			down[j.Machine] = err
		}
	}
	for _, j := range r.ciJobs() {
		if err := down[j.Machine]; err != nil {
			if j.Started.IsZero() && !j.Dropped {
				queued = append(queued, j)
			} else {
				unknown(j, err)
			}
			continue
		}
		if j.Dropped {
			if !r.dropCIJob(j) {
				unknown(j, fmt.Errorf("%w: %s: a dropped job is not confirmed ended", errLocalCIMachine, j.Machine))
			}
			continue
		}
		st, _, err := r.ciJobState(j)
		if err != nil {
			unknown(j, err)
			continue
		}
		switch st {
		case jobRunning:
			limit := time.Duration(j.LimitS) * time.Second
			if time.Since(j.Started) < limit {
				running[j.Machine]++
				continue
			}
			if err := r.killCIJob(j); err != nil {
				unknown(j, err)
				continue
			}
			j.TimedOut = true
			_ = r.saveCIJob(j)
			if err := r.timeOutCIJob(j, limit); err != nil {
				unknown(j, err)
			}
		case jobGone:
			if j.TimedOut { // killed at its limit, and 124 did not get written then: never a restart
				if err := r.timeOutCIJob(j, time.Duration(j.LimitS)*time.Second); err != nil {
					unknown(j, err)
				}
				continue
			}
			if err := r.killCIOrphans(j); err != nil {
				unknown(j, err)
				continue
			}
			if j.Restarts > 0 {
				j.Failed = "the local gate job ended twice without a result"
			} else {
				j.Restarts, j.Rank, j.Started, j.PID = 1, -1, time.Time{}, 0
				queued = append(queued, j)
				r.printf("%s: the local gate job is gone without a result; starting it again\n", j.Task)
			}
			_ = r.saveCIJob(j)
		case jobQueued:
			queued = append(queued, j)
		}
	}
	slices.SortStableFunc(queued, func(a, b *ciJob) int {
		if a.Rank != b.Rank {
			return a.Rank - b.Rank
		}
		return a.Order.Compare(b.Order)
	})
	limit := max(1, cfgInt(r.Store, "local_ci_jobs"))
	for _, j := range queued {
		if err := down[j.Machine]; err != nil {
			errs[j.key()] = err
			continue
		}
		if running[j.Machine] >= limit {
			continue
		}
		if err := r.startCIJob(j); err != nil {
			j.Started, j.PID = time.Time{}, 0
			errs[j.key()] = err
			if errors.Is(err, errLocalCIMachine) {
				down[j.Machine], j.StartLost = err, true
			} else {
				j.Failed, j.StartFailed = err.Error(), true
			}
			_ = r.saveCIJob(j)
			continue
		}
		if err := r.saveCIJob(j); err != nil {
			_ = r.killCIJob(j) // an unrecorded job would run beyond the cap
			errs[j.key()] = err
			continue
		}
		if st, _, err := r.ciJobState(j); err != nil || st == jobRunning {
			running[j.Machine]++
		}
	}
	return errs
}

// timeOutCIJob records exit 124 for a job that was killed at its limit, unless it finished before the kill.
func (r *Runner) timeOutCIJob(j *ciJob, limit time.Duration) error {
	note := fmt.Sprintf("\ntimed out after %s\n", limit)
	if j.Machine != "" {
		if _, err := r.rshOK(j.Machine, "", 30*time.Second, fmt.Sprintf("[ -f %[3]s ] || { printf %%s %[1]s >>%[2]s; echo 124 >%[3]s; }",
			shlex.Quote(note), shlex.Quote(path.Join(j.RDir, "log")), shlex.Quote(path.Join(j.RDir, "rc")))); err != nil {
			return fmt.Errorf("%w: %w", errLocalCIMachine, err)
		}
		return nil
	}
	if _, err := os.Stat(filepath.Join(j.Dir, "result")); err == nil {
		return nil
	}
	if f, err := os.OpenFile(filepath.Join(j.Dir, "gate-cmd.log"), os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = f.WriteString(note)
		_ = f.Close()
	}
	return writeFile(filepath.Join(j.Dir, "result"), "124", 0o600)
}

// requestCIJob is one look at the (run, head) job, made and queued when there is none. With a result it records it on
// the run and returns gate_cmd's note. Without one it returns errLocalCIRunning ("local CI running since <t>"). Any
// other error means gate_cmd gave no result. retry replaces a job that had failed for good.
func (r *Runner) requestCIJob(run *Run, head, cmd string, limit time.Duration, logdir string, retry bool) (string, error) {
	l := r.lock("localci", true)
	if l == nil {
		return "", errors.New("gate_cmd: cannot lock the local CI jobs")
	}
	defer l.Close()
	want := &ciJob{Task: run.Task, Head: head}
	var job *ciJob
	for _, j := range r.ciJobs() {
		switch {
		case j.Task != run.Task:
		case j.key() != want.key() || j.Head != head || (retry && j.Failed != ""):
			r.dropCIJob(j) // an older head's job has no use any more
		case j.Cmd != cmd || j.LimitS != int(limit/time.Second):
			// the project's gate_cmd or limit changed: the old one's result says nothing about the new one
			if !r.dropCIJob(j) {
				return "", fmt.Errorf("%w: %s: a dropped job is not confirmed ended", errLocalCIMachine, j.Machine)
			}
		case j.Dropped:
			if !r.dropCIJob(j) {
				return "", fmt.Errorf("%w: %s: a dropped job is not confirmed ended", errLocalCIMachine, j.Machine)
			}
		default:
			job = j
		}
	}
	if job == nil {
		job = r.newCIJob(run, head, cmd, limit, logdir)
		r.jobPlace(job, nil).git(job.Clone, "fetch", "-q", "origin", run.Branch)
		if err := r.saveCIJob(job); err != nil {
			return "", fmt.Errorf("gate_cmd: %w", err)
		}
	}
	errs := r.scheduleCIJobs()
	if err := errs[job.key()]; err != nil {
		if !errors.Is(err, errLocalCIMachine) {
			r.forgetCIJob(job) // its owner has read why
		}
		return "", err
	}
	for _, j := range r.ciJobs() {
		if j.key() != job.key() {
			continue
		}
		st, rc, err := r.ciJobState(j)
		switch {
		case err != nil:
			return "", err
		case st == jobDone:
			return r.adoptCIJob(run, j, rc, logdir)
		case st == jobFailed:
			if j.StartFailed {
				r.forgetCIJob(j)
			}
			return "", errors.New(j.Failed)
		case st == jobQueued:
			return "", fmt.Errorf("%w queued since %s", errLocalCIRunning, j.Queued.Format(time.RFC3339))
		}
		return "", fmt.Errorf("%w running since %s", errLocalCIRunning, j.Started.Format(time.RFC3339))
	}
	return "", errors.New("gate_cmd: the job record is gone")
}

func (r *Runner) newCIJob(run *Run, head, cmd string, limit time.Duration, logdir string) *ciJob {
	now := time.Now()
	j := &ciJob{Task: run.Task, Head: head, Clone: run.Worktree, Cmd: cmd, LimitS: int(limit / time.Second), Queued: now,
		Rank: 1, Order: now}
	if run.cloned() {
		j.Machine, j.RemoteHome = run.Machine, run.RemoteHome
		j.Checkout = path.Join(remoteBase(j.RemoteHome), "gate", j.key())
		j.RDir = j.Checkout + ".job"
	}
	j.Dir = filepath.Join(logdir, "localci-"+fmt.Sprintf("%.12s", head))
	since := ""
	switch run.State {
	case Judged, Merging:
		j.Rank = 0
		if n := len(run.Verdicts); n > 0 {
			since = run.Verdicts[n-1].TS
		}
	case Gated:
		since = run.Gated
	}
	if t, err := time.Parse(time.RFC3339, since); err == nil {
		j.Order = t
	}
	return j
}

// adoptCIJob records a finished job's result on the run and removes the job. The log stays in the round's log dir.
func (r *Runner) adoptCIJob(run *Run, j *ciJob, rc int, logdir string) (string, error) {
	var b []byte
	var err error
	if j.Machine == "" {
		b, err = os.ReadFile(filepath.Join(j.Dir, "gate-cmd.log"))
	} else if b, err = r.jobPlace(j, nil).read(path.Join(j.RDir, "log")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", fmt.Errorf("%w: %w", errLocalCIMachine, err)
	}
	if err != nil {
		r.printf("%s: gate_cmd log: %v\n", j.Task, err)
	}
	// the checkout's name differs between jobs: an unchanged failure must read the same, or it looks new each time
	text := strings.ReplaceAll(string(b), j.Checkout, "<checkout>")
	_ = os.MkdirAll(logdir, 0o700)
	log := filepath.Join(logdir, "gate-cmd.log")
	_ = os.WriteFile(log, b, 0o600)
	if j.RemoteHome != "" { // the other machine's home names its user: it must not reach the PR comment
		text = strings.ReplaceAll(text, j.RemoteHome, "~")
	}
	// the whole log is checked: the tail alone may have lost the line that marks it as private
	private := true
	if args, why := r.guardArgs(); why == "" {
		refused, _ := r.guardText(args, r.redactHomes(text))
		private = refused != 0
	}
	run.LocalCI = &LocalCI{Head: j.Head, RC: rc, Tail: r.commentTail(text), Private: private}
	r.dropCIJob(j)
	return strings.ReplaceAll(gateNote(j.Cmd, rc, time.Duration(j.LimitS)*time.Second, log, text), j.Checkout, "<checkout>"), nil
}

const ciWaiter = "localci-wait-"

// ciMachineGrace is how long a waiting gate bears an unreachable machine before it gives up.
var ciMachineGrace = 3 * time.Minute

func (r *Runner) ciWaited(task string) bool {
	l := r.lock(ciWaiter+task, false)
	if l == nil {
		return true
	}
	l.Close()
	return false
}

// waitLocalCI is the gate's gate_cmd step: the same background job, waited for here (the session's Stop hook must give
// its answer; the daemon passes on meanwhile). A job that outlives this process is picked up by the next one.
func (r *Runner) waitLocalCI(run *Run, head, cmd string, limit time.Duration, logdir string) (bool, string, error) {
	// tells the sweep that this job has a waiter, whatever its run's state (`hivemill gate T-x`, an eval)
	if w := r.lock(ciWaiter+run.Task, true); w != nil {
		defer w.Close()
	}
	var lost time.Time // since when the machine has not answered
	for retry := true; ; retry = false {
		note, err := r.requestCIJob(run, head, cmd, limit, logdir, retry)
		switch {
		case err == nil:
			return run.LocalCI.RC == 0, note, nil
		case errors.Is(err, errLocalCIRunning):
			lost = time.Time{}
		case !errors.Is(err, errLocalCIMachine):
			return false, "", err
		case lost.IsZero():
			lost = time.Now()
		case time.Since(lost) >= ciMachineGrace:
			return false, "", err
		}
		time.Sleep(r.Poll)
	}
}

// sweepLocalCI is each pass's look at the jobs, so a new daemon adopts what finished while none ran: jobs of ended runs
// are dropped, queued and lost jobs are started, and results are recorded on their runs.
func (r *Runner) sweepLocalCI() {
	if len(r.ciJobs()) == 0 {
		return
	}
	l := r.lock("localci", true)
	if l == nil {
		return
	}
	for _, j := range r.ciJobs() {
		if run, err := r.Store.Run(j.Task); err == nil && (run == nil || !Active(run.State)) && !r.ciWaited(j.Task) {
			r.dropCIJob(j)
		}
	}
	for _, err := range r.scheduleCIJobs() {
		r.printf("local CI: %v\n", err)
	}
	jobs := r.ciJobs()
	l.Close() // a task lock is never taken while holding this one: its holder may be waiting for it
	for _, j := range jobs {
		r.adoptFinished(j)
	}
}

func (r *Runner) adoptFinished(j *ciJob) {
	task := r.lock(j.Task, false)
	if task == nil {
		return // its gate is waiting for this job
	}
	defer task.Close()
	if j.Dropped || r.ciWaited(j.Task) {
		return
	}
	l := r.lock("localci", true)
	if l == nil {
		return
	}
	defer l.Close()
	run, err := r.Store.Run(j.Task)
	// a working session's gate has yet to read it (its hook may have ended first): adopting it here would make that
	// gate start gate_cmd over, for good when gate_cmd outlasts the hook
	if err != nil || run == nil || run.State == Working {
		return
	}
	// the record as it is now: another process may have started or dropped the job since the caller read it
	jobs := r.ciJobs()
	i := slices.IndexFunc(jobs, func(x *ciJob) bool { return x.key() == j.key() })
	if i < 0 || jobs[i].Dropped {
		return
	}
	j = jobs[i]
	cmd, limit, err := r.readProjectGate(run.Project)
	if err != nil {
		return // unread: nothing to compare the job with
	}
	if j.Cmd != cmd || j.LimitS != int(limit/time.Second) {
		r.dropCIJob(j) // the project's gate_cmd or limit changed: this result says nothing about the new one
		return
	}
	st, rc, err := r.ciJobState(j)
	if err != nil || st != jobDone {
		return
	}
	if _, err := r.adoptCIJob(run, j, rc, filepath.Dir(j.Dir)); err != nil {
		r.printf("%s: %v\n", j.Task, err)
		return
	}
	r.noteLocalCI(run)
	if err := r.Store.PutRun(run); err != nil {
		r.printf("%s: %v\n", j.Task, err)
	}
}

func (r *Runner) noteLocalCI(run *Run) {
	_ = r.Log.Emit("ci_gate.gate_cmd", run, Event{Data: map[string]any{"head": run.LocalCI.Head, "exit": run.LocalCI.RC}})
}
