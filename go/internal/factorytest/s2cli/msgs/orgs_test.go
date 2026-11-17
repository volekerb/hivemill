// Go tests for tests/test_orgs.py: all 40 cases of "Ran 40" (32 def test_ plus the 8 inherited reruns of the four RoutingTests
// under LeadBareHqRoutingTests and OrgPrefixDefaultRoutingTests, each a top-level TestOrgsLeadBareHq*/TestOrgsOrgPrefixDefault* test).
// Nothing is not ported. spawn/up/tick alert paths use NeedPython; msg routing uses the Go CLI.
package s2msgs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/scout"
)

func TestOrgsModelsPerOrg(t *testing.T) {
	if m, _ := scout.Model("claude", "director"); m != "claude-opus-5-5" {
		t.Fatal(m)
	}
	if m, _ := scout.Model("cursor", "coder"); m != "composer-2.5" {
		t.Fatal(m)
	}
	for _, role := range append(append([]string{}, scout.JudgementRoles...), scout.BuildRoles...) {
		for _, org := range []string{"cursor", "claude"} {
			m, _ := scout.Model(org, role)
			if strings.Contains(m, "fable") {
				t.Fatalf("fable in %s %s", org, role)
			}
		}
	}
	var claudeAnthropic []string
	for role, model := range scout.Orgs["cursor"].Models {
		if strings.HasPrefix(model, "claude-") {
			claudeAnthropic = append(claudeAnthropic, role)
		}
	}
	if len(claudeAnthropic) != 1 || claudeAnthropic[0] != "evaluator" {
		t.Fatalf("cursor claude models: %v", claudeAnthropic)
	}
}

func TestOrgsNames(t *testing.T) {
	n, _ := scout.OrgName("cursor", "coder-t-0001")
	if n != "cursor-coder-t-0001" {
		t.Fatal(n)
	}
	n, _ = scout.OrgName("cursor", "cursor-coder-t-0001")
	if n != "cursor-coder-t-0001" {
		t.Fatal(n)
	}
	if scout.PrefixOf("claude-boss") != "claude" || scout.PrefixOf("boss") != "" {
		t.Fatal("prefix")
	}
	if scout.StripPrefix("cursor-lead-g-001-a") != "lead-g-001-a" {
		t.Fatal("strip")
	}
	if _, err := scout.CheckOrg("codex"); err == nil {
		t.Fatal("expected error")
	}
}

func TestOrgsOfAgent(t *testing.T) {
	if scout.OfAgent(scout.AgentRow{Org: "cursor", Name: "x"}) != "cursor" {
		t.Fatal()
	}
	if scout.OfAgent(scout.AgentRow{Name: "cursor-boss"}) != "cursor" {
		t.Fatal()
	}
	sa, _ := json.Marshal(map[string]string{"runtime": "cursor"})
	if scout.OfAgent(scout.AgentRow{Name: "lead-g-1", SpawnArgs: string(sa)}) != "cursor" {
		t.Fatal()
	}
	if scout.OfAgent(scout.AgentRow{Name: "boss"}) != "claude" {
		t.Fatal()
	}
}

func orgGoalEnv(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedAgent(ft.AgentRow{Name: "boss", Role: "boss"})
	e.SeedAgent(ft.AgentRow{Name: "director", Role: "director"})
	return e
}

func TestOrgsGoalNewOrgListAndMove(t *testing.T) {
	e := orgGoalEnv(t)
	gid := strings.TrimSpace(ok(t, e, "boss", nil, "goal", "new", "--title", "zz-test cursor goal", "--org", "cursor").Stdout)
	if sql1(t, e, "SELECT org FROM goals WHERE id=?", gid) != "cursor" {
		t.Fatal()
	}
	contains(t, "list", ok(t, e, "director", nil, "goal", "list", "--org", "cursor").Stdout, gid)
	notContains(t, "list claude", ok(t, e, "director", nil, "goal", "list", "--org", "claude").Stdout, gid)
	contains(t, "show", ok(t, e, "director", nil, "goal", "show", gid).Stdout, "org=cursor")
	ok(t, e, "director", nil, "goal", "org", gid, "claude")
	if sql1(t, e, "SELECT org FROM goals WHERE id=?", gid) != "claude" {
		t.Fatal()
	}
	refused(t, e, "director", 2, nil, "goal", "org", gid, "codex")
}

