package s2cli

import (
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

func TestResumeUpAdoptsFlaglessResumedAgentAndFlagsIt(t *testing.T) {
	r := newResumeEnv(t)
	r.resumeSetAgents(t, [][2]string{
		{"restored-1", "sess-dir"}, {"restored-2", "sess-boss"}, {"restored-3", "sess-lead"},
	})
	r.resumeFakeProcBare(t, 101, "sess-dir")
	r.resumeFakeProcRole(t, 102, "sess-boss", "boss")
	r.resumeFakeProcRole(t, 103, "sess-lead", "lead")
	r.NeedPython()
	p := r.OK("up")
	contains(t, "up stdout", p.Stdout, "zz-test-director")
	if st := r.resumeAgentStatus(t, "zz-test-director"); st != "live" {
		t.Fatalf("status = %s, want live", st)
	}
	fl, since := r.resumeFlagless(t, "zz-test-director")
	if fl != "1" || since == "NULL" || since == "" {
		t.Fatalf("flagless director = (%s, %s), want (1, set)", fl, since)
	}
	if fl, _ := r.resumeFlagless(t, "zz-test-boss"); fl != "0" {
		t.Fatalf("zz-test-boss flagless = %s, want 0", fl)
	}
	if n := r.Count("SELECT count(*) FROM agents WHERE status='flagless'"); n != 0 {
		t.Fatalf("status=flagless count = %d, want 0", n)
	}
	if len(resumeStartsMap(r)) != 0 {
		t.Fatalf("agent start calls = %v, want none", resumeStartsMap(r))
	}
	for _, pair := range resumeHerdrArgv2(r) {
		if pair == [2]string{"pane", "close"} {
			t.Fatal("herdr pane close was called")
		}
	}
	ev := r.SQL("SELECT detail FROM events WHERE action='flagless'")
	if len(ev) != 1 {
		t.Fatalf("flagless events = %d, want 1", len(ev))
	}
	detail := resumeEventDetail(t, ev[0][0])
	if detail["name"] != "zz-test-director" || detail["pane"] != "px0" {
		t.Fatalf("flagless event = %v", detail)
	}
	switch pid := detail["pid"].(type) {
	case float64:
		if int(pid) != 101 {
			t.Fatalf("flagless event pid = %v, want 101", detail)
		}
	case string:
		if n, _ := strconv.Atoi(pid); n != 101 {
			t.Fatalf("flagless event pid = %v, want 101", detail)
		}
	default:
		t.Fatalf("flagless event pid = %v, want 101", detail)
	}
	msgs := resumeFlaglessMsgs(r)
	if len(msgs) != 1 {
		t.Fatalf("flagless msgs = %d, want 1", len(msgs))
	}
	contains(t, "msg body", msgs[0][1], "flagless agents after restore: zz-test-director")
	contains(t, "msg body", msgs[0][1], "Relaunch needs owner approval")
}

func TestResumeUpDoesNotRealertAlreadyFlagless(t *testing.T) {
	r := newResumeEnv(t)
	r.resumeSetAgents(t, [][2]string{
		{"restored-1", "sess-dir"}, {"restored-2", "sess-boss"}, {"restored-3", "sess-lead"},
	})
	r.resumeFakeProcBare(t, 101, "sess-dir")
	r.resumeFakeProcRole(t, 102, "sess-boss", "boss")
	r.resumeFakeProcRole(t, 103, "sess-lead", "lead")
	r.NeedPython()
	r.OK("up")
	if len(resumeFlaglessMsgs(r)) != 1 {
		t.Fatalf("flagless msgs after first up = %d, want 1", len(resumeFlaglessMsgs(r)))
	}
	r.OK("up")
	if len(resumeFlaglessMsgs(r)) != 1 {
		t.Fatalf("flagless msgs after second up = %d, want 1", len(resumeFlaglessMsgs(r)))
	}
	if fl, _ := r.resumeFlagless(t, "zz-test-director"); fl != "1" {
		t.Fatalf("director flagless = %s, want 1", fl)
	}
	r.resumeFakeProcBare(t, 104, "sess-lead")
	sqlExec(t, r.Env, "UPDATE agents SET flagless=0, flagless_since=NULL WHERE name='zz-test-lead'")
	resumeRemoveFakeProc(t, r.Env, 103)
	r.OK("up")
	msgs := resumeFlaglessMsgs(r)
	if len(msgs) != 2 {
		t.Fatalf("flagless msgs = %d, want 2", len(msgs))
	}
	contains(t, "second msg", msgs[1][1], "zz-test-lead")
	notContains(t, "second msg", msgs[1][1], "zz-test-director")
}

func TestResumeUpAdoptsResumedAgentWithFlags(t *testing.T) {
	r := newResumeEnv(t)
	r.resumeSetAgents(t, [][2]string{
		{"restored-1", "sess-dir"}, {"restored-2", "sess-boss"}, {"restored-3", "sess-lead"},
	})
	r.resumeFakeProcRole(t, 101, "sess-dir", "director")
	r.resumeFakeProcRole(t, 102, "sess-boss", "boss")
	r.resumeFakeProcRole(t, 103, "sess-lead", "lead")
	r.NeedPython()
	r.OK("up")
	if len(resumeStartsMap(r)) != 0 {
		t.Fatalf("starts = %v, want none", resumeStartsMap(r))
	}
	for _, n := range []string{"zz-test-director", "zz-test-boss", "zz-test-lead"} {
		if st := r.resumeAgentStatus(t, n); st != "live" {
			t.Fatalf("%s status = %s, want live", n, st)
		}
	}
	if len(resumeFlaglessMsgs(r)) != 0 {
		t.Fatal("unexpected flagless messages")
	}
	if n := r.Count("SELECT count(*) FROM events WHERE action='flagless'"); n != 0 {
		t.Fatalf("flagless events = %d, want 0", n)
	}
}

func TestResumeFlaglessDetectedByTickThrottled(t *testing.T) {
	r := newResumeEnv(t)
	sqlExec(t, r.Env, "UPDATE agents SET status='live', pane_id='px0' WHERE name='zz-test-director'")
	r.resumeSetAgents(t, [][2]string{{"zz-test-director", "sess-dir"}})
	r.resumeFakeProcBare(t, 101, "sess-dir")
	r.OK("tick")
	if st := r.resumeAgentStatus(t, "zz-test-director"); st != "live" {
		t.Fatalf("status = %s, want live", st)
	}
	if fl, _ := r.resumeFlagless(t, "zz-test-director"); fl != "1" {
		t.Fatalf("flagless = %s, want 1", fl)
	}
	if len(resumeFlaglessMsgs(r)) != 1 {
		t.Fatalf("flagless msgs = %d, want 1", len(resumeFlaglessMsgs(r)))
	}
	sqlExec(t, r.Env, "UPDATE agents SET flagless=0, flagless_since=NULL WHERE name='zz-test-director'")
	r.OK("tick")
	if fl, _ := r.resumeFlagless(t, "zz-test-director"); fl != "0" {
		t.Fatalf("flagless after throttled tick = %s, want 0", fl)
	}
	if len(resumeFlaglessMsgs(r)) != 1 {
		t.Fatalf("flagless msgs = %d, want 1", len(resumeFlaglessMsgs(r)))
	}
	sqlExec(t, r.Env, "DELETE FROM throttle WHERE key='flagless_check:zz-test-director'")
	r.OK("tick")
	if fl, _ := r.resumeFlagless(t, "zz-test-director"); fl != "1" {
		t.Fatalf("flagless after throttle clear = %s, want 1", fl)
	}
	if n := r.Count("SELECT count(*) FROM events WHERE action='flagless'"); n != 2 {
		t.Fatalf("flagless events = %d, want 2", n)
	}
	if err := os.Remove(filepath.Join(r.ProcDir, "101", "cmdline")); err != nil {
		t.Fatal(err)
	}
	r.resumeFakeProcRole(t, 102, "sess-dir", "director")
	sqlExec(t, r.Env, "DELETE FROM throttle WHERE key='flagless_check:zz-test-director'")
	r.OK("tick")
	fl, since := r.resumeFlagless(t, "zz-test-director")
	if fl != "0" || since != "NULL" {
		t.Fatalf("flagless cleared = (%s, %s), want (0, NULL)", fl, since)
	}
	if st := r.resumeAgentStatus(t, "zz-test-director"); st != "live" {
		t.Fatalf("status = %s, want live", st)
	}
}

func TestResumeTickSkipsFlaglessEvaluatorButAlertsOtherRoles(t *testing.T) {
	r := newResumeEnv(t)
	for _, row := range []struct {
		name, sess, role string
		pid              int
	}{
		{"zz-test-eval", "sess-eval", "evaluator", 110},
		{"zz-test-coder", "sess-coder", "coder", 111},
	} {
		r.SeedAgent(ft.AgentRow{Name: row.name, Role: row.role, Status: "live"})
		sqlExec(t, r.Env, "UPDATE agents SET session_id=?, pane_id='px0' WHERE name=?", row.sess, row.name)
		r.resumeFakeProcBare(t, row.pid, row.sess)
	}
	r.resumeSetAgents(t, [][2]string{{"zz-test-eval", "sess-eval"}, {"zz-test-coder", "sess-coder"}})
	r.OK("tick")
	fl, since := r.resumeFlagless(t, "zz-test-eval")
	if fl != "0" || since != "NULL" {
		t.Fatalf("evaluator flagless = (%s, %s), want (0, NULL)", fl, since)
	}
	if fl, _ := r.resumeFlagless(t, "zz-test-coder"); fl != "1" {
		t.Fatalf("coder flagless = %s, want 1", fl)
	}
	msgs := resumeFlaglessMsgs(r)
	if len(msgs) != 1 {
		t.Fatalf("flagless msgs = %d, want 1", len(msgs))
	}
	contains(t, "msg", msgs[0][1], "zz-test-coder")
	notContains(t, "msg", msgs[0][1], "zz-test-eval")
	if n := r.Count("SELECT count(*) FROM events WHERE action='flagless' AND detail LIKE '%zz-test-eval%'"); n != 0 {
		t.Fatalf("evaluator flagless events = %d, want 0", n)
	}
}

func TestResumeThrottleKeysStayOutOfConfigAndStateDump(t *testing.T) {
	r := newResumeEnv(t)
	sqlExec(t, r.Env, "UPDATE agents SET status='live', pane_id='px0' WHERE name='zz-test-director'")
	r.resumeSetAgents(t, [][2]string{{"zz-test-director", "sess-dir"}})
	r.resumeFakeProcBare(t, 101, "sess-dir")
	r.OK("tick")
	if n := r.Count("SELECT count(*) FROM throttle WHERE key LIKE 'flagless_check%'"); n != 1 {
		t.Fatalf("throttle rows = %d, want 1", n)
	}
	notContains(t, "config stdout", r.OK("config").Stdout, "flagless_check")
	if n := r.Count("SELECT count(*) FROM config WHERE key LIKE 'flagless_check%'"); n != 0 {
		t.Fatalf("config flagless_check keys = %d, want 0", n)
	}
	r.OK("export", "--no-push")
	sql, err := os.ReadFile(filepath.Join(r.Home, "state", "factory.sql"))
	if err != nil {
		t.Fatal(err)
	}
	notContains(t, "state dump", string(sql), "flagless_check")
}

func TestResumeFlaglessDirectorStillNudged(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.OK("msg", "director", "work", "do it")
	r.resumeDetect(t)
	var nudges int
	for _, a := range resumePrompts(r) {
		if len(a) >= 3 && a[2] == "zz-test-director" && strings.Contains(resumeJoinLines(a, " "), "📬") {
			nudges++
		}
	}
	if nudges != 1 {
		t.Fatalf("nudges = %d, want 1; prompts %v", nudges, resumePrompts(r))
	}
}

func TestResumeFlaglessIdentityRecoveredFromPane(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.OK("msg", "director", "work", "do it")
	r.resumeDetect(t)
	p := r.OKWith(ft.Opts{Env: r.resumePaneEnv()}, "whoami")
	contains(t, "whoami", p.Stdout, "zz-test-director")
	contains(t, "whoami", p.Stdout, "role=director")
	p = r.OKWith(ft.Opts{Env: r.resumePaneEnv()}, "inbox")
	contains(t, "inbox", p.Stdout, "work")
}

func TestResumeFlaglessCountedByGate(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.OK("config", "max_live_agents", "1")
	r.resumeDetect(t)
	contains(t, "res", r.OK("res").Stdout, "CLOSED — 1/1")
}

func TestResumeFlaglessDuplicateSpawnRefused(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.resumeDetect(t)
	r.NeedPython()
	p := r.RunWith(ft.Opts{Agent: "zz-test-director"}, "spawn", "director", "--name", "zz-test-director")
	if p.Exit == 0 {
		t.Fatalf("spawn succeeded, want refusal\n%s%s", p.Stdout, p.Stderr)
	}
	contains(t, "stderr", p.Stderr, "already live")
	if len(resumeStartsMap(r)) != 0 {
		t.Fatal("unexpected agent start")
	}
}

func TestResumeFreshRespawnClearsOldFlagless(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.resumeFlaglessPM(t)
	p := r.resumeSpawnPM(t)
	if p.Exit != 0 {
		t.Fatalf("spawn: %s", p.Stderr)
	}
	starts := resumeStartsMap(r)
	argv, ok := starts["zz-test-pm"]
	if !ok {
		t.Fatalf("starts = %v", starts)
	}
	if resumeIndexAfter(argv, "--session-id") < 0 {
		t.Fatalf("spawn argv missing --session-id: %v", argv)
	}
	fl, since := r.resumeFlagless(t, "zz-test-pm")
	if fl != "0" || since != "NULL" {
		t.Fatalf("flagless after spawn = (%s, %s), want (0, NULL)", fl, since)
	}
	ev := r.SQL("SELECT detail FROM events WHERE action='flagless-cleared' AND entity_id='zz-test-pm'")
	var got []map[string]any
	for _, row := range ev {
		got = append(got, resumeEventDetail(t, row[0]))
	}
	if !reflect.DeepEqual(got, []map[string]any{{"via": "spawn"}}) {
		t.Fatalf("flagless-cleared events = %v", got)
	}
}

func TestResumeFlaglessKeptWhenStartFails(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.resumeFlaglessPM(t)
	r.SetHerdr(map[string]any{"start_fail": true})
	p := r.resumeSpawnPM(t)
	if p.Exit != 0 {
		t.Fatalf("spawn: %s", p.Stderr)
	}
	fl, since := r.resumeFlagless(t, "zz-test-pm")
	if fl != "1" || since != "2026-10-23T10:00:00+00:00" {
		t.Fatalf("flagless = (%s, %s), want (1, 2026-10-23T10:00:00+00:00)", fl, since)
	}
	if n := r.Count("SELECT count(*) FROM events WHERE action='flagless-cleared'"); n != 0 {
		t.Fatalf("flagless-cleared events = %d, want 0", n)
	}
	if n := r.Count("SELECT count(*) FROM events WHERE action='start-uncertain'"); n != 1 {
		t.Fatalf("start-uncertain events = %d, want 1", n)
	}
}

func TestResumeFlaglessListedInAgentsWithMarker(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.resumeDetect(t)
	out := r.OK("agents").Stdout
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "zz-test-director") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("zz-test-director not listed")
	}
	contains(t, "agents line", line, "FLAGLESS")
	contains(t, "agents line", line, "live")
	if n := r.Count("SELECT count(*) FROM agents WHERE status='flagless'"); n != 0 {
		t.Fatalf("status=flagless count = %d, want 0", n)
	}
	contains(t, "digest", r.OK("digest").Stdout, "agent zz-test-director running without role flags since")
}

