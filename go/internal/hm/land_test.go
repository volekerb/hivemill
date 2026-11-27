package hm

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const landPR = "https://github.com/zz-test/www/pull/1"

var landHead = strings.Repeat("a", 40) // the worktree head of testRunner's fake git

// landRunner is testRunner with a run of T-0001 and a PR that gh shows in the given state at the given head.
func landRunner(t *testing.T, state, head string) (*Runner, *SQLite, *Run) {
	t.Helper()
	r, s := testRunner(t, "claude-task-t-0001")
	gh := r.Tools.GH
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
			return 0, fmt.Sprintf(`{"state":%q,"headRefOid":%q,"url":%q}`, state, head, landPR), ""
		}
		return gh(in, args...)
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: t.TempDir(),
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Working, Round: 1,
		Dispatched: time.Now().Add(-time.Hour).Format(time.RFC3339)}
	putRuns(t, s, run)
	return r, s, run
}

func noOpenPR(r *Runner) {
	r.Tools.GH = func(string, ...string) (int, string, string) { return 0, "", "" }
}

// stall makes the run one idle spell away from "stalled".
func stall(r *Runner, run *Run) {
	run.IdleSince, run.IdleNudges = time.Now().Add(-time.Hour).Format(time.RFC3339), 1
	r.idleWatch(run, []Terminal{{Name: run.Agent, Status: "idle"}})
}

func TestRescueOpenPRStalled(t *testing.T) {
	r, s, run := landRunner(t, "OPEN", landHead)
	stall(r, run)
	if run.State != Gated || run.PRURL != landPR || run.Head != landHead || run.LastGate == nil || len(run.LastGate.Problems) != 1 {
		t.Fatalf("run %s %q %s %+v (%s)", run.State, run.PRURL, run.Head, run.LastGate, run.FailReason)
	}
	task, err := s.Task(run.Task)
	if err != nil || task.PRURL != landPR || task.Status != "in_review" || events(t, r, "run.rescued") != 1 {
		t.Errorf("task %+v %v", task, err)
	}
	// the same head is rescued once: a run that comes back and stalls again fails
	run.State = Working
	stall(r, run)
	if run.State != Failed {
		t.Errorf("second rescue of one head: %s", run.State)
	}
}

func TestRescueOpenPRNoneStillFails(t *testing.T) {
	r, _, run := landRunner(t, "OPEN", landHead)
	noOpenPR(r)
	stall(r, run)
	if run.State != Failed || !strings.HasPrefix(run.FailReason, "stalled:") || run.PRURL != "" {
		t.Errorf("run %s %q %q", run.State, run.FailReason, run.PRURL)
	}
}

func TestRescueOpenPRRefused(t *testing.T) {
	for name, set := range map[string]func(r *Runner, s *SQLite, run *Run){
		"eval run": func(_ *Runner, _ *SQLite, run *Run) { run.Eval, run.EvalMode = true, EvalFull },
		"rejected head": func(_ *Runner, s *SQLite, run *Run) {
			if err := s.Verdict(run.Task, landPR, landHead, "fail", "no", "judge"); err != nil {
				t.Fatal(err)
			}
		},
		"worktree not at the PR head": func(r *Runner, _ *SQLite, _ *Run) {
			r.Tools.Git = func(string, ...string) (int, string, string) { return 0, strings.Repeat("b", 40), "" }
		},
	} {
		r, s, run := landRunner(t, "OPEN", landHead)
		set(r, s, run)
		if r.rescueOpenPR(run, "x") || run.State != Working {
			t.Errorf("%s: rescued, run %s", name, run.State)
		}
	}
}

func TestRescueOpenPRTimedOut(t *testing.T) {
	r, _, run := landRunner(t, "OPEN", landHead)
	now := time.Now().Truncate(time.Second)
	run.RoundStart, run.LongPct = now.Add(-5*time.Hour).Format(time.RFC3339), 100
	run.LongResumed = now.Add(-time.Hour).Format(time.RFC3339)
	if !r.workingLimit(run, now) || run.State != Gated || run.PRURL != landPR {
		t.Errorf("run %s %q", run.State, run.FailReason)
	}
}

