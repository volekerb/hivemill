package s2cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Go tests for tests/test_stop.py: 62 of 63 cases. The Python module stays as the parity oracle until P7a.
//
// PARTIAL (P7a, T-1495): keep tests/test_stop.py until the Go inbox handles stale STOP orders.
// not ported: test_inbox_marks_pre_spawn_stop_stale - the Go inbox (go/internal/cli/cmd_inbox.go) has no T-1616
// stale-STOP handling (no "[stale: sent to an earlier pane]" marker, no stop-stale-dropped event), so the case
// fails against the Go CLI; follow-up: port T-1616 into cmd_inbox.go, then port this case.

func TestStopSendsDecisionEscWaitPromptInOrder(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder", "--reason", "scope changed"}, "zz-lead", 0, nil)
	msgs := s.SQL("SELECT kind, sender, read_at, body FROM messages WHERE recipient=? AND subject LIKE ?", "zz-coder", "STOP")
	if len(msgs) != 1 {
		t.Fatalf("STOP msgs = %d, want 1", len(msgs))
	}
	if msgs[0][0] != "decision" || msgs[0][1] != "zz-lead" {
		t.Fatalf("msg kind/sender = %v, want decision/zz-lead", msgs[0])
	}
	if msgs[0][2] != "" && msgs[0][2] != "NULL" {
		t.Fatalf("read_at = %q, want unset", msgs[0][2])
	}
	contains(t, "body", msgs[0][3], "scope changed")
	contains(t, "body", msgs[0][3], "Stop work now. Push any work in progress to your branch")
	contains(t, "body", msgs[0][3], "factory done '<stopped: pushed sha|nothing; state>'")
	ev := s.stopEvents("stop-requested", "zz-coder")
	d := stopEventDetail(t, ev[0][6])
	if d["by"] != "zz-lead" || d["reason"] != "scope changed" {
		t.Fatalf("stop-requested detail = %v", d)
	}
	if s.stopField(t, "zz-coder", "stop_requested_at") == "" || s.stopField(t, "zz-coder", "stop_requested_at") == "NULL" {
		t.Fatal("stop_requested_at unset")
	}
	calls := s.stopHerdrArgvs()
	iEsc := stopIndexArgvs(calls, "agent", "send-keys")
	if iEsc < 0 || calls[iEsc][3] != "esc" {
		t.Fatalf("esc call missing: %v", calls)
	}
	wait := stopIndexArgvs(calls, "agent", "wait")
	if wait < 0 {
		t.Fatal("wait call missing")
	}
	if got := stopWaitFlags(calls[wait]); !reflect.DeepEqual(got, []string{"--until", "--until", "--until", "--timeout"}) {
		t.Fatalf("wait flags = %v", got)
	}
	if calls[wait][len(calls[wait])-1] != "30000" {
		t.Fatalf("wait timeout = %s, want 30000", calls[wait])
	}
	prompt := stopIndexArgvs(calls, "agent", "prompt")
	if iEsc >= wait || wait >= prompt {
		t.Fatalf("order esc=%d wait=%d prompt=%d", iEsc, wait, prompt)
	}
	want := "📬 STOP from zz-lead: run factory inbox now and follow the STOP order."
	if calls[prompt][3] != want {
		t.Fatalf("prompt = %q, want %q", calls[prompt][3], want)
	}
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatalf("status = %s, want live", s.stopField(t, "zz-coder", "status"))
	}
}

func TestStopDryRunChangesNothing(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	beforeM := s.Count("SELECT count(*) FROM messages")
	beforeE := s.Count("SELECT count(*) FROM events")
	p := s.stop(t, []string{"zz-coder", "--dry-run"}, "zz-lead", 0, nil)
	contains(t, "stdout", p.Stdout, "send-keys zz-coder esc")
	contains(t, "stdout", p.Stdout, "herdr agent wait zz-coder")
	if s.Count("SELECT count(*) FROM messages") != beforeM {
		t.Fatal("messages changed")
	}
	if s.Count("SELECT count(*) FROM events") != beforeE {
		t.Fatal("events changed")
	}
	if at := s.stopField(t, "zz-coder", "stop_requested_at"); at != "" && at != "NULL" {
		t.Fatalf("stop_requested_at = %q, want nil", at)
	}
	if len(s.Calls("herdr")) != 0 {
		t.Fatalf("herdr calls = %v", s.Calls("herdr"))
	}
}

func TestStopLeadMayStopOwnTeamWorkerOnly(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	p := s.stop(t, []string{"zz-coder-b"}, "zz-lead", 3, nil)
	contains(t, "stderr", p.Stderr, "not in your team")
	if len(s.Calls("herdr")) != 0 {
		t.Fatal("herdr was called")
	}
	if at := s.stopField(t, "zz-coder-b", "stop_requested_at"); at != "" && at != "NULL" {
		t.Fatal("zz-coder-b stop_requested_at set")
	}
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	if at := s.stopField(t, "zz-coder", "stop_requested_at"); at == "" || at == "NULL" {
		t.Fatal("zz-coder stop_requested_at unset")
	}
}

func TestStopDirectorMayStopAnyWorkerNotHq(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder-b"}, "zz-director", 0, nil)
	if at := s.stopField(t, "zz-coder-b", "stop_requested_at"); at == "" || at == "NULL" {
		t.Fatal("zz-coder-b stop_requested_at unset")
	}
	for _, hq := range []string{"zz-lead", "zz-boss", "zz-director"} {
		p := s.stop(t, []string{hq}, "zz-director", 3, nil)
		contains(t, "stderr", p.Stderr, "workers only")
		if at := s.stopField(t, hq, "stop_requested_at"); at != "" && at != "NULL" {
			t.Fatalf("%s stop_requested_at set", hq)
		}
	}
	s.stop(t, []string{"zz-coder"}, "owner", 0, nil)
	if at := s.stopField(t, "zz-coder", "stop_requested_at"); at == "" || at == "NULL" {
		t.Fatal("owner stop unset")
	}
}

func TestStopWorkerMayNotStop(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	p := s.stop(t, []string{"zz-coder-b"}, "zz-coder", 3, nil)
	contains(t, "stderr", p.Stderr, "workers may not run `factory stop`")
	if at := s.stopField(t, "zz-coder-b", "stop_requested_at"); at != "" && at != "NULL" {
		t.Fatal("stop_requested_at set")
	}
}

func TestStopUnknownAndNonLiveAgentExit2(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-nobody"}, "zz-director", 2, nil)
	sqlExec(t, s.Env, "UPDATE agents SET status='exited' WHERE name='zz-coder'")
	s.stop(t, []string{"zz-coder"}, "zz-director", 2, nil)
}

