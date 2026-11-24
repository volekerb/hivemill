package tick

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// Tests for unit 21 reap_loop (_tick_u_reap_loop, factory_cli.py:18698) and the unit 20 -> 21 handoff.

// loopState is the handoff of units 3-19 as the reap units read it: agents, hc_back and t.
func loopState(t *testing.T, now float64, agents ...map[string]any) State {
	t.Helper()
	st := agentsState(t, agents...)
	st["hc_back"] = json.RawMessage("false")
	st["t"] = json.RawMessage(fmt.Sprintf("%.1f", now))
	return st
}

func (u *unitFix) reapRow(name, role, status, pane string) {
	u.t.Helper()
	u.exec("INSERT OR REPLACE INTO agents(name,role,status,task_id,pane_id,started,session_id,spawn_args) VALUES(?,?,?,?,?,?,?,'{}')",
		name, role, status, "T-5", pane, "2026-11-17T08:00:00+00:00", "s-"+name)
}

func (u *unitFix) eventRows(action string) []string {
	u.t.Helper()
	rs, err := u.db.Query("SELECT actor||'|'||entity||'|'||entity_id||'|'||coalesce(detail,'NULL') FROM events WHERE action=? ORDER BY id", action)
	if err != nil {
		u.t.Fatal(err)
	}
	defer rs.Close()
	var out []string
	for rs.Next() {
		var s string
		if err := rs.Scan(&s); err != nil {
			u.t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

// TestReapLoopLiveAgent: a live agent's pane is never closed; a done agent is closed, its row exited and one "reaped"
// event written as Python writes it; a foreign pane is skipped once; an unreadable host leaves the done row alone.
func TestReapLoopLiveAgent(t *testing.T) {
	t.Run("live pane is not closed", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("coder-1", "coder", "live", "p-1")
		u.stub.set("agent_list", "out", `{"agents":[{"name":"coder-1","agent":"claude","pane_id":"p-1","agent_status":"working"}]}`)
		if err := u.run("reap_loop", loopState(t, u.now, hostAgent("coder-1", "p-1", "working", "s-coder-1"))); err != nil {
			t.Fatal(err)
		}
		if n := u.stub.count("pane close"); n != 0 {
			t.Errorf("pane close calls = %d: %q", n, u.stub.calls())
		}
		if got := u.col("SELECT status FROM agents WHERE name='coder-1'"); got != "live" {
			t.Errorf("status = %s", got)
		}
		if got := u.col("SELECT count(*) FROM events"); got != "0" {
			t.Errorf("events = %s", got)
		}
	})
	t.Run("done agent is closed and reaped", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("coder-1", "coder", "done", "p-1")
		u.stub.set("agent_list", "out", `{"agents":[{"name":"coder-1","agent":"claude","pane_id":"p-1","agent_status":"done"}]}`)
		if err := u.run("reap_loop", loopState(t, u.now, hostAgent("coder-1", "p-1", "done", "s-coder-1"))); err != nil {
			t.Fatal(err)
		}
		if n := u.stub.count("pane close p-1"); n != 1 {
			t.Errorf("pane close p-1 calls = %d: %q", n, u.stub.calls())
		}
		if got := u.col("SELECT status||'|'||coalesce(pane_id,'NULL')||'|'||(ended IS NOT NULL) FROM agents WHERE name='coder-1'"); got != "exited|p-1|1" {
			t.Errorf("row = %s", got)
		}
		// Python: _event(con, "owner", "agent", name, "reaped", r["pane_id"]) - the detail is the bare pane id.
		if got := u.eventRows("reaped"); !reflect.DeepEqual(got, []string{"owner|agent|coder-1|p-1"}) {
			t.Errorf("reaped events = %q", got)
		}
	})
	t.Run("a foreign pane is never closed and skipped once", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("x-1", "coder", "live", "p-9")
		u.reapRow("old-1", "coder", "exited", "p-0")
		host := hostAgent("old-1", "p-9", "idle", "")
		for i := 0; i < 2; i++ {
			if err := u.run("reap_loop", loopState(t, u.now, host)); err != nil {
				t.Fatal(err)
			}
		}
		if n := u.stub.count("pane close"); n != 0 {
			t.Errorf("pane close calls = %d: %q", n, u.stub.calls())
		}
		if got := u.eventRows("pane-foreign-skip"); !reflect.DeepEqual(got, []string{"owner|agent|x-1|p-9"}) {
			t.Errorf("pane-foreign-skip events = %q, want exactly one", got)
		}
		if got := u.col("SELECT status FROM agents WHERE name='x-1'"); got != "live" {
			t.Errorf("status = %s", got)
		}
	})
	t.Run("a host answer of unknown leaves the done row alone", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("coder-1", "coder", "done", "p-1")
		u.stub.set("agent_list", "rc", "1")
		if err := u.run("reap_loop", loopState(t, u.now, hostAgent("coder-1", "p-1", "done", "s-coder-1"))); err != nil {
			t.Fatal(err)
		}
		if got := u.col("SELECT status FROM agents WHERE name='coder-1'"); got != "done" {
			t.Errorf("status = %s, want done", got)
		}
		if got := u.eventRows("reaped"); len(got) != 0 {
			t.Errorf("reaped events = %q", got)
		}
	})
	t.Run("a vanished live agent is lost with a director alert", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("coder-1", "coder", "live", "p-1")
		if err := u.run("reap_loop", loopState(t, u.now)); err != nil {
			t.Fatal(err)
		}
		if got := u.col("SELECT status FROM agents WHERE name='coder-1'"); got != "lost" {
			t.Errorf("status = %s", got)
		}
		if got := u.eventRows("lost"); !reflect.DeepEqual(got, []string{"owner|agent|coder-1|p-1"}) {
			t.Errorf("lost events = %q", got)
		}
		if got := u.col("SELECT body FROM messages WHERE subject='agent coder-1 vanished'"); !strings.HasPrefix(got, "pane p-1 no longer hosts it. Task T-5. Decide: respawn or reassign.") {
			t.Errorf("alert body = %q", got)
		}
	})
	t.Run("a blocked unknown dialog arms blocked_since then alerts once", func(t *testing.T) {
		u := newUnitFix(t)
		u.reapRow("coder-1", "coder", "live", "p-1")
		u.stub.set("agent_list", "out", `{"agents":[{"name":"coder-1","agent":"claude","pane_id":"p-1","agent_status":"blocked"}]}`)
		b, _ := json.Marshal(map[string]any{"text": dlgUnknown})
		u.stub.set("agent_read", "out", string(b))
		host := hostAgent("coder-1", "p-1", "blocked", "s-coder-1")
		if err := u.run("reap_loop", loopState(t, u.now, host)); err != nil {
			t.Fatal(err)
		}
		if got := u.col("SELECT blocked_since FROM agents WHERE name='coder-1'"); got != fmt.Sprint(u.now) && got != fmt.Sprintf("%.0f", u.now) {
			t.Errorf("blocked_since = %s, want %v", got, u.now)
		}
		later := u.now + 700
		for i := 0; i < 2; i++ {
			if err := u.run("reap_loop", loopState(t, later, host)); err != nil {
				t.Fatal(err)
			}
		}
		if got := u.col("SELECT count(*) FROM messages WHERE subject='coder-1 blocked >10 min'"); got != "1" {
			t.Errorf("blocked alerts = %s, want 1", got)
		}
		if k := u.stub.count("agent send-keys"); k != 0 {
			t.Errorf("keys sent for an unknown dialog: %d", k)
		}
	})
}