func TestOrgsGoalNewDefaultsToClaude(t *testing.T) {
	e := orgGoalEnv(t)
	e.NeedPython()
	gid := strings.TrimSpace(ok(t, e, "boss", nil, "goal", "new", "--title", "zz-test default goal").Stdout)
	if sql1(t, e, "SELECT org FROM goals WHERE id=?", gid) != "claude" {
		t.Fatal()
	}
}

func TestOrgsUnknownOrgRefused(t *testing.T) {
	e := orgGoalEnv(t)
	refused(t, e, "boss", 2, nil, "goal", "new", "--title", "zz-test bad", "--org", "codex")
}

func routingEnv(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedGoal("G-001", "", "g1")
	e.SeedGoal("G-002", "", "g2")
	sqlExec(t, e, "UPDATE goals SET org='cursor' WHERE id='G-002'")
	e.SeedTeam("g-001-a", "G-001", "lead-g-001-a")
	e.SeedTeam("g-002-a", "G-002", "cursor-lead-g-002-a")
	for _, row := range []struct{ name, role, goal, team string }{
		{"boss", "boss", "", ""}, {"director", "director", "", ""},
		{"cursor-boss", "boss", "", ""}, {"cursor-director", "director", "", ""},
		{"lead-g-001-a", "lead", "G-001", "g-001-a"}, {"cursor-lead-g-002-a", "lead", "G-002", "g-002-a"},
	} {
		e.SeedAgent(ft.AgentRow{Name: row.name, Role: row.role, Goal: row.goal, Team: row.team})
	}
	sqlExec(t, e, "UPDATE agents SET org='cursor' WHERE name LIKE 'cursor-%'")
	sqlExec(t, e, "UPDATE agents SET org='claude' WHERE org IS NULL")
	return e
}

func lastRcpt(t *testing.T, e *ft.Env) string {
	t.Helper()
	return sql1(t, e, "SELECT recipient FROM messages ORDER BY id DESC LIMIT 1")
}

