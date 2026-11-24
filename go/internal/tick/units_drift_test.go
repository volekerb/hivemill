package tick

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/ghx"
	"factory/go/internal/store"
)

// The fixture (testdata/drift_seed.sql, drift_gh.sh) was run through Python's tick_drift with the clock fixed at
// 2026-11-17T12:00:00+00:00 (TZ=UTC); drift_expected.txt is what it left behind (messages, events, config, throttle).
// drift_expected_budget0.txt is the same run with FACTORY_DRIFT_GH_BUDGET_S=0.

var driftFixedNow = time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// driftRig is a DB with the drift tables and seed, the fixed clocks and a gh stub on PATH.
type driftRig struct {
	t  *testing.T
	db *sql.DB
	r  *reapDB
	at time.Time
}

func newDriftRig(t *testing.T, ghBody string) *driftRig {
	t.Helper()
	db := dbtest.Raw(t, filepath.Join(t.TempDir(), "f.db"))
	t.Cleanup(func() { db.Close() })
	mustExec(t, db, readTestdata(t, "drift_schema.sql"))
	mustExec(t, db, readTestdata(t, "drift_seed.sql"))
	d := &driftRig{t: t, db: db, at: driftFixedNow}
	d.r = &reapDB{&store.Store{DB: db, Clock: func() time.Time { return d.at }, Zone: time.UTC}}
	oldNow, oldMono := driftNow, driftMono
	driftNow = func() time.Time { return d.at }
	t.Cleanup(func() { driftNow, driftMono = oldNow, oldMono })
	if ghBody == "" {
		ghBody = readTestdata(t, "drift_gh.sh")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(ghBody), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
	t.Setenv("FACTORY_DRIFT_GH_BUDGET_S", "")
	return d
}

// dump is the output of the Python generator: one JSON line per row.
func (d *driftRig) dump() string {
	d.t.Helper()
	var sb strings.Builder
	for _, q := range []struct{ tag, sql string }{
		{"msg", "SELECT id,sender,recipient,kind,subject,body,goal_id,task_id,created,read_at FROM messages ORDER BY id"},
		{"event", "SELECT id,ts,actor,entity,entity_id,action,detail FROM events ORDER BY id"},
		{"config", "SELECT key,value FROM config ORDER BY key"},
		{"throttle", "SELECT key,ts FROM throttle ORDER BY key"},
	} {
		rows, err := d.db.Query(q.sql)
		if err != nil {
			d.t.Fatal(err)
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				d.t.Fatal(err)
			}
			list := make([]any, len(vals))
			for i, v := range vals {
				switch x := v.(type) {
				case int64:
					list[i] = x
				case []byte:
					list[i] = string(x)
				default:
					list[i] = x
				}
			}
			s, err := store.PyDumps(list)
			if err != nil {
				d.t.Fatal(err)
			}
			sb.WriteString(q.tag + " " + s + "\n")
		}
		rows.Close()
	}
	return sb.String()
}

func (d *driftRig) run() {
	d.t.Helper()
	tickDrift(d.r)
}

func (d *driftRig) count(q string, args ...any) int {
	d.t.Helper()
	var n int
	if err := d.db.QueryRow(q, args...).Scan(&n); err != nil {
		d.t.Fatal(err)
	}
	return n
}

// TestDriftItems: the D1-D4 item set of every goal equals the one Python recorded in its drift events.
func TestDriftItems(t *testing.T) {
	d := newDriftRig(t, "")
	states := map[string]string{"https://github.com/o/r/pull/88": "MERGED", "https://github.com/o/r/pull/89": "OPEN"}
	want := map[string]string{} // goal -> the event detail Python wrote
	for _, l := range strings.Split(readTestdata(t, "drift_expected.txt"), "\n") {
		if !strings.HasPrefix(l, "event ") {
			continue
		}
		var row []any
		if err := json.Unmarshal([]byte(l[len("event "):]), &row); err != nil {
			t.Fatal(err)
		}
		if row[5] == "drift" && row[1] == "2026-11-17T12:00:00+00:00" {
			want[row[4].(string)] = row[6].(string)
		}
	}
	goals, err := driftGoals(d.db)
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, g := range goals {
		items, err := driftItems(d.db, g, driftFixedNow, states)
		if err != nil {
			t.Fatal(err)
		}
		w, ok := want[g.id]
		if len(items) == 0 {
			if ok && g.id != "G-008" {
				t.Errorf("%s: Python reported drift, Go found none", g.id)
			}
			continue
		}
		ia := make([]any, len(items))
		for i, s := range items {
			ia[i] = s
		}
		got, err := store.PyDumps([]any{g.id, ia})
		if err != nil {
			t.Fatal(err)
		}
		if !ok && g.id != "G-008" {
			t.Errorf("%s: Go found %s, Python none", g.id, got)
		} else if ok && got != w {
			t.Errorf("%s:\n got %s\nwant %s", g.id, got, w)
		}
		seen++
	}
	if seen != 9 { // G-001..G-008 and G-010; G-009 is done and not read
		t.Fatalf("goals with items: %d, want 9", seen)
	}
}

