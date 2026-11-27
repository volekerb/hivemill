package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// answerHeld files the held question of the run's round, as the planner pass does, and answers it.
func answerHeld(t *testing.T, r *Runner, s *SQLite, run *Run, answer string) {
	t.Helper()
	r.fileHeldAsk(s, run, "zz why")
	open, err := s.Asks("open")
	if err != nil || len(open) != 1 {
		t.Fatalf("one open question: %v %v", open, err)
	}
	if err := s.CloseAsk(open[0].ID, "answered", answer, "zz-owner"); err != nil {
		t.Fatal(err)
	}
}

// heldRunner is a held run with a closed task session sid-1 and an open PR of the given mergeability.
func heldRunner(t *testing.T, mergeable string) (*Runner, *SQLite, *Run) {
	t.Helper()
	r, s := testRunner(t, "")
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, f := range []string{"task.json.in", "task.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", f), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(wt, ".git"), 0o755)
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "ls" {
			return 0, `{"terminals": []}`, "" // hold closed the session
		}
		return 0, "{}", ""
	}
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "view" {
			return 0, `{"number":1,"state":"OPEN","headRefOid":"a","mergeable":"` + mergeable + `"}`, ""
		}
		return 1, "", "no"
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: wt,
		Agent: "claude-coder-t-0001-a7", State: Merging, Round: 1, Head: "a", JudgeHead: "a", Model: "claude-sonnet-5-5",
		Effort: "medium", PRURL: "https://github.com/zz-test/www/pull/1"}
	_ = s.AddSession(Session{SID: "sid-1", Task: "T-0001", Role: "task", Agent: run.Agent, Model: run.Model, Effort: run.Effort,
		Started: nowISO()})
	r.hold(run, "judge HOLD: zz")
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return r, s, run
}

