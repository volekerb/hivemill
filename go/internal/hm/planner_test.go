package hm

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHC is an hc that lists the given terminals and records every call.
type fakeHC struct {
	terms []Terminal
	calls [][]string
}

func (f *fakeHC) hc(args ...string) (int, string, string) {
	f.calls = append(f.calls, args)
	switch args[0] {
	case "ls":
		b, _ := json.Marshal(map[string]any{"terminals": f.terms})
		return 0, string(b), ""
	case "server":
		return 0, `{"core":{"name":"core-a"}}`, ""
	}
	return 0, "", ""
}

func (f *fakeHC) sent() []string {
	var out []string
	for _, c := range f.calls {
		if c[0] == "send" && c[1] == PlannerName {
			out = append(out, c[2])
		}
	}
	return out
}

func (f *fakeHC) count(verb string) int {
	n := 0
	for _, c := range f.calls {
		if c[0] == verb {
			n++
		}
	}
	return n
}

// plannerRunner is a runner on a full standalone factory DB (CreateSQLite) with one project, one active goal and a fake hc.
func plannerRunner(t *testing.T, actor string) (*Runner, *SQLite, *fakeHC) {
	t.Helper()
	home := t.TempDir()
	s, err := CreateSQLite(filepath.Join(home, "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	if err := s.AddProject(Project{ID: "www", RepoPath: "/nonexistent", GHRepo: "zz-test/www"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.NewGoal("www", "zz-test goal", "", "", 2); err != nil {
		t.Fatal(err)
	}
	f := &fakeHC{}
	e := Env{Home: home, UserHome: t.TempDir(), Actor: actor}
	_ = os.WriteFile(filepath.Join(e.UserHome, ".claude.json"), []byte("{}\n"), 0o600)
	tools := Tools{
		Git: func(string, ...string) (int, string, string) { return 1, "", "no" },
		GH:  func(string, ...string) (int, string, string) { return 1, "", "no" },
		HC:  f.hc,
		Sh:  func(string, time.Duration, []string, ...string) (int, string) { return 0, "" },
	}
	return &Runner{Env: e, Store: s, Tools: tools, Log: Log{Path: filepath.Join(e.StateDir(), "events.jsonl")}, Out: io.Discard,
		ReadyWait: 5 * time.Millisecond, Poll: time.Millisecond}, s, f
}

func addReady(t *testing.T, s *SQLite, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if _, err := s.AddTask("G-001", fmt.Sprintf("zz-test task %d", i), "spec", "1. works - cmd:true => exit 0", "todo", "", 1); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCoalesce(t *testing.T) {
	if got := Coalesce([]string{"task.failed:T-1", "run.held:T-3", "task.failed:T-2", "task.failed:T-1", "budget.reached"}, []string{"queue_low:www=0/3"}); strings.Join(got, " ") !=
		"task.failed:T-1,T-2 run.held:T-3 budget.reached queue_low:www=0/3" {
		t.Errorf("coalesce: %v", got)
	}
}

// ackAll marks every open work item done, as the planner does at the end of a wake.
func ackAll(t *testing.T, s *SQLite, now time.Time) {
	t.Helper()
	rows, _ := s.PlannerWorkOpenRows()
	var ids []int64
	for _, w := range rows {
		ids = append(ids, w.ID)
	}
	if len(ids) > 0 {
		if err := s.PlannerWorkDone(ids, now); err != nil {
			t.Fatal(err)
		}
	}
}

// The daemon's planner pass wakes once, coalesces what happens in the next 10 minutes into one wake, delivers an item again
// only when it was not acknowledged, and waits while the planner works.
func TestPlannerPassDebouncesAndCoalesces(t *testing.T) {
	r, s, f := plannerRunner(t, "")
	_ = s.SetMeta("planner", "on")
	_ = s.SetMeta("planner_cursor", "0")
	f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
	t0 := time.Now()
	failed := func(task string) {
		_ = r.Log.Emit("task.failed", &Run{Task: task, Goal: "G-001", Project: "www"}, Event{Data: map[string]any{"reason": "x"}})
	}

	r.PlannerPass(t0)
	if got := f.sent(); len(got) != 1 || !strings.HasPrefix(got[0], "hivemill-wake v1 ") || !strings.Contains(got[0], "queue_low:www=0/3") ||
		!strings.Contains(got[0], "not_dispatched:www") || !strings.Contains(got[0], "items=1:") {
		t.Fatalf("first wake: %q", got)
	}
	// the levels clear (a refilled queue, a dispatched project): their items close "cleared"
	_ = s.SetCfg("projects", "www")
	addReady(t, s, 3)
	r.PlannerPass(t0.Add(time.Minute))
	if rows, _ := s.PlannerWorkOpenRows(); len(rows) != 0 || len(f.sent()) != 1 {
		t.Fatalf("after clearing: open %v, wakes %d", rows, len(f.sent()))
	}
	failed("T-0001")
	r.PlannerPass(t0.Add(2 * time.Minute))
	if n := len(f.sent()); n != 1 {
		t.Fatalf("a wake inside the 10-minute gap (%d wakes)", n)
	}
	if p := s.Meta("planner_pending"); !strings.Contains(p, "debounce") || !strings.Contains(p, "task.failed:T-0001") {
		t.Errorf("pending: %q", p)
	}
	failed("T-0002")
	_ = r.Log.Emit("run.held", &Run{Task: "T-0003"}, Event{})
	_ = r.Log.Emit("pr.merged", &Run{Task: "T-0004"}, Event{}) // not a wake reason
	r.PlannerPass(t0.Add(11 * time.Minute))
	got := f.sent()
	if len(got) != 2 || !strings.Contains(got[1], "task.failed:T-0001,T-0002") || !strings.Contains(got[1], "run.held:T-0003") ||
		strings.Contains(got[1], "T-0004") {
		t.Fatalf("coalesced wake: %q", got)
	}
	r.PlannerPass(t0.Add(12 * time.Minute))
	if n := len(f.sent()); n != 2 {
		t.Fatalf("a delivered item woke the planner again inside planner_ack_s (%d wakes)", n)
	}
	ackAll(t, s, t0.Add(13*time.Minute))
	r.PlannerPass(t0.Add(30 * time.Minute))
	if n := len(f.sent()); n != 2 {
		t.Fatalf("nothing is open but the planner was woken (%d wakes)", n)
	}
	// busy: the item waits until the planner is idle
	failed("T-0005")
	f.terms[0].Status = "working"
	r.PlannerPass(t0.Add(45 * time.Minute))
	if n := len(f.sent()); n != 2 {
		t.Fatalf("a working planner was interrupted")
	}
	f.terms[0].Status = "idle"
	r.PlannerPass(t0.Add(46 * time.Minute))
	if got := f.sent(); len(got) != 3 || !strings.Contains(got[2], "task.failed:T-0005") || strings.Contains(got[2], "T-0001") {
		t.Fatalf("wake after busy: %q", got)
	}
	// planner off: no wake at all
	_ = s.SetMeta("planner", "off")
	failed("T-0006")
	r.PlannerPass(t0.Add(90 * time.Minute))
	if n := len(f.sent()); n != 3 {
		t.Fatalf("planner off but woken")
	}
}

func openRowsOf(t *testing.T, s *SQLite) []PlannerWork {
	t.Helper()
	rows, err := s.PlannerWorkOpenRows()
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func TestPlannerWorkIdempotent(t *testing.T) {
	_, s, _ := plannerRunner(t, "")
	now := time.Now()
	id1, created1, err := s.PlannerWorkOpen("task.failed", "T-0001", now)
	id2, created2, err2 := s.PlannerWorkOpen("task.failed", "T-0001", now.Add(time.Minute))
	if err != nil || err2 != nil || id1 != id2 || !created1 || created2 {
		t.Fatalf("ids %d %d created %v %v errs %v %v", id1, id2, created1, created2, err, err2)
	}
	if rows := openRowsOf(t, s); len(rows) != 1 {
		t.Fatalf("open rows: %+v", rows)
	}
	// the same event in the log twice, over two passes, is still one row
	r, s2, f := plannerRunner(t, "")
	_ = s2.SetMeta("planner", "on")
	f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
	_ = s2.SetCfg("projects", "www")
	addReady(t, s2, 3)
	for i := 0; i < 2; i++ {
		_ = r.Log.Emit("task.failed", &Run{Task: "T-0009", Goal: "G-001", Project: "www"}, Event{})
		r.PlannerPass(now.Add(time.Duration(i) * time.Minute))
	}
	if rows := openRowsOf(t, s2); len(rows) != 1 || rows[0].Kind != "task.failed" || rows[0].Ref != "T-0009" {
		t.Fatalf("open rows: %+v", rows)
	}
}

func TestPlannerWorkRedelivers(t *testing.T) {
	r, s, f := plannerRunner(t, "")
	_ = s.SetMeta("planner", "on")
	_ = s.SetCfg("projects", "www")
	addReady(t, s, 3)
	f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
	t0 := time.Now().Truncate(time.Second)
	_ = r.Log.Emit("run.held", &Run{Task: "T-0003"}, Event{})
	r.PlannerPass(t0)
	if len(f.sent()) != 1 {
		t.Fatalf("first delivery: %q", f.sent())
	}
	r.PlannerPass(t0.Add(900 * time.Second))
	if len(f.sent()) != 1 {
		t.Fatalf("redelivered at exactly planner_ack_s: %q", f.sent())
	}
	r.PlannerPass(t0.Add(901 * time.Second))
	got := f.sent()
	rows := openRowsOf(t, s)
	if len(got) != 2 || !strings.Contains(got[1], "run.held:T-0003") || len(rows) != 1 || rows[0].Attempts != 2 {
		t.Fatalf("redelivery: %q rows %+v", got, rows)
	}
}

func TestPlannerWorkLevelReopens(t *testing.T) {
	r, s, f := plannerRunner(t, "")
	_ = s.SetMeta("planner", "on")
	_ = s.SetCfg("projects", "www")
	f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
	t0 := time.Now()
	r.PlannerPass(t0)
	rows := openRowsOf(t, s)
	if len(rows) != 1 || rows[0].Kind != "queue_low" || rows[0].Ref != "www" {
		t.Fatalf("rows: %+v", rows)
	}
	first := rows[0].ID
	if err := s.PlannerWorkDone([]int64{first}, t0); err != nil {
		t.Fatal(err)
	}
	if err := s.PlannerWorkDone([]int64{first}, t0); err == nil {
		t.Error("a closed id was accepted twice")
	}
	if err := s.PlannerWorkDone([]int64{999}, t0); err == nil {
		t.Error("an unknown id was accepted")
	}
	r.PlannerPass(t0.Add(time.Minute)) // still low: a new row
	rows = openRowsOf(t, s)
	if len(rows) != 1 || rows[0].ID == first {
		t.Fatalf("not reopened: %+v", rows)
	}
	addReady(t, s, 3)
	r.PlannerPass(t0.Add(2 * time.Minute))
	var reason string
	_ = s.db.QueryRow(`SELECT close_reason FROM planner_work WHERE id=?`, rows[0].ID).Scan(&reason)
	if len(openRowsOf(t, s)) != 0 || reason != "cleared" {
		t.Fatalf("cleared: open %v reason %q", openRowsOf(t, s), reason)
	}
}

func TestPlannerWorkEscalates(t *testing.T) {
	setup := func() (*Runner, *SQLite, *fakeHC, time.Time) {
		r, s, f := plannerRunner(t, "")
		_ = s.SetMeta("planner", "on")
		_ = s.SetCfg("projects", "www")
		addReady(t, s, 3)
		_ = s.SetMeta("planner_sid", "11111111-2222-4333-8444-555555555555")
		f.terms = []Terminal{{Name: PlannerName, State: "live", Agent: "claude-code", Status: "idle"}}
		return r, s, f, time.Now().Truncate(time.Second)
	}
	restarted := func(r *Runner) string {
		b, _ := os.ReadFile(r.Log.Path)
		for _, l := range strings.Split(string(b), "\n") {
			if strings.Contains(l, `"planner.restarted"`) {
				return l
			}
		}
		return ""
	}
	// delivered three times without a done: restart
	r, s, f, t0 := setup()
	_ = r.Log.Emit("run.held", &Run{Task: "T-0003"}, Event{})
	_ = s.SetCfg("planner_wake_gap_s", "0")
	for i := 0; i < 3; i++ {
		r.PlannerPass(t0.Add(time.Duration(i) * 901 * time.Second))
	}
	if rows := openRowsOf(t, s); len(rows) != 1 || rows[0].Attempts != 3 || f.count("kill") != 0 {
		t.Fatalf("before: rows %+v kills %d", rows, f.count("kill"))
	}
	r.PlannerPass(t0.Add(3 * 901 * time.Second))
	if f.count("kill") != 1 || !strings.Contains(restarted(r), "delivered 3 times") {
		t.Fatalf("attempts>=3: kills %d event %q", f.count("kill"), restarted(r))
	}
	// busy longer than planner_busy_max_s with a due row: restart
	r, s, f, t0 = setup()
	_ = r.Log.Emit("run.held", &Run{Task: "T-0003"}, Event{})
	f.terms[0].Status = "working"
	r.PlannerPass(t0)
	r.PlannerPass(t0.Add(1800 * time.Second))
	if f.count("kill") != 0 {
		t.Fatalf("restarted at exactly planner_busy_max_s")
	}
	r.PlannerPass(t0.Add(1801 * time.Second))
	if f.count("kill") != 1 || !strings.Contains(restarted(r), "busy for") {
		t.Fatalf("busy: kills %d event %q", f.count("kill"), restarted(r))
	}
	// past the restart limit: one owner alert per row
	r, s, f, t0 = setup()
	_ = s.SetMeta("planner_lost", t0.Format(time.RFC3339))
	_ = r.Log.Emit("run.held", &Run{Task: "T-0003"}, Event{})
	f.terms[0].Status = "working"
	r.PlannerPass(t0)
	r.PlannerPass(t0.Add(1801 * time.Second))
	r.PlannerPass(t0.Add(1900 * time.Second))
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-planner' AND closed IS NULL`).Scan(&n)
	if n != 1 || f.count("kill") != 0 {
		t.Fatalf("alerts %d kills %d", n, f.count("kill"))
	}
}

// A dead planner is resumed with its last session id, at most resume_max times in 24 h; then the owner is alerted once.
func TestPlannerPassRestartsADeadPlannerThenAlerts(t *testing.T) {
	r, s, f := plannerRunner(t, "")
	_ = s.SetMeta("planner", "on")
	_ = s.SetMeta("planner_sid", "11111111-2222-4333-8444-555555555555")
	t0 := time.Now()
	for i := 0; i < 3; i++ {
		r.PlannerPass(t0.Add(time.Duration(i) * time.Minute))
	}
	if n := f.count("run"); n != 3 {
		t.Fatalf("restarts: %d, want 3", n)
	}
	for _, c := range f.calls {
		if c[0] == "run" && !strings.Contains(strings.Join(c, " "), "--name planner --cwd "+filepath.Join(r.Env.StateDir(), "planner")+" --on core-a sh ") {
			t.Errorf("not pinned to the local core: %v", c)
		}
	}
	b, err := os.ReadFile(filepath.Join(r.Env.StateDir(), "spawn", PlannerName+".sh"))
	if err != nil || !strings.Contains(string(b), "--resume 11111111-2222-4333-8444-555555555555") ||
		!strings.Contains(string(b), "claude-opus-5-5") || !strings.Contains(string(b), "--effort medium") ||
		!strings.Contains(string(b), "FACTORY_AGENT=planner") {
		t.Fatalf("spawn script: %s %v", b, err)
	}
	if role := s.AgentRole(PlannerName); role != "planner" {
		t.Errorf("agents row role %q", role)
	}
	r.PlannerPass(t0.Add(4 * time.Minute))
	r.PlannerPass(t0.Add(5 * time.Minute))
	if n := f.count("run"); n != 3 {
		t.Fatalf("restarted past the cap: %d", n)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-planner' AND closed IS NULL`).Scan(&n)
	if n != 1 || s.Meta("planner_lost") == "" {
		t.Fatalf("lost alert rows %d, planner_lost %q", n, s.Meta("planner_lost"))
	}
	// a fresh cap a day later after `planner start`
	if err := r.PlannerStart(t0.Add(25 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if s.Meta("planner_lost") != "" || f.count("run") != 4 {
		t.Errorf("planner start did not reset: lost %q runs %d", s.Meta("planner_lost"), f.count("run"))
	}
}

func TestPlannerRefusesFable(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	if err := s.SetCfg("planner_model", "claude-fable-5"); err == nil {
		t.Fatal("planner_model accepted a Fable model")
	}
	_, _ = s.db.Exec(`INSERT INTO hm_config(key,value) VALUES('planner_model','claude-fable-5')`)
	if err := r.PlannerStart(time.Now()); err == nil || !strings.Contains(err.Error(), "never Fable") {
		t.Fatalf("start with a Fable model: %v", err)
	}
}

// The ready queue counts what a pass would really start: a task that ran once (failed) is not ready.
func TestPlannerLevelsUseTheDispatchRule(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	_ = s.SetCfg("projects", "www")
	addReady(t, s, 3)
	if lv, _ := r.PlannerLevels(); len(lv) != 0 {
		t.Fatalf("3 ready: %v", lv)
	}
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Project: "www", State: Failed})
	if _, err := s.AddTask("G-001", "zz-test unlinted", "spec", "1. works", "todo", "", 1); err != nil {
		t.Fatal(err)
	}
	if lv, _ := r.PlannerLevels(); strings.Join(lv, " ") != "queue_low:www=2/3" {
		t.Fatalf("levels: %v", lv)
	}
	for _, id := range []string{"T-0001", "T-0002", "T-0003"} {
		_ = s.UpdateTask(id, map[string]any{"status": "done"})
	}
	_ = s.UpdateTask("T-0004", map[string]any{"status": "cancelled"})
	if lv, _ := r.PlannerLevels(); strings.Join(lv, " ") != "goal_done:G-001 queue_low:www=0/3" {
		t.Fatalf("levels: %v", lv)
	}
}

// Starvation: an active goal, a free slot and no dispatch for idle_alert_h raise one rate-limited alert with the cause.
func TestStarvationAlert(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	now := time.Now()
	_, _ = s.db.Exec(`UPDATE goals SET created=?`, now.Add(-3*time.Hour).Format(time.RFC3339))
	checks := r.OwnerChecks(now)
	if len(checks) != 1 || checks[0].Kind != "hivemill-idle" || !strings.Contains(checks[0].Text, "config projects is empty") ||
		!strings.Contains(checks[0].Text, "0 of 2 run slots busy") || !strings.Contains(checks[0].Text, "the planner is off") {
		t.Fatalf("checks: %+v", checks)
	}
	if sent, err := r.RaiseOwnerAlerts(now, checks); err != nil || strings.Join(sent, ",") != "hivemill-idle" {
		t.Fatalf("raise: %v %v", sent, err)
	}
	if sent, _ := r.RaiseOwnerAlerts(now.Add(10*time.Minute), r.OwnerChecks(now.Add(10*time.Minute))); len(sent) != 0 {
		t.Fatalf("raised again inside alert_every_s: %v", sent)
	}
	openRows := func(kind string) int {
		var n int
		_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind=? AND closed IS NULL`, kind).Scan(&n)
		return n
	}
	if sent, _ := r.RaiseOwnerAlerts(now.Add(2*time.Hour), r.OwnerChecks(now.Add(2*time.Hour))); len(sent) != 1 || openRows("hivemill-idle") != 1 {
		t.Fatalf("after alert_every_s: sent %v, open rows %d", sent, openRows("hivemill-idle"))
	}
	// the goal's project is configured but has nothing ready
	_ = s.SetCfg("projects", "www")
	if c := r.OwnerChecks(now); len(c) != 1 || !strings.Contains(c[0].Text, "0 lint-passing ready tasks") {
		t.Fatalf("no ready tasks: %+v", c)
	}
	// a recent dispatch: not idle, and the open alert is closed
	_ = s.PutRun(&Run{Task: "T-0099", Project: "www", State: Merged, Dispatched: now.Add(-30 * time.Minute).Format(time.RFC3339)})
	if c := r.OwnerChecks(now); len(c) != 0 {
		t.Fatalf("recent dispatch: %+v", c)
	}
	_, _ = r.RaiseOwnerAlerts(now, nil)
	if openRows("hivemill-idle") != 0 {
		t.Errorf("cleared alert stays open")
	}
	// every slot busy for hours: busy, not starved
	old := now.Add(-5 * time.Hour).Format(time.RFC3339)
	_ = s.PutRun(&Run{Task: "T-0099", Project: "www", State: Merged, Dispatched: old})
	_ = s.PutRun(&Run{Task: "T-0097", Project: "www", State: Working, Dispatched: old})
	_ = s.PutRun(&Run{Task: "T-0098", Project: "www", State: Judging, Dispatched: old})
	if c := r.OwnerChecks(now); len(c) != 0 {
		t.Fatalf("all slots busy: %+v", c)
	}
	// drain is the owner's own stop
	_ = s.PutRun(&Run{Task: "T-0098", Project: "www", State: Merged, Dispatched: old})
	_ = r.SetDrain(true)
	if c := r.OwnerChecks(now); len(c) != 0 {
		t.Fatalf("drain on: %+v", c)
	}
}

// The owner queue: questions older than ask_alert_h go out in one digest row, raised again only when the list changes;
// an answer wakes the planner.
func TestAskQueue(t *testing.T) {
	r, s, _ := plannerRunner(t, PlannerName)
	addReady(t, s, 1)
	now := time.Now()
	s.Now = func() time.Time { return now.Add(-2 * time.Hour) }
	id1, err := r.AskAdd(Ask{Task: "T-0001", Question: "Ship the dark mode toggle in this goal, or later?"})
	if err != nil || id1 != 1 {
		t.Fatalf("add: %d %v", id1, err)
	}
	if a, _ := s.Ask(id1); a.Goal != "G-001" || a.Project != "www" || a.AskedBy != PlannerName || a.Status != "open" {
		t.Errorf("ask: %+v", a)
	}
	if _, err := r.AskAdd(Ask{Question: "  "}); err == nil {
		t.Error("empty question accepted")
	}
	s.Now = time.Now
	if _, err := r.AskAdd(Ask{Project: "www", Question: "fresh question"}); err != nil {
		t.Fatal(err)
	}
	checks := r.OwnerChecks(now)
	var asks *OwnerAlert
	for i := range checks {
		if checks[i].Kind == "hivemill-asks" {
			asks = &checks[i]
		}
	}
	if asks == nil || !strings.Contains(asks.Ref, "1 owner question(s) waiting: #1 T-0001: Ship the dark mode") || strings.Contains(asks.Ref, "fresh") {
		t.Fatalf("asks digest: %+v", checks)
	}
	if sent, _ := r.RaiseOwnerAlerts(now, checks); !contains(sent, "hivemill-asks") {
		t.Fatalf("not sent: %v", sent)
	}
	if sent, _ := r.RaiseOwnerAlerts(now.Add(time.Minute), r.OwnerChecks(now.Add(time.Minute))); contains(sent, "hivemill-asks") {
		t.Fatalf("same list raised twice: %v", sent)
	}
	later := now.Add(90 * time.Minute) // the fresh question is now over an hour old: the list changed
	if sent, _ := r.RaiseOwnerAlerts(later, r.OwnerChecks(later)); !contains(sent, "hivemill-asks") {
		t.Fatalf("changed list not raised: %v", sent)
	}
	var open int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-asks' AND closed IS NULL`).Scan(&open)
	if open != 1 {
		t.Errorf("open digest rows: %d, want 1", open)
	}
	// the owner answers: an ask.answered event, which is a wake reason
	r.Env.Actor = ""
	if err := r.AskClose(id1, true, "later"); err != nil {
		t.Fatal(err)
	}
	if err := r.AskClose(id1, true, "again"); err == nil {
		t.Error("answered twice")
	}
	if evs, _ := r.wakeEvents(); !contains(evs, "ask.answered:#1") {
		t.Errorf("wake events: %v", evs)
	}
	if err := r.AskClose(2, false, "moot"); err != nil {
		t.Fatal(err)
	}
	if left, _ := s.Asks("open"); len(left) != 0 {
		t.Errorf("open: %+v", left)
	}
	_, _ = r.RaiseOwnerAlerts(later, r.OwnerChecks(later))
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-asks' AND closed IS NULL`).Scan(&open)
	if open != 0 {
		t.Errorf("answered digest row still open")
	}
}

// A held run is never silent: it gets one owner question, withdrawn once the owner merged the PR.
func TestHeldRunFilesOneAsk(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	addReady(t, s, 1)
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Project: "www", State: Held, Round: 2, MergeHold: "judge HOLD: unasked retry change",
		PRURL: "https://github.com/zz-test/www/pull/7"})
	r.PlannerPass(time.Now())
	r.PlannerPass(time.Now())
	asks, _ := s.Asks("open")
	if len(asks) != 1 || asks[0].Ref != "held:T-0001:2" || asks[0].AskedBy != "runner" || !strings.Contains(asks[0].Question, "judge HOLD") ||
		!strings.Contains(asks[0].Question, "/pull/7") {
		t.Fatalf("asks: %+v", asks)
	}
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Project: "www", State: Merged, Round: 2})
	r.PlannerPass(time.Now())
	if asks, _ := s.Asks("open"); len(asks) != 0 {
		t.Fatalf("still open after the merge: %+v", asks)
	}
}

