package factorytest

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Tests of the stateful herdr stub (stub/herdr.go), the Go port of tests/stubs/herdr.

func herdrEnv(t *testing.T) *Env {
	t.Helper()
	e := New(t)
	e.HerdrStateful()
	return e
}

func (e *Env) herdr(args ...string) Result { e.T.Helper(); return e.RunStub("herdr", "", nil, args...) }

func wantOut(t *testing.T, r Result, exit int, stdout string) {
	t.Helper()
	if r.Exit != exit || r.Stdout != stdout {
		t.Fatalf("exit %d stdout %q stderr %q; want exit %d stdout %q", r.Exit, r.Stdout, r.Stderr, exit, stdout)
	}
}

func TestHerdrStubLifecycle(t *testing.T) {
	e := herdrEnv(t)
	wantOut(t, e.herdr("tab", "create", "--workspace", "w1", "--label", "x"), 0,
		`{"id": "fake", "result": {"tab": {"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}, "root_pane": {"pane_id": "p2", "tab_id": "t1", "workspace_id": "w1"}}}`+"\n")
	wantOut(t, e.herdr("agent", "start", "zz-a", "--pane", "p2", "--timeout", "5000"), 0,
		`{"id": "fake", "result": {"agent": {"name": "zz-a", "agent": "claude", "pane_id": "p2", "agent_status": "idle", "agent_session": {"value": "sess-zz-a"}}}}`+"\n")
	wantOut(t, e.herdr("agent", "list"), 0,
		`{"id": "fake", "result": {"agents": [{"name": "zz-a", "agent": "claude", "pane_id": "p2", "agent_status": "idle", "agent_session": {"value": "sess-zz-a"}}]}}`+"\n")
	wantOut(t, e.herdr("tab", "list", "--workspace", "w1"), 0,
		`{"id": "fake", "result": {"tabs": [{"tab_id": "t1", "workspace_id": "w1", "label": "x", "pane_count": 1}]}}`+"\n")
	wantOut(t, e.herdr("tab", "list", "--workspace", "w2"), 0, `{"id": "fake", "result": {"tabs": []}}`+"\n")
	wantOut(t, e.herdr("pane", "close", "p2"), 0, `{"id": "fake", "result": {}}`+"\n")
	wantOut(t, e.herdr("agent", "list"), 0, `{"id": "fake", "result": {"agents": []}}`+"\n")
	// the log has one record per call, in the Python byte format, with the sock field
	lines := e.CallLines()
	if len(lines) != 7 || lines[0] != `{"tool": "herdr", "argv": ["tab", "create", "--workspace", "w1", "--label", "x"], "sock": null}` {
		t.Fatalf("call log = %q", lines)
	}
	if b, _ := os.ReadFile(e.HerdrStatePath()); !strings.HasPrefix(string(b), `{"fail": false, "seq": 2, "agents": [], "workspaces": [], "tabs": [{"tab_id": "t1"`) {
		t.Fatalf("state = %s", b)
	}
}

func TestHerdrStubWorkspaceAndSplit(t *testing.T) {
	e := herdrEnv(t)
	r := e.herdr("workspace", "create", "--label", "ws")
	wantOut(t, r, 0, `{"id": "fake", "result": {"workspace": {"workspace_id": "w1", "label": "ws"}, "tab": {"tab_id": "t2", "workspace_id": "w1", "label": "1", "pane_count": 1}, "root_pane": {"pane_id": "p3", "tab_id": "t2", "workspace_id": "w1"}}}`+"\n")
	wantOut(t, e.herdr("pane", "split", "p3"), 0, `{"id": "fake", "result": {"pane": {"pane_id": "p4", "tab_id": "t2", "workspace_id": "w1"}}}`+"\n")
	wantOut(t, e.herdr("tab", "list", "--workspace", "w1"), 0, `{"id": "fake", "result": {"tabs": [{"tab_id": "t2", "workspace_id": "w1", "label": "1", "pane_count": 2}]}}`+"\n")
	wantOut(t, e.herdr("tab", "rename", "t2", "new"), 0, `{"id": "fake", "result": {}}`+"\n")
	wantOut(t, e.herdr("workspace", "list"), 0, `{"id": "fake", "result": {"workspaces": [{"workspace_id": "w1", "label": "ws"}]}}`+"\n")
	wantOut(t, e.herdr("pane", "list", "--workspace", "w1"), 0, `{"id": "fake", "result": {"panes": [{"pane_id": "p3", "tab_id": "t2", "workspace_id": "w1"}, {"pane_id": "p4", "tab_id": "t2", "workspace_id": "w1"}]}}`+"\n")
}

