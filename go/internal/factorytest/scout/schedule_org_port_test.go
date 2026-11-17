package scout

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Port of tests/test_scout_schedule_org.py (10 cases; see ported_scout.txt): the scout schedule on a post-G-028
// install (org_prefix_default=1: the scout's agents row is claude-scout). It reuses the Base of the schedule port
// (rpSch). `tick`, `config`, `scout runs|report`, `inbox` and `done` are handed to bin/factory by the Go CLI, so
// every case calls NeedPython (through rpSchNew) and skips where python3 is absent.
// not ported: none.

const rpOrgScout = "claude-scout"

func rpOrgNew(t *testing.T) *rpSch {
	t.Helper()
	b := rpSchNew(t)
	b.ok("config", "org_prefix_default", "1")
	b.scout = rpOrgScout
	return b
}

func TestScoutScheduleOrgRunFollowsTheOrgPrefixedScoutRowAndIsNotFailedAsMissing(t *testing.T) {
	b := rpOrgNew(t)
	b.tick(rpAt("09:01"))
	eqStrings(t, "starts", b.startNames(), []string{rpOrgScout})
	b.pin(rpAt("09:01"), "")
	b.tick(rpAt("09:05"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "running" || rpStr(runs[0]["agent"]) != rpOrgScout || runs[0]["stop_reason"] != nil {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgWallBudgetStopsTheOrgPrefixedScout(t *testing.T) {
	b := rpOrgNew(t)
	b.spawned()
	b.tick(rpAt("09:21"))
	rpEq(t, "agent status", b.agent()["status"], "stopped")
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != "wall" {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgTokenBudgetStopsTheOrgPrefixedScoutAndARealCountIsRecorded(t *testing.T) {
	b := rpOrgNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 200000, 100000, 1, 0)
	b.tick(rpAt("09:06"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != "tokens" || rpStr(runs[0]["tokens"]) != "300001" {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgFinishedScoutRecordsItsRealTokenCount(t *testing.T) {
	b := rpOrgNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 10, 20, 30, 0)
	b.finish()
	b.tick(rpAt("09:10"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "done" || rpStr(runs[0]["tokens"]) != "60" {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgALegacyCursorScoutRowDoesNotPullTheScheduledScoutOntoTheCursorOrg(t *testing.T) {
	b := rpOrgNew(t)
	// live 2026-11-01: agents row 'scout' org=cursor runtime=cursor, status stopped (the run before)
	args, err := json.Marshal(map[string]string{"role": "scout", "name": "scout", "org": "cursor", "runtime": "cursor"})
	if err != nil {
		t.Fatal(err)
	}
	b.sql("INSERT INTO agents(name,role,status,spawn_args,org) VALUES('scout','scout','stopped',?,'cursor')", string(args))
	b.tick(rpAt("09:01"))
	eqStrings(t, "starts", b.startNames(), []string{rpOrgScout})
	row := b.agent()
	if rpStr(row["org"]) != "claude" || rpStr(row["status"]) != "live" {
		t.Fatalf("row = %v", row)
	}
	var sa map[string]any
	if err := json.Unmarshal([]byte(rpStr(row["spawn_args"])), &sa); err != nil {
		t.Fatal(err)
	}
	if rt, ok := sa["runtime"]; ok && rt != "claude" {
		t.Fatalf("spawn_args runtime = %v, want claude", rt)
	}
	ev := b.events("spawn")
	notContains(t, "spawn event", rpStr(ev[len(ev)-1]["detail"]), "cursor") // live: spawn event runtime=cursor kind=cursor, no Claude session id
	b.pin(rpAt("09:01"), "")
	b.usageLog(rpStr(row["session_id"]), 5, 6, 7, 0)
	b.finish()
	b.tick(rpAt("09:10"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["agent"]) != rpOrgScout || rpStr(runs[0]["status"]) != "done" || rpStr(runs[0]["tokens"]) != "18" {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgUnreadableSessionLogStoresNullNotZero(t *testing.T) {
	b := rpOrgNew(t)
	b.spawned() // no usage log written for its session
	b.finish()
	b.tick(rpAt("09:10"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "done" || runs[0]["tokens"] != nil {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgALogWithNoUsageLineIsUnknownToo(t *testing.T) {
	b := rpOrgNew(t)
	sid := rpStr(b.spawned()["session_id"])
	d := filepath.Join(b.e.UserHome, ".claude", "projects", "-zz-proj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"content": "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, sid+".jsonl"), append(line, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	b.finish()
	b.tick(rpAt("09:10"))
	if v := b.runs()[0]["tokens"]; v != nil {
		t.Fatalf("tokens = %v, want NULL", v)
	}
}

func TestScoutScheduleOrgALogThatShowsZeroUsageIsARealZero(t *testing.T) {
	b := rpOrgNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 0, 0, 0, 0)
	b.finish()
	b.tick(rpAt("09:10"))
	rpEq(t, "tokens", b.runs()[0]["tokens"], "0")
}

func TestScoutScheduleOrgUnknownTokensDoNotTripTheTokenBudgetAndTheWallStillDoes(t *testing.T) {
	b := rpOrgNew(t)
	b.spawned()
	b.tick(rpAt("09:06"))
	rpEq(t, "status", b.runs()[0]["status"], "running") // unreadable log: not over budget
	b.tick(rpAt("09:21"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != "wall" || runs[0]["tokens"] != nil {
		t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleOrgScoutRunsPrintsADashAndTheSkillScoutReportRenders(t *testing.T) {
	b := rpOrgNew(t)
	b.spawned()
	b.finish()
	b.tick(rpAt("09:10"))
	out := b.okAs("owner", "scout", "runs").Stdout
	contains(t, "scout runs", out, "tokens=- ")
	notContains(t, "scout runs", out, "None")
	b.okAs("owner", "scout", "report")
}
