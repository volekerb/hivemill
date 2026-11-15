package retro

import (
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"factory/go/internal/scout"
)

type fakeAPI struct {
	clock    time.Time
	cfg      map[string]string
	spawnErr error
	closed   bool // gate closed: the spawned agent is queued
	spawned  []string
	stopped  []string
	stopRes  string
}

func newAPI() *fakeAPI {
	return &fakeAPI{clock: time.Date(2026, 11, 14, 7, 0, 0, 0, time.UTC), stopRes: "stopped",
		cfg: map[string]string{"retro_enabled": "1", "retro_hour": "6", "retro_max_minutes": "30"}}
}
func (a *fakeAPI) Clock() time.Time { return a.clock }
func (a *fakeAPI) Now() string      { return scout.Stamp(a.clock) }
func (a *fakeAPI) Cfg(_ *sql.DB, k string) (string, error) {
	return a.cfg[k], nil
}
func (a *fakeAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	_, err := db.Exec("INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)", scout.Stamp(a.clock), entity, id, action, detail)
	return err
}
func (a *fakeAPI) Spawn(db *sql.DB, role, name, org, model, prompt string) error {
	if a.spawnErr != nil {
		return a.spawnErr
	}
	a.spawned = append(a.spawned, strings.Join([]string{role, name, org, model, prompt}, "|"))
	st := "live"
	if a.closed {
		st = "queued"
	}
	_, err := db.Exec("INSERT OR REPLACE INTO agents(name, role, status, started) VALUES(?,?,?,?)", name, role, st, scout.Stamp(a.clock))
	return err
}
func (a *fakeAPI) ForceStop(db *sql.DB, name, reason string) (string, string, error) {
	a.stopped = append(a.stopped, name+":"+reason)
	return a.stopRes, "", nil
}

func run(t *testing.T, db *sql.DB, day string) (status, reason, started, ended sql.NullString) {
	t.Helper()
	if err := db.QueryRow("SELECT status, stop_reason, started, ended FROM retro_runs WHERE day=?", day).Scan(&status, &reason, &started, &ended); err != nil {
		t.Fatalf("no run for %s: %v", day, err)
	}
	return
}

func events(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query("SELECT action||':'||entity_id||':'||detail FROM events ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	return out
}

func TestTickStartsOnceFromRetroHour(t *testing.T) {
	db, api := newDB(t), newAPI()
	api.clock = time.Date(2026, 11, 14, 5, 59, 0, 0, time.UTC)
	if err := Tick(db, api); err != nil || len(api.spawned) != 0 {
		t.Fatalf("before retro_hour nothing starts: %v %v", err, api.spawned)
	}
	api.clock = time.Date(2026, 11, 14, 6, 0, 0, 0, time.UTC)
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	wantSpawn := "retro|cursor-retro|cursor|" + mustModel(t) + "|Scheduled daily retro for 2026-11-14. Run factory retro packet, investigate, then factory retro file."
	if len(api.spawned) != 1 || api.spawned[0] != wantSpawn {
		t.Fatalf("spawned %q", api.spawned)
	}
	if st, _, started, _ := run(t, db, "2026-11-14"); st.String != "running" || started.String != "2026-11-14T06:00:00+00:00" {
		t.Fatalf("%v %v", st, started)
	}
	if err := Tick(db, api); err != nil || len(api.spawned) != 1 { // idempotent within the day
		t.Fatalf("second tick spawned again: %v %v", err, api.spawned)
	}
	if got := events(t, db); len(got) != 1 || got[0] != "retro-day:2026-11-14:2026-11-14" {
		t.Fatalf("events %q", got)
	}
}

func mustModel(t *testing.T) string {
	t.Helper()
	m, err := scout.Model(Org, Role)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestTickDisabledAndClosedGate(t *testing.T) {
	db, api := newDB(t), newAPI()
	api.cfg["retro_enabled"] = "0"
	if err := Tick(db, api); err != nil || len(api.spawned) != 0 {
		t.Fatal("disabled must not start", err)
	}
	api.cfg["retro_enabled"], api.closed = "1", true
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if st, _, started, _ := run(t, db, "2026-11-14"); st.String != "queued" || started.Valid {
		t.Fatalf("closed gate must queue: %v %v", st, started)
	}
}

func TestTickWaitsForOtherQueuedAgents(t *testing.T) {
	db, api := newDB(t), newAPI()
	mustExec(t, db, "INSERT INTO agents(name, role, status) VALUES('c1','coder','queued')")
	if err := Tick(db, api); err != nil || len(api.spawned) != 0 {
		t.Fatalf("T-1135: no claim while others are queued: %v", err)
	}
	if err := db.QueryRow("SELECT 1 FROM retro_runs").Scan(new(int)); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("no run row expected: %v", err)
	}
}

func TestClaimSkipsWhileRetroAgentBusy(t *testing.T) {
	db, api := newDB(t), newAPI()
	mustExec(t, db, "INSERT INTO agents(name, role, status) VALUES('cursor-retro','retro','live')")
	st, err := Claim(db, api, "2026-11-14")
	if err != nil || st != "skipped" {
		t.Fatalf("%q %v", st, err)
	}
	if s, why, _, ended := run(t, db, "2026-11-14"); s.String != "skipped" || why.String != "previous run active" || !ended.Valid {
		t.Fatalf("%v %v %v", s, why, ended)
	}
	if st, err := Claim(db, api, "2026-11-14"); err != nil || st != "" {
		t.Fatalf("a recorded day is not claimed again: %q %v", st, err)
	}
	if got := events(t, db); got[0] != "retro-day:2026-11-14:2026-11-14 skipped: previous run active" {
		t.Fatalf("%q", got)
	}
}

func TestStartSpawnFailure(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{errors.New("boom"), "spawn failed: boom"},
		{&SpawnExit{Code: 3}, "spawn failed: exit 3; see the tick's stderr"},
	} {
		db, api := newDB(t), newAPI()
		api.spawnErr = c.err
		if err := Tick(db, api); err != nil {
			t.Fatal(err)
		}
		st, why, _, ended := run(t, db, "2026-11-14")
		if st.String != "failed" || why.String != c.want || !ended.Valid {
			t.Fatalf("%v %v %v", st, why, ended)
		}
		if err := Tick(db, api); err != nil { // the failed day is not retried
			t.Fatal(err)
		}
	}
}

