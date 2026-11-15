package factorytest

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
)

// Port of tests/test_identity.py (22 cases; see ported.txt). The Python module stays as the oracle. Cases whose
// command the Go CLI does not answer itself (judge, serve, park, tick, and the worker-detection paths that record
// events) call NeedPython: they run where python3 exists and skip, visibly, where it does not.

const identityPane = "w9:p9"

var (
	inPane     = map[string]string{"CLAUDECODE": "1", "HERDR_PANE_ID": identityPane}
	inClaude   = map[string]string{"CLAUDECODE": "1"}
	needsAgent = "FACTORY_AGENT=<your agent name>, or FACTORY_AGENT=owner if you are the owner"
)

// identityEnv is IdentityTests.setUp: goal G-001, team g-001-a led by zz-test-lead, task T-0001 in review.
func identityEnv(t *testing.T) *Env {
	t.Helper()
	e := New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedTask(Task{ID: "T-0001", Role: "coder", Status: "in_review", Team: "g-001-a"})
	return e
}

func (e *Env) events(action string) [][]string {
	e.T.Helper()
	return e.SQL("SELECT actor, entity, entity_id, action, detail FROM events WHERE action=?", action)
}

func extraEnvs(maps ...map[string]string) []map[string]string { return maps }

func contains(t *testing.T, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func TestIdentityUnsetInPaneRecoversFromPaneID(t *testing.T) {
	e := identityEnv(t)
	e.SeedAgent(AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a", Task: "T-0001", PaneID: identityPane})
	r := e.OKWith(Opts{Env: inPane}, "whoami")
	contains(t, "stdout", r.Stdout, "zz-test-coder")
	contains(t, "stdout", r.Stdout, "role=coder")
	contains(t, "stderr", r.Stderr, "FACTORY_AGENT")
	// recovered as coder: role gates apply
	e.FailWith(Opts{Env: inPane}, 3, "goal", "new", "--title", "zz-test")
}

func TestIdentityUnsetInPaneNoMatchRefusesWrite(t *testing.T) {
	e := identityEnv(t)
	for _, extra := range extraEnvs(inClaude, inPane) {
		r := e.RunWith(Opts{Env: extra}, "whoami")
		if r.Exit != 0 {
			t.Fatalf("whoami exit %d: %s", r.Exit, r.Stderr)
		}
		if strings.Contains(r.Stdout, "not a factory agent") || !strings.HasPrefix(r.Stdout, "unknown") {
			t.Fatalf("whoami stdout = %q, want it to start with unknown", r.Stdout)
		}
		w := e.FailWith(Opts{Env: extra}, 3, "goal", "new", "--title", "zz-test")
		contains(t, "stderr", w.Stderr, needsAgent)
	}
	if n := e.Count("SELECT count(*) FROM goals"); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
}

func TestIdentityUnknownServeNotRefused(t *testing.T) {
	e := identityEnv(t)
	e.NeedPython() // serve is not ported
	// serve is read-only: with an unknown identity it fails on its own terms (bad bind), never exit 3
	for _, extra := range extraEnvs(inClaude, inPane) {
		r := e.RunWith(Opts{Env: extra}, "serve", "--bind", "0.0.0.0")
		if r.Exit == 3 {
			t.Fatalf("serve exit 3: %s", r.Stderr)
		}
		if strings.Contains(r.Stderr, "identity unknown") {
			t.Fatalf("serve refused as unknown: %s", r.Stderr)
		}
	}
}

func TestIdentityOwnerShellUnchanged(t *testing.T) {
	e := identityEnv(t)
	contains(t, "whoami", e.OK("whoami").Stdout, "owner")
	e.OK("goal", "new", "--title", "zz-test owner goal")
}

func TestIdentityUnsetInPaneCannotMerge(t *testing.T) {
	e := identityEnv(t)
	e.NeedPython() // judge is not ported
	e.SeedAgent(AgentRow{Name: "zz-test-judge", Role: "judge", Goal: "G-001", Team: "g-001-a", PaneID: "w1:p1"})
	for _, extra := range extraEnvs(inClaude, inPane) {
		e.FailWith(Opts{Env: extra}, 3, "judge", "T-0001", "--merge", "--reason", "zz-test")
	}
	if st := e.SQL("SELECT status FROM tasks WHERE id='T-0001'")[0][0]; st != "in_review" {
		t.Fatalf("task status = %s, want in_review", st)
	}
	if ev := e.events("merged"); len(ev) != 0 {
		t.Fatalf("merged events: %v", ev)
	}
}

func TestIdentityExplicitOwnerInAgentContext(t *testing.T) {
	e := identityEnv(t)
	contains(t, "whoami", e.OKWith(Opts{Agent: "owner", Env: inPane}, "whoami").Stdout, "owner")
	e.OKWith(Opts{Agent: "owner", Env: inClaude}, "goal", "new", "--title", "zz-test owner goal")
}

func TestIdentityUnknownInboxNoSideEffects(t *testing.T) {
	e := identityEnv(t)
	e.NeedPython() // inbox with an unknown identity is handed to Python
	e.SeedAgent(AgentRow{Name: "zz-test-coder", Role: "coder", PaneID: "w1:p1"})
	e.OKWith(Opts{Agent: "owner"}, "msg", "zz-test-coder", "hello", "body")
	unread := func() int { return e.Count("SELECT count(*) FROM messages WHERE read_at IS NULL") }
	if n := unread(); n != 1 {
		t.Fatalf("unread = %d, want 1", n)
	}
	for _, extra := range extraEnvs(inClaude, inPane) {
		if r := e.OKWith(Opts{Env: extra}, "inbox"); r.Stdout != "" {
			t.Fatalf("inbox stdout = %q, want empty", r.Stdout)
		}
		if r := e.OKWith(Opts{Env: extra}, "inbox", "--agent", "zz-test-coder"); r.Stdout != "" {
			t.Fatalf("inbox --agent stdout = %q, want empty", r.Stdout)
		}
	}
	if n := unread(); n != 1 {
		t.Fatalf("unread after inbox = %d, want 1", n)
	}
}

func TestIdentityAgentPaneClaimingOwnerRefused(t *testing.T) {
	e := identityEnv(t)
	e.SeedAgent(AgentRow{Name: "zz-test-coder", Role: "coder", Goal: "G-001", Team: "g-001-a", PaneID: identityPane})
	r := e.FailWith(Opts{Agent: "owner", Env: inPane}, 3, "goal", "new", "--title", "zz-test")
	contains(t, "stderr", r.Stderr, "zz-test-coder")
	ev := e.events("identity-spoof")
	if len(ev) != 1 {
		t.Fatalf("identity-spoof events = %v, want 1", ev)
	}
	contains(t, "detail", ev[0][4], identityPane)
	if ev[0][2] != "zz-test-coder" {
		t.Fatalf("entity_id = %s, want zz-test-coder", ev[0][2])
	}
	if n := e.Count("SELECT count(*) FROM goals"); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
}

func TestIdentityStalePaneMayClaimOwner(t *testing.T) {
	e := identityEnv(t)
	// statuses that no longer own a pane: exited (reaped), lost, queued with a stale pane_id
	for i, st := range []string{"exited", "lost", "queued"} {
		e.SeedAgent(AgentRow{Name: "zz-test-old" + string(rune('0'+i)), Role: "coder", Status: st, PaneID: identityPane})
		e.OKWith(Opts{Agent: "owner", Env: inPane}, "goal", "new", "--title", "zz-test "+st)
		e.OKWith(Opts{Agent: "owner", Env: inPane}, "whoami")
	}
	if ev := e.events("identity-spoof"); len(ev) != 0 {
		t.Fatalf("identity-spoof events: %v", ev)
	}
}

func TestIdentityDonePaneClaimingOwnerRefused(t *testing.T) {
	e := identityEnv(t)
	e.SeedAgent(AgentRow{Name: "zz-test-done", Role: "coder", Status: "done", PaneID: identityPane})
	e.FailWith(Opts{Agent: "owner", Env: inPane}, 3, "whoami")
	ev := e.events("identity-spoof")
	if len(ev) != 1 {
		t.Fatalf("identity-spoof events = %v, want 1", ev)
	}
	if ev[0][0] != "pane:"+identityPane {
		t.Fatalf("actor = %s, want pane:%s", ev[0][0], identityPane)
	}
}

func TestIdentityStalePaneNotRecovered(t *testing.T) {
	e := identityEnv(t)
	e.SeedAgent(AgentRow{Name: "zz-test-old", Role: "coder", Status: "exited", PaneID: identityPane})
	if out := e.OKWith(Opts{Env: inPane}, "whoami").Stdout; !strings.HasPrefix(out, "unknown") {
		t.Fatalf("whoami = %q, want unknown", out)
	}
}

func TestIdentityExplicitAgentUnchangedInPane(t *testing.T) {
	e := identityEnv(t)
	e.SeedAgent(AgentRow{Name: "zz-test-director", Role: "director", PaneID: "w1:p1"})
	r := e.OKWith(Opts{Agent: "zz-test-director", Env: inPane}, "whoami")
	contains(t, "stdout", r.Stdout, "role=director")
	if r.Stderr != "" {
		t.Fatalf("stderr = %q, want empty", r.Stderr)
	}
}

// ---- StalePaneRowTests (T-1292): a stale live worker row on a reused pane id must not make an HQ agent a worker ----

// staleEnv is StalePaneRowTests.setUp: lead zz-test-lead and a reviewer whose live row sits on the lead's pane id.
func staleEnv(t *testing.T) *Env {
	t.Helper()
	e := New(t)
	e.NeedPython() // worker detection on a pane with a live worker row is decided (and recorded) by Python
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(AgentRow{Name: "zz-test-reviewer", Role: "reviewer", Goal: "G-001", Team: "g-001-a", PaneID: identityPane})
	e.exec("UPDATE agents SET session_id='sess-rev' WHERE name='zz-test-reviewer'")
	e.exec("UPDATE agents SET session_id='sess-lead' WHERE name='zz-test-lead'")
	return e
}

// ancestry makes the test process (the parent of every CLI run) a child of a fake `claude --resume <session>`.
func (e *Env) ancestry(session string) {
	e.T.Helper()
	e.FakeClaude(4201, 1, "claude", "--resume", session)
	e.FakeProc(os.Getpid(), "factory-test", 4201, "factory-test")
}

var workerRefusal = regexp.MustCompile(`role '(\w+)' \(\S+\) may not do this: workers may not run`)

// workerOf is caller_is_worker seen through the CLI: `goal status` is refused with "role '<r>' ... workers may not run"
// when the caller counts as a worker, and with another message (or succeeds) when it does not.
func (e *Env) workerOf(agent string) string {
	e.T.Helper()
	r := e.RunWith(Opts{Agent: agent, Env: map[string]string{"HERDR_PANE_ID": identityPane, "CLAUDECODE": "1", "FACTORY_ROLE": ""}},
		"goal", "status", "G-001", "active")
	if m := workerRefusal.FindStringSubmatch(r.Stderr); m != nil {
		return m[1]
	}
	return ""
}

func TestIdentityLeadOnReusedPaneWithStaleReviewerRowIsNotWorker(t *testing.T) {
	e := staleEnv(t)
	e.ancestry("sess-lead")
	// the T-1292 incident: the reviewer's claude is gone, its row stayed live on the pane id the lead now holds
	if w := e.workerOf("zz-test-lead"); w != "" {
		t.Fatalf("lead counted as worker %q", w)
	}
	ev := e.events("identity-conflict")
	if len(ev) != 1 {
		t.Fatalf("identity-conflict events = %v, want 1", ev)
	}
	contains(t, "detail", ev[0][4], "zz-test-reviewer")
	contains(t, "detail", ev[0][4], "zz-test-lead")
	if len(e.events("identity-spoof")) != 0 {
		t.Fatal("identity-spoof recorded")
	}
	if w := e.workerOf("zz-test-lead"); w != "" { // asking again records no second event
		t.Fatalf("second ask: worker %q", w)
	}
	if n := len(e.events("identity-conflict")); n != 1 {
		t.Fatalf("identity-conflict events = %d, want 1", n)
	}
}

func TestIdentityLeadOnReusedPaneCanRunParkViaTheCLI(t *testing.T) {
	e := staleEnv(t)
	e.ancestry("sess-lead")
	r := e.RunWith(Opts{Agent: "zz-test-lead", Env: map[string]string{"HERDR_PANE_ID": identityPane, "CLAUDECODE": "1"}}, "park", "zz-test note")
	if strings.Contains(r.Stderr, "workers may not run") {
		t.Fatalf("park refused as worker: %s", r.Stderr)
	}
}

func TestIdentityWorkerForgingHQEnvInItsOwnPaneStillRefused(t *testing.T) {
	e := staleEnv(t)
	// a live worker: a claude process runs its session, so the row is not stale whatever env it forges
	e.FakeClaude(4101, 1, "claude", "--resume", "sess-rev")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
	if n := len(e.events("identity-spoof")); n != 1 {
		t.Fatalf("identity-spoof events = %d, want 1", n)
	}
	if n := len(e.events("identity-conflict")); n != 0 {
		t.Fatalf("identity-conflict events = %d, want 0", n)
	}
	e.FailWith(Opts{Agent: "zz-test-lead", Env: map[string]string{"HERDR_PANE_ID": identityPane, "CLAUDECODE": "1"}}, 3, "park", "zz-test note")
}

func TestIdentityForgerWhoseSessionDriftedFromItsRowStillRefused(t *testing.T) {
	e := staleEnv(t)
	// no claude runs the row's stored session (drift), but the caller's claude is not the lead's session
	e.ancestry("sess-drifted-worker")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
	if n := len(e.events("identity-conflict")); n != 0 {
		t.Fatalf("identity-conflict events = %d, want 0", n)
	}
}

func TestIdentityCursorRuntimeWorkerRowIsNeverStale(t *testing.T) {
	e := staleEnv(t)
	e.exec(`UPDATE agents SET spawn_args='{"runtime": "cursor"}' WHERE name='zz-test-reviewer'`)
	e.ancestry("sess-lead")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
}

func TestIdentityWorkerRowWithoutSessionIDFailsClosed(t *testing.T) {
	e := staleEnv(t)
	e.exec("UPDATE agents SET session_id=NULL WHERE name='zz-test-reviewer'")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
}

func TestIdentityRealIncidentRowSessionOverwrittenWithLeadsOwnSession(t *testing.T) {
	e := staleEnv(t)
	// the stale reviewer row's session_id equals the lead's session; the lead's claude runs it
	e.exec("UPDATE agents SET session_id='sess-lead' WHERE name='zz-test-reviewer'")
	e.ancestry("sess-lead")
	if w := e.workerOf("zz-test-lead"); w != "" {
		t.Fatalf("lead counted as worker %q", w)
	}
	if n := len(e.events("identity-conflict")); n != 1 {
		t.Fatalf("identity-conflict events = %d, want 1", n)
	}
}

func TestIdentityForgerWithRowSessionEqualToLeadsButOwnAncestryRefused(t *testing.T) {
	e := staleEnv(t)
	e.exec("UPDATE agents SET session_id='sess-lead' WHERE name='zz-test-reviewer'")
	e.ancestry("sess-forger")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
}

func TestIdentityTickOnReusedPaneNeverRewritesAnotherRowsSessionID(t *testing.T) {
	e := staleEnv(t)
	e.exec("UPDATE agents SET status='live', pane_id=? WHERE name='zz-test-lead'", identityPane)
	list, err := json.Marshal(map[string]any{"id": "fake", "result": map[string]any{"agents": []any{map[string]any{
		"name": "zz-test-lead", "agent": "claude", "pane_id": identityPane, "agent_status": "idle",
		"agent_session": map[string]any{"value": "sess-lead"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	e.Script("herdr", StubRule{Match: []string{"agent list"}, Out: string(list) + "\n"})
	e.OK("tick")
	if s := e.SQL("SELECT session_id FROM agents WHERE name='zz-test-reviewer'")[0][0]; s != "sess-rev" {
		t.Fatalf("reviewer session_id = %s, want sess-rev", s)
	}
	if ev := e.events("session-mismatch"); len(ev) != 0 {
		t.Fatalf("session-mismatch events: %v", ev)
	}
}

func TestIdentityStoppedRowNoLongerMatchesItsPaneID(t *testing.T) {
	e := staleEnv(t)
	e.FakeClaude(4101, 1, "claude", "--resume", "sess-rev")
	if w := e.workerOf("zz-test-lead"); w != "reviewer" {
		t.Fatalf("worker = %q, want reviewer", w)
	}
	e.exec("UPDATE agents SET status='stopped' WHERE name='zz-test-reviewer'")
	if w := e.workerOf("zz-test-lead"); w != "" {
		t.Fatalf("worker = %q, want none", w)
	}
	if n := len(e.events("identity-conflict")); n != 0 {
		t.Fatalf("identity-conflict events = %d, want 0", n)
	}
}