func bareRolesBody(t *testing.T, e *ft.Env) {
	ok(t, e, "cursor-lead-g-002-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
	ok(t, e, "lead-g-001-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "director" {
		t.Fatal()
	}
	ok(t, e, "cursor-director", nil, "msg", "owner", "zz-test s", "b")
	if lastRcpt(t, e) != "cursor-boss" {
		t.Fatal()
	}
}

func TestOrgsBareRolesResolveInsideSenderOrg(t *testing.T) { bareRolesBody(t, routingEnv(t)) }

func TestOrgsLeadBareHqBareRolesResolveInsideSenderOrg(t *testing.T) { bareRolesBody(t, routingEnv(t)) }

func TestOrgsOrgPrefixDefaultBareRolesResolveInsideSenderOrg(t *testing.T) {
	needPy(t)
	bareRolesBody(t, orgPrefixRoutingEnv(t))
}

func goalRoutesBody(t *testing.T, e *ft.Env) {
	ok(t, e, "director", nil, "msg", "boss", "zz-test s", "b", "--goal", "G-002")
	if lastRcpt(t, e) != "cursor-boss" {
		t.Fatal()
	}
}

func TestOrgsGoalRoutesToGoalOrg(t *testing.T) { goalRoutesBody(t, routingEnv(t)) }

func TestOrgsLeadBareHqGoalRoutesToGoalOrg(t *testing.T) { goalRoutesBody(t, routingEnv(t)) }

func TestOrgsOrgPrefixDefaultGoalRoutesToGoalOrg(t *testing.T) {
	needPy(t)
	goalRoutesBody(t, orgPrefixRoutingEnv(t))
}

func directorsTalkBody(t *testing.T, e *ft.Env) {
	ok(t, e, "director", nil, "msg", "cursor-director", "zz-test capacity", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
	ok(t, e, "cursor-director", nil, "msg", "director", "zz-test ack", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
	ok(t, e, "cursor-boss", nil, "msg", "director", "zz-test fyi", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
}

func TestOrgsDirectorsTalkAcrossOrgs(t *testing.T) { directorsTalkBody(t, routingEnv(t)) }

func TestOrgsLeadBareHqDirectorsTalkAcrossOrgs(t *testing.T) { directorsTalkBody(t, routingEnv(t)) }

func TestOrgsOrgPrefixDefaultDirectorsTalkAcrossOrgs(t *testing.T) {
	needPy(t)
	directorsTalkBody(t, orgPrefixRoutingEnv(t))
}

func leadMayNotCrossBody(t *testing.T, e *ft.Env) {
	p := refused(t, e, "cursor-lead-g-002-a", 3, nil, "msg", "lead-g-001-a", "zz-test s", "b")
	contains(t, "stderr", p.Stderr, "only the boss and director talk across orgs")
	p = refused(t, e, "lead-g-001-a", 3, nil, "msg", "cursor-director", "zz-test s", "b")
	contains(t, "stderr", p.Stderr, "across orgs")
}

func TestOrgsLeadMayNotCrossOrgs(t *testing.T) { leadMayNotCrossBody(t, routingEnv(t)) }

func TestOrgsLeadBareHqLeadMayNotCrossOrgs(t *testing.T) { leadMayNotCrossBody(t, routingEnv(t)) }

func TestOrgsOrgPrefixDefaultLeadMayNotCrossOrgs(t *testing.T) {
	needPy(t)
	leadMayNotCrossBody(t, orgPrefixRoutingEnv(t))
}

func TestOrgsClaudeLeadBareHqWithCursorGoalStaysInOwnOrg(t *testing.T) {
	e := routingEnv(t)
	for _, tc := range []struct{ to, want string }{
		{"director", "director"}, {"boss", "boss"}, {"owner", "boss"},
	} {
		ok(t, e, "lead-g-001-a", nil, "msg", tc.to, "zz-test s", "b", "--goal", "G-002")
		if lastRcpt(t, e) != tc.want {
			t.Fatalf("%s -> %s", tc.to, lastRcpt(t, e))
		}
	}
}

func TestOrgsCursorLeadBareDirectorWithClaudeGoalStaysInCursor(t *testing.T) {
	e := routingEnv(t)
	ok(t, e, "cursor-lead-g-002-a", nil, "msg", "director", "zz-test s", "b", "--goal", "G-001")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
}

func TestOrgsWorkerBareDirectorWithOtherOrgGoalStaysInOwnOrg(t *testing.T) {
	e := routingEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	sqlExec(t, e, "UPDATE agents SET org='claude' WHERE name='zz-test-coder'")
	ok(t, e, "zz-test-coder", nil, "msg", "director", "zz-test s", "b", "--goal", "G-002")
	if lastRcpt(t, e) != "director" {
		t.Fatal()
	}
}

func TestOrgsExplicitOtherOrgAgentStillRefused(t *testing.T) {
	e := routingEnv(t)
	p := refused(t, e, "lead-g-001-a", 3, nil, "msg", "cursor-director", "zz-test s", "b", "--goal", "G-002")
	contains(t, "stderr", p.Stderr, "across orgs")
	p = refused(t, e, "lead-g-001-a", 3, nil, "msg", "cursor-lead-g-002-a", "zz-test s", "b", "--goal", "G-001")
	contains(t, "stderr", p.Stderr, "across orgs")
}

func TestOrgsDirectorWithCursorGoalStillResolvesToCursorDirector(t *testing.T) {
	e := routingEnv(t)
	ok(t, e, "director", nil, "msg", "director", "zz-test s", "b", "--goal", "G-002")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
}

func TestOrgsLeadReportToDirectorWithCursorGoalStaysInOwnOrg(t *testing.T) {
	e := routingEnv(t)
	ok(t, e, "lead-g-001-a", nil, "report", "zz-test h", "b", "--goal", "G-002")
	if lastRcpt(t, e) != "director" {
		t.Fatal()
	}
}

func orgPrefixRoutingEnv(t *testing.T) *ft.Env {
	e := routingEnv(t)
	ok(t, e, "director", nil, "config", "org_prefix_default", "1")
	return e
}

func TestOrgsDefaultOrgBareHQReachesLegacyRow(t *testing.T) {
	needPy(t)
	e := orgPrefixRoutingEnv(t)
	sqlExec(t, e, "DELETE FROM agents WHERE name IN ('claude-director', 'claude-boss')")
	ok(t, e, "lead-g-001-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "director" {
		t.Fatal()
	}
	ok(t, e, "director", nil, "msg", "boss", "zz-test s", "b")
	if lastRcpt(t, e) != "boss" {
		t.Fatal()
	}
}

func TestOrgsLivePrefixedHQWinsOverLegacy(t *testing.T) {
	needPy(t)
	e := orgPrefixRoutingEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "claude-director", Role: "director"})
	e.SeedAgent(ft.AgentRow{Name: "claude-boss", Role: "boss"})
	ok(t, e, "lead-g-001-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "claude-director" {
		t.Fatal()
	}
}

func TestOrgsStoppedPrefixedRowFallsBackToLegacy(t *testing.T) {
	needPy(t)
	e := orgPrefixRoutingEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "claude-director", Role: "director", Status: "stopped"})
	ok(t, e, "lead-g-001-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "director" {
		t.Fatal()
	}
}