func TestStopTickForceStopsAfterTimeout(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stopOK(t, "zz-director", "config", "max_live_agents", "2")
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	contains(t, "res", s.stopOK(t, "zz-director", "res").Stdout, "agents live 2/2")
	s.stopAgeStop(t, 200, "zz-coder")
	s.stopTick(t, nil)
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("still live before timeout")
	}
	s.stopAgeStop(t, 301, "zz-coder")
	s.stopTick(t, nil)
	if !stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatalf("verbs = %v", s.stopHerdrVerbs())
	}
	found := false
	for _, c := range s.Calls("herdr") {
		if reflect.DeepEqual(c.Argv, []string{"pane", "close", "p1"}) {
			found = true
		}
	}
	if !found {
		t.Fatal("pane close p1 missing")
	}
	r := s.stopRow(t, "zz-coder")
	if r["status"] != "stopped" || r["ended"] == "" || r["ended"] == "NULL" {
		t.Fatalf("row = %v", r)
	}
	d := stopEventDetail(t, s.stopEvents("stopped", "zz-coder")[0][6])
	if d["reason"] != "timeout" || d["pane"] != "p1" {
		t.Fatalf("stopped event = %v", d)
	}
	contains(t, "res", s.stopOK(t, "zz-director", "res").Stdout, "agents live 1/2")
	msgs := s.stopMsgs("zz-lead", "agent zz-coder force-stopped")
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	if !stopForceRe.MatchString(msgs[0][5]) {
		t.Fatalf("body = %q", msgs[0][5])
	}
	contains(t, "body", msgs[0][5], "gh pr list --head factory/t-0001")
	contains(t, "body", msgs[0][5], "`factory task show T-0001` before reassigning")
	contains(t, "agents --all", s.stopOK(t, "zz-director", "agents", "--all").Stdout, "zz-coder")
	allOut := s.stopOK(t, "zz-director", "agents").Stdout
	if strings.Contains(allOut, "zz-coder ") {
		t.Fatalf("live list contains zz-coder: %q", allOut)
	}
}

func TestStopNowClosesImmediately(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, nil)
	contains(t, "stdout", p.Stdout, "stopped now")
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != 1 {
		t.Fatal("STOP msg missing")
	}
	verbs := s.stopHerdrVerbs()
	if !stopHasVerb(verbs, [2]string{"pane", "close"}) {
		t.Fatal("no pane close")
	}
	for _, bad := range [][2]string{{"agent", "send-keys"}, {"agent", "wait"}, {"agent", "prompt"}} {
		if stopHasVerb(verbs, bad) {
			t.Fatalf("unexpected %v", bad)
		}
	}
	d := stopEventDetail(t, s.stopEvents("stopped", "zz-coder")[0][6])
	if d["reason"] != "now" {
		t.Fatalf("reason = %v", d["reason"])
	}
}

func TestStopDoneAfterStopClearsFlagAndNoForceClose(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	sqlExec(t, s.Env, "UPDATE agents SET stop_attempt_at=?, stop_failed_kind='close-failed' WHERE name='zz-coder'", stopOLD)
	p := s.RunWith(ft.Opts{Agent: "zz-coder"}, "done", "x")
	if p.Exit == 0 {
		t.Fatal("done should fail before reading STOP")
	}
	contains(t, "stderr", p.Stderr, "unread orders")
	s.stopOK(t, "zz-coder", "done", "stopped: nothing; clean")
	if st := s.stopField(t, "zz-coder", "status"); st != "done" {
		t.Fatalf("status = %s", st)
	}
	for _, c := range []string{"stop_requested_at", "stop_attempt_at", "stop_failed_kind"} {
		if v := s.stopField(t, "zz-coder", c); v != "" && v != "NULL" {
			t.Fatalf("%s = %q", c, v)
		}
	}
	s.stopTick(t, nil)
	if len(s.stopEvents("stopped", "zz-coder")) != 0 {
		t.Fatal("force stopped")
	}
	if s.stopField(t, "zz-coder", "status") != "exited" {
		t.Fatal("not reaped to exited")
	}
}

func TestStopDoneRefusedWhenNotLive(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	for _, st := range []string{"stopped", "done", "lost"} {
		sqlExec(t, s.Env, "UPDATE agents SET status=? WHERE name='zz-coder'", st)
		p := s.stopRefused(t, 2, "zz-coder", "done", "again")
		contains(t, "stderr", p.Stderr, "not live")
	}
	if len(s.stopMsgs("zz-lead", "zz-coder finished%")) != 0 {
		t.Fatal("unexpected finished msgs")
	}
}

func TestStopMigrationAddsStopRequestedAtIdempotent(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	cols := []string{"stop_requested_at", "stop_attempt_at", "stop_failed_kind"}
	for _, c := range cols {
		sqlExec(t, s.Env, "ALTER TABLE agents DROP COLUMN "+c)
	}
	for i := 0; i < 2; i++ {
		s.stopOK(t, "zz-director", "agents", "--all")
		if !stopColBool(t, s.Env, cols...) {
			t.Fatalf("run %d: missing columns", i)
		}
	}
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	if at := s.stopField(t, "zz-coder", "stop_requested_at"); at == "" || at == "NULL" {
		t.Fatal("stop_requested_at unset after stop")
	}
}

func TestStopFailedWhenPaneCloseFailsKeepsCounted(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.SetHerdr(map[string]any{"pane_close_fail": true})
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	contains(t, "stderr", p.Stderr, "close-failed")
	contains(t, "stderr", p.Stderr, "still counted")
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("status changed")
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	if d["name"] != "zz-coder" || d["pane"] != "p1" || d["reason"] != "close-failed" {
		t.Fatalf("detail = %v", d)
	}
	contains(t, "res", s.stopOK(t, "zz-director", "res").Stdout, "agents live 2/")
}

func TestStopFailedWhenProcessAliveKeepsCounted(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.FakeClaude(4242, 1, "claude", "--resume", stopSID)
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	contains(t, "stderr", p.Stderr, "process-alive")
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if !stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane close missing")
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	if d["reason"] != "process-alive" {
		t.Fatalf("reason = %v", d["reason"])
	}
	pids, _ := d["pids"].([]interface{})
	if len(pids) != 1 {
		t.Fatalf("pids = %v", d["pids"])
	}
}