func TestPlannerBash(t *testing.T) {
	for _, c := range []struct {
		cmd string
		ok  bool
	}{
		{"hivemill task list --status todo", true},
		{"~/.local/bin/hivemill goal show G-001", true},
		{"hivemill run status", true},
		{"hivemill report --json | jq '.rows[0]'", true},
		{"hivemill ask add --task T-0001 'Merge now; or wait?'", true},
		{"hivemill task add --goal G-001 --title 'x' --spec 'line one\nline two' --acceptance-file - <<'EOF'\n1. a - cmd:true => exit 0\nEOF", true},
		{"git -C /src/www log --oneline -20", true},
		{"gh pr list --repo zz-test/www --state merged --limit 20 | head -5", true},
		{"hivemill run", false},
		{"hivemill run T-0001", false},
		{"hivemill run stop T-0001", false},
		{"hivemill config projects www", false},
		{"hivemill ask answer 1 yes", false},
		{"hivemill merge T-0001", false},
		{"hivemill planner start", false},
		{"hivemill task list; rm -rf /", false},
		{"hivemill task list && git push", false},
		{"hivemill task list > /tmp/x", false},
		{"hivemill task add --title \"$(id)\"", false},
		{"hivemill task add --title `id`", false},
		{"hivemill task list || true", false},
		{"hivemill task list\nhivemill run", false},
		{"hivemill task add --acceptance-file - <<EOF\n1. $(id)\nEOF", false},
		{"git push origin HEAD:main", false},
		{"git -c core.pager=sh log", false},
		{"git diff --output=/tmp/x", false},
		{"git checkout main", false},
		{"gh pr merge 7", false},
		{"gh api repos/x/y -X DELETE", false},
		{"gh pr list | sh", false},
		{"gh pr list | sort -o /tmp/x", false},
		{"GH_TOKEN=x gh pr list", false},
		{"sqlite3 db/factory.db", false},
		{"cat roles/hivemill/planner.md", false},
		{"hivemill task list | head -n 5", true},
		{"hivemill task list | head -5 | wc -l", true},
		{"hivemill report --json | jq --arg t T-1 '.rows[] | select(.task==$t)'", true},
		{"gh pr list | grep -c -e open", true},
		{"hivemill task list | head ~/.ssh/id_ed25519", false},
		{"hivemill task list | grep x ~/.config/gh/hosts.yml", false},
		{"hivemill task list | jq . ~/.claude.json", false},
		{"hivemill task list | jq --rawfile k /etc/passwd .", false},
		{"hivemill task list | grep -f /etc/passwd", false},
		{"hivemill task list | grep -r token /", false},
		{"hivemill task list | grep -e x secrets.txt", false},
		{"hivemill task list | tail -f", false},
		{"git diff --no-index /dev/null /home/user/.ssh/id_ed25519", false},
		{"git grep --no-index token", false},
	} {
		why := PlannerBash(c.cmd)
		if (why == "") != c.ok {
			t.Errorf("%q: allowed=%v (%s), want %v", c.cmd, why == "", why, c.ok)
		}
	}
	home := "/home/user"
	for _, c := range []struct {
		in string
		ok bool
	}{
		{`{"tool_name":"Edit","tool_input":{"file_path":"/src/www/a.go"}}`, false},
		{`{"tool_name":"Write","tool_input":{"file_path":"/tmp/a"}}`, false},
		{`{"tool_name":"Read","tool_input":{"file_path":"/home/user/.ssh/id_ed25519"}}`, false},
		{`{"tool_name":"Grep","tool_input":{"pattern":"x","path":"~/.config"}}`, false},
		{`{"tool_name":"Read","tool_input":{"file_path":"/home/user/src/www/README.md"}}`, true},
		{`{"tool_name":"Glob","tool_input":{"pattern":"**/*.go"}}`, true},
		{`{"tool_name":"Read","cwd":"/home/user/hm/state/hivemill/planner","tool_input":{"file_path":"../../../../.ssh/id_ed25519"}}`, false},
		{`{"tool_name":"Read","cwd":"/home/user/hm/state/hivemill/planner","tool_input":{"file_path":"../../../roles/hivemill/planner.md"}}`, true},
		{`{"tool_name":"Grep","tool_input":{"pattern":"x","path":"relative/dir"}}`, false},
		{`{"tool_name":"Bash","tool_input":{"command":"hivemill task list"}}`, true},
		{`not json`, false},
	} {
		why := PlannerHook(strings.NewReader(c.in), home)
		if (why == "") != c.ok {
			t.Errorf("%s: allowed=%v (%s), want %v", c.in, why == "", why, c.ok)
		}
	}
}