func TestResumeFlaglessClearedWhenFlagsPresent(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.resumeDetect(t)
	if err := os.Remove(filepath.Join(r.ProcDir, "101", "cmdline")); err != nil {
		t.Fatal(err)
	}
	r.resumeFakeProcRole(t, 102, "sess-dir", "director")
	sqlExec(t, r.Env, "DELETE FROM throttle")
	r.OK("tick")
	fl, since := r.resumeFlagless(t, "zz-test-director")
	if fl != "0" || since != "NULL" {
		t.Fatalf("flagless = (%s, %s), want (0, NULL)", fl, since)
	}
	var line string
	for _, l := range strings.Split(r.OK("agents").Stdout, "\n") {
		if strings.Contains(l, "zz-test-director") {
			line = l
			break
		}
	}
	notContains(t, "agents line", line, "FLAGLESS")
}

func TestResumeDoneLeavesFlaglessColumn(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	r.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a", Task: "T-0001"})
	sqlExec(t, r.Env, "UPDATE agents SET flagless=1, flagless_since='t', session_id='s-c' WHERE name='zz-test-coder'")
	r.OKWith(ft.Opts{Agent: "zz-test-coder"}, "done", "ok")
	row := r.SQL("SELECT status, flagless FROM agents WHERE name='zz-test-coder'")[0]
	if row[0] != "done" || row[1] != "1" {
		t.Fatalf("agent row = (%s, %s), want (done, 1)", row[0], row[1])
	}
}