func TestOrgsCursorHQUnchangedUnderOrgPrefixDefault(t *testing.T) {
	needPy(t)
	e := orgPrefixRoutingEnv(t)
	ok(t, e, "cursor-lead-g-002-a", nil, "msg", "director", "zz-test s", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
	ok(t, e, "director", nil, "msg", "cursor-director", "zz-test s", "b")
	if lastRcpt(t, e) != "cursor-director" {
		t.Fatal()
	}
	ok(t, e, "director", nil, "msg", "cursor-boss", "zz-test s", "b")
	if lastRcpt(t, e) != "cursor-boss" {
		t.Fatal()
	}
}

func spawnOrgEnv(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "g1")
	e.SeedGoal("G-002", "", "g2")
	sqlExec(t, e, "UPDATE goals SET org='cursor' WHERE id='G-002'")
	e.SeedTeam("g-002-a", "G-002", "cursor-lead-g-002-a")
	e.SeedAgent(ft.AgentRow{Name: "director", Role: "director"})
	agent := filepath.Join(e.StubBin, "agent")
	os.WriteFile(agent, []byte("#!/bin/sh\necho CURSOR_OK\n"), 0o755)
	return e
}

func TestOrgsCursorGoalSpawnPrefixRuntimeModels(t *testing.T) {
	e := spawnOrgEnv(t)
	e.NeedPython()
	p := ok(t, e, "director", nil, "spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", "cursor-pm-g-002-a")
	contains(t, "stdout", p.Stdout, "SPAWNED cursor-pm-g-002-a")
	row := e.SQL("SELECT org, model, spawn_args FROM agents WHERE name='cursor-pm-g-002-a'")[0]
	if row[0] != "cursor" {
		t.Fatal(row)
	}
	var sa map[string]any
	json.Unmarshal([]byte(row[2]), &sa)
	if sa["runtime"] != "cursor" || sa["org"] != "cursor" {
		t.Fatal(sa)
	}
	starts := e.HerdrStarts()
	if len(starts) == 0 {
		t.Fatal("no start")
	}
	argv := starts[len(starts)-1]
	if indexOf(argv, "--kind") < 0 || argv[indexOf(argv, "--kind")+1] != "cursor" {
		t.Fatal(argv)
	}
	if indexOf(argv, "--model") < 0 || argv[indexOf(argv, "--model")+1] != "grok-4.7-high" {
		t.Fatal(argv)
	}
	prompt, _ := os.ReadFile(filepath.Join(e.Home, "run", "cursor-pm-g-002-a.prompt.md"))
	contains(t, "prompt", string(prompt), "**Cursor org**")
	contains(t, "prompt", string(prompt), "`cursor-boss`")
}

