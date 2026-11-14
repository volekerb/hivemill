package herdr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The stub is the Go twin of tests/stubs/herdr and tests/stubs/hc (tests/harness.py contract): it
// logs every call's argv and the socket env, and answers from files in the stub dir:
//
//	<tool>.<key>.out   stdout to print      <tool>.<key>.err  stderr to print
//	<tool>.<key>.rc    exit code            <tool>.<key>.sleep seconds to sleep first
//
// key is "<a>_<b>" for herdr (first two args) and "<a>" for hc (first arg).
const stubScript = `#!/bin/sh
tool=$(basename "$0")
echo "$tool $*" >> "$STUB_DIR/calls"
echo "$tool ${HERDR_SOCKET_PATH:-unset}" >> "$STUB_DIR/socks"
if [ "$tool" = hc ]; then key=$1; else key="$1_$2"; fi
f="$STUB_DIR/$tool.$key"
if [ -f "$f.sleep" ]; then sleep "$(cat "$f.sleep")"; fi
if [ -f "$f.err" ]; then cat "$f.err" >&2; fi
if [ -f "$f.out" ]; then cat "$f.out"; fi
if [ -f "$f.rc" ]; then exit "$(cat "$f.rc")"; fi
exit 0
`

type stub struct {
	t   *testing.T
	dir string
}

