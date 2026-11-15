package s2cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Go tests for tests/test_dequeue.py: all 52 cases (OrderTests 28, GoalPriorityTests 3, DequeueCmdTests 21), the
// counterpart of the Python module. They run against the stateful herdr stub (Env.HerdrStateful), like the Python
// harness's tests/stubs/herdr, so a started agent shows up in the next `agent list`.
//
// Cases that call Python in-process in the original (ns.spawn(... from_queue=True) and queue_order_key) are the same
// scenario through the CLI: the rows are seeded as in the original and `factory tick`/`spawn`/`dequeue` is driven
// (see the comment of each test). spawn, tick, up, res and agents still run in Python: the CLI hands them to
// bin/factory (Python), so those tests call NeedPython (through queue() or directly) and skip visibly where python3
// is missing. The Python module stays as the parity oracle until P7a.

type dqEnv struct {
	*ft.Env
}

// dqSlots bounds how many dequeue tests run at once: each CLI call that Go hands to Python starts an interpreter, and
// the box is shared (3 at a time keep the module faster than the Python original without loading it).
var dqSlots = make(chan struct{}, 3)

// newDequeueEnv is DequeueBase.setUp. Every test is independent (its own home, DB, stub state), so they run in parallel.
func newDequeueEnv(t *testing.T) *dqEnv {
	t.Helper()
	t.Parallel()
	dqSlots <- struct{}{}
	t.Cleanup(func() { <-dqSlots })
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'") // the Python harness pins the TUI (both stubs are on PATH and auto prefers hc)
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

// herdrDown makes every herdr call fail (set_herdr(fail=True)); herdrUp restores it.
func (d *dqEnv) herdrDown() { d.HerdrFail(true) }
func (d *dqEnv) herdrUp()   { d.HerdrFail(false) }

func (d *dqEnv) agentStatus(t *testing.T, name string) string {
	t.Helper()
	rows := d.SQL("SELECT status FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	return rows[0][0]
}

// statuses is the status of each named agent, in order.
func (d *dqEnv) statuses(t *testing.T, names ...string) []string {
	t.Helper()
	var out []string
	for _, n := range names {
		out = append(out, d.agentStatus(t, n))
	}
	return out
}

func (d *dqEnv) wantStatuses(t *testing.T, names []string, want ...string) {
	t.Helper()
	if got := d.statuses(t, names...); !reflect.DeepEqual(got, want) {
		t.Fatalf("status of %v = %v, want %v", names, got, want)
	}
}

// starts is the agent names of the logged `agent start` calls (DequeueBase.starts()[i][2]).
func (d *dqEnv) starts() []string {
	out := []string{}
	for _, a := range d.HerdrStarts() {
		out = append(out, a[2])
	}
	return out
}

func (d *dqEnv) wantStarts(t *testing.T, want ...string) {
	t.Helper()
	if want == nil {
		want = []string{}
	}
	if got := d.starts(); !reflect.DeepEqual(got, want) {
		t.Fatalf("agent start calls = %v, want %v", got, want)
	}
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
	d.NeedPython() // spawn still runs in Python
	contains(t, "spawn stdout", d.OK(args...).Stdout, "QUEUED")
	sqlExec(t, d.Env, "UPDATE agents SET queued_at=? WHERE name=?", queuedAt, name)
	d.herdrUp()
}

// at is at(n, mins): a queued_at relative to the real clock (aging reads it): n orders rows of the same age, mins ages them.
func at(n int, mins ...int) string {
	m := 0
	if len(mins) > 0 {
		m = mins[0]
	}
	return time.Now().Add(-time.Duration(m)*time.Minute - time.Duration(100-n)*time.Second).Format("2006-01-02T15:04:05-07:00")
}

func (d *dqEnv) prio(t *testing.T, id string) string {
	t.Helper()
	return d.SQL("SELECT priority FROM goals WHERE id=?", id)[0][0]
}

// tickOne is DequeueBase.tick_one: room for one agent, one tick.
func (d *dqEnv) tickOne(t *testing.T) {
	t.Helper()
	d.OK("config", "max_live_agents", "1")
	d.OK("tick")
}

// first is OrderTests.first: tick once; the first name must be live and the rest still queued.
func (d *dqEnv) first(t *testing.T, names ...string) {
	t.Helper()
	d.tickOne(t)
	want := []string{"live"}
	for range names[1:] {
		want = append(want, "queued")
	}
	d.wantStatuses(t, names, want...)
}

func (d *dqEnv) setPrompt(t *testing.T, name, prompt string) {
	t.Helper()
	sqlExec(t, d.Env, "UPDATE agents SET spawn_args=json_set(spawn_args,'$.prompt',?) WHERE name=?", prompt, name)
}

func TestDequeueOrderP1PmBeatsEarlierP3Worker(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-c3", "pm", "G-003", at(1), "")
	d.queue(t, "zz-p1", "pm", "G-001", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-p1", "zz-c3"}, "live", "queued")
	if n := len(d.starts()); n != 1 {
		t.Fatalf("agent start calls = %d, want 1", n)
	}
}

func TestDequeueOrderLeadBeatsP1Worker(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-p1", "pm", "G-001", at(1), "")
	d.queue(t, "zz-ld", "lead", "G-003", at(2), "g-001-a")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-ld", "zz-p1"}, "live", "queued")
}

func TestDequeueOrderJudgeBeatsP1NonJudge(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-p1", "pm", "G-001", at(1), "")
	d.queue(t, "zz-jd", "judge", "G-003", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-jd", "zz-p1"}, "live", "queued")
}

func TestDequeueOrderSamePriorityFallsBackToQueuedAt(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-late", "pm", "G-001", at(9), "")
	d.queue(t, "zz-early", "pm", "G-001", at(1), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-early", "zz-late"}, "live", "queued")
}

func TestDequeueOrderNullGoalCountsAsPriority2(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "pm", "G-003", at(1), "")
	d.queue(t, "zz-none", "pm", "", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-none", "zz-p3"}, "live", "queued")
}

// T-0094: the old 4-class order (director, lead, judge, rest) is now 6 classes.
func TestDequeueOrderP3ReviewerBeatsP1Coder(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-co", "coder", "G-001", at(1), "")
	d.queue(t, "zz-rv", "reviewer", "G-003", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-rv", "zz-co"}, "live", "queued")
}

func TestDequeueOrderQaBeatsCoder(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-co", "coder", "G-001", at(1), "")
	d.queue(t, "zz-qa", "qa", "G-003", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-qa", "zz-co"}, "live", "queued")
}

func TestDequeueOrderJudgeBeatsReviewerBeatsQa(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-qa", "qa", "G-001", at(1), "")
	d.queue(t, "zz-rv", "reviewer", "G-001", at(2), "")
	d.queue(t, "zz-jd", "judge", "G-003", at(3), "")
	sqlExec(t, d.Env, "UPDATE agents SET status='done' WHERE name IN ('zz-lead','zz-lead-b','zz-coder','zz-director')")
	d.OK("config", "max_live_agents", "1")
	for _, expect := range []string{"zz-jd", "zz-rv", "zz-qa"} {
		d.OK("tick")
		if got := d.agentStatus(t, expect); got != "live" {
			t.Fatalf("%s = %s, want live", expect, got)
		}
		sqlExec(t, d.Env, "UPDATE agents SET status='done' WHERE name=?", expect)
	}
}

func TestDequeueOrderGoalPriorityDecidesWithinClass(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-r3", "reviewer", "G-003", at(1), "")
	d.queue(t, "zz-r1", "reviewer", "G-001", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-r1", "zz-r3"}, "live", "queued")
}

func TestDequeueOrderAgedP3ReviewerBeatsNewP1Reviewer(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-old", "reviewer", "G-003", at(1, 91), "")
	d.queue(t, "zz-new", "reviewer", "G-001", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-old", "zz-new"}, "live", "queued")
}

func TestDequeueOrderYoungP3ReviewerDoesNotAgePastP1(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-old", "reviewer", "G-003", at(1, 29), "")
	d.queue(t, "zz-new", "reviewer", "G-001", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-new", "zz-old"}, "live", "queued")
}

func TestDequeueOrderAgedP3CoderStillLosesToAnyReviewer(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-co", "coder", "G-003", at(1, 300), "")
	d.queue(t, "zz-rv", "reviewer", "G-003", at(2), "")
	d.tickOne(t)
	d.wantStatuses(t, []string{"zz-rv", "zz-co"}, "live", "queued")
}

// T-0183: aging never erases priority (see queue_order_key)
func TestDequeueOrderAgedP1BeatsOlderP3(t *testing.T) { // (c) P1 waited 30 min, the P3 was queued 2 h before it
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 180), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2, 31), "")
	d.first(t, "zz-p1", "zz-p3")
}

func TestDequeueOrderAgedP1BeatsOlderP2(t *testing.T) { // (c) for P2
	d := newDequeueEnv(t)
	d.SeedGoal("G-002", "", "zz-test goal")
	d.queue(t, "zz-p2", "reviewer", "G-002", at(1, 180), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2, 31), "")
	d.first(t, "zz-p1", "zz-p2")
}

func TestDequeueOrderP1Waited30minBeatsP3Waited3hP1FarOlderToo(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 235), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2, 200), "")
	d.first(t, "zz-p1", "zz-p3")
}

func TestDequeueOrderCapP3250minBeatsP1Waited60min(t *testing.T) { // the 240-min cap puts the P3 at level 0 with more steps
	d := newDequeueEnv(t)
	d.queue(t, "zz-p1", "reviewer", "G-001", at(1, 60), "")
	d.queue(t, "zz-p3", "reviewer", "G-003", at(2, 250), "")
	d.first(t, "zz-p3", "zz-p1")
}

func TestDequeueOrderBelowCapP3200minLosesToP1Waited30min(t *testing.T) { // (c) still holds under the cap
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 200), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2, 31), "")
	d.first(t, "zz-p1", "zz-p3")
}