func TestHerdrStubKnobs(t *testing.T) {
	e := herdrEnv(t)
	e.herdr("tab", "create", "--workspace", "w1", "--label", "x")
	e.SetHerdr(map[string]any{"start_fail": true})
	r := e.herdr("agent", "start", "zz-a", "--pane", "p2")
	if r.Exit != 1 || r.Stderr != "herdr: agent_not_ready\n" {
		t.Fatalf("start_fail: %+v", r)
	}
	e.SetHerdr(map[string]any{"start_fail": "custom"})
	if r := e.herdr("agent", "start", "zz-a"); r.Exit != 1 || r.Stderr != "herdr: custom\n" {
		t.Fatalf("start_fail text: %+v", r)
	}
	e.SetHerdr(map[string]any{"start_fail": false, "slow_list": true})
	e.herdr("agent", "start", "zz-a", "--pane", "p2")
	wantOut(t, e.herdr("agent", "list"), 0, `{"id": "fake", "result": {"agents": []}}`+"\n")
	e.SetHerdr(map[string]any{"slow_list": false, "pane_close_fail": true})
	if r := e.herdr("pane", "close", "p2"); r.Exit != 1 || r.Stderr != "herdr: pane close failed\n" {
		t.Fatalf("pane_close_fail: %+v", r)
	}
	e.SetHerdr(map[string]any{"pane_close_fail": false, "pane_close_noop": true})
	wantOut(t, e.herdr("pane", "close", "p2"), 0, `{"id": "fake", "result": {}}`+"\n")
	wantOut(t, e.herdr("pane", "list", "--workspace", "w1"), 0, `{"id": "fake", "result": {"panes": [{"pane_id": "p2", "tab_id": "t1", "workspace_id": "w1"}]}}`+"\n")
	e.SetHerdr(map[string]any{"pane_close_noop": false, "fail_cmds": []string{"tab create"}})
	if r := e.herdr("tab", "create", "--workspace", "w1"); r.Exit != 1 || r.Stderr != "herdr: tab create failed\n" {
		t.Fatalf("fail_cmds: %+v", r)
	}
	if r := e.herdr("tab", "list", "--workspace", "w1"); r.Exit != 0 {
		t.Fatalf("fail_cmds must hit only the listed command: %+v", r)
	}
	e.SetHerdr(map[string]any{"fail_cmds": []string{}, "read_text": map[string]string{"zz-a": "hello"}, "pane_read_text": "pt"})
	wantOut(t, e.herdr("agent", "read", "zz-a"), 0, `{"id": "fake", "result": {"text": "hello"}}`+"\n")
	wantOut(t, e.herdr("agent", "read", "zz-q"), 0, `{"id": "fake", "result": {"text": ""}}`+"\n")
	wantOut(t, e.herdr("pane", "read"), 0, `{"id": "fake", "result": {"text": "pt"}}`+"\n")
	e.SetHerdr(map[string]any{"agent_list_ok": 1})
	if r := e.herdr("agent", "list"); r.Exit != 0 {
		t.Fatalf("first list must work: %+v", r)
	}
	if r := e.herdr("agent", "list"); r.Exit != 1 || r.Stderr != "herdr: agent list failed\n" {
		t.Fatalf("second list must fail: %+v", r)
	}
	e.HerdrFail(true)
	if r := e.herdr("tab", "list"); r.Exit != 1 || r.Stderr != "herdr: server not running\n" {
		t.Fatalf("fail: %+v", r)
	}
	e.HerdrFail(false)
	wantOut(t, e.herdr("pane", "process-info", "--pane", "p2"), 0,
		`{"id": "fake", "result": {"process_info": {"foreground_processes": [{"argv": ["zsh"], "cmdline": "zsh", "name": "zsh", "pid": 1}], "pane_id": "p2", "shell_pid": 1}, "type": "pane_process_info"}}`+"\n")
	if r := e.herdr("pane", "process-info", "x"); r.Exit != 2 || r.Stderr != "herdr: unknown option: x\n" {
		t.Fatalf("process-info positional: %+v", r)
	}
	if r := e.herdr("agent", "start", "zz-a", "--timeout", "3000"); r.Exit != 1 || !strings.Contains(r.Stderr, "invalid_agent_timeout") {
		t.Fatalf("timeout: %+v", r)
	}
}

