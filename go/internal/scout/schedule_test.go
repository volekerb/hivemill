package scout

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// schedAPI is the bin/factory side of the tick, faked at the process boundary: config, clock, spawn, messaging.
type schedAPI struct {
	fakeAPI
	clock      time.Time
	cfg        map[string]string
	tokens     map[string]*int64
	spawnErr   error
	gateClosed bool
	spawned    []string
	stopped    []string
	stopRes    string
	cancelled  bool
}

func newSched() *schedAPI {
	return &schedAPI{clock: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), tokens: map[string]*int64{}, stopRes: "stopped",
		cfg: map[string]string{"scout_enabled": "1", "scout_slots": "09,18", "scout_max_minutes": "60", "scout_max_tokens": "1000", "scout_queue_max_h": "6"}}
}
func (a *schedAPI) Clock() time.Time { return a.clock }
func (a *schedAPI) Now() string      { return Stamp(a.clock) }
func (a *schedAPI) Cfg(_ *sql.DB, k string) (string, error) {
	return a.cfg[k], nil
}
func (a *schedAPI) SessionTokens(s string) (*int64, error) { return a.tokens[s], nil }
func (a *schedAPI) ForceStop(db *sql.DB, name, reason string) (string, string, error) {
	a.stopped = append(a.stopped, name+":"+reason)
	return a.stopRes, "", nil
}
func (a *schedAPI) CancelQueued(*sql.DB, string, string, string) (bool, error) {
	return a.cancelled, nil
}
func (a *schedAPI) AgentName(*sql.DB) string { return "claude-scout" }
func (a *schedAPI) Spawn(db *sql.DB, role, name, org, prompt string) error {
	if a.spawnErr != nil {
		return a.spawnErr
	}
	a.spawned = append(a.spawned, role+"|"+name+"|"+org+"|"+prompt)
	st := "live"
	if a.gateClosed {
		st = "queued"
	}
	_, err := db.Exec("INSERT OR REPLACE INTO agents(name, role, status, session_id, queued_at, started) VALUES(?,?,?,?,?,?)",
		name, role, st, "sess-1", Stamp(a.clock), Stamp(a.clock))
	return err
}
func (a *schedAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	_, err := db.Exec("INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)", Stamp(a.clock), entity, id, action, detail)
	return err
}

func runRow(t *testing.T, db *sql.DB, slot string) (status, reason string, started, ended sql.NullString, newC sql.NullInt64) {
	t.Helper()
	var r sql.NullString
	if err := db.QueryRow("SELECT status, stop_reason, started, ended, new_candidates FROM scout_runs WHERE slot=?", slot).
		Scan(&status, &r, &started, &ended, &newC); err != nil {
		t.Fatalf("run %s: %v", slot, err)
	}
	return status, r.String, started, ended, newC
}

func TestParseSlots(t *testing.T) {
	got, err := ParseSlots("18, 09,9")
	if err != nil || !reflect.DeepEqual(got, []int{9, 18}) {
		t.Fatalf("got %v %v", got, err)
	}
	for _, bad := range []string{"", "24", "-1", "x", "9,,y"} {
		if _, err := ParseSlots(bad); err == nil || !strings.Contains(err.Error(), "scout_slots") {
			t.Errorf("ParseSlots(%q) = %v, want an error naming scout_slots", bad, err)
		}
	}
}

func TestCurrentSlot(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 14, h, 30, 0, 0, time.UTC) }
	for _, c := range []struct {
		h    int
		want string
	}{{8, "2026-11-13T18"}, {9, "2026-11-14T09"}, {17, "2026-11-14T09"}, {23, "2026-11-14T18"}} {
		if got := CurrentSlot(at(c.h), []int{9, 18}); got != c.want {
			t.Errorf("hour %d: %s want %s", c.h, got, c.want)
		}
	}
}

func TestTickClaimsSpawnsOncePerSlot(t *testing.T) {
	db, api := newDB(t), newSched()
	for i := 0; i < 2; i++ {
		if err := Tick(db, api); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.spawned) != 1 || api.spawned[0] != "scout|claude-scout|claude|Scheduled scout run, slot 2026-11-14T09." {
		t.Fatalf("spawned %v", api.spawned)
	}
	if st, _, started, _, _ := runRow(t, db, "2026-11-14T09"); st != "running" || !started.Valid {
		t.Errorf("status %s started %v", st, started)
	}
}

