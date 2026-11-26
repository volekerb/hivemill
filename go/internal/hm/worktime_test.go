package hm

import (
	"os"
	"strings"
	"testing"
	"time"
)

const longText = "You have worked 180 minutes without a passing gate. Stop any wait loop, finish or report what blocks you."

// longRun is a run in working since `ago` before now, and the number of events of one kind in the runner's log.
func longRun(t *testing.T, now time.Time, ago time.Duration) (*Runner, *SQLite, *Run, *[]string) {
	t.Helper()
	r, s := testRunner(t, "")
	var sent []string
	hc := r.Tools.HC
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "send" {
			sent = append(sent, strings.Join(args, " "))
		}
		return hc(args...)
	}
	run := &Run{Task: "T-0001", Goal: "G-001", Project: "www", Agent: "zz-test-agent", State: Working, Round: 1,
		RoundStart: now.Add(-ago).Format(time.RFC3339)}
	putRuns(t, s, run)
	return r, s, run, &sent
}

func events(t *testing.T, r *Runner, kind string) int {
	t.Helper()
	b, err := os.ReadFile(r.Log.Path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return strings.Count(string(b), `"`+kind+`"`)
}

func TestRunMaxWorkingResumes(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r, _, run, sent := longRun(t, now, 89*time.Minute)
	for _, c := range []struct {
		after          time.Duration // since now
		events, resume int
	}{
		{0, 0, 0},                 // 89 of 180 minutes
		{time.Minute, 1, 0},       // 50%
		{2 * time.Minute, 1, 0},   // said once
		{91 * time.Minute, 2, 1},  // 100%: the resume
		{100 * time.Minute, 2, 1}, // one resume only
		{120 * time.Minute, 2, 1}, // 29 minutes after it
	} {
		r.workingLimit(run, now.Add(c.after))
		if n := events(t, r, "run.long_working"); n != c.events || len(*sent) != c.resume || run.State != Working {
			t.Fatalf("after %s: %d events, sends %v, state %s", c.after, n, *sent, run.State)
		}
	}
	if (*sent)[0] != "send zz-test-agent "+longText {
		t.Fatalf("sent: %q", (*sent)[0])
	}
	if run.Round != 1 || run.LongPct != 100 || run.LongResumed == "" {
		t.Fatalf("run: %+v", run)
	}

	for _, off := range []string{"0", "off"} {
		r, s, run, sent := longRun(t, now, 1000*time.Minute)
		_ = s.SetCfg("run_max_working_min", off)
		if r.workingLimit(run, now) || len(*sent) != 0 || events(t, r, "run.long_working") != 0 {
			t.Fatalf("%s: sends %v", off, *sent)
		}
	}
}

func TestRunMaxWorkingFails(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r, s, run, _ := longRun(t, now, 180*time.Minute)
	if !r.workingLimit(run, now) || run.State != Working {
		t.Fatalf("at the limit: %s", run.State)
	}
	if r.workingLimit(run, now.Add(29*time.Minute)) || run.State != Working {
		t.Fatalf("inside the grace: %s", run.State)
	}
	if !r.workingLimit(run, now.Add(30*time.Minute)) || run.State != Failed {
		t.Fatalf("after the grace: %s", run.State)
	}
	if !strings.Contains(run.FailReason, "timed out: 210 minutes in working") {
		t.Fatalf("reason: %q", run.FailReason)
	}
	if st, _ := s.Task("T-0001"); st.ID != "" && st.Status != "blocked" {
		t.Fatalf("task status %s", st.Status)
	}
}

func TestDeadDependencyMakesPlannerItem(t *testing.T) {
	r, s, _ := plannerRunner(t, "")
	_ = s.SetCfg("projects", "www")
	addReady(t, s, 6) // T-0001..T-0006
	dead := func() string {
		lv, err := r.PlannerLevels()
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, l := range lv {
			if strings.HasPrefix(l, "dead_dep:") {
				out = append(out, l)
			}
		}
		return strings.Join(out, " ")
	}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.UpdateTask("T-0003", map[string]any{"depends_on": `["T-0001","T-0002"]`}))
	must(s.UpdateTask("T-0006", map[string]any{"depends_on": `["T-0004","T-0005"]`}))
	if got := dead(); got != "" {
		t.Fatalf("live dependencies: %s", got)
	}
	must(s.UpdateTask("T-0001", map[string]any{"status": "cancelled"}))
	must(s.PutRun(&Run{Task: "T-0004", Goal: "G-001", Project: "www", State: Failed}))
	must(s.UpdateTask("T-0004", map[string]any{"status": "blocked"}))
	must(s.UpdateTask("T-0005", map[string]any{"status": "done"}))
	// one item per waiting task, the same on the next pass
	want := "dead_dep:T-0003=T-0001(cancelled) dead_dep:T-0006=T-0004(failed)"
	for range 2 {
		if got := dead(); got != want {
			t.Fatalf("items: %q, want %q", got, want)
		}
	}
	// a failed task set back to todo is being retried, and a run held for the owner's merge can still merge
	must(s.UpdateTask("T-0004", map[string]any{"status": "todo"}))
	if got := dead(); got != "dead_dep:T-0003=T-0001(cancelled)" {
		t.Fatalf("retried dependency: %s", got)
	}
	must(s.UpdateTask("T-0004", map[string]any{"status": "blocked"}))
	must(s.PutRun(&Run{Task: "T-0004", Goal: "G-001", Project: "www", State: Held}))
	if got := dead(); got != "dead_dep:T-0003=T-0001(cancelled)" {
		t.Fatalf("held dependency: %s", got)
	}
	// re-pointed, or no longer waiting: no item
	must(s.UpdateTask("T-0003", map[string]any{"depends_on": `["T-0002"]`}))
	must(s.UpdateTask("T-0006", map[string]any{"status": "cancelled"}))
	if got := dead(); got != "" {
		t.Fatalf("after the fix: %s", got)
	}
}

