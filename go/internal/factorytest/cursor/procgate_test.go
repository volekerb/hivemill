package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/gate"
)

// Port of tests/test_cursor_proc_gate.py (12 cases; see ../ported_cursor.txt).
// The host-process scan and the gate decision are the Go gate package (the port of total_claude_procs / gate());
// the config comes from the env's DB like the Python gate reads it. adapted:
//   - claude_only_fixture_matches_origin_main compared two Python module versions; here the Python module and the Go
//     scan are compared on the same fixture (the oracle the Go port must match; it needs no git history).
//   - the flagless_* cases and cursor_hooks_ok are Python-only functions (no Go counterpart yet), run through pyEval.

func gateEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(ft.Task{Status: "todo", PRURL: "-"})
	sqlExec(t, e, "UPDATE tasks SET pr_url=NULL")
	e.SeedAgent(ft.AgentRow{Name: director, Role: "director", Goal: "G-001", Team: "g-001-a"})
	writeFile(t, filepath.Join(e.StubBin, "agent"), "#!/bin/sh\necho ok\n", 0o755)
	e.SetHerdr(map[string]any{"agents": []any{}})
	return e
}

func fakeCursor(e *ft.Env, pid int, workspace string, ppid int) {
	e.FakeProc(pid, "cursor-agent", ppid, "cursor-agent", "--workspace", workspace, "--model", "composer-2.5", "--trust", "--force")
}

func cfgGet(e *ft.Env) func(string) (string, error) {
	return func(key string) (string, error) {
		rows := e.SQL("SELECT value FROM config WHERE key=?", key)
		if len(rows) == 0 {
			return "", os.ErrNotExist
		}
		return rows[0][0], nil
	}
}

// decide is gate(con, own_slot=...) over the env's config and the fake /proc.
func decide(t *testing.T, e *ft.Env, own bool) gate.Decision {
	t.Helper()
	cfg, err := gate.LoadConfig(cfgGet(e))
	ok(t, err)
	now, err := time.Parse(time.RFC3339, e.Now)
	if err != nil {
		now = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	snap := gate.Snapshot{Now: now, HerdrReachable: true, MemMB: 4000, Procs: gate.ScanProcs(e.ProcDir), Load1: 0, NumCPU: 8}
	return gate.Decide(snap, cfg, gate.Options{OwnSlot: own})
}

func TestCursorProcGateClaudeOnlyFixtureMatchesPython(t *testing.T) {
	e := gateEnv(t)
	e.NeedPython()
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "")
	e.FakeClaude(100, 1, "claude", "--session-id", "sid-live", "--settings", "/x/settings/coder.json")
	e.FakeClaude(101, 1, "claude", "--session-id", "sid-flagless") // no --settings
	e.SeedAgent(ft.AgentRow{Name: "zz-live", Role: "coder", Goal: "G-001", PaneID: "p-live"})
	e.SeedAgent(ft.AgentRow{Name: "zz-flag", Role: "coder", Goal: "G-001", PaneID: "p-flag"})
	sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", "sid-live", "zz-live")
	sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", "sid-flagless", "zz-flag")
	e.SetHerdr(map[string]any{"agents": []any{
		map[string]any{"name": "zz-live", "agent": "claude", "pane_id": "p-live"},
		map[string]any{"name": "zz-flag", "agent": "claude", "pane_id": "p-flag"}}})
	got := gate.ScanProcs(e.ProcDir)
	py := pyEval(t, e, `t = mod.total_claude_procs(); res['total'] = t['total']; res['pids'] = t['pids']
con = mod.db()
rows = con.execute("SELECT * FROM agents WHERE status='live' AND name LIKE 'zz-%' AND role='coder'").fetchall()
ok, why, stats = mod.gate(con)
res['gate_ok'] = ok; res['gate_why'] = why; res['other'] = stats['other']; res['stats_total'] = stats['total']
res['flagless'] = len(mod.flagless_scan(con, rows))
con.close()`)
	eq(t, "python total", int(py["total"].(float64)), got.Total)
	// the gate decision over the same state: Go gate vs the Python gate()
	d := decide(t, e, false)
	eq(t, "gate ok", d.OK, py["gate_ok"].(bool))
	eq(t, "gate reason", d.Reason, py["gate_why"].(string))
	eq(t, "stats total", *d.Stats.Total, int(py["stats_total"].(float64)))
	eq(t, "flagless agents (the one without --settings)", py["flagless"], any(1.0))
	var pids []int
	for _, p := range py["pids"].([]any) {
		pids = append(pids, int(p.(float64)))
	}
	eq(t, "pids", strings.Join(strs2(pids), ","), strings.Join(strs2(got.PIDs), ","))
	eq(t, "total", got.Total, 2)
}

