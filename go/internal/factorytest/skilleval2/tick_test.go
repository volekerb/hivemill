package skilleval2

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"factory/go/internal/sandbox"
	"factory/go/internal/skilleval"
)

// Port of the scheduler classes of tests/test_skilleval_tick.py (QueueModel, Scheduler, ClaimRace, Cleanup,
// CandidateStatus, Cancel, Summary); the CLI classes are in tick_cli_test.go. The Python tests mock check_token,
// check_userns and nsbox.run_contained; the Go code has no such seams, so the start path runs against a real 0600 token
// file and real user namespaces (a test that needs them skips with the reason where they are restricted) and the
// checker is the real contained check.py of the eval task (a solved work dir passes, an unsolved one fails).

func TestTickQueueTrialsOfATaskAreBackToBackWithThenWithout(t *testing.T) {
	w := newTickW(t)
	if n := len(w.queue()); n != 6 {
		t.Fatalf("queued %d, want 6", n)
	}
	rows := tickRows(t, w.db, "SELECT task, condition FROM skill_trials ORDER BY id")
	var got, want []string
	for _, r := range rows {
		got = append(got, r["task"]+"/"+r["condition"])
	}
	for _, task := range []string{"fix-bug", "add-feature", "review-diff"} {
		for _, c := range []string{"with", "without"} {
			want = append(want, task+"/"+c)
		}
	}
	tickEqual(t, "trial order", got, want)
	if s := w.candStatus(); s != "queued-eval" {
		t.Fatalf("candidate %s", s)
	}
}

func TestTickCapIsEnforcedAcrossTicksAndResetsNextDay(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	if n := len(w.queue("t1", "t2", "t3", "t4")); n != 8 {
		t.Fatalf("queued %d", n)
	}
	var started []int64
	for minute := 0; minute < 30; minute++ { // each started trial finishes, so only the daily cap holds back
		a := w.tick(tickAtDay(fmt.Sprintf("10:%02d", minute), tickDay))
		started = append(started, a.launched...)
		tickExec(t, w.db, "UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", a.Now())
	}
	if len(started) != 6 {
		t.Fatalf("started %v, want 6", started)
	}
	if n := tickCount(w.status(), "pending"); n != 2 {
		t.Fatalf("%d pending, want 2", n)
	}
	var next []int64
	for minute := 0; minute < 5; minute++ {
		a := w.tick(tickAtDay(fmt.Sprintf("09:%02d", minute), tickNextDay))
		next = append(next, a.launched...)
		tickExec(t, w.db, "UPDATE skill_trials SET status='passed', ended=? WHERE status='running'", a.Now())
	}
	if len(next) != 2 {
		t.Fatalf("next day started %v, want 2", next)
	}
	if n := tickCount(w.status(), "pending"); n != 0 {
		t.Fatalf("%d pending, want 0", n)
	}
}

func TestTickSecondTrialWaitsWhileOneRuns(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	if n := len(w.tick(tickAt("10:00")).launched); n != 1 {
		t.Fatalf("first tick launched %d", n)
	}
	if n := len(w.tick(tickAt("10:01")).launched); n != 0 {
		t.Fatalf("second tick launched %d", n)
	}
	tickEqual(t, "status", w.status(), []string{"running", "pending"})
}

func TestTickMaxConcurrentTwoStartsOnePerTick(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	tickExec(t, w.db, "UPDATE config SET value='2' WHERE key='skilleval_max_concurrent'")
	w.queue("t1", "t2")
	var counts []int
	for i := 0; i < 3; i++ {
		counts = append(counts, len(w.tick(tickAt(fmt.Sprintf("10:0%d", i))).launched))
	}
	if fmt.Sprint(counts) != "[1 1 0]" { // never two in one tick, never above the limit
		t.Fatalf("counts %v", counts)
	}
}

func TestTickGateClosedQueuedAgentCountsAsRunning(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	if n := len(w.tickGate(tickAt("10:00"), "queued").launched); n != 1 {
		t.Fatalf("launched %d", n)
	}
	var tid int64
	tickMust(t, w.db.QueryRow("SELECT id FROM skill_trials WHERE status='running'").Scan(&tid))
	for minute := 1; minute < 40; minute++ { // a queued agent has no wall clock and no tokens: neither started again nor collected
		a := w.tickGate(tickAt(fmt.Sprintf("10:%02d", minute)), "queued")
		if len(a.launched) != 0 || len(a.stopped) != 0 {
			t.Fatalf("minute %d: launched %v stopped %v", minute, a.launched, a.stopped)
		}
	}
	if s := w.trial(tid).Status; s != "running" {
		t.Fatalf("status %s", s)
	}
}

