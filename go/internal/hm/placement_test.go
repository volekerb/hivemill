package hm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const meminfoText = "MemTotal:       12183124 kB\nMemFree:         2933552 kB\nMemAvailable:    %d kB\n"

const vmStatText = `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                                     9650.
Pages active:                                 633543.
Pages inactive:                               100000.
Pages speculative:                              2750.
Pages wired down:                             200000.
`

func TestParseProbe(t *testing.T) {
	if !strings.Contains(probeScript, `env -i HOME="$HOME" "$HOME/.local/bin/hivemill" local coordinator`) {
		t.Error("the probe must ask the worker hivemill under env -i: sessions start that way, so only its coordinator file reaches hooks")
	}
	p := ParseProbe("home=/u\nos=Linux\nmarker=tok\ncoordinator=coord\n---mem\n" + fmt.Sprintf(meminfoText, 9118088))
	if p.Home != "/u" || p.OS != "Linux" || p.Marker != "tok" || p.Coordinator != "coord" || p.MemMB != 8904 {
		t.Errorf("linux probe %+v", p)
	}
	// (9650 + 100000 + 2750) pages x 16 KiB = 1756 MB
	if p := ParseProbe("os=Darwin\n---mem\npressure=1\n" + vmStatText); p.MemMB != 1756 {
		t.Errorf("darwin probe %+v", p)
	}
	if p := ParseProbe("os=Darwin\n---mem\npressure=4\n" + vmStatText); p.MemMB != 0 {
		t.Errorf("darwin under memory pressure counts as no memory: %+v", p)
	}
	if p := ParseProbe("os=Linux\n---mem\n"); p.MemMB != -1 || p.Coordinator != "" {
		t.Errorf("unreadable: %+v", p)
	}
}

// fakeMachines answers hc for a set of workers: mem in MB, whether it shares the home (reads the marker), its user home,
// its coordinator; it records every hc call.
type fakeWorker struct {
	memMB       int
	shared      bool
	home, coord string
	cloneHead   string
}

func placementRunner(t *testing.T, workers map[string]fakeWorker) (*Runner, *SQLite, *[]string) {
	r, s := testRunner(t, "")
	var calls []string
	r.Tools.HC = func(args ...string) (int, string, string) {
		calls = append(calls, strings.Join(args, " "))
		switch {
		case args[0] == "server":
			return 0, `{"core": {"name": "coord-core"}}`, ""
		case args[0] == "ls":
			return 0, `{"machines": [{"name": "w1", "state": "connected"}, {"name": "w-down", "state": "disconnected"}], "terminals": []}`, ""
		case args[0] == "exec" && args[1] == "--on":
			w, ok := workers[args[2]]
			if !ok {
				return 1, "", "no such machine"
			}
			if args[3] == "git" {
				return 0, w.cloneHead + "\n", ""
			}
			if args[3] == "env" {
				return 0, "", ""
			}
			marker := ""
			if w.shared {
				b, _ := os.ReadFile(args[len(args)-2])
				marker = strings.TrimSpace(string(b))
			}
			home := w.home
			if home == "" {
				home = r.Env.UserHome
			}
			return 0, fmt.Sprintf("home=%s\nos=Linux\nmarker=%s\ncoordinator=%s\n---mem\n"+meminfoText, home, marker, w.coord,
				w.memMB*1024), ""
		}
		return 0, "{}", ""
	}
	return r, s, &calls
}

func withLocalMem(t *testing.T, mb int) {
	old := MemAvailableMB
	MemAvailableMB = func() int { return mb }
	t.Cleanup(func() { MemAvailableMB = old })
}