func TestResumeMigrationAddsFlaglessColumnsIdempotent(t *testing.T) {
	r := newResumeStillLiveEnv(t)
	d := r.RawDB()
	defer d.Close()
	for _, q := range []string{
		"ALTER TABLE agents DROP COLUMN flagless",
		"ALTER TABLE agents DROP COLUMN flagless_since",
		"DROP TABLE throttle",
	} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	cols := resumeAgentCols(t, r.Env)
	if cols["flagless"] || cols["flagless_since"] {
		t.Fatal("columns not dropped")
	}
	before := r.SQL("SELECT name, status FROM agents ORDER BY name")
	for i := 0; i < 2; i++ {
		r.OK("agents")
		cols = resumeAgentCols(t, r.Env)
		if !cols["flagless"] || !cols["flagless_since"] {
			t.Fatalf("run %d: missing flagless columns", i)
		}
	}
	after := r.SQL("SELECT name, status FROM agents ORDER BY name")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("agent rows changed:\nbefore %v\nafter %v", before, after)
	}
	if r.SQL("SELECT flagless FROM agents WHERE name='zz-test-director'")[0][0] != "0" {
		t.Fatal("director flagless default not 0")
	}
	if n := r.Count("SELECT count(*) FROM throttle"); n != 0 {
		t.Fatalf("throttle rows = %d, want 0", n)
	}
}