func TestTickDoneInTempDBIsCollectedWithTheCheckerResult(t *testing.T) {
	tickNeedUserns(t)
	tasksNeedPython(t)
	w := newTickW(t)
	tid, d, _ := w.makeTrial(tickMk{})
	a := w.tick(tickAt("10:10"))
	row := w.trial(tid)
	if row.Status != "passed" || !row.Passed.Valid || row.Passed.Int64 != 1 {
		t.Fatalf("%+v", row)
	}
	want := [2]string{"collect", fmt.Sprintf("trial %d passed", tid)}
	found := false
	for _, e := range a.events {
		found = found || e == want
	}
	if !found {
		t.Fatalf("events %v lack %v", a.events, want)
	}
	if tickExists(d) {
		t.Fatal("a passed trial's sandbox is removed")
	}
}

func TestTickFailedCheckerIsRecordedFailed(t *testing.T) {
	tickNeedUserns(t)
	tasksNeedPython(t)
	w := newTickW(t)
	tid, _, _ := w.makeTrial(tickMk{unsolved: true}) // the template fails check.py (Python mocks rc 1)
	w.tick(tickAt("10:10"))
	row := w.trial(tid)
	if row.Status != "failed" || !row.Passed.Valid || row.Passed.Int64 != 0 {
		t.Fatalf("%+v", row)
	}
}

func TestTickLostLiveRowIsAnError(t *testing.T) {
	w := newTickW(t)
	tid, _, _ := w.makeTrial(tickMk{notDone: true, live: "lost"})
	w.tick(tickAt("10:10"))
	row := w.trial(tid)
	if row.Status != "error" {
		t.Fatalf("%+v", row)
	}
	tickContains(t, "check_output", row.CheckOutput.String, "exited without finishing")
}

func TestTickMissingAgentRowIsAnError(t *testing.T) {
	w := newTickW(t)
	tid, _, _ := w.makeTrial(tickMk{notDone: true, live: "-"})
	w.tick(tickAt("10:10"))
	if s := w.trial(tid).Status; s != "error" {
		t.Fatalf("status %s", s)
	}
}

func TestTickWallTimeoutForceStopsAndRecordsTimeout(t *testing.T) {
	w := newTickW(t)
	tid, d, agent := w.makeTrial(tickMk{notDone: true}) // started 10:00, live, not done
	a := w.tick(tickAt("10:14"))
	if s := w.trial(tid).Status; s != "running" || len(a.stopped) != 0 {
		t.Fatalf("14 min is inside the 15 min budget: %s %v", s, a.stopped)
	}
	a = w.tick(tickAt("10:16"))
	row := w.trial(tid)
	if row.Status != "timeout" || !row.Passed.Valid || row.Passed.Int64 != 0 {
		t.Fatalf("%+v", row)
	}
	if len(a.stopped) != 1 || a.stopped[0].Name != agent {
		t.Fatalf("stopped %v", a.stopped)
	}
	if s := w.agentStatus(agent); s != "stopped" {
		t.Fatalf("agent %s", s)
	}
	if !strings.HasPrefix(row.CheckOutput.String, "[timeout]") {
		t.Fatalf("check_output %q", row.CheckOutput.String)
	}
	if !tickExists(d) {
		t.Fatal("a timeout sandbox is kept for the keep_failed_h window")
	}
}

func TestTickTokenOverrunForceStopsAndRecordsOverBudget(t *testing.T) {
	w := newTickW(t)
	tid, d, agent := w.makeTrial(tickMk{notDone: true, transcript: "-"})
	w.bigLog(d, 200000, 100000, 1, 0) // 300001 > 300000
	a := w.tick(tickAt("10:05"))
	row := w.trial(tid)
	if row.Status != "over-budget" || !row.Passed.Valid || row.Passed.Int64 != 0 {
		t.Fatalf("%+v", row)
	}
	if len(a.stopped) != 1 || a.stopped[0].Name != agent {
		t.Fatalf("stopped %v", a.stopped)
	}
}