func TestStopStoppedOnlyAfterCloseAndNoProcess(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	fast := s.stopFastEnv()
	s.SetHerdr(map[string]any{"pane_close_fail": true})
	s.FakeClaude(4242, 1, "claude", "--session-id", stopSID)
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, fast)
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("early stop")
	}
	s.SetHerdr(map[string]any{"pane_close_fail": false})
	sqlExec(t, s.Env, "UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, fast)
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("still live with process")
	}
	s.stopRemoveProc(t, 4242)
	s.stopHerdrAgents(t, [][2]string{{"zz-coder", "p1"}, {"zz-coder-b", "p2"}})
	sqlExec(t, s.Env, "UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, fast)
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestStopRetryThrottledAndMessageOncePerKind(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	fast := s.stopFastEnv()
	s.FakeClaude(4242, 1, "claude", "--resume", stopSID)
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	s.stopAgeStop(t, 3600, "zz-coder")
	s.stopTick(t, fast)
	failed := len(s.stopEvents("stop-failed", "zz-coder"))
	alerts := len(s.stopMsgs("zz-lead", "stop of zz-coder failed%"))
	if failed != 1 || alerts != 1 {
		t.Fatalf("failed=%d alerts=%d", failed, alerts)
	}
	msgs := s.stopMsgs("zz-lead", "stop of zz-coder failed%")
	contains(t, "body", msgs[0][5], "process-alive")
	contains(t, "body", msgs[0][5], "still counted")
	contains(t, "body", msgs[0][5], "gh pr list --head factory/t-0001")
	s.stopTick(t, fast)
	if failed2 := len(s.stopEvents("stop-failed", "zz-coder")); failed2 != 1 {
		t.Fatalf("failed events = %d", failed2)
	}
	if alerts2 := len(s.stopMsgs("zz-lead", "stop of zz-coder failed%")); alerts2 != 1 {
		t.Fatalf("alerts = %d", alerts2)
	}
	sqlExec(t, s.Env, "UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", stopAgoWall(301))
	s.stopTick(t, fast)
	if len(s.stopEvents("stop-failed", "zz-coder")) != 2 || len(s.stopMsgs("zz-lead", "stop of zz-coder failed%")) != 1 {
		t.Fatal("retry same kind")
	}
	sqlExec(t, s.Env, "UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", stopAgoWall(301))
	s.stopHerdrAgents(t, [][2]string{{"zz-coder", "p1"}, {"zz-coder-b", "p2"}})
	s.SetHerdr(map[string]any{"pane_close_fail": true})
	s.stopTick(t, fast)
	if len(s.stopEvents("stop-failed", "zz-coder")) != 3 || len(s.stopMsgs("zz-lead", "stop of zz-coder failed%")) != 2 {
		t.Fatal("kind change message")
	}
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
}

func TestStopForceStopNullSessionFailsClosed(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	sqlExec(t, s.Env, "UPDATE agents SET session_id=NULL WHERE name='zz-coder'")
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	contains(t, "stderr", p.Stderr, "process-unknown")
	contains(t, "stderr", p.Stderr, "The pane was NOT closed")
	contains(t, "stderr", p.Stderr, "end it by hand or relaunch it")
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	contains(t, "note", d["note"].(string), "NOT closed")
	if s.stopField(t, "zz-coder", "status") != "live" || s.stopField(t, "zz-coder", "stop_failed_kind") != "process-unknown" {
		t.Fatal("row state")
	}
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane closed")
	}
	contains(t, "res", s.stopOK(t, "zz-director", "res").Stdout, "agents live 2/")
	s.stopAgeStop(t, 3600, "zz-coder")
	s.stopTick(t, s.stopFastEnv())
	if len(s.stopMsgs("zz-director", "stop of zz-coder failed%")) != 1 {
		t.Fatal("second alert")
	}
}

func TestStopForceStopNullSessionUsesHerdrAgentSession(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	sqlExec(t, s.Env, "UPDATE agents SET session_id=NULL WHERE name='zz-coder'")
	s.SetHerdr(map[string]any{"agents": []map[string]any{{
		"name": "zz-coder", "agent": "claude", "pane_id": "p1", "agent_status": "working",
		"agent_session": map[string]string{"value": stopSID},
	}}})
	s.FakeClaude(4242, 1, "claude", "--resume", stopSID)
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	contains(t, "stderr", p.Stderr, "process-alive")
	s.stopRemoveProc(t, 4242)
	sqlExec(t, s.Env, "UPDATE agents SET stop_requested_at=NULL WHERE name='zz-coder'")
	s.SetHerdr(map[string]any{"agents": []map[string]any{{
		"name": "zz-coder", "agent": "claude", "pane_id": "p1", "agent_status": "working",
		"agent_session": map[string]string{"value": stopSID},
	}}})
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, s.stopFastEnv())
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestStopForceStopProcUnreadableFailsClosed(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	gone := filepath.Join(s.Home, "no-such-proc-dir")
	env := map[string]string{"FACTORY_PROC_DIR": gone}
	for k, v := range s.stopFastEnv() {
		env[k] = v
	}
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, env)
	contains(t, "stderr", p.Stderr, "process-unknown")
	contains(t, "stderr", p.Stderr, "The pane was NOT closed")
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane closed")
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	if d["reason"] != "process-unknown" {
		t.Fatalf("reason = %v", d["reason"])
	}
}