func strs2(n []int) []string {
	out := make([]string, len(n))
	for i, v := range n {
		out[i] = strconv.Itoa(v)
	}
	return out
}

func TestCursorProcGateCapCountsCursorAtMaxTotalClaude(t *testing.T) {
	e := gateEnv(t)
	e.NeedPython() // spawn still runs in Python
	n := 3
	e.OKWith(ft.Opts{Agent: director}, "config", "max_total_claude", strconv.Itoa(n))
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "*=cursor")
	e.OKWith(ft.Opts{Agent: director}, "config", "max_live_agents", "20")
	for i := 0; i < n; i++ {
		ws := mkdir(t, filepath.Join(e.Home, "ws-"+strconv.Itoa(i)))
		fakeCursor(e, 200+i, ws, 1)
		e.FakeProc(300+i, "node", 200+i, "node", "/usr/bin/cursor-agent")
	}
	eq(t, "host total", gate.ScanProcs(e.ProcDir).Total, n)
	r := e.OKWith(ft.Opts{Agent: director}, "spawn", "coder", "--name", "zz-cap-coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001")
	contains(t, "stdout", r.Stdout, "QUEUED")
	contains(t, "stdout", r.Stdout, "claude processes")
}

func TestCursorProcGateRelaunchOwnSlotAndSecondSpawnRefusedAtCap(t *testing.T) {
	e := gateEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	n := 2
	e.OKWith(ft.Opts{Agent: director}, "config", "max_total_claude", strconv.Itoa(n))
	for i := 0; i < n; i++ {
		fakeCursor(e, 400+i, filepath.Join(e.Home, "cap-ws-"+strconv.Itoa(i)), 1)
	}
	own := decide(t, e, true)
	if !own.OK {
		t.Fatalf("own slot must pass: %s", own.Reason)
	}
	if own.Stats.Total == nil || *own.Stats.Total != n {
		t.Fatalf("stats total = %v, want %d", own.Stats.Total, n)
	}
	second := decide(t, e, false)
	if second.OK {
		t.Fatal("a second spawn at the cap must be refused")
	}
	contains(t, "reason", second.Reason, "claude processes")
}

func TestCursorProcGateFreshCursorSpawnRefusedOnLoad(t *testing.T) {
	e := gateEnv(t)
	e.NeedPython()
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "*=cursor")
	// far above gate_max_load_ratio x CPUs on any host
	r := e.OKWith(ft.Opts{Agent: director, Env: map[string]string{"FACTORY_LOADAVG": "100000"}},
		"spawn", "coder", "--name", "zz-load-coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001")
	contains(t, "stdout", r.Stdout, "QUEUED")
	contains(t, "stdout", r.Stdout, "load1")
}

func TestCursorProcGateForeignCursorAndClaudePanesInOther(t *testing.T) {
	e := gateEnv(t)
	cfg, err := gate.LoadConfig(cfgGet(e))
	ok(t, err)
	snap := gate.Snapshot{Now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), HerdrReachable: true, MemMB: 4000, NumCPU: 8,
		HerdrAgents: []gate.HerdrAgent{{Name: "zz-foreign-c", PaneID: "pc", Agent: "cursor"}, {Name: "zz-foreign-cl", PaneID: "pl", Agent: "claude"}}}
	eq(t, "other", gate.Decide(snap, cfg, gate.Options{}).Stats.Other, 2)
}