func TestPickMachine(t *testing.T) {
	workers := map[string]fakeWorker{
		"w-low":     {memMB: 2000, shared: true, coord: "c"},
		"w-unshare": {memMB: 9000, shared: false, coord: "c"},
		"w-nofwd":   {memMB: 9000, shared: true},
		"w-ohome":   {memMB: 9000, shared: true, coord: "c", home: "/elsewhere"},
		"w1":        {memMB: 9000, shared: true, coord: "c"},
	}
	r, s, _ := placementRunner(t, workers)
	withLocalMem(t, 8000)
	if pl := r.PickMachine(); pl.Machine != "" || len(pl.Skipped) != 0 {
		t.Errorf("empty session_machines: local, no probe: %+v", pl)
	}
	_ = s.SetCfg("session_machines", "w-low,w-unshare,w-nofwd,w-ohome,w-missing,w1,local")
	pl := r.PickMachine()
	if pl.Machine != "w1" || len(pl.Skipped) != 5 {
		t.Fatalf("pick %+v", pl)
	}
	for i, want := range []string{"w-low: 2000 MB MemAvailable < 2500 MB", "w-unshare: does not share the factory home",
		"w-nofwd: its hivemill does not forward", "w-ohome: user home", "w-missing: hc exec exit 1"} {
		if !strings.HasPrefix(pl.Skipped[i], want) {
			t.Errorf("skip %d: %q, want %q", i, pl.Skipped[i], want)
		}
	}
	if left, _ := filepath.Glob(filepath.Join(r.Env.StateDir(), "placement", "marker-*")); len(left) != 0 {
		t.Errorf("markers left behind: %v", left)
	}
	// the floor is a config key; local listed first wins when it has room
	_ = s.SetCfg("session_machines", "local,w1")
	if pl := r.PickMachine(); pl.Machine != "" {
		t.Errorf("local first with room: %+v", pl)
	}
	withLocalMem(t, 1000)
	if pl := r.PickMachine(); pl.Machine != "w1" || !strings.HasPrefix(pl.Skipped[0], "local: 1000 MB") {
		t.Errorf("local below the floor moves on: %+v", pl)
	}
	// fallback: no listed machine qualifies -> the local core
	_ = s.SetCfg("session_machines", "w-low,w-unshare")
	if pl := r.PickMachine(); pl.Machine != "" || len(pl.Skipped) != 2 {
		t.Errorf("fallback to local: %+v", pl)
	}
	_ = s.SetCfg("machine_mem_floor_mb", "1500")
	if pl := r.PickMachine(); pl.Machine != "w-low" {
		t.Errorf("a lower floor admits w-low: %+v", pl)
	}
	_ = s.SetCfg("session_machines", "-oProxyCommand=x,coord-core")
	withLocalMem(t, 8000)
	if pl := r.PickMachine(); pl.Machine != "" || len(pl.Skipped) != 1 || !strings.Contains(pl.Skipped[0], "not a machine name") {
		t.Errorf("option-like names are refused and the core name is local: %+v", pl)
	}
}

func TestPlacementLocalFloorOnlyWhenLocal(t *testing.T) {
	r, s, _ := placementRunner(t, nil)
	c := Caps{MemMB: 500, MemMin: 1024}
	if why := c.DispatchBlock(s, ""); !strings.HasPrefix(why, "memory:") && !strings.HasPrefix(why, "usage unknown") {
		t.Errorf("no placement: the local floor holds dispatch: %q", why)
	}
	_ = s.SetCfg("session_machines", "w1")
	_ = r
	if why := c.DispatchBlock(s, ""); strings.HasPrefix(why, "memory:") {
		t.Errorf("with placement the local floor waits for the pick: %q", why)
	}
}

func setupRoles(t *testing.T, r *Runner) {
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, f := range []string{"task.json.in", "task.md", "task-judge.json.in", "task-judge.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", f), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
}

