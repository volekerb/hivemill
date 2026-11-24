package tick

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/factorydb/dbtest"
)

// Tests for ReapAgentTests on a fake proc tree ($FACTORY_SERVE_PROC_DIR): every process is a directory of files, the
// signals are recorded by the reapKill seam and never sent, and the clock is fake.

const reapSchema = `
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT NOT NULL, recipient TEXT NOT NULL,
  kind TEXT NOT NULL DEFAULT 'msg', subject TEXT NOT NULL, body TEXT, goal_id TEXT, task_id TEXT, created TEXT NOT NULL,
  read_at TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, org TEXT);
CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT, status TEXT, pr_url TEXT, worktree TEXT);
CREATE TABLE teams (id TEXT PRIMARY KEY, lead_agent TEXT);
CREATE TABLE throttle (key TEXT PRIMARY KEY, ts TEXT NOT NULL);
`

type kill struct {
	pid int
	sig syscall.Signal
}

type fakeProc struct {
	pid, ppid, pgid, start int
	argv                   []string
	env                    map[string]string
	cwd                    string // symlink target of /proc/<pid>/cwd; "" = none
	cgroup                 string
}

type reapFix struct {
	t     *testing.T
	db    *sql.DB
	home  string
	proc  string
	wt    string // worktree of G-1-T-5
	kills []kill
	clock time.Time
}

// newReapFix is a non-live FACTORY_HOME with an ended coder agent "coder-x" of task T-5, a fake proc tree holding only
// the caller (pid 7000, parent 6000, group 6000) and a fake clock.
func newReapFix(t *testing.T) *reapFix {
	t.Helper()
	home := t.TempDir()
	db := dbtest.Raw(t, dbtest.New(t, home, 0))
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(reapSchema); err != nil {
		t.Fatal(err)
	}
	f := &reapFix{t: t, db: db, home: home, proc: filepath.Join(t.TempDir(), "proc"),
		clock: time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC)}
	f.wt = filepath.Join(home, "worktrees", "G-1-T-5")
	for _, d := range []string{f.proc, f.wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(f.wt)
	if err != nil {
		t.Fatal(err)
	}
	f.wt = real
	t.Setenv("FACTORY_HOME", home)
	t.Setenv("FACTORY_SERVE_PROC_DIR", f.proc)
	t.Setenv("FACTORY_SERVE_MAINPID", "")
	t.Setenv("FACTORY_SERVE_KILL_GRACE_S", "")
	t.Setenv("FACTORY_TEST_NOW", "")
	t.Setenv("FACTORY_TMP_DIR", filepath.Join(t.TempDir(), "tmp"))
	f.addProc(fakeProc{pid: 7000, ppid: 6000, pgid: 6000, start: 10, argv: []string{"factory", "tick"}})
	oldKill, oldSleep, oldNow, oldPid, oldPpid, oldPgrp, oldUID, oldPre :=
		reapKill, reapSleep, reapNow, reapGetpid, reapGetppid, reapGetpgrp, reapGetuid, reapPreSignal
	t.Cleanup(func() {
		reapKill, reapSleep, reapNow, reapGetpid, reapGetppid, reapGetpgrp, reapGetuid, reapPreSignal =
			oldKill, oldSleep, oldNow, oldPid, oldPpid, oldPgrp, oldUID, oldPre
	})
	reapKill = func(pid int, sig syscall.Signal) error {
		f.kills = append(f.kills, kill{pid, sig})
		return nil
	}
	reapSleep = func(d time.Duration) { f.clock = f.clock.Add(d) }
	reapNow = func() time.Time { return f.clock }
	reapGetpid = func() int { return 7000 }
	reapGetppid = func() int { return 6000 }
	reapGetpgrp = func() int { return 6000 }
	f.agent("coder-x", "coder", "exited", "T-5")
	f.exec("INSERT INTO tasks(id,goal_id,status) VALUES('T-5','G-1','in_progress')")
	return f
}

func (f *reapFix) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *reapFix) agent(name, role, status, task string) {
	f.t.Helper()
	var tk any
	if task != "" {
		tk = task
	}
	f.exec("INSERT OR REPLACE INTO agents(name,role,status,task_id,started,ended) VALUES(?,?,?,?,?,?)",
		name, role, status, tk, "2026-11-17T08:00:00+00:00", "2026-11-17T08:30:00+00:00")
}