// TestDriftRouting: a whole run leaves the rows Python left (live lead, parked lead, no lead -> the org's director, a
// cross-org lead ignored, the goal's own org, byte-equal subject and body, G-008 deduped); the throttle holds the second
// run; the same item set is not re-sent inside 24 h and is sent again after it.
func TestDriftRouting(t *testing.T) {
	d := newDriftRig(t, "")
	d.run()
	if got, want := d.dump(), readTestdata(t, "drift_expected.txt"); got != want {
		t.Fatalf("rows differ from Python\n--- got\n%s--- want\n%s", got, want)
	}
	before := d.count("SELECT count(*) FROM messages")
	d.run() // inside the hour: the throttle returns
	if n := d.count("SELECT count(*) FROM messages"); n != before {
		t.Fatalf("second run inside the throttle sent %d messages", n-before)
	}
	d.at = driftFixedNow.Add(2 * time.Hour) // throttle over, dedupe not
	d.run()
	if n := d.count("SELECT count(*) FROM messages"); n != before {
		t.Fatalf("same item set re-sent inside 24 h: %d new messages", n-before)
	}
	d.at = driftFixedNow.Add(26 * time.Hour) // dedupe over (the events of the first run are 26 h old)
	mustExec(t, d.db, "DELETE FROM throttle")
	d.run()
	if n := d.count("SELECT count(*) FROM messages"); n <= before {
		t.Fatalf("after 26 h nothing was re-sent (messages %d, before %d)", n, before)
	}
}

// TestDriftRoutingRefusals: a lead of another org gets no mail (G-010 went to the director), a stopped lead without
// parked_at is no lead, and a throttle value that is not a number is a recorded failure.
func TestDriftRoutingRefusals(t *testing.T) {
	d := newDriftRig(t, "")
	mustExec(t, d.db, "UPDATE teams SET parked_at=NULL WHERE id='t-p'") // stopped and not parked: no lead
	d.run()
	var to string
	if err := d.db.QueryRow("SELECT recipient FROM messages WHERE goal_id='G-006'").Scan(&to); err != nil {
		t.Fatal(err)
	}
	if to != "claude-director" {
		t.Fatalf("G-006 with a stopped, unparked lead went to %s, want claude-director", to)
	}
	if err := d.db.QueryRow("SELECT recipient FROM messages WHERE goal_id='G-010'").Scan(&to); err != nil || to != "claude-director" {
		t.Fatalf("G-010 (cursor lead on a claude goal) went to %q (%v), want claude-director", to, err)
	}
}

// TestDriftGhBudget0: FACTORY_DRIFT_GH_BUDGET_S=0 never calls gh and reports every PR "D2 unchecked ... used up", as Python.
func TestDriftGhBudget0(t *testing.T) {
	d := newDriftRig(t, "#!/bin/sh\necho called >> "+filepath.Join(t.TempDir(), "calls")+"\nexit 9\n")
	t.Setenv("FACTORY_DRIFT_GH_BUDGET_S", "0")
	d.run()
	if got, want := d.dump(), readTestdata(t, "drift_expected_budget0.txt"); got != want {
		t.Fatalf("rows differ from Python\n--- got\n%s--- want\n%s", got, want)
	}
}

// TestDriftGhBudget: a gh that hangs is killed when the budget is used up: the run returns within budget + 1 s and the
// PRs are "D2 unchecked".
func TestDriftGhBudget(t *testing.T) {
	d := newDriftRig(t, "#!/bin/sh\nexec sleep 60\n")
	t.Setenv("FACTORY_DRIFT_GH_BUDGET_S", "2")
	start := time.Now()
	d.run()
	if el := time.Since(start); el > 3*time.Second {
		t.Fatalf("drift took %v with a 2 s budget", el)
	}
	var body string
	if err := d.db.QueryRow("SELECT body FROM messages WHERE goal_id='G-001'").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^D2 unchecked T-1 \(Command '\['gh', 'pr', 'view', 'https://github.com/o/r/pull/88', '--j`).MatchString(body) {
		t.Fatalf("T-1 is not reported as a gh timeout:\n%s", body)
	}
	if !strings.Contains(body, "D2 unchecked T-2 (gh time budget of 2s used up)") {
		t.Fatalf("T-2 is not reported as budget used up:\n%s", body)
	}
	if strings.Contains(body, "D2 T-1 in_review") {
		t.Fatalf("an unchecked PR must not be a D2 item:\n%s", body)
	}
}