// atCap stops the session once with a gate block counted that reaches the cap.
func atCap(t *testing.T, r *Runner, s *SQLite, run *Run) *Run {
	t.Helper()
	run.GateBlocks = cfgInt(r.Store, "gate_max_blocks")
	putRuns(t, s, run)
	_ = os.MkdirAll(filepath.Join(run.Worktree, ".hivemill"), 0o755)
	_ = os.WriteFile(filepath.Join(run.Worktree, ".hivemill", "findings.json"), []byte(`{"findings": []}`), 0o600)
	var out bytes.Buffer
	if err := r.GateHook(strings.NewReader(`{"session_id":"s-1"}`), &out); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(run.Task)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestRescueOpenPRNotWithMoreGateProblems(t *testing.T) {
	r, s, run := landRunner(t, "OPEN", landHead)
	gh, lists := r.Tools.GH, 0
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "list" {
			// the hook's early look finds the PR, the gate's own lookup fails, and the PR is there at the rescue
			if lists++; lists == 2 {
				return 0, "", ""
			}
		}
		return gh(in, args...)
	}
	// the worktree is dirty in this fixture too: a gate with another failure is not rescued
	if got := atCap(t, r, s, run); got.State != Failed || got.HookStarted != "" {
		t.Errorf("run %s %q (%d lookups)", got.State, got.PRURL, lists)
	}
}

func TestRescueOpenPROnlyNoPR(t *testing.T) {
	noPR := noPRProblem + "factory/t-0001: push"
	if !onlyNoPR([]string{noPR}) || onlyNoPR(nil) || onlyNoPR([]string{noPR, "affected: tests failed"}) ||
		onlyNoPR([]string{"pr: the PR is CLOSED"}) {
		t.Error("onlyNoPR")
	}
}

func TestRescueOpenPRJudgePacket(t *testing.T) {
	r, _, run := landRunner(t, "OPEN", landHead)
	stall(r, run)
	r.Tools.Git = func(string, ...string) (int, string, string) { return 0, "", "" }
	packet, err := r.Packet(run)
	if err != nil || !strings.Contains(packet, "no proof, test or review was verified") || !strings.Contains(packet, "stalled:") {
		t.Errorf("%v\n%s", err, packet)
	}
}

// A gate that found the PR and failed for something else is not the "no PR" path.
func TestRescueOpenPRNotForOtherGateProblems(t *testing.T) {
	r, s, run := landRunner(t, "OPEN", landHead)
	if got := atCap(t, r, s, run); got.State != Failed || !strings.HasPrefix(got.FailReason, "gate blocked") {
		t.Errorf("run %s %q", got.State, got.FailReason)
	}
}

func TestIdleWatchGateHookBusy(t *testing.T) {
	r, _, run := landRunner(t, "OPEN", landHead)
	run.HookStarted = time.Now().Add(-20 * time.Minute).Format(time.RFC3339)
	stall(r, run)
	if run.State != Working || run.IdleSince != "" || run.IdleNudges != 1 || events(t, r, "session.nudged") != 0 {
		t.Errorf("run %s idle %q nudges %d (%s)", run.State, run.IdleSince, run.IdleNudges, run.FailReason)
	}
}

func TestIdleWatchGateHookBusyUntilItsTimeout(t *testing.T) {
	r, _, run := landRunner(t, "OPEN", landHead)
	noOpenPR(r)
	run.HookStarted = time.Now().Add(-gateHookTimeout - time.Minute).Format(time.RFC3339)
	r.idleWatch(run, []Terminal{{Name: run.Agent, Status: "idle"}})
	if run.State != Failed || !strings.Contains(run.FailReason, "gate hook timed out") || strings.Contains(run.FailReason, "stalled") {
		t.Errorf("run %s %q", run.State, run.FailReason)
	}
	b, err := assets.ReadFile("assets/task.json.in")
	if want := fmt.Sprintf(`"timeout": %d`, int(gateHookTimeout/time.Second)); err != nil || !strings.Contains(string(b), want) {
		t.Errorf("assets/task.json.in lacks %s: %v", want, err)
	}
}

