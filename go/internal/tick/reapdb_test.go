package tick

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/store"
)

// Tests for the tick side of the shared support: the event clock, the herdr store adapter over the DB and the handoff
// state accessors. The DB helpers themselves are tested in internal/store.

const reapExtraSchema = `
CREATE TABLE messages (id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT, recipient TEXT, kind TEXT, subject TEXT,
  body TEXT, goal_id TEXT, task_id TEXT, created TEXT, read_at TEXT);
CREATE TABLE goals (id TEXT PRIMARY KEY, org TEXT);
CREATE TABLE tasks (id TEXT PRIMARY KEY, goal_id TEXT, status TEXT, pr_url TEXT);
CREATE TABLE teams (id TEXT PRIMARY KEY, lead_agent TEXT);
CREATE TABLE throttle (key TEXT PRIMARY KEY, ts TEXT);
`

func reapTestDB(t *testing.T) (*sql.DB, *reapDB) {
	t.Helper()
	db := dbtest.Raw(t, dbtest.New(t, t.TempDir(), 0))
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(reapExtraSchema); err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2026, 11, 17, 9, 30, 15, 0, time.UTC)
	return db, &reapDB{&store.Store{DB: db, Clock: func() time.Time { return fixed }, Zone: time.UTC}}
}

func mustExec(t *testing.T, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func addAgent(t *testing.T, db *sql.DB, name, role, status string) {
	t.Helper()
	mustExec(t, db, "INSERT INTO agents(name,role,status) VALUES(?,?,?)", name, role, status)
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
	r.Zone = time.FixedZone("", 60*60)
	if got := r.Now(); got != "2026-04-01T04:06:07+01:00" {
		t.Errorf("non-live home must honour FACTORY_TEST_NOW (+02:00 input, shown in the +01:00 local zone), got %s", got)
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
	r.Zone = time.FixedZone("", 60*60)
	if got := r.Now(); got != "1999-01-02T04:04:05+01:00" {
		t.Errorf("a live home must use the real clock, got %s", got)
	}
}

// TestReapStoreWithHerdr: the herdr client over the DB store closes the pane the host lists under the agent's name and
// records the skip event through the DB; a DB failure surfaces through Err.
func TestReapStoreWithHerdr(t *testing.T) {
	db, r := reapTestDB(t)
	mustExec(t, db, "INSERT INTO config(key,value) VALUES('terminal_backend','herdr')")
	addAgent(t, db, "w1", "coder", "live")
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
	var detail, actor string
	if err := db.QueryRow("SELECT detail, actor FROM events WHERE action='pane-close-skipped' AND entity_id='ghost'").Scan(&detail, &actor); err != nil {
		t.Fatal(err)
	}
	// Python close_named_pane records with event() = actor() = "owner", not event_as("tick").
	if actor != "owner" {
		t.Errorf("skip event actor = %q, Python writes owner", actor)
	}
	if detail != `{"agent":"ghost","pane":"stale","reason":"pane held by other"}` {
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