// seedCursorAgent inserts a live cursor agent whose workspace is ws.
func seedCursorAgent(t *testing.T, e *ft.Env, name, pane, ws string) {
	t.Helper()
	b, err := json.Marshal(map[string]any{"runtime": "cursor", "cursor_workspace": ws})
	ok(t, err)
	sqlExec(t, e, "INSERT INTO agents(name,role,goal_id,status,spawn_args,pane_id,flagless) VALUES(?,?,?,?,?,?,0)",
		name, "coder", "G-001", "live", string(b), pane)
}

const flaglessSnippet = `con = mod.db()
row = con.execute("SELECT * FROM agents WHERE name=?", (sys.argv[3],)).fetchone()
st, pid = mod.flagless_state(row)
res['state'] = st
res['pid'] = pid
res['hooks_ok'] = mod.cursor_hooks_ok(sys.argv[4])
res['found'] = len(mod.flagless_scan(con, [row])) if sys.argv[5] == 'scan' else None
after = con.execute("SELECT status, pane_id FROM agents WHERE name=?", (sys.argv[3],)).fetchone()
res['after'] = [after['status'], after['pane_id']]
res['events'] = [r['action'] for r in con.execute("SELECT action FROM events WHERE entity_id=? ORDER BY id", (sys.argv[3],))]
con.close()
`

func TestCursorProcGateFlaglessCursorOk(t *testing.T) {
	e := gateEnv(t)
	name := "zz-cur-ok"
	ws := filepath.Join(stateCursorDir(e), name)
	materialize(t, ws, "coder", ch.MaterializeOpts{})
	fakeCursor(e, 500, ws, 1)
	seedCursorAgent(t, e, name, "p-ok", ws)
	got := pyEvalArg(t, e, flaglessSnippet, name, ws, "noscan")
	eq(t, "state", got["state"], any("ok"))
	eq(t, "pid", got["pid"], any(500.0))
}

func TestCursorProcGateFlaglessCursorFlaggedScanNoKill(t *testing.T) {
	e := gateEnv(t)
	name := "zz-cur-bad"
	ws := filepath.Join(stateCursorDir(e), name)
	materialize(t, ws, "coder", ch.MaterializeOpts{})
	hooks := filepath.Join(ws, ".cursor", "hooks.json")
	data := readJSON(t, hooks)
	for _, entries := range sub(t, data, "hooks") {
		if l := entries.([]any); len(l) > 0 {
			l[0].(map[string]any)["failClosed"] = false
		}
	}
	b, err := json.MarshalIndent(data, "", "  ")
	ok(t, err)
	writeFile(t, hooks, string(b)+"\n", 0o644)
	fakeCursor(e, 501, ws, 1)
	seedCursorAgent(t, e, name, "p-bad", ws)
	got := pyEvalArg(t, e, flaglessSnippet, name, ws, "scan")
	eq(t, "state", got["state"], any("flagless"))
	eq(t, "pid", got["pid"], any(501.0))
	eq(t, "found", got["found"], any(1.0))
	eq(t, "status", got["after"].([]any)[0], any("live"))
	eq(t, "last event", got["events"].([]any)[len(got["events"].([]any))-1], any("flagless"))
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) >= 2 && c.Argv[0] == "pane" && c.Argv[1] == "close" {
			t.Fatalf("a flagless scan must not close the pane: %v", c.Argv)
		}
	}
}

