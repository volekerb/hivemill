package holdexpiry_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorytest"
	"factory/go/internal/holdexpiry"
)

const (
	taskID = "T-0001"
	nowISO = "2026-11-15T12:00:00+01:00"
	lead   = "claude-lead-g-001-a"
)

// fx is one throwaway factory database (the factorytest schema fixture) with a fixed clock.
type fx struct {
	t   *testing.T
	db  *sql.DB
	now time.Time
}

func newFx(t *testing.T) *fx {
	t.Helper()
	e := factorytest.New(t)
	now, err := time.Parse(time.RFC3339, nowISO)
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{t: t, db: e.RawDB(), now: now}
	t.Cleanup(func() { f.db.Close() })
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(factorytest.Task{ID: taskID})
	e.SeedAgent(factorytest.AgentRow{Name: "claude-boss", Role: "boss"})
	e.SeedAgent(factorytest.AgentRow{Name: "claude-director", Role: "director"})
	e.SeedAgent(factorytest.AgentRow{Name: lead, Role: "lead"})
	return f
}

func (f *fx) exec(q string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Exec(q, args...); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
}

func (f *fx) ago(min int) string {
	return f.now.Add(-time.Duration(min) * time.Minute).Format("2006-01-02T15:04:05-07:00")
}

// hold sets the hold the way `factory task update --judge-hold` does: the column and one judge-hold-set event.
func (f *fx) hold(reason, by string, minAgo int) {
	f.t.Helper()
	f.exec("UPDATE tasks SET judge_hold_reason=? WHERE id=?", reason, taskID)
	f.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,'task',?,'judge-hold-set',?)",
		f.ago(minAgo), by, taskID, reason)
}

func (f *fx) verdict(role, v string, minAgo int) {
	f.t.Helper()
	f.exec("INSERT INTO verdicts(task_id,pr_url,role,verdict,created) VALUES(?,?,?,?,?)",
		taskID, factorytest.PRURL, role, v, f.ago(minAgo))
}

func (f *fx) passPass() {
	f.t.Helper()
	f.verdict("reviewer", "PASS", 300)
	f.verdict("qa", "PASS", 290)
}

// message inserts a messages row with a fixed id (the M-id a hold text names).
func (f *fx) message(id int, sender, kind, task, subject, body string) {
	f.t.Helper()
	var tid any
	if task != "" {
		tid = task
	}
	f.exec("INSERT INTO messages(id,sender,recipient,kind,subject,body,task_id,created) VALUES(?,?,?,?,?,?,?,?)",
		id, sender, "claude-director", kind, subject, body, tid, f.ago(400))
}

func (f *fx) run() holdexpiry.Result {
	f.t.Helper()
	res, err := holdexpiry.Run(context.Background(), holdexpiry.Deps{DB: f.db, Now: func() time.Time { return f.now }})
	if err != nil {
		f.t.Fatalf("Run: %v", err)
	}
	return res
}

