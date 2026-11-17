package tick

import (
	"database/sql"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"factory/go/internal/cli/pydefaults"
	"factory/go/internal/factorydb/dbtest"
)

// Tests for the shared support of the reap units (reapdb.go, reapjson.go, reapstate.go). The expected texts in
// testdata/reap are the output of one-off `python3 -I` runs (json.dumps, repr(float), str.isspace); gen.py is in the PR body.

const reapExtraSchema = `
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT, recipient TEXT, kind TEXT, subject TEXT,
  body TEXT, goal_id TEXT, task_id TEXT, created TEXT, read_at TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, org TEXT);
CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT, status TEXT, pr_url TEXT);
CREATE TABLE teams (id TEXT PRIMARY KEY, lead_agent TEXT);
CREATE TABLE throttle (key TEXT PRIMARY KEY, ts TEXT);
`

// reapTestDB is a throw-away DB with the factory schema plus the tables send reads, and a reapDB stamping events at the
// fixed time.
func reapTestDB(t *testing.T) (*sql.DB, *reapDB) {
	t.Helper()
	home := t.TempDir()
	path := dbtest.New(t, home, 0)
	db := dbtest.Raw(t, path)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(reapExtraSchema); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 11, 17, 9, 30, 15, 0, time.FixedZone("", 3600))
	return db, &reapDB{db: db, clock: func() time.Time { return fixed }}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func addAgent(t *testing.T, db *sql.DB, name, role, status, org, spawnArgs, started string) {
	t.Helper()
	mustExec(t, db, "INSERT INTO agents(name,role,status,org,spawn_args,started) VALUES(?,?,?,?,?,?)",
		name, role, status, nullIfEmpty(org), nullIfEmpty(spawnArgs), nullIfEmpty(started))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// TestReapDefaultsMatchPython pins reapDefaults to the DEFAULT_CONFIG literal of the real bin/factory_cli.py.
func TestReapDefaultsMatchPython(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "bin", "factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	lit, err := pydefaults.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	py := map[string]string{}
	for _, e := range lit {
		py[e.Key] = e.Value
	}
	for k, want := range reapDefaults {
		got, ok := py[k]
		if !ok {
			t.Errorf("reapDefaults has %q, DEFAULT_CONFIG does not", k)
		} else if got != want {
			t.Errorf("reapDefaults[%q] = %q, DEFAULT_CONFIG says %q", k, want, got)
		}
	}
	if len(reapDefaults) < 8 {
		t.Fatalf("only %d defaults checked", len(reapDefaults))
	}
}

func TestReapCfg(t *testing.T) {
	db, r := reapTestDB(t)
	if v, err := r.cfg("blocked_alert_s"); err != nil || v != "600" {
		t.Fatalf("default: %q %v", v, err)
	}
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('blocked_alert_s','42')")
	if v, err := r.cfg("blocked_alert_s"); err != nil || v != "42" {
		t.Fatalf("row wins: %q %v", v, err)
	}
	if _, err := r.cfg("no_such_key"); err == nil || !strings.Contains(err.Error(), "no_such_key") {
		t.Fatalf("a key without a default must be an error naming it, got %v", err)
	}
}

func TestPyDumpsParity(t *testing.T) {
	var want map[string]string
	b, err := os.ReadFile("testdata/reap/dumps.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	got := map[string]any{
		"none": nil, "true": true, "int": 42, "neg": -7, "str_plain": "abc",
		"str_escapes": "a\"b\\c\nd\re\tf\bg\fh",
		"str_ctrl":    "x\x01y\x1fz\x7f",
		"str_unicode": "caf\xc3\xa9 \xd9\xa3 \xc2\xa0 \xe2\x80\xa8",
		"str_astral":  "smile \U0001F600",
		"list":        []any{1, "a", nil, []any{2.5}},
		"dict_order":  ordered{{"zeta", 1}, {"alpha", ordered{{"b", 2}, {"a", []any{true, false}}}}},
		"dict_empty":  ordered{}, "list_empty": []any{},
	}
	if len(got) != len(want) {
		t.Fatalf("%d cases here, %d in the fixture", len(got), len(want))
	}
	for name, v := range got {
		s, err := pyDumps(v)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if s != want[name] {
			t.Errorf("%s: pyDumps = %s, Python json.dumps = %s", name, s, want[name])
		}
	}
	if _, err := pyDumps(map[string]any{"a": 1}); err == nil {
		t.Error("a Go map has no insertion order: pyDumps must refuse it")
	}
}

func TestPyFloatReprParity(t *testing.T) {
	var rows [][2]string
	b, err := os.ReadFile("testdata/reap/floats.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		bits, err := strconv.ParseUint(r[0], 16, 64)
		if err != nil {
			t.Fatal(err)
		}
		f := math.Float64frombits(bits)
		if got := pyFloatRepr(f); got != r[1] {
			t.Errorf("pyFloatRepr(%s) = %s, Python says %s", r[0], got, r[1])
		}
	}
	if len(rows) < 20 {
		t.Fatalf("only %d float cases", len(rows))
	}
}

// TestPyIsSpaceUnicode: pyStrip must strip exactly what Python's str.strip() strips. space.txt lists every code point
// with str.isspace() true (29); NBSP U+00A0, \v and U+001C-U+001F are among them, U+0663 (an Arabic-Indic digit) is not.
func TestPyIsSpaceUnicode(t *testing.T) {
	b, err := os.ReadFile("testdata/reap/space.txt")
	if err != nil {
		t.Fatal(err)
	}
	want := map[rune]bool{}
	for _, h := range strings.Split(strings.TrimSpace(string(b)), ",") {
		n, err := strconv.ParseUint(h, 16, 32)
		if err != nil {
			t.Fatal(err)
		}
		want[rune(n)] = true
	}
	for r := rune(0); r <= 0x10ffff; r++ {
		if pyIsSpace(r) != want[r] {
			t.Fatalf("pyIsSpace(U+%04X) = %v, Python str.isspace says %v", r, pyIsSpace(r), want[r])
		}
	}
	if got := pyStrip("\xc2\xa0\v 1\xd9\xa3 \x1f"); got != "1\xd9\xa3" {
		t.Errorf("pyStrip = %q", got)
	}
	if len(want) != 29 {
		t.Errorf("fixture has %d space characters, expected 29", len(want))
	}
}

func TestReapEventAs(t *testing.T) {
	db, r := reapTestDB(t)
	if err := r.event("agent", "a1", "lost", ordered{{"pid", 7}, {"log", nil}, {"name", "caf\xc3\xa9"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.event("agent", "a1", "note", "plain text"); err != nil {
		t.Fatal(err)
	}
	if err := r.eventAs("owner", "task", "T-1", "x", nil, "2020-01-02T03:04:05+00:00"); err != nil {
		t.Fatal(err)
	}
	type row struct{ ts, actor, detail string }
	var got []row
	rs, err := db.Query("SELECT ts, actor, coalesce(detail,'<NULL>') FROM events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var x row
		if err := rs.Scan(&x.ts, &x.actor, &x.detail); err != nil {
			t.Fatal(err)
		}
		got = append(got, x)
	}
	want := []row{
		{"2026-11-17T09:30:15+01:00", "tick", "{\"pid\": 7, \"log\": null, \"name\": \"caf\\u00e9\"}"},
		{"2026-11-17T09:30:15+01:00", "tick", "plain text"},
		{"2020-01-02T03:04:05+00:00", "owner", "<NULL>"},
	}
	if len(got) != len(want) {
		t.Fatalf("rows: %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: %v, want %v", i, got[i], want[i])
		}
	}
	if err := r.event("a", "b", "c", map[string]any{"k": 1}); err == nil {
		t.Error("an unsupported detail type must fail, not write a guess")
	}
}

// TestNewReapDBClock: the events clock is FACTORY_TEST_NOW on a non-live home, the real clock otherwise, and an invalid
// value is an error (as in Python now()).
func TestNewReapDBClock(t *testing.T) {
	db, _ := reapTestDB(t)
	pw, home := t.TempDir(), t.TempDir()
	real := func() time.Time { return time.Date(1999, 1, 2, 3, 4, 5, 0, time.UTC) }
	withPruneIdentity(t, pruneTestEnv(pw, home, "2026-04-01T05:06:07+02:00", real))
	r, err := newReapDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.now(); got != "2026-04-01T05:06:07+02:00" {
		t.Errorf("non-live home must honour FACTORY_TEST_NOW, got %s", got)
	}
	withPruneIdentity(t, pruneTestEnv(pw, home, "2026-04-01T05:06:07", real))
	if _, err := newReapDB(db); err == nil || !strings.Contains(err.Error(), "FACTORY_TEST_NOW") {
		t.Errorf("an offset-less FACTORY_TEST_NOW is an error naming the variable, got %v", err)
	}
	// a live home ignores it
	liveHome := filepath.Join(pw, "factory")
	if err := os.MkdirAll(liveHome, 0o755); err != nil {
		t.Fatal(err)
	}
	withPruneIdentity(t, pruneTestEnv(pw, liveHome, "2026-04-01T05:06:07+02:00", real))
	r, err = newReapDB(db)
	if err != nil {
		t.Fatal(err)
	}
	if got := r.now(); !strings.HasPrefix(got, "1999-01-02T03:04:05") {
		t.Errorf("a live home must use the real clock, got %s", got)
	}
}

type msgRow struct{ sender, rcpt, kind, subject, body, goal, task, created string }

func messages(t *testing.T, db *sql.DB) []msgRow {
	t.Helper()
	rs, err := db.Query("SELECT sender, recipient, kind, subject, coalesce(body,'<NULL>'), coalesce(goal_id,'<NULL>'), " +
		"coalesce(task_id,'<NULL>'), created FROM messages ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	var out []msgRow
	for rs.Next() {
		var m msgRow
		if err := rs.Scan(&m.sender, &m.rcpt, &m.kind, &m.subject, &m.body, &m.goal, &m.task, &m.created); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	return out
}

// TestSendResolution ports resolve_recipient/org_for through send (the cases of tests/test_orgs*.py that reach the tick's
// alerts): a bare HQ role resolves inside the message's org; the live agent of that org wins; lead:<team>; owner means boss.
func TestSendResolution(t *testing.T) {
	db, r := reapTestDB(t)
	mustExec(t, db, "INSERT INTO goals(id,org) VALUES('G-1','cursor'),('G-2',NULL)")
	mustExec(t, db, "INSERT INTO tasks(id,goal_id) VALUES('T-1','G-1')")
	mustExec(t, db, "INSERT INTO teams(id,lead_agent) VALUES('tm-1','cursor-lead-g-1'),('tm-2',NULL)")
	addAgent(t, db, "cursor-director", "director", "live", "cursor", "", "2026-11-17T08:00:00+01:00")
	addAgent(t, db, "claude-director", "director", "live", "claude", "", "2026-11-17T08:00:00+01:00")
	addAgent(t, db, "claude-boss", "boss", "live", "", "", "2026-11-17T08:00:00+01:00")
	addAgent(t, db, "coder-x", "coder", "live", "", `{"runtime": "cursor"}`, "")

	cases := []struct {
		name string
		m    Msg
		want string
	}{
		{"goal org picks the cursor director", Msg{To: "director", Goal: strp("G-1")}, "cursor-director"},
		{"task org via its goal", Msg{To: "director", Task: strp("T-1")}, "cursor-director"},
		{"explicit org beats the goal", Msg{To: "director", Goal: strp("G-1"), Org: "claude"}, "claude-director"},
		{"no goal: sender's org (a cursor-runtime agent)", Msg{To: "director", Sender: "coder-x"}, "cursor-director"},
		{"owner is the boss", Msg{To: "owner"}, "claude-boss"},
		{"default org, prefix off, no bare director row: the org director that is live", Msg{To: "director", Goal: strp("G-2")}, "claude-director"},
		{"lead:<team>", Msg{To: "lead:tm-1"}, "cursor-lead-g-1"},
		{"an agent name", Msg{To: "coder-x"}, "coder-x"},
		{"a role with a live agent", Msg{To: "coder"}, "coder-x"},
		{"no live staff engineer: stored for the future org agent", Msg{To: "staff-engineer", Goal: strp("G-1")}, "cursor-staff-engineer"},
	}
	for _, c := range cases {
		c.m.Subject, c.m.Kind = "s", "alert"
		if c.m.Sender == "" {
			c.m.Sender = "tick"
		}
		_, got, err := r.send(c.m)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q err %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct{ to, msg string }{
		{"lead:tm-2", "team tm-2 has no lead"}, {"lead:nope", "team nope has no lead"},
		{"nobody-here", "unknown recipient nobody-here"},
	} {
		if _, _, err := r.send(Msg{To: c.to, Subject: "s"}); err == nil || err.Error() != c.msg {
			t.Errorf("send to %s: error %v, want %q", c.to, err, c.msg)
		}
	}
	n := len(messages(t, db))
	if n != len(cases) {
		t.Fatalf("%d rows for %d good sends: a failed send must write nothing", n, len(cases))
	}
}

func TestSendRowAndPrefixOn(t *testing.T) {
	db, r := reapTestDB(t)
	id, rcpt, err := r.send(Msg{To: "director", Subject: "drift G-9: 2 items", Body: strp("body\ntext"), Kind: "alert",
		Goal: strp("G-9"), Sender: "tick"})
	if err != nil || id != 1 || rcpt != "director" {
		t.Fatalf("send: %d %q %v (prefix off: the default org keeps the bare name)", id, rcpt, err)
	}
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('org_prefix_default',' 1 ')")
	_, rcpt, err = r.send(Msg{To: "boss", Subject: "s"})
	if err != nil || rcpt != "claude-boss" {
		t.Fatalf("prefix on (Python strips the value): %q %v", rcpt, err)
	}
	// prefix on and a legacy un-prefixed live boss of the default org: it is used
	addAgent(t, db, "boss", "boss", "live", "", "", "2026-11-17T08:00:00+01:00")
	_, rcpt, err = r.send(Msg{To: "boss", Subject: "s"})
	if err != nil || rcpt != "boss" {
		t.Fatalf("legacy boss: %q %v", rcpt, err)
	}
	got := messages(t, db)
	want := msgRow{"tick", "director", "alert", "drift G-9: 2 items", "body\ntext", "G-9", "<NULL>", "2026-11-17T09:30:15+01:00"}
	if got[0] != want {
		t.Errorf("row: %+v, want %+v", got[0], want)
	}
	if got[1].kind != "msg" || got[1].sender != "owner" || got[1].body != "<NULL>" {
		t.Errorf("defaults (kind msg, sender owner, body NULL): %+v", got[1])
	}
}

func TestAgentOrg(t *testing.T) {
	db, r := reapTestDB(t)
	addAgent(t, db, "a-col", "coder", "live", "cursor", "", "")
	addAgent(t, db, "cursor-pre", "coder", "live", "", "", "")
	addAgent(t, db, "rt", "coder", "live", "", `{"runtime": "cursor"}`, "")
	addAgent(t, db, "bad-json", "coder", "live", "", `{not json`, "")
	addAgent(t, db, "bad-org", "coder", "live", "mars", "", "")
	for name, want := range map[string]string{"a-col": "cursor", "cursor-pre": "cursor", "rt": "cursor",
		"bad-json": "claude", "bad-org": "claude", "unregistered": "", "claude-unregistered": "claude", "": ""} {
		if got, err := r.agentOrg(name); err != nil || got != want {
			t.Errorf("agentOrg(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

// TestReapStoreWithHerdr: the herdr client over the DB store closes the pane the host lists under the agent's name and
// records the skip event through the DB; a DB failure surfaces through Err.
func TestReapStoreWithHerdr(t *testing.T) {
	db, r := reapTestDB(t)
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('terminal_backend','herdr')")
	addAgent(t, db, "w1", "coder", "live", "", "", "")
	bin := t.TempDir()
	script := "#!/bin/sh\necho \"$*\" >> \"$STUB_LOG\"\n" +
		"if [ \"$1 $2\" = \"agent list\" ]; then echo '{\"id\":\"x\",\"result\":{\"agents\":[{\"name\":\"w1\",\"pane_id\":\"hp1\"},{\"name\":\"other\",\"pane_id\":\"stale\"}]}}'; fi\n"
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "log")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("STUB_LOG", log)
	c, st := newHerdrClient(r)
	state, pane, err := c.ClosePane("w1", "stale")
	if err != nil || state != "closed" || pane != "hp1" {
		t.Fatalf("ClosePane: %s %s %v", state, pane, err)
	}
	state, _, _ = c.ClosePane("ghost", "stale")
	if state != "not-found" {
		t.Fatalf("ghost: %s", state)
	}
	var detail string
	if err := db.QueryRow("SELECT detail FROM events WHERE action='pane-close-skipped' AND entity_id='ghost'").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "pane held by other") {
		t.Errorf("skip event: %s", detail)
	}
	if st.Err() != nil {
		t.Fatalf("store error: %v", st.Err())
	}
	lb, _ := os.ReadFile(log)
	if !strings.Contains(string(lb), "pane close hp1") {
		t.Errorf("herdr calls: %s", lb)
	}
	db.Close()
	if v, ok := st.Config("terminal_backend"); ok || v != "" || st.Err() == nil {
		t.Errorf("a closed DB must surface through Err: %q %v %v", v, ok, st.Err())
	}
}

func rawState(t *testing.T, m map[string]string) State {
	t.Helper()
	s := State{}
	for k, v := range m {
		s[k] = json.RawMessage(v)
	}
	return s
}

func TestReapStateHelpers(t *testing.T) {
	s := rawState(t, map[string]string{
		"agents":      `[{"name":"a","pane_id":"p1","agent_status":"idle"},{"name":"b","pane_id":"p2"}]`,
		"hc_back":     `false`,
		"hc_by_name":  `{"b":{"name":"b","pane_id":"hcb"},"c":{"name":"c"}}`,
		"t":           `1763366582.5`,
		"tui_by_name": `null`,
	})
	agents, null, err := stateAgents(s)
	if err != nil || null || len(agents) != 2 || agents[0].Name() != "a" {
		t.Fatalf("stateAgents: %v %v %v", agents, null, err)
	}
	byName, byPane, err := reapIndex(s)
	if err != nil {
		t.Fatal(err)
	}
	if byName["b"].PaneID() != "hcb" || byName["c"] == nil || byName["a"] == nil || byPane["p2"].Name() != "b" {
		t.Errorf("not hc-backed: the hc rows go over by_name: %v %v", byName, byPane)
	}
	s["hc_back"] = json.RawMessage("true")
	byName, _, _ = reapIndex(s)
	if byName["b"].PaneID() != "p2" || byName["c"] != nil {
		t.Errorf("hc-backed: by_name is the agents list alone: %v", byName)
	}
	if m, err := stateAgentMap(s, "tui_by_name"); err != nil || m == nil || len(m) != 0 {
		t.Errorf("null map is empty: %v %v", m, err)
	}
	if f, err := stateFloat(s, "t"); err != nil || f != 1763366582.5 {
		t.Errorf("stateFloat: %v %v", f, err)
	}
	if b, err := stateBoolOr(s, "hc_list_ok", true); err != nil || !b {
		t.Errorf("stateBoolOr default: %v %v", b, err)
	}
	// agents null: the host could not be asked
	s["agents"] = json.RawMessage("null")
	if a, null, err := stateAgents(s); err != nil || !null || a != nil {
		t.Errorf("null agents: %v %v %v", a, null, err)
	}
	// every refusal names the key
	for _, c := range []struct {
		key, val string
		call     func(State) error
	}{
		{"agents", `{"x":1}`, func(s State) error { _, _, e := stateAgents(s); return e }},
		{"hc_back", `"yes"`, func(s State) error { _, e := stateBool(s, "hc_back"); return e }},
		{"t", `"soon"`, func(s State) error { _, e := stateFloat(s, "t"); return e }},
		{"hc_by_name", `[1]`, func(s State) error { _, e := stateAgentMap(s, "hc_by_name"); return e }},
	} {
		bad := rawState(t, map[string]string{c.key: c.val})
		if err := c.call(bad); err == nil || !strings.Contains(err.Error(), c.key) {
			t.Errorf("%s=%s: error %v must name the key", c.key, c.val, err)
		}
	}
	if _, err := stateFloat(State{}, "t"); err == nil || !strings.Contains(err.Error(), "herdr_list") {
		t.Errorf("a missing key says which unit has to run first: %v", err)
	}
}

// TestSetStateFloatKeepsFloatType: Python writes t as a float; an integral value must stay "N.0" so a child reads a float.
func TestSetStateFloatKeepsFloatType(t *testing.T) {
	s := State{}
	setStateFloat(s, "t", 1763366582)
	if string(s["t"]) != "1763366582.0" {
		t.Errorf("t = %s", s["t"])
	}
	setStateFloat(s, "t", 1763366582.25)
	if string(s["t"]) != "1763366582.25" {
		t.Errorf("t = %s", s["t"])
	}
}

func FuzzStateAgents(f *testing.F) {
	for _, s := range []string{`null`, `[]`, `[{"name":"a","pane_id":"p1"}]`, `[1]`, `{}`, `"x"`, ``, `[{"name":null}]`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		s := State{"agents": json.RawMessage(raw), "hc_back": json.RawMessage(raw), "hc_by_name": json.RawMessage(raw)}
		_, _, _ = stateAgents(s)
		_, _ = stateBoolOr(s, "hc_back", false)
		_, _ = stateAgentMap(s, "hc_by_name")
		_, _, _ = reapIndex(s)
	})
}

func FuzzSpawnArgsMap(f *testing.F) {
	for _, s := range []string{``, `{}`, `{"runtime":"cursor"}`, `[1]`, `{not json`, `null`, `{"host_backend":"hc","pid":3}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = spawnArgsMap(raw)
		_ = spawnRuntime(raw)
	})
}

func FuzzPyDumpsString(f *testing.F) {
	for _, s := range []string{"", "abc", "a\"b\\", "\x00\x1f\x7f", "caf\xc3\xa9", "\U0001F600", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := pyDumps(s)
		if err != nil {
			t.Fatal(err)
		}
		var back string
		if err := json.Unmarshal([]byte(out), &back); err != nil {
			t.Fatalf("pyDumps(%q) = %s is not valid JSON: %v", s, out, err)
		}
		var exp strings.Builder
		for _, r := range s { // each invalid byte is one U+FFFD, as in the writer
			exp.WriteRune(r)
		}
		if exp.String() != back {
			t.Fatalf("round trip: %q -> %s -> %q", s, out, back)
		}
		for _, r := range out {
			if r > 0x7f {
				t.Fatalf("non-ASCII byte in %s", out)
			}
		}
	})
}