func (f *reapFix) mode(m string) {
	f.t.Helper()
	f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('orphan_reap_mode',?)", m)
}

// statText is /proc/<pid>/stat: the fields after ")" are state, ppid, pgid, 16 unused, start time (index 19).
func statText(pid, ppid, pgid, start int) string {
	f := []string{"S", strconv.Itoa(ppid), strconv.Itoa(pgid)}
	for i := 3; i < 19; i++ {
		f = append(f, "0")
	}
	f = append(f, strconv.Itoa(start), "0", "0")
	return fmt.Sprintf("%d (a b) %s\n", pid, strings.Join(f, " "))
}

func (f *reapFix) write(pid int, leaf, content string) {
	f.t.Helper()
	d := filepath.Join(f.proc, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, leaf), []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reapFix) addProc(p fakeProc) {
	f.t.Helper()
	f.write(p.pid, "stat", statText(p.pid, p.ppid, p.pgid, p.start))
	f.write(p.pid, "cmdline", strings.Join(p.argv, "\x00")+"\x00")
	var env []string
	for k, v := range p.env {
		env = append(env, k+"="+v)
	}
	f.write(p.pid, "environ", strings.Join(env, "\x00")+"\x00")
	f.write(p.pid, "cgroup", p.cgroup)
	if p.cwd != "" {
		link := filepath.Join(f.proc, strconv.Itoa(p.pid), "cwd")
		os.Remove(link)
		if err := os.Symlink(p.cwd, link); err != nil {
			f.t.Fatal(err)
		}
	}
}

// restart gives pid a new start time, as a reused pid has.
func (f *reapFix) restart(pid, ppid, pgid, start int) {
	f.write(pid, "stat", statText(pid, ppid, pgid, start))
}

