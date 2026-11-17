package s2cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// PARTIAL (P7a, T-1495): 32 of 33 cases ported.
// not ported: test_feature_off_cursor_pane_limit_does_not_defer_idle_relaunch - calls mod.tick_relaunches in-process
// (a Python internal: no CLI entry point and no Go counterpart).
//
// Go tests for tests/test_relaunch_idle.py: 32 of 33 cases, stateful herdr stub, roles/ copytree like Python.

func TestRelaunchIdleRequestFlagsRowAndTickRelaunchesWithResumeAndNewPrompt(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.editLeadRole(t)
	p := r.queue(t, "zz-director")
	contains(t, "relaunch stdout", p.Stdout, "relaunch of "+riLead+" queued: the tick runs it when "+riLead+" is idle")
	at, by := r.relaunchFlag(t, "")
	expectFlagTimestamp(t, at)
	if by != "zz-director" {
		t.Fatalf("relaunch_requested_by = %q, want zz-director", by)
	}
	wantLen(t, "relaunch-requested events", r.eventDetails(t, "relaunch-requested"), 1)
	wantEmpty(t, "starts before tick", r.starts())
	r.NeedPython()
	r.OK("tick")
	if got := r.closes(); len(got) != 1 || got[0][2] != riPane {
		t.Fatalf("pane close = %v, want [[pane close %s]]", got, riPane)
	}
	starts := r.starts()
	wantLen(t, "starts after tick", starts, 1)
	argv := starts[0]
	if len(argv) <= 2 || argv[2] != riLead {
		t.Fatalf("start agent = %v, want name %s", argv, riLead)
	}
	if j := argvIndex(argv, "--resume"); j < 0 || argv[j+1] != riSess {
		t.Fatalf("--resume %s missing in %v", riSess, argv)
	}
	if argvIndex(argv, "--append-system-prompt-file") < 0 {
		t.Fatalf("--append-system-prompt-file missing in %v", argv)
	}
	r.closeThenStartOrder(t)
	prompt, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(prompt), "ZZ-NEW-RULE") {
		t.Fatalf("prompt = %q, want ZZ-NEW-RULE", prompt)
	}
	if got := rowField(r.SQL("SELECT session_id FROM agents WHERE name=?", riLead), 0); got != riSess {
		t.Fatalf("session_id = %q, want %s", got, riSess)
	}
	at2, by2 := r.relaunchFlag(t, "")
	expectFlagClearPair(t, at2, by2)
	wantLen(t, "relaunch-run events", r.eventDetails(t, "relaunch-run"), 1)
	r.OK("tick")
	wantLen(t, "starts after second tick", r.starts(), 1)
	wantLen(t, "closes after second tick", r.closes(), 1)
}

func TestRelaunchIdleWorkingAgentWaitsThenRelaunchesWhenIdle(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	r.setStatus(t, "working")
	r.NeedPython()
	r.OK("tick")
	wantEmpty(t, "starts while working", r.starts())
	wantEmpty(t, "closes while working", r.closes())
	at, _ := r.relaunchFlag(t, "")
	expectFlagTimestamp(t, at)
	r.setStatus(t, "blocked")
	r.OK("tick")
	wantEmpty(t, "closes while blocked", r.closes())
	r.setStatus(t, "idle")
	r.OK("tick")
	wantLen(t, "starts when idle", r.starts(), 1)
	at2, by2 := r.relaunchFlag(t, "")
	expectFlagClearPair(t, at2, by2)
}

func TestRelaunchIdleMemoryFloorDefersWithoutClosingThePane(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	r.OK("config", "min_mem_available_mb", "100000000")
	r.NeedPython()
	r.OK("tick")
	wantEmpty(t, "closes on mem floor", r.closes())
	wantEmpty(t, "starts on mem floor", r.starts())
	atM, _ := r.relaunchFlag(t, "")
	expectFlagTimestamp(t, atM)
	wantLen(t, "relaunch-deferred events", r.eventDetails(t, "relaunch-deferred"), 1)
	r.OK("tick")
	wantLen(t, "relaunch-deferred events throttled", r.eventDetails(t, "relaunch-deferred"), 1)
	r.OK("config", "min_mem_available_mb", "1")
	r.OK("tick")
	wantLen(t, "closes after mem ok", r.closes(), 1)
	wantLen(t, "starts after mem ok", r.starts(), 1)
	atM2, byM2 := r.relaunchFlag(t, "")
	expectFlagClearPair(t, atM2, byM2)
}