func TestTickDisabledOnlySyncs(t *testing.T) {
	db, api := newDB(t), newSched()
	api.cfg["scout_enabled"] = "0"
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM scout_runs").Scan(&n)
	if n != 0 || len(api.spawned) != 0 {
		t.Errorf("disabled tick made %d runs, %d spawns", n, len(api.spawned))
	}
}

func TestTickBadSlotsConfigIsAnError(t *testing.T) {
	db, api := newDB(t), newSched()
	api.cfg["scout_slots"] = "oops"
	if err := Tick(db, api); err == nil || !strings.Contains(err.Error(), "scout_slots") {
		t.Fatalf("err %v", err)
	}
}

func TestClaimSkipsWhileAgentBusy(t *testing.T) {
	db, api := newDB(t), newSched()
	mustExec(t, db, "INSERT INTO agents(name, role, status) VALUES('claude-scout','scout','live')")
	got, err := Claim(db, api, "2026-11-14T09")
	if err != nil || got != "skipped" {
		t.Fatalf("claim %q %v", got, err)
	}
	st, reason, _, ended, _ := runRow(t, db, "2026-11-14T09")
	if st != "skipped" || reason != "previous run active" || !ended.Valid {
		t.Errorf("row %s %s %v", st, reason, ended)
	}
	if again, err := Claim(db, api, "2026-11-14T09"); err != nil || again != "" {
		t.Errorf("second claim %q %v, want none", again, err)
	}
}

func TestStartRecordsSpawnFailure(t *testing.T) {
	db, api := newDB(t), newSched()
	api.spawnErr = errors.New("gate refused")
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	st, reason, _, ended, _ := runRow(t, db, "2026-11-14T09")
	if st != "failed" || reason != "spawn failed: gate refused" || !ended.Valid {
		t.Errorf("row %s %q", st, reason)
	}
	var n int
	db.QueryRow("SELECT COUNT(*) FROM events WHERE action='scout-spawn-failed'").Scan(&n)
	if n != 1 {
		t.Errorf("%d spawn-failed events", n)
	}
}

func TestStartQueuedWhenGateClosed(t *testing.T) {
	db, api := newDB(t), newSched()
	api.gateClosed = true
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if st, _, started, _, _ := runRow(t, db, "2026-11-14T09"); st != "queued" || started.Valid {
		t.Errorf("status %s started %v", st, started)
	}
	api.clock = api.clock.Add(7 * time.Hour) // over scout_queue_max_h; the dequeue cancels it
	api.cancelled = true
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if st, reason, _, _, _ := runRow(t, db, "2026-11-14T09"); st != "skipped" || reason != "gate closed > 6h" {
		t.Errorf("status %s %q", st, reason)
	}
}

func TestSyncFinishesAndCounts(t *testing.T) {
	db, api := newDB(t), newSched()
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	// a candidate and a triage row land inside the run window; one candidate outside
	mustExec(t, db, "INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen) VALUES('C-0001','gh:a/b','[]','2026-11-14T10:05:00+00:00','2026-11-14T10:05:00+00:00')")
	mustExec(t, db, "INSERT INTO scout_candidates(id,key,sources,first_seen,last_seen) VALUES('C-0002','gh:a/c','[]','2026-11-13T10:05:00+00:00','2026-11-13T10:05:00+00:00')")
	mustExec(t, db, "INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created) VALUES('C-0001','p','none','o','watch','r','2026-11-14T10:06:00+00:00')")
	mustExec(t, db, "UPDATE agents SET status='done'")
	api.clock = api.clock.Add(30 * time.Minute)
	n := int64(42)
	api.tokens["sess-1"] = &n
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	st, _, _, ended, newC := runRow(t, db, "2026-11-14T09")
	if st != "done" || !ended.Valid || newC.Int64 != 1 {
		t.Errorf("status %s ended %v new %v", st, ended, newC)
	}
	var tok, tri sql.NullInt64
	db.QueryRow("SELECT tokens, triaged FROM scout_runs").Scan(&tok, &tri)
	if tok.Int64 != 42 || tri.Int64 != 1 {
		t.Errorf("tokens %v triaged %v", tok, tri)
	}
	ids, err := SendPending(db, api)
	if err != nil || len(ids) != 1 || api.sent[0]["to"] != "director" {
		t.Errorf("report: %v %v %v", ids, err, api.sent)
	}
}

