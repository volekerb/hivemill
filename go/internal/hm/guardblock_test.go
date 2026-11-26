package hm

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// stoppedOn is an idle task session whose last tool result is the given one; it returns the prompts the runner sends.
func stoppedOn(t *testing.T, result string, isError bool) (*Runner, *Run, *[]string) {
	t.Helper()
	r, s := testRunner(t, "")
	var sent []string
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "send" {
			sent = append(sent, args[2])
		}
		return 0, `{"terminals": []}`, ""
	}
	if err := s.AddSession(Session{SID: "sid-g", Task: "T-0001", Role: "task", Agent: "a", Started: nowISO()}); err != nil {
		t.Fatal(err)
	}
	line, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "tool_result", "is_error": isError, "content": []any{map[string]any{"type": "text", "text": result}}}}}})
	dir := filepath.Join(r.Env.UserHome, ".claude", "projects", "p")
	_ = os.MkdirAll(dir, 0o755)
	if err := os.WriteFile(filepath.Join(dir, "sid-g.jsonl"), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	run := &Run{Task: "T-0001", Agent: "a", State: Working, Worktree: t.TempDir(),
		IdleSince: time.Now().Add(-time.Hour).Format(time.RFC3339)}
	return r, run, &sent
}

// guardResume runs one idle pass on a blocked session and checks the prompt and the event.
func guardResume(t *testing.T, block, class string, want ...string) {
	t.Helper()
	r, run, sent := stoppedOn(t, block, true)
	r.idleWatch(run, []Terminal{{Name: "a", Status: "idle"}})
	if len(*sent) != 1 || run.IdleNudges != 1 || run.State != Working {
		t.Fatalf("sent %q nudges %d state %s", *sent, run.IdleNudges, run.State)
	}
	for _, w := range append(want, block) {
		if !strings.Contains((*sent)[0], w) {
			t.Errorf("prompt misses %q:\n%s", w, (*sent)[0])
		}
	}
	ev, _ := os.ReadFile(r.Log.Path)
	if !strings.Contains(string(ev), `"event":"run.guard_block","task_id":"T-0001"`) ||
		!strings.Contains(string(ev), `"class":"`+class+`"`) || strings.Contains(string(ev), "session.nudged") {
		t.Errorf("events: %s", ev)
	}
}

func TestGuardBlockResumePublicText(t *testing.T) {
	guardResume(t, "PreToolUse:Bash hook error: worker-guard: blocked (public text: 3 hits: private-host)\n", "public-text",
		"your diff contains private text (3 hits: private-host). Remove or generalize that text")
}

func TestGuardBlockResumeReadBefore(t *testing.T) {
	guardResume(t, "worker-guard(go): blocked (text file '.hivemill/body.md' is read before the command runs, so the command "+
		"must be only the gh call)", "read-before",
		"Split it: write the file in one command, then read or use it in the next command.")
}

func TestGuardBlockResumeOther(t *testing.T) {
	guardResume(t, "worker-guard: blocked (kill needs a literal process id)", "other",
		"Do not run it in another form.", ".hivemill/blocked.md")

	// A command that only prints a marker succeeded: the session gets the generic nudge.
	r, run, sent := stoppedOn(t, "worker-guard: blocked (x)", false)
	r.idleWatch(run, []Terminal{{Name: "a", Status: "idle"}})
	if len(*sent) != 1 || !strings.HasPrefix((*sent)[0], "You stopped before your task was finished") {
		t.Errorf("sent %q", *sent)
	}
	// Nor is a failed command whose output has a marker further down.
	if isGuardBlock("Exit code 1\n--- FAIL: TestX\nworker-guard: blocked (x)") {
		t.Error("test output counted as a block")
	}
}

func TestBlockedMdFails(t *testing.T) {
	const want = "blocked by policy: kill of the test server is denied"
	write := func(wt string) {
		_ = os.MkdirAll(filepath.Join(wt, ".hivemill"), 0o755)
		if err := os.WriteFile(filepath.Join(wt, ".hivemill", "blocked.md"),
			[]byte("\n# kill of the test server is denied\nkill \"$server\"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(r *Runner, run *Run) {
		t.Helper()
		ev, _ := os.ReadFile(r.Log.Path)
		if run.State != Failed || !strings.Contains(string(ev), want) || strings.Contains(string(ev), "stalled") {
			t.Fatalf("state %s events %s", run.State, ev)
		}
		var reasons []string
		for _, l := range strings.Split(string(ev), "\n") {
			if why := wakeReason([]byte(l)); why != "" {
				reasons = append(reasons, why)
			}
		}
		if len(reasons) != 1 || reasons[0] != "task.refile:T-0001" {
			t.Errorf("planner reasons %q", reasons)
		}
	}

	// the idle watch, without waiting for the idle spell
	r, run, sent := stoppedOn(t, "ok", false)
	run.IdleSince = ""
	write(run.Worktree)
	r.idleWatch(run, []Terminal{{Name: "a", Status: "idle"}})
	check(r, run)
	if len(*sent) != 0 {
		t.Errorf("sent %q", *sent)
	}

	// the Stop hook, before a PR exists
	r, s := testRunner(t, "claude-task-t-0001")
	run = &Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: t.TempDir(),
		Branch: "factory/t-0001", Agent: "claude-task-t-0001", State: Working, Round: 1}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	write(run.Worktree)
	if err := r.GateHook(strings.NewReader(`{"session_id":"s-1"}`), &strings.Builder{}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Run("T-0001")
	if err != nil {
		t.Fatal(err)
	}
	check(r, got)
}

func TestTaskPromptKeepsNoCircumvention(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("assets", "task.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"A denied command stays denied: never retry it in another spelling or wrapper.",
		"Fixing your own content (for example, removing private text) and choosing a different approach are allowed.",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("the task prompt misses %q", want)
		}
	}
}