func TestDigest(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	addReady(t, s, 3)
	_ = s.UpdateTask("T-0001", map[string]any{"status": "done"})
	day := time.Date(2026, 11, 24, 0, 0, 0, 0, time.Now().Location())
	write := func(ts time.Time, kind, task string, data map[string]any) {
		b, _ := json.Marshal(Event{TS: ts.Format(time.RFC3339), Event: kind, TaskID: task, GoalID: "G-001", Project: "www", Harness: Harness, Data: data})
		_ = os.MkdirAll(filepath.Dir(r.Log.Path), 0o700)
		fh, _ := os.OpenFile(r.Log.Path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		fh.Write(append(b, '\n'))
		fh.Close()
	}
	write(day.Add(9*time.Hour), "task.dispatched", "T-0001", nil)
	write(day.Add(11*time.Hour), "pr.merged", "T-0001", map[string]any{"pr": "https://github.com/zz-test/www/pull/9"})
	write(day.Add(13*time.Hour), "task.failed", "T-0002", map[string]any{"reason": "judge REJECT x2: scope"})
	write(day.Add(-time.Hour), "pr.merged", "T-0090", nil)     // the day before
	write(day.Add(25*time.Hour), "task.failed", "T-0091", nil) // the day after
	_ = s.PutRun(&Run{Task: "T-0003", Goal: "G-001", Project: "www", State: Held, MergeHold: "tier T3: the owner merges"})
	_, _ = r.AskAdd(Ask{Project: "www", Question: "zz-test question"})
	d, err := r.BuildDigest(day.Add(15 * time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if d.Date != "2026-11-24" || len(d.Merges) != 1 || d.Merges[0].Task != "T-0001" || len(d.Failed) != 1 || d.Failed[0].Task != "T-0002" ||
		len(d.Held) != 1 || len(d.Asks) != 1 || d.Dispatched != 1 || len(d.Goals) != 1 {
		t.Fatalf("digest: %+v", d)
	}
	if g := d.Goals[0]; g.Done != 1 || g.Total != 3 || g.Ready != 1 {
		t.Errorf("goal progress: %+v", g)
	}
	md := d.Markdown(time.Now())
	for _, want := range []string{"1 merged, $0.00, 1 failed, 1 held, 1 open questions", "- T-0001 https://github.com/zz-test/www/pull/9",
		"T-0002: judge REJECT x2: scope", "T-0003: tier T3", "zz-test question", "G-001 www: 1/3 done"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown lacks %q:\n%s", want, md)
		}
	}
	p, err := r.SendDigest(d, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "# hivemill digest 2026-11-24") {
		t.Errorf("digest file: %s", b)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed WHERE kind='hivemill-digest' AND link=?`, p).Scan(&n)
	if n != 1 {
		t.Errorf("digest feed rows: %d", n)
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}