func TestDeadDependencyListedAsBlocked(t *testing.T) {
	r, s, _ := queueFixture(t) // T-0003 waits on T-0001 and the unknown T-0099
	line := func() string {
		t.Helper()
		for _, l := range listQueue(t, r, true) {
			if strings.HasPrefix(l, "T-0003 ") {
				return l
			}
		}
		t.Fatal("T-0003 is not listed as blocked")
		return ""
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.UpdateTask("T-0001", map[string]any{"status": "cancelled"}))
	if got, want := line(), "T-0003 G-001 zz-test task: deps: T-0001 cancelled, T-0099 (missing)"; got != want {
		t.Fatalf("cancelled: %q, want %q", got, want)
	}
	// T-0004 has a failed run
	must(s.UpdateTask("T-0004", map[string]any{"status": "blocked"}))
	must(s.UpdateTask("T-0003", map[string]any{"depends_on": `["T-0004"]`}))
	if got, want := line(), "T-0003 G-001 zz-test task: deps: T-0004 failed"; got != want {
		t.Fatalf("failed: %q, want %q", got, want)
	}
}

func TestTaskPromptNoForeignWaitLoop(t *testing.T) {
	for _, p := range []string{"assets/task.md", "../../../roles/hivemill/task.md"} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"Never wait in a loop on processes you did not start (`pgrep`, `ps`)",
			"Wait on your own background job, or run the command in the foreground with a timeout."} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: no %q", p, want)
			}
		}
	}
}