// The hook records its start while it runs and clears it when it returns.
func TestIdleWatchGateHookBusyMarker(t *testing.T) {
	r, s, run := landRunner(t, "OPEN", landHead)
	run.IdleSince = time.Now().Add(-time.Hour).Format(time.RFC3339)
	putRuns(t, s, run)
	gh, during := r.Tools.GH, ""
	r.Tools.GH = func(in string, args ...string) (int, string, string) {
		if x, err := s.Run(run.Task); err == nil && x.IdleSince == "" {
			during = x.HookStarted
		}
		return gh(in, args...)
	}
	var out bytes.Buffer
	if err := r.GateHook(strings.NewReader(`{"session_id":"s-1"}`), &out); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run(run.Task)
	if err != nil || during == "" || got.HookStarted != "" {
		t.Errorf("during %q after %+v %v", during, got, err)
	}
}

// held is a run held on a judged head with its owner question answered.
func held(t *testing.T, head, answer string) (*Runner, *SQLite, *Run) {
	t.Helper()
	r, s, run := landRunner(t, "OPEN", head)
	run.State, run.PRURL, run.Head, run.JudgeHead, run.MergeHold = Held, landPR, landHead, landHead, "judge HOLD"
	putRuns(t, s, run)
	if err := s.Verdict(run.Task, landPR, landHead, "fail", "HOLD", "judge"); err != nil {
		t.Fatal(err)
	}
	r.fileHeldAsk(s, run, "judge HOLD")
	asks, err := s.Asks("open")
	if err != nil || len(asks) != 1 {
		t.Fatalf("asks %v %v", asks, err)
	}
	r.Env.Actor = "" // the owner answers
	if err := r.AskClose(asks[0].ID, true, answer); err != nil {
		t.Fatal(err)
	}
	return r, s, run
}

func decide(t *testing.T, s *SQLite, run *Run, head string) MergeDecision {
	t.Helper()
	v, _, err := s.LaneVerdict(run.Task, head)
	if err != nil {
		t.Fatal(err)
	}
	return Decide(MergeInput{PRState: "OPEN", Mergeable: "MERGEABLE", Head: head, LaneVerdict: v, CI: "green", Tier: "T1"})
}

func TestApprovedHoldMerge(t *testing.T) {
	for _, answer := range []string{"Approved, ship it", "  MERGE IT"} {
		r, s, run := held(t, landHead, answer)
		r.checkHeld(run)
		if run.State != Merging || run.MergeHold != "" || run.Outcome != "" {
			t.Fatalf("%q: run %s %q", answer, run.State, run.MergeHold)
		}
		if d := decide(t, s, run, landHead); d.Why == "the judge rejected this head" || d.Action == "rejudge" {
			t.Errorf("%q: %+v", answer, d)
		}
	}
}

func TestApprovedHoldMergeNotWithoutApproval(t *testing.T) {
	for _, answer := range []string{"not approved", "change the title first", "approvedly", "do not merge it"} {
		r, s, run := held(t, landHead, answer)
		r.checkHeld(run)
		if d := decide(t, s, run, landHead); run.State != Held || d.Why != "the judge rejected this head" {
			t.Errorf("%q: run %s %+v", answer, run.State, d)
		}
	}
}

func TestApprovedHoldMergeOnlyFromTheOwner(t *testing.T) {
	r, s, run := held(t, landHead, "not yet")
	if _, err := s.db.Exec(`UPDATE hm_asks SET answer='approved', answered_by='claude-director'`); err != nil {
		t.Fatal(err)
	}
	r.checkHeld(run)
	if run.State != Held {
		t.Errorf("run %s", run.State)
	}
}

func TestApprovedHoldMergeNewHeadNeedsAJudge(t *testing.T) {
	head := strings.Repeat("b", 40)
	r, s, run := held(t, head, "approved")
	r.checkHeld(run)
	if d := decide(t, s, run, head); run.State != Held || d.Action != "rejudge" {
		t.Errorf("run %s %+v", run.State, d)
	}
	if d := decide(t, s, run, landHead); d.Action != "block" {
		t.Errorf("the held head %+v", d)
	}
}

// A later run of the task asks under the same ref: an answer from before it was dispatched is not for it.
func TestApprovedHoldMergeNotFromAnEarlierRun(t *testing.T) {
	r, _, run := held(t, landHead, "approved")
	run.Dispatched = time.Now().Add(time.Hour).Format(time.RFC3339)
	r.checkHeld(run)
	if run.State != Held {
		t.Errorf("run %s", run.State)
	}
}