func TestDequeueOrderCapP1StreamCannotHoldBackP3PastCap(t *testing.T) { // a P1 arrives every 10 min; the P3 is 241 min old
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 241), "")
	for i, m := 0, 0; m < 240; i, m = i+1, m+10 {
		d.queue(t, fmt.Sprintf("zz-p1-%02d", i), "reviewer", "G-001", at(2+i, m), "")
	}
	d.first(t, "zz-p3", "zz-p1-00")
}

func TestDequeueOrderSameTimePriorityOrderAfterAging(t *testing.T) { // (a) P3 queued a few seconds before P2, both 2 h old
	d := newDequeueEnv(t)
	d.SeedGoal("G-002", "", "zz-test goal")
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 120), "")
	d.queue(t, "zz-p2", "reviewer", "G-002", at(2, 120), "")
	d.first(t, "zz-p2", "zz-p3")
}

func TestDequeueOrderSameTimePriorityOrderFresh(t *testing.T) { // (a)
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2), "")
	d.first(t, "zz-p1", "zz-p3")
}

func TestDequeueOrderStarvationBoundP3BeatsFreshP1(t *testing.T) { // (b) a P3 queued 61 min ago is at level 1, ahead of a fresh P1
	d := newDequeueEnv(t)
	d.queue(t, "zz-p1", "reviewer", "G-001", at(1), "")
	d.queue(t, "zz-p3", "reviewer", "G-003", at(2, 61), "")
	d.first(t, "zz-p3", "zz-p1")
}

