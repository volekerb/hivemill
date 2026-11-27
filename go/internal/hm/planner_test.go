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

func TestDecideWake(t *testing.T) {
	t0 := time.Date(2026, 11, 25, 3, 0, 0, 0, time.UTC)
	gap := 10 * time.Minute
	for _, c := range []struct {
		name   string
		in     WakeInput
		wake   bool
		adv    bool
		why    string
		reason string
	}{
		{"first short queue wakes", WakeInput{Now: t0, Levels: []string{"queue_low:www=1/3"}, Idle: true}, true, false, "", "queue_low:www=1/3"},
		{"same levels, no events: nothing changed", WakeInput{Now: t0, Levels: []string{"queue_low:www=1/3"}, LastFingerprint: "queue_low:www=1/3",
			Idle: true}, false, true, "nothing changed", ""},
		{"no reasons at all", WakeInput{Now: t0, LastFingerprint: "queue_low:www=1/3", Idle: true}, false, true, "nothing changed", ""},
		{"an event inside the gap waits", WakeInput{Now: t0, LastWake: t0.Add(-9 * time.Minute), Events: []string{"task.failed:T-1"}, MinGap: gap,
			Idle: true}, false, false, "debounce", "task.failed:T-1"},
		{"an event after the gap wakes", WakeInput{Now: t0, LastWake: t0.Add(-10 * time.Minute), Events: []string{"task.failed:T-1"}, MinGap: gap,
			Idle: true}, true, false, "", "task.failed:T-1"},
		{"a busy planner is not interrupted", WakeInput{Now: t0, Events: []string{"run.held:T-2"}, MinGap: gap}, false, false, "busy", "run.held:T-2"},
		{"changed levels wake", WakeInput{Now: t0, Levels: []string{"queue_low:www=2/3"}, LastFingerprint: "queue_low:www=1/3", Idle: true},
			true, false, "", "queue_low:www=2/3"},
		{"a level that went away is no news", WakeInput{Now: t0, Levels: []string{"not_dispatched:www"},
			LastFingerprint: "not_dispatched:www queue_low:www=1/3", Idle: true}, false, true, "nothing changed", ""},
		{"a manual wake ignores the gap and busy", WakeInput{Now: t0, LastWake: t0, MinGap: gap, Force: true}, true, false, "", "manual"},
	} {
		d := DecideWake(c.in)
		if d.Wake != c.wake || d.Advance != c.adv || !strings.Contains(d.Why, c.why) {
			t.Errorf("%s: got wake=%v advance=%v why=%q", c.name, d.Wake, d.Advance, d.Why)
		}
		if c.reason != "" && !strings.Contains(strings.Join(d.Reasons, " "), c.reason) {
			t.Errorf("%s: reasons %v lack %q", c.name, d.Reasons, c.reason)
		}
	}
	if got := Coalesce([]string{"task.failed:T-1", "run.held:T-3", "task.failed:T-2", "task.failed:T-1", "budget.reached"}, []string{"queue_low:www=0/3"}); strings.Join(got, " ") !=
		"task.failed:T-1,T-2 run.held:T-3 budget.reached queue_low:www=0/3" {
		t.Errorf("coalesce: %v", got)
	}
}