func TestSyncStates(t *testing.T) {
	cases := []struct{ agent, wantStatus, wantReason string }{
		{"done", "done", ""}, {"exited", "done", ""}, {"stopped", "stopped", "stopped"}, {"lost", "failed", "agent lost"},
		{"cancelled", "skipped", "dequeued"}, {"queued", "running", ""}, {"starting", "running", ""},
	}
	for _, c := range cases {
		db, api := newDB(t), newAPI()
		api.cfg["retro_enabled"] = "0"
		mustExec(t, db, "INSERT INTO retro_runs(day, status, agent) VALUES('2026-11-14','running','cursor-retro')")
		mustExec(t, db, "INSERT INTO agents(name, role, status) VALUES('cursor-retro','retro',?)", c.agent)
		if err := Tick(db, api); err != nil {
			t.Fatal(err)
		}
		if st, why, _, _ := run(t, db, "2026-11-14"); st.String != c.wantStatus || why.String != c.wantReason {
			t.Errorf("%s: got %v/%v want %s/%q", c.agent, st, why, c.wantStatus, c.wantReason)
		}
	}
	// no agent row, and an older open run is superseded
	db, api := newDB(t), newAPI()
	api.cfg["retro_enabled"] = "0"
	mustExec(t, db, "INSERT INTO retro_runs(day, status) VALUES('2026-11-13','running'),('2026-11-14','queued')")
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if st, why, _, _ := run(t, db, "2026-11-13"); st.String != "failed" || why.String != "superseded" {
		t.Errorf("%v %v", st, why)
	}
	if st, why, _, _ := run(t, db, "2026-11-14"); st.String != "failed" || why.String != "retro agent row missing" {
		t.Errorf("%v %v", st, why)
	}
}

func TestBudgetWallStop(t *testing.T) {
	db, api := newDB(t), newAPI()
	api.cfg["retro_enabled"] = "0"
	mustExec(t, db, "INSERT INTO retro_runs(day, status, agent) VALUES('2026-11-14','running','cursor-retro')")
	mustExec(t, db, "INSERT INTO agents(name, role, status, started) VALUES('cursor-retro','retro','live',?)", "2026-11-14T06:31:00+00:00")
	if err := Tick(db, api); err != nil || len(api.stopped) != 0 { // 29 min of 30
		t.Fatalf("within budget: %v %v", err, api.stopped)
	}
	api.clock = time.Date(2026, 11, 14, 7, 2, 0, 0, time.UTC)
	if err := Tick(db, api); err != nil || len(api.stopped) != 1 || api.stopped[0] != "cursor-retro:budget" {
		t.Fatalf("over budget: %v %v", err, api.stopped)
	}
	if st, why, _, ended := run(t, db, "2026-11-14"); st.String != "stopped" || why.String != "wall" || !ended.Valid {
		t.Fatalf("%v %v %v", st, why, ended)
	}
	if got := events(t, db); len(got) != 1 || got[0] != "retro-budget:2026-11-14:wall" {
		t.Fatalf("%q", got)
	}
}

func TestBadConfigRecordedOncePerDayAndStopsLiveAgent(t *testing.T) {
	db, api := newDB(t), newAPI()
	api.cfg["retro_hour"] = "24"
	mustExec(t, db, "INSERT INTO retro_runs(day, status, agent) VALUES('2026-11-14','running','cursor-retro')")
	mustExec(t, db, "INSERT INTO agents(name, role, status, started) VALUES('cursor-retro','retro','live',?)", "2026-11-14T06:50:00+00:00")
	for i := 0; i < 2; i++ {
		if err := Tick(db, api); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.stopped) != 1 || api.stopped[0] != "cursor-retro:config" {
		t.Fatalf("stopped %v", api.stopped)
	}
	got := events(t, db)
	if len(got) != 2 || !strings.HasPrefix(got[0], "retro-failed:-:retro_hour must be an integer 0-23, got '24'") || got[1] != "retro-config:-:config" {
		t.Fatalf("events %q", got)
	}
	if st, why, _, _ := run(t, db, "2026-11-14"); st.String != "stopped" || why.String != "config" {
		t.Fatalf("%v %v", st, why)
	}
}

func TestClaimLeavesNoOpenTransaction(t *testing.T) {
	// Claim must leave the connection usable (no open transaction) after every outcome
	db, api := newDB(t), newAPI()
	for i := 0; i < 2; i++ {
		if _, err := Claim(db, api, "2026-11-14"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		t.Fatalf("a transaction was left open: %v", err)
	}
	if _, err := db.Exec("ROLLBACK"); err != nil {
		t.Fatal(err)
	}
}