func TestRelaunchIdleUsageLimitDefersToo(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	r.writeUsage(t, 99, 0, 0)
	r.NeedPython()
	r.OK("tick")
	wantEmpty(t, "closes on usage limit", r.closes())
	wantLen(t, "relaunch-deferred events", r.eventDetails(t, "relaunch-deferred"), 1)
}

func TestRelaunchIdleAgentHoldingTheLastSlotStillRelaunches(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.OK("config", "max_live_agents", "1")
	r.queue(t, "zz-director")
	r.NeedPython()
	r.OK("tick")
	wantLen(t, "starts with cap 1", r.starts(), 1)
}

func TestRelaunchIdleOverCapWithoutTheAgentDefersWithoutClosingThePane(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.herdrAgents(t, [][4]string{
		{"idle", riLead, riSess, riPane},
		{"working", "zz-director", "sess-dir", "p-director"},
	})
	r.OK("config", "max_live_agents", "1")
	r.queue(t, "zz-director")
	r.NeedPython()
	r.OK("tick")
	wantEmpty(t, "closes over cap", r.closes())
	wantEmpty(t, "starts over cap", r.starts())
	atC, _ := r.relaunchFlag(t, "")
	expectFlagTimestamp(t, atC)
	wantLen(t, "relaunch-deferred events", r.eventDetails(t, "relaunch-deferred"), 1)
	if got := rowField(r.SQL("SELECT status FROM agents WHERE name=?", riLead), 0); got != "live" {
		t.Fatalf("lead status = %q, want live", got)
	}
	r.OK("config", "max_live_agents", "2")
	r.OK("tick")
	wantLen(t, "closes after raising cap", r.closes(), 1)
	wantLen(t, "starts after raising cap", r.starts(), 1)
}

func TestRelaunchIdleFailureInTickRecordsEventOneAlertAndExit0(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	r.SetHerdr(map[string]any{"pane_close_fail": true})
	r.NeedPython()
	p := r.OK("tick")
	wantLen(t, "relaunch-failed events", r.eventDetails(t, "relaunch-failed"), 1)
	alerts := msgRelaunchFailed(r)
	if len(alerts) != 1 || alerts[0][0] != "zz-director" || alerts[0][1] != "tick" || alerts[0][2] != "alert" {
		t.Fatalf("alerts = %v, want [(zz-director tick alert)]", alerts)
	}
	atF, byF := r.relaunchFlag(t, "")
	expectFlagClearPair(t, atF, byF)
	r.OK("tick")
	wantLen(t, "failure messages", r.SQL("SELECT 1 FROM messages WHERE subject LIKE 'relaunch of %failed'"), 1)
	if p.Exit != 0 {
		t.Fatalf("tick exit = %d, want 0", p.Exit)
	}
}

func TestRelaunchIdleRepeatRequestRefreshesTimestamp(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	sqlExec(t, r.Env, "UPDATE agents SET relaunch_requested_at='2020-01-01T00:00:00+00:00' WHERE name=?", riLead)
	r.queue(t, "zz-director")
	at, _ := r.relaunchFlag(t, "")
	if at == "2020-01-01T00:00:00+00:00" {
		t.Fatalf("relaunch_requested_at still frozen at 2020-01-01")
	}
}

func TestRelaunchIdleDryRunWhenIdleChangesNothing(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	p := r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", riLead, "--when-idle", "--dry-run")
	contains(t, "dry run", p.Stdout, "dry run")
	atD, byD := r.relaunchFlag(t, "")
	expectFlagClearPair(t, atD, byD)
	wantEmpty(t, "relaunch-requested events", r.eventDetails(t, "relaunch-requested"))
}

func TestRelaunchIdleAgentsShowsRelaunchPending(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	notContains(t, "agents before queue", r.OK("agents").Stdout, "RELAUNCH-PENDING")
	r.queue(t, "zz-director")
	row := agentsLine(r, r.OK("agents").Stdout)
	if !strings.Contains(row, "RELAUNCH-PENDING") {
		t.Fatalf("agents row = %q, want RELAUNCH-PENDING", row)
	}
}

func TestRelaunchIdleDirectorSelfRelaunchNeedsWhenIdle(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	p := r.FailWith(ft.Opts{Agent: "zz-director"}, 3, "relaunch", "zz-director")
	contains(t, "stderr", p.Stderr, "use --when-idle")
	wantEmpty(t, "closes", r.closes())
	r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", "zz-director", "--when-idle")
	atD2, _ := r.relaunchFlag(t, "zz-director")
	expectFlagTimestamp(t, atD2)
}

