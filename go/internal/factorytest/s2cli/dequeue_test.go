package s2cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Partial Go tests for tests/test_dequeue.py (7 of 52 cases, no line in ported_s2cli.txt: the module stays a follow-up, because the 45 stateful-herdr cases need a stateful herdr stub).
//
// The Go stub (go/internal/factorytest/stub) is scripted and stateless: it cannot keep the agent/pane/tab state
// tests/stubs/herdr keeps, nor run FAKE_HERDR_HOOK. So these Python cases are NOT ported (they need a started agent
// to appear in `agent list`, a hook that runs a second CLI call in the middle of `agent start`/`tab create`, or the
// Python interpreter's spawn()/queue_order_key() directly):
//   OrderTests (all 26 cases) - the tick starts the first queued row through a stateful herdr and the test reads
//     `agent start` calls and the live/queued split; the order itself is Python queue_order_key.
//   DequeueCmdTests: test_cancelled_never_started_and_not_counted, test_gate_ignores_cancelled,
//     test_dequeue_during_earlier_spawn_is_honoured, test_dequeue_during_place_is_refused_not_started_and_cancelled,
//     test_failed_start_returns_claim_to_queue, test_starting_row_is_refused_by_dequeue_and_recovered_after_ttl,
//     test_up_does_not_relaunch_cancelled_lead_or_director, test_respawn_after_cancel, test_requeue_after_cancel,
//     test_gate_closed_from_queue_upsert_leaves_cancelled_row_alone, test_manual_spawn_of_starting_row_exits_2,
//     the six T-0130 queued_at tests, test_help_lists_dequeue (Python argparse --help) - spawn/tick/up through the
//     stateful herdr, hooks, or ns.spawn() called in-process.
// The four DequeueCmd cases call NeedPython through queue() (spawn is not ported): they skip visibly where python3
// is missing.
// A follow-up needs a stateful herdr stub in the Go harness (or the Go spawn/tick) before they can move.

type dqEnv struct {
	*ft.Env
}