// btime writes the boot time line of the fake /proc/stat.
func (f *reapFix) btime(sec int64) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.proc, "stat"), []byte(fmt.Sprintf("cpu 1 2 3\nbtime %d\n", sec)), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *reapFix) events(action string) []string {
	f.t.Helper()
	rows, err := f.db.Query("SELECT entity_id || '|' || COALESCE(detail,'') FROM events WHERE action=? ORDER BY id", action)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			f.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func (f *reapFix) allEvents() int {
	var n int
	if err := f.db.QueryRow("SELECT count(*) FROM events").Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

var testRunnerArgv = []string{"python3", "-m", "unittest", "discover"}

// runner is a test run of agent coder-x on its worktree: pid, parent 50 (a shell), own group.
func (f *reapFix) runner(pid, start int) fakeProc {
	return fakeProc{pid: pid, ppid: 50, pgid: pid, start: start, argv: testRunnerArgv,
		env: map[string]string{"FACTORY_AGENT": "coder-x"}, cwd: f.wt}
}

func (f *reapFix) shell() {
	f.addProc(fakeProc{pid: 50, ppid: 1, pgid: 50, start: 5, argv: []string{"bash"}})
}

func cmdSha(argv []string) string {
	sum := sha256.Sum256([]byte(strings.Join(argv, "\x00")))
	return hex.EncodeToString(sum[:])[:12]
}

// wantDetail is the event detail Python writes for the runner pid at start with signal how (key order and spacing as
// json.dumps).
func (f *reapFix) wantDetail(pid, start int, how string) string {
	return fmt.Sprintf(`{"pid": %d, "start_time": %d, "argv0": "python3", "cmd_sha": %q, "cwd": %q, "worktree": %q, `+
		`"task": "T-5", "agent": "coder-x", "attributed_by": "cwd", "signal": %q, "trigger": "done"}`,
		pid, start, cmdSha(testRunnerArgv), f.wt, f.home+"/worktrees/G-1-T-5", how)
}

// TestReapAgentTestsDryRun: mode unset and mode 'bogus' behave as 'dry-run': one would-reap event per (pid, start time)
// with Python's payload, no signal.
func TestReapAgentTestsDryRun(t *testing.T) {
	for _, mode := range []string{"", "bogus", "dry-run"} {
		t.Run("mode="+mode, func(t *testing.T) {
			f := newReapFix(t)
			if mode != "" {
				f.mode(mode)
			}
			f.shell()
			f.addProc(f.runner(100, 4242))
			ReapAgentTests(f.db, "coder-x", "done")
			ReapAgentTests(f.db, "coder-x", "done") // the same (pid, start time) is logged once
			got := f.events("agent-tests-would-reap")
			if want := "coder-x|" + f.wantDetail(100, 4242, "none"); len(got) != 1 || got[0] != want {
				t.Fatalf("events = %q, want one %q", got, want)
			}
			f.restart(100, 50, 100, 4243) // a new process under the same pid is a new episode
			ReapAgentTests(f.db, "coder-x", "done")
			if got := f.events("agent-tests-would-reap"); len(got) != 2 {
				t.Fatalf("after the pid was reused: %d events, want 2", len(got))
			}
			if len(f.kills) != 0 {
				t.Fatalf("dry-run signalled %v", f.kills)
			}
		})
	}
}

// TestReapAgentTestsOff: mode 'off' scans nothing: no event, even a proc root that cannot be read stays quiet.
func TestReapAgentTestsOff(t *testing.T) {
	f := newReapFix(t)
	f.mode("off")
	f.shell()
	f.addProc(f.runner(100, 4242))
	t.Setenv("FACTORY_SERVE_PROC_DIR", filepath.Join(f.home, "no-such-proc"))
	ReapAgentTests(f.db, "coder-x", "done")
	if n := f.allEvents(); n != 0 || len(f.kills) != 0 {
		t.Fatalf("off: %d events, kills %v; want none", n, f.kills)
	}
}

// TestReapAgentTestsOn: SIGTERM then, after the grace, SIGKILL, only to the ended agent's own runner; never to the
// caller, an ancestor, another agent's runner, a runner outside the task, or a pane tree.
func TestReapAgentTestsOn(t *testing.T) {
	f := newReapFix(t)
	f.mode("on")
	f.shell()
	f.addProc(f.runner(100, 4242))
	// not ours: another agent's runner, a runner in another worktree, the caller's own test run, an ancestor's run
	other := f.runner(101, 4300)
	other.env = map[string]string{"FACTORY_AGENT": "coder-y"}
	f.addProc(other)
	elsewhere := f.runner(102, 4301)
	elsewhere.cwd = f.home
	f.addProc(elsewhere)
	self := f.runner(7001, 4302)
	self.ppid = 7000
	f.addProc(self)
	anc := f.runner(6000, 4303)
	anc.ppid = 1
	f.addProc(anc)
	// a runner under a live pane (claude of another agent)
	f.addProc(fakeProc{pid: 60, ppid: 1, pgid: 60, start: 6, argv: []string{"claude"}, env: map[string]string{"FACTORY_AGENT": "coder-y"}})
	pane := f.runner(103, 4304)
	pane.ppid = 60
	f.addProc(pane)
	// a herdr pane host above the runner
	f.addProc(fakeProc{pid: 61, ppid: 1, pgid: 61, start: 7, argv: []string{"herdr"}})
	hp := f.runner(104, 4305)
	hp.ppid = 61
	f.addProc(hp)

	ReapAgentTests(f.db, "coder-x", "done")
	want := []kill{{-100, syscall.SIGTERM}, {-100, syscall.SIGKILL}} // the runner leads its own group
	if fmt.Sprint(f.kills) != fmt.Sprint(want) {
		t.Fatalf("kills = %v, want %v", f.kills, want)
	}
	got := f.events("agent-tests-reaped")
	if w := "coder-x|" + f.wantDetail(100, 4242, "KILL"); len(got) != 1 || got[0] != w {
		t.Fatalf("events = %q, want one %q", got, w)
	}
	if n := len(f.events("agent-tests-would-reap")) + len(f.events("agent-tests-reap-failed")); n != 0 {
		t.Fatalf("unexpected would-reap/failed events: %d", n)
	}
}

// TestReapAgentTestsTermOnly: when the runner is gone after SIGTERM, no SIGKILL is sent and the event says TERM; a
// pid that came back with another start time during the grace is not killed either.
func TestReapAgentTestsTermOnly(t *testing.T) {
	for _, reused := range []bool{false, true} {
		t.Run(fmt.Sprintf("reused=%v", reused), func(t *testing.T) {
			f := newReapFix(t)
			f.mode("on")
			f.shell()
			f.addProc(f.runner(100, 4242))
			reapKill = func(pid int, sig syscall.Signal) error {
				f.kills = append(f.kills, kill{pid, sig})
				if reused {
					f.restart(100, 50, 100, 9999)
				} else {
					os.RemoveAll(filepath.Join(f.proc, "100"))
				}
				return nil
			}
			ReapAgentTests(f.db, "coder-x", "done")
			if want := []kill{{-100, syscall.SIGTERM}}; fmt.Sprint(f.kills) != fmt.Sprint(want) {
				t.Fatalf("kills = %v, want %v", f.kills, want)
			}
			if got := f.events("agent-tests-reaped"); len(got) != 1 || !strings.Contains(got[0], `"signal": "TERM"`) {
				t.Fatalf("events = %q, want one with signal TERM", got)
			}
		})
	}
}

// TestReapAgentTestsIdentityReread: the identity is re-read right before each signal. A process whose start time changed
// between the scan and the signal is not signalled and gets no event. (Removing the re-read in signal() fails this.)
func TestReapAgentTestsIdentityReread(t *testing.T) {
	f := newReapFix(t)
	f.mode("on")
	f.shell()
	f.addProc(f.runner(100, 4242))
	reapPreSignal = func() { f.restart(100, 50, 100, 5555) } // the pid is reused after the scan
	ReapAgentTests(f.db, "coder-x", "done")
	if len(f.kills) != 0 {
		t.Fatalf("signalled a process whose identity changed: %v", f.kills)
	}
	if n := len(f.events("agent-tests-reaped")); n != 0 {
		t.Fatalf("%d reaped events for an unsignalled process", n)
	}
}

// TestReapAgentTestsGroup: the group is signalled only when its leader is also the agent's own runner; otherwise the pid
// alone.
func TestReapAgentTestsGroup(t *testing.T) {
	t.Run("leader is not a runner", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.shell()
		p := f.runner(100, 4242)
		p.pgid = 50 // led by the shell
		f.addProc(p)
		ReapAgentTests(f.db, "coder-x", "done")
		if len(f.kills) == 0 || f.kills[0] != (kill{100, syscall.SIGTERM}) {
			t.Fatalf("kills = %v, want the pid alone", f.kills)
		}
	})
	t.Run("leader is a runner", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.shell()
		lead := f.runner(90, 4000)
		f.addProc(lead)
		p := f.runner(100, 4242)
		p.pgid = 90
		f.addProc(p)
		ReapAgentTests(f.db, "coder-x", "done")
		// pid 90 is scanned first (names sort as strings: "100" < "90"): 100 goes first and signals group 90
		if len(f.kills) == 0 || f.kills[0] != (kill{-90, syscall.SIGTERM}) {
			t.Fatalf("kills = %v, want the group -90 first", f.kills)
		}
	})
	t.Run("the callers own group is never signalled as a group", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.shell()
		p := f.runner(100, 4242)
		p.pgid = 6000 // the tick's own group
		f.addProc(p)
		ReapAgentTests(f.db, "coder-x", "done")
		if len(f.kills) == 0 || f.kills[0] != (kill{100, syscall.SIGTERM}) {
			t.Fatalf("kills = %v, want the pid alone", f.kills)
		}
	})
}