// newStub installs herdr and hc stubs first on PATH.
func newStub(t *testing.T) *stub {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"herdr", "hc"} {
		if err := os.WriteFile(filepath.Join(bin, n), []byte(stubScript), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_DIR", dir)
	t.Setenv(socketEnv, "/caller/env/must/not/win.sock")
	return &stub{t: t, dir: dir}
}

func (s *stub) set(name, ext, content string) {
	s.t.Helper()
	if err := os.WriteFile(filepath.Join(s.dir, name+"."+ext), []byte(content), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *stub) calls() []string {
	b, err := os.ReadFile(filepath.Join(s.dir, "calls"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (s *stub) socks() []string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "socks"))
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

type memStore struct {
	cfg    map[string]string
	rows   map[string]*Row
	events []string
}

func (m *memStore) Config(k string) (string, bool) { v, ok := m.cfg[k]; return v, ok }
func (m *memStore) AgentRow(n string) *Row         { return m.rows[n] }
func (m *memStore) Event(entity, id, action, detail string) {
	m.events = append(m.events, strings.Join([]string{entity, id, action, detail}, "|"))
}

func newClient(backend string) (*Client, *memStore) {
	st := &memStore{cfg: map[string]string{"terminal_backend": backend, "herdr_socket": "/pinned/herdr.sock"}, rows: map[string]*Row{}}
	return &Client{Store: st}, st
}

func envelope(result string) string { return `{"id":"fake","result":` + result + "}\n" }

// TestAgentsUnreachable: Python herdr_agents() returns None when herdr cannot be reached; that must
// stay distinguishable from an empty list (a reaper treating None as "no agents" would kill panes).
func TestAgentsUnreachable(t *testing.T) {
	// herdr answers, but exits 1 ("server not running", as the stub's "fail": true state).
	s := newStub(t)
	s.set("herdr.agent_list", "err", "herdr: server not running\n")
	s.set("herdr.agent_list", "rc", "1")
	c, _ := newClient("herdr")
	got, err := c.Agents()
	if got != nil || err == nil {
		t.Fatalf("exit 1: want nil list + non-nil error, got %#v, %v", got, err)
	}

	// herdr answers with an unknown shape.
	s.set("herdr.agent_list", "rc", "0")
	s.set("herdr.agent_list", "out", envelope(`{"nope":1}`))
	if got, err = c.Agents(); got != nil || err == nil {
		t.Fatalf("unknown shape: want nil+error, got %#v, %v", got, err)
	}

	// binary missing from PATH altogether.
	t.Setenv("PATH", t.TempDir())
	if got, err = c.Agents(); got != nil || err == nil {
		t.Fatalf("herdr missing: want nil+error, got %#v, %v", got, err)
	}
}

func TestAgentsEmptyAndRows(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	s.set("herdr.agent_list", "out", envelope(`{"agents":[]}`))
	got, err := c.Agents()
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("zero agents: want empty non-nil list and nil error, got %#v, %v", got, err)
	}
	s.set("herdr.agent_list", "out", envelope(`{"agents":[{"name":"a","pane_id":"p1"},{"name":"b","agent_status":"idle"},{"pane_id":"p9"}]}`))
	got, err = c.Agents()
	if err != nil || len(got) != 3 {
		t.Fatalf("rows: %#v %v", got, err)
	}
	if got[0]["agent_status"] != "unknown" || got[1]["agent_status"] != "idle" {
		t.Fatalf("agent_status normalisation: %#v", got)
	}
	byName, byPane := Index(got)
	if len(byName) != 2 || byName["a"].PaneID() != "p1" || len(byPane) != 2 || byPane["p9"].Name() != "" {
		t.Fatalf("Index: %#v %#v", byName, byPane)
	}
}

// TestHcAgentsList drives hc ls end to end (hc ls envelope -> normalised agent rows).
func TestHcAgentsList(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("hc")
	s.set("hc.ls", "out", `{"hidden_finished":0,"terminals":[{"name":"a","agent":"claude","state":"live","status":{"state":"working"}},{"name":"b","agent":"claude","state":"live"}]}`+"\n")
	got, err := c.Agents()
	if err != nil || len(got) != 2 {
		t.Fatalf("%#v %v", got, err)
	}
	if got[0]["agent_status"] != "working" || got[1]["agent_status"] != "unknown" {
		t.Fatalf("statuses: %#v", got)
	}
	if !reflect.DeepEqual(s.calls(), []string{"hc ls"}) {
		t.Fatalf("calls: %v", s.calls())
	}
}

// TestArgvGolden: testdata/argv.golden was produced by the one-off Python command in the PR body
// (bin/factory_cli.py _hc_translate_args, same four calls). The Go client must log the same argv.
func TestArgvGolden(t *testing.T) {
	want, err := os.ReadFile("testdata/argv.golden")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, backend := range []string{"herdr", "hc"} {
		s := newStub(t)
		c, st := newClient(backend)
		s.set(backend+".agent_list", "out", envelope(`{"agents":[]}`))
		s.set("hc.ls", "out", `{"terminals":[{"name":"agent-a"}]}`+"\n")

		step := func(label string, f func()) {
			before := len(s.calls())
			if s.calls() == nil {
				before = 0
			}
			f()
			calls := s.calls()
			if s.calls() == nil {
				calls = nil
			}
			if len(calls) > before {
				got = append(got, backend+" "+label+": "+calls[len(calls)-1])
			} else {
				got = append(got, backend+" "+label+": <no hc equivalent>")
			}
		}
		step("list", func() { _, _ = c.Agents() })
		step("close-pane", func() { _ = c.ClosePaneByID(nil, "p7") })
		if backend == "hc" {
			st.rows["agent-a"] = &Row{Name: "agent-a", PaneID: "agent-a"}
			step("kill-terminal", func() {
				if r := c.KillTerminal("agent-a", nil); r != "killed" {
					t.Fatalf("kill: %s", r)
				}
			})
			// KillTerminal lists first (hc ls); the kill is the last call.
		} else {
			got = append(got, "herdr kill-terminal: <not applicable: hc only>")
			st.rows["agent-a"] = &Row{Name: "agent-a", PaneID: "p1", SpawnArgs: `{"host_backend":"herdr"}`}
			if r := c.KillTerminal("agent-a", nil); r != "unknown" {
				t.Fatalf("herdr row kill must be unknown, got %s", r)
			}
		}
	}
	if strings.Join(got, "\n")+"\n" != string(want) {
		t.Fatalf("argv drift from golden\n--- got\n%s\n--- want\n%s", strings.Join(got, "\n"), want)
	}
}

// TestSocketPin: the TUI gets the configured socket whatever the caller env holds; hc gets the env
// untouched (it uses its own HC_SOCKET).
func TestSocketPin(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	s.set("herdr.agent_list", "out", envelope(`{"agents":[]}`))
	if _, err := c.Agents(); err != nil {
		t.Fatal(err)
	}
	if got := s.socks(); !reflect.DeepEqual(got, []string{"herdr /pinned/herdr.sock"}) {
		t.Fatalf("socks: %v", got)
	}
	// A worker in a non-live home on the live default socket gets the dead socket.
	c2, st := newClient("herdr")
	delete(st.cfg, "herdr_socket")
	c2.IsWorker = func() bool { return true }
	c2.HomeLive = func() bool { return false }
	if c2.Socket() != DeadSocket {
		t.Fatalf("worker socket = %q", c2.Socket())
	}
	// hc keeps the caller's env socket var (no pin).
	c3, _ := newClient("hc")
	s.set("hc.ls", "out", `{"terminals":[]}`+"\n")
	if _, err := c3.Agents(); err != nil {
		t.Fatal(err)
	}
	if got := s.socks(); got[len(got)-1] != "hc /caller/env/must/not/win.sock" {
		t.Fatalf("hc socket: %v", got)
	}
}

// TestTimeout: a stub that outlives the per-call timeout yields Python's herdr_start_error text.
func TestTimeout(t *testing.T) {
	s := newStub(t)
	s.set("herdr.agent_list", "sleep", "5")
	c, _ := newClient("herdr")
	c.Timeout = 300 * time.Millisecond
	start := time.Now()
	_, errText := c.StartError("agent", "list")
	if time.Since(start) > 4*time.Second {
		t.Fatalf("timeout not enforced: %v", time.Since(start))
	}
	// Python: "timed out: " + str(subprocess.TimeoutExpired) for herdr_start_error.
	want := "timed out: Command '['herdr', 'agent', 'list']' timed out after 0.3 seconds"
	if errText != want {
		t.Fatalf("got %q want %q", errText, want)
	}
	// The default is HERDR_TIMEOUT_S = 90, printed as an integer by Python.
	if got := timeoutText([]string{"herdr", "agent", "list"}, DefaultTimeout); got != "Command '['herdr', 'agent', 'list']' timed out after 90 seconds" {
		t.Fatalf("default text: %q", got)
	}
	// herdr(check=True) wraps the same text: "herdr agent list failed: ...".
	_, err := c.Herdr(true, "agent", "list")
	if err == nil || err.Error() != "herdr agent list failed: Command '['herdr', 'agent', 'list']' timed out after 0.3 seconds" {
		t.Fatalf("check err: %v", err)
	}
	// fail-soft: no error, nil result.
	if r, err := c.Herdr(false, "agent", "list"); r != nil || err != nil {
		t.Fatalf("soft: %v %v", r, err)
	}
}

func TestStartErrorStrings(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	s.set("herdr.agent_start", "err", "agent_not_ready\n")
	s.set("herdr.agent_start", "rc", "1")
	if _, e := c.StartError("agent", "start", "x"); e != "agent_not_ready" {
		t.Fatalf("stderr text: %q", e)
	}
	s.set("herdr.agent_start", "err", "")
	if _, e := c.StartError("agent", "start", "x"); e != "exit 1" {
		t.Fatalf("exit text: %q", e)
	}
	s.set("herdr.agent_start", "rc", "0")
	s.set("herdr.agent_start", "out", "not json\n")
	if r, e := c.StartError("agent", "start", "x"); e != "" || !reflect.DeepEqual(r, map[string]any{"raw": "not json"}) {
		t.Fatalf("raw: %#v %q", r, e)
	}
	t.Setenv("PATH", t.TempDir())
	if _, e := c.StartError("agent", "list"); e != "herdr not found: [Errno 2] No such file or directory: 'herdr'" {
		t.Fatalf("not found: %q", e)
	}
	if _, e := (&Client{Store: &memStore{cfg: map[string]string{"terminal_backend": "hc"}}}).StartError("pane", "close", "p1"); e != "hc has no equivalent for herdr pane close p1" {
		t.Fatalf("no equivalent: %q", e)
	}
}

// TestHcTranslate: cases ported from tests/test_hc_status.py (status words through _hc_unwrap_result)
// and tests/test_hc_one_agent.py (verbs the tick sends: list, prompt, read, send-keys, start, and the
// pane/workspace verbs hc has no equivalent for).
func TestHcTranslate(t *testing.T) {
	c := &Client{}
	argv := []struct {
		name string
		in   []string
		want []string // nil = no hc equivalent
	}{
		{"list", []string{"agent", "list"}, []string{"ls"}},
		{"prompt", []string{"agent", "prompt", "a", "hello"}, []string{"send", "a", "hello"}},
		{"read with lines", []string{"agent", "read", "a", "--lines", "40"}, []string{"read", "a", "--lines", "40"}},
		{"read drops other flags", []string{"agent", "read", "a", "--x", "--lines", "5"}, []string{"read", "a", "--lines", "5"}},
		{"send-keys", []string{"agent", "send-keys", "a", "Escape", "Enter"}, []string{"keys", "a", "Escape", "Enter"}},
		{"start default kind", []string{"agent", "start", "a", "--pane", "p1", "--timeout", "90000"}, []string{"run", "--bg", "--name", "a", "claude"}},
		{"start kind and post", []string{"agent", "start", "a", "--kind", "cursor", "--", "--workspace", "/w", "-x"}, []string{"run", "--bg", "--name", "a", "--cwd", "/w", "cursor", "--workspace", "/w", "-x"}},
		{"wait has none", []string{"agent", "wait", "a"}, nil},
		{"pane close has none", []string{"pane", "close", "p1"}, nil},
		{"workspace list has none", []string{"workspace", "list"}, nil},
		{"tab list has none", []string{"tab", "list", "--workspace", "w1"}, nil},
		{"empty", nil, nil},
		{"short prompt", []string{"agent", "prompt", "a"}, nil},
	}
	for _, tc := range argv {
		got, ok := c.HcTranslate(tc.in)
		if tc.want == nil {
			if ok {
				t.Errorf("%s: want no equivalent, got %v", tc.name, got)
			}
			continue
		}
		if !ok || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: got %v ok=%v want %v", tc.name, got, ok, tc.want)
		}
	}
	// spawn-time overrides: RunCwd beats --workspace, RunProgram beats --kind, RunTail shapes the tail.
	c2 := &Client{RunCwd: "/spawn", RunProgram: "cursor-agent", RunTail: func(p string, post []string) []string { return []string{"sh", "/s/" + p} }}
	got, _ := c2.HcTranslate([]string{"agent", "start", "a", "--", "--workspace", "/w"})
	if want := []string{"run", "--bg", "--name", "a", "--cwd", "/spawn", "sh", "/s/cursor-agent"}; !reflect.DeepEqual(got, want) {
		t.Errorf("overrides: %v", got)
	}

	// status words (test_hc_status.py test_osc_object_states, test_string_status_passthrough)
	status := []struct {
		raw  string
		want string
	}{
		{`{"state":"idle"}`, "idle"}, {`{"state":"working"}`, "working"}, {`{"state":"done"}`, "done"},
		{`{"state":"blocked"}`, "blocked"}, {`{"state":"error"}`, "error"}, {`{"state":"clear"}`, "unknown"},
		{`null`, "unknown"}, {`"idle"`, "idle"}, {`"bogus"`, "unknown"},
	}
	for _, tc := range status {
		env := `{"hidden_finished":0,"terminals":[{"name":"n","agent":"claude","state":"live","status":` + tc.raw + `}]}`
		out := hcParseHostStdout([]string{"ls"}, env)
		agents, _ := out["agents"].([]any)
		if len(agents) != 1 || agents[0].(map[string]any)["agent_status"] != tc.want {
			t.Errorf("status %s: got %#v want %s", tc.raw, out, tc.want)
		}
	}
	// an hc ls terminal with no status key at all gets no agent_status (the tick normalises it later)
	out := hcParseHostStdout([]string{"ls"}, `{"terminals":[{"name":"n"}]}`)
	if row := out["agents"].([]any)[0].(map[string]any); row["agent_status"] != nil {
		t.Errorf("no status key: %#v", row)
	}
	// stdout parsing branches
	if got := hcParseHostStdout([]string{"read", "a"}, "plain text"); got["text"] != "plain text" {
		t.Errorf("read: %#v", got)
	}
	if got := hcParseHostStdout([]string{"ls"}, ""); len(got) != 0 {
		t.Errorf("empty: %#v", got)
	}
	if got := hcParseHostStdout([]string{"ls"}, "garbage"); got["raw"] != "garbage" {
		t.Errorf("garbage: %#v", got)
	}
	if got := hcParseHostStdout([]string{"send", "a", "x"}, `{"id":"f","result":{}}`); len(got) != 0 {
		t.Errorf("send: %#v", got)
	}
	if got := hcParseHostStdout([]string{"run"}, `{"foo":1}`); got["raw"] == nil {
		t.Errorf("run unknown shape: %#v", got)
	}
}

// TestClosePane ports close_named_pane: it closes the host's pane id for the exact name, never the
// row's stale id, and records why it skipped.
func TestClosePane(t *testing.T) {
	s := newStub(t)
	c, st := newClient("herdr")
	s.set("herdr.agent_list", "out", envelope(`{"agents":[{"name":"a","pane_id":"hostp1"},{"name":"other","pane_id":"stale"}]}`))
	state, pane := c.ClosePane("a", "stale")
	if state != "closed" || pane != "hostp1" {
		t.Fatalf("closed: %s %s", state, pane)
	}
	if calls := s.calls(); calls[len(calls)-1] != "herdr pane close hostp1" {
		t.Fatalf("calls: %v", calls)
	}
	// not found: names the holder of the row's id in one event, closes nothing.
	n := len(s.calls())
	state, pane = c.ClosePane("ghost", "stale")
	if state != "not-found" || pane != "" || len(s.calls()) != n+1 {
		t.Fatalf("not-found: %s %s %v", state, pane, s.calls())
	}
	var d map[string]string
	ev := st.events[len(st.events)-1]
	if !strings.HasPrefix(ev, "agent|ghost|pane-close-skipped|") {
		t.Fatalf("event: %s", ev)
	}
	_ = json.Unmarshal([]byte(strings.SplitN(ev, "|", 4)[3]), &d)
	if d["reason"] != "pane held by other" {
		t.Fatalf("reason: %v", d)
	}
	// host cannot be asked: unknown, nothing closed, no event.
	s.set("herdr.agent_list", "rc", "1")
	ne := len(st.events)
	n = len(s.calls())
	if state, _ = c.ClosePane("a", "x"); state != "unknown" || len(st.events) != ne || len(s.calls()) != n+1 {
		t.Fatalf("unknown: %s", state)
	}
}

func TestClosePaneHc(t *testing.T) {
	s := newStub(t)
	c, st := newClient("hc")
	s.set("hc.ls", "out", `{"terminals":[{"name":"a","agent":"claude"}]}`+"\n")
	st.rows["a"] = &Row{Name: "a", PaneID: "a", SpawnArgs: `{"host_backend":"hc"}`}
	if state, pane := c.ClosePane("a", "a"); state != "closed" || pane != "a" {
		t.Fatalf("%s %s", state, pane)
	}
	if calls := s.calls(); calls[len(calls)-1] != "hc kill a" {
		t.Fatalf("calls: %v", calls)
	}
	if len(st.events) != 1 || st.events[0] != "agent|a|hc-kill|a" {
		t.Fatalf("events: %v", st.events)
	}
	if state, _ := c.ClosePane("gone", "gone"); state != "not-found" {
		t.Fatalf("not found: %s", state)
	}
	// a herdr-hosted row on an hc config is TUI-hosted: the pane close goes through herdr, not hc.
	st.rows["t"] = &Row{Name: "t", PaneID: "tp", SpawnArgs: `{"host_backend":"herdr"}`}
	s.set("herdr.agent_list", "out", envelope(`{"agents":[{"name":"t","pane_id":"tp"}]}`))
	if state, pane := c.ClosePane("t", "tp"); state != "closed" || pane != "tp" {
		t.Fatalf("tui row: %s %s", state, pane)
	}
	if calls := s.calls(); calls[len(calls)-1] != "herdr pane close tp" {
		t.Fatalf("calls: %v", calls)
	}
}

func TestWorkspacesTabsPanes(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	s.set("herdr.workspace_list", "out", envelope(`{"workspaces":[{"workspace_id":"w1","label":"x"}]}`))
	s.set("herdr.tab_list", "out", envelope(`{"tabs":[{"tab_id":"t1"}]}`))
	s.set("herdr.pane_list", "out", envelope(`{"panes":[{"pane_id":"p1"},{"pane_id":"p2"}]}`))
	if w := c.Workspaces(); len(w) != 1 || w["w1"]["label"] != "x" {
		t.Fatalf("workspaces: %v", w)
	}
	if tabs := c.Tabs("w1"); len(tabs) != 1 {
		t.Fatalf("tabs: %v", tabs)
	}
	if panes := c.Panes("w1"); len(panes) != 2 {
		t.Fatalf("panes: %v", panes)
	}
	want := []string{"herdr workspace list", "herdr tab list --workspace w1", "herdr pane list --workspace w1"}
	if !reflect.DeepEqual(s.calls(), want) {
		t.Fatalf("calls: %v", s.calls())
	}
	// herdr down: empty results, no panic.
	s.set("herdr.workspace_list", "rc", "1")
	if w := c.Workspaces(); len(w) != 0 {
		t.Fatalf("down: %v", w)
	}
}

func TestBackendChoice(t *testing.T) {
	newStub(t)
	c, st := newClient("auto")
	if label, exe := c.TerminalBackendEffective(); label != "auto→herdr" || exe != "herdr" {
		t.Fatalf("auto both: %s %s", label, exe)
	}
	st.cfg["terminal_backend"] = "bogus"
	if c.TerminalBackendConfig() != "auto" {
		t.Fatal("invalid config must fall back to auto")
	}
	st.cfg["terminal_backend"] = " HC "
	if _, exe := c.TerminalBackendEffective(); exe != "hc" {
		t.Fatalf("hc: %s", exe)
	}
	// per-spawn host_backend beats config; pane_id == name marks an hc row.
	st.rows["x"] = &Row{Name: "x", SpawnArgs: `{"host_backend":"herdr"}`}
	st.rows["y"] = &Row{Name: "y", PaneID: "y"}
	if c.agentHostExe(st.rows["x"]) != "herdr" || c.agentHostExe(st.rows["y"]) != "hc" {
		t.Fatal("agentHostExe")
	}
	c.SpawnHostExe = "herdr"
	if c.hostAgentIPCExe([]string{"agent", "start", "y"}) != "herdr" {
		t.Fatal("SpawnHostExe must win")
	}
}

func TestGateRefuses(t *testing.T) {
	s := newStub(t)
	c, _ := newClient("herdr")
	c.Gate = func(args []string) error { return os.ErrPermission }
	if _, err := c.Herdr(false, "pane", "close", "p1"); err == nil {
		t.Fatal("gate must refuse")
	}
	if _, e := c.StartError("pane", "close", "p1"); e == "" {
		t.Fatal("gate must refuse StartError")
	}
	if len(s.calls()) != 0 {
		t.Fatalf("gated call reached the stub: %v", s.calls())
	}
}

// TestSelfReviewFixes covers: Path.resolve()-style non-strict resolution (a symlinked parent with a
// missing leaf), IsTestStub on a plain tests/stubs file, and agent_status values Python keeps.
func TestSelfReviewFixes(t *testing.T) {
	d := t.TempDir()
	real := filepath.Join(d, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	want, _ := filepath.EvalSymlinks(real)
	if got := resolve(filepath.Join(link, "missing", "x.sock")); got != filepath.Join(want, "missing", "x.sock") {
		t.Fatalf("resolve: %s", got)
	}
	// IsTestStub: a herdr under tests/stubs with no real herdr behind it is a stub; elsewhere it is not.
	stubDir := filepath.Join(d, "tests", "stubs")
	if err := os.MkdirAll(stubDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stubDir, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir)
	if !IsTestStub() {
		t.Fatal("tests/stubs herdr must count as a stub")
	}
	t.Setenv("PATH", real)
	if err := os.WriteFile(filepath.Join(real, "herdr"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if IsTestStub() {
		t.Fatal("a herdr outside tests/stubs is not a stub")
	}
	if a := normalizeAgentRow(map[string]any{"agent_status": float64(3)}); a["agent_status"] != float64(3) {
		t.Fatalf("non-string status must be kept: %v", a)
	}
}
