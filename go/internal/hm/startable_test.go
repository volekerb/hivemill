package hm

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

// queueFixture has T-0001 startable, T-0002 failing the spec gate, T-0003 waiting on T-0001 and an unknown task, T-0004
// with a run and T-0005 startable because its dependency is done (T-0006).
func queueFixture(t *testing.T) (*Runner, *SQLite, *fakeHC) {
	t.Helper()
	r, s, f := plannerRunner(t, "")
	if err := s.SetCfg("projects", "www"); err != nil {
		t.Fatal(err)
	}
	add := func(acceptance, status, deps string) {
		if _, err := s.AddTask("G-001", "zz-test task", "spec", acceptance, status, deps, 1); err != nil {
			t.Fatal(err)
		}
	}
	good := "1. works - cmd:true => exit 0"
	add(good, "todo", "[]")
	add("1. works", "todo", `["T-0001"]`)
	add(good, "todo", `["T-0001","T-0099"]`)
	add(good, "todo", "[]")
	add(good, "todo", `["T-0006"]`)
	add(good, "done", "[]")
	if err := s.PutRun(&Run{Task: "T-0004", Goal: "G-001", Project: "www", State: Failed}); err != nil {
		t.Fatal(err)
	}
	return r, s, f
}

func listQueue(t *testing.T, r *Runner, blocked bool) []string {
	t.Helper()
	var b strings.Builder
	if err := r.ListQueue(&b, blocked); err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(b.String()), "\n")
}

func TestTaskListStartable(t *testing.T) {
	r, _, _ := queueFixture(t)
	got := listQueue(t, r, false)
	if len(got) != 2 || !strings.HasPrefix(got[0], "T-0001 ") || !strings.HasPrefix(got[1], "T-0005 ") {
		t.Fatalf("startable: %q", got)
	}
	cands, err := r.Candidates()
	if err != nil || len(cands) != 2 || cands[0].ID != "T-0001" || cands[1].ID != "T-0005" {
		t.Fatalf("a pass would start other tasks: %v %v", cands, err)
	}
}

func TestTaskListBlockedReasons(t *testing.T) {
	r, _, _ := queueFixture(t)
	got := listQueue(t, r, true)
	want := []string{"T-0002 G-001 zz-test task: lint: criterion 1 names no proof", "T-0003 G-001 zz-test task: deps: T-0001 (todo), T-0099 (missing)",
		"T-0004 G-001 zz-test task: has a run"}
	if len(got) != len(want) {
		t.Fatalf("blocked: %q", got)
	}
	for i := range want {
		if !strings.HasPrefix(got[i], want[i]) {
			t.Errorf("line %d: %q, want %q", i, got[i], want[i])
		}
	}
}

func TestLintFailingTodoMakesPlannerItem(t *testing.T) {
	r, s, f := queueFixture(t)
	_ = s.SetMeta("planner", "on")
	f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
	events := func() (out []Event) {
		all, err := r.Log.Read()
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range all {
			if e.Event == "task.unstartable" {
				out = append(out, e)
			}
		}
		return out
	}
	pass := func() {
		t.Helper()
		if err := r.Pass(false); err != nil {
			t.Fatal(err)
		}
	}
	pass()
	pass()
	ev := events()
	if len(ev) != 1 || ev[0].TaskID != "T-0002" || ev[0].Project != "www" || !strings.HasPrefix(ev[0].Data["reason"].(string), "lint: criterion 1") {
		t.Fatalf("events: %+v", ev)
	}
	lv, err := r.PlannerLevels()
	if err != nil || strings.Count(strings.Join(lv, " "), "unstartable:") != 1 || !contains(lv, "unstartable:T-0002") {
		t.Fatalf("levels: %v %v", lv, err)
	}
	now := time.Now()
	r.PlannerPass(now)
	r.PlannerPass(now.Add(time.Hour))
	if got := f.sent(); len(got) != 1 || !strings.Contains(got[0], "unstartable:T-0002") {
		t.Fatalf("wakes: %q", got)
	}
	// paused and resumed: the same failure, no second report
	_ = s.SetCfg("projects", "")
	pass()
	_ = s.SetCfg("projects", "www")
	pass()
	if n := len(events()); n != 1 {
		t.Fatalf("%d events after the project came back", n)
	}
	// fixed, then broken again: that is a new report
	_ = s.UpdateTask("T-0002", map[string]any{"acceptance": "1. works - cmd:true => exit 0"})
	pass()
	if lv, _ := r.PlannerLevels(); strings.Contains(strings.Join(lv, " "), "unstartable:") {
		t.Fatalf("a fixed task keeps its item: %v", lv)
	}
	_ = s.UpdateTask("T-0002", map[string]any{"acceptance": "1. works"})
	pass()
	if n := len(events()); n != 2 {
		t.Fatalf("%d events after a second failure", n)
	}
}

// Todo tasks that cannot start are not a queue: the idle alert, queue_low and the wake reason count startable ones.
func TestIdleAlertUsesStartable(t *testing.T) {
	r, s, _ := queueFixture(t)
	now := time.Now()
	_ = s.PutRun(&Run{Task: "T-0004", Goal: "G-001", Project: "www", State: Failed, Dispatched: now.Add(-time.Hour).Format(time.RFC3339)})
	idle := func() string {
		t.Helper()
		c := r.OwnerChecks(now)
		if len(c) != 1 || c[0].Kind != "hivemill-idle" {
			t.Fatalf("checks: %+v", c)
		}
		return c[0].Text
	}
	if text := idle(); strings.Contains(text, "startable") {
		t.Fatalf("2 tasks are startable: %s", text)
	}
	if lv, _ := r.PlannerLevels(); !contains(lv, "queue_low:www=2/3") || strings.Contains(strings.Join(lv, " "), "none_startable") {
		t.Fatalf("levels: %v", lv)
	}
	for _, id := range []string{"T-0001", "T-0005"} {
		_ = s.UpdateTask(id, map[string]any{"depends_on": `["T-0099"]`})
	}
	if text := idle(); !strings.Contains(text, "0 startable tasks") || !strings.Contains(text, "5 todo: 3 blocked by deps, 1 by lint, 1 have a run") {
		t.Fatalf("idle alert: %s", text)
	}
	lv, _ := r.PlannerLevels()
	if !contains(lv, "queue_low:www=0/3") || !contains(lv, "none_startable:todo=5,blocked_by_deps=3,blocked_by_lint=1,have_a_run=1") {
		t.Fatalf("levels: %v", lv)
	}
	// other counts are not news
	d := DecideWake(WakeInput{Levels: []string{"none_startable:todo=6,blocked_by_deps=4,blocked_by_lint=1"},
		LastFingerprint: "none_startable:todo=5,blocked_by_deps=3,blocked_by_lint=1", Idle: true})
	if d.Wake {
		t.Fatalf("woken for changed counts: %+v", d)
	}
	var many []string
	for i := range maxUnstartableIDs + 2 {
		many = append(many, fmt.Sprintf("unstartable:T-%04d", i))
	}
	if got := Coalesce(nil, many); len(got) != 1 || !strings.HasSuffix(got[0], ",T-0039,+2_more") {
		t.Fatalf("grouped: %q", got)
	}
	// no free slot: the queue is not starved yet
	_ = s.SetCfg("max_concurrent", "1")
	_ = s.PutRun(&Run{Task: "T-0098", Goal: "G-001", Project: "www", State: Working})
	if lv, _ := r.PlannerLevels(); strings.Contains(strings.Join(lv, " "), "none_startable") {
		t.Fatalf("no slot is free: %v", lv)
	}
}