func TestDequeueOrderP3YoungerThanBoundLosesToFreshP1(t *testing.T) { // the bound is 60 min, not less
	d := newDequeueEnv(t)
	d.queue(t, "zz-p3", "reviewer", "G-003", at(1, 50), "")
	d.queue(t, "zz-p1", "reviewer", "G-001", at(2), "")
	d.first(t, "zz-p1", "zz-p3")
}

func TestDequeueOrderRoleClassStillFirstForAgedP1(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-co", "coder", "G-001", at(1, 300), "")
	d.queue(t, "zz-rv", "reviewer", "G-003", at(2), "")
	d.first(t, "zz-rv", "zz-co")
}

func TestDequeueOrderMergeOnlyRefreshCoderBeatsOlderNormalCoder(t *testing.T) { // (d)
	d := newDequeueEnv(t)
	d.queue(t, "zz-old", "coder", "G-001", at(1, 300), "")
	d.queue(t, "zz-ref", "coder", "G-003", at(2), "")
	d.setPrompt(t, "zz-ref", "Task T-0001: Merge-Only Refresh of the PR branch")
	d.first(t, "zz-ref", "zz-old")
}

func TestDequeueOrderMergeOnlyRefreshCoderAlsoBeatsOlderPm(t *testing.T) { // (d)
	d := newDequeueEnv(t)
	d.queue(t, "zz-pm", "pm", "G-001", at(1, 300), "")
	d.queue(t, "zz-ref", "coder", "G-003", at(2), "")
	d.setPrompt(t, "zz-ref", "do a merge-only refresh")
	d.first(t, "zz-ref", "zz-pm")
}

