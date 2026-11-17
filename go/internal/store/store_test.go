package store

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

// Tests for the shared store. The expected texts in testdata are the output of one-off `python3 -I` runs
// (json.dumps, repr(float), str.isspace); the send/org cases port the resolution cases of internal/cli (message,
// orgs) against this package.

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

// storeTestDB is a throw-away DB with the factory schema plus the tables send reads, and a reapDB stamping events at the
// fixed time.
func storeTestDB(t *testing.T) (*sql.DB, *Store) {
	t.Helper()
	home := t.TempDir()
	path := dbtest.New(t, home, 0)
	db := dbtest.Raw(t, path)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(reapExtraSchema); err != nil {
		t.Fatal(err)
	}
	// Python now() converts to the process zone (astimezone()): pin a zone so the expected stamps do not depend on it.
	zone := time.FixedZone("", 60*60)
	fixed := time.Date(2026, 11, 17, 9, 30, 15, 0, zone)
	return db, &Store{DB: db, Clock: func() time.Time { return fixed }, Zone: zone}
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

// TestDefaultsMatchPython pins the generated DefaultConfig to the DEFAULT_CONFIG literal of the real
// bin/factory_cli.py, in order (a stale defaults_gen.go says so: run go generate ./internal/store).
func TestDefaultsMatchPython(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "..", "bin", "factory_cli.py"))
	if err != nil {
		t.Fatal(err)
	}
	lit, err := pydefaults.Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if len(lit) != len(DefaultConfig) || len(lit) < 100 {
		t.Fatalf("defaults_gen.go has %d keys, DEFAULT_CONFIG has %d: run `go generate ./internal/store`", len(DefaultConfig), len(lit))
	}
	for i, e := range lit {
		if DefaultConfig[i].Key != e.Key || DefaultConfig[i].Value != e.Value {
			t.Fatalf("defaults_gen.go drifted at %s (%q vs %q): run `go generate ./internal/store`", e.Key, DefaultConfig[i].Value, e.Value)
		}
	}
}

// strp is a *string for Msg fields.
func strp(s string) *string { return &s }

// event is event_as with the tick as the actor, the call the reap units make.
func (r *Store) event(entity, id, action string, detail any) error {
	return r.EventAs("tick", entity, id, action, detail, "")
}

func TestCfg(t *testing.T) {
	db, r := storeTestDB(t)
	if v, err := r.Cfg("blocked_alert_s"); err != nil || v != "600" {
		t.Fatalf("default: %q %v", v, err)
	}
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('blocked_alert_s','42')")
	if v, err := r.Cfg("blocked_alert_s"); err != nil || v != "42" {
		t.Fatalf("row wins: %q %v", v, err)
	}
	if _, err := r.Cfg("no_such_key"); err == nil || !strings.Contains(err.Error(), "no_such_key") {
		t.Fatalf("a key without a default must be an error naming it, got %v", err)
	}
}

func TestPyDumpsParity(t *testing.T) {
	var want map[string]string
	b, err := os.ReadFile("testdata/dumps.json")
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
		"dict_order":  Ordered{{"zeta", 1}, {"alpha", Ordered{{"b", 2}, {"a", []any{true, false}}}}},
		"dict_empty":  Ordered{}, "list_empty": []any{},
	}
	if len(got) != len(want) {
		t.Fatalf("%d cases here, %d in the fixture", len(got), len(want))
	}
	for name, v := range got {
		s, err := PyDumps(v)
		if err != nil {
			t.Errorf("%s: %v", name, err)
		} else if s != want[name] {
			t.Errorf("%s: pyDumps = %s, Python json.dumps = %s", name, s, want[name])
		}
	}
	if _, err := PyDumps(map[string]any{"a": 1}); err == nil {
		t.Error("a Go map has no insertion order: pyDumps must refuse it")
	}
}

func TestPyFloatReprParity(t *testing.T) {
	var rows [][2]string
	b, err := os.ReadFile("testdata/floats.json")
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
		if got := PyFloatRepr(f); got != r[1] {
			t.Errorf("PyFloatRepr(%s) = %s, Python says %s", r[0], got, r[1])
		}
	}
	if len(rows) < 20 {
		t.Fatalf("only %d float cases", len(rows))
	}
}

