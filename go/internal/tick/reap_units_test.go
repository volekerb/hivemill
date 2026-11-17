package tick

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/store"
)

// Tests for units 22 (exited_panes) and 23 (idle_coders) with a PATH herdr stub, a git worktree and a fake proc tree.

const stubSh = `#!/bin/sh
echo "$*" >> "$STUB_DIR/calls"
key="$1_$2"
if [ "$key" = pane_close ]; then touch "$STUB_DIR/closed"; fi
f="$STUB_DIR/$key"
if [ -f "$STUB_DIR/closed" ] && [ -f "$f.after" ]; then cat "$f.after"; exit 0; fi
if [ -f "$f.out" ]; then cat "$f.out"; fi
if [ -f "$f.rc" ]; then exit "$(cat "$f.rc")"; fi
exit 0
`

type herdrStub struct {
	t   *testing.T
	dir string
}

func newHerdrStub(t *testing.T) *herdrStub {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(stubSh), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_DIR", dir)
	return &herdrStub{t: t, dir: dir}
}

func (s *herdrStub) set(key, ext, result string) {
	s.t.Helper()
	body := result
	if ext != "rc" {
		body = `{"id":"fake","result":` + result + "}\n"
	}
	if err := os.WriteFile(filepath.Join(s.dir, key+"."+ext), []byte(body), 0o644); err != nil {
		s.t.Fatal(err)
	}
}

func (s *herdrStub) calls() []string {
	b, _ := os.ReadFile(filepath.Join(s.dir, "calls"))
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func (s *herdrStub) count(prefix string) int {
	n := 0
	for _, c := range s.calls() {
		if strings.HasPrefix(c, prefix) {
			n++
		}
	}
	return n
}

// unitFix is reapFix plus the herdr stub, config for the herdr backend and a fixed unit clock.
type unitFix struct {
	*reapFix
	stub *herdrStub
	out  bytes.Buffer
	now  float64
}

func newUnitFix(t *testing.T) *unitFix {
	t.Helper()
	u := &unitFix{reapFix: newReapFix(t), stub: newHerdrStub(t)}
	u.clock = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	u.now = float64(u.clock.Unix())
	u.exec("INSERT INTO config(key,value) VALUES('terminal_backend','herdr'),('herdr_socket','/pinned/herdr.sock')")
	prev := pruneNow
	pruneNow = func() (float64, error) { return u.now, nil }
	t.Cleanup(func() { pruneNow = prev })
	return u
}

func agentsState(t *testing.T, agents ...map[string]any) State {
	t.Helper()
	b, err := json.Marshal(agents)
	if err != nil {
		t.Fatal(err)
	}
	return State{"agents": b}
}

func (u *unitFix) run(name string, st State) error {
	u.t.Helper()
	return goUnits[name](&UnitCtx{DB: u.db, State: st, Stdout: func(s string) { u.out.WriteString(s) }})
}

func (u *unitFix) throttle() map[string]string {
	rows, err := u.db.Query("SELECT key, ts FROM throttle ORDER BY key")
	if err != nil {
		u.t.Fatal(err)
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			u.t.Fatal(err)
		}
		m[k] = v
	}
	return m
}

func (u *unitFix) col(q string, args ...any) string {
	u.t.Helper()
	var v any
	if err := u.db.QueryRow(q, args...).Scan(&v); err != nil {
		u.t.Fatalf("%s: %v", q, err)
	}
	return fmt.Sprint(v)
}

// TestUnitTable: units 22 and 23 are Go units in the table, and every registered Go unit is a unit of the walk.
func TestUnitTable(t *testing.T) {
	for _, name := range []string{"exited_panes", "idle_coders"} {
		if _, ok := goUnits[name]; !ok {
			t.Errorf("unit %s is not Go-capable", name)
		}
		if unitTableIndex(name) < 0 {
			t.Errorf("unit %s is not in the table", name)
		}
	}
	if unitTableIndex("exited_panes") != 22 || unitTableIndex("idle_coders") != 23 {
		t.Errorf("indices: %d %d, want 22 and 23", unitTableIndex("exited_panes"), unitTableIndex("idle_coders"))
	}
	walk := map[string]bool{}
	for _, u := range walkUnits() {
		walk[u.Name] = true
	}
	for _, n := range sortedKeys(goUnits) {
		if !walk[n] {
			t.Errorf("Go unit %q is not a walk unit", n)
		}
	}
}