func TestDequeueOrderPmPromptSayingMergeOnlyRefreshGetsNoBoost(t *testing.T) { // only coders count
	d := newDequeueEnv(t)
	d.queue(t, "zz-old", "coder", "G-001", at(1, 300), "")
	d.queue(t, "zz-pm", "pm", "G-003", at(2), "")
	d.setPrompt(t, "zz-pm", "merge-only refresh")
	d.first(t, "zz-old", "zz-pm")
}

func TestDequeueOrderNormalCoderPromptGetsNoBoost(t *testing.T) {
	d := newDequeueEnv(t)
	d.queue(t, "zz-old", "coder", "G-001", at(1, 300), "")
	d.queue(t, "zz-new", "coder", "G-003", at(2), "")
	d.setPrompt(t, "zz-new", "implement the task; merge origin/main first")
	d.first(t, "zz-old", "zz-new")
}

func TestDequeueOrderPassVerdictJudgeBeatsOlderNormalCoder(t *testing.T) { // (e)
	d := newDequeueEnv(t)
	d.SeedTask(ft.Task{ID: "T-0002", Status: "in_review", Team: "g-001-a"})
	d.queue(t, "zz-co", "coder", "G-001", at(1, 300), "")
	d.queue(t, "zz-jd", "judge", "G-003", at(2), "")
	sqlExec(t, d.Env, "UPDATE agents SET task_id='T-0002' WHERE name='zz-jd'")
	sqlExec(t, d.Env, "INSERT INTO verdicts(task_id,role,verdict,summary,created) VALUES('T-0002','reviewer','PASS','ok','t')")
	d.first(t, "zz-jd", "zz-co")
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
	d.herdrUp()
	d.OKWith(ft.Opts{}, "config", "max_live_agents", "5")
	return d
}

func (d *dqEnv) dq(t *testing.T, agent string, code int, args ...string) ft.Result {
	t.Helper()
	return d.FailWith(ft.Opts{Agent: agent}, code, append([]string{"dequeue"}, args...)...)
}

// shq single-quotes s for sh.
func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// dequeueHook is the hook command that runs `factory dequeue name` as the director; out "" discards the output,
// else it writes the output and the exit code to out (the original runs bin/factory with sys.executable).
func (d *dqEnv) dequeueHook(name, out string) string {
	base := "FACTORY_AGENT=zz-director " + shq(d.GoBin) + " dequeue " + shq(name)
	if out == "" {
		return base + " >/dev/null 2>&1"
	}
	return base + " >" + shq(out) + " 2>&1; echo $? >>" + shq(out)
}

// failHook flips "fail" to true in the stub's state, the original's `json.load/dump` one-liner.
const failHook = `sed -i 's/"fail": *false/"fail": true/' "$FAKE_HERDR_STATE"`

func (d *dqEnv) dequeuedEvents(name string) int {
	return d.Count("SELECT count(*) FROM events WHERE action='dequeued' AND entity_id=?", name)
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

func TestDequeueCmdCancelledNeverStartedAndNotCounted(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.dq(t, "zz-director", 0, "zz-q")
	d.OK("tick")
	d.wantStarts(t)
	if got := d.agentStatus(t, "zz-q"); got != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got)
	}
	out := d.OK("res").Stdout
	contains(t, "res", out, "queued 0")
	contains(t, "res", out, "spawn gate: OPEN")
	notContains(t, "agents", d.OK("agents").Stdout, "zz-q")
	contains(t, "agents --all", d.OK("agents", "--all").Stdout, "cancelled")
}

func TestDequeueCmdGateIgnoresCancelled(t *testing.T) {
	d := newDequeueCmdEnv(t)
	// a cancelled row started seconds ago would hold the single slot if it counted (live or still starting)
	sqlExec(t, d.Env, "UPDATE agents SET status='cancelled', started=datetime('now') WHERE name='zz-q'")
	sqlExec(t, d.Env, "UPDATE agents SET status='done' WHERE name='zz-coder'")
	d.OK("config", "max_live_agents", "1")
	contains(t, "res", d.OK("res").Stdout, "agents live 0/1")
	contains(t, "spawn", d.OK("spawn", "pm", "--name", "zz-new", "--goal", "G-001").Stdout, "SPAWNED")
}