func TestHerdrStubEmptyAndMissingState(t *testing.T) {
	e := herdrEnv(t)
	for name, content := range map[string]*string{"missing": nil, "empty": ptr(""), "blank": ptr(" \n")} {
		if content == nil {
			must(t, os.Remove(e.HerdrStatePath()))
		} else {
			must(t, os.WriteFile(e.HerdrStatePath(), []byte(*content), 0o644))
		}
		wantOut(t, e.herdr("agent", "list"), 0, `{"id": "fake", "result": {"agents": []}}`+"\n")
		if b, _ := os.ReadFile(e.HerdrStatePath()); string(b) != `{"fail": false, "seq": 0, "agents": [], "workspaces": [], "tabs": [], "panes": []}` {
			t.Fatalf("%s state: state file = %s", name, b)
		}
	}
	must(t, os.WriteFile(e.HerdrStatePath(), []byte("[1]"), 0o644))
	if r := e.herdr("agent", "list"); r.Exit != 1 || !strings.Contains(r.Stderr, "not a JSON object") {
		t.Fatalf("a list state: %+v", r)
	}
	must(t, os.WriteFile(e.HerdrStatePath(), []byte("{"), 0o644))
	if r := e.herdr("agent", "list"); r.Exit != 1 || !strings.Contains(r.Stderr, "is not JSON") {
		t.Fatalf("a broken state: %+v", r)
	}
}

func ptr(s string) *string { return &s }

func (e *Env) hookEnv(cmd, at string) map[string]string {
	return map[string]string{"FAKE_HERDR_HOOK": cmd, "FAKE_HERDR_HOOK_AT": at}
}

func TestHerdrStubHookOnce(t *testing.T) {
	e := herdrEnv(t)
	x := e.hookEnv("echo hook-ran", "")
	r := e.RunStub("herdr", "", x, "agent", "list")
	wantOut(t, r, 0, "hook-ran\n"+`{"id": "fake", "result": {"agents": []}}`+"\n")
	if _, err := os.Stat(e.HerdrStatePath() + ".hook-ran"); err != nil {
		t.Fatalf("marker: %v", err)
	}
	wantOut(t, e.RunStub("herdr", "", x, "agent", "list"), 0, `{"id": "fake", "result": {"agents": []}}`+"\n")
}

func TestHerdrStubHookAt(t *testing.T) {
	e := herdrEnv(t)
	x := e.hookEnv("echo hook-ran", "agent start")
	wantOut(t, e.RunStub("herdr", "", x, "agent", "list"), 0, `{"id": "fake", "result": {"agents": []}}`+"\n")
	wantOut(t, e.RunStub("herdr", "", x, "agent"), 0, `{"id": "fake", "result": {"text": ""}}`+"\n")
	if r := e.RunStub("herdr", "", x, "agent", "start", "zz-a"); !strings.HasPrefix(r.Stdout, "hook-ran\n") || r.Exit != 0 {
		t.Fatalf("hook-at start: %+v", r)
	}
	if r := e.RunStub("herdr", "", x, "agent", "start", "zz-b"); strings.Contains(r.Stdout, "hook-ran") {
		t.Fatalf("the hook ran twice: %+v", r)
	}
}

// The hook's own herdr call is logged, runs, and does not run the hook again (the marker exists before the hook starts);
// a failing hook is ignored.
func TestHerdrStubHookReentrantAndFailing(t *testing.T) {
	e := herdrEnv(t)
	x := e.hookEnv("herdr tab create --workspace w1 --label hooked; exit 3", "tab list")
	r := e.RunStub("herdr", "", x, "tab", "list", "--workspace", "w1")
	if r.Exit != 0 || strings.Count(r.Stdout, `"label": "hooked"`) != 2 { // the hook's tab create answer, then the list that holds the new tab
		t.Fatalf("re-entrant hook: %+v", r)
	}
	lines := e.CallLines()
	if len(lines) != 2 || !strings.Contains(lines[0], `"tab", "list"`) || !strings.Contains(lines[1], `"tab", "create"`) {
		t.Fatalf("call log = %q", lines)
	}
}