func (f *fx) str(q string, args ...any) string {
	f.t.Helper()
	var s sql.NullString
	if err := f.db.QueryRow(q, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	if !s.Valid {
		return "<NULL>"
	}
	return s.String
}

func (f *fx) count(q string, args ...any) int {
	f.t.Helper()
	var n int
	if err := f.db.QueryRow(q, args...).Scan(&n); err != nil {
		f.t.Fatalf("%s: %v", q, err)
	}
	return n
}

func (f *fx) holdText() string {
	return f.str("SELECT judge_hold_reason FROM tasks WHERE id=?", taskID)
}

func (f *fx) events(action string) int {
	return f.count("SELECT COUNT(*) FROM events WHERE entity='task' AND entity_id=? AND action=?", taskID, action)
}

func (f *fx) reports() int {
	return f.count("SELECT COUNT(*) FROM messages WHERE sender='tick'")
}

// wantKept asserts the rule did not fire: hold text unchanged, no event and no message written.
func (f *fx) wantKept(reason string) {
	f.t.Helper()
	if got := f.holdText(); got != reason {
		f.t.Errorf("hold text = %q, want it kept as %q", got, reason)
	}
	if n := f.events("judge-hold-clear") + f.events("judge-hold-expired"); n != 0 {
		f.t.Errorf("%d clear/expired events written, want 0", n)
	}
	if n := f.reports(); n != 0 {
		f.t.Errorf("%d tick messages written, want 0", n)
	}
}

// wantExpired asserts the rule fired n times in total on the task, and returns the detail of the last
// judge-hold-expired event.
func (f *fx) wantExpired(n int) map[string]any {
	f.t.Helper()
	if got := f.holdText(); got != "<NULL>" {
		f.t.Errorf("hold text = %q, want NULL", got)
	}
	if got := f.events("judge-hold-clear"); got != n {
		f.t.Errorf("%d judge-hold-clear events, want %d", got, n)
	}
	if got := f.events("judge-hold-expired"); got != n {
		f.t.Fatalf("%d judge-hold-expired events, want %d", got, n)
	}
	if got := f.reports(); got != n {
		f.t.Errorf("%d tick messages, want %d", got, n)
	}
	raw := f.str("SELECT detail FROM events WHERE entity_id=? AND action='judge-hold-expired' ORDER BY id DESC LIMIT 1", taskID)
	var d map[string]any
	if err := json.Unmarshal([]byte(raw), &d); err != nil {
		f.t.Fatalf("judge-hold-expired detail %q is not JSON: %v", raw, err)
	}
	return d
}

func TestExpireFires(t *testing.T) {
	f := newFx(t)
	f.passPass()
	const reason = "hold until PR 703 merges"
	f.hold(reason, lead, 121)

	res := f.run()

	if len(res.Expired) != 1 || res.Expired[0].Task != taskID {
		t.Fatalf("Result.Expired = %+v, want one entry for %s", res.Expired, taskID)
	}
	d := f.wantExpired(1)
	want := map[string]any{"task": taskID, "pr": float64(1), "held_min": float64(121), "set_by": lead,
		"reason": reason, "marker": "none"}
	if len(d) != len(want) {
		t.Errorf("expired detail has keys %v, want exactly %v", d, want)
	}
	for k, v := range want {
		if d[k] != v {
			t.Errorf("expired detail %s = %v, want %v", k, d[k], v)
		}
	}
	clear := f.str("SELECT detail FROM events WHERE entity_id=? AND action='judge-hold-clear'", taskID)
	if clear != "expired after 121 min: "+reason {
		t.Errorf("judge-hold-clear detail = %q", clear)
	}
	if a := f.str("SELECT actor FROM events WHERE entity_id=? AND action='judge-hold-clear'", taskID); a != "tick" {
		t.Errorf("judge-hold-clear actor = %q, want tick", a)
	}
	if ts := f.str("SELECT ts FROM events WHERE entity_id=? AND action='judge-hold-expired'", taskID); ts != nowISO {
		t.Errorf("event ts = %q, want the injected clock %q", ts, nowISO)
	}
	if n := f.count("SELECT COUNT(*) FROM messages WHERE sender='tick' AND recipient=? AND kind='report' AND task_id=? AND goal_id='G-001'",
		lead, taskID); n != 1 {
		t.Errorf("%d reports to the setter %s, want exactly 1", n, lead)
	}
	if s := f.str("SELECT subject FROM messages WHERE sender='tick'"); s != "judge hold expired: "+taskID+" PR #1" {
		t.Errorf("subject = %q", s)
	}
	body := f.str("SELECT body FROM messages WHERE sender='tick'")
	for _, part := range []string{reason, "121 min",
		"a hold on a reviewer+QA PASS PR expires after 2 h unless its text names a boss decision: boss-approved M<id>",
		"To hold again, get a boss decision first"} {
		if !strings.Contains(body, part) {
			t.Errorf("body lacks %q:\n%s", part, body)
		}
	}

	// a second run finds nothing left to do
	f.run()
	f.wantExpired(1)
}

func TestExpireClearDetailClipsReason(t *testing.T) {
	f := newFx(t)
	f.passPass()
	reason := strings.Repeat("é", 250)
	f.hold(reason, lead, 500)
	f.run()
	d := f.wantExpired(1)
	if d["reason"] != reason {
		t.Errorf("expired detail reason is not the full old reason")
	}
	clear := f.str("SELECT detail FROM events WHERE entity_id=? AND action='judge-hold-clear'", taskID)
	if want := "expired after 500 min: " + strings.Repeat("é", 200); clear != want {
		t.Errorf("judge-hold-clear detail = %q, want the first 200 characters", clear)
	}
}

func TestExpireReportGoesToDirectorWhenSetterIsNoAgent(t *testing.T) {
	for _, by := range []string{"owner", "tick", "unknown", ""} {
		t.Run("set by "+by, func(t *testing.T) {
			f := newFx(t)
			f.exec("UPDATE goals SET org='cursor' WHERE id='G-001'")
			f.passPass()
			f.hold("wait", by, 200)
			f.run()
			f.wantExpired(1)
			if to := f.str("SELECT recipient FROM messages WHERE sender='tick'"); to != "cursor-director" {
				t.Errorf("recipient = %q, want the director of the goal's org", to)
			}
		})
	}
}

func TestExpireKept(t *testing.T) {
	const reason = "hold until PR 703 merges"
	cases := []struct {
		name  string
		setup func(f *fx) string // returns the hold text
	}{
		{"hold set 119 min ago", func(f *fx) string { f.passPass(); f.hold(reason, lead, 119); return reason }},
		{"latest qa verdict FAIL", func(f *fx) string {
			f.passPass()
			f.verdict("qa", "FAIL", 280)
			f.hold(reason, lead, 121)
			return reason
		}},
		{"latest reviewer verdict FAIL after a PASS", func(f *fx) string {
			f.passPass()
			f.verdict("reviewer", "FAIL", 280)
			f.hold(reason, lead, 121)
			return reason
		}},
		{"no reviewer verdict", func(f *fx) string { f.verdict("qa", "PASS", 290); f.hold(reason, lead, 121); return reason }},
		{"no qa verdict", func(f *fx) string { f.verdict("reviewer", "PASS", 290); f.hold(reason, lead, 121); return reason }},
		{"task status blocked", func(f *fx) string {
			f.passPass()
			f.hold(reason, lead, 121)
			f.exec("UPDATE tasks SET status='blocked' WHERE id=?", taskID)
			return reason
		}},
		{"task without pr_url", func(f *fx) string {
			f.passPass()
			f.hold(reason, lead, 121)
			f.exec("UPDATE tasks SET pr_url=NULL WHERE id=?", taskID)
			return reason
		}},
		{"config 0 with a hold 600 min old", func(f *fx) string {
			f.passPass()
			f.hold(reason, lead, 600)
			f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('judge_hold_expiry_min','0')")
			return reason
		}},
		{"boss decision with task_id", func(f *fx) string {
			r := "boss-approved M41: release freeze"
			f.passPass()
			f.message(41, "claude-boss", "decision", taskID, "hold", "keep it")
			f.hold(r, lead, 600)
			return r
		}},
		{"owner decision naming the task in the body", func(f *fx) string {
			r := "release freeze, boss-approved M41"
			f.passPass()
			f.message(41, "owner", "decision", "", "hold", "keep "+taskID+" held until the release")
			f.hold(r, lead, 600)
			return r
		}},
		{"one valid marker next to an invalid one", func(f *fx) string {
			r := "boss-approved M40 boss-approved M41"
			f.passPass()
			f.message(41, "claude-boss", "decision", "", "hold "+taskID, "")
			f.hold(r, lead, 600)
			return r
		}},
		{"no judge-hold-set event (age unknown)", func(f *fx) string {
			f.passPass()
			f.exec("UPDATE tasks SET judge_hold_reason=? WHERE id=?", reason, taskID)
			return reason
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			want := c.setup(f)
			if res := f.run(); len(res.Expired) != 0 {
				t.Errorf("Result.Expired = %+v, want none", res.Expired)
			}
			f.wantKept(want)
		})
	}
}

func TestExpireForgedMarker(t *testing.T) {
	cases := []struct {
		name  string
		setup func(f *fx)
	}{
		{"decision from a director", func(f *fx) { f.message(41, "claude-director", "decision", taskID, "hold", "") }},
		{"decision from an unknown sender", func(f *fx) { f.message(41, "nobody", "decision", taskID, "hold", "") }},
		{"kind report from the boss", func(f *fx) { f.message(41, "claude-boss", "report", taskID, "hold", "") }},
		{"decision naming another task", func(f *fx) {
			f.message(41, "claude-boss", "decision", "T-0002", "hold T-0002", "T-00010 and XT-0001 and T-0001-b stay held")
		}},
		{"message id does not exist", func(f *fx) {}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			f.passPass()
			c.setup(f)
			f.hold("boss-approved M41: release freeze", lead, 121)
			f.run()
			if d := f.wantExpired(1); d["marker"] != "invalid:M41" {
				t.Errorf("marker = %v, want invalid:M41", d["marker"])
			}
		})
	}
	t.Run("wrong case is no marker", func(t *testing.T) {
		f := newFx(t)
		f.passPass()
		f.message(41, "claude-boss", "decision", taskID, "hold", "")
		f.hold("Boss-Approved M41", lead, 121)
		f.run()
		if d := f.wantExpired(1); d["marker"] != "none" {
			t.Errorf("marker = %v, want none", d["marker"])
		}
	})
}