// ---- unit 22 ----

const (
	oldEnded   = "2026-01-01T00:00:00+00:00"
	freshEnded = "2026-11-17T11:55:00+00:00" // 5 min before the fixture clock
)

func (u *unitFix) exited(name, role, status, pane, ended string) {
	u.t.Helper()
	u.exec("INSERT OR REPLACE INTO agents(name,role,status,task_id,pane_id,ended,session_id,cwd,spawn_args) VALUES(?,?,?,?,?,?,?,?,'{}')",
		name, role, status, "T-5", pane, ended, "s-"+name, "/w/"+name)
}

func hostAgent(name, pane, status, session string) map[string]any {
	h := map[string]any{"name": name, "agent": "claude", "pane_id": pane, "agent_status": status}
	if session != "" {
		h["agent_session"] = map[string]any{"value": session}
	}
	return h
}

func (u *unitFix) hostPanes(panes ...string) {
	var l []string
	for _, p := range panes {
		l = append(l, fmt.Sprintf(`{"pane_id":%q,"cwd":"/w/qa-1"}`, p))
	}
	u.stub.set("pane_list", "out", `{"panes":[`+strings.Join(l, ",")+`]}`)
}

func (u *unitFix) exitedMode(m string) {
	u.exec("INSERT OR REPLACE INTO config(key,value) VALUES('exited_pane_reap_mode',?)", m)
}

func wantClosed(name, pane, branch string) string {
	return fmt.Sprintf(`%s|{"agent": %q, "pane": %q, "role": "qa", "ended": %q, "branch": %q}`, name, name, pane, oldEnded, branch)
}

// TestReapExitedPanesOn: an exited worker row past 10 min whose pane the host still lists under its exact name and
// session is closed, the close confirmed and one exited-pane-closed event written (Python's detail, key order and all).
func TestReapExitedPanesOn(t *testing.T) {
	u := newUnitFix(t)
	u.exitedMode("on")
	u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
	u.hostPanes("p-1")
	u.stub.set("pane_list", "after", `{"panes":[]}`)
	if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))); err != nil {
		t.Fatal(err)
	}
	if n := u.stub.count("pane close p-1"); n != 1 {
		t.Fatalf("pane close calls = %d, want 1 (%v)", n, u.stub.calls())
	}
	if got := u.events("exited-pane-closed"); len(got) != 1 || got[0] != wantClosed("qa-1", "p-1", "session") {
		t.Fatalf("events = %q", got)
	}
	if u.col("SELECT status FROM agents WHERE name='qa-1'") != "exited" {
		t.Fatal("the row changed")
	}
	if _, ok := u.throttle()["exitedpanereap"]; !ok {
		t.Fatalf("throttle = %v", u.throttle())
	}
}

// TestReapExitedPanesLiveAgent: an agent whose pane is live in the host (a live row, or any non-exited row) is never
// closed and never marked exited; HQ rows, rows under 10 min and rows with another session are left too.
func TestReapExitedPanesLiveAgent(t *testing.T) {
	u := newUnitFix(t)
	u.exitedMode("on")
	u.exited("qa-live", "qa", "live", "p-1", "")
	u.exited("lead-1", "lead", "exited", "p-2", oldEnded)
	u.exited("qa-fresh", "qa", "exited", "p-3", freshEnded)
	u.exited("qa-other", "qa", "exited", "p-4", oldEnded)
	u.exited("qa-null", "qa", "exited", "p-5", oldEnded)
	u.exec("UPDATE agents SET ended=NULL WHERE name='qa-null'")
	u.hostPanes("p-1", "p-2", "p-3", "p-4", "p-5")
	u.stub.set("pane_list", "after", `{"panes":[]}`)
	st := agentsState(t,
		hostAgent("qa-live", "p-1", "working", "s-qa-live"),
		hostAgent("lead-1", "p-2", "idle", "s-lead-1"),
		hostAgent("qa-fresh", "p-3", "idle", "s-qa-fresh"),
		hostAgent("qa-other", "p-4", "idle", "s-newer-agent"), // a newer agent under the same name
		hostAgent("qa-null", "p-5", "idle", "s-qa-null"),
		hostAgent("ghost", "p-9", "idle", "s-ghost"), // no agents row
		hostAgent("", "p-8", "idle", ""),             // no name
	)
	if err := u.run("exited_panes", st); err != nil {
		t.Fatal(err)
	}
	if n := u.stub.count("pane close"); n != 0 {
		t.Fatalf("closed %d panes: %v", n, u.stub.calls())
	}
	if n := u.allEvents(); n != 0 {
		t.Fatalf("%d events, want none", n)
	}
	if got := u.col("SELECT group_concat(name||'='||status) FROM (SELECT * FROM agents ORDER BY name)"); !strings.Contains(got, "qa-live=live") {
		t.Fatalf("rows = %s", got)
	}
}