// TestDriftConstants pins the Python values (factory_cli.py:9339-9345).
func TestDriftConstants(t *testing.T) {
	for name, c := range map[string][2]int{"THROTTLE": {driftThrottleS, 3600}, "DEDUPE": {driftDedupeS, 86400}, "IDLE": {driftIdleS, 7200},
		"BLOCKED": {driftBlockedS, 86400}, "GH_BUDGET": {driftGhBudgetS, 60}, "GH_CALL": {driftGhCallS, 30}} {
		if c[0] != c[1] {
			t.Errorf("DRIFT_%s_S = %d, Python has %d", name, c[0], c[1])
		}
	}
	if strings.Join(driftGoalOver, ",") != "done,closed,cancelled" {
		t.Errorf("DRIFT_GOAL_OVER = %v", driftGoalOver)
	}
	if pyG6(0) != "0" || pyG6(60) != "60" || pyG6(1234567) != "1.23457e+06" || pyG6(2.5) != "2.5" {
		t.Errorf("pyG6: %s %s %s %s", pyG6(0), pyG6(60), pyG6(1234567), pyG6(2.5))
	}
}

// TestDriftBudgetEnv: an invalid override falls back to 60, a valid one (also 0 and a negative number) is used.
func TestDriftBudgetEnv(t *testing.T) {
	for in, want := range map[string]float64{"": 60, "abc": 60, "2": 2, "0": 0, "-1": -1, " 7.5 ": 7.5} {
		t.Setenv("FACTORY_DRIFT_GH_BUDGET_S", in)
		if got := driftBudget(); got != want {
			t.Errorf("FACTORY_DRIFT_GH_BUDGET_S=%q: %v, want %v", in, got, want)
		}
	}
}

// TestDriftErrorContinues: a DB error inside drift is recorded as the drift-failed event (entity tick/drift, actor
// owner) and the unit returns nil; so does a failing event write.
func TestDriftErrorContinues(t *testing.T) {
	d := newDriftRig(t, "")
	mustExec(t, d.db, "ALTER TABLE goals RENAME TO goals_gone")
	u := &UnitCtx{DB: d.db}
	t.Setenv("FACTORY_TEST_NOW", "")
	if err := driftUnit(u); err != nil {
		t.Fatalf("drift returned %v, the tick must go on", err)
	}
	var actor, ent, id, detail string
	if err := d.db.QueryRow("SELECT actor,entity,entity_id,detail FROM events WHERE action='drift-failed'").Scan(&actor, &ent, &id, &detail); err != nil {
		t.Fatalf("no drift-failed event: %v", err)
	}
	if actor != "owner" || ent != "tick" || id != "drift" || !strings.Contains(detail, "no such table: goals") {
		t.Fatalf("drift-failed event: %s %s %s %q", actor, ent, id, detail)
	}
	if d.count("SELECT count(*) FROM throttle WHERE key='drift'") != 1 {
		t.Fatal("Python sets the throttle before reading the goals; so must Go")
	}
	mustExec(t, d.db, "DROP TABLE events")
	mustExec(t, d.db, "DELETE FROM throttle")
	if err := driftUnit(u); err != nil {
		t.Fatalf("a failing drift-failed event write stopped the tick: %v", err)
	}
}

// TestDriftThrottleGarbage: a throttle value that is not a float is Python's ValueError, recorded.
func TestDriftThrottleGarbage(t *testing.T) {
	d := newDriftRig(t, "")
	mustExec(t, d.db, "INSERT INTO throttle(key,ts) VALUES('drift','soon')")
	d.run()
	var detail string
	if err := d.db.QueryRow("SELECT detail FROM events WHERE action='drift-failed'").Scan(&detail); err != nil || detail != "could not convert string to float: 'soon'" {
		t.Fatalf("detail %q (%v)", detail, err)
	}
	if d.count("SELECT count(*) FROM messages") != 0 {
		t.Fatal("a failed throttle read must not send")
	}
}