func TestStopForceStopProcLostAfterCloseSaysPaneClosed(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	s.stopAgeStop(t, 3600, "zz-coder")
	s.SetHerdr(map[string]any{"agents": []map[string]any{}})
	body := `
con = mod.db()
try:
    with mock.patch.object(mod, "_scan_claude", side_effect=[{}, None]), mock.patch.object(mod, "pane_gone", return_value=True):
        got, note = mod.force_stop(con, "zz-coder", "timeout")
        if got != "failed:process-unknown" or note != mod._UNKNOWN_AFTER:
            print("FAIL", repr(got), repr(note))
            sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatalf("force_stop python exit %d", exit)
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	contains(t, "note", d["note"].(string), "The pane was closed but the process state is unknown")
	contains(t, "note", d["note"].(string), "end it by hand or relaunch it")
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
}

func TestStopIgnoresNonLiveNullSidRows(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	fast := s.stopFastEnv()
	for _, st := range []string{"exited", "done", "lost"} {
		sqlExec(t, s.Env, "UPDATE agents SET status=?, session_id=NULL, stop_requested_at=NULL WHERE name='zz-coder-b'", st)
		s.stop(t, []string{"zz-coder-b"}, "zz-director", 2, nil)
		s.stop(t, []string{"zz-coder-b", "--now"}, "zz-director", 2, nil)
		s.stopTick(t, fast)
		cur := s.stopField(t, "zz-coder-b", "status")
		if cur != st && cur != "exited" {
			t.Fatalf("status = %s for %s", cur, st)
		}
	}
	if len(s.stopEvents("stop-failed", "zz-coder-b")) != 0 || len(s.stopEvents("stopped", "zz-coder-b")) != 0 {
		t.Fatal("stop events for zz-coder-b")
	}
}

func TestStopForceStopRechecksStatusBeforeClose(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stopHookDone(t)
	env := s.stopFastEnv()
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, env)
	contains(t, "stdout", p.Stdout, "finished meanwhile")
	if s.stopField(t, "zz-coder", "status") != "done" {
		t.Fatal("not done")
	}
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane closed")
	}
	if len(s.stopEvents("stop-failed", "zz-coder")) != 0 || len(s.stopEvents("stopped", "zz-coder")) != 0 {
		t.Fatal("events")
	}
}

func TestStopNowFailureMessagesCaller(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.SetHerdr(map[string]any{"pane_close_fail": true})
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	msgs := s.stopMsgs("zz-director", "stop of zz-coder failed%")
	if len(msgs) != 1 {
		t.Fatalf("msgs = %d", len(msgs))
	}
	if msgs[0][3] != "alert" || msgs[0][1] != "zz-director" {
		t.Fatalf("kind/sender = %v", msgs[0])
	}
	contains(t, "body", msgs[0][5], "close-failed")
	s.stopAgeStop(t, 3600, "zz-coder")
	s.stopTick(t, s.stopFastEnv())
	if len(s.stopMsgs("zz-director", "stop of zz-coder failed%")) != 1 {
		t.Fatal("second alert")
	}
}

func TestStopRepeatStopKeepsOriginalDeadline(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	sqlExec(t, s.Env, "UPDATE agents SET stop_requested_at=?, stop_attempt_at=?, stop_failed_kind='close-failed' WHERE name='zz-coder'",
		stopAgoWall(100), stopAgoWall(50))
	before := s.stopRow(t, "zz-coder")
	nMsgs := len(s.stopMsgs("zz-coder", "STOP"))
	nCalls := len(s.Calls("herdr"))
	for _, who := range []string{"zz-lead", "zz-director"} {
		p := s.stop(t, []string{"zz-coder"}, who, 0, nil)
		contains(t, "stdout", p.Stdout, "already stopping since "+before["stop_requested_at"]+"; the hard fallback runs at")
		notContains(t, "stdout", p.Stdout, "--now is ignored")
	}
	p := s.stop(t, []string{"zz-coder"}, "zz-director", 0, nil)
	contains(t, "stdout", p.Stdout, "(retrying; last attempt "+before["stop_attempt_at"]+")")
	due := stopDueAttempt(before["stop_attempt_at"])
	contains(t, "stdout", p.Stdout, "the hard fallback runs at "+due)
	after := s.stopRow(t, "zz-coder")
	for _, col := range []string{"stop_requested_at", "stop_attempt_at", "stop_failed_kind"} {
		if after[col] != before[col] {
			t.Fatalf("%s changed %q -> %q", col, before[col], after[col])
		}
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != nMsgs || len(s.Calls("herdr")) != nCalls {
		t.Fatal("side effects on repeat")
	}
	p = s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, s.stopFastEnv())
	notContains(t, "stdout", p.Stdout, "--now is ignored")
	contains(t, "stdout", p.Stdout, "stopped now")
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	if s.stopField(t, "zz-coder", "stop_requested_at") != before["stop_requested_at"] {
		t.Fatal("stop_requested_at changed")
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != nMsgs {
		t.Fatal("extra STOP msg")
	}
}

func TestStopRespawnClearsStopFlag(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, nil)
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	s.stopOK(t, "zz-director", "spawn", "coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", "zz-coder", "--cwd", s.Home)
	for _, c := range []string{"status", "stop_requested_at", "stop_attempt_at", "stop_failed_kind"} {
		v := s.stopField(t, "zz-coder", c)
		if c == "status" {
			if v != "live" {
				t.Fatalf("status = %s", v)
			}
		} else if v != "" && v != "NULL" {
			t.Fatalf("%s = %q", c, v)
		}
	}
}

func TestStopRespawnViaQueueClearsStopFlag(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, nil)
	s.stopOK(t, "zz-director", "config", "max_live_agents", "0")
	s.stopOK(t, "zz-director", "spawn", "coder", "--goal", "G-001", "--name", "zz-coder", "--cwd", s.Home)
	if s.stopField(t, "zz-coder", "status") != "queued" {
		t.Fatal("not queued")
	}
	for _, c := range []string{"stop_requested_at", "stop_attempt_at", "stop_failed_kind"} {
		if v := s.stopField(t, "zz-coder", c); v != "" && v != "NULL" {
			t.Fatalf("%s = %q", c, v)
		}
	}
}

func TestStopFallbackDoesNotOverwriteDone(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	sqlExec(t, s.Env, "UPDATE agents SET status='done', summary='finished' WHERE name='zz-coder'")
	n := len(s.Calls("herdr"))
	body := `
con = mod.db()
try:
    got, note = mod.force_stop(con, "zz-coder", "timeout")
    if got != "skip" or note is not None:
        print("FAIL", repr(got), repr(note))
        sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
	if len(s.Calls("herdr")) != n {
		t.Fatal("herdr called")
	}
	if s.stopField(t, "zz-coder", "status") != "done" || s.stopField(t, "zz-coder", "summary") != "finished" {
		t.Fatal("row changed")
	}
	if len(s.stopEvents("stopped", "zz-coder")) != 0 || len(s.stopMsgs("zz-lead", "agent zz-coder force-stopped")) != 0 {
		t.Fatal("unexpected events/msgs")
	}
}

func TestStopTickDoesNotMarkStoppingAgentLost(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stopHerdrAgents(t, [][2]string{{"zz-coder-b", "p2"}})
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	s.stopTick(t, nil)
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if len(s.stopEvents("lost", "zz-coder")) != 0 {
		t.Fatal("lost event")
	}
	if len(s.SQL("SELECT * FROM messages WHERE recipient='director' AND subject LIKE ?", "agent zz-coder vanished")) != 0 {
		t.Fatal("vanished msg")
	}
	s.FakeClaude(4242, 1, "claude", "--resume", stopSID)
	s.stopAgeStop(t, 3600, "zz-coder")
	s.stopTick(t, s.stopFastEnv())
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live during process-alive")
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	if d["reason"] != "process-alive" {
		t.Fatalf("reason = %v", d["reason"])
	}
	s.stopRemoveProc(t, 4242)
	sqlExec(t, s.Env, "UPDATE agents SET stop_attempt_at=NULL WHERE name='zz-coder'")
	s.stopTick(t, nil)
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
}