// newDequeueEnv is DequeueBase.setUp.
func newDequeueEnv(t *testing.T) *dqEnv {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-003", "", "zz-test goal")
	sqlExec(t, e, "UPDATE goals SET priority=3 WHERE id='G-003'")
	sqlExec(t, e, "UPDATE goals SET priority=1 WHERE id='G-001'")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	e.SeedTeam("g-001-b", "G-001", "zz-lead-b")
	e.SeedTask(ft.Task{ID: "T-0001", Goal: "G-001", Role: "coder", Status: "todo", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director", Goal: "G-001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-lead-b", Role: "lead", Goal: "G-001", Team: "g-001-b"})
	e.SeedAgent(ft.AgentRow{Name: "zz-coder", Role: "coder", Goal: "G-001", Team: "g-001-a"})
	return &dqEnv{e}
}

// herdrDown scripts every herdr call to fail (set_herdr(fail=True)); herdrUp restores the default answers.
func (d *dqEnv) herdrDown() { d.Script("herdr", ft.StubRule{Exit: 1, Err: "herdr: unreachable\n"}) }
func (d *dqEnv) herdrUp()   { d.Script("herdr") }

func (d *dqEnv) agentStatus(t *testing.T, name string) string {
	t.Helper()
	rows := d.SQL("SELECT status FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	return rows[0][0]
}

// queue is DequeueBase.queue: queue a spawn while herdr is unreachable (the gate is closed), then pin its queued_at.
func (d *dqEnv) queue(t *testing.T, name, role, goal, queuedAt, team string) {
	t.Helper()
	d.herdrDown()
	args := []string{"spawn", role, "--name", name}
	if goal != "" {
		args = append(args, "--goal", goal)
	}
	if team != "" {
		args = append(args, "--team", team)
	}
	d.NeedPython() // spawn is not ported
	contains(t, "spawn stdout", d.OK(args...).Stdout, "QUEUED")
	sqlExec(t, d.Env, "UPDATE agents SET queued_at=? WHERE name=?", queuedAt, name)
	d.herdrUp()
}

// at is at(): a queued_at relative to the real clock (aging reads it).
func at(n int) string {
	return time.Now().Add(-time.Duration(100-n) * time.Second).Format("2006-01-02T15:04:05-07:00")
}

func (d *dqEnv) prio(t *testing.T, id string) string {
	t.Helper()
	return d.SQL("SELECT priority FROM goals WHERE id=?", id)[0][0]
}

func TestDequeueGoalPriorityEditPriority2Sets2(t *testing.T) {
	d := newDequeueEnv(t)
	if got := d.prio(t, "G-001"); got != "1" {
		t.Fatalf("priority = %s, want 1", got)
	}
	d.OKWith(ft.Opts{Agent: "zz-director"}, "goal", "edit", "G-001", "--priority", "2")
	if got := d.prio(t, "G-001"); got != "2" {
		t.Fatalf("priority = %s, want 2", got)
	}
}

func TestDequeueGoalPriorityEditWithoutPriorityChangesNothing(t *testing.T) {
	d := newDequeueEnv(t)
	d.FailWith(ft.Opts{Agent: "zz-director"}, 1, "goal", "edit", "G-001")
	d.OKWith(ft.Opts{Agent: "zz-director"}, "goal", "edit", "G-001", "--title", "zz-test renamed")
	if got := d.prio(t, "G-001"); got != "1" {
		t.Fatalf("priority = %s, want 1", got)
	}
}

func TestDequeueGoalPriorityNewDefaultsTo2AndExplicitIsKept(t *testing.T) {
	d := newDequeueEnv(t)
	gid := strings.TrimSpace(d.OKWith(ft.Opts{Agent: "owner"}, "goal", "new", "--title", "zz-test default").Stdout)
	if got := d.prio(t, gid); got != "2" {
		t.Fatalf("%s priority = %s, want 2", gid, got)
	}
	gid = strings.TrimSpace(d.OKWith(ft.Opts{Agent: "owner"}, "goal", "new", "--title", "zz-test p1", "--priority", "1").Stdout)
	if got := d.prio(t, gid); got != "1" {
		t.Fatalf("%s priority = %s, want 1", gid, got)
	}
}

// newDequeueCmdEnv is DequeueCmdTests.setUp.
func newDequeueCmdEnv(t *testing.T) *dqEnv {
	t.Helper()
	d := newDequeueEnv(t)
	d.queue(t, "zz-q", "pm", "G-001", at(1), "g-001-a")
	sqlExec(t, d.Env, "UPDATE agents SET team_id='g-001-a', task_id='T-0001' WHERE name='zz-q'")
	d.OKWith(ft.Opts{}, "config", "max_live_agents", "5")
	return d
}

func (d *dqEnv) dq(t *testing.T, agent string, code int, args ...string) ft.Result {
	t.Helper()
	return d.FailWith(ft.Opts{Agent: agent}, code, append([]string{"dequeue"}, args...)...)
}

func TestDequeueCmdDirectorCancelsQueued(t *testing.T) {
	d := newDequeueCmdEnv(t)
	before := d.SQL("SELECT status FROM tasks WHERE id='T-0001'")[0][0]
	ncalls := len(d.Calls("herdr"))
	d.dq(t, "zz-director", 0, "zz-q", "--reason", "not needed")
	if got := d.agentStatus(t, "zz-q"); got != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got)
	}
	ev := d.SQL("SELECT detail FROM events WHERE action='dequeued' AND entity_id='zz-q'")
	if len(ev) != 1 {
		t.Fatalf("dequeued events = %v, want one", ev)
	}
	var detail map[string]string
	if err := json.Unmarshal([]byte(ev[0][0]), &detail); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(detail) != fmt.Sprint(map[string]string{"by": "zz-director", "reason": "not needed"}) {
		t.Fatalf("detail = %v", detail)
	}
	if got := len(d.Calls("herdr")); got != ncalls {
		t.Fatalf("herdr calls %d -> %d: a dequeue must not touch herdr", ncalls, got)
	}
	if got := d.SQL("SELECT status FROM tasks WHERE id='T-0001'")[0][0]; got != before {
		t.Fatalf("task status %s -> %s", before, got)
	}
	if got := d.Count("SELECT count(*) FROM messages"); got != 0 {
		t.Fatalf("messages = %d, want 0", got)
	}
}

func TestDequeueCmdSameTeamLeadOkOtherTeamExit3(t *testing.T) {
	d := newDequeueCmdEnv(t)
	r := d.dq(t, "zz-lead-b", 3, "zz-q")
	contains(t, "stderr", r.Stderr, "not in your team")
	if got := d.agentStatus(t, "zz-q"); got != "queued" {
		t.Fatalf("status = %s, want queued", got)
	}
	d.dq(t, "zz-lead", 0, "zz-q")
	if got := d.agentStatus(t, "zz-q"); got != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got)
	}
}

func TestDequeueCmdWorkerRefused(t *testing.T) {
	d := newDequeueCmdEnv(t)
	r := d.dq(t, "zz-coder", 3, "zz-q")
	contains(t, "stderr", r.Stderr, "workers may not run `factory dequeue`")
	if got := d.agentStatus(t, "zz-q"); got != "queued" {
		t.Fatalf("status = %s, want queued", got)
	}
}

func TestDequeueCmdLiveUnknownAndRepeatExit2(t *testing.T) {
	d := newDequeueCmdEnv(t)
	r := d.dq(t, "zz-director", 2, "zz-coder")
	contains(t, "stderr", r.Stderr, "not queued")
	d.dq(t, "zz-director", 2, "zz-nobody")
	d.dq(t, "zz-director", 0, "zz-q")
	d.dq(t, "zz-director", 2, "zz-q")
}