// A hook that hangs is ended by the Env run limit with a timeout error, not a hang.
func TestHerdrStubHangingHookEndsWithTimeoutError(t *testing.T) {
	e := herdrEnv(t)
	e.Timeout = time.Second
	r := e.RunStub("herdr", "", e.hookEnv("sleep 3", ""), "agent", "list")
	if r.Exit != -1 || !strings.Contains(r.Stderr, "timed out after 1s") {
		t.Fatalf("hanging hook: %+v", r)
	}
}

func TestHerdrStubScriptedModeIsTheDefault(t *testing.T) {
	e := New(t)
	e.Script("herdr", StubRule{Match: []string{"agent", "list"}, Out: "scripted\n"})
	wantOut(t, e.herdr("agent", "list"), 0, "scripted\n")
	wantOut(t, e.herdr("tab", "list"), 0, `{"id": "fake", "result": {}}`+"\n")
	if _, err := os.Stat(e.HerdrStatePath()); err == nil {
		t.Fatal("the scripted mode must not create the state file")
	}
}

// ---- parity with tests/stubs/herdr -------------------------------------------------------------------------------

// pstep is one call of the parity sequence. seed (when set) is written to both state files first; rmMarker removes
// the hook marker; env adds environment (the hook).
type pstep struct {
	seed      *string
	rmMarker  bool
	env       map[string]string
	argv      []string
	wantExit  int
	describes string
}

func seedOf(s string) *string { return &s }