func TestStopAbortsStepsWhenAgentDone(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stopHookDone(t)
	p := s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	contains(t, "stdout", p.Stdout, "no longer live")
	verbs := s.stopHerdrVerbs()
	if !stopHasVerb(verbs, [2]string{"agent", "send-keys"}) {
		t.Fatal("send-keys missing")
	}
	for _, bad := range [][2]string{{"agent", "wait"}, {"agent", "prompt"}} {
		if stopHasVerb(verbs, bad) {
			t.Fatalf("unexpected %v", bad)
		}
	}
}

func TestStopHoldsNoTransactionDuringWait(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.SetHerdr(map[string]any{"wait_sleep": float64(4)})
	cmd := exec.Command(s.GoBin, "stop", "zz-coder")
	cmd.Env = s.Environ("zz-lead", stopUnsetTestNow(nil))
	var se bytes.Buffer
	cmd.Stderr = &se
	var so bytes.Buffer
	cmd.Stdout = &so
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if stopHasVerb(s.stopHerdrVerbs(), [2]string{"agent", "wait"}) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("stop never reached wait")
		}
		time.Sleep(50 * time.Millisecond)
	}
	t0 := time.Now()
	sqlExec(t, s.Env, "UPDATE agents SET summary='written during the wait' WHERE name='zz-coder'")
	if time.Since(t0) > 2*time.Second {
		t.Fatal("blocked behind stop transaction")
	}
	select {
	case err := <-done:
		t.Fatalf("stop had already exited during the wait: %v stderr=%s", err, se.String())
	default: // the stop was still waiting
	}
	select {
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("stop hung")
	case err := <-done:
		if err != nil {
			t.Logf("stop wait: %v stderr=%s", err, se.String())
		}
	}
}

func TestStopDequeueWaitsAfterStopSameWorktree(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	wt := filepath.Join(s.Home, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	sqlExec(t, s.Env, "UPDATE agents SET cwd=? WHERE name='zz-coder'", wt)
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, nil)
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	args, _ := json.Marshal(map[string]any{
		"role": "coder", "goal": "G-001", "team": "g-001-a", "task": "T-0001",
		"name": "zz-succ", "cwd": wt, "prompt": nil, "model": nil, "resume": nil,
	})
	sqlExec(t, s.Env, "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,spawn_args,queued_at) VALUES('zz-succ','coder','G-001','g-001-a','T-0001','queued',?,?)",
		string(args), stopAgoWall(10))
	s.stopTick(t, nil)
	if s.stopField(t, "zz-succ", "status") != "queued" {
		t.Fatal("succ spawned early")
	}
	sqlExec(t, s.Env, "UPDATE agents SET ended=? WHERE name='zz-coder'", stopAgoWall(200))
	s.stopTick(t, nil)
	if s.stopField(t, "zz-succ", "status") != "live" {
		t.Fatal("succ not live")
	}
}

func TestStopEscalationClosesPane(t *testing.T) {
	s := newStopEscalationEnv(t)
	before := s.stopField(t, "zz-coder", "stop_requested_at")
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, s.stopFastEnv())
	contains(t, "stdout", p.Stdout, "stopped now")
	if s.stopField(t, "zz-coder", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	if !stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("no close")
	}
	d := stopEventDetail(t, s.stopEvents("stop-escalated", "zz-coder")[0][6])
	if d["by"] != "zz-director" {
		t.Fatalf("escalated = %v", d)
	}
	sd := stopEventDetail(t, s.stopEvents("stopped", "zz-coder")[0][6])
	if sd["reason"] != "now" {
		t.Fatalf("stopped = %v", sd)
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != 1 {
		t.Fatal("STOP msgs")
	}
	if s.stopField(t, "zz-coder", "stop_requested_at") != before {
		t.Fatal("stop_requested_at changed")
	}
	if len(s.stopEvents("stop-requested", "zz-coder")) != 1 {
		t.Fatal("stop-requested count")
	}
}

func TestStopEscalationCloseFailed(t *testing.T) {
	s := newStopEscalationEnv(t)
	s.SetHerdr(map[string]any{"pane_close_fail": true})
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	stopAssertEscalationFailed(t, s, p, "close-failed")
}

func TestStopEscalationProcessAlive(t *testing.T) {
	s := newStopEscalationEnv(t)
	s.FakeClaude(4242, 1, "claude", "--resume", stopSID)
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, s.stopFastEnv())
	stopAssertEscalationFailed(t, s, p, "process-alive")
}

func TestStopEscalationProcessUnknown(t *testing.T) {
	s := newStopEscalationEnv(t)
	gone := filepath.Join(s.Home, "no-such-proc-dir")
	env := map[string]string{"FACTORY_PROC_DIR": gone}
	for k, v := range s.stopFastEnv() {
		env[k] = v
	}
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 1, env)
	stopAssertEscalationFailed(t, s, p, "process-unknown")
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane closed")
	}
}

func TestStopEscalationPermissionsUnchanged(t *testing.T) {
	s := newStopEscalationEnv(t)
	nCalls := len(s.Calls("herdr"))
	p := s.stop(t, []string{"zz-coder", "--now"}, "zz-lead-b", 3, nil)
	contains(t, "stderr", p.Stderr, "not in your team")
	p = s.stop(t, []string{"zz-coder", "--now"}, "zz-coder-b", 3, nil)
	contains(t, "stderr", p.Stderr, "workers may not run `factory stop`")
	sqlExec(t, s.Env, "UPDATE agents SET status='exited' WHERE name='zz-coder'")
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 2, nil)
	if len(s.stopEvents("stop-escalated", "zz-coder")) != 0 {
		t.Fatal("extra escalated")
	}
	if len(s.Calls("herdr")) != nCalls {
		t.Fatal("herdr calls")
	}
}