// TestReapExitedPanesDryRun: unset and unknown modes log one would-close per (agent, pane) and close nothing.
func TestReapExitedPanesDryRun(t *testing.T) {
	for _, mode := range []string{"", "bogus", "dry-run"} {
		t.Run("mode="+mode, func(t *testing.T) {
			u := newUnitFix(t)
			if mode != "" {
				u.exitedMode(mode)
			}
			u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
			u.hostPanes("p-1")
			st := agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))
			for i := 0; i < 2; i++ {
				u.exec("DELETE FROM throttle")
				if err := u.run("exited_panes", st); err != nil {
					t.Fatal(err)
				}
			}
			if got := u.events("exited-pane-would-close"); len(got) != 1 || got[0] != wantClosed("qa-1", "p-1", "session") {
				t.Fatalf("events = %q", got)
			}
			if u.stub.count("pane close") != 0 {
				t.Fatal("dry-run closed a pane")
			}
		})
	}
}

// TestReapExitedPanesOffAndThrottle: off does nothing and keeps no throttle row; a second run within 10 min does
// nothing; an empty or null host list does nothing.
func TestReapExitedPanesOffAndThrottle(t *testing.T) {
	u := newUnitFix(t)
	u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
	u.hostPanes("p-1")
	st := agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))
	u.exitedMode("off")
	if err := u.run("exited_panes", st); err != nil {
		t.Fatal(err)
	}
	if u.allEvents() != 0 || len(u.throttle()) != 0 {
		t.Fatalf("off wrote events or a throttle: %d %v", u.allEvents(), u.throttle())
	}
	u.exitedMode("dry-run")
	for _, s := range []State{agentsState(t), {"agents": json.RawMessage("null")}} {
		if err := u.run("exited_panes", s); err != nil {
			t.Fatal(err)
		}
	}
	if u.allEvents() != 0 || len(u.throttle()) != 0 {
		t.Fatal("an empty or null host list did work")
	}
	if err := u.run("exited_panes", st); err != nil {
		t.Fatal(err)
	}
	u.exec("DELETE FROM events")
	u.clock = u.clock.Add(599 * time.Second)
	if err := u.run("exited_panes", st); err != nil {
		t.Fatal(err)
	}
	if u.allEvents() != 0 {
		t.Fatal("ran again inside the throttle")
	}
	u.clock = u.clock.Add(2 * time.Second) // 601 s after the first run
	if err := u.run("exited_panes", st); err != nil {
		t.Fatal(err)
	}
	if got := u.events("exited-pane-would-close"); len(got) != 1 {
		t.Fatalf("after the throttle: %q", got)
	}
}

// TestReapExitedPanesCwdBranch: with no session on the host pane, only an idle/done pane in the row's own cwd qualifies,
// and a live peer with the same cwd keeps it.
func TestReapExitedPanesCwdBranch(t *testing.T) {
	cases := []struct {
		name   string
		status string
		cwd    string // the pane's cwd on the host
		peer   bool
		want   int
	}{
		{"idle in its cwd", "idle", "/w/qa-1", false, 1},
		{"deleted suffix ignored", "done", "/w/qa-1 (deleted)", false, 1},
		{"normalized path", "idle", "/w//qa-1/", false, 1},
		{"working never", "working", "/w/qa-1", false, 0},
		{"other cwd", "idle", "/w/other", false, 0},
		{"live peer in the same cwd", "idle", "/w/qa-1", true, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := newUnitFix(t)
			u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
			if c.peer {
				u.exited("qa-2", "qa", "live", "p-2", "")
				u.exec("UPDATE agents SET cwd='/w/qa-1' WHERE name='qa-2'")
			}
			u.stub.set("pane_list", "out", fmt.Sprintf(`{"panes":[{"pane_id":"p-1","cwd":%q}]}`, c.cwd))
			if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", c.status, ""))); err != nil {
				t.Fatal(err)
			}
			got := u.events("exited-pane-would-close")
			if len(got) != c.want {
				t.Fatalf("events = %q, want %d", got, c.want)
			}
			if c.want == 1 && !strings.Contains(got[0], `"branch": "cwd"`) {
				t.Fatalf("detail = %s", got[0])
			}
		})
	}
}