// TestReapAgentTestsEndedClaude: a claude ancestor protects the runner, except the ended agent's own claude that
// started inside its started..ended window.
func TestReapAgentTestsEndedClaude(t *testing.T) {
	f := newReapFix(t)
	f.mode("on")
	// btime 1000, ticks/100 s: began = 1000 + start/100. started 08:00Z .. ended 08:30Z are epoch seconds below.
	started := time.Date(2026, 11, 17, 8, 0, 0, 0, time.UTC).Unix()
	f.btime(started - 1000)
	// inside the window: began = btime + start/100 = started - 1000 + start/100; start 100000 -> started
	f.addProc(fakeProc{pid: 60, ppid: 1, pgid: 60, start: 100000, argv: []string{"claude"},
		env: map[string]string{"FACTORY_AGENT": "coder-x"}})
	p := f.runner(100, 4242)
	p.ppid = 60
	f.addProc(p)
	ReapAgentTests(f.db, "coder-x", "done")
	if len(f.kills) == 0 {
		t.Fatalf("own ended claude must not protect its runner; events %q", f.events("agent-tests-reap-failed"))
	}
	// outside the window: the claude started 5 h later is another agent run under the same name
	f2 := newReapFix(t)
	f2.mode("on")
	f2.btime(started - 1000)
	f2.addProc(fakeProc{pid: 60, ppid: 1, pgid: 60, start: 100000 + 100*3600*5, argv: []string{"claude"},
		env: map[string]string{"FACTORY_AGENT": "coder-x"}})
	p2 := f2.runner(100, 4242)
	p2.ppid = 60
	f2.addProc(p2)
	ReapAgentTests(f2.db, "coder-x", "done")
	if len(f2.kills) != 0 {
		t.Fatalf("a claude started outside the agent's window protects the runner, got kills %v", f2.kills)
	}
}