func TestRelaunchIdleBossMayRelaunchOnlyTheDirector(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-boss"}, "relaunch", "zz-director", "--when-idle")
	if _, by := r.relaunchFlag(t, "zz-director"); by != "zz-boss" {
		t.Fatalf("director relaunch_requested_by = %q, want zz-boss", by)
	}
	r.FailWith(ft.Opts{Agent: "zz-boss"}, 3, "relaunch", riLead, "--when-idle")
	r.FailWith(ft.Opts{Agent: "zz-boss"}, 3, "relaunch", "zz-boss", "--when-idle")
	atB, byB := r.relaunchFlag(t, "")
	expectFlagClearPair(t, atB, byB)
}

func TestRelaunchIdleWorkersAndLeadsRefused(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	for _, role := range []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "lead"} {
		n := "zz-" + role
		r.SeedAgent(ft.AgentRow{Name: n, Role: role, Goal: "G-001", Team: "g-001-a"})
		r.FailWith(ft.Opts{Agent: n}, 3, "relaunch", riLead, "--when-idle")
		r.FailWith(ft.Opts{Agent: n}, 3, "relaunch", riLead, "--dry-run")
	}
	atW, byW := r.relaunchFlag(t, "")
	expectFlagClearPair(t, atW, byW)
}

func TestRelaunchIdleOwnerMayRelaunchAnyAgentWhenIdle(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	r.OK("relaunch", riLead, "--when-idle")
	if _, by := r.relaunchFlag(t, ""); by != "owner" {
		t.Fatalf("relaunch_requested_by = %q, want owner", by)
	}
}

func TestRelaunchIdleWorkingAgentRefusedWithExit2UnlessForce(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.setStatus(t, "working")
	r.NeedPython()
	p := r.FailWith(ft.Opts{Agent: "zz-director"}, 2, "relaunch", riLead)
	contains(t, "stderr", p.Stderr, "mid-turn")
	wantEmpty(t, "closes", r.closes())
	wantEmpty(t, "starts", r.starts())
	r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", riLead, "--force")
	wantLen(t, "closes on force", r.closes(), 1)
	wantLen(t, "starts on force", r.starts(), 1)
}

func TestRelaunchIdleTickPathPassesSnapshotOff(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.queue(t, "zz-director")
	r.NeedPython()
	r.OK("tick")
	r.assertResumeArgv(t, r.starts()[0])
}

func TestRelaunchIdleImmediateRelaunchPassesSnapshotOff(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", riLead)
	r.assertResumeArgv(t, r.starts()[0])
}

func TestRelaunchIdleDryRunPrintsTheFlag(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	p := r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", riLead, "--dry-run")
	contains(t, "stdout", p.Stdout, "--system-prompt-snapshot off")
	wantEmpty(t, "starts", r.starts())
}

func TestRelaunchIdleFreshSpawnHasNoSnapshotFlag(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-director"}, "spawn", "lead", "--goal", "G-001", "--team", "g-001-a", "--name", "zz-fresh")
	argv, ok := startsNamed(r.starts(), "zz-fresh")
	if !ok {
		t.Fatal("no start for zz-fresh")
	}
	if argvIndex(argv, "--system-prompt-snapshot") >= 0 {
		t.Fatalf("fresh spawn must not pass --system-prompt-snapshot: %v", argv)
	}
	if argvIndex(argv, "--resume") >= 0 {
		t.Fatalf("fresh spawn must not pass --resume: %v", argv)
	}
	if argvIndex(argv, "--session-id") < 0 {
		t.Fatalf("--session-id missing in %v", argv)
	}
}

func TestRelaunchIdleSpawnWithResumePassesItWhateverTheCaller(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.NeedPython()
	r.pythonSpawn(t, "zz-other", "sess-other", false)
	r.pythonSpawn(t, "zz-other2", "sess-other2", true)
	for _, pair := range [][2]string{{"zz-other", "sess-other"}, {"zz-other2", "sess-other2"}} {
		argv, ok := startsNamed(r.starts(), pair[0])
		if !ok {
			t.Fatalf("no start for %s", pair[0])
		}
		i := argvIndex(argv, "--system-prompt-snapshot")
		if i < 0 || argv[i+1] != "off" {
			t.Fatalf("--system-prompt-snapshot off missing for %s: %v", pair[0], argv)
		}
		j := argvIndex(argv, "--resume")
		if j < 0 || argv[j+1] != pair[1] {
			t.Fatalf("--resume %s missing for %s: %v", pair[1], pair[0], argv)
		}
	}
}