func TestExpireResetAfterExpiry(t *testing.T) {
	first := func(t *testing.T) *fx {
		f := newFx(t)
		f.passPass()
		f.hold("wait for PR 703", lead, 121)
		f.run()
		f.wantExpired(1)
		f.now = f.now.Add(5 * time.Minute)
		return f
	}
	t.Run("set again with no marker expires at the next run", func(t *testing.T) {
		f := first(t)
		f.hold("still waiting", "claude-director", 1)
		f.run()
		d := f.wantExpired(2)
		if d["held_min"] != float64(1) || d["set_by"] != "claude-director" || d["reason"] != "still waiting" {
			t.Errorf("second expired detail = %v", d)
		}
		if n := f.count("SELECT COUNT(*) FROM messages WHERE sender='tick' AND recipient='claude-director'"); n != 1 {
			t.Errorf("%d reports to the second setter, want 1", n)
		}
	})
	t.Run("new qa FAIL after the expiry keeps it", func(t *testing.T) {
		f := first(t)
		f.verdict("qa", "FAIL", 2)
		f.hold("still waiting", lead, 1)
		f.run()
		if got := f.holdText(); got != "still waiting" {
			t.Errorf("hold text = %q, want it kept", got)
		}
		if n := f.events("judge-hold-expired"); n != 1 {
			t.Errorf("%d judge-hold-expired events, want only the first", n)
		}
	})
	t.Run("FAIL then PASS after the expiry gives the new hold the full time", func(t *testing.T) {
		f := first(t)
		f.verdict("qa", "FAIL", 3)
		f.verdict("qa", "PASS", 2)
		f.hold("still waiting", lead, 1)
		f.run()
		if got := f.holdText(); got != "still waiting" {
			t.Errorf("hold text = %q, want it kept for the expiry time", got)
		}
		f.now = f.now.Add(120 * time.Minute)
		f.run()
		f.wantExpired(2)
	})
	t.Run("valid boss marker keeps it", func(t *testing.T) {
		f := first(t)
		f.message(40001, "claude-boss", "decision", "", "release freeze", "hold "+taskID+".")
		f.hold("boss-approved M40001: release freeze", lead, 1)
		f.run()
		f.now = f.now.Add(48 * time.Hour)
		f.run()
		if got := f.holdText(); got != "boss-approved M40001: release freeze" {
			t.Errorf("hold text = %q, want it kept without limit", got)
		}
		if n := f.events("judge-hold-expired"); n != 1 {
			t.Errorf("%d judge-hold-expired events, want only the first", n)
		}
	})
}

