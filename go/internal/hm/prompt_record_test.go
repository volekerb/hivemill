package hm

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// promptRunner is a test runner whose hc starts terminals that read as idle and keeps every sent text.
func promptRunner(t *testing.T) (*Runner, *Run, *[]string) {
	t.Helper()
	r, s := testRunner(t, "")
	var sent []string
	name := "claude-coder-t-0001-a7"
	started := false
	r.Tools.HC = func(args ...string) (int, string, string) {
		switch args[0] {
		case "run":
			started = true
		case "send":
			sent = append(sent, args[2])
		case "ls":
			if started {
				return 0, `{"terminals": [{"name": "` + name + `", "state": "live", "agent": "claude-code", "status": "idle"}]}`, ""
			}
			return 0, `{"terminals": []}`, ""
		}
		return 0, "{}", ""
	}
	r.ReadyWait = time.Second
	_ = os.MkdirAll(filepath.Join(r.Env.Home, "roles", "hivemill"), 0o755)
	for _, f := range []string{"task.json.in", "task.md"} {
		_ = os.WriteFile(filepath.Join(r.Env.Home, "roles", "hivemill", f), []byte(`{}`), 0o644)
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
	run := &Run{Task: "T-0001", Goal: "G-001", Worktree: t.TempDir(), Base: "main", Agent: name, State: Working, Round: 1,
		Model: "claude-sonnet-5-5", Effort: "medium"}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	return r, run, &sent
}

func eventsOf(t *testing.T, r *Runner, kind string) []Event {
	t.Helper()
	f, err := os.Open(r.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Event
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var e Event
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if e.Event == kind {
			out = append(out, e)
		}
	}
	return out
}

func TestPromptRecordedFirstAndResume(t *testing.T) {
	r, run, sent := promptRunner(t)
	first, second := "first prompt: zz-test é", "resume text\nwith two lines"
	if err := r.launchTask(run, first); err != nil {
		t.Fatal(err)
	}
	r.resume(run, second)
	if len(*sent) != 2 {
		t.Fatalf("sent %q", *sent)
	}
	ev := eventsOf(t, r, "prompt.sent")
	if len(ev) != 2 {
		t.Fatalf("prompt.sent events: %+v", ev)
	}
	for i, want := range []struct {
		kind, file, text string
		chars            int
	}{{"first", "001-first.txt", first, 23}, {"resume", "002-resume.txt", second, 26}} {
		e, rel := ev[i], "prompts/T-0001/"+want.file
		sum := fmt.Sprintf("%x", sha256.Sum256([]byte(want.text)))
		if e.TaskID != "T-0001" || e.Role != "task" || e.Data["kind"] != want.kind || e.Data["path"] != rel ||
			e.Data["chars"] != float64(want.chars) || e.Data["sha256"] != sum {
			t.Errorf("event %d: %+v", i, e)
		}
		p := filepath.Join(r.Env.StateDir(), rel)
		if b, err := os.ReadFile(p); err != nil || string(b) != want.text {
			t.Errorf("%s: %q %v", want.file, b, err)
		}
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode: %v %v", want.file, fi, err)
		}
	}
	if fi, err := os.Stat(filepath.Join(r.Env.StateDir(), "prompts", "T-0001")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("dir mode: %v %v", fi, err)
	}
}

func TestPromptRecordFailureStillSends(t *testing.T) {
	r, run, sent := promptRunner(t)
	// a file where the prompts dir belongs: unwritable even for root
	if err := os.MkdirAll(r.Env.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.Env.StateDir(), "prompts"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	r.resume(run, "go on")
	if len(*sent) != 1 || !strings.Contains((*sent)[0], "go on") || run.State != Working {
		t.Fatalf("sent %q, state %s", *sent, run.State)
	}
	failed := eventsOf(t, r, "prompt.record_failed")
	if len(failed) != 1 || failed[0].TaskID != "T-0001" || failed[0].Data["kind"] != "resume" {
		t.Errorf("prompt.record_failed: %+v", failed)
	}
	if ev := eventsOf(t, r, "prompt.sent"); len(ev) != 0 {
		t.Errorf("a failed record is not prompt.sent: %+v", ev)
	}
}