// A late first check, a failed send and a relaunched session each leave the session a full grace.
func TestRunMaxWorkingKeepsTheGrace(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r, _, run, sent := longRun(t, now, 400*time.Minute)
	up := r.Tools.HC
	down := func(...string) (int, string, string) { return 1, "", "not up" }
	r.Tools.HC = down
	if r.workingLimit(run, now) || r.workingLimit(run, now.Add(29*time.Minute)) || run.State != Working {
		t.Fatalf("failed sends inside the grace: %s", run.State)
	}
	r.Tools.HC = up
	run.IdleSince = now.Format(time.RFC3339)
	if !r.workingLimit(run, now.Add(40*time.Minute)) || run.State != Working || len(*sent) != 1 || run.IdleSince != "" {
		t.Fatalf("send works again: %s %v %q", run.State, *sent, run.IdleSince)
	}
	r.Tools.HC = down
	run.LongResumed, run.LongSendFailed = "", "" // as after a relaunch
	if r.workingLimit(run, now.Add(70*time.Minute)) || run.State != Working {
		t.Fatalf("first failed send: %s", run.State)
	}
	if !r.workingLimit(run, now.Add(100*time.Minute)) || run.State != Failed {
		t.Fatalf("unreachable for the whole grace: %s", run.State)
	}
}

func TestDeadDependencyIsLastInAWake(t *testing.T) {
	d := DecideWake(WakeInput{Now: time.Now(), Idle: true, Levels: []string{"queue_low:www=0/3", "dead_dep:T-2=T-1(cancelled)",
		"goal_done:G-001"}})
	if n := len(d.Reasons); n != 3 || !strings.HasPrefix(d.Reasons[n-1], "dead_dep:") {
		t.Fatalf("reasons: %v", d.Reasons)
	}
}

func TestRunMaxWorkingShowsInFlow(t *testing.T) {
	r, _ := testRunner(t, "")
	now := time.Now().UTC()
	at := func(ago time.Duration) string { return now.Add(-ago).Format(time.RFC3339) }
	long := func(task string, pct int) Event {
		return ev(at(10*time.Minute), "run.long_working", task, "G-1", "www", "", "",
			map[string]any{"pct": pct, "minutes": 90, "limit": 180})
	}
	writeEvents(t, r.Log.Path,
		ev(at(2*time.Hour), "task.dispatched", "T-1", "G-1", "www", "", "", nil), long("T-1", 50),
		ev(at(5*time.Minute), "gate.blocked", "T-1", "G-1", "www", "", "", nil), // still working
		ev(at(2*time.Hour), "task.dispatched", "T-2", "G-1", "www", "", "", nil), long("T-2", 50),
		ev(at(5*time.Minute), "gate.passed", "T-2", "G-1", "www", "", "", nil), // left working
	)
	h, err := r.ObsHandler()
	if err != nil {
		t.Fatal(err)
	}
	flow := flowBanners(t, h)
	if len(flow.Long) != 1 || flow.Long[0].Task != "T-1" || flow.Long[0].LongPct != 50 || flow.Long[0].LimitSec != 10800 ||
		flow.Long[0].WorkingSec < 6000 {
		t.Fatalf("long: %+v", flow.Long)
	}
	if _, page := obsGet(t, h, "/flow"); !strings.Contains(page, "Long in working") || !strings.Contains(page, ">50%</td>") {
		t.Fatalf("page:\n%s", page)
	}
}

// Time a worker is disconnected after the resume does not count against its grace.
func TestRunMaxWorkingOutageRestartsTheGrace(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	r, _, run, _ := longRun(t, now, 180*time.Minute)
	run.Machine = "w1"
	state := "connected"
	up := r.Tools.HC
	r.Tools.HC = func(args ...string) (int, string, string) {
		if args[0] == "ls" {
			return 0, `{"machines": [{"name": "w1", "state": "` + state + `"}], "terminals": []}`, ""
		}
		return up(args...)
	}
	r.workingLimit(run, now)
	state = "disconnected"
	r.workingLimit(run, now.Add(2*time.Hour))
	state = "connected"
	if r.workingLimit(run, now.Add(2*time.Hour+29*time.Minute)) || run.State != Working {
		t.Fatalf("just reconnected: %s", run.State)
	}
	if !r.workingLimit(run, now.Add(2*time.Hour+30*time.Minute)) || run.State != Failed {
		t.Fatalf("a full grace after the outage: %s", run.State)
	}
}