func TestTickExactlyAtTheCapAndCacheReadsDoNotCount(t *testing.T) {
	w := newTickW(t)
	tid, d, _ := w.makeTrial(tickMk{notDone: true, transcript: "-"})
	w.bigLog(d, 200000, 100000, 0, 5000000) // 300000 counted, reads ignored
	a := w.tick(tickAt("10:05"))
	if s := w.trial(tid).Status; s != "running" || len(a.stopped) != 0 {
		t.Fatalf("%s %v", s, a.stopped)
	}
}

func TestTickFailedStopLeavesTheTrialRunningForTheNextTick(t *testing.T) {
	w := newTickW(t)
	tid, _, _ := w.makeTrial(tickMk{notDone: true})
	w.stopRes, w.stopNote = "failed", "pane open"
	a := w.tick(tickAt("10:20"))
	if s := w.trial(tid).Status; s != "running" {
		t.Fatalf("status %s", s)
	}
	retry := false
	for _, e := range a.events {
		retry = retry || e[0] == "collect-retry"
	}
	if !retry {
		t.Fatalf("no collect-retry event: %v", a.events)
	}
	w.stopRes, w.stopNote = "", "" // the stop works now
	w.tick(tickAt("10:21"))
	if s := w.trial(tid).Status; s != "timeout" {
		t.Fatalf("status %s", s)
	}
}

func TestTickBlockedStartLeavesTheQueuePendingAndSaysSo(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	w.noToken() // check_token refuses: owner decision D3 pending
	a1 := w.tick(tickAt("10:00"))
	w.tick(tickAt("10:01"))
	tickEqual(t, "status", w.status(), []string{"pending", "pending"})
	if len(a1.launched) != 0 {
		t.Fatalf("launched %v", a1.launched)
	}
	if len(a1.events) != 1 || a1.events[0][0] != "skilleval-blocked" {
		t.Fatalf("events %v", a1.events)
	}
}

func TestTickRefusedStartMarksThatTrialErrorAndTheNextTickGoesOn(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	w.launchFn = func(call int) error {
		if call == 1 {
			return fmt.Errorf("%w: preflight failed", sandbox.ErrSandboxRefused)
		}
		return nil
	}
	calls := 0
	inner := w.launchFn
	w.launchFn = func(int) error { calls++; return inner(calls) }
	for _, m := range []string{"10:00", "10:01"} {
		w.tick(tickAt(m))
	}
	tickEqual(t, "status", w.status(), []string{"error", "running"})
}

// ---- ClaimRace ----

func TestTickGateWaitDoesNotCountTowardTheTrialWall(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	w.tickGate(tickAt("10:00"), "queued")
	for minute := 1; minute <= 20; minute++ { // queued at a closed gate for 20 min, over the 15 min wall
		if s := w.tickGate(tickAt(fmt.Sprintf("10:%02d", minute)), "queued").stopped; len(s) != 0 {
			t.Fatalf("minute %d stopped %v", minute, s)
		}
	}
	tickExec(t, w.db, "UPDATE agents SET status='live', started=? WHERE role='evaluator'", tickAt("10:21").Format("2006-01-02T15:04:05-07:00"))
	a := w.tick(tickAt("10:22")) // one live minute: not a timeout
	if len(a.stopped) != 0 || w.status()[0] != "running" {
		t.Fatalf("stopped %v status %v", a.stopped, w.status())
	}
	a = w.tick(tickAt("10:37")) // 16 live minutes: now it is
	if len(a.stopped) != 1 || w.status()[0] != "timeout" {
		t.Fatalf("stopped %v status %v", a.stopped, w.status())
	}
}