func pass(t *testing.T, r *Runner, s *SQLite) *Run {
	t.Helper()
	if err := r.Pass(false); err != nil {
		t.Fatal(err)
	}
	run, err := s.Run("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	return run
}

func lastPrompt(t *testing.T, r *Runner) string {
	t.Helper()
	files, _ := filepath.Glob(filepath.Join(r.Env.StateDir(), "prompts", "T-0001", "*.txt"))
	if len(files) == 0 {
		t.Fatal("no prompt was sent")
	}
	b, err := os.ReadFile(files[len(files)-1])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestApprovedAnswerMovesHeldToMerging(t *testing.T) {
	// a deleted guard test: a T3 hard limit, which the owner approves
	f := newT3(t, map[string]string{"go/internal/guard/zz_test.go": ""})
	r, s := f.r, f.s
	// the judge's HOLD is on record for the head, as after a judge HOLD
	time.Sleep(time.Second)
	if err := s.Verdict("T-0001", f.run.PRURL, f.run.Head, "fail", "HOLD", "zz-judge"); err != nil {
		t.Fatal(err)
	}
	f.run.JudgeHead = f.run.Head
	r.hold(f.run, "judge HOLD: zz")
	_ = s.PutRun(f.run)
	answerHeld(t, r, s, f.run, "  APPROVED by the director")

	got := pass(t, r, s)
	if got.State != Merging || got.Approved != f.run.Head || got.Round != 1 {
		t.Fatalf("approved: %s approved %q round %d", got.State, got.Approved, got.Round)
	}
	if ev := f.events(t, "ask.applied"); len(ev) != 1 || ev[0].Data["state"] != string(Merging) {
		t.Errorf("ask.applied: %+v", ev)
	}
	// the used answer cannot act again, and the round can get a new question
	if a, _ := heldAnswer(s, got); a != nil {
		t.Errorf("the answer is still pending: %+v", a)
	}
	got = pass(t, r, s)
	if got.State != Merged || len(f.merges) != 1 {
		t.Errorf("the normal merge path merges the approved head: %s %q, %d merges", got.State, got.MergeHold, len(f.merges))
	}

	var note string
	if err := s.db.QueryRow(`SELECT detail FROM events WHERE action='merged'`).Scan(&note); err != nil || !strings.Contains(note, "owner approval") {
		t.Errorf("the merge is recorded as the owner's: %q %v", note, err)
	}
	// a refresh carries the approval, never as a judge pass on the held head
	wt := got.Worktree
	sh(t, wt, "fetch", "-q", "origin", "main")
	sh(t, wt, "checkout", "-q", "-b", "zz-main", "origin/main")
	_ = os.WriteFile(filepath.Join(wt, "zz-main.txt"), []byte("zz\n"), 0o644)
	sh(t, wt, "add", "-A")
	sh(t, wt, "commit", "-q", "-m", "main moves")
	sh(t, wt, "push", "-q", "origin", "HEAD:main")
	sh(t, wt, "checkout", "-q", "factory/t-0001")
	sh(t, wt, "fetch", "-q", "origin", "main")
	old := got.Head
	r.refresh(got)
	if v, _, _ := s.LaneVerdict("T-0001", got.Head); got.Head == old || got.Approved != got.Head || v != "" {
		t.Errorf("refresh: head %s approved %q verdict %q %q", got.Head, got.Approved, v, got.MergeHold)
	}

	// another head is not approved: it needs the judge again, and the tier rule holds
	in := MergeInput{PRState: "OPEN", Head: "b"}
	if d := Decide(in); d.Action != "rejudge" {
		t.Errorf("unapproved head: %+v", d)
	}
}

func TestChangeAnswerResumesHeldRun(t *testing.T) {
	r, s, run := heldRunner(t, "MERGEABLE")
	answerHeld(t, r, s, run, "change needed: zz rename the flag")
	got := pass(t, r, s)
	if got.State != Working || got.Round != 2 || got.Approved != "" || got.Outcome != "" || got.MergeHold != "" {
		t.Fatalf("resumed: %s round %d approved %q outcome %q", got.State, got.Round, got.Approved, got.Outcome)
	}
	script, _ := os.ReadFile(filepath.Join(r.Env.StateDir(), "spawn", run.Agent+".sh"))
	if !strings.Contains(string(script), "--resume sid-1") {
		t.Errorf("the closed session restarts by session id: %q", script)
	}
	if p := lastPrompt(t, r); !strings.Contains(p, "change needed: zz rename the flag") {
		t.Errorf("the prompt carries the answer: %q", p)
	}
	evs, _ := r.Log.Read()
	n := 0
	for _, e := range evs {
		if e.Event == "ask.applied" && e.Data["state"] == string(Working) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d ask.applied events", n)
	}
	// held again in a later run of the task: the old answer is retired, so a new question is filed
	if err := s.RetireHeldAsks("T-0001"); err != nil {
		t.Fatal(err)
	}
	got.Round = 1
	r.hold(got, "judge HOLD: zz again")
	_ = s.PutRun(got)
	r.fileHeldAsk(s, got, "zz again")
	if open, _ := s.Asks("open"); len(open) != 1 {
		t.Errorf("a new question: %+v", open)
	}
	if next := pass(t, r, s); next.State != Held {
		t.Errorf("the old answer acted again: %s", next.State)
	}
}

func TestHeldConflictingResumes(t *testing.T) {
	r, s, run := heldRunner(t, "CONFLICTING")
	// a run with no session (a revert) stays with the owner
	agent := run.Agent
	run.Agent = ""
	_ = s.PutRun(run)
	if got := pass(t, r, s); got.State != Held {
		t.Fatalf("no session to resume: %s %q", got.State, got.FailReason)
	}
	run.Agent = agent
	_ = s.PutRun(run)
	got := pass(t, r, s)
	if got.State != Working || got.Round != 2 {
		t.Fatalf("conflicting held run: %s round %d", got.State, got.Round)
	}
	if p := lastPrompt(t, r); !strings.Contains(p, "merge origin/main and resolve, keep the approved changes") {
		t.Errorf("prompt: %q", p)
	}
}

func TestUnansweredHeldStays(t *testing.T) {
	r, s, run := heldRunner(t, "MERGEABLE")
	r.fileHeldAsk(s, run, "zz why")
	for i := 0; i < 2; i++ {
		if got := pass(t, r, s); got.State != Held || got.Round != 1 {
			t.Fatalf("unanswered: %s round %d", got.State, got.Round)
		}
	}
	// an answer to another task's question does nothing
	id, _, _ := s.AddAsk(Ask{Ref: "held:T-0002:1", Task: "T-0002", Question: "zz", AskedBy: "runner"})
	_ = s.CloseAsk(id, "answered", "approved", "zz-owner")
	if got := pass(t, r, s); got.State != Held {
		t.Errorf("another task's answer: %s", got.State)
	}
}