// TestReapExitedPanesPaneRules: the pane id handed to a newer live agent, a pane the host no longer lists (stale tick
// list) and an unreachable host are all left alone; a close the host ignores is logged once per (agent, pane).
func TestReapExitedPanesPaneRules(t *testing.T) {
	t.Run("pane held by a newer agent", func(t *testing.T) {
		u := newUnitFix(t)
		u.exitedMode("on")
		u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
		u.exited("qa-new", "qa", "live", "p-1", "")
		u.hostPanes("p-1")
		if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))); err != nil {
			t.Fatal(err)
		}
		if u.stub.count("pane close") != 0 || u.allEvents() != 0 {
			t.Fatal("touched a pane that a newer agent holds")
		}
	})
	t.Run("stale list", func(t *testing.T) {
		u := newUnitFix(t)
		u.exitedMode("on")
		u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
		u.hostPanes("p-2") // p-1 is already gone
		if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))); err != nil {
			t.Fatal(err)
		}
		if u.stub.count("pane close") != 0 || u.allEvents() != 0 {
			t.Fatal("closed a pane the host does not list")
		}
	})
	t.Run("host unreachable", func(t *testing.T) {
		u := newUnitFix(t)
		u.exitedMode("on")
		u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
		u.stub.set("pane_list", "rc", "1")
		u.stub.set("workspace_list", "rc", "1")
		if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))); err != nil {
			t.Fatal(err)
		}
		if u.stub.count("pane close") != 0 || u.allEvents() != 0 {
			t.Fatal("acted on an unreachable host")
		}
	})
	t.Run("close ignored", func(t *testing.T) {
		u := newUnitFix(t)
		u.exitedMode("on")
		u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
		u.hostPanes("p-1") // no .after: the pane stays listed
		st := agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))
		for i := 0; i < 2; i++ {
			u.exec("DELETE FROM throttle")
			if err := u.run("exited_panes", st); err != nil {
				t.Fatal(err)
			}
		}
		if got := u.events("exited-pane-close-failed"); len(got) != 1 || len(u.events("exited-pane-closed")) != 0 {
			t.Fatalf("failed events = %q", got)
		}
		if u.stub.count("pane close") != 2 {
			t.Fatalf("close attempts = %d, want 2 (retried silently)", u.stub.count("pane close"))
		}
	})
	t.Run("workspace fallback", func(t *testing.T) {
		u := newUnitFix(t)
		u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
		u.stub.set("pane_list", "out", `{}`) // no "panes" key on the global list
		u.stub.set("workspace_list", "out", `{"workspaces":[{"workspace_id":"w1"}]}`)
		if err := u.run("exited_panes", agentsState(t, hostAgent("qa-1", "p-1", "idle", "s-qa-1"))); err != nil {
			t.Fatal(err)
		}
		// the per-workspace list is the same stub answer ({}), so the host cannot be asked: nothing happens
		if u.allEvents() != 0 {
			t.Fatalf("events = %d", u.allEvents())
		}
	})
}

// TestReapExitedPanesFailed: an error inside ends in one exited-pane-reap-failed event and the unit returns nil; a
// missing agents key is a tick error, as Python's KeyError.
func TestReapExitedPanesFailed(t *testing.T) {
	u := newUnitFix(t)
	u.exitedMode("on")
	u.exited("qa-1", "qa", "exited", "p-1", oldEnded)
	bad := hostAgent("qa-1", "p-1", "idle", "")
	bad["agent_session"] = "oops" // not an object
	if err := u.run("exited_panes", agentsState(t, bad)); err != nil {
		t.Fatalf("unit error: %v", err)
	}
	got := u.events("exited-pane-reap-failed")
	if len(got) != 1 || !strings.HasPrefix(got[0], "exited-pane-reap|") || !strings.Contains(got[0], "'str' object has no attribute 'get'") {
		t.Fatalf("events = %q", got)
	}
	if err := u.run("exited_panes", State{}); err == nil || !strings.Contains(err.Error(), "agents") {
		t.Fatalf("missing agents key: err = %v", err)
	}
}