func TestCursorProcGateGitNestedWorkspaceFlagsButNeverStopsOrRelaunches(t *testing.T) {
	// T-1252: a pane still on a workspace under a git tree turns flagless; the pane is left alone.
	e := gateEnv(t)
	name := "zz-cur-nested"
	repo := filepath.Join(e.UserHome, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	ws := filepath.Join(repo, "run", "cursor", name)
	ok(t, ch.WriteAtomic(filepath.Join(ws, ".cursor", "hooks.json"), scoutHooks(t), 0o644))
	fakeCursor(e, 502, ws, 1)
	seedCursorAgent(t, e, name, "p-nested", ws)
	before := len(e.Calls("herdr"))
	got := pyEvalArg(t, e, flaglessSnippet, name, ws, "scan")
	eq(t, "hooks ok", got["hooks_ok"], any(false))
	eq(t, "state", got["state"], any("flagless"))
	eq(t, "found", got["found"], any(1.0))
	after := got["after"].([]any)
	eq(t, "status", after[0], any("live"))
	eq(t, "pane", after[1], any("p-nested"))
	evs := got["events"].([]any)
	eq(t, "events", len(evs), 1)
	eq(t, "event", evs[0], any("flagless"))
	for _, c := range e.Calls("herdr")[before:] {
		if len(c.Argv) > 0 && (c.Argv[0] == "pane" || c.Argv[0] == "agent" || c.Argv[0] == "tab" || c.Argv[0] == "workspace") {
			t.Fatalf("the scan touched herdr: %v", c.Argv)
		}
	}
}

func TestCursorProcGateUnreadableProcSkipsCap(t *testing.T) {
	e := gateEnv(t)
	ok(t, os.Chmod(e.ProcDir, 0))
	t.Cleanup(func() { _ = os.Chmod(e.ProcDir, 0o755) }) // restore so t.TempDir cleanup can remove it
	if _, err := os.ReadDir(e.ProcDir); err == nil {
		t.Skip("the process can read a mode-0 dir (running as root): the unreadable /proc case cannot be built")
	}
	scan := gate.ScanProcs(e.ProcDir)
	if scan.Known {
		t.Fatal("an unreadable proc dir must give an unknown scan")
	}
	d := decide(t, e, false)
	if !d.OK {
		t.Fatalf("an unknown scan must not refuse: %s", d.Reason)
	}
	if d.Stats.Total != nil {
		t.Fatalf("stats total = %d, want unknown", *d.Stats.Total)
	}
}

func TestCursorProcGateNodeChildNotDoubleCounted(t *testing.T) {
	e := gateEnv(t)
	fakeCursor(e, 600, filepath.Join(e.Home, "parent-ws"), 1)
	e.FakeProc(601, "node", 600, "node", "/usr/bin/cursor-agent")
	got := gate.ScanProcs(e.ProcDir)
	eq(t, "total", got.Total, 1)
	eq(t, "pids", strings.Join(strs2(got.PIDs), ","), "600")
}

func TestCursorProcGateTwoAgentsSameWorkspaceBothInTotal(t *testing.T) {
	e := gateEnv(t)
	ws := mkdir(t, filepath.Join(stateCursorDir(e), "zz-a1"))
	fakeCursor(e, 700, ws, 1)
	fakeCursor(e, 701, ws, 1)
	eq(t, "total", gate.ScanProcs(e.ProcDir).Total, 2)
}

// C4: dropping cursor from the host scan would miss cap enforcement.
func TestCursorProcGateCombinedTotalRequiresCursorProcs(t *testing.T) {
	e := gateEnv(t)
	fakeCursor(e, 800, mkdir(t, filepath.Join(e.Home, "c4-ws")), 1)
	scan := gate.ScanProcs(e.ProcDir)
	eq(t, "total", scan.Total, 1)
	claudeOnly := 0
	for _, argv := range scan.Argv {
		for i, x := range argv {
			if i < 3 && filepath.Base(x) == "claude" {
				claudeOnly++
				break
			}
		}
	}
	eq(t, "claude-only procs", claudeOnly, 0)
}

func scoutHooks(t *testing.T) string {
	t.Helper()
	return string(mustJSON(t, hooksOf(t, "coder", false)))
}