func TestDequeueCmdDequeueDuringEarlierSpawnIsHonoured(t *testing.T) {
	d := newDequeueCmdEnv(t)
	// rows A (zz-q, first in order) and B are queued; B is dequeued while A's `agent start` runs
	d.queue(t, "zz-b", "pm", "G-003", at(9), "g-001-a")
	d.HerdrHook(d.dequeueHook("zz-b", ""), "agent start")
	d.OK("tick")
	d.wantStatuses(t, []string{"zz-q", "zz-b"}, "live", "cancelled")
	d.wantStarts(t, "zz-q")
	if n := d.dequeuedEvents("zz-b"); n != 1 {
		t.Fatalf("dequeued events of zz-b = %d, want 1", n)
	}
}

func TestDequeueCmdDequeueDuringPlaceIsRefusedNotStartedAndCancelled(t *testing.T) {
	d := newDequeueCmdEnv(t)
	// the reviewer's repro: the tick has claimed zz-q and is in place() (`tab create`) when the dequeue runs
	out := filepath.Join(d.Home, "dq.out")
	d.HerdrHook(d.dequeueHook("zz-q", out), "tab create")
	d.OK("tick")
	b, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("the hook did not run: %v", err)
	}
	contains(t, "dequeue output", string(b), "already starting")
	if !strings.HasSuffix(strings.TrimSpace(string(b)), "2") {
		t.Fatalf("dequeue output = %q, want exit code 2 at the end", b)
	}
	if got := d.agentStatus(t, "zz-q"); got != "live" {
		t.Fatalf("status = %s, want live", got)
	}
	d.wantStarts(t, "zz-q")
	if n := d.dequeuedEvents("zz-q"); n != 0 {
		t.Fatalf("dequeued events = %d, want 0", n)
	}
}

func (d *dqEnv) claimedAt(t *testing.T, name string) string {
	t.Helper()
	return d.SQL("SELECT spawn_args FROM agents WHERE name=?", name)[0][0]
}

func TestDequeueCmdFailedStartReturnsClaimToQueue(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.HerdrHook(failHook, "tab create")
	d.OK("tick")
	if got := d.agentStatus(t, "zz-q"); got != "queued" {
		t.Fatalf("status = %s, want queued", got)
	}
	notContains(t, "spawn_args", d.claimedAt(t, "zz-q"), "claimed_at")
	d.herdrUp()
	d.dq(t, "zz-director", 0, "zz-q") // and it can be dequeued again
	if got := d.agentStatus(t, "zz-q"); got != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got)
	}
}

// claim marks zz-q as claimed by a spawn that started at epoch seconds ts (Python time.time()).
func (d *dqEnv) claim(t *testing.T, ts float64) {
	t.Helper()
	sqlExec(t, d.Env, "UPDATE agents SET status='starting', spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'", ts)
}

func nowS() float64 { return float64(time.Now().UnixNano()) / 1e9 }

func TestDequeueCmdStartingRowIsRefusedByDequeueAndRecoveredAfterTtl(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.claim(t, nowS())
	contains(t, "dequeue stderr", d.dq(t, "zz-director", 2, "zz-q").Stderr, "already starting")
	d.OK("tick")
	if got := d.agentStatus(t, "zz-q"); got != "starting" { // fresh claim: the tick leaves it alone
		t.Fatalf("status = %s, want starting", got)
	}
	d.wantStarts(t)
	contains(t, "agents", d.OK("agents").Stdout, "starting")
	sqlExec(t, d.Env, "UPDATE agents SET spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'", nowS()-601)
	d.OK("tick") // crash recovery: the stale claim is queued again and started
	if got := d.agentStatus(t, "zz-q"); got != "live" {
		t.Fatalf("status = %s, want live", got)
	}
	d.wantStarts(t, "zz-q")
}