func TestResumeUpRelaunchesInRestoredShellPaneWithFullFlags(t *testing.T) {
	r := newResumeEnv(t)
	r.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "x"}},
		"tabs":       []map[string]any{{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 3}},
		"panes": []map[string]string{
			{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1"},
			{"pane_id": "p2", "tab_id": "t1", "workspace_id": "w1"},
			{"pane_id": "p3", "tab_id": "t1", "workspace_id": "w1"},
		},
		"seq": float64(10),
	})
	for n, pane := range map[string]string{"zz-test-director": "p1", "zz-test-boss": "p2", "zz-test-lead": "p3"} {
		sqlExec(t, r.Env, "UPDATE agents SET pane_id=? WHERE name=?", pane, n)
	}
	r.NeedPython()
	r.OK("up")
	starts := resumeStartsMap(r)
	want := map[string]bool{"zz-test-director": true, "zz-test-boss": true, "zz-test-lead": true}
	if len(starts) != len(want) {
		t.Fatalf("starts = %v", starts)
	}
	for name := range want {
		if _, ok := starts[name]; !ok {
			t.Fatalf("missing start for %s", name)
		}
	}
	for _, spec := range []struct {
		name, pane, sess, role string
	}{
		{"zz-test-director", "p1", "sess-dir", "director"},
		{"zz-test-boss", "p2", "sess-boss", "boss"},
		{"zz-test-lead", "p3", "sess-lead", "lead"},
	} {
		argv := starts[spec.name]
		if i := resumeIndexAfter(argv, "--pane"); i < 0 || argv[i+1] != spec.pane {
			t.Fatalf("%s pane arg = %v, want %s", spec.name, argv, spec.pane)
		}
		if i := resumeIndexAfter(argv, "--resume"); i < 0 || argv[i+1] != spec.sess {
			t.Fatalf("%s resume session = %v, want %s", spec.name, argv, spec.sess)
		}
		if i := resumeIndexAfter(argv, "--settings"); i < 0 || argv[i+1] != r.resumeSettings(spec.role) {
			t.Fatalf("%s settings = %v", spec.name, argv)
		}
		if resumeIndexAfter(argv, "--model") < 0 || resumeIndexAfter(argv, "--append-system-prompt-file") < 0 {
			t.Fatalf("%s missing model flags: %v", spec.name, argv)
		}
		if i := resumeIndexAfter(argv, "--permission-mode"); i < 0 || argv[i+1] != "auto" {
			t.Fatalf("%s permission-mode = %v", spec.name, argv)
		}
	}
	var made [][2]string
	for _, c := range r.Calls("herdr") {
		if len(c.Argv) < 2 {
			continue
		}
		pair := [2]string{c.Argv[0], c.Argv[1]}
		if pair == [2]string{"tab", "create"} || pair == [2]string{"pane", "split"} {
			made = append(made, pair)
		}
	}
	if len(made) != 0 {
		t.Fatalf("unexpected layout calls %v", made)
	}
}