func TestRelaunchIdleUpRestartPassesIt(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.SetHerdr(map[string]any{"agents": []any{}})
	sqlExec(t, r.Env, "UPDATE agents SET status='lost', pane_id=NULL WHERE name='zz-director'")
	r.NeedPython()
	r.OK("up", "--no-boss")
	argv, ok := startsNamed(r.starts(), "zz-director")
	if !ok {
		t.Fatal("no start for zz-director from up")
	}
	i := argvIndex(argv, "--system-prompt-snapshot")
	if i < 0 || argv[i+1] != "off" {
		t.Fatalf("--system-prompt-snapshot off missing: %v", argv)
	}
}

func TestRelaunchIdleSnapshotFlagLivesInOneConstant(t *testing.T) {
	src, err := os.ReadFile(factorySrcPath(t))
	if err != nil {
		t.Fatal(err)
	}
	n := len(regexp.MustCompile(`"--system-prompt-snapshot"`).FindAll(src, -1))
	if n != 1 {
		t.Fatalf(`"--system-prompt-snapshot" occurrences = %d, want 1`, n)
	}
}

func TestRelaunchIdleLiveAgentsPromptFileIsNotRewrittenByOtherSpawns(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	sentinel := []byte("SENTINEL live prompt\n")
	if err := os.WriteFile(r.promptPath(), sentinel, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	r.NeedPython()
	p := r.RunWith(ft.Opts{Agent: "zz-director"}, "spawn", "lead", "--goal", "G-001", "--team", "g-001-a", "--name", riLead)
	if p.Exit == 0 {
		t.Fatalf("spawn live agent: want non-zero exit")
	}
	contains(t, "stderr", p.Stderr, "already live")
	after, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatalf("prompt changed after CLI spawn")
	}
	for _, fq := range []bool{false, true} {
		if code := r.pythonSpawn(t, riLead, riSess, fq); code == 0 {
			t.Fatalf("inproc spawn from_queue=%v: want non-zero exit", fq)
		}
	}
	after2, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(after2) != string(before) {
		t.Fatalf("prompt changed after inproc spawn")
	}
	wantEmpty(t, "starts", r.starts())
}

func TestRelaunchIdleOwnRelaunchRewritesItAfterThePaneIsClosed(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	if err := os.WriteFile(r.promptPath(), []byte("SENTINEL live prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", riLead)
	prompt, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(prompt), "SENTINEL") {
		t.Fatalf("prompt still contains SENTINEL: %q", prompt)
	}
	wantLen(t, "closes", r.closes(), 1)
}