func TestDequeueCmdUpDoesNotRelaunchCancelledLeadOrDirector(t *testing.T) {
	d := newDequeueCmdEnv(t)
	sqlExec(t, d.Env, "UPDATE agents SET status='cancelled', session_id='s' WHERE name IN ('zz-lead','zz-director')")
	d.SeedAgent(ft.AgentRow{Name: "zz-boss", Role: "boss"})
	d.NeedPython()
	d.OKWith(ft.Opts{Agent: "zz-boss"}, "up", "--no-boss")
	started := d.starts()
	for _, n := range started {
		if n == "zz-lead" || n == "zz-director" {
			t.Fatalf("up relaunched cancelled %s (started %v)", n, started)
		}
	}
	d.wantStatuses(t, []string{"zz-lead", "zz-director"}, "cancelled", "cancelled")
}

func TestDequeueCmdRespawnAfterCancel(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.dq(t, "zz-director", 0, "zz-q")
	contains(t, "spawn", d.OK("spawn", "pm", "--name", "zz-q", "--goal", "G-001").Stdout, "SPAWNED")
	if got := d.agentStatus(t, "zz-q"); got != "live" {
		t.Fatalf("status = %s, want live", got)
	}
}

func TestDequeueCmdRequeueAfterCancel(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.dq(t, "zz-director", 0, "zz-q")
	d.herdrDown()
	contains(t, "spawn", d.OK("spawn", "pm", "--name", "zz-q", "--goal", "G-001").Stdout, "QUEUED")
	if got := d.agentStatus(t, "zz-q"); got != "queued" {
		t.Fatalf("status = %s, want queued", got)
	}
}

// hookInSpawnGate runs cmd on the third `agent list` of a tick: the first two are the tick's reachability check and
// its gate check, the third is the gate check of spawn(from_queue=True) for the row the tick selected, after the
// row was read and before it is upserted (the window the original reaches by calling ns.spawn in-process).
func (d *dqEnv) hookInSpawnGate(cmd string) { d.HerdrHookAfter(cmd, "agent list", 2) }

// The original calls ns.spawn(from_queue=True) in-process after a dequeue, with herdr down: the tick selected the
// queued row, a dequeue cancels it, then the gate-closed from_queue re-queue runs and must leave the row cancelled.
// Through the CLI: the tick selects zz-q, the hook dequeues it and closes herdr in the spawn's gate check, so the
// gate-closed upsert runs on the cancelled row; the row must stay cancelled and never be started. (Run with the
// `WHERE agents.status != 'cancelled'` of that upsert removed, the row ends queued: the window is real.)
func TestDequeueCmdGateClosedFromQueueUpsertLeavesCancelledRowAlone(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.hookInSpawnGate(d.dequeueHook("zz-q", "") + "; " + failHook)
	d.OK("tick")
	if got := d.agentStatus(t, "zz-q"); got != "cancelled" {
		t.Fatalf("status = %s, want cancelled", got)
	}
	d.wantStarts(t)
}

func TestDequeueCmdManualSpawnOfStartingRowExits2(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.claim(t, nowS())
	d.NeedPython()
	r := d.FailWith(ft.Opts{Agent: "zz-director"}, 2, "spawn", "pm", "--name", "zz-q", "--goal", "G-001")
	contains(t, "stderr", r.Stderr, "already starting")
	if got := d.agentStatus(t, "zz-q"); got != "starting" {
		t.Fatalf("status = %s, want starting", got)
	}
	d.wantStarts(t)
}

// T-0130: a re-queue must not reset aging (queued_at); only a new row or one from a terminal status gets a fresh one.
const oldQueuedAt = "2026-01-01T00:00:00+00:00"

func (d *dqEnv) queuedAt(t *testing.T) string {
	t.Helper()
	return d.SQL("SELECT queued_at FROM agents WHERE name='zz-q'")[0][0]
}

func (d *dqEnv) wantQueued(t *testing.T, status, queuedAt string) {
	t.Helper()
	if got, at := d.agentStatus(t, "zz-q"), d.queuedAt(t); got != status || at != queuedAt {
		t.Fatalf("zz-q = (%s, %s), want (%s, %s)", got, at, status, queuedAt)
	}
}

// requeueFromQueue is the tick's gate-closed re-spawn of zz-q (from_queue=True, the original calls ns.spawn) while herdr
// is unreachable. The CLI drives the same code: the tick selects the row while the gate is open, the hook closes herdr in
// the spawn's own gate check, and the spawn re-queues the row. (A starting row the tick finds with a stale claim is
// returned to queued first, so the upsert sees a queued row; the TTL test below covers that step.)
func (d *dqEnv) requeueFromQueue(t *testing.T) {
	t.Helper()
	d.hookInSpawnGate(failHook)
	d.OK("tick")
}