// TestReapAgentTestsSkips: HQ roles, a mismatched trigger, a row without a task, an unknown agent and another user's
// process are never touched.
func TestReapAgentTestsSkips(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(f *reapFix)
		agent   string
		trigger string
	}{
		{"hq role", func(f *reapFix) { f.agent("coder-x", "lead", "exited", "T-5") }, "coder-x", "done"},
		{"trigger stop but status exited", func(f *reapFix) {}, "coder-x", "stop"},
		{"trigger done but status stopped", func(f *reapFix) { f.agent("coder-x", "coder", "stopped", "T-5") }, "coder-x", "done"},
		{"unknown trigger", func(f *reapFix) {}, "coder-x", "other"},
		{"no task", func(f *reapFix) { f.agent("coder-x", "coder", "exited", "") }, "coder-x", "done"},
		{"unknown agent", func(f *reapFix) {}, "nobody", "done"},
		{"another user", func(f *reapFix) { reapGetuid = func() int { return os.Getuid() + 1 } }, "coder-x", "done"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newReapFix(t)
			f.mode("on")
			f.shell()
			f.addProc(f.runner(100, 4242))
			c.prepare(f)
			ReapAgentTests(f.db, c.agent, c.trigger)
			if f.allEvents() != 0 || len(f.kills) != 0 {
				t.Fatalf("%d events, kills %v; want none", f.allEvents(), f.kills)
			}
		})
	}
	t.Run("stop trigger on a stopped agent reaps", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.agent("coder-x", "coder", "stopped", "T-5")
		f.shell()
		f.addProc(f.runner(100, 4242))
		ReapAgentTests(f.db, "coder-x", "stop")
		if got := f.events("agent-tests-reaped"); len(got) != 1 || !strings.Contains(got[0], `"trigger": "stop"`) {
			t.Fatalf("events = %q", got)
		}
	})
}

// TestReapAgentTestsFailed: an error or a panic inside ends in one agent-tests-reap-failed event and nothing is
// returned (the function has no return value, and must not panic).
func TestReapAgentTestsFailed(t *testing.T) {
	t.Run("bad grace", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		t.Setenv("FACTORY_SERVE_KILL_GRACE_S", "soon")
		ReapAgentTests(f.db, "coder-x", "done")
		if got := f.events("agent-tests-reap-failed"); len(got) != 1 || !strings.Contains(got[0], "could not convert string to float: 'soon'") {
			t.Fatalf("events = %q", got)
		}
	})
	t.Run("proc root unreadable", func(t *testing.T) {
		f := newReapFix(t)
		t.Setenv("FACTORY_SERVE_PROC_DIR", filepath.Join(f.home, "missing"))
		ReapAgentTests(f.db, "coder-x", "done")
		if got := f.events("agent-tests-reap-failed"); len(got) != 1 {
			t.Fatalf("events = %q", got)
		}
	})
	t.Run("panic", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.shell()
		f.addProc(f.runner(100, 4242))
		reapKill = func(int, syscall.Signal) error { panic("boom") }
		ReapAgentTests(f.db, "coder-x", "done")
		if got := f.events("agent-tests-reap-failed"); len(got) != 1 || !strings.Contains(got[0], "boom") {
			t.Fatalf("events = %q", got)
		}
	})
	t.Run("kill error is not an error", func(t *testing.T) {
		f := newReapFix(t)
		f.mode("on")
		f.shell()
		f.addProc(f.runner(100, 4242))
		reapKill = func(int, syscall.Signal) error { return syscall.ESRCH }
		ReapAgentTests(f.db, "coder-x", "done")
		if f.allEvents() != 0 {
			t.Fatalf("a refused signal logged %d events, want none", f.allEvents())
		}
	})
}