// TestDriftThrottleHolds: 30 minutes after the last run nothing happens (no row changes).
func TestDriftThrottleHolds(t *testing.T) {
	d := newDriftRig(t, "")
	mustExec(t, d.db, "INSERT INTO throttle(key,ts) VALUES('drift','1794915000.0')")
	d.run()
	if d.count("SELECT count(*) FROM messages") != 0 || d.count("SELECT count(*) FROM events WHERE action LIKE 'drift%'") != 1 {
		t.Fatal("throttled run changed rows")
	}
}

// TestUnitTable: unit 24 is the Go unit drift, in the reap block between idle_coders and flagless.
func TestUnitTable(t *testing.T) {
	w := walkUnits()
	if len(w) < 24 || w[23].Name != "drift" || w[23].Block != "reap" || w[22].Name != "idle_coders" || w[24].Name != "flagless" {
		t.Fatalf("unit 24 is %+v", w[23])
	}
	if goUnits["drift"] == nil {
		t.Fatal("unit drift has no Go implementation")
	}
	for _, n := range []string{"pending_nudges", "reap_loop"} {
		if goUnits[n] == nil {
			t.Fatalf("unit %s has no Go implementation", n)
		}
	}
	if w[19].Name != "pending_nudges" || w[20].Name != "reap_loop" {
		t.Fatalf("units 20/21 are %s/%s", w[19].Name, w[20].Name)
	}
	// Units 20 and 21 are Go-capable only together: a Go set with exactly one of them is refused.
	fn := func(*UnitCtx) error { return nil }
	for name, set := range map[string]map[string]GoFunc{
		"20 only": {"pending_nudges": fn}, "21 only": {"reap_loop": fn},
		"20 and drift": {"pending_nudges": fn, "drift": fn}, "21 and drift": {"reap_loop": fn, "drift": fn},
	} {
		if err := checkGoSet(set); err == nil || !strings.Contains(err.Error(), "only together") {
			t.Errorf("%s: checkGoSet = %v, want a refusal", name, err)
		}
	}
	for name, set := range map[string]map[string]GoFunc{
		"both": {"pending_nudges": fn, "reap_loop": fn}, "neither": {"drift": fn}, "empty": {}, "nil": nil,
	} {
		if err := checkGoSet(set); err != nil {
			t.Errorf("%s: checkGoSet = %v, want nil", name, err)
		}
	}
	var out, errb bytes.Buffer
	code := Run(Config{Home: t.TempDir(), Go: map[string]GoFunc{"reap_loop": fn}, Stdout: &out, Stderr: &errb})
	if code == 0 || !strings.Contains(errb.String(), "only together") {
		t.Errorf("Run with only unit 21: code %d, stderr %q", code, errb.String())
	}
}

// TestSegmentsDriftGo: with drift in Go the offline row no longer rides along in the child that ends at judge_ci_prune
// (the offline branch runs the Go drift first); without it the old grouping holds.
func TestSegmentsDriftGo(t *testing.T) {
	has := func(segs []segment, unit string) bool {
		for _, s := range segs {
			for _, u := range s.units {
				if u == unit {
					return true
				}
			}
		}
		return false
	}
	with := segments(map[string]GoFunc{"drift": func(*UnitCtx) error { return nil }})
	without := segments(map[string]GoFunc{})
	if has(with, OfflineUnit) {
		t.Fatal("offline rides along in a child although drift is a Go unit")
	}
	if !has(without, OfflineUnit) {
		t.Fatal("without Go drift the offline row must ride along as before")
	}
}

// TestTickOfflineRunsGoDrift: herdr unreachable: the Go drift runs, then the offline child; each once.
func TestTickOfflineRunsGoDrift(t *testing.T) {
	r := newRig(t, []step{{}, {Stop: true}})
	r.goFns["judge_ci_prune"] = func(u *UnitCtx) error {
		u.State["agents"] = json.RawMessage("null")
		return nil
	}
	n := 0
	r.goFns["drift"] = func(u *UnitCtx) error { n++; return nil }
	if got := r.run(); got != 0 {
		t.Fatalf("exit %d %s", got, r.errb.String())
	}
	cs := r.calls()
	if n != 1 || len(cs) != 2 || cs[1].Units != "offline" {
		t.Fatalf("Go drift ran %d times, calls %+v", n, cs)
	}
}