func TestStopEscalationDryRunChangesNothing(t *testing.T) {
	s := newStopEscalationEnv(t)
	before := s.stopRow(t, "zz-coder")
	nEvents := s.Count("SELECT count(*) FROM events")
	nCalls := len(s.Calls("herdr"))
	p := s.stop(t, []string{"zz-coder", "--now", "--dry-run"}, "zz-director", 0, nil)
	contains(t, "stdout", p.Stdout, "escalates to the hard fallback now")
	after := s.stopRow(t, "zz-coder")
	for k, v := range before {
		if after[k] != v {
			t.Fatalf("%s changed %q -> %q", k, v, after[k])
		}
	}
	if s.Count("SELECT count(*) FROM events") != nEvents {
		t.Fatal("events changed")
	}
	if len(s.Calls("herdr")) != nCalls {
		t.Fatal("herdr changed")
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != 1 {
		t.Fatal("STOP msgs")
	}
}

func TestStopStaleAttemptGivesSkipAndNoClose(t *testing.T) {
	s := newStopEscalationEnv(t)
	seenAt := s.stopField(t, "zz-coder", "stop_attempt_at")
	seenPy := stopAttemptSeenPy(t, s)
	if seenAt != "" && seenAt != "NULL" {
		seenPy = strconv.Quote(seenAt)
	}
	nCalls := len(s.Calls("herdr"))
	sqlExec(t, s.Env, "UPDATE agents SET stop_attempt_at=? WHERE name='zz-coder'", stopAgoWall(1))
	body := fmt.Sprintf(`
con = mod.db()
try:
    got, note = mod.force_stop(con, "zz-coder", "now", attempt_seen=%s)
    if got != "skip" or note is not None:
        print("FAIL", repr(got), repr(note))
        sys.exit(1)
finally:
    con.close()
`, seenPy)
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if len(s.Calls("herdr")) != nCalls {
		t.Fatal("herdr called")
	}
	if len(s.stopEvents("stopped", "zz-coder")) != 0 || len(s.stopEvents("stop-failed", "zz-coder")) < 1 {
		// stop-failed may exist from setup attempts
	}
}

func TestStopCmdStopSkipsWhenAttemptRacesTheRead(t *testing.T) {
	s := newStopEscalationEnv(t)
	nCalls := len(s.Calls("herdr"))
	body := `
real = mod.force_stop
def racing(con, name, how, attempt_seen=None):
    import datetime as dt
    ago = (dt.datetime.now().astimezone() - dt.timedelta(seconds=1)).isoformat(timespec='seconds')
    con.execute("UPDATE agents SET stop_attempt_at=? WHERE name=?", (ago, name))
    return real(con, name, how, attempt_seen=attempt_seen)
os.environ["FACTORY_AGENT"], os.environ["FACTORY_ROLE"] = "zz-director", "director"
out = __import__("io").StringIO()
import sys
try:
    with mock.patch.object(mod, "force_stop", racing), mock.patch("sys.stdout", out):
        mod.cmd_stop(argparse.Namespace(agent="zz-coder", now=True, dry_run=False, reason=None))
finally:
    os.environ.pop("FACTORY_AGENT", None)
    os.environ.pop("FACTORY_ROLE", None)
want = "another stop attempt ran meanwhile; nothing was closed"
if want not in out.getvalue():
    print("MISSING", out.getvalue())
    sys.exit(1)
`
	out, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatalf("python exit %d out=%s", exit, out)
	}
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if len(s.Calls("herdr")) != nCalls {
		t.Fatal("herdr")
	}
	if len(s.stopEvents("stopped", "zz-coder")) != 0 {
		t.Fatal("stopped")
	}
}

func stopAssertEscalationFailed(t *testing.T, s *stopEnv, p ft.Result, kind string) {
	t.Helper()
	before := s.stopField(t, "zz-coder", "stop_requested_at")
	contains(t, "stderr", p.Stderr, kind)
	if s.stopField(t, "zz-coder", "status") != "live" {
		t.Fatal("not live")
	}
	if s.stopField(t, "zz-coder", "stop_requested_at") != before {
		t.Fatal("stop_requested_at changed")
	}
	if len(s.stopMsgs("zz-coder", "STOP")) != 1 {
		t.Fatal("STOP msgs")
	}
	d := stopEventDetail(t, s.stopEvents("stop-escalated", "zz-coder")[0][6])
	if d["by"] != "zz-director" {
		t.Fatalf("escalated = %v", d)
	}
	fd := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[len(s.stopEvents("stop-failed", "zz-coder"))-1][6])
	if fd["reason"] != kind {
		t.Fatalf("reason = %v", fd["reason"])
	}
	if s.stopField(t, "zz-coder", "stop_failed_kind") != kind {
		t.Fatalf("kind = %s", s.stopField(t, "zz-coder", "stop_failed_kind"))
	}
}

func TestStopReturnsStoppedNone(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	body := `
con = mod.db()
try:
    with mock.patch.object(mod, "_scan_claude", return_value={}), mock.patch.object(mod, "pane_gone", return_value=True):
        got, note = mod.force_stop(con, "zz-coder", "now")
        if got != "stopped" or note is not None:
            print("FAIL", repr(got), repr(note))
            sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
}

func TestStopReturnsUnknownBeforeNote(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	body := `
con = mod.db()
try:
    with mock.patch.object(mod, "_scan_claude", return_value=None):
        got, note = mod.force_stop(con, "zz-coder", "now")
        if got != "failed:process-unknown" or note != mod._UNKNOWN_BEFORE:
            print("FAIL", repr(got), repr(note))
            sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
	d := stopEventDetail(t, s.stopEvents("stop-failed", "zz-coder")[0][6])
	contains(t, "note", d["note"].(string), "The pane was NOT closed")
}

func TestStopReturnsUnknownAfterNote(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	body := `
con = mod.db()
try:
    with mock.patch.object(mod, "_scan_claude", side_effect=[{}, None]), mock.patch.object(mod, "pane_gone", return_value=True):
        got, note = mod.force_stop(con, "zz-coder", "now")
        if got != "failed:process-unknown" or note != mod._UNKNOWN_AFTER:
            print("FAIL", repr(got), repr(note))
            sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
}

func TestStopOtherFailuresHaveNoNote(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	body := `
con = mod.db()
try:
    with mock.patch.object(mod, "_scan_claude", return_value={}), mock.patch.object(mod, "pane_gone", return_value=False):
        got, note = mod.force_stop(con, "zz-coder", "now")
        if got != "failed:close-failed" or note is not None:
            print("FAIL", repr(got), repr(note))
            sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
}

func TestStopSkipReturnsNoneNote(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	body := `
con = mod.db()
try:
    got, note = mod.force_stop(con, "zz-coder", "now", attempt_seen="stale")
    if got != "skip" or note is not None:
        print("FAIL", repr(got), repr(note))
        sys.exit(1)
finally:
    con.close()
`
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("force_stop")
	}
}