// The daemon's planner pass wakes once, coalesces what happens in the next 10 minutes into one wake, never wakes for an
// unchanged state, and waits while the planner works.
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
		!strings.Contains(got[0], "not_dispatched:www") {
		t.Fatalf("first wake: %q", got)
	}
	r.PlannerPass(t0.Add(time.Minute))
	if n := len(f.sent()); n != 1 {
		t.Fatalf("unchanged state woke the planner again (%d wakes)", n)
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
	r.PlannerPass(t0.Add(30 * time.Minute))
	if n := len(f.sent()); n != 2 {
		t.Fatalf("nothing changed but the planner was woken (%d wakes)", n)
	}
	// busy: the reason waits until the planner is idle
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
	// a refilled queue changes the levels but needs no wake; when it runs low again, that is news
	addReady(t, s, 3)
	r.PlannerPass(t0.Add(60 * time.Minute))
	if n := len(f.sent()); n != 3 {
		t.Fatalf("a full queue woke the planner")
	}
	// planner off: no wake at all
	_ = s.SetMeta("planner", "off")
	failed("T-0006")
	r.PlannerPass(t0.Add(90 * time.Minute))
	if n := len(f.sent()); n != 3 {
		t.Fatalf("planner off but woken")
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
	if lv, _ := r.PlannerLevels(); strings.Join(lv, " ") != "queue_low:www=2/3 unstartable:T-0004" {
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
	if c := r.OwnerChecks(now); len(c) != 1 || !strings.Contains(c[0].Text, "0 startable tasks") {
		t.Fatalf("no ready tasks: %+v", c)
	}
	// a recent dispatch: not idle, and the open alert is closed
	_ = s.PutRun(&Run{Task: "T-0099", Project: "www", State: Merged, Dispatched: now.Add(-10 * time.Minute).Format(time.RFC3339)})
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
		{"hivemill upstream add --title 'x' --spec 's' --acceptance-file - <<'EOF'\n1. a - cmd:true => exit 0\nEOF", true},
		{"hivemill upstream status T-0042", true},
		{"hivemill upstream add --title 'x' --spec 'costs $5' --acceptance 'a' --priority 1", true},
		{"hivemill upstream add --title x --spec \"cost \\$5\" --acceptance a", true},
		{"hivemill upstream add --title x --spec \"$ZZ_TOKEN\" --acceptance a", false},
		{"hivemill upstream add --title $ZZ_TOKEN --spec s --acceptance a", false},
		{"hivemill upstream add --title x --spec ${ZZ_TOKEN} --acceptance a", false},
		{"hivemill upstream add --title x --spec s --acceptance-file - <<'EOF'\n1. a\nEOF\ncurl example.test\nEOF", false},
		{"hivemill task add --acceptance-file - <<-'EOF'\n\tEOF\ncurl example.test\nEOF", false},
		{"hivemill report # <<'EOF'\ncurl example.test\nEOF", false},
		{"hivemill ask add 'see #12'", true},
		{"hivemill report $'\\'' ; curl example.test #'", false},
		{"hivemill upstream", false},
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
		{"gh pr list | sort -rn -k2,2 -t, -u", true},
		{"gh pr list | sort -S1b --compress-program=sh", false},
		{"gh pr list | sort --out=x", false},
		{"hivemill report \\\n# <<'EOF'\necho zz\nEOF", false},
		{"hivemill report $\\\n'\\'' ; echo zz #'", false},
		{"hivemill report \"$\\\n(echo zz)\"", false},
		{"hivemill ask add 'a \\\nb'", true},
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

func TestPlannerWorkIdempotent(t *testing.T) {
	r, s, _ := plannerRunner(t, "runner")
	evs, levels := []string{"task.failed:T-1", "goal.checked:G-001"}, []string{"queue_low:www=0/3"}
	r.enqueuePlannerWork(evs, levels, true)
	r.enqueuePlannerWork(evs, levels, true)
	work, err := s.OpenPlannerWork()
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 3 {
		t.Fatalf("want 3 open rows after the same events twice, got %+v", work)
	}
	if work[2].Kind != "queue_low" || work[2].Ref != "www" {
		t.Fatalf("level row = %+v", work[2])
	}
	if err := r.PlannerDone([]int64{work[0].ID}, "handled"); err != nil {
		t.Fatal(err)
	}
	r.enqueuePlannerWork(evs[:1], nil, false) // closed: a new event opens a new row
	if work, _ = s.OpenPlannerWork(); len(work) != 3 {
		t.Fatalf("want 3 open rows after a closed one came back, got %+v", work)
	}
	r.enqueuePlannerWork(nil, nil, true) // the level cleared: its row closes
	if work, _ = s.OpenPlannerWork(); len(work) != 2 {
		t.Fatalf("want the cleared level closed, got %+v", work)
	}
}

func TestPlannerDoneUnknownID(t *testing.T) {
	r, s, _ := plannerRunner(t, "runner")
	r.enqueuePlannerWork([]string{"task.failed:T-1"}, nil, false)
	work, _ := s.OpenPlannerWork()
	if err := r.PlannerDone([]int64{work[0].ID, 999}, ""); err == nil {
		t.Fatal("an unknown id must fail")
	}
	if w, _ := s.OpenPlannerWork(); len(w) != 1 {
		t.Fatal("a failed done must close nothing")
	}
	if err := r.PlannerDone([]int64{work[0].ID}, ""); err != nil {
		t.Fatal(err)
	}
	if err := r.PlannerDone([]int64{work[0].ID}, ""); err == nil {
		t.Fatal("a closed id must fail")
	}
}

func TestPlannerWorkStartableLevelsClear(t *testing.T) {
	r, s, _ := plannerRunner(t, "runner")
	r.enqueuePlannerWork(nil, []string{"unstartable:T-1", "none_startable:todo=1,blocked_by_deps=0,blocked_by_lint=1"}, true)
	r.enqueuePlannerWork(nil, []string{"unstartable:T-1", "none_startable:todo=2,blocked_by_deps=1,blocked_by_lint=1",
		"dead_dep:T-2=T-3(cancelled)"}, true)
	work, err := s.OpenPlannerWork()
	if err != nil {
		t.Fatal(err)
	}
	if len(work) != 3 || work[0].Kind != "unstartable" || work[0].Ref != "T-1" || work[1].Kind != "none_startable" || work[1].Ref != "" ||
		work[2].Kind != "dead_dep" || work[2].Ref != "T-2" {
		t.Fatalf("want one row per level after changed counts, got %+v", work)
	}
	r.enqueuePlannerWork(nil, nil, true)
	var cleared int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM planner_work WHERE close_reason='cleared'`).Scan(&cleared); err != nil {
		t.Fatal(err)
	}
	if work, _ = s.OpenPlannerWork(); len(work) != 0 || cleared != 3 {
		t.Fatalf("want all rows closed as cleared, got %d cleared, open %+v", cleared, work)
	}
}
