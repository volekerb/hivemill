package hm

import (
	"errors"
	"strings"
	"testing"
)

func readyIDs(t *testing.T, r *Runner) map[string]bool {
	t.Helper()
	ready, _, err := r.Queue([]string{"www"})
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, x := range ready {
		out[x.ID] = true
	}
	return out
}

func failRun(t *testing.T, r *Runner, s *SQLite, id, dispatched, reason string) {
	t.Helper()
	run := &Run{Task: id, Goal: "G-001", Project: "www", Repo: "zz-test/www", Dispatched: dispatched}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	r.fail(run, reason)
}

func TestRetryInPlace(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	id := refileAdd(t, s, "zz-test task", "todo", "[]")
	if _, err := r.Retry(id, ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("never ran: %v", err)
	}
	failRun(t, r, s, id, "2026-11-27T01:00:00Z", "stalled: idle")
	if readyIDs(t, r)[id] {
		t.Fatal("a failed run is not started again without a retry")
	}
	out, err := r.Retry(id, "zz-test fix the proof")
	if err != nil || !strings.Contains(out, "attempt 2 of 3") {
		t.Fatalf("retry: %q %v", out, err)
	}
	if tk, _ := s.Task(id); tk.Status != "todo" {
		t.Fatalf("status %s, want todo", tk.Status)
	}
	if !readyIDs(t, r)[id] {
		t.Fatal("a retried task is ready")
	}
	if out, err := r.Retry(id, ""); err != nil || !strings.Contains(out, "already retried") {
		t.Fatalf("second retry before the run: %q %v", out, err)
	}
	if m := r.retryMark(id); m.N != 1 || m.Note != "zz-test fix the proof" {
		t.Fatalf("mark %+v", m)
	}

	// the new run replaces the failed one: no longer pending, and its failure needs its own retry
	failRun(t, r, s, id, "2026-11-27T02:00:00Z", "budget")
	if readyIDs(t, r)[id] {
		t.Fatal("second failure: not ready until retried")
	}
	if _, err := r.Retry(id, ""); err != nil {
		t.Fatal(err)
	}
	failRun(t, r, s, id, "2026-11-27T03:00:00Z", "budget")
	if _, err := r.Retry(id, ""); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "ask the owner") {
		t.Fatalf("past retry_max: %v", err)
	}
	if err := s.SetCfg("retry_max", "3"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Retry(id, ""); err != nil {
		t.Fatalf("retry_max raised: %v", err)
	}
}

func TestRetryRefusesHeldAndOwnerClosed(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	held := refileAdd(t, s, "zz-test held", "blocked", "[]")
	if err := s.PutRun(&Run{Task: held, State: Held}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Retry(held, ""); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "held") {
		t.Fatalf("held: %v", err)
	}
	closed := refileAdd(t, s, "zz-test closed", "todo", "[]")
	failRun(t, r, s, closed, "2026-11-27T01:00:00Z", "the owner closed the held PR")
	if _, err := r.Retry(closed, ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("owner closed: %v", err)
	}
	gone := refileAdd(t, s, "zz-test cancelled", "todo", "[]")
	failRun(t, r, s, gone, "2026-11-27T01:00:00Z", "stalled")
	cancelTask(t, s, gone)
	if _, err := r.Retry(gone, ""); !errors.Is(err, ErrRefused) {
		t.Fatalf("cancelled: %v", err)
	}
}

func TestRetryBriefKeepsThePR(t *testing.T) {
	b := retryBrief(2, &Run{FailReason: "gate blocked 3 times"}, "factory/t-0001", "https://github.com/zz-test/www/pull/7", "zz-test note")
	for _, want := range []string{"attempt 2", "gate blocked 3 times", "factory/t-0001", "pull/7", "never open a second PR", "zz-test note"} {
		if !strings.Contains(b, want) {
			t.Errorf("brief misses %q: %s", want, b)
		}
	}
}

func TestCloseTaskPR(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	id := refileAdd(t, s, "zz-test task", "todo", "[]")
	tk, _ := s.Task(id)
	var calls []string
	r.Tools.GH = func(_ string, args ...string) (int, string, string) {
		calls = append(calls, strings.Join(args, " "))
		if args[1] == "list" {
			return 0, "https://github.com/zz-test/www/pull/9\n", ""
		}
		return 0, "", ""
	}
	pr, err := r.CloseTaskPR(tk)
	if err != nil || pr != "https://github.com/zz-test/www/pull/9" {
		t.Fatalf("close: %q %v", pr, err)
	}
	if len(calls) != 2 || !strings.Contains(calls[0], "--head factory/"+strings.ToLower(id)) || !strings.HasPrefix(calls[1], "pr close https://github.com/zz-test/www/pull/9") {
		t.Fatalf("gh calls %q", calls)
	}
	r.Tools.GH = func(string, ...string) (int, string, string) { return 0, "", "" }
	if pr, err := r.CloseTaskPR(tk); pr != "" || err != nil {
		t.Fatalf("no PR: %q %v", pr, err)
	}
}