func TestRelaunchIdleAProcessStillRunningTheSessionBlocksTheRewrite(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	if err := os.WriteFile(r.promptPath(), []byte("SENTINEL live prompt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	proc := filepath.Join(r.Root, "fakeproc-relaunch")
	if err := os.Mkdir(proc, 0o755); err != nil {
		t.Fatal(err)
	}
	procDir := filepath.Join(proc, "4242")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "cmdline"), []byte("claude\x00--resume\x00"+riSess+"\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(procDir, "stat"), []byte("4242 (claude) S 1 4242 4242 0 -1 0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r.NeedPython()
	p := r.FailWith(ft.Opts{Agent: "zz-director", Env: map[string]string{"FACTORY_PROC_DIR": proc}}, 3, "relaunch", riLead)
	contains(t, "stderr", p.Stderr, "still runs session")
	prompt, err := os.ReadFile(r.promptPath())
	if err != nil {
		t.Fatal(err)
	}
	if string(prompt) != "SENTINEL live prompt\n" {
		t.Fatalf("prompt = %q, want sentinel unchanged", prompt)
	}
	wantEmpty(t, "starts", r.starts())
}

func TestRelaunchIdleNothingDeletesPromptFiles(t *testing.T) {
	src, err := os.ReadFile(factorySrcPath(t))
	if err != nil {
		t.Fatal(err)
	}
	re := regexp.MustCompile(`unlink|rmtree|os\.remove|rm -`)
	for _, line := range strings.Split(string(src), "\n") {
		if strings.Contains(line, "prompt.md") && re.MatchString(line) {
			t.Fatalf("factory_cli.py must not delete prompt files: %q", line)
		}
	}
}

func TestRelaunchIdleColumnsAddedOnFirstCommand(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	for _, c := range []string{"relaunch_requested_at", "relaunch_requested_by"} {
		sqlExec(t, r.Env, "ALTER TABLE agents DROP COLUMN "+c)
	}
	cols := r.agentCols(t)
	if _, ok := cols["relaunch_requested_at"]; ok {
		t.Fatal("relaunch_requested_at still present after DROP")
	}
	if _, ok := cols["relaunch_requested_by"]; ok {
		t.Fatal("relaunch_requested_by still present after DROP")
	}
	r.NeedPython()
	r.OK("agents")
	cols = r.agentCols(t)
	if _, ok := cols["relaunch_requested_at"]; !ok {
		t.Fatal("relaunch_requested_at missing after agents")
	}
	if _, ok := cols["relaunch_requested_by"]; !ok {
		t.Fatal("relaunch_requested_by missing after agents")
	}
	r.OK("agents")
	wantLen(t, "lead row", r.SQL("SELECT 1 FROM agents WHERE name=?", riLead), 1)
}

// PlacementRelaunchTests (T-1585): the placement floor on the tick's idle relaunch and on an immediate relaunch.

func TestRelaunchIdlePlacementFloorDefersALiveLeadWithoutClosing(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.riArm(t)
	r.queue(t, "zz-director")
	r.OKWith(ft.Opts{Agent: "zz-director", Env: riLowHQ}, "tick")
	wantEmpty(t, "closes", r.closes())
	wantEmpty(t, "starts", r.starts())
	at, _ := r.relaunchFlag(t, "")
	expectFlagTimestamp(t, at)
	row := r.SQL("SELECT status, pane_id FROM agents WHERE name=?", riLead)[0]
	if row[0] != "live" || row[1] != riPane {
		t.Fatalf("agent row = %v, want live/%s", row, riPane)
	}
	wantLen(t, "relaunch-deferred events", r.eventDetails(t, "relaunch-deferred"), 1)
}

func TestRelaunchIdleImmediateRelaunchUnderPlacementFloorKeepsThePane(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	r.riArm(t)
	r.NeedPython()
	p := r.FailWith(ft.Opts{Agent: "zz-director", Env: riLowHQ}, 3, "relaunch", riLead, "--force")
	contains(t, "stderr", p.Stderr, "not relaunching "+riLead)
	contains(t, "stderr", p.Stderr, "floor")
	wantEmpty(t, "closes", r.closes())
	row := r.SQL("SELECT status, pane_id FROM agents WHERE name=?", riLead)[0]
	if row[0] != "live" || row[1] != riPane {
		t.Fatalf("agent row = %v, want live/%s", row, riPane)
	}
}

// FeatureOffRelaunchTests: worker_machines empty, the tick's idle relaunch uses gate(own_slot=True) and nothing else.

func TestRelaunchIdleFeatureOffCursorFiveHourDefersIdleRelaunch(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	name := "cursor-coder-zz"
	r.riFeatureOffAgent(t, name, "coder", "cursor")
	r.writeUsage(t, 99, 0, 0)
	r.OKWith(ft.Opts{Agent: "zz-director", Env: riRoomy}, "tick")
	wantEmpty(t, "closes", r.closes())
	at, _ := r.relaunchFlag(t, name)
	expectFlagTimestamp(t, at)
	rows := r.riDeferred(name)
	wantLen(t, "relaunch-deferred events", rows, 1)
	contains(t, "deferred detail", rows[0][0], "5h usage")
}

func TestRelaunchIdleFeatureOffCursorIdleRelaunchRunsWhenFiveHourIsOpen(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	name := "cursor-coder-open"
	r.riFeatureOffAgent(t, name, "coder", "cursor")
	r.writeUsage(t, 10, 0, 0)
	r.OKWith(ft.Opts{Agent: "zz-director", Env: riRoomy}, "tick")
	wantEmpty(t, "relaunch-deferred events", r.riDeferred(name))
	wantLen(t, "relaunch-run events", r.riRan(name), 1)
	at, by := r.relaunchFlag(t, name)
	expectFlagClearPair(t, at, by)
}

func TestRelaunchIdleFeatureOffCleanerTmpCapDefersIdleRelaunch(t *testing.T) {
	r := newRelaunchIdleEnv(t)
	name := "zz-cleaner-zz"
	r.riFeatureOffAgent(t, name, "cleaner", "")
	env := map[string]string{"FACTORY_TMP_STATVFS": "1000000000,50000000"}
	for k, v := range riRoomy {
		env[k] = v
	}
	r.OKWith(ft.Opts{Agent: "zz-director", Env: env}, "tick")
	wantEmpty(t, "closes", r.closes())
	at, _ := r.relaunchFlag(t, name)
	expectFlagTimestamp(t, at)
	rows := r.riDeferred(name)
	wantLen(t, "relaunch-deferred events", rows, 1)
	contains(t, "deferred detail", rows[0][0], "/tmp")
}