// TestHandoffStateAfterReap: after units 20 and 21 the state holds agents, hc_back and t with the JSON types Python's
// st dict holds (testdata/handoff_python_fixture.json, from handoff_python_fixture.gen.txt, which runs the real
// _tick_u_pending_nudges), and no by_name / by_pane.
func TestHandoffStateAfterReap(t *testing.T) {
	var fx struct {
		Keys   []string          `json:"keys"`
		Types  map[string]string `json:"types"`
		Agents []map[string]any  `json:"agents"`
	}
	b, err := os.ReadFile("testdata/handoff_python_fixture.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &fx); err != nil {
		t.Fatal(err)
	}
	u := newUnitFix(t)
	u.reapRow("coder-x", "coder", "live", "p-1")
	st := agentsState(t, fx.Agents...)
	st["hc_back"] = json.RawMessage("false")
	for _, unit := range []string{"pending_nudges", "reap_loop"} {
		prev := reapNow
		reapNow = func() time.Time { return u.clock }
		err := u.run(unit, st)
		reapNow = prev
		if err != nil {
			t.Fatalf("%s: %v", unit, err)
		}
	}
	var keys []string
	for k := range st {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	if !reflect.DeepEqual(keys, fx.Keys) {
		t.Errorf("handoff keys = %q, python has %q", keys, fx.Keys)
	}
	for k, want := range fx.Types {
		var v any
		if err := json.Unmarshal(st[k], &v); err != nil {
			t.Fatalf("%s: %v", k, err)
		}
		got := map[string]string{"float64": "float", "bool": "bool", "[]interface {}": "list"}[fmt.Sprintf("%T", v)]
		if k == "t" && !strings.ContainsAny(string(st[k]), ".eE") {
			got = "int" // Python's time.time() is a float: its JSON always has a fraction
		}
		if got != want {
			t.Errorf("%s: JSON type %s (%s), python has %s", k, got, st[k], want)
		}
	}
	var agents []map[string]any
	if err := json.Unmarshal(st["agents"], &agents); err != nil || !reflect.DeepEqual(agents, fx.Agents) {
		t.Errorf("agents changed: %v %v", agents, err)
	}
}
