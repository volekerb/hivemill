package s2cli

import (
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"factory/go/internal/factorydb"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_db_layout.py (see ported_s2cli.txt). The tick and `init` are not ported to Go: tests that run
// them call NeedPython and skip, visibly, where python3 is missing.
//
// not ported (Python internals or other Python modules, no Go counterpart):
//   SchemaVersionTests.test_schema_hash_requires_a_version_bump - hashes bin/factory_cli.py SCHEMA + migrate() source;
//     Go's schema pin is go/internal/factorydb's own test.
//   SchemaVersionTests.test_go_schema_version_matches - compares the Python SCHEMA_VERSION constant with the Go one.
//   LayoutTests.test_db_open_is_rw_only_no_create - calls Python f.db() on a missing file; the Go open path is
//     covered by factorydb's tests and by TestDBLayoutNoImplicitCreate through the CLI.
//   LayoutTests.test_settings_have_db_and_legacy_deny_lines - renders the Python settings templates (tests/harness.py).
//   NoOverrideTests.test_d_hook_temp_home_exemption_cannot_reach_live_db - drives the Python hook (guard_impl).
//   NoOverrideTests.test_web_context_picks_the_home_only, WebReaderTests (2) - the Python web package.
//   NoOverrideTests.test_no_factory_db_reads_left - git grep over bin/, web/, scout/, roles/ (outside go/, G9).
//   MigrationTests.test_rename_failure_after_commit_still_records_event, test_stale_legacy_path_after_migration_says_moved
//     - monkeypatch os.rename / f.DB inside the Python interpreter.
//   GuardLayoutTests (3) - the Python hook reading the DB layout (ReadsBase).

func openPath(t testing.TB, path string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(30000)"}).String()
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	return d // the caller closes it: an open connection keeps -wal/-shm files and locks the CLI under test would meet
}

func pathExec(t testing.TB, path, q string, args ...any) {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func pathInt(t testing.TB, path, q string, args ...any) int {
	t.Helper()
	var n int
	d := openPath(t, path)
	defer d.Close()
	if err := d.QueryRow(q, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func tables(t testing.TB, d *sql.DB) []string {
	t.Helper()
	rows, err := d.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// fillEveryTable adds one extra row to every table, so a migration that loses a table or rows shows up in the counts.
func fillEveryTable(t testing.TB, path string) {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	for _, tb := range tables(t, d) {
		rows, err := d.Query(`PRAGMA table_info("` + tb + `")`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		var vals []any
		for rows.Next() {
			var cid, notnull, pk int
			var name, typ string
			var dflt any
			if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
				t.Fatal(err)
			}
			if pk != 0 && strings.EqualFold(typ, "INTEGER") {
				continue
			}
			names = append(names, name)
			if u := strings.ToUpper(typ); u == "INTEGER" || u == "REAL" {
				vals = append(vals, "1")
			} else {
				vals = append(vals, "zz-legacy")
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		q := `INSERT OR IGNORE INTO "` + tb + `"(` + strings.Join(names, ",") + `) VALUES(` + strings.TrimSuffix(strings.Repeat("?,", len(names)), ",") + `)`
		if _, err := d.Exec(q, vals...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func rowCounts(t testing.TB, path string) map[string]int {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	out := map[string]int{}
	for _, tb := range tables(t, d) {
		var n int
		if err := d.QueryRow(`SELECT count(*) FROM "` + tb + `"`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[tb] = n
	}
	// the tick keeps these bookkeeping keys (util_alerts, state dump throttle); they are not settings the test changes
	var cfg int
	if err := d.QueryRow("SELECT count(*) FROM config WHERE key NOT IN ('util_cap_breach_last_id', 'last_state_dump_ts')").Scan(&cfg); err != nil {
		t.Fatal(err)
	}
	out["config"] = cfg
	return out
}

// legacyEnv is LegacyCase.setUp: a temp home in the pre-migration layout (HOME/factory.db, no db/ dir).
func legacyEnv(t *testing.T) (e *ft.Env, legacy, newDB string) {
	t.Helper()
	e = ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	legacy = filepath.Join(e.Home, "factory.db")
	newDB = e.DBPath
	pathExec(t, newDB, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := os.Rename(newDB, legacy); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.Remove(newDB + ext); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	if err := os.Remove(filepath.Dir(newDB)); err != nil {
		t.Fatal(err)
	}
	fillEveryTable(t, legacy)
	// the tick starts the daily retro agent once the local hour is >= retro_hour: that spawn writes hq_workspace,
	// so the counts would depend on the wall clock. These tests are not about retro.
	pathExec(t, legacy, "UPDATE config SET value='0' WHERE key='retro_enabled'")
	return e, legacy, newDB
}

func migrated(t testing.TB, legacy string) []string {
	t.Helper()
	m, err := filepath.Glob(legacy + ".migrated-*")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(m)
	return m
}

func eventDetails(t testing.TB, path, action string) []string {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	rows, err := d.Query("SELECT detail FROM events WHERE action=?", action)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s sql.NullString
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s.String)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// dropTriggers is DROP_TRIGGERS of the Python module, the command docs/db-layout.md prints for the rollback.
func dropTriggers(t testing.TB, path string) {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	rows, err := d.Query("SELECT name FROM sqlite_master WHERE type='trigger' AND name LIKE 'factory_moved_%'")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		names = append(names, n)
	}
	rows.Close()
	for _, n := range names {
		if _, err := d.Exec(`DROP TRIGGER "` + n + `"`); err != nil {
			t.Fatal(err)
		}
	}
}

// ---- SchemaVersionTests --------------------------------------------------------------------------------------------

func TestDBLayoutInitStampsUserVersion(t *testing.T) {
	e := ft.New(t)
	if got := pathInt(t, e.DBPath, "PRAGMA user_version"); got != factorydb.SchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, factorydb.SchemaVersion)
	}
}

func TestDBLayoutMigrateStampsAnOldDBAndALaterOpenDoesNotRewriteIt(t *testing.T) {
	e := ft.New(t)
	pathExec(t, e.DBPath, "PRAGMA user_version = 0")
	e.OK("whoami") // any command opens the DB through migrate()
	if got := pathInt(t, e.DBPath, "PRAGMA user_version"); got != factorydb.SchemaVersion {
		t.Fatalf("user_version = %d, want %d", got, factorydb.SchemaVersion)
	}
}

// ---- SchedulesTableTests -------------------------------------------------------------------------------------------

var schedulesCols = []string{"id", "hour", "days", "job", "goal_id", "token_cap", "created_by", "created",
	"removed_at", "last_fired_date", "last_outcome", "last_outcome_at"}

func schedCols(t testing.TB, path string) []string {
	t.Helper()
	d := openPath(t, path)
	defer d.Close()
	rows, err := d.Query("PRAGMA table_info(schedules)")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			t.Fatal(err)
		}
		out = append(out, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDBLayoutInitHasSchedulesTable(t *testing.T) {
	e := ft.New(t)
	if got := schedCols(t, e.DBPath); !reflect.DeepEqual(got, schedulesCols) {
		t.Fatalf("schedules columns = %v", got)
	}
}

func TestDBLayoutMigrateAddsSchedulesIdempotent(t *testing.T) {
	e := ft.New(t)
	pathExec(t, e.DBPath, "DROP TABLE schedules")
	e.NeedPython() // the schedules table is re-created by the Python migrate(); the Go open does not add tables
	py := ft.Opts{Env: map[string]string{"FACTORY_CLI_IMPL": "python"}}
	e.OKWith(py, "whoami") // opens the DB through migrate() ...
	e.OKWith(py, "whoami") // ... twice: the second run finds the table and changes nothing
	if got := schedCols(t, e.DBPath); !reflect.DeepEqual(got, schedulesCols) {
		t.Fatalf("schedules columns = %v", got)
	}
}

func TestDBLayoutSchedulesCheckConstraints(t *testing.T) {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	const ins = "INSERT INTO schedules(id,hour,days,job,goal_id,token_cap,created_by,created) VALUES(?,?,?,?,?,?,?,?)"
	if _, err := d.Exec(ins, "S-001", 9, "mon", "job", "G-001", 1000, "owner", "t"); err != nil {
		t.Fatal(err)
	}
	for _, hour := range []int{24, -1} {
		if _, err := d.Exec(ins, "S-002", hour, "mon", "job", "G-001", 1000, "owner", "t"); err == nil {
			t.Errorf("hour=%d was accepted", hour)
		}
	}
	for _, cap := range []int{0, -1} {
		if _, err := d.Exec(ins, "S-003", 9, "mon", "job", "G-001", cap, "owner", "t"); err == nil {
			t.Errorf("token_cap=%d was accepted", cap)
		}
	}
}

// ---- LayoutTests ---------------------------------------------------------------------------------------------------

func TestDBLayoutInitCreatesDBDirOnly(t *testing.T) {
	e := ft.New(t)
	if _, err := os.Stat(e.DBPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(e.Home, "factory.db")); !os.IsNotExist(err) {
		t.Fatalf("a legacy factory.db exists next to db/: %v", err)
	}
	st, err := os.Stat(filepath.Dir(e.DBPath))
	if err != nil {
		t.Fatal(err)
	}
	if mode := st.Mode().Perm(); mode != 0o700 {
		t.Fatalf("db dir mode = %o, want 700", mode)
	}
	contains(t, "whoami", e.OK("whoami").Stdout, "owner")
	e.OK("goal", "list")
}

func TestDBLayoutNoImplicitCreate(t *testing.T) {
	e := ft.New(t)
	home := t.TempDir()
	r := e.FailWith(ft.Opts{Agent: "owner", Env: map[string]string{"FACTORY_HOME": home}}, 1, "goal", "list")
	contains(t, "stderr", r.Stderr, "run `factory init`")
	left, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("the CLI created %v in an empty home", left)
	}
}

func TestDBLayoutFactoryDBEnvHasNoEffect(t *testing.T) {
	e := ft.New(t)
	other := filepath.Join(e.Home, "other", "f.db")
	if err := os.Mkdir(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	e.OKWith(ft.Opts{Env: map[string]string{"FACTORY_DB": other}}, "config", "nudge_cooldown_s", "77")
	if got := e.SQL("SELECT value FROM config WHERE key='nudge_cooldown_s'")[0][0]; got != "77" { // the home's DB took the write
		t.Fatalf("nudge_cooldown_s = %q, want 77", got)
	}
	if _, err := os.Stat(other); !os.IsNotExist(err) {
		t.Fatalf("FACTORY_DB file was created: %v", err)
	}
	if left, _ := os.ReadDir(filepath.Dir(other)); len(left) != 0 {
		t.Fatalf("files appeared next to FACTORY_DB: %v", left)
	}
}

// ---- NoOverrideTests -----------------------------------------------------------------------------------------------

// T-0159: no env var but FACTORY_HOME picks the DB. The temp home is the "live" layout; its db/factory.db holds G-001
// (the marker a leak would show).
type noOverride struct {
	e        *ft.Env
	live     string
	other    string
	snap     []byte
	messages int
}

func newNoOverride(t *testing.T) *noOverride {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	pathExec(t, e.DBPath, "PRAGMA wal_checkpoint(TRUNCATE)")
	snap, err := os.ReadFile(e.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(e.Root, "other-home")
	if err := os.Mkdir(other, 0o755); err != nil {
		t.Fatal(err)
	}
	return &noOverride{e: e, live: e.DBPath, other: other, snap: snap, messages: pathInt(t, e.DBPath, "SELECT count(*) FROM messages")}
}

// run is sh(): the CLI with FACTORY_HOME=home and FACTORY_DB pointing at the live DB (unless db is false).
func (n *noOverride) run(home string, db bool, agent string, args ...string) ft.Result {
	env := map[string]string{"FACTORY_HOME": home}
	if db {
		env["FACTORY_DB"] = n.live
	}
	return n.e.RunWith(ft.Opts{Agent: agent, Env: env}, args...)
}

func (n *noOverride) untouched(t *testing.T, r ft.Result) {
	t.Helper()
	notContains(t, "output", r.Stdout+r.Stderr, "G-001")
	if got := pathInt(t, n.live, "SELECT count(*) FROM messages"); got != n.messages {
		t.Fatalf("messages = %d, want %d", got, n.messages)
	}
	pathExec(t, n.live, "PRAGMA wal_checkpoint(TRUNCATE)")
	b, err := os.ReadFile(n.live)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != string(n.snap) {
		t.Fatal("the live DB file changed")
	}
}

func TestDBLayoutNoOverrideAFactoryDBHasNoEffect(t *testing.T) {
	n := newNoOverride(t)
	control := n.run(n.e.Home, false, "", "goal", "list")
	contains(t, "control stdout", control.Stdout, "G-001") // the marker is visible through the real home
	r := n.run(n.other, true, "", "goal", "list")          // FACTORY_DB=<live db> exported
	if r.Exit != 1 {
		t.Fatalf("exit %d, want 1: %s%s", r.Exit, r.Stdout, r.Stderr)
	}
	contains(t, "stderr", r.Stderr, "run `factory init`")
	n.untouched(t, r)
	n.e.NeedPython() // init is not ported
	if i := n.run(n.other, true, "", "init"); i.Exit != 0 {
		t.Fatalf("init exit %d: %s%s", i.Exit, i.Stdout, i.Stderr)
	}
	r = n.run(n.other, true, "", "goal", "list")
	if r.Exit != 0 {
		t.Fatalf("goal list exit %d: %s%s", r.Exit, r.Stdout, r.Stderr)
	}
	if _, err := os.Stat(filepath.Join(n.other, "db", "factory.db")); err != nil { // init built the home's DB, not the override
		t.Fatal(err)
	}
	n.untouched(t, r)
}

func TestDBLayoutNoOverrideBParentOfLiveHomeIsNotTheLiveDB(t *testing.T) {
	n := newNoOverride(t)
	for _, parent := range []string{n.e.Root, n.e.Home + "/..", "/"} {
		for _, cmd := range [][]string{{"goal", "list"}, {"agents"}} {
			r := n.run(parent, true, "", cmd...)
			if r.Exit == 0 {
				t.Fatalf("%s %v: exit 0: %s%s", parent, cmd, r.Stdout, r.Stderr)
			}
			n.untouched(t, r)
		}
	}
}

func TestDBLayoutNoOverrideCReviewerB1ReproMsgAsOwner(t *testing.T) {
	n := newNoOverride(t)
	for _, parent := range []string{n.e.Root, n.e.Home + "/.."} {
		r := n.run(parent, true, "owner", "msg", "boss", "x", "y")
		if r.Exit == 0 {
			t.Fatalf("%s: exit 0: %s%s", parent, r.Stdout, r.Stderr)
		}
		n.untouched(t, r)
	}
}

// ---- MigrationTests ------------------------------------------------------------------------------------------------

func TestDBLayoutMigrationResolutionPrefersLegacyUntilMigrated(t *testing.T) {
	e, legacy, newDB := legacyEnv(t)
	e.NeedPython() // legacy-layout homes and the tick are served by bin/factory
	contains(t, "goal list before the tick", e.OK("goal", "list").Stdout, "G-001")
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy db missing before the tick: %v", err)
	}
	e.OK("tick")
	if _, err := os.Stat(newDB); err != nil {
		t.Fatalf("db/factory.db missing after the tick: %v", err)
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy db still present after the tick: %v", err)
	}
}

func TestDBLayoutMigrationTickMigratesOnce(t *testing.T) {
	e, legacy, newDB := legacyEnv(t)
	e.NeedPython()
	before := rowCounts(t, legacy)
	for tb, n := range before {
		if n <= 0 {
			t.Fatalf("table %s is empty before the migration: %v", tb, before)
		}
	}
	e.OK("tick")
	after := rowCounts(t, newDB)
	// the rest of the tick may add rows (throttle, events): never lose any
	if len(after) != len(before) {
		t.Fatalf("tables differ: before %v after %v", before, after)
	}
	for tb, n := range before {
		if after[tb] < n {
			t.Fatalf("table %s lost rows: before %d after %d", tb, n, after[tb])
		}
	}
	if _, err := os.Stat(legacy); !os.IsNotExist(err) {
		t.Fatalf("legacy db still present: %v", err)
	}
	if m := migrated(t, legacy); len(m) != 1 {
		t.Fatalf("migrated files = %v, want one", m)
	}
	ev := eventDetails(t, newDB, "db-migrated")
	if len(ev) != 1 {
		t.Fatalf("db-migrated events = %v, want one", ev)
	}
	var detail struct {
		Rows map[string]int `json:"rows"`
	}
	if err := json.Unmarshal([]byte(ev[0]), &detail); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(detail.Rows, before) {
		t.Fatalf("event rows = %v, want %v", detail.Rows, before)
	}
	e.OK("tick")
	if got := len(eventDetails(t, newDB, "db-migrated")); got != 1 {
		t.Fatalf("db-migrated events after the second tick = %d", got)
	}
	if m := migrated(t, legacy); len(m) != 1 {
		t.Fatalf("migrated files after the second tick = %v", m)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(newDB), "factory.db.tmp")); !os.IsNotExist(err) {
		t.Fatalf("factory.db.tmp left behind: %v", err)
	}
}

func TestDBLayoutMigrationLateWriterBlockedAndAllowed(t *testing.T) {
	e, legacy, newDB := legacyEnv(t)
	e.NeedPython()
	late := openPath(t, legacy)
	defer late.Close()
	if _, err := late.Exec("SELECT count(*) FROM config"); err != nil { // opened before the migration
		t.Fatal(err)
	}
	e.OK("tick")
	_, err := late.Exec("INSERT INTO config VALUES('zz-late','1')")
	if err == nil {
		t.Fatal("a late writer on the moved file was not refused")
	}
	contains(t, "late writer error", err.Error(), "factory.db moved")
	const q = "SELECT count(*) FROM config WHERE key='zz-late'"
	if got := e.Count(q); got != 0 {
		t.Fatalf("late row reached the new DB: %d", got)
	}
	if got := pathInt(t, migrated(t, legacy)[0], q); got != 0 {
		t.Fatalf("late row reached the migrated file: %d", got)
	}
	e.OK("config", "nudge_cooldown_s", "55") // 5b: the CLI after migration lands in db/factory.db
	if got := pathInt(t, newDB, "SELECT value FROM config WHERE key='nudge_cooldown_s'"); got != 55 {
		t.Fatalf("nudge_cooldown_s = %d, want 55", got)
	}
}

func TestDBLayoutMigrationFailureKeepsLegacyAuthoritative(t *testing.T) {
	e, legacy, _ := legacyEnv(t)
	e.NeedPython()
	if err := os.WriteFile(filepath.Join(e.Home, "db"), []byte("a file where the db dir should be"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := rowCounts(t, legacy)
	// the Python harness sets no FACTORY_TEST_NOW: the 1 h "already notified" window compares the event's ts with the
	// real clock, so a pinned test clock would send the alert twice
	realClock := ft.Opts{Env: map[string]string{"FACTORY_TEST_NOW": ""}}
	e.OKWith(realClock, "tick")
	e.OKWith(realClock, "tick")
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("legacy db is gone: %v", err)
	}
	if m := migrated(t, legacy); len(m) != 0 {
		t.Fatalf("migrated files = %v, want none", m)
	}
	if tmp, _ := filepath.Glob(filepath.Join(e.Home, "db*", "*.tmp")); len(tmp) != 0 {
		t.Fatalf("temp files left: %v", tmp)
	}
	if got := len(eventDetails(t, legacy, "db-migrate-failed")); got != 2 {
		t.Fatalf("db-migrate-failed events = %d, want 2", got)
	}
	ld := openPath(t, legacy)
	defer ld.Close()
	rows, err := ld.Query("SELECT recipient FROM messages WHERE subject LIKE '%migration%failed%'")
	if err != nil {
		t.Fatal(err)
	}
	var recips []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		recips = append(recips, r)
	}
	rows.Close()
	if !reflect.DeepEqual(recips, []string{"director"}) { // two failed ticks, one message
		t.Fatalf("migration-failed messages to %v, want [director]", recips)
	}
	e.OK("config", "nudge_cooldown_s", "66") // still writable
	if got := rowCounts(t, legacy)["config"]; got != before["config"] {
		t.Fatalf("config rows = %d, want %d", got, before["config"])
	}
	if got := pathInt(t, legacy, "SELECT value FROM config WHERE key='nudge_cooldown_s'"); got != 66 {
		t.Fatalf("nudge_cooldown_s = %d, want 66", got)
	}
}

func TestDBLayoutMigrationMissingWALAfterCommitIsNotAFailure(t *testing.T) {
	// SQLite may delete -wal/-shm when the last connection closes: only the main file is left to rename
	e, _, newDB := legacyEnv(t)
	e.NeedPython()
	e.OK("tick")
	ev := eventDetails(t, newDB, "db-migrated")
	if len(ev) != 1 {
		t.Fatalf("db-migrated events = %v, want one", ev)
	}
	var detail map[string]any
	if err := json.Unmarshal([]byte(ev[0]), &detail); err != nil {
		t.Fatal(err)
	}
	if p, has := detail["problems"]; has {
		t.Fatalf("problems = %v, want none", p)
	}
}

func TestDBLayoutMigrationRollbackSteps(t *testing.T) {
	e, legacy, newDB := legacyEnv(t)
	e.NeedPython()
	e.OK("tick")
	n := rowCounts(t, newDB)
	// docs/db-layout.md: (tick timer stopped) checkpoint, move the file back, drop the -wal/-shm leftovers
	pathExec(t, newDB, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := os.Rename(newDB, legacy); err != nil {
		t.Fatal(err)
	}
	for _, ext := range []string{"-wal", "-shm"} {
		if err := os.Remove(newDB + ext); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	dropTriggers(t, legacy)
	e.OK("config", "nudge_cooldown_s", "88")
	got := rowCounts(t, legacy)
	delete(got, "events")
	delete(n, "events")
	if !reflect.DeepEqual(got, n) {
		t.Fatalf("counts after rollback %v, want %v", got, n)
	}
	if v := pathInt(t, legacy, "SELECT value FROM config WHERE key='nudge_cooldown_s'"); v != 88 {
		t.Fatalf("nudge_cooldown_s = %d, want 88", v)
	}
	e.OK("goal", "list")
}

func TestDBLayoutMigrationRollbackFromMigratedFileDropsTriggers(t *testing.T) {
	e, legacy, _ := legacyEnv(t)
	e.NeedPython()
	e.OK("tick")
	old := migrated(t, legacy)[0]
	od := openPath(t, old)
	_, werr := od.Exec("INSERT INTO config VALUES('zz','1')")
	od.Close()
	if werr == nil {
		t.Fatal("the migrated file accepted a write while its triggers exist")
	}
	dropTriggers(t, old)
	pathExec(t, old, "INSERT INTO config VALUES('zz','1')")
}