// TestReapAgentTestsLiveHomeIgnoresProcDir: on a live home FACTORY_SERVE_PROC_DIR is ignored (the real /proc is read)
// and one proc-dir-ignored event is recorded.
func TestReapAgentTestsLiveHomeIgnoresProcDir(t *testing.T) {
	f := newReapFix(t)
	pw := t.TempDir()
	live := filepath.Join(pw, "factory")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	f.mode("on")
	f.shell()
	f.addProc(f.runner(100, 4242))
	t.Setenv("FACTORY_AGENT", "coder-x")
	procIgnoredMu.Lock()
	procIgnored = map[string]bool{}
	procIgnoredMu.Unlock()
	env := pruneTestEnv(pw, live, "", func() time.Time { return f.clock })
	vars := map[string]string{"FACTORY_HOME": live, "FACTORY_SERVE_PROC_DIR": f.proc, "FACTORY_AGENT": "coder-x"}
	env.Getenv = func(k string) string { return vars[k] }
	env.LookupEnv = func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	withPruneIdentity(t, env)
	ReapAgentTests(f.db, "coder-x", "done")
	if len(f.kills) != 0 {
		t.Fatalf("a forged proc dir steered a signal on the live home: %v", f.kills)
	}
	if got := f.events("proc-dir-ignored"); len(got) != 1 || !strings.Contains(got[0], "FACTORY_SERVE_PROC_DIR") {
		t.Fatalf("proc-dir-ignored events = %q", got)
	}
}

func TestIsTestRunner(t *testing.T) {
	cases := []struct {
		argv []string
		want bool
	}{
		{nil, false},
		{[]string{"pytest"}, true},
		{[]string{"/usr/bin/py.test", "x"}, true},
		{[]string{"python3", "ci/affected.py", "--run"}, true},
		{[]string{"python3.12", "/w/ci/affected.py"}, true},
		{[]string{"python3", "-m", "unittest"}, true},
		{[]string{"python3", "-m", "pytest", "-q"}, true},
		{[]string{"python3", "-m", "http.server"}, false},
		{[]string{"python3", "bin/factory", "test"}, true},
		{[]string{"python3", "bin/factory_cli.py", "test"}, true},
		{[]string{"python3", "bin/factory", "done"}, false},
		{[]string{"python3"}, false},
		{[]string{"python", "-m", "unittest"}, false},
		{[]string{"python3.٣", "-m", "unittest"}, true},  // Python's \d matches U+0663
		{[]string{"python3. ", "-m", "unittest"}, false}, // NBSP is not a digit
		{[]string{"python3.\v1", "-m", "unittest"}, false},
		{[]string{"go", "test"}, false},
	}
	for _, c := range cases {
		if got := isTestRunner(c.argv); got != c.want {
			t.Errorf("isTestRunner(%q) = %v, want %v", c.argv, got, c.want)
		}
	}
}