func TestResumePaneWithParentShellButClaudeForegroundIsNotAShell(t *testing.T) {
	r := newResumeEnv(t)
	r.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "x"}},
		"tabs":       []map[string]any{{"tab_id": "t1", "workspace_id": "w1", "label": "director", "pane_count": 1}},
		"panes":      []map[string]any{{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "fg": "claude", "parent": "zsh"}},
		"seq":        float64(10),
	})
	sqlExec(t, r.Env, "UPDATE agents SET pane_id='p1' WHERE name='zz-test-director'")
	r.NeedPython()
	r.OK("up")
	argv := resumeStartsMap(r)["zz-test-director"]
	if i := resumeIndexAfter(argv, "--pane"); i >= 0 && argv[i+1] == "p1" {
		t.Fatalf("typed into running TUI pane p1: %v", argv)
	}
	for _, c := range resumeHerdrCalls(r) {
		if len(c) >= 3 && c[0] == "pane" && c[1] == "run" && c[2] == "p1" {
			t.Fatalf("pane run p1 called: %v", c)
		}
	}
}

func TestResumeUpPlacesNewPaneWhenRestoredPaneIsNotAShell(t *testing.T) {
	r := newResumeEnv(t)
	r.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "x"}},
		"tabs":       []map[string]any{{"tab_id": "t1", "workspace_id": "w1", "label": "director", "pane_count": 1}},
		"panes":      []map[string]any{{"pane_id": "p1", "tab_id": "t1", "workspace_id": "w1", "fg": "vim"}},
		"seq":        float64(10),
	})
	sqlExec(t, r.Env, "UPDATE agents SET pane_id='p1' WHERE name='zz-test-director'")
	r.NeedPython()
	r.OK("up")
	argv := resumeStartsMap(r)["zz-test-director"]
	if i := resumeIndexAfter(argv, "--pane"); i >= 0 && argv[i+1] == "p1" {
		t.Fatalf("reused vim pane p1: %v", argv)
	}
}

