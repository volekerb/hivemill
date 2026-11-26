package hm

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func putRuns(t *testing.T, s *SQLite, runs ...*Run) {
	t.Helper()
	for _, x := range runs {
		if err := s.PutRun(x); err != nil {
			t.Fatal(err)
		}
	}
}

// one run in each waiting state
func waitingRuns() []*Run {
	var out []*Run
	for i, st := range []string{Gated, Judged, Merging, Held} {
		out = append(out, &Run{Task: fmt.Sprintf("T-010%d", i), State: st})
	}
	return out
}

func TestDispatchIgnoresWaitingRuns(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("max_concurrent", "2")
	putRuns(t, s, waitingRuns()...)
	putRuns(t, s, &Run{Task: "T-0001", State: Working}, &Run{Task: "T-0002", State: Merged})
	if working, waits := r.runCounts(); working != 1 || waits != 4 {
		t.Fatalf("working %d waiting %d, want 1 and 4", working, waits)
	}
	if why := r.runCapBlock(); why != "" {
		t.Errorf("one working run of two, four waiting: %q", why)
	}
	putRuns(t, s, &Run{Task: "T-0003", State: Judging})
	if why := r.runCapBlock(); !strings.HasPrefix(why, "max_concurrent=2") {
		t.Errorf("two working runs: %q", why)
	}
}

func TestDispatchPausesAtMaxWaiting(t *testing.T) {
	r, s := testRunner(t, "")
	_ = s.SetCfg("max_waiting", "4")
	events := func() int {
		b, _ := os.ReadFile(r.Log.Path)
		return strings.Count(string(b), `"dispatch.paused_waiting"`)
	}
	runs := waitingRuns()
	putRuns(t, s, runs[:3]...)
	if why := r.runCapBlock(); why != "" || events() != 0 {
		t.Fatalf("below the cap: %q, %d events", why, events())
	}
	putRuns(t, s, runs[3])
	for range 2 {
		if why := r.runCapBlock(); !strings.HasPrefix(why, "max_waiting=4") {
			t.Fatalf("at the cap: %q", why)
		}
	}
	if events() != 1 {
		t.Errorf("one event per pause, got %d", events())
	}
	_, err := s.db.Exec(`INSERT INTO tasks(id,goal_id,title,spec,status,acceptance) VALUES('T-0009','G-001','x','spec','todo','1. x. cmd:true => exit 0')`)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start("T-0009"); !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "max_waiting=4") {
		t.Errorf("start at the cap: %v", err)
	}
	_ = s.SetMeta("waiting_paused", "x")
	runs[0].State = Merged
	putRuns(t, s, runs[0])
	if why := r.runCapBlock(); why != "" {
		t.Fatalf("below the cap again: %q", why)
	}
	// a pass that dispatches nothing still ends a pause that is over
	_ = s.SetCfg("max_waiting", "5")
	if err := r.Pass(false); err != nil || s.Meta("waiting_paused") != "" {
		t.Fatalf("pass below the cap: %v, mark %q", err, s.Meta("waiting_paused"))
	}
	_ = s.SetCfg("max_waiting", "4")
	runs[0].State = Held
	putRuns(t, s, runs[0])
	r.runCapBlock()
	if events() != 2 {
		t.Errorf("a new pause emits again, got %d events", events())
	}
}

func TestPickMachineCountsOpenWaitingSessions(t *testing.T) {
	r, s, _ := placementRunner(t, map[string]fakeWorker{
		"w1": {memMB: 9000, shared: true, coord: "c"},
		"w2": {memMB: 9000, shared: true, coord: "c"},
	})
	withLocalMem(t, 8000)
	_ = s.SetCfg("session_machines", "w1,w2")
	hc := r.Tools.HC
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "ls" {
			return 0, `{"terminals": [{"name": "open-a", "state": "live"}, {"name": "open-b", "state": "live"}]}`, ""
		}
		return hc(args...)
	}
	// w1 has two waiting runs with open sessions, w2 one working run
	putRuns(t, s,
		&Run{Task: "T-0001", State: Gated, Machine: "w1", Agent: "open-a"},
		&Run{Task: "T-0002", State: Held, Machine: "w1", Agent: "open-b"},
		&Run{Task: "T-0003", State: Working, Machine: "w2", Agent: "busy-c"})
	if pl := r.PickMachine(); pl.Machine != "w2" {
		t.Errorf("open waiting sessions count: %+v", pl)
	}
	// closed sessions of waiting runs do not
	putRuns(t, s,
		&Run{Task: "T-0001", State: Merging, Machine: "w1", Agent: "gone-a"},
		&Run{Task: "T-0002", State: Held, Machine: "w1", Agent: "gone-b"})
	if pl := r.PickMachine(); pl.Machine != "w1" {
		t.Errorf("closed waiting sessions do not count: %+v", pl)
	}
}