func TestOrgsClaudeGoalSpawnUnchanged(t *testing.T) {
	e := spawnOrgEnv(t)
	e.NeedPython()
	p := ok(t, e, "director", nil, "spawn", "pm", "--goal", "G-001", "--name", "pm-g-001-a")
	contains(t, "stdout", p.Stdout, "SPAWNED pm-g-001-a")
	row := e.SQL("SELECT org, spawn_args FROM agents WHERE name='pm-g-001-a'")[0]
	if row[0] != "claude" {
		t.Fatal()
	}
	var sa map[string]any
	json.Unmarshal([]byte(row[1]), &sa)
	if sa["runtime"] != "claude" {
		t.Fatal(sa)
	}
}

func TestOrgsOrgPrefixDefaultOn(t *testing.T) {
	e := spawnOrgEnv(t)
	e.NeedPython()
	ok(t, e, "director", nil, "config", "org_prefix_default", "1")
	p := ok(t, e, "director", nil, "spawn", "pm", "--goal", "G-001", "--name", "claude-pm-g-001-a")
	contains(t, "stdout", p.Stdout, "SPAWNED claude-pm-g-001-a")
}

func TestOrgsResumeDroppedWhenRuntimeChanges(t *testing.T) {
	e := spawnOrgEnv(t)
	e.NeedPython()
	e.SeedTeam("g-001-a", "G-001", "lead-g-001-a")
	e.SeedAgent(ft.AgentRow{Name: "lead-g-001-a", Role: "lead", Goal: "G-001", Team: "g-001-a", Status: "stopped"})
	sa, _ := json.Marshal(map[string]string{"runtime": "cursor"})
	sqlExec(t, e, "UPDATE agents SET session_id='cursor-sess-1', spawn_args=? WHERE name='lead-g-001-a'", string(sa))
	ok(t, e, "", nil, "config", "org_prefix_default", "1")
	ok(t, e, "", nil, "up", "--no-boss")
	row := e.SQL("SELECT spawn_args, session_id FROM agents WHERE name='claude-lead-g-001-a'")[0]
	var args map[string]any
	json.Unmarshal([]byte(row[0]), &args)
	if args["runtime"] != "claude" || row[1] == "cursor-sess-1" {
		t.Fatal(row)
	}
	if sql1(t, e, "SELECT lead_agent FROM teams WHERE id='g-001-a'") != "claude-lead-g-001-a" {
		t.Fatal()
	}
	for _, a := range e.HerdrStarts() {
		for _, x := range a {
			if x == "cursor-sess-1" {
				t.Fatal("resumed cursor session")
			}
		}
	}
}

func TestOrgsSpawnOrgMustMatchGoal(t *testing.T) {
	e := spawnOrgEnv(t)
	e.NeedPython()
	refused(t, e, "director", 2, nil, "spawn", "pm", "--goal", "G-001", "--org", "cursor")
}