func TestResumeRelaunchDryRunPrintsPlanAndChangesNothing(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	before := r.SQL("SELECT * FROM agents ORDER BY name")
	nEvents := r.Count("SELECT count(*) FROM events")
	r.NeedPython()
	p := r.OKWith(ft.Opts{Agent: "zz-test-director"}, "relaunch", "zz-test-lead", "--dry-run")
	contains(t, "stdout", p.Stdout, "herdr pane close px2")
	contains(t, "stdout", p.Stdout, "--resume sess-lead")
	contains(t, "stdout", p.Stdout, "--settings")
	contains(t, "stdout", p.Stdout, "dry run")
	if got := r.SQL("SELECT * FROM agents ORDER BY name"); !reflect.DeepEqual(got, before) {
		t.Fatal("agents table changed during dry run")
	}
	if n := r.Count("SELECT count(*) FROM events"); n != nEvents {
		t.Fatalf("events count = %d, want %d", n, nEvents)
	}
	if len(resumeMutatingHerdr(r)) != 0 {
		t.Fatalf("mutating herdr calls = %v", resumeMutatingHerdr(r))
	}
}

func TestResumeRelaunchRefusesNonFactoryPane(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	r.NeedPython()
	p := r.FailWith(ft.Opts{Agent: "zz-test-director"}, 3, "relaunch", "stranger", "--dry-run")
	contains(t, "stderr", p.Stderr, "not a factory agent")
	r.SetHerdr(map[string]any{
		"agents": []map[string]any{{
			"name": "owner-claude", "agent": "claude", "pane_id": "px2",
			"agent_status": "idle", "agent_session": map[string]string{"value": "owner-sess"},
		}},
	})
	p = r.FailWith(ft.Opts{Agent: "zz-test-director"}, 3, "relaunch", "zz-test-lead")
	contains(t, "stderr", p.Stderr, "refusing to touch")
	if len(resumeMutatingHerdr(r)) != 0 {
		t.Fatalf("mutating herdr calls = %v", resumeMutatingHerdr(r))
	}
}