func parityCalls() []pstep {
	c := func(argv ...string) pstep { return pstep{argv: argv} }
	k := func(seed string, argv ...string) pstep { return pstep{seed: seedOf(seed), argv: argv} }
	ex := func(exit int, argv ...string) pstep { return pstep{argv: argv, wantExit: exit} }
	hook := func(cmd, at string) map[string]string {
		return map[string]string{"FAKE_HERDR_HOOK": cmd, "FAKE_HERDR_HOOK_AT": at}
	}
	return []pstep{
		// ids, tabs, panes, workspaces, agents
		k(`{}`, "tab", "create", "--workspace", "w9", "--label", "lab"),
		c("workspace", "create", "--label", "ws1"),
		c("workspace", "list"),
		c("tab", "create", "--workspace", "w2", "--label", "t"),
		c("tab", "list", "--workspace", "w2"),
		c("tab", "list"),
		c("tab", "rename", "t1", "renamed"),
		c("pane", "list"),
		c("pane", "list", "--workspace", "w9"),
		c("pane", "split", "p2"),
		c("tab", "list", "--workspace", "w9"),
		c("agent", "start", "zz-a", "--pane", "p2", "--session-id", "s-a", "--timeout", "5000"),
		c("agent", "start", "zz-b", "--pane", "p3"),
		c("agent", "start", "zz-a", "--pane", "p7"),
		ex(1, "agent", "start", "zz-c", "--timeout", "3000"),
		ex(1, "agent", "start", "zz-c", "--timeout", "300001"),
		c("agent", "start", "zz-c", "--timeout", "300000"),
		c("agent", "start", "zz-d", "--timeout", "٣٠٠٠٠"),
		c("agent", "start", "zz-e", "--timeout", " 1_0000 "),
		c("agent", "list"),
		c("pane", "process-info", "--pane", "p2"),
		c("pane", "process-info", "--pane", "nope"),
		ex(2, "pane", "process-info"),
		ex(2, "pane", "process-info", "zzz"),
		c("pane", "close", "p2"),
		c("agent", "list"),
		c("agent", "wait", "zz-b"), c("agent", "send-keys", "zz-b", "x"), c("agent", "prompt", "zz-b", "hi"),
		c("agent", "read", "zz-b"), c("pane", "read"), c("agent", "stop", "zz-b"), c("agent"), c(),
		// every state knob
		k(`{"slow_list": true}`, "agent", "start", "zz-c", "--pane", "p7"), c("agent", "list"),
		k(`{"start_fail": true}`, "agent", "start", "zz-c"),
		k(`{"start_fail": "custom err"}`, "agent", "start", "zz-c"),
		k(`{"start_fail": ""}`, "agent", "start", "zz-c"),
		k(`{"start_sleep": 0.01}`, "agent", "start", "zz-d", "--timeout", "90000", "--pane", "p1"),
		c("agent", "start", "zz-d", "--timeout", "5000"),
		k(`{"wait_sleep": 0.01}`, "agent", "wait", "x"),
		k(`{"panes": [{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}], "agents": [{"name": "a", "pane_id": "p1"}], "pane_close_fail": true}`, "pane", "close", "p1"),
		k(`{"panes": [{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}], "pane_close_noop": true}`, "pane", "close", "p1"), c("pane", "list"),
		k(`{"fail_cmds": ["tab create", "agent list"]}`, "tab", "create", "--workspace", "w1"),
		c("agent", "list"), c("tab", "list", "--workspace", "w1"),
		k(`{"read_text": {"zz-a": "hello é"}, "pane_read_text": "pt"}`, "agent", "read", "zz-a"),
		c("agent", "read", "zz-q"), c("pane", "read"),
		k(`{"agent_list_ok": 1}`, "agent", "list"), ex(1, "agent", "list"), ex(1, "agent", "list"),
		k(`{"fail": true}`, "tab", "list"), ex(1, "agent", "list"),
		k(`{"fail": false, "panes": [{"pane_id": "p1", "fg": "claude"}, {"pane_id": "p2", "raw_result": {"x": [1, 2]}}]}`, "pane", "process-info", "--pane", "p1"),
		c("pane", "process-info", "--pane", "p2"),
		k(`{"seq": 9, "panes": [{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"}], "tabs": [{"tab_id": "t1", "workspace_id": "w1", "label": "a", "pane_count": 1}]}`, "pane", "split", "p1"),
		c("tab", "list", "--workspace", "w1"),
		// values the Python json module round-trips
		k(`{"start_sleep": 0.5, "wait_sleep": 1e-05, "big": 123456789012345678901234567890, "f": 1e22, "n": NaN, "i": -Infinity, "z": -0.0, "s": "é😀\u0000\ud800", "k": 1, "k": 2}`, "agent", "list"),
		{argv: []string{"tab", "create", "--workspace", "wé", "--label", "\U0001f600 \"q\" \\ \t"}},
		{argv: []string{"tab", "create", "--workspace", "w1", "--label", "bad\xffbyte"}},
		{env: map[string]string{"HERDR_SOCKET_PATH": "/tmp/x.sock"}, argv: []string{"agent", "list"}},
		// hooks: once on the first call; at a call; re-entrant; failing; a non-matching at
		{seed: seedOf(`{}`), rmMarker: true, env: hook("echo hook-ran", ""), argv: []string{"agent", "list"}},
		{env: hook("echo hook-ran", ""), argv: []string{"agent", "list"}},
		{rmMarker: true, env: hook("echo hook-at", "agent start"), argv: []string{"agent", "list"}},
		{env: hook("echo hook-at", "agent start"), argv: []string{"agent", "start", "zz-h", "--pane", "p1"}},
		{env: hook("echo hook-at", "agent start"), argv: []string{"agent", "start", "zz-i"}},
		{rmMarker: true, env: hook("herdr tab create --workspace w1 --label hooked; exit 3", ""), argv: []string{"tab", "list", "--workspace", "w1"}},
		{rmMarker: true, env: hook("echo never", "agent  start"), argv: []string{"agent", "start", "zz-j"}},
		{rmMarker: true, env: hook("echo prefix", "agent"), argv: []string{"agent", "start", "zz-k"}},
		{rmMarker: true, env: hook("echo longer-at", "agent start zz-k extra"), argv: []string{"agent", "start", "zz-k"}},
	}
}