func TestTickTwoConnectionsRacingToClaimOnePendingTrialOneWins(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	tid := w.firstID()
	conns := []*sql.DB{w.other(), w.other()}
	won := make([]bool, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup
	gate := make(chan struct{})
	for i, c := range conns {
		wg.Add(1)
		go func(i int, c *sql.DB) {
			defer wg.Done()
			<-gate
			won[i], errs[i] = skilleval.Claim(c, tid, "2026-10-25T10:00:00+00:00")
		}(i, c)
	}
	close(gate)
	wg.Wait()
	for _, err := range errs {
		tickMust(t, err)
	}
	if won[0] == won[1] {
		t.Fatalf("won %v, want exactly one winner", won)
	}
	if s := w.trial(tid).Status; s != "starting" {
		t.Fatalf("status %s", s)
	}
}

func TestTickCancelAfterAClaimDoesNotCancelAndAClaimAfterACancelLoses(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	var ids []int64
	rows := tickRows(t, w.db, "SELECT id FROM skill_trials ORDER BY id")
	for _, r := range rows {
		n, err := strconv.ParseInt(r["id"], 10, 64)
		tickMust(t, err)
		ids = append(ids, n)
	}
	a, b := ids[0], ids[1]
	ok, err := skilleval.Claim(w.other(), a, "2026-10-25T10:00:00+00:00")
	tickMust(t, err)
	if !ok {
		t.Fatal("the first claim must win")
	}
	lines, errs, err := w.c.Cancel(w.db, strconv.FormatInt(a, 10), w.root, w.api(tickAt("10:00"), ""))
	tickMust(t, err)
	if len(lines) != 0 || len(errs) != 1 {
		t.Fatalf("lines %v errors %v", lines, errs)
	}
	tickContains(t, "error", errs[0], "starting")
	if s := w.trial(a).Status; s != "starting" {
		t.Fatalf("status %s", s)
	}
	_, _, err = w.c.Cancel(w.db, strconv.FormatInt(b, 10), w.root, w.api(tickAt("10:00"), ""))
	tickMust(t, err)
	ok, err = skilleval.Claim(w.other(), b, "2026-10-25T10:00:00+00:00")
	tickMust(t, err)
	if ok {
		t.Fatal("a claim after a cancel must lose")
	}
	if s := w.trial(b).Status; s != "cancelled" {
		t.Fatalf("status %s", s)
	}
}

// Python wraps se.claim to cancel the picked trial first. The Go scheduler has no claim seam; the fake API's Home()
// is the one call startNext makes between picking the pending trial and claiming it, so the cancel runs there.
func TestTickATrialCancelledBetweenPickAndClaimIsNotStarted(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	first := w.firstID()
	other := w.other()
	a := w.api(tickAt("10:00"), "")
	a.onHome = func() {
		oa := w.api(tickAt("10:00"), "")
		oa.db = other
		if _, _, err := w.c.Cancel(other, strconv.FormatInt(first, 10), w.root, oa); err != nil {
			t.Errorf("cancel: %v", err)
		}
	}
	tickMust(t, w.c.Tick(w.db, a))
	if len(a.launched) != 0 {
		t.Fatalf("launched %v", a.launched)
	}
	if s := w.status()[0]; s != "cancelled" {
		t.Fatalf("status %s", s)
	}
}

func TestTickAStartingTrialCountsTowardMaxConcurrent(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	ok, err := skilleval.Claim(w.db, w.firstID(), "2026-10-25T10:00:00+00:00")
	tickMust(t, err)
	if !ok {
		t.Fatal("claim lost")
	}
	if n := len(w.tick(tickAt("10:01")).launched); n != 0 {
		t.Fatalf("launched %d", n)
	}
}

func TestTickAnUnexpectedStartErrorMarksTheTrialErrorAndTheQueueMovesOn(t *testing.T) {
	tickNeedUserns(t)
	w := newTickW(t)
	w.queue("t1")
	calls := 0
	w.launchFn = func(int) error {
		calls++
		if calls == 1 {
			return OSError("disk full")
		}
		return nil
	}
	for _, m := range []string{"10:00", "10:01"} {
		w.tick(tickAt(m))
	}
	tickEqual(t, "status", w.status(), []string{"error", "running"})
	var out string
	tickMust(t, w.db.QueryRow("SELECT check_output FROM skill_trials ORDER BY id").Scan(&out))
	tickContains(t, "check_output", out, "OSError")
}

func TestTickATrialStuckInStartingIsErroredAfter15Minutes(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	first := w.firstID()
	if ok, err := skilleval.Claim(w.db, first, tickAt("10:00").Format("2006-01-02T15:04:05-07:00")); err != nil || !ok {
		t.Fatalf("claim: %v %v", ok, err)
	}
	w.tick(tickAt("10:10"))
	if s := w.trial(first).Status; s != "starting" {
		t.Fatalf("status %s", s)
	}
	w.tick(tickAt("10:16"))
	if s := w.trial(first).Status; s != "error" {
		t.Fatalf("status %s", s)
	}
}

func TestTickABlockerThatReturnsAfterAResolvedPeriodIsReportedAgain(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	w.noToken()
	a1 := w.tick(tickAt("10:00"))
	if len(a1.events) != 1 || a1.events[0][0] != "skilleval-blocked" {
		t.Fatalf("events %v", a1.events)
	}
	// a launch went through in between: the blocker was resolved
	tickExec(t, w.db, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('t','x','skilleval','-','try','ok')")
	a2 := w.tick(tickAt("10:05"))
	if len(a2.events) != 1 || a2.events[0][0] != "skilleval-blocked" {
		t.Fatalf("events %v", a2.events)
	}
}

// ---- Cleanup ----

func (w *tickW) trialWithDir(status, ended string, n int) (int64, string) {
	var e any
	if ended != "" {
		e = ended
	}
	res := tickExec(w.t, w.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,ended) VALUES('C-0001',?,?,?,?,'t',?)",
		tickSHA, fmt.Sprintf("t%d", n), "with", status, e)
	tid, err := res.LastInsertId()
	tickMust(w.t, err)
	d := filepath.Join(w.root, "trials", sandbox.TrialName(int(tid)))
	tickMust(w.t, os.MkdirAll(filepath.Join(d, "work"), 0o755))
	return tid, d
}

func TestTickOldFailedSandboxesAndOrphansGoAndFreshOnesStay(t *testing.T) {
	w := newTickW(t)
	_, oldTimeout := w.trialWithDir("timeout", "2026-10-24T09:00:00+00:00", 1) // 49 h before the tick
	_, oldError := w.trialWithDir("error", "2026-10-24T09:00:00+00:00", 2)
	_, oldOver := w.trialWithDir("over-budget", "2026-10-24T09:00:00+00:00", 3)
	_, fresh := w.trialWithDir("timeout", "2026-10-26T09:00:00+00:00", 4) // 1 h
	w.trialWithDir("running", "", 5)
	orphan := filepath.Join(w.root, "trials", "tr9999")
	tickMust(t, os.MkdirAll(filepath.Join(orphan, "work"), 0o755))
	other := filepath.Join(w.root, "trials", "notes")
	tickMust(t, os.MkdirAll(other, 0o755))
	tickMust(t, os.MkdirAll(filepath.Join(w.root, "locks"), 0o755))
	w.tick(tickAtDay("10:00", tickNextDay))
	for _, p := range []string{oldTimeout, oldError, oldOver, orphan} {
		if tickExists(p) {
			t.Errorf("%s must be removed", p)
		}
	}
	if !tickExists(fresh) {
		t.Error("a fresh failed sandbox stays")
	}
	if !tickExists(other) {
		t.Error("only trNNNN dirs are ours to delete")
	}
	if !tickExists(filepath.Join(w.root, "locks")) {
		t.Error("locks stays")
	}
	if s := w.trial(1).Status; s != "timeout" {
		t.Errorf("status %s", s)
	}
}

// ---- CandidateStatus ----

func TestTickCandidateBecomesEvaluatedAfterItsLastTrialIsTerminal(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	tickExec(t, w.db, "UPDATE skill_trials SET status='passed' WHERE id=(SELECT min(id) FROM skill_trials)")
	tickExec(t, w.db, "UPDATE config SET value='0' WHERE key='skilleval_max_trials_per_day'") // nothing new starts
	w.tick(tickAt("10:00"))
	tickEqual(t, "status", w.status(), []string{"passed", "pending"})
	if s := w.candStatus(); s != "queued-eval" {
		t.Fatalf("one trial is still pending: %s", s)
	}
	tickExec(t, w.db, "UPDATE skill_trials SET status='failed' WHERE status='pending'")
	w.tick(tickAt("10:01"))
	if s := w.candStatus(); s != "evaluated" {
		t.Fatalf("status %s", s)
	}
}

func TestTickACandidateHeldBackIsNotFlipped(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	tickExec(t, w.db, "UPDATE skill_trials SET status='passed'")
	tickExec(t, w.db, "UPDATE scout_candidates SET status='needs-owner' WHERE id='C-0001'")
	w.tick(tickAt("10:00"))
	if s := w.candStatus(); s != "needs-owner" {
		t.Fatalf("status %s", s)
	}
}

// ---- Cancel ----

func TestTickCancelCandidateStopsTheRunningTrialAndCancelsPendingOnes(t *testing.T) {
	w := newTickW(t)
	tid, d, agent := w.makeTrial(tickMk{notDone: true})
	w.queue("t1")
	a := w.api(tickAt("10:05"), "")
	lines, errs, err := w.c.Cancel(w.db, "C-0001", w.root, a)
	tickMust(t, err)
	if len(errs) != 0 || len(lines) != 3 {
		t.Fatalf("lines %v errors %v", lines, errs)
	}
	row := w.trial(tid)
	if row.Status != "cancelled" {
		t.Fatalf("status %s", row.Status)
	}
	if len(a.stopped) != 1 || a.stopped[0].Name != agent {
		t.Fatalf("stopped %v", a.stopped)
	}
	for _, s := range w.status() {
		if s != "cancelled" {
			t.Fatalf("statuses %v", w.status())
		}
	}
	if tickExists(d) {
		t.Fatal("the cancelled trial's sandbox is removed")
	}
	if row.CheckOutput.Valid && row.CheckOutput.String != "" { // Python: run_contained.assert_not_called()
		t.Fatalf("a cancelled trial is not checked, but check_output is %q", row.CheckOutput.String)
	}
}

func TestTickCancelByTrialIDTouchesOnlyThatTrial(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	_, _, err := w.c.Cancel(w.db, strconv.FormatInt(w.firstID(), 10), w.root, w.api(tickAt("10:00"), ""))
	tickMust(t, err)
	tickEqual(t, "status", w.status(), []string{"cancelled", "pending"})
}

func TestTickCancelUnknownTargetIsRefused(t *testing.T) {
	w := newTickW(t)
	for _, target := range []string{"tr9999", "C-9999", "nonsense"} {
		t.Run(target, func(t *testing.T) {
			_, _, err := w.c.Cancel(w.db, target, w.root, w.api(tickAt("10:00"), ""))
			var ce *skilleval.CollectError
			if !errors.As(err, &ce) {
				t.Fatalf("err %v, want a CollectError", err)
			}
		})
	}
}

func TestTickFailedStopIsReportedAndTheTrialStaysRunning(t *testing.T) {
	w := newTickW(t)
	tid, _, _ := w.makeTrial(tickMk{notDone: true})
	a := w.api(tickAt("10:00"), "")
	a.stopRes, a.stopNote = "failed", "pane open"
	lines, errs, err := w.c.Cancel(w.db, strconv.FormatInt(tid, 10), w.root, a)
	tickMust(t, err)
	if len(lines) != 0 || len(errs) != 1 {
		t.Fatalf("lines %v errors %v", lines, errs)
	}
	if s := w.trial(tid).Status; s != "running" {
		t.Fatalf("status %s", s)
	}
}

// ---- Summary ----

func TestTickResultsAndListShowPendingAndTheCandidateSummary(t *testing.T) {
	w := newTickW(t)
	w.queue("t1")
	tickExec(t, w.db, "UPDATE skill_trials SET status='passed', tokens_in=100, tokens_out=50, wall_s=60 WHERE condition='with'")
	fns := map[string]func() ([]string, error){
		"result_lines": func() ([]string, error) { return skilleval.ResultLines(w.db, "") },
		"list_lines":   func() ([]string, error) { return skilleval.ListLines(w.db, "") },
	}
	var names []string
	for n := range fns {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		lines, err := fns[n]()
		tickMust(t, err)
		if len(lines) != 3 {
			t.Fatalf("%s: %d lines %q", n, len(lines), lines)
		}
		tickContains(t, n+" line 2", lines[1], "status=pending")
		want := "C-0001 summary: with: passed 1/1 tasks, median tokens 150, median wall_s 60; " +
			"without: passed 0/0 tasks, median tokens -, median wall_s -; pending 1"
		if lines[2] != want {
			t.Fatalf("%s: summary %q, want %q", n, lines[2], want)
		}
	}
}