func TestPlacedSessionEventsAndStatus(t *testing.T) {
	r, s, calls := placementRunner(t, map[string]fakeWorker{"w1": {memMB: 9000, shared: true, coord: "c"}})
	setupRoles(t, r)
	run := &Run{Task: "T-0001", Goal: "G-001", Agent: "claude-coder-t-0001-a7", State: Working, Round: 1, Worktree: t.TempDir(),
		Base: "main", Model: "claude-sonnet-5-5", Effort: "medium", Machine: "w1"}
	_ = s.PutRun(run)
	if err := r.launchTask(run, "go"); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(*calls, "\n")
	if !strings.Contains(joined, "run --bg --no-parent --name claude-coder-t-0001-a7 --cwd "+run.Worktree+" --on w1 sh ") {
		t.Errorf("hc run must target w1:\n%s", joined)
	}
	if !strings.Contains(joined, "exec --on w1 env FACTORY_HOME="+r.Env.Home+" "+filepath.Join(r.Env.UserHome, ".local", "bin", "hivemill")+
		" local trust "+run.Worktree) {
		t.Errorf("the clone is trusted on w1:\n%s", joined)
	}
	ev, _ := os.ReadFile(r.Log.Path)
	if !strings.Contains(string(ev), `"event":"session.started"`) || !strings.Contains(string(ev), `"machine":"w1"`) {
		t.Errorf("session.started carries machine: %s", ev)
	}
	// the judge stays local even for a placed run
	*calls = nil
	r2, s2, calls2 := placementRunner(t, nil)
	setupRoles(t, r2)
	if _, err := r2.StartSession(run, Launch{Role: "task-judge", Name: "claude-judge-t-0001", Cwd: run.Worktree, Model: "claude-sonnet-5-5",
		Effort: "medium", PromptFile: filepath.Join(r2.Env.Home, "roles", "hivemill", "task-judge.md"), Kickoff: "x"}); err != nil {
		t.Fatal(err)
	}
	if j := strings.Join(*calls2, "\n"); !strings.Contains(j, "--on coord-core sh ") || strings.Contains(j, "--on w1") {
		t.Errorf("judge on the local core:\n%s", j)
	}
	ev2, _ := os.ReadFile(r2.Log.Path)
	if !strings.Contains(string(ev2), `"machine":"local"`) {
		t.Errorf("local session event: %s", ev2)
	}
	_ = s2
	var out bytes.Buffer
	r.Out = &out
	_ = r.Status()
	if !strings.Contains(out.String(), "T-0001 working   round 1 blocks 0 judge 0 ci 0 on w1") {
		t.Errorf("run status shows the machine: %q", out.String())
	}
}

func TestReconcileResumesOnTheRecordedMachine(t *testing.T) {
	r, s, calls := placementRunner(t, nil)
	setupRoles(t, r)
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wt, ".git"), 0o755)
	run := &Run{Task: "T-0001", Goal: "G-001", Agent: "claude-coder-t-0001-a7", State: Working, Round: 1, Worktree: wt, Base: "main",
		Model: "claude-sonnet-5-5", Effort: "medium", Machine: "w1"}
	_ = s.PutRun(run)
	_ = s.AddSession(Session{SID: "sid-1", Task: "T-0001", Role: "task", Agent: run.Agent, Model: run.Model, Effort: run.Effort, Started: nowISO()})
	if _, err := r.Advance("T-0001", []Terminal{}, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Run("T-0001")
	if got.Resumes != 1 || !strings.Contains(strings.Join(*calls, "\n"), "--cwd "+wt+" --on w1 sh ") {
		t.Fatalf("resume on w1: resumes %d calls\n%s", got.Resumes, strings.Join(*calls, "\n"))
	}
	// a run on a machine hc lists as disconnected is not resumed (hc keeps the session until it reconnects)
	got.Machine = "w-down"
	_ = s.PutRun(got)
	*calls = nil
	_, _ = r.Advance("T-0001", []Terminal{}, true)
	if got, _ = s.Run("T-0001"); got.Resumes != 1 || got.State != Working || strings.Contains(strings.Join(*calls, "\n"), "run --bg") {
		t.Errorf("disconnected machine: resumes %d state %s\n%s", got.Resumes, got.State, strings.Join(*calls, "\n"))
	}
}

func TestCloneOnAndHostMem(t *testing.T) {
	r, _, _ := placementRunner(t, map[string]fakeWorker{"w1": {cloneHead: strings.Repeat("a", 40)}, "w2": {cloneHead: "b"}})
	if why := r.cloneOn("w1", "/x"); why != "" {
		t.Errorf("same head: %q", why)
	}
	if why := r.cloneOn("w2", "/x"); why != "the clone head differs there" {
		t.Errorf("other head: %q", why)
	}
	mb := MemAvailableMB()
	t.Logf("this host: MemAvailable %d MB", mb)
	if mb < 0 {
		t.Errorf("this host's memory must be readable (linux /proc, darwin vm_stat): %d", mb)
	}
}