func TestResumeRelaunchRefusesWhenSessionPaneUnknown(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	r.resumeFakeProcBare(t, 101, "sess-lead")
	r.SetHerdr(map[string]any{"agents": []map[string]any{}})
	r.NeedPython()
	p := r.RunWith(ft.Opts{Agent: "zz-test-director"}, "relaunch", "zz-test-lead")
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	contains(t, "stderr", p.Stderr, "second copy")
	if len(resumeMutatingHerdr(r)) != 0 {
		t.Fatalf("mutating herdr calls = %v", resumeMutatingHerdr(r))
	}
}

func TestResumeRelaunchFindsHostBySessionWhenRowPaneIsStale(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	sqlExec(t, r.Env, "UPDATE agents SET pane_id='pstale' WHERE name='zz-test-lead'")
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-test-director"}, "relaunch", "zz-test-lead")
	calls := resumeHerdrCalls(r)
	if !sliceContainsCall(calls, []string{"pane", "close", "px2"}) {
		t.Fatalf("missing close px2 in %v", calls)
	}
	if sliceContainsCall(calls, []string{"pane", "close", "pstale"}) {
		t.Fatalf("closed stale pane pstale: %v", calls)
	}
}

func TestResumeRelaunchRefusedForWorkers(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	r.SeedAgent(ft.AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	r.NeedPython()
	r.FailWith(ft.Opts{Agent: "zz-test-coder"}, 3, "relaunch", "zz-test-lead", "--dry-run")
}

func TestResumeRelaunchClosesPaneAndResumesWithFullFlags(t *testing.T) {
	r := newResumeRelaunchEnv(t)
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-test-director"}, "relaunch", "zz-test-lead")
	calls := resumeHerdrCalls(r)
	if !sliceContainsCall(calls, []string{"pane", "close", "px2"}) {
		t.Fatalf("missing close px2 in %v", calls)
	}
	argv := resumeStartsMap(r)["zz-test-lead"]
	if i := resumeIndexAfter(argv, "--resume"); i < 0 || argv[i+1] != "sess-lead" {
		t.Fatalf("resume session: %v", argv)
	}
	if i := resumeIndexAfter(argv, "--settings"); i < 0 || argv[i+1] != r.resumeSettings("lead") {
		t.Fatalf("settings: %v", argv)
	}
	if i := resumeIndexAfter(argv, "--pane"); i >= 0 && argv[i+1] == "px2" {
		t.Fatalf("reused closed pane px2: %v", argv)
	}
}

func sliceContainsCall(calls [][]string, want []string) bool {
	for _, c := range calls {
		if len(c) < len(want) {
			continue
		}
		ok := true
		for i := range want {
			if c[i] != want[i] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}
