package hm

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// jobFake is a localCIFake with real background jobs. Its gate_cmd notes its task in dir/started, then waits for the
// file dir/go-<task> and exits with the number in it.
func jobFake(t *testing.T) (*localCIFake, string) {
	t.Helper()
	dir := t.TempDir()
	cmd := fmt.Sprintf(`d=%s; n=$(basename "$PWD"); n=${n%%-*}; echo $n >>$d/started; echo zz-gate-output-line; i=0; `+
		`while [ ! -f $d/go-$n ] && [ $i -lt 1200 ]; do sleep 0.05; i=$((i+1)); done; exit $(cat $d/go-$n)`, dir)
	f := newLocalCIFake(t, "none", cmd)
	f.r.Tools.Detach = detach
	if err := f.s.PutRun(f.run); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, j := range f.r.ciJobs() {
			f.r.dropCIJob(j)
		}
	})
	return f, dir
}

func release(t *testing.T, dir, task string, rc int) {
	t.Helper()
	if err := writeFile(filepath.Join(dir, "go-"+task), fmt.Sprint(rc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(20 * time.Second); !ok(); time.Sleep(20 * time.Millisecond) {
		if time.Now().After(end) {
			t.Fatal("timed out waiting for " + what)
		}
	}
}

func started(dir string) string {
	b, _ := os.ReadFile(filepath.Join(dir, "started"))
	return strings.Join(strings.Fields(string(b)), " ")
}

func theJob(t *testing.T, r *Runner, task string) *ciJob {
	t.Helper()
	for _, j := range r.ciJobs() {
		if j.Task == task {
			return j
		}
	}
	t.Fatal("no job for " + task)
	return nil
}

func ended(r *Runner, j *ciJob) func() bool {
	return func() bool { st, _, _ := r.ciJobState(j); return st == jobDone }
}

// restarted is a new runner on the same home, as a new daemon is.
func restarted(r *Runner) *Runner {
	return &Runner{Env: r.Env, Store: r.Store, Tools: r.Tools, Log: r.Log, Out: r.Out, Poll: r.Poll, SSH: r.SSH}
}

func TestLocalCIRunsInBackground(t *testing.T) {
	f, dir := jobFake(t)
	f.r.gated(f.run) // returns although gate_cmd waits
	if !strings.HasPrefix(f.run.MergeHold, "local CI running since ") || f.run.LocalCI != nil || f.run.JudgeHead != "" {
		t.Fatalf("hold %q, local ci %+v", f.run.MergeHold, f.run.LocalCI)
	}
	j := theJob(t, f.r, "T-0001")
	if pg, err := syscall.Getpgid(j.PID); err != nil || pg != j.PID {
		t.Fatalf("the job must lead its own process group: %d (%v)", pg, err)
	}
	waitFor(t, "gate_cmd", func() bool { return started(dir) == "T-0001" })
	f.run.CI = nil // the next poll
	f.r.gated(f.run)
	if started(dir) != "T-0001" || len(f.r.ciJobs()) != 1 {
		t.Fatalf("one job per run and head: started %q", started(dir))
	}
	release(t, dir, "T-0001", 0)
	waitFor(t, "the result", ended(f.r, j))
	f.run.CI = nil
	f.r.gated(f.run)
	if f.run.LocalCI == nil || f.run.LocalCI.RC != 0 || f.run.JudgeHead != headA || len(f.r.ciJobs()) != 0 {
		t.Fatalf("local ci %+v, judge head %q, hold %q", f.run.LocalCI, f.run.JudgeHead, f.run.MergeHold)
	}
	if !strings.Contains(f.run.LocalCI.Tail, "zz-gate-output-line") {
		t.Errorf("tail %q", f.run.LocalCI.Tail)
	}
	if b, err := os.ReadFile(filepath.Join(f.r.logDir(f.run), "gate-cmd.log")); err != nil || !strings.Contains(string(b), "zz-gate") {
		t.Errorf("log %q %v", b, err)
	}

	// the gate's own step waits for the same kind of job
	_ = os.Remove(filepath.Join(dir, "go-T-0001"))
	go func() {
		time.Sleep(200 * time.Millisecond)
		_ = writeFile(filepath.Join(dir, "go-T-0001"), "3", 0o600)
	}()
	cmd, limit := f.r.projectGate("www")
	ok, note, err := f.r.waitLocalCI(f.run, headC, cmd, limit, f.r.logDir(f.run))
	if err != nil || ok || !strings.Contains(note, "exited 3") {
		t.Fatalf("ok %v note %q err %v", ok, note, err)
	}
}

func TestLocalCISurvivesDaemonRestart(t *testing.T) {
	f, dir := jobFake(t)
	f.r.gated(f.run)
	j := theJob(t, f.r, "T-0001")
	waitFor(t, "gate_cmd", func() bool { return started(dir) == "T-0001" })

	r2 := restarted(f.r)
	r2.sweepLocalCI()
	if st, _, err := r2.ciJobState(j); err != nil || st != jobRunning || started(dir) != "T-0001" {
		t.Fatalf("the job must run on: %s %v, started %q", st, err, started(dir))
	}

	// gone without a result: started again, once
	kill := func() {
		j = theJob(t, r2, "T-0001")
		if err := syscall.Kill(-j.PID, syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		waitFor(t, "the kill", func() bool { st, _, _ := r2.ciJobState(j); return st == jobGone })
	}
	kill()
	r2.sweepLocalCI()
	waitFor(t, "the second start", func() bool { return started(dir) == "T-0001 T-0001" })
	kill()
	r2.sweepLocalCI()
	err := r2.localCI(f.run, headA)
	if err == nil || !strings.Contains(err.Error(), "twice without a result") || f.run.LocalCI != nil {
		t.Fatalf("a second loss must not start a third job or record a result: %v %+v", err, f.run.LocalCI)
	}
	if started(dir) != "T-0001 T-0001" {
		t.Fatalf("started %q", started(dir))
	}
}

func TestLocalCIJobCap(t *testing.T) {
	f, dir := jobFake(t)
	at := func(n int) string { return time.Date(2026, 1, 1, 0, n, 0, 0, time.UTC).Format(time.RFC3339) }
	add := func(task, state, gated, judged string) *Run {
		run := *f.run
		run.Task, run.State, run.Gated = task, state, gated
		if judged != "" {
			run.Verdicts = []Verdict{{TS: judged}}
		}
		if err := f.s.PutRun(&run); err != nil {
			t.Fatal(err)
		}
		return &run
	}
	runs := []*Run{f.run, add("T-0002", Gated, at(1), ""), add("T-0003", Gated, at(2), ""), add("T-0004", Gated, at(3), ""),
		add("T-0005", Merging, at(9), at(10))}
	for i, run := range runs {
		want := "local CI running since "
		if i >= 2 {
			want = "local CI queued since "
		}
		if err := f.r.localCI(run, headA); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("%s: %v, want %q", run.Task, err, want)
		}
	}
	waitFor(t, "two jobs", func() bool { return len(strings.Fields(started(dir))) == 2 })
	running := func() (out []string) {
		for _, j := range f.r.ciJobs() {
			if st, _, _ := f.r.ciJobState(j); st == jobRunning {
				out = append(out, j.Task)
			}
		}
		return out
	}
	// judged runs go first, then the order the gates passed
	for _, step := range []struct{ done, want string }{{"T-0001", "T-0002 T-0005"}, {"T-0002", "T-0003 T-0005"},
		{"T-0005", "T-0003 T-0004"}} {
		j := theJob(t, f.r, step.done)
		release(t, dir, step.done, 0)
		waitFor(t, step.done, ended(f.r, j))
		f.r.sweepLocalCI()
		if got := strings.Join(running(), " "); got != step.want {
			t.Fatalf("after %s: running %q, want %q", step.done, got, step.want)
		}
	}
	if err := f.s.SetCfg("local_ci_jobs", "3"); err != nil {
		t.Fatal(err)
	}
	if err := f.r.localCI(add("T-0006", Gated, at(4), ""), headA); err == nil || !strings.HasPrefix(err.Error(), "local CI running") {
		t.Fatalf("a higher cap: %v", err)
	}
}

func TestLocalCIResultAdoptedOnStart(t *testing.T) {
	f, dir := jobFake(t)
	f.r.gated(f.run)
	j := theJob(t, f.r, "T-0001")
	release(t, dir, "T-0001", 7)
	waitFor(t, "the result", ended(f.r, j))

	r2 := restarted(f.r)
	r2.sweepLocalCI()
	run, err := f.s.Run("T-0001")
	if err != nil || run.LocalCI == nil || run.LocalCI.Head != headA || run.LocalCI.RC != 7 {
		t.Fatalf("run %+v %v", run.LocalCI, err)
	}
	if len(r2.ciJobs()) != 0 || started(dir) != "T-0001" {
		t.Errorf("jobs %v, started %q", r2.ciJobs(), started(dir))
	}

	// a job of an ended run is stopped and forgotten
	_ = os.Remove(filepath.Join(dir, "go-T-0001"))
	run.LocalCI = nil
	if err := r2.localCI(run, headA); err == nil {
		t.Fatal("want a running job")
	}
	j = theJob(t, r2, "T-0001")
	run.State = Failed
	if err := f.s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	r2.sweepLocalCI()
	waitFor(t, "the job to be stopped", func() bool { return !lockHeld(filepath.Join(j.Dir, "lock")) })
	if len(r2.ciJobs()) != 0 {
		t.Errorf("jobs %v", r2.ciJobs())
	}
}

// A run placed on another machine runs its job there (T-2017): started, polled and read over ssh.
func TestLocalCIJobOnRunMachine(t *testing.T) {
	r, _ := testRunner(t, "")
	m := newFakeRemote(t, r)
	repo := filepath.Join(m.home, "clone")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	mustGit(t, repo, "init", "-q")
	mustGit(t, repo, "commit", "-q", "--allow-empty", "-m", "zz")
	head := mustGit(t, repo, "rev-parse", "HEAD")
	run := &Run{Task: "T-0001", State: Gated, Round: 1, Machine: "w1", Mirror: t.TempDir(), RemoteHome: m.home, Worktree: repo}
	logdir := r.logDir(run)
	request := func() (string, error) {
		return r.requestCIJob(run, head, "echo zz-remote $HOME/zz; exit 5", time.Minute, logdir, false)
	}
	var note string
	waitFor(t, "the remote job", func() bool {
		var err error
		note, err = request()
		if err != nil && !strings.HasPrefix(err.Error(), "local CI running") {
			t.Fatal(err)
		}
		return err == nil
	})
	if run.LocalCI.RC != 5 || !strings.Contains(note, "exited 5") || !strings.Contains(run.LocalCI.Tail, "zz-remote") {
		t.Fatalf("local ci %+v note %q", run.LocalCI, note)
	}
	// the other machine's home names its user: neither the PR comment nor the note carries it
	if strings.Contains(run.LocalCI.Tail+note, m.home) || !strings.Contains(run.LocalCI.Tail, "zz-remote ~/zz") {
		t.Errorf("tail %q", run.LocalCI.Tail)
	}
	if left, _ := filepath.Glob(filepath.Join(remoteBase(m.home), "gate", "*")); len(left) != 0 {
		t.Errorf("left on the machine: %v", left)
	}

	// a job dropped while its machine is unreachable keeps its record until it is confirmed ended
	if _, err := r.requestCIJob(run, head, "sleep 60", time.Minute, logdir, false); !errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	j := theJob(t, r, "T-0001")
	up := r.SSH
	r.SSH = func(string, string, time.Duration, string) (int, string, string) { return sshFailed, "", "no route" }
	if r.dropCIJob(j) || !theJob(t, r, "T-0001").Dropped {
		t.Fatal("an unconfirmed job must stay on record")
	}
	r.SSH = up
	r.sweepLocalCI()
	waitFor(t, "the dropped job to end", func() bool { return syscall.Kill(j.PID, 0) != nil || zombie(j.PID) })
	if len(r.ciJobs()) != 0 {
		t.Errorf("jobs %v", r.ciJobs())
	}

	// a start whose answer was lost leaves a process: the next start ends it first
	if _, err := r.requestCIJob(run, head, "sleep 60", time.Minute, logdir, false); !errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	j = theJob(t, r, "T-0001")
	orphan := j.PID
	r.removeCICheckout(j)
	waitFor(t, "the orphan to end", func() bool { return syscall.Kill(orphan, 0) != nil || zombie(orphan) })
	r.forgetCIJob(j)

	// the pid on record may be missing (its starter died): the machine's pid file names the job
	if _, err := r.requestCIJob(run, head, "sleep 60", time.Minute, logdir, false); !errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	j = theJob(t, r, "T-0001")
	pid := j.PID
	j.PID = 0
	if st, _, err := r.ciJobState(j); err != nil || st != jobRunning || r.jobPID(j) != pid {
		t.Fatalf("%s %v, pid %d want %d", st, err, r.jobPID(j), pid)
	}
	// a start whose answer was lost leaves a queued record: it is forgotten only once the machine confirms the end
	j.Started, j.StartLost = time.Time{}, true
	r.SSH = func(string, string, time.Duration, string) (int, string, string) { return sshFailed, "", "no route" }
	if r.dropCIJob(j) {
		t.Fatal("dropped without the machine")
	}
	r.SSH = up
	if !r.dropCIJob(j) || len(r.ciJobs()) != 0 {
		t.Fatalf("jobs %v", r.ciJobs())
	}
	waitFor(t, "the lost start to end", func() bool { return syscall.Kill(pid, 0) != nil || zombie(pid) })
}

func zombie(pid int) bool {
	b, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	return strings.Contains(string(b), ") Z ")
}

// A result no gate has read stays for a working session's gate; one that finished at its time limit keeps its exit code.
func TestLocalCILeftForWorkingSession(t *testing.T) {
	f, dir := jobFake(t)
	f.run.State = Working
	if err := f.s.PutRun(f.run); err != nil {
		t.Fatal(err)
	}
	if err := f.r.localCI(f.run, headA); !errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	j := theJob(t, f.r, "T-0001")
	release(t, dir, "T-0001", 3)
	waitFor(t, "the result", ended(f.r, j))
	f.r.sweepLocalCI()
	f.r.timeOutCIJob(j, time.Second)
	if run, err := f.s.Run("T-0001"); err != nil || run.LocalCI != nil {
		t.Fatalf("adopted behind the session: %+v %v", run.LocalCI, err)
	}
	if st, rc, err := f.r.ciJobState(j); err != nil || st != jobDone || rc != 3 {
		t.Fatalf("%s %d %v", st, rc, err)
	}
}

// Why a job could not start reaches the next request of its run, even when another look tried the start.
func TestLocalCIStartFailureKept(t *testing.T) {
	f, _ := jobFake(t)
	f.r.Tools.Detach = func(string, string, string, []string, ...string) (int, error) { return 0, errors.New("zz-no-start") }
	if err := f.r.saveCIJob(f.r.newCIJob(f.run, headA, "true", time.Minute, f.r.logDir(f.run))); err != nil {
		t.Fatal(err)
	}
	f.r.sweepLocalCI()
	f.r.sweepLocalCI()
	if err := f.r.localCI(f.run, headA); err == nil || !strings.Contains(err.Error(), "zz-no-start") || len(f.r.ciJobs()) != 0 {
		t.Fatalf("%v, jobs %v", err, f.r.ciJobs())
	}
}

// A merging run whose local CI has no result alerts the owner once.
func TestLocalCIMergeWaitAlerts(t *testing.T) {
	f, _ := jobFake(t)
	now := time.Now()
	f.r.localCIWaits(f.run, headA, now)
	if f.run.CI == nil || f.run.CI.Alerted {
		t.Fatalf("%+v", f.run.CI)
	}
	f.r.localCIWaits(f.run, headA, now.Add(24*time.Hour))
	if !f.run.CI.Alerted {
		t.Fatalf("%+v", f.run.CI)
	}
}

// A daemon that died while it started a job (no pid recorded) neither starts a second copy nor loses the first; a wrapper
// that dies alone takes what it ran with it; a changed gate_cmd gets its own job.
func TestLocalCIJobLeftovers(t *testing.T) {
	f, dir := jobFake(t)
	if err := f.r.localCI(f.run, headA); !errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	j := theJob(t, f.r, "T-0001")
	waitFor(t, "gate_cmd", func() bool { return started(dir) == "T-0001" })
	pid := j.PID
	j.PID = 0
	if err := f.r.saveCIJob(j); err != nil {
		t.Fatal(err)
	}
	f.r.sweepLocalCI()
	if got := f.r.jobPID(theJob(t, f.r, "T-0001")); got != pid || started(dir) != "T-0001" {
		t.Fatalf("pid %d, want %d; started %q", got, pid, started(dir))
	}

	group := func() bool { return syscall.Kill(-pid, 0) == nil }
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil { // the wrapper alone
		t.Fatal(err)
	}
	waitFor(t, "the wrapper to end", func() bool { st, _, _ := f.r.ciJobState(j); return st == jobGone })
	if !group() {
		t.Fatal("gate_cmd should still run")
	}
	f.r.sweepLocalCI()
	waitFor(t, "the old group to end", func() bool { return !group() })
	waitFor(t, "the second start", func() bool { return started(dir) == "T-0001 T-0001" })

	old := theJob(t, f.r, "T-0001").PID
	if _, err := f.r.requestCIJob(f.run, headA, "exit 0", time.Minute, f.r.logDir(f.run), false); err != nil &&
		!errors.Is(err, errLocalCIRunning) {
		t.Fatal(err)
	}
	waitFor(t, "the old job to end", func() bool { return syscall.Kill(old, 0) != nil || zombie(old) })
	for _, j := range f.r.ciJobs() {
		if j.Cmd != "exit 0" {
			t.Errorf("job of the old command: %+v", j)
		}
	}
}