func TestSyncWallBudgetStopsLiveAgent(t *testing.T) {
	db, api := newDB(t), newSched()
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	api.clock = api.clock.Add(61 * time.Minute)
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.stopped, []string{"claude-scout:budget"}) {
		t.Fatalf("stopped %v", api.stopped)
	}
	if st, reason, _, _, _ := runRow(t, db, "2026-11-14T09"); st != "stopped" || reason != "wall" {
		t.Errorf("status %s %q", st, reason)
	}
}

func TestSyncTokenBudget(t *testing.T) {
	db, api := newDB(t), newSched()
	if err := Tick(db, api); err != nil {
		t.Fatal(err)
	}
	over := int64(5000)
	api.tokens["sess-1"] = &over
	api.stopRes = "pending" // the stop is requested but not yet confirmed: the run stays open with its reason
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if st, reason, _, _, _ := runRow(t, db, "2026-11-14T09"); st != "running" || reason != "tokens" {
		t.Errorf("status %s %q", st, reason)
	}
	if err := Sync(db, api, api.clock); err != nil || len(api.stopped) != 1 {
		t.Errorf("a stop in progress must not be requested again: %v %v", err, api.stopped)
	}
}

func TestSyncStaleClaimAndSuperseded(t *testing.T) {
	db, api := newDB(t), newSched()
	mustExec(t, db, "INSERT INTO scout_runs(slot,status) VALUES('2026-11-13T18','running')")
	mustExec(t, db, "INSERT INTO scout_runs(slot,status) VALUES('2026-11-14T09','claimed')")
	mustExec(t, db, "INSERT INTO events(ts,entity,entity_id,action) VALUES(?, 'scout','2026-11-14T09','scout-slot')", Stamp(api.clock.Add(-11*time.Minute)))
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if st, reason, _, _, newC := runRow(t, db, "2026-11-13T18"); st != "failed" || reason != Superseded || !newC.Valid {
		t.Errorf("old run %s %q %v (a superseded run is counted, window ends at its own end)", st, reason, newC)
	}
	if st, reason, _, _, newC := runRow(t, db, "2026-11-14T09"); st != "failed" || reason != "claimed but never spawned" || newC.Valid {
		t.Errorf("claimed run %s %q %v (a never-spawned run keeps NULL counts)", st, reason, newC)
	}
}

func TestSyncFreshClaimIsLeftAlone(t *testing.T) {
	db, api := newDB(t), newSched()
	mustExec(t, db, "INSERT INTO scout_runs(slot,status) VALUES('2026-11-14T09','claimed')")
	mustExec(t, db, "INSERT INTO events(ts,entity,entity_id,action) VALUES(?, 'scout','2026-11-14T09','scout-slot')", Stamp(api.clock.Add(-time.Minute)))
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if st, _, _, _, _ := runRow(t, db, "2026-11-14T09"); st != "claimed" {
		t.Errorf("status %s", st)
	}
}

func TestSyncMissingAgentAndTerminalAgentStates(t *testing.T) {
	for agentStatus, want := range map[string][2]string{
		"lost": {"failed", "agent lost"}, "stopped": {"stopped", "stopped"}, "cancelled": {"skipped", "dequeued"}, "exited": {"done", ""},
	} {
		db, api := newDB(t), newSched()
		mustExec(t, db, "INSERT INTO scout_runs(slot,status) VALUES('2026-11-14T09','running')")
		mustExec(t, db, "INSERT INTO agents(name,role,status) VALUES('claude-scout','scout',?)", agentStatus)
		if err := Sync(db, api, api.clock); err != nil {
			t.Fatal(err)
		}
		if st, reason, _, _, _ := runRow(t, db, "2026-11-14T09"); st != want[0] || reason != want[1] {
			t.Errorf("agent %s: run %s %q, want %v", agentStatus, st, reason, want)
		}
	}
	db, api := newDB(t), newSched()
	mustExec(t, db, "INSERT INTO scout_runs(slot,status) VALUES('2026-11-14T09','running')")
	if err := Sync(db, api, api.clock); err != nil {
		t.Fatal(err)
	}
	if st, reason, _, _, _ := runRow(t, db, "2026-11-14T09"); st != "failed" || reason != "scout agent row missing" {
		t.Errorf("run %s %q", st, reason)
	}
}