func goalAlertEnv(t *testing.T) *ft.Env {
	t.Helper()
	slot(t)
	e := ft.New(t)
	e.SeedGoal("G-001", "", "g")
	e.SeedTeam("g-001-a", "G-001", "claude-lead-g-001-a")
	e.SeedAgent(ft.AgentRow{Name: "claude-lead-g-001-a", Role: "lead", Goal: "G-001", Team: "g-001-a", Status: "live"})
	sqlExec(t, e, "UPDATE agents SET org='claude' WHERE name='claude-lead-g-001-a'")
	e.SeedAgent(ft.AgentRow{Name: "cursor-director", Role: "director"})
	sqlExec(t, e, "UPDATE agents SET org='cursor' WHERE name='cursor-director'")
	e.SeedAgent(ft.AgentRow{Name: "claude-director", Role: "director"})
	sqlExec(t, e, "UPDATE agents SET org='claude' WHERE name='claude-director'")
	e.SeedTask(ft.Task{ID: "T-0001", Goal: "G-001", Status: "in_progress", Team: "g-001-a"})
	sqlExec(t, e, "UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
	ok(t, e, "", nil, "config", "red_pr_owner_alert", "1")
	sha := strings.Repeat("a", 40)
	seedPRStub(t, e, ft.PRURL, sha, []map[string]string{{"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"}})
	// the tick probes the PR over REST (gh api pulls/N, check-runs, status), as the Python gh stub answers from gh.json
	appendGhRules(t, e,
		ft.StubRule{Match: []string{"api", "repos/zz-test/repo/pulls/1"}, Out: `{"number": 1, "state": "open", "head": {"sha": "` + sha + `"}, "mergeable_state": "clean"}`},
		ft.StubRule{Match: []string{"api", "commits/" + sha + "/check-runs"}, Out: `{"check_runs": [{"name": "build", "status": "completed", "conclusion": "failure"}], "total_count": 1}`},
		ft.StubRule{Match: []string{"api", "commits/" + sha + "/status"}, Out: `{"state": "success", "statuses": []}`},
		ft.StubRule{Match: []string{"pr", "list"}, Out: "[]\n"})
	return e
}

func redPrMails(t *testing.T, e *ft.Env) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, r := range e.SQL("SELECT recipient FROM messages WHERE subject LIKE 'red PR%'") {
		out[r[0]] = struct{}{}
	}
	return out
}