func TestDequeueCmdGateClosedRequeueOfQueuedRowKeepsQueuedAt(t *testing.T) {
	d := newDequeueCmdEnv(t)
	sqlExec(t, d.Env, "UPDATE agents SET queued_at=? WHERE name='zz-q'", oldQueuedAt)
	d.requeueFromQueue(t)
	d.wantQueued(t, "queued", oldQueuedAt)
}

// Mutating the CASE to IN ('queued') alone fails this test (checked).
func TestDequeueCmdGateClosedRequeueOfStartingRowKeepsQueuedAt(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.NeedPython()
	sqlExec(t, d.Env, "UPDATE agents SET queued_at=? WHERE name='zz-q'", oldQueuedAt)
	// The Python case hands spawn(from_queue=True) a row whose status is 'starting' with no claim. A stale claim the
	// tick would recover to 'queued' first, so the hook sets 'starting' in the spawn's gate check, right before the
	// gate closes: the upsert then takes the 'starting' arm of its queued_at CASE.
	marker := filepath.Join(d.Home, "starting.out")
	set := "python3 -c " + shq("import sqlite3,sys; c=sqlite3.connect(sys.argv[1]); "+
		"n=c.execute(\"UPDATE agents SET status='starting' WHERE name='zz-q' AND status='queued'\").rowcount; c.commit(); print(n)") +
		" " + shq(d.DBPath) + " >" + shq(marker)
	d.hookInSpawnGate(set + "; " + failHook)
	d.OK("tick")
	// the assert that fails when the arm is skipped: the hook found a queued row and made it starting
	b, err := os.ReadFile(marker)
	if err != nil || strings.TrimSpace(string(b)) != "1" {
		t.Fatalf("hook did not turn the queued zz-q into a starting row (marker %q, err %v): the starting arm was not reached", b, err)
	}
	d.wantQueued(t, "queued", oldQueuedAt)
}

func TestDequeueCmdTtlRecoveryOfStartingRowKeepsQueuedAt(t *testing.T) {
	d := newDequeueCmdEnv(t)
	sqlExec(t, d.Env, "UPDATE agents SET status='starting', queued_at=?, spawn_args=json_set(spawn_args,'$.claimed_at',?) WHERE name='zz-q'",
		oldQueuedAt, nowS()-86400)
	d.OK("config", "max_live_agents", "0") // gate closed: the recovered row stays queued
	d.OK("tick")
	d.wantQueued(t, "queued", oldQueuedAt)
}

func TestDequeueCmdFailedStartReturnedToQueuedKeepsQueuedAt(t *testing.T) {
	d := newDequeueCmdEnv(t)
	sqlExec(t, d.Env, "UPDATE agents SET queued_at=? WHERE name='zz-q'", oldQueuedAt)
	d.HerdrHook(failHook, "tab create")
	d.OK("tick")
	d.wantQueued(t, "queued", oldQueuedAt)
}

func TestDequeueCmdRespawnAfterTerminalStatusGetsFreshQueuedAt(t *testing.T) {
	d := newDequeueCmdEnv(t)
	d.Now = "2026-11-10T12:00:00+00:00" // the CLI stamps a fresh queued_at with this clock
	for _, term := range []string{"done", "stopped", "lost", "cancelled", "exited"} {
		sqlExec(t, d.Env, "UPDATE agents SET status=?, queued_at=? WHERE name='zz-q'", term, oldQueuedAt)
		d.herdrDown()
		d.NeedPython()
		contains(t, term+" spawn", d.OKWith(ft.Opts{Agent: "zz-director"}, "spawn", "pm", "--name", "zz-q", "--goal", "G-001").Stdout, "QUEUED")
		if got := d.agentStatus(t, "zz-q"); got != "queued" {
			t.Fatalf("%s: status = %s, want queued", term, got)
		}
		if got := d.queuedAt(t); got == oldQueuedAt || got <= "2026-11-01" || !strings.HasPrefix(got, "2026-11-1") {
			t.Fatalf("%s: queued_at %s was not refreshed to the CLI clock %s", term, got, d.Now)
		}
	}
}

func TestDequeueCmdHelpListsDequeue(t *testing.T) {
	d := newDequeueCmdEnv(t)
	contains(t, "--help", d.OK("--help").Stdout, "dequeue")
}
