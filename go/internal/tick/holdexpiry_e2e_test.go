package tick_test

import (
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// End-to-end tests of the hold_expiry unit row (T-1610): the real factory-go runs `factory tick` on a throwaway
// home, its Python children run the other units. They prove the row makes the Go tick call the T-1609 rule, and that
// judge_hold_expiry_min switches it without a deploy. The unit reads the real clock, so the hold-set event is dated
// relative to it; the margins (121 against 120 minutes, 10 against 120) are far wider than one tick.

const holdTask = "T-0001"

// heldEnv is a home with one in_review PR task, reviewer PASS and QA PASS, and a manual judge hold set ageMin ago.
func heldEnv(t *testing.T, ageMin int) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.NeedPython() // every unit but hold_expiry runs in a Python child
	// a reachable pane server: without one the tick ends in the offline branch, before the flow block
	e.HerdrStateful()
	exec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	e.SeedTask(ft.Task{ID: holdTask, Team: "g-001-a"})
	for _, role := range []string{"reviewer", "qa"} {
		exec(t, e, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,'PASS','zz-test','zz-'||?,'t')",
			holdTask, ft.PRURL, "a", role, role)
	}
	setHold(t, e, "wait for PR 703", ageMin)
	return e
}

// setHold writes what `factory task update --judge-hold` writes, with the event dated ageMin minutes ago.
func setHold(t *testing.T, e *ft.Env, reason string, ageMin int) {
	t.Helper()
	ts := time.Now().Add(-time.Duration(ageMin) * time.Minute).Format("2006-01-02T15:04:05-07:00")
	exec(t, e, "UPDATE tasks SET judge_hold_reason=? WHERE id=?", reason, holdTask)
	exec(t, e, "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,'zz-lead','task',?,'judge-hold-set',?)",
		ts, holdTask, reason)
}

func exec(t *testing.T, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func hold(e *ft.Env) string {
	return e.SQL("SELECT COALESCE(judge_hold_reason,'') FROM tasks WHERE id=?", holdTask)[0][0]
}

func expiredEvents(e *ft.Env) int {
	return e.Count("SELECT COUNT(*) FROM events WHERE action='judge-hold-expired' AND entity_id=?", holdTask)
}

func TestTickHoldExpiryClearsAnOldHold(t *testing.T) {
	e := heldEnv(t, 121)
	r := e.OK("tick")
	if h := hold(e); h != "" {
		t.Fatalf("hold %q kept after a tick, want it cleared (121 min old, reviewer+QA PASS)\n%s%s", h, r.Stdout, r.Stderr)
	}
	if n := expiredEvents(e); n != 1 {
		t.Fatalf("%d judge-hold-expired events, want 1\n%s", n, r.Stdout)
	}
	if !strings.Contains(r.Stdout, "judge-hold-expired "+holdTask+":") {
		t.Fatalf("tick stdout has no judge-hold-expired line: %q", r.Stdout)
	}
}

func TestTickHoldExpiryKeepsAYoungHold(t *testing.T) {
	e := heldEnv(t, 10)
	r := e.OK("tick")
	if h := hold(e); h != "wait for PR 703" {
		t.Fatalf("hold = %q after a tick, want it kept (10 min old)\n%s%s", h, r.Stdout, r.Stderr)
	}
	if n := expiredEvents(e); n != 0 {
		t.Fatalf("%d judge-hold-expired events, want 0", n)
	}
}

// The switch works without a deploy: 0 turns the rule off, 120 turns it on again at the next tick.
func TestTickHoldExpiryConfigSwitch(t *testing.T) {
	e := heldEnv(t, 600)
	e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director"})
	e.OKWith(ft.Opts{Agent: "zz-director"}, "config", "judge_hold_expiry_min", "0")
	r := e.OK("tick")
	if h := hold(e); h != "wait for PR 703" || expiredEvents(e) != 0 {
		t.Fatalf("judge_hold_expiry_min=0: hold = %q, %d expired events; want the hold kept\n%s%s", h, expiredEvents(e), r.Stdout, r.Stderr)
	}
	e.OKWith(ft.Opts{Agent: "zz-director"}, "config", "judge_hold_expiry_min", "120")
	r = e.OK("tick")
	if h := hold(e); h != "" || expiredEvents(e) != 1 {
		t.Fatalf("judge_hold_expiry_min=120: hold = %q, %d expired events; want it cleared\n%s%s", h, expiredEvents(e), r.Stdout, r.Stderr)
	}
}

// A fresh home gets the default from DEFAULT_CONFIG (`factory init` and the harness seed write it).
func TestHoldExpiryConfigDefault(t *testing.T) {
	e := ft.New(t)
	if got := e.SQL("SELECT value FROM config WHERE key='judge_hold_expiry_min'"); len(got) != 1 || got[0][0] != "120" {
		t.Fatalf("config judge_hold_expiry_min = %v, want one row 120", got)
	}
}

// With the pane server down the tick ends in the offline branch: the hold is kept (the flow block does not run) and,
// with the timers on, exactly one tick-phases line is printed.
func TestTickHoldExpiryHerdrDown(t *testing.T) {
	e := heldEnv(t, 121)
	e.HerdrFail(true)
	exec(t, e, "UPDATE config SET value='1' WHERE key='tick_phase_timers'")
	r := e.OK("tick")
	if h := hold(e); h != "wait for PR 703" {
		t.Fatalf("hold = %q, want it kept while herdr is unreachable\n%s%s", h, r.Stdout, r.Stderr)
	}
	if n := strings.Count(r.Stdout, "tick-phases "); n != 1 {
		t.Fatalf("%d tick-phases lines, want 1:\n%s", n, r.Stdout)
	}
	if n := e.Count("SELECT COUNT(*) FROM events WHERE action='tick-phases'"); n != 1 {
		t.Fatalf("%d tick-phases events, want 1", n)
	}
}

// With the timers on, a normal tick laps the unit between gate and mech_merge.
func TestTickHoldExpiryLap(t *testing.T) {
	e := heldEnv(t, 10)
	exec(t, e, "UPDATE config SET value='1' WHERE key='tick_phase_timers'")
	r := e.OK("tick")
	i, j, k := strings.Index(r.Stdout, " gate="), strings.Index(r.Stdout, " hold_expiry="), strings.Index(r.Stdout, " mech_merge=")
	if strings.Count(r.Stdout, "tick-phases ") != 1 || i < 0 || j < i || k < j {
		t.Fatalf("want one tick-phases line with gate, hold_expiry, mech_merge in that order:\n%s%s", r.Stdout, r.Stderr)
	}
}
