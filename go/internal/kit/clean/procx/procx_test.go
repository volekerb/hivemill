package procx

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func startChild(t *testing.T, dir string, extra ...*os.File) *exec.Cmd {
	t.Helper()
	cmd := exec.Command("sleep", "30")
	cmd.Dir = dir
	cmd.ExtraFiles = extra
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	return cmd
}

func stop(cmd *exec.Cmd) {
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func want(t *testing.T, got Result, s Status) {
	t.Helper()
	if got.Status != s {
		t.Fatalf("got %v (%s), want %v", got.Status, got.Reason, s)
	}
}

// wantGone asserts the child's evidence is gone: Pass, or Unknown when the host
// has same-user processes the kernel hides from us (sandbox, extra capabilities);
// never Fail. Pass itself is proven deterministically by the fake /proc tests.
func wantGone(t *testing.T, got Result) {
	t.Helper()
	if got.Status == Fail {
		t.Fatalf("still reported after exit: %s", got.Reason)
	}
	if got.Status == Unknown {
		t.Logf("host hides some processes, Unknown is the fail-closed answer: %s", got.Reason)
	}
}

func TestCwdInsideRealChild(t *testing.T) {
	tmp := t.TempDir()
	a := filepath.Join(tmp, "a")
	if err := os.Mkdir(a, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := startChild(t, a)
	want(t, CwdInside(tmp), Fail)
	want(t, CwdInside(a), Fail)
	stop(cmd)
	wantGone(t, CwdInside(tmp))
}

func TestHeldOpenRealChild(t *testing.T) {
	tmp := t.TempDir()
	held := filepath.Join(tmp, "held.tmp")
	f, err := os.Create(held)
	if err != nil {
		t.Fatal(err)
	}
	cmd := startChild(t, os.TempDir(), f)
	f.Close() // parent must not hold it
	want(t, HeldOpen(held), Fail)
	want(t, HeldOpen(tmp), Fail)
	stop(cmd)
	wantGone(t, HeldOpen(held))
	wantGone(t, HeldOpen(tmp))
}

func TestProcessesRealSelf(t *testing.T) {
	ps, err := Processes()
	if err != nil {
		t.Fatal(err)
	}
	cwd, _ := os.Getwd()
	for _, p := range ps {
		if p.PID == os.Getpid() {
			if p.PPID != os.Getppid() || p.UID != os.Getuid() || p.Comm == "" || p.Start == "" {
				t.Fatalf("bad self row %+v", p)
			}
			if p.Cwd != cwd {
				t.Fatalf("cwd %q want %q", p.Cwd, cwd)
			}
			return
		}
	}
	t.Fatal("own pid not listed")
}

func TestAgentContextEnv(t *testing.T) {
	procs := []Process{{PID: 1, PPID: 0, Comm: "systemd"}, {PID: 10, PPID: 1, Comm: "hm-clean"}}
	for _, e := range []string{"CLAUDECODE=1", "FACTORY_AGENT=x", "FACTORY_ROLE=x", "CURSOR_AGENT=1"} {
		got, _, _ := agentContextFrom([]string{"PATH=/bin", e}, procs, 10)
		if !got {
			t.Errorf("%s: want agent", e)
		}
	}
	// the live-environment entry point
	got, _, _ := AgentContext([]string{"CLAUDECODE=1"}, nil)
	if !got {
		t.Error("public AgentContext with env var: want agent")
	}
}

func TestAgentContextAncestry(t *testing.T) {
	mk := func(comm string, argv ...string) []Process {
		return []Process{
			{PID: 1, PPID: 0, Comm: "systemd"},
			{PID: 5, PPID: 1, Comm: comm, Argv: argv},
			{PID: 6, PPID: 5, Comm: "bash"},
			{PID: 10, PPID: 6, Comm: "hm-clean"},
		}
	}
	for name, ps := range map[string][]Process{
		"claude":       mk("claude"),
		"cursor-agent": mk("cursor-agent"),
		"herdr":        mk("herdr"),
		"argv0":        mk("node", "/usr/bin/claude"),
		"npm":          mk("node", "node", "/x/@anthropic-ai/claude-code/cli.js"),
	} {
		got, why, err := agentContextFrom(nil, ps, 10)
		if !got || err != nil || why == "" {
			t.Errorf("%s: got %v %q %v", name, got, why, err)
		}
	}
	got, _, err := AgentContext(nil, append(mk("claude"), Process{PID: os.Getpid(), PPID: 10, Comm: "x"}))
	if !got || err != nil {
		t.Errorf("public ancestry: %v %v", got, err)
	}
}

func TestAgentContextErrorFailsClosed(t *testing.T) {
	ps := []Process{{PID: 1, Comm: "systemd"}, {PID: 10, PPID: 6, Comm: "hm-clean"}} // 6 missing
	got, _, err := agentContextFrom(nil, ps, 10)
	if !got || err == nil {
		t.Errorf("missing ancestor: %v %v", got, err)
	}
	got, _, err = agentContextFrom(nil, ps, 99)
	if !got || err == nil {
		t.Errorf("missing self: %v %v", got, err)
	}
	loop := []Process{{PID: 5, PPID: 6}, {PID: 6, PPID: 5}, {PID: 10, PPID: 5}}
	if got, _, err = agentContextFrom(nil, loop, 10); !got || err == nil {
		t.Errorf("loop: %v %v", got, err)
	}
}

func TestAgentContextScheduledJobAllowed(t *testing.T) {
	for _, init := range []string{"systemd", "launchd"} {
		ps := []Process{{PID: 1, PPID: 0, Comm: init}, {PID: 10, PPID: 1, Comm: "hm-clean", Argv: []string{"hm-clean"}}}
		got, why, err := agentContextFrom([]string{"PATH=/usr/bin", "CLAUDECODE="}, ps, 10)
		if got || err != nil {
			t.Errorf("%s: got %v %q %v", init, got, why, err)
		}
	}
}

func TestDarwinParsers(t *testing.T) {
	read := func(n string) []byte {
		b, err := os.ReadFile(filepath.Join("testdata", n))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	ps, err := parsePS(read("ps_darwin.txt"))
	if err != nil || len(ps) != 4 {
		t.Fatalf("%v %+v", err, ps)
	}
	if ps[1].Comm != "/Applications/Visual Studio Code.app/Contents/MacOS/Electron" || ps[2].Start != "Tue Nov 11 9:01:02 2026" || ps[2].UID != 501 || ps[3].PPID != 800 {
		t.Errorf("bad parse %+v", ps)
	}
	args, err := parsePSArgs(read("psargs_darwin.txt"))
	if err != nil || args[800][1] != "/usr/lib/node_modules/@anthropic-ai/claude-code/cli.js" {
		t.Fatalf("%v %v", err, args)
	}
	if _, err := parsePS([]byte("garbage\n")); err == nil {
		t.Error("garbage ps accepted")
	}
	if _, err := parseLsofF([]byte("n/x\n")); err == nil {
		t.Error("name before pid accepted")
	}
	// full darwin Processes + AgentContext on fixtures
	fake := func(path string, a ...string) ([]byte, []byte, int, error) {
		switch {
		case path == psPath && a[1] == "pid=,ppid=,uid=,lstart=,comm=":
			return read("ps_darwin.txt"), nil, 0, nil
		case path == psPath:
			return read("psargs_darwin.txt"), nil, 0, nil
		}
		return read("lsof_cwd_darwin.txt"), nil, 0, nil
	}
	procs, err := processesPS(fake)
	if err != nil || procs[2].Cwd != "/Users/o/work/proj" || procs[3].Argv[1] != "--dry-run" {
		t.Fatalf("%v %+v", err, procs)
	}
	if got, _, _ := agentContextFrom(nil, procs, 900); !got {
		t.Error("fixture ancestry should be an agent (claude)")
	}
}

func TestDarwinLsofCwdAndHeld(t *testing.T) {
	cwd, _ := os.ReadFile("testdata/lsof_cwd_darwin.txt")
	held, _ := os.ReadFile("testdata/lsof_held_darwin.txt")
	ok := func(out []byte, exit int) runner {
		return func(string, ...string) ([]byte, []byte, int, error) { return out, nil, exit, nil }
	}
	want(t, cwdInsideLsof(ok(cwd, 0), "/private/var/folders/x/T/scratch"), Fail)
	want(t, cwdInsideLsof(ok(cwd, 0), "/private/var/folders/x/T/other"), Pass)
	dir := t.TempDir()
	want(t, heldOpenLsof(ok(held, 0), dir), Fail)
	want(t, heldOpenLsof(ok(nil, 1), dir), Pass) // exit 1, empty output = none held
	f := filepath.Join(dir, "f")
	os.WriteFile(f, nil, 0o600)
	want(t, heldOpenLsof(ok(held, 0), f), Fail)
	want(t, heldOpenLsof(ok(nil, 1), f), Pass)
}

func TestDarwinUnknownFailsClosed(t *testing.T) {
	dir := t.TempDir()
	missing := func(string, ...string) ([]byte, []byte, int, error) {
		return nil, nil, -1, errors.New("fork/exec /usr/sbin/lsof: no such file or directory")
	}
	want(t, cwdInsideLsof(missing, dir), Unknown)
	want(t, heldOpenLsof(missing, dir), Unknown)
	bad := func(string, ...string) ([]byte, []byte, int, error) {
		return nil, []byte("lsof: can't do it\n"), 1, nil
	}
	want(t, cwdInsideLsof(bad, dir), Unknown)
	want(t, heldOpenLsof(bad, dir), Unknown)
	exit2 := func(string, ...string) ([]byte, []byte, int, error) { return nil, nil, 2, nil }
	want(t, heldOpenLsof(exit2, dir), Unknown)
	garbage := func(string, ...string) ([]byte, []byte, int, error) { return []byte("zzz\nn/x\n"), nil, 0, nil }
	want(t, heldOpenLsof(garbage, dir), Unknown)
	want(t, heldOpenLsof(missing, filepath.Join(dir, "nonexistent")), Unknown)
	if _, err := processesPS(missing); err == nil {
		t.Error("processesPS with missing ps must error")
	}
}