func TestStopTwoInterleavedCallersCloseOnePane(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder"}, "zz-lead", 0, nil)
	seen := stopAttemptSeenPy(t, s)
	body := fmt.Sprintf(`
results = []
closes = []
real_herdr = mod.herdr
def herdr(*args, **kw):
    if args[:2] == ("pane", "close"):
        closes.append(args)
        if len(closes) == 1:
            con2 = mod.db()
            try:
                results.append(mod.force_stop(con2, "zz-coder", "now", attempt_seen=%s))
            finally:
                con2.close()
    return real_herdr(*args, **kw)
con = mod.db()
try:
    with mock.patch.object(mod, "herdr", herdr), mock.patch.object(mod, "_scan_claude", return_value={}):
        results.append(mod.force_stop(con, "zz-coder", "now", attempt_seen=%s))
finally:
    con.close()
if len(closes) != 1:
    print("closes", closes); sys.exit(1)
codes = sorted(r[0] for r in results)
if codes != ["skip", "stopped"]:
    print("results", results); sys.exit(1)
`, seen, seen)
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("interleaved")
	}
}

func TestStopCmdStopSecondCallerDoesNotResetClaim(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	claim := stopAgoWall(1)
	body := fmt.Sprintf(`
import datetime as dt
real_cfg = mod.cfg
def cfg(con, key):
    ago2 = (dt.datetime.now().astimezone() - dt.timedelta(seconds=2)).isoformat(timespec='seconds')
    con.execute("UPDATE agents SET stop_requested_at=?, stop_attempt_at=? WHERE name='zz-coder'", (ago2, %s))
    return real_cfg(con, key)
os.environ["FACTORY_AGENT"], os.environ["FACTORY_ROLE"] = "zz-director", "director"
out = __import__("io").StringIO()
try:
    with mock.patch.object(mod, "cfg", cfg), mock.patch("sys.stdout", out):
        mod.cmd_stop(argparse.Namespace(agent="zz-coder", now=True, dry_run=False, reason=None))
finally:
    os.environ.pop("FACTORY_AGENT", None)
    os.environ.pop("FACTORY_ROLE", None)
want = "another stop is already in progress; nothing was closed"
if want not in out.getvalue():
    print("MISSING", out.getvalue()); sys.exit(1)
`, strconv.Quote(claim))
	_, exit := s.stopPython(t, body, nil)
	if exit != 0 {
		t.Fatal("cmd_stop")
	}
	if s.stopField(t, "zz-coder", "stop_attempt_at") != claim {
		t.Fatal("claim reset")
	}
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane closed")
	}
}

func TestStopReusedPaneRowIsReapedPaneUntouchedEventWritten(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	p := s.stopOK(t, "zz-director", "reap-agent", "zz-rev")
	contains(t, "stdout", p.Stdout, "marked stopped")
	r := s.stopRow(t, "zz-rev")
	if r["status"] != "stopped" || r["ended"] == "" || r["ended"] == "NULL" {
		t.Fatalf("row = %v", r)
	}
	if r["pane_id"] != "" && r["pane_id"] != "NULL" {
		t.Fatalf("pane_id = %q", r["pane_id"])
	}
	if s.stopField(t, "zz-lead", "status") != "live" {
		t.Fatal("lead not live")
	}
	ev := s.stopEvents("reaped-reused-pane", "zz-rev")
	if len(ev) != 1 {
		t.Fatalf("events = %d", len(ev))
	}
	d := stopEventDetail(t, ev[0][6])
	if d["pane"] != "p3" || d["pane_host"] != "zz-lead" || d["by"] != "zz-director" {
		t.Fatalf("detail = %v", d)
	}
	if stopHasVerb(s.stopHerdrVerbs(), [2]string{"pane", "close"}) {
		t.Fatal("pane close")
	}
	if _, err := os.Stat(filepath.Join(s.ProcDir, "4300")); err != nil {
		t.Fatal("proc removed")
	}
}

func TestStopDryRunChangesNothingReap(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	p := s.stopOK(t, "zz-director", "reap-agent", "zz-rev", "--dry-run")
	contains(t, "stdout", p.Stdout, "DRY RUN")
	if s.stopField(t, "zz-rev", "status") != "live" {
		t.Fatal("status changed")
	}
	if len(s.stopEvents("reaped-reused-pane", "zz-rev")) != 0 {
		t.Fatal("event written")
	}
}

func TestStopAgentWithLiveProcessIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.FakeClaude(4301, 1, "claude", "--resume", "other-sid")
	s.stopSetEnviron(t, 4301, "zz-rev")
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "4301")
}

func TestStopAgentStillListedByHostIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.stopHerdrAgents(t, [][2]string{{"zz-rev", "p3"}, {"zz-lead", "p4"}})
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "still listed")
}

func TestStopPaneWithoutAnotherAgentIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.stopHerdrAgents(t, [][2]string{{"zz-coder", "p1"}})
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "does not host a different registered agent")
}

func TestStopPaneHostingUnregisteredNameIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.stopHerdrAgents(t, [][2]string{{"zz-ghost", "p3"}})
	stopReapRefused(t, s, 2)
}

func TestStopUnreadableEnvironOfSessionClaudeFailsClosed(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	d := filepath.Join(s.ProcDir, "4300", "environ")
	os.Remove(d)
	os.Mkdir(d, 0o755)
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "cannot read")
}

func TestStopWorkerCallerIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	stopReapRefusedAgent(t, s, 3, "zz-coder")
}

func TestStopLeadCallerIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	stopReapRefusedAgent(t, s, 3, "zz-lead")
}

func TestStopRowWithoutSessionIdFailsClosed(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	sqlExec(t, s.Env, "UPDATE agents SET session_id=NULL WHERE name='zz-rev'")
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "no session id")
}

func TestStopRowWithoutPaneIdIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	sqlExec(t, s.Env, "UPDATE agents SET pane_id=NULL WHERE name='zz-rev'")
	p := stopReapRefused(t, s, 2)
	contains(t, "stderr", p.Stderr, "no pane id")
}

func TestStopUnknownAgentIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.stopRefused(t, 2, "zz-director", "reap-agent", "zz-nobody")
}

func TestStopOwnerCanReapAndEventCarriesEvidence(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	s.stopOK(t, "owner", "reap-agent", "zz-rev")
	if s.stopField(t, "zz-rev", "status") != "stopped" {
		t.Fatal("not stopped")
	}
	d := stopEventDetail(t, s.stopEvents("reaped-reused-pane", "zz-rev")[0][6])
	if d["pane_host_status"] != "live" || d["session_id"] != stopRev || d["was"] != "live" {
		t.Fatalf("detail = %v", d)
	}
}

func TestStopNonLiveRowIsRefused(t *testing.T) {
	s := newReapAgentEnv(t)
	s.NeedPython()
	sqlExec(t, s.Env, "UPDATE agents SET status='stopped' WHERE name='zz-rev'")
	s.stopRefused(t, 2, "zz-director", "reap-agent", "zz-rev")
}