func TestExpireOneTransaction(t *testing.T) {
	f := newFx(t)
	f.passPass()
	const reason = "hold until PR 703 merges"
	f.hold(reason, lead, 121)
	f.exec(`CREATE TRIGGER zz_fail_report BEFORE INSERT ON messages
		BEGIN SELECT RAISE(ABORT, 'injected message failure'); END`)

	res, err := holdexpiry.Run(context.Background(), holdexpiry.Deps{DB: f.db, Now: func() time.Time { return f.now }})

	if err == nil || !strings.Contains(err.Error(), "injected message failure") || !strings.Contains(err.Error(), taskID) {
		t.Fatalf("Run error = %v, want the injected failure naming the task", err)
	}
	if len(res.Expired) != 0 {
		t.Errorf("Result.Expired = %+v, want none", res.Expired)
	}
	f.wantKept(reason)

	// the failure is not sticky: with the fault gone the next run expires the hold
	f.exec("DROP TRIGGER zz_fail_report")
	f.run()
	f.wantExpired(1)
}

func TestExpireConfigParsing(t *testing.T) {
	cases := []struct {
		value string // "-" = no row
		want  int
	}{{"-", 120}, {"90", 90}, {" 90 ", 90}, {"abc", 120}, {"", 120}, {"-5", 120}, {"1.5", 120},
		{"99999999999999999999", 120}, {"0", 0}}
	for _, c := range cases {
		t.Run("value "+c.value, func(t *testing.T) {
			f := newFx(t)
			if c.value == "-" {
				// the harness seed has the default row since T-1610: a missing row is made here
				f.exec("DELETE FROM config WHERE key='judge_hold_expiry_min'")
			} else {
				f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('judge_hold_expiry_min',?)", c.value)
			}
			got, err := holdexpiry.ExpiryMin(context.Background(), f.db)
			if err != nil || got != c.want {
				t.Fatalf("ExpiryMin = %d, %v; want %d", got, err, c.want)
			}
			if res := f.run(); res.ExpiryMin != c.want {
				t.Errorf("Result.ExpiryMin = %d, want %d", res.ExpiryMin, c.want)
			}
		})
	}
	t.Run("90 is the limit Run applies", func(t *testing.T) {
		f := newFx(t)
		f.exec("INSERT OR REPLACE INTO config(key,value) VALUES('judge_hold_expiry_min','90')")
		f.passPass()
		f.hold("wait", lead, 89)
		f.run()
		f.wantKept("wait")
		f.now = f.now.Add(time.Minute)
		f.run()
		f.wantExpired(1)
		if body := f.str("SELECT body FROM messages WHERE sender='tick'"); !strings.Contains(body, "expires after 90 min unless") {
			t.Errorf("body does not state the configured expiry:\n%s", body)
		}
	})
}