// ---- unit 23 ----

type idleFix struct {
	*unitFix
	head string
	pr   map[string]any
}

// newIdleFix is a live coder coder-1 on in_review task T-5 with a PR whose head is the worktree's HEAD, a clean git
// worktree, no mail, a pane that only runs claude, and a claude process without shells in the fake proc tree.
func newIdleFix(t *testing.T) *idleFix {
	t.Helper()
	u := newUnitFix(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", u.wt, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("commit", "-q", "--allow-empty", "-m", "c1")
	f := &idleFix{unitFix: u, head: git("rev-parse", "HEAD")}
	u.exec("UPDATE tasks SET status='in_review', pr_url='https://github.com/o/r/pull/1', worktree=? WHERE id='T-5'", u.wt)
	u.exec("INSERT INTO agents(name,role,status,goal_id,task_id,pane_id) VALUES('coder-1','coder','live','G-1','T-5','p-1')")
	u.exec("INSERT INTO config(key,value) VALUES('idle_coder_reap','on')")
	f.pr = map[string]any{"state": "OPEN", "headRefOid": f.head}
	prev := reapPRState
	reapPRState = func(string) (map[string]any, error) { return f.pr, nil }
	t.Cleanup(func() { reapPRState = prev })
	u.stub.set("pane_process-info", "out", `{"process_info":{"foreground_processes":[{"name":"claude"}]}}`)
	f.claude(0)
	return f
}

// claude puts a claude process of coder-1 in the fake proc tree; shellChild != 0 adds a bash child with that pid.
func (f *idleFix) claude(shellChild int) {
	f.addProc(fakeProc{pid: 300, ppid: 1, pgid: 300, start: 20, argv: []string{"claude"}, env: map[string]string{"FACTORY_AGENT": "coder-1"}})
	if shellChild != 0 {
		f.addProc(fakeProc{pid: shellChild, ppid: 300, pgid: shellChild, start: 21, argv: []string{"-bash"}})
	}
}

// idleTick runs the unit once with coder-1 reported idle by the host.
func (f *idleFix) idleTick() error {
	return f.run("idle_coders", agentsState(f.t, hostAgent("coder-1", "p-1", "idle", "")))
}

// stopWindow makes coder-1 idle for 1000 s (> the 900 s default limit): the first tick seeds the clock, the second is due.
func (f *idleFix) stopWindow() {
	f.t.Helper()
	if err := f.idleTick(); err != nil {
		f.t.Fatal(err)
	}
	f.now += 1000
}

func TestReapIdleCodersStops(t *testing.T) {
	f := newIdleFix(t)
	f.stopWindow()
	if f.allEvents() != 0 {
		t.Fatal("the first tick only starts the clock")
	}
	if _, ok := f.throttle()["idlereap:coder-1"]; !ok {
		t.Fatalf("throttle = %v", f.throttle())
	}
	if err := f.idleTick(); err != nil {
		t.Fatal(err)
	}
	reason := "idle_coder_reap: idle 1000s (> 900s), working tree clean, PR head pushed, no mail: " +
		"nothing left for you to do; the tick brings the task back when it needs a coder"
	if got := f.col("SELECT stop_requested_at FROM agents WHERE name='coder-1'"); got == "<nil>" || got == "" {
		t.Fatalf("stop_requested_at = %s", got)
	}
	if got := f.col("SELECT subject||'|'||kind||'|'||sender||'|'||recipient||'|'||goal_id||'|'||task_id FROM messages"); got != "STOP|decision|tick|coder-1|G-1|T-5" {
		t.Fatalf("message = %s", got)
	}
	if got := f.col("SELECT body FROM messages"); got != reason+"\nStop work now. Push any work in progress to your branch (a WIP commit is fine), then run factory done '<stopped: pushed sha|nothing; state>'" {
		t.Fatalf("body = %q", got)
	}
	wantEvents := map[string]string{
		"stop-requested":     `coder-1|{"by": "tick", "reason": ` + jsonStr(t, reason) + `}`,
		"idle-coder-stopped": `coder-1|{"agent": "coder-1", "task": "T-5", "idle_s": 1000, "threshold_s": 900}`,
		"note":               "T-5|tick stopped idle coder coder-1: " + reason,
		"nudge-pending":      `coder-1|{"nudge": "\ud83d\udcec STOP from tick: run factory inbox now and follow the STOP order.", "what": "STOP order", "pane_id": "p-1", "started": null}`,
	}
	for action, want := range wantEvents {
		if got := f.events(action); len(got) != 1 || got[0] != want {
			t.Errorf("%s events = %q, want %q", action, got, want)
		}
	}
	if f.stub.count("agent send-keys coder-1 esc") != 1 || f.out.String() != "sent Esc to coder-1\n" {
		t.Errorf("esc calls %v, stdout %q", f.stub.calls(), f.out.String())
	}
	// the stop is not repeated: the row is already stopping
	f.out.Reset()
	f.now += 1000
	if err := f.idleTick(); err != nil {
		t.Fatal(err)
	}
	if n := len(f.events("idle-coder-stopped")); n != 1 {
		t.Fatalf("stopped %d times", n)
	}
}

func jsonStr(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	// Python json.dumps writes ASCII only; the reasons here are ASCII, so only quoting and the ">" escape differ
	return strings.ReplaceAll(strings.ReplaceAll(string(b), `\u003e`, ">"), `\u003c`, "<")
}

// TestReapIdleCodersBlockers: each blocker keeps the coder running (nothing is written but the idle clock).
func TestReapIdleCodersBlockers(t *testing.T) {
	cases := []struct {
		name string
		do   func(f *idleFix)
	}{
		{"unread mail", func(f *idleFix) {
			f.exec("INSERT INTO messages(sender,recipient,subject,created) VALUES('lead','coder-1','hi','2026-11-17T11:00:00+00:00')")
		}},
		{"dirty tree", func(f *idleFix) {
			if err := os.WriteFile(filepath.Join(f.wt, "new.txt"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"PR head moved", func(f *idleFix) { f.pr["headRefOid"] = strings.Repeat("0", 40) }},
		{"PR not open", func(f *idleFix) { f.pr["state"] = "MERGED" }},
		{"no PR", func(f *idleFix) { f.exec("UPDATE tasks SET pr_url=NULL WHERE id='T-5'") }},
		{"task done", func(f *idleFix) { f.exec("UPDATE tasks SET status='done' WHERE id='T-5'") }},
		{"no task row", func(f *idleFix) { f.exec("DELETE FROM tasks") }},
		{"worktree missing", func(f *idleFix) { f.exec("UPDATE tasks SET worktree='/no/such/dir' WHERE id='T-5'") }},
		{"already stopping", func(f *idleFix) {
			f.exec("UPDATE agents SET stop_requested_at='2026-11-17T11:00:00+00:00' WHERE name='coder-1'")
		}},
		{"pane runs a command", func(f *idleFix) {
			f.stub.set("pane_process-info", "out", `{"process_info":{"foreground_processes":[{"name":"claude"},{"name":"pytest -q"}]}}`)
		}},
		{"pane unreadable", func(f *idleFix) { f.stub.set("pane_process-info", "rc", "1") }},
		{"shell under claude", func(f *idleFix) { f.claude(301) }},
		{"no claude process", func(f *idleFix) { os.RemoveAll(filepath.Join(f.proc, "300")) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newIdleFix(t)
			c.do(f)
			f.stopWindow()
			if err := f.idleTick(); err != nil {
				t.Fatal(err)
			}
			if n := len(f.events("idle-coder-stopped")); n != 0 {
				t.Fatalf("stopped despite %q", c.name)
			}
			if f.col("SELECT count(*) FROM messages WHERE subject='STOP'") != "0" {
				t.Fatal("STOP sent")
			}
			if f.stub.count("agent send-keys") != 0 {
				t.Fatal("Esc sent")
			}
		})
	}
}

// TestReapIdleCodersDryRun: dry-run (unset or unknown) logs one would-stop per idle episode and stops nothing; a worker
// that works again resets the episode.
func TestReapIdleCodersDryRun(t *testing.T) {
	f := newIdleFix(t)
	f.exec("UPDATE config SET value='bogus' WHERE key='idle_coder_reap'")
	f.stopWindow()
	for i := 0; i < 2; i++ {
		if err := f.idleTick(); err != nil {
			t.Fatal(err)
		}
	}
	want := `coder-1|{"agent": "coder-1", "task": "T-5", "idle_s": 1000, "threshold_s": 900}`
	if got := f.events("idle-coder-would-stop"); len(got) != 1 || got[0] != want {
		t.Fatalf("events = %q", got)
	}
	if f.col("SELECT count(*) FROM messages") != "0" || f.col("SELECT stop_requested_at FROM agents WHERE name='coder-1'") != "<nil>" {
		t.Fatal("dry-run stopped the coder")
	}
	// works again: both clocks are dropped, the next idle spell is a new episode
	if err := f.run("idle_coders", agentsState(t, hostAgent("coder-1", "p-1", "working", ""))); err != nil {
		t.Fatal(err)
	}
	if len(f.throttle()) != 0 {
		t.Fatalf("clocks kept while working: %v", f.throttle())
	}
	f.exec("DELETE FROM config WHERE key='idle_coder_reap'") // unset: the default is off
	if err := f.idleTick(); err != nil {
		t.Fatal(err)
	}
	if len(f.throttle()) != 0 {
		t.Fatalf("off kept clocks: %v", f.throttle())
	}
}

// TestReapIdleCodersClocks: the stale clock of a gone agent is dropped, off forgets every clock, a bad threshold stops
// nobody, an empty host list changes nothing.
func TestReapIdleCodersClocks(t *testing.T) {
	f := newIdleFix(t)
	f.exec("INSERT INTO throttle(key,ts) VALUES('idlereap:gone','1.0'),('idlereap-dry:gone','2.0'),('other','3.0')")
	if err := f.idleTick(); err != nil {
		t.Fatal(err)
	}
	if got := f.throttle(); len(got) != 2 || got["other"] != "3.0" || got["idlereap:coder-1"] != store.PyFloatRepr(f.now) {
		t.Fatalf("throttle = %v", got)
	}
	f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('idle_coder_reap_s','abc')")
	f.now += 5000
	if err := f.idleTick(); err != nil || f.allEvents() != 0 {
		t.Fatalf("bad threshold: err=%v events=%d", err, f.allEvents())
	}
	f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('idle_coder_reap_s','900')")
	if err := f.run("idle_coders", agentsState(t)); err != nil || f.allEvents() != 0 {
		t.Fatalf("empty host list: err=%v events=%d", err, f.allEvents())
	}
	f.exec("UPDATE config SET value='off' WHERE key='idle_coder_reap'")
	if err := f.run("idle_coders", agentsState(t)); err != nil {
		t.Fatal(err)
	}
	if len(f.throttle()) != 1 { // only the unrelated key stays
		t.Fatalf("off kept clocks: %v", f.throttle())
	}
}

// TestReapIdleCodersMaxPerTick: at most three stops per tick, the longest idle first.
func TestReapIdleCodersMaxPerTick(t *testing.T) {
	f := newIdleFix(t)
	var hosts []map[string]any
	for i := 1; i <= 5; i++ {
		name := fmt.Sprintf("coder-%d", i+10)
		f.exec("INSERT INTO tasks(id,goal_id,status,pr_url,worktree) VALUES(?,?,?,?,?)", fmt.Sprintf("T-%d", i+10), "G-1", "in_review", "https://github.com/o/r/pull/1", f.wt)
		f.exec("INSERT INTO agents(name,role,status,goal_id,task_id,pane_id) VALUES(?,'coder','live','G-1',?,?)", name, fmt.Sprintf("T-%d", i+10), fmt.Sprintf("p-%d", i+10))
		f.addProc(fakeProc{pid: 400 + i, ppid: 1, pgid: 400 + i, start: 30, argv: []string{"claude"}, env: map[string]string{"FACTORY_AGENT": name}})
		hosts = append(hosts, hostAgent(name, fmt.Sprintf("p-%d", i+10), "idle", ""))
		// the later the number, the later the clock started: coder-11 has been idle longest
		f.exec("INSERT INTO throttle(key,ts) VALUES(?,?)", "idlereap:"+name, fmt.Sprint(f.now-2000+float64(i)))
	}
	f.exec("DELETE FROM agents WHERE name='coder-1'")
	if err := f.run("idle_coders", agentsState(t, hosts...)); err != nil {
		t.Fatal(err)
	}
	got := f.events("idle-coder-stopped")
	if len(got) != 3 || !strings.HasPrefix(got[0], "coder-11|") || !strings.HasPrefix(got[2], "coder-13|") {
		t.Fatalf("stopped = %q, want coder-11..13", got)
	}
}

// TestReapIdleCodersFailed: an error inside ends in one idle-coder-reap-failed event (truncated to 300 chars) and the
// unit returns nil; the stop's transaction is rolled back whole.
func TestReapIdleCodersFailed(t *testing.T) {
	f := newIdleFix(t)
	f.stopWindow()
	f.exec("DROP TABLE messages") // the STOP message cannot be written: the whole stop must roll back
	if err := f.idleTick(); err != nil {
		t.Fatalf("unit error: %v", err)
	}
	if got := f.events("idle-coder-reap-failed"); len(got) != 1 || !strings.HasPrefix(got[0], "idle-coder-reap|") {
		t.Fatalf("events = %q", got)
	}
	if f.col("SELECT stop_requested_at FROM agents WHERE name='coder-1'") != "<nil>" || len(f.events("stop-requested")) != 0 {
		t.Fatal("a half-written stop survived")
	}
	if err := f.run("idle_coders", State{}); err == nil {
		t.Fatal("missing agents key must be a tick error")
	}
}

func TestPaneBusyParsing(t *testing.T) {
	cases := []struct {
		name string
		info any
		busy bool
	}{
		{"claude only", `{"process_info":{"foreground_processes":[{"name":"claude"}]}}`, false},
		{"node and claude", `{"process_info":{"foreground_processes":[{"name":"/x/node"},{"argv":["claude"],"name":""}]}}`, false},
		{"string entries", `{"process_info":{"foreground_processes":["claude","node --x"]}}`, false},
		{"shell", `{"process_info":{"foreground_processes":[{"name":"zsh"}]}}`, true},
		{"nbsp name", "{\"process_info\":{\"foreground_processes\":[{\"name\":\"\u00a0claude\"}]}}", false},
		{"vt name", "{\"process_info\":{\"foreground_processes\":[{\"name\":\"\\u000bclaude\"}]}}", false},
		{"no list", `{"process_info":{}}`, true},
		{"not a dict", `[]`, true},
		{"empty fg", `{"process_info":{"foreground_processes":[]}}`, false},
		{"nameless", `{"process_info":{"foreground_processes":[{}]}}`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newHerdrStub(t)
			s.set("pane_process-info", "out", c.info.(string))
			f := newReapFix(t)
			r, err := newReapDB(f.db)
			if err != nil {
				t.Fatal(err)
			}
			cl, _ := newHerdrClient(r)
			f.exec("INSERT INTO config(key,value) VALUES('terminal_backend','herdr')")
			got, err := paneBusy(cl, "p-1")
			if err != nil || got != c.busy {
				t.Fatalf("paneBusy = %v, %v; want %v", got, err, c.busy)
			}
		})
	}
	if got, err := paneBusy(nil, ""); err != nil || !got {
		t.Fatalf("no pane id: %v %v", got, err)
	}
}

func FuzzPaneIdle(f *testing.F) {
	f.Add(`{"panes":[{"pane_id":"p","cwd":"/a (deleted)"}]}`, "/a")
	f.Add(`[]`, "")
	f.Add(`{"panes":[1,"x",null]}`, "/")
	f.Fuzz(func(t *testing.T, listJSON, cwd string) {
		var v any
		if json.Unmarshal([]byte(listJSON), &v) != nil {
			t.Skip()
		}
		c := fakeHerdr{v}
		paneIdleInCwd(c, map[string]any{"agent_status": "idle"}, "p", cwd)
		paneGone(c, "p")
		paneBusy(c, "p")
		hostSession(map[string]any{"agent_session": v})
	})
}

type fakeHerdr struct{ v any }

func (f fakeHerdr) Herdr(bool, ...string) (any, error) { return f.v, nil }