// TestHerdrStubParity runs one call sequence through the Go stub and, when python3 is on PATH, through
// tests/stubs/herdr with the same seeds, and compares stdout, exit code, stderr, the state file and calls.jsonl byte for byte.
func TestHerdrStubParity(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH: the Go stub is checked against fixed answers in the other TestHerdrStub tests; the byte comparison with tests/stubs/herdr runs where python3 exists")
	}
	repo, err := RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(repo, "tests", "stubs", "herdr")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("the Python stub %s is missing: %v; the parity test reads the real file", script, err)
	}
	e := herdrEnv(t)
	dir := t.TempDir()
	pyBin := filepath.Join(dir, "bin")
	must(t, os.MkdirAll(pyBin, 0o755))
	must(t, os.Symlink(script, filepath.Join(pyBin, "herdr")))
	pyState, pyCalls := filepath.Join(dir, "herdr.json"), filepath.Join(dir, "calls.jsonl")
	must(t, os.WriteFile(pyCalls, nil, 0o644))
	for i, s := range parityCalls() {
		if s.seed != nil {
			must(t, os.WriteFile(e.HerdrStatePath(), []byte(*s.seed), 0o644))
			must(t, os.WriteFile(pyState, []byte(*s.seed), 0o644))
		}
		if s.rmMarker {
			for _, m := range []string{e.HerdrStatePath() + ".hook-ran", pyState + ".hook-ran"} {
				if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
		g := e.RunStub("herdr", "", s.env, s.argv...)
		cmd := exec.Command(py, append([]string{script}, s.argv...)...)
		env := e.Environ("", s.env)
		env = append(env, "FAKE_HERDR_STATE="+pyState, "FAKE_CALLS="+pyCalls, "PATH="+pyBin+":"+os.Getenv("PATH"))
		cmd.Env = env
		for j, kv := range cmd.Env { // the later duplicates win in exec, but keep the env unambiguous
			if strings.HasPrefix(kv, "FAKE_HERDR_STATE=") && !strings.HasSuffix(kv, pyState) || strings.HasPrefix(kv, "FAKE_CALLS=") && !strings.HasSuffix(kv, pyCalls) ||
				strings.HasPrefix(kv, "PATH=") && !strings.HasPrefix(kv, "PATH="+pyBin) {
				cmd.Env[j] = "X_DROPPED=1"
			}
		}
		cmd.Dir = e.Root
		var so, se bytes.Buffer
		cmd.Stdout, cmd.Stderr = &so, &se
		perr := cmd.Run()
		pexit := 0
		if ee, ok := perr.(*exec.ExitError); ok {
			pexit = ee.ExitCode()
		} else if perr != nil {
			t.Fatal(perr)
		}
		at := func(what string) string { return "step " + itoa(i) + " " + strings.Join(s.argv, " ") + ": " + what }
		if g.Exit != pexit || g.Exit != s.wantExit && s.wantExit != 0 {
			t.Errorf("%s go %d python %d (want %d)", at("exit"), g.Exit, pexit, s.wantExit)
		}
		if g.Stdout != so.String() {
			t.Errorf("%s\n go: %q\n py: %q", at("stdout"), g.Stdout, so.String())
		}
		if g.Stderr != se.String() {
			t.Errorf("%s\n go: %q\n py: %q", at("stderr"), g.Stderr, se.String())
		}
		gs, _ := os.ReadFile(e.HerdrStatePath())
		ps, _ := os.ReadFile(pyState)
		if !bytes.Equal(gs, ps) {
			t.Errorf("%s\n go: %s\n py: %s", at("state"), gs, ps)
		}
		gc, _ := os.ReadFile(e.CallsFile)
		pc, _ := os.ReadFile(pyCalls)
		if !bytes.Equal(gc, pc) {
			t.Fatalf("%s\n go: %s\n py: %s", at("calls.jsonl"), gc, pc)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// HerdrHookAfter (Go-only): the first n matching calls pass, the hook runs on the next one, once.
func TestHerdrStubHookAfterSkipsMatchingCalls(t *testing.T) {
	e := herdrEnv(t)
	e.HerdrHookAfter("echo hook-ran", "agent list", 2)
	list := `{"id": "fake", "result": {"agents": []}}` + "\n"
	wantOut(t, e.RunStub("herdr", "", nil, "agent", "list"), 0, list)
	wantOut(t, e.RunStub("herdr", "", nil, "tab", "list"), 0, `{"id": "fake", "result": {"tabs": []}}`+"\n") // not a match: not counted
	wantOut(t, e.RunStub("herdr", "", nil, "agent", "list"), 0, list)
	wantOut(t, e.RunStub("herdr", "", nil, "agent", "list"), 0, "hook-ran\n"+list)
	wantOut(t, e.RunStub("herdr", "", nil, "agent", "list"), 0, list)
	e2 := herdrEnv(t)
	if r := e2.RunStub("herdr", "", map[string]string{"FAKE_HERDR_HOOK": "echo x", "FAKE_HERDR_HOOK_SKIP": "-1"}, "agent", "list"); r.Exit != 1 || !strings.Contains(r.Stderr, "FAKE_HERDR_HOOK_SKIP") {
		t.Fatalf("bad skip count: %+v", r)
	}
}