func TestExpireLeavesOtherTasksAlone(t *testing.T) {
	f := newFx(t)
	f.passPass()
	f.hold("wait", lead, 121)
	f.exec(`INSERT INTO tasks(id,goal_id,title,status,pr_url,judge_hold_reason,created,updated)
		VALUES('T-0002','G-001','other','in_review','https://github.com/zz-test/repo/pull/2','no verdicts yet','t','t')`)
	f.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,'task','T-0002','judge-hold-set','x')", f.ago(900), lead)
	f.run()
	f.wantExpired(1)
	if got := f.str("SELECT judge_hold_reason FROM tasks WHERE id='T-0002'"); got != "no verdicts yet" {
		t.Errorf("hold of the task without verdicts = %q, want it kept", got)
	}
}

func TestExpiryClockIgnoresRepeatedSet(t *testing.T) {
	t.Run("re-set inside one hold span does not restart the clock", func(t *testing.T) {
		f := newFx(t)
		f.passPass()
		f.hold("wait for PR 703", lead, 125)
		f.hold("wait for PR 703", lead, 25)
		f.run()
		d := f.wantExpired(1)
		if d["held_min"] != float64(125) {
			t.Errorf("held_min = %v, want 125 (from the first set)", d["held_min"])
		}
	})
	t.Run("set after a clear measures from the new set", func(t *testing.T) {
		f := newFx(t)
		f.passPass()
		f.hold("old hold", lead, 300)
		f.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,'task',?,'judge-hold-clear','')",
			f.ago(200), lead, taskID)
		f.hold("new hold", lead, 130)
		f.hold("new hold", lead, 10)
		f.run()
		if n := f.events("judge-hold-expired"); n != 1 {
			t.Fatalf("%d judge-hold-expired events, want 1", n)
		}
		raw := f.str("SELECT detail FROM events WHERE entity_id=? AND action='judge-hold-expired'", taskID)
		if !strings.Contains(raw, `"held_min":130`) {
			t.Errorf("expired detail %s, want held_min 130 (first set after the clear)", raw)
		}
	})
	t.Run("set after a clear within the window is kept", func(t *testing.T) {
		f := newFx(t)
		f.passPass()
		f.hold("old hold", lead, 300)
		f.exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,'task',?,'judge-hold-clear','')",
			f.ago(200), lead, taskID)
		f.hold("new hold", lead, 60)
		f.run()
		if got := f.holdText(); got != "new hold" {
			t.Errorf("hold text = %q, want it kept", got)
		}
		if n := f.events("judge-hold-expired"); n != 0 {
			t.Errorf("%d judge-hold-expired events, want 0", n)
		}
	})
	t.Run("empty-reason set does not start the span", func(t *testing.T) {
		f := newFx(t)
		f.passPass()
		f.hold("", lead, 300)
		f.hold("real hold", lead, 60)
		f.run()
		if got := f.holdText(); got != "real hold" {
			t.Errorf("hold text = %q, want it kept", got)
		}
	})
}