func stopReapRefused(t *testing.T, s *stopEnv, code int) ft.Result {
	t.Helper()
	p := s.stopRefused(t, code, "zz-director", "reap-agent", "zz-rev")
	if s.stopField(t, "zz-rev", "status") != "live" {
		t.Fatal("status changed")
	}
	if len(s.stopEvents("reaped-reused-pane", "zz-rev")) != 0 {
		t.Fatal("event")
	}
	return p
}

func stopReapRefusedAgent(t *testing.T, s *stopEnv, code int, agent string) ft.Result {
	t.Helper()
	p := s.stopRefused(t, code, agent, "reap-agent", "zz-rev")
	if s.stopField(t, "zz-rev", "status") != "live" {
		t.Fatal("status changed")
	}
	if len(s.stopEvents("reaped-reused-pane", "zz-rev")) != 0 {
		t.Fatal("event")
	}
	return p
}

// T-1616 (F22): a STOP order left unread by a dead pane must not stop the next agent spawned under that name.

func (s *stopEnv) stopRespawn(t *testing.T) {
	t.Helper()
	s.stopOK(t, "zz-director", "spawn", "coder", "--goal", "G-001", "--team", "g-001-a", "--task", "T-0001", "--name", "zz-coder", "--cwd", s.Home)
}

func (s *stopEnv) stopInbox(t *testing.T, args ...string) string {
	t.Helper()
	return s.stopOK(t, "zz-coder", append([]string{"inbox"}, args...)...).Stdout
}

func isNull(v string) bool { return v == "" || v == "NULL" }

func (s *stopEnv) stopStaleEvents() [][]string {
	return s.SQL("SELECT detail FROM events WHERE action='stop-stale-dropped' AND entity_id='zz-coder'")
}

func (s *stopEnv) stopStaleRow(t *testing.T, created, started string) string {
	t.Helper()
	sqlExec(t, s.Env, "UPDATE agents SET started=? WHERE name='zz-coder'", started)
	sqlExec(t, s.Env, "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
		"zz-lead", "zz-coder", "decision", "STOP", "pane hung\nStop work now. Push, then run factory done", created)
	rows := s.SQL("SELECT id FROM messages WHERE recipient='zz-coder' AND subject='STOP' ORDER BY id DESC LIMIT 1")
	return rows[0][0]
}

func TestStopRespawnDropsStaleStopOrder(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stop(t, []string{"zz-coder", "--now", "--reason", "old pane stuck"}, "zz-director", 0, nil)
	old := s.SQL("SELECT kind, read_at FROM messages WHERE recipient='zz-coder' AND subject='STOP'")
	if len(old) != 1 || old[0][0] != "decision" || !isNull(old[0][1]) {
		t.Fatalf("old STOP = %v, want one unread decision", old)
	}
	s.stopRespawn(t)
	m := s.SQL("SELECT id, read_at FROM messages WHERE recipient='zz-coder' AND subject='STOP'")
	if len(m) != 1 || isNull(m[0][1]) {
		t.Fatalf("STOP after respawn = %v, want one read", m)
	}
	ev := s.stopStaleEvents()
	if len(ev) != 1 {
		t.Fatalf("stop-stale-dropped events = %v", ev)
	}
	d := stopEventDetail(t, ev[0][0])
	if d["agent"] != "zz-coder" || fmt.Sprint(d["message"]) != m[0][0] || d["by"] != "spawn" {
		t.Fatalf("event detail = %v", d)
	}
	out := s.stopInbox(t)
	contains(t, "inbox", out, "kickoff")
	for _, bad := range []string{"STOP", "old pane stuck"} {
		if strings.Contains(out, bad) {
			t.Fatalf("inbox shows %q:\n%s", bad, out)
		}
	}
}

func TestStopRespawnKeepsNonStopMailAndLaterStopIsDelivered(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	s.stopOK(t, "zz-lead", "msg", "zz-coder", "rebase note", "rebase on main first")
	s.stopOK(t, "zz-lead", "msg", "zz-coder", "scope", "keep the flag", "--kind", "decision")
	s.stop(t, []string{"zz-coder", "--now"}, "zz-director", 0, nil)
	s.stopRespawn(t)
	unread := map[string]bool{}
	for _, r := range s.SQL("SELECT subject FROM messages WHERE recipient='zz-coder' AND read_at IS NULL") {
		unread[r[0]] = true
	}
	if !unread["rebase note"] || !unread["scope"] || unread["STOP"] {
		t.Fatalf("unread after respawn = %v", unread)
	}
	s.stopHerdrAgents(t, [][2]string{{"zz-coder", s.stopField(t, "zz-coder", "pane_id")}})
	s.stop(t, []string{"zz-coder", "--reason", "new order"}, "zz-lead", 0, nil)
	live := s.SQL("SELECT id FROM messages WHERE recipient='zz-coder' AND subject='STOP' AND read_at IS NULL")
	if len(live) != 1 {
		t.Fatalf("live STOP = %v, want 1", live)
	}
	if n := len(s.stopStaleEvents()); n != 1 {
		t.Fatalf("stop-stale-dropped = %d, want 1", n)
	}
	out := s.stopInbox(t)
	contains(t, "inbox", out, "   STOP\n")
	contains(t, "inbox", out, "new order\n   Stop work now.")
	if strings.Contains(out, "[stale") {
		t.Fatalf("new STOP marked stale:\n%s", out)
	}
	contains(t, "inbox", out, "rebase on main first")
	if n := len(s.stopStaleEvents()); n != 1 {
		t.Fatalf("stop-stale-dropped after inbox = %d, want 1", n)
	}
}

func TestStopInboxDeliversStopSentAfterStartOrToAnotherKind(t *testing.T) {
	s := newStopEnv(t)
	s.NeedPython()
	// other timezone offset, later instant: compared as instants, not as text
	s.stopStaleRow(t, "2026-03-01T12:05:01+02:00", "2026-03-01T10:05:00+00:00")
	sqlExec(t, s.Env, "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
		"zz-lead", "zz-coder", "msg", "STOP", "not a decision", "2026-03-01T09:00:00+00:00")
	sqlExec(t, s.Env, "INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)",
		"zz-lead", "zz-coder", "decision", "STOP", "unreadable time", "not-a-time")
	out := s.stopInbox(t)
	if strings.Contains(out, "[stale") {
		t.Fatalf("stale marker:\n%s", out)
	}
	contains(t, "inbox", out, "Stop work now")
	contains(t, "inbox", out, "not a decision")
	contains(t, "inbox", out, "unreadable time")
	if len(s.stopStaleEvents()) != 0 {
		t.Fatal("dropped an order it could not decide")
	}
}
