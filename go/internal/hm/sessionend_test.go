package hm

import (
	"os"
	"strings"
	"testing"
)

func TestFailedRunEndsItsSessions(t *testing.T) {
	r, s := testRunner(t, "")
	var killed []string
	r.Tools.HC = func(a ...string) (int, string, string) {
		switch a[0] {
		case "ls":
			return 0, `{"terminals": [{"name": "claude-task-t-0001", "state": "live"}, {"name": "claude-judge-t-0001", "state": "live"}]}`, ""
		case "kill":
			// the terminal state is already saved when a session closes
			if run, _ := s.Run("T-0001"); run == nil || run.State != Failed {
				t.Errorf("kill %s before the failed run was saved", a[1])
			}
			killed = append(killed, a[1])
		}
		return 0, "", ""
	}
	wt := t.TempDir()
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Agent: "claude-task-t-0001", Machine: "zz-worker",
		Judges: []string{"claude-judge-t-0001"}, Worktree: wt, State: Working}
	if err := s.PutRun(run); err != nil {
		t.Fatal(err)
	}
	r.fail(run, "zz-test")
	if got := strings.Join(killed, " "); got != "claude-judge-t-0001 claude-task-t-0001" {
		t.Errorf("killed %q", got)
	}
	b, err := os.ReadFile(r.Log.Path)
	if err != nil {
		t.Fatal(err)
	}
	var closed []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, `"session.closed"`) {
			closed = append(closed, line)
		}
	}
	if len(closed) != 2 || !strings.Contains(closed[1], "T-0001") || !strings.Contains(closed[1], `"machine":"zz-worker"`) {
		t.Errorf("session.closed lines: %q", closed)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Errorf("the worktree must stay: %v", err)
	}
}