// TestDriftUnicode: Python's str.strip() on gh's stderr removes NBSP and \v; a non-ASCII digit stays part of a task id.
func TestDriftUnicode(t *testing.T) {
	old := driftGh
	defer func() { driftGh = old }()
	driftGh = func(time.Duration, ...string) (ghx.Result, error) {
		return ghx.Result{Stderr: "\v\u00a0boom \u0663\u00a0\v", ExitCode: 1}, nil
	}
	_, err := driftPRState("u", 1)
	if err == nil || err.Error() != "boom \u0663" { // "\x0b\xa0boom \u0663\xa0\x0b".strip() == 'boom \u0663'
		t.Fatalf("err %v", err)
	}
	deps, err := driftDeps(`["T-\u0663", "T-3"]`)
	if err != nil || !deps["T-\u0663"] || !deps["T-3"] || len(deps) != 2 {
		t.Fatalf("deps %v %v", deps, err)
	}
}

// TestDriftPRState: the answers of gh and Python's text for each failure; the cases are what json.loads and the dict
// lookup raise.
func TestDriftPRState(t *testing.T) {
	old := driftGh
	defer func() { driftGh = old }()
	cases := []struct {
		out, errOut string
		code        int
		want        string // "" = ok with state "OPEN"; prefix "!" = uncaught in Python (reaches the catch-all)
	}{
		{out: `{"state": "OPEN"}`},
		{out: "", want: "Expecting value: line 1 column 1 (char 0)"},
		{out: "  ", want: "Expecting value: line 1 column 3 (char 2)"},
		{out: "\n x", want: "Expecting value: line 2 column 2 (char 2)"},
		{out: `{"a": 1} {`, want: "Extra data: line 1 column 10 (char 9)"},
		{out: "\n\n{} x", want: "Extra data: line 3 column 4 (char 5)"},
		{out: `{}`, want: "'state'"},
		{out: `[]`, want: "!"},
		{errOut: "  gh: nope \n", code: 1, want: "gh: nope"},
		{code: 4, want: "gh exit 4"},
	}
	for _, c := range cases {
		driftGh = func(time.Duration, ...string) (ghx.Result, error) {
			return ghx.Result{Stdout: c.out, Stderr: c.errOut, ExitCode: c.code}, nil
		}
		st, err := driftPRState("u", 1)
		switch {
		case c.want == "" && (err != nil || st != "OPEN"):
			t.Errorf("%q: %q %v", c.out, st, err)
		case c.want == "!":
			var bad *driftBadJSON
			if err == nil || !asBad(err, &bad) || !bad.fatal {
				t.Errorf("%q: want an uncaught error, got %v", c.out, err)
			}
		case c.want != "" && (err == nil || err.Error() != c.want):
			t.Errorf("%q: %v, want %s", c.out, err, c.want)
		}
	}
}

func asBad(err error, target **driftBadJSON) bool {
	b, ok := err.(*driftBadJSON)
	if ok {
		*target = b
	}
	return ok
}

// TestDriftDeps: set(json.loads(depends_on or "[]")) for the shapes Python accepts and refuses.
func TestDriftDeps(t *testing.T) {
	for _, s := range []string{"", "[]", `["T-1","T-2"]`, `[1, null, "T-1"]`, `{"T-1": 2}`, `"ab"`} {
		if _, err := driftDeps(s); err != nil {
			t.Errorf("%q: %v", s, err)
		}
	}
	for _, s := range []string{"{", "5", "null", `[[1]]`, `[{}]`} {
		if _, err := driftDeps(s); err == nil {
			t.Errorf("%q: want an error", s)
		}
	}
}

func FuzzDriftDeps(f *testing.F) {
	for _, s := range []string{"[]", `["T-1", "T-2"]`, `{"a": 1}`, `"x"`, "[1,", "\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { _, _ = driftDeps(s) }) // must not panic; the result is checked in TestDriftDeps
}

func FuzzDriftPRState(f *testing.F) {
	for _, s := range []string{`{"state": "OPEN"}`, "", "[]", `{"state": 1}`, `{"state": "MERGED"} x`, "\xff"} {
		f.Add(s, "")
	}
	f.Add("", "boom")
	f.Fuzz(func(t *testing.T, out, errOut string) {
		old := driftGh
		defer func() { driftGh = old }()
		code := 0
		if errOut != "" {
			code = 1
		}
		driftGh = func(time.Duration, ...string) (ghx.Result, error) {
			return ghx.Result{Stdout: out, Stderr: errOut, ExitCode: code}, nil
		}
		_, _ = driftPRState("u", 1)
	})
}

func FuzzDriftIdleTS(f *testing.F) {
	for _, s := range []string{"2026-11-17T12:00:00+00:00", "", "2026-11-17", "x", "2026-11-17 12:00:00Z", "\u0663"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = driftTS(sql.NullString{String: s, Valid: true})
		_ = jsonErrText(s)
		_, _ = driftFloat(s)
	})
}