func TestOrgsReviewNoVerdictAlertOnCursorGoal(t *testing.T) {
	needPy(t)
	e := goalAlertEnv(t)
	pyReviewAlert(t, e, "cursor", "g-001-a", strings.Repeat("a", 40))
	want := map[string]struct{}{"cursor-director": {}}
	got := map[string]struct{}{}
	for _, r := range e.SQL("SELECT recipient FROM messages WHERE subject LIKE 'review autospawn: qa ended%'") {
		got[r[0]] = struct{}{}
	}
	if !reflectSetEq(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestOrgsReviewNoVerdictAlertOnClaudeGoal(t *testing.T) {
	needPy(t)
	e := goalAlertEnv(t)
	pyReviewAlert(t, e, "claude", "g-001-a", strings.Repeat("a", 40))
	got := map[string]struct{}{}
	for _, r := range e.SQL("SELECT recipient FROM messages WHERE subject LIKE 'review autospawn: qa ended%'") {
		got[r[0]] = struct{}{}
	}
	if !reflectSetEq(got, map[string]struct{}{"claude-lead-g-001-a": {}}) {
		t.Fatalf("got %v", got)
	}
}

func blockedMails(t *testing.T, e *ft.Env, goalOrg string, want map[string]struct{}) {
	t.Helper()
	tickReady(t, e)
	sqlExec(t, e, "UPDATE goals SET org=? WHERE id='G-001'", goalOrg)
	e.SeedAgent(ft.AgentRow{Name: "coder-x", Role: "coder", Goal: "G-001", Team: "g-001-a", PaneID: "p-c"})
	sqlExec(t, e, "UPDATE agents SET pane_id='p-lead' WHERE name='claude-lead-g-001-a'")
	sqlExec(t, e, "UPDATE agents SET blocked_since=? WHERE name='coder-x'", float64(time.Now().Unix()-3600))
	e.SetHerdr(map[string]any{
		"workspaces": []any{map[string]any{"workspace_id": "w1", "label": "w"}},
		"panes": []any{
			map[string]any{"pane_id": "p-c", "tab_id": "t1", "workspace_id": "w1"},
			map[string]any{"pane_id": "p-lead", "tab_id": "t1", "workspace_id": "w1"},
		},
		"agents": []any{
			map[string]any{"name": "coder-x", "agent": "claude", "pane_id": "p-c", "agent_status": "blocked"},
			map[string]any{"name": "claude-lead-g-001-a", "agent": "claude", "pane_id": "p-lead", "agent_status": "idle"},
		},
	})
	ok(t, e, "", nil, "tick")
	got := map[string]struct{}{}
	for _, r := range e.SQL("SELECT recipient FROM messages WHERE subject='coder-x blocked >10 min'") {
		got[r[0]] = struct{}{}
	}
	if !reflectSetEq(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func reflectSetEq(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func TestOrgsCursorQaRunsOnJudgementModel(t *testing.T) {
	// T-1615: QA is part of the review gate, so cursor qa uses the judgement model.
	get := func(org, role string) string {
		m, err := scout.Model(org, role)
		if err != nil {
			t.Fatalf("Model(%s,%s): %v", org, role, err)
		}
		return m
	}
	got := []string{get("cursor", "qa"), get("cursor", "reviewer"), get("cursor", "coder"), get("claude", "qa")}
	want := []string{"grok-4.7-high", "grok-4.7-high", "composer-2.5", "claude-sonnet-5-5"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v want %v", got, want)
		}
	}
	// no other cursor build role moved
	wantBuild := map[string]string{"coder": "composer-2.5", "cleaner": "composer-2.5", "scout": "composer-2.5", "retro": "composer-2.5", "evaluator": "claude-sonnet-5-5"}
	for _, role := range scout.BuildRoles {
		if role == "qa" {
			continue
		}
		if w, ok := wantBuild[role]; !ok || get("cursor", role) != w {
			t.Fatalf("cursor build role %s = %s, want %q", role, get("cursor", role), w)
		}
		delete(wantBuild, role)
	}
	if len(wantBuild) != 0 {
		t.Fatalf("build roles missing from scout.BuildRoles: %v", wantBuild)
	}
}

func redPrAlert(t *testing.T, goalOrg string, want map[string]struct{}) {
	e := goalAlertEnv(t)
	tickReady(t, e)
	sqlExec(t, e, "UPDATE goals SET org=? WHERE id='G-001'", goalOrg)
	ok(t, e, "", nil, "tick")
	if got := redPrMails(t, e); !reflectSetEq(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestOrgsRedPrOnACursorGoalGoesToTheCursorDirectorNotTheClaudeLead(t *testing.T) {
	needPy(t)
	redPrAlert(t, "cursor", map[string]struct{}{"cursor-director": {}})
}

func TestOrgsRedPrOnAClaudeGoalGoesToItsClaudeLead(t *testing.T) {
	needPy(t)
	redPrAlert(t, "claude", map[string]struct{}{"claude-lead-g-001-a": {}})
}

func TestOrgsBlockedAlertOnACursorGoalGoesToTheCursorDirectorNotTheClaudeLead(t *testing.T) {
	needPy(t)
	blockedMails(t, goalAlertEnv(t), "cursor", map[string]struct{}{"cursor-director": {}})
}

func TestOrgsBlockedAlertOnAClaudeGoalGoesToItsClaudeLead(t *testing.T) {
	needPy(t)
	blockedMails(t, goalAlertEnv(t), "claude", map[string]struct{}{"claude-lead-g-001-a": {}})
}

// tickReady makes `factory tick` reach its probes: the Python tick, a reachable stateful pane stub and the pane backend.
func tickReady(t *testing.T, e *ft.Env) {
	t.Helper()
	e.NeedPython()
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
}