// TestPyIsSpaceUnicode: pyStrip must strip exactly what Python's str.strip() strips. space.txt lists every code point
// with str.isspace() true (29); NBSP U+00A0, \v and U+001C-U+001F are among them, U+0663 (an Arabic-Indic digit) is not.
func TestPyIsSpaceUnicode(t *testing.T) {
	b, err := os.ReadFile("testdata/space.txt")
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
		if PyIsSpace(r) != want[r] {
			t.Fatalf("PyIsSpace(U+%04X) = %v, Python str.isspace says %v", r, PyIsSpace(r), want[r])
		}
	}
	if got := PyStrip("\xc2\xa0\v 1\xd9\xa3 \x1f"); got != "1\xd9\xa3" {
		t.Errorf("pyStrip = %q", got)
	}
	if len(want) != 29 {
		t.Errorf("fixture has %d space characters, expected 29", len(want))
	}
}

func TestEventAs(t *testing.T) {
	db, r := storeTestDB(t)
	if err := r.event("agent", "a1", "lost", Ordered{{"pid", 7}, {"log", nil}, {"name", "caf\xc3\xa9"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.event("agent", "a1", "note", "plain text"); err != nil {
		t.Fatal(err)
	}
	if err := r.EventAs("owner", "task", "T-1", "x", nil, "2020-01-02T03:04:05+00:00"); err != nil {
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
	db, r := storeTestDB(t)
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
		_, got, err := r.Send(c.m)
		if err != nil || got != c.want {
			t.Errorf("%s: got %q err %v, want %q", c.name, got, err, c.want)
		}
	}
	for _, c := range []struct{ to, msg string }{
		{"lead:tm-2", "team tm-2 has no lead"}, {"lead:nope", "team nope has no lead"},
		{"nobody-here", "unknown recipient nobody-here"},
	} {
		if _, _, err := r.Send(Msg{To: c.to, Subject: "s"}); err == nil || err.Error() != c.msg {
			t.Errorf("send to %s: error %v, want %q", c.to, err, c.msg)
		}
	}
	n := len(messages(t, db))
	if n != len(cases) {
		t.Fatalf("%d rows for %d good sends: a failed send must write nothing", n, len(cases))
	}
}

func TestSendRowAndPrefixOn(t *testing.T) {
	db, r := storeTestDB(t)
	id, rcpt, err := r.Send(Msg{To: "director", Subject: "drift G-9: 2 items", Body: strp("body\ntext"), Kind: "alert",
		Goal: strp("G-9"), Sender: "tick"})
	if err != nil || id != 1 || rcpt != "director" {
		t.Fatalf("send: %d %q %v (prefix off: the default org keeps the bare name)", id, rcpt, err)
	}
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('org_prefix_default',' 1 ')")
	_, rcpt, err = r.Send(Msg{To: "boss", Subject: "s"})
	if err != nil || rcpt != "claude-boss" {
		t.Fatalf("prefix on (Python strips the value): %q %v", rcpt, err)
	}
	// prefix on and a legacy un-prefixed live boss of the default org: it is used
	addAgent(t, db, "boss", "boss", "live", "", "", "2026-11-17T08:00:00+01:00")
	_, rcpt, err = r.Send(Msg{To: "boss", Subject: "s"})
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
	db, r := storeTestDB(t)
	addAgent(t, db, "a-col", "coder", "live", "cursor", "", "")
	addAgent(t, db, "cursor-pre", "coder", "live", "", "", "")
	addAgent(t, db, "rt", "coder", "live", "", `{"runtime": "cursor"}`, "")
	addAgent(t, db, "bad-json", "coder", "live", "", `{not json`, "")
	addAgent(t, db, "bad-org", "coder", "live", "mars", "", "")
	for name, want := range map[string]string{"a-col": "cursor", "cursor-pre": "cursor", "rt": "cursor",
		"bad-json": "claude", "bad-org": "claude", "unregistered": "", "claude-unregistered": "claude", "": ""} {
		if got, err := r.AgentOrg(name); err != nil || got != want {
			t.Errorf("agentOrg(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
}

func FuzzSpawnArgsMap(f *testing.F) {
	for _, s := range []string{``, `{}`, `{"runtime":"cursor"}`, `[1]`, `{not json`, `null`, `{"host_backend":"hc","pid":3}`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		_, _ = SpawnArgsMap(raw)
		_ = SpawnRuntime(raw)
	})
}

func FuzzPyDumpsString(f *testing.F) {
	for _, s := range []string{"", "abc", "a\"b\\", "\x00\x1f\x7f", "caf\xc3\xa9", "\U0001F600", "\xff\xfe"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := PyDumps(s)
		if err != nil {
			t.Fatal(err)
		}
		var back string
		if err := json.Unmarshal([]byte(out), &back); err != nil {
			t.Fatalf("PyDumps(%q) = %s is not valid JSON: %v", s, out, err)
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