// TestUnicodeTaskAttribution: Python's \d in the worktree and scratchpad patterns matches non-ASCII digits, and the
// task id keeps them; NBSP and \v are not digits.
func TestUnicodeTaskAttribution(t *testing.T) {
	f := newReapFix(t)
	p := newProc(f.db)
	for _, c := range []struct {
		dir  string
		ok   bool
		task string
	}{
		{"G-1-T-5", true, "T-5"},
		{"G-٣-T-٤", true, "T-٤"},
		{"G-1-T- 5", false, ""},
		{"G-1-T-\v5", false, ""},
		{"G-1-T-5x", false, ""},
	} {
		d := filepath.Join(f.home, "worktrees", c.dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		_, task, _, ok := p.orphanTask(realpath(d), nil)
		if ok != c.ok || task != c.task {
			t.Errorf("orphanTask(%q) = %q, %v; want %q, %v", c.dir, task, ok, c.task, c.ok)
		}
	}
	for _, c := range []struct {
		name string
		want string
		ok   bool
	}{
		{"qa-t-0283-x", "T-0283", true},
		{"qa-t-٣-x", "T-٣", true},
		{"xt-5", "", false},
		{"t- 5", "", false},
		{"t-\v5", "", false},
	} {
		got, ok := searchTaskID(c.name)
		if got != c.want || ok != c.ok {
			t.Errorf("searchTaskID(%q) = %q, %v; want %q, %v", c.name, got, ok, c.want, c.ok)
		}
	}
}

// TestOrphanTaskAttribution: cwd, the Claude scratchpad and an argv path attribute to one task; two tasks attribute to
// none; a path through a dangling symlink, a loop and a missing component is simply not in a worktree.
func TestOrphanTaskAttribution(t *testing.T) {
	f := newReapFix(t)
	p := newProc(f.db)
	other := filepath.Join(f.home, "worktrees", "G-1-T-6")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, task, by, ok := p.orphanTask(f.wt, []string{"python3", "x.py"}); !ok || task != "T-5" || by != "cwd" {
		t.Fatalf("cwd: %v %v %v", task, by, ok)
	}
	if _, _, _, ok := p.orphanTask(f.wt, []string{"python3", realpath(other)}); ok {
		t.Fatal("cwd in one task and argv in another must attribute to none")
	}
	if _, task, by, ok := p.orphanTask(f.home, []string{"python3", f.wt + "/a.py"}); !ok || by != "argv" || task != "T-5" {
		t.Fatalf("argv: %v %v %v", task, by, ok)
	}
	tmp := realpath(os.Getenv("FACTORY_TMP_DIR"))
	slug := nonAlnumDash(filepath.Join(f.home, "worktrees")) + "-G-1-T-5"
	pad := filepath.Join(tmp, fmt.Sprintf("claude-%d", os.Getuid()), slug)
	if _, task, by, ok := p.orphanTask(pad, nil); !ok || by != "scratchpad" || task != "T-5" {
		t.Fatalf("scratchpad: %v %v %v", task, by, ok)
	}
	// symlinks: dangling, chain through a missing component, loop
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Symlink(filepath.Join(dir, "gone"), filepath.Join(dir, "dangling")))
	must(os.Symlink(filepath.Join(dir, "missing", "x"), filepath.Join(dir, "chain")))
	must(os.Symlink("l", filepath.Join(dir, "l")))
	for _, name := range []string{"dangling", "chain", "l"} {
		if _, _, _, ok := p.orphanTask(dir, []string{"python3", name}); ok {
			t.Errorf("argv %s attributed to a task", name)
		}
	}
	// a dangling symlink into a worktree still counts for what it names
	must(os.Symlink(filepath.Join(f.wt, "nope"), filepath.Join(dir, "into")))
	if _, task, _, ok := p.orphanTask(dir, []string{"python3", "into"}); !ok || task != "T-5" {
		t.Errorf("dangling link into the worktree: %v %v", task, ok)
	}
}

func TestPyHelpers(t *testing.T) {
	for _, c := range [][2]string{{"", "."}, {"/a//b/./c/", "/a/b/c"}, {"//a", "//a"}, {"///a", "/a"}, {"a/../..", ".."},
		{"/..", "/"}, {"a/b/../c", "a/c"}} {
		if got := pyNormpath(c[0]); got != c[1] {
			t.Errorf("pyNormpath(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	for _, c := range []struct {
		in string
		n  int
		ok bool
	}{{" 42 ", 42, true}, {"+7", 7, true}, {"-3", -3, true}, {"1_000", 1000, true}, {"٣٤", 34, true},
		{"1__0", 0, false}, {"_1", 0, false}, {"", 0, false}, {"1.5", 0, false}, {" 7 ", 7, true}, {"\v7", 7, true}} {
		n, err := pyIntStr(c.in)
		if (err == nil) != c.ok || (c.ok && n != c.n) {
			t.Errorf("pyIntStr(%q) = %d, %v", c.in, n, err)
		}
	}
	if got := decodeReplace([]byte{'a', 0xff, 0xfe, 'b'}); got != "a��b" {
		t.Errorf("decodeReplace = %q", got)
	}
}

func FuzzProcParsers(f *testing.F) {
	f.Add("1 (a b) S 1 1 1 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0 7 0 0")
	f.Add("garbage")
	f.Add(") ) ) 1 2")
	f.Fuzz(func(t *testing.T, s string) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, "5"), 0o755); err != nil {
			t.Skip()
		}
		for _, leaf := range []string{"stat", "cmdline", "environ", "cgroup"} {
			if err := os.WriteFile(filepath.Join(dir, "5", leaf), []byte(s), 0o644); err != nil {
				t.Skip()
			}
		}
		p := &proc{root: dir}
		p.stat(5)
		p.argv(5)
		p.environ(5)
		p.unitPids()
		isTestRunner(strings.Fields(s))
		searchTaskID(s)
		pyNormpath(s)
		pyIntStr(s)
	})
}
