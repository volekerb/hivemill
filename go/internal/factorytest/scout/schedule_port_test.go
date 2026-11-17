package scout

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	ft "factory/go/internal/factorytest"
	sc "factory/go/internal/scout"
)

// Port of tests/test_scout_schedule.py (29 cases; see ported_scout.txt): the tick starts the skill scout once per
// slot and keeps it within its budgets. The clock is $FACTORY_NOW (TZ=UTC), herdr is the stateful stub and no real
// Claude runs. `tick`, `config`, `scout runs`, `inbox` and `done` are still handed to bin/factory by the Go CLI, so
// those cases call NeedPython and skip where python3 is absent.
// The Python cases that mock bin/factory internals (load_factory + mock.patch of f.spawn / f.force_stop, two sqlite
// connections racing f.scout_tick) are ported against internal/scout with a ScheduleAPI fake that has the same
// effect: the race (Tick on two connections: one spawn), the refused spawn (Spawn returns an error: failed, no
// retry), and the budget stop reason (ForceStop is called with "budget"; a failed stop keeps the budget reason).
// not ported: the DEFAULT_CONFIG["scout_enabled"] lookup through load_factory is read from the source line of
// bin/factory_cli.py instead (the Python dict itself is not loadable from Go).

const (
	rpSD    = "2026-10-26"
	rpPrior = "2026-10-25"
)

func rpNowEnv(now string) map[string]string {
	return map[string]string{"FACTORY_NOW": now, "TZ": "UTC"}
}

type rpSch struct {
	t     *testing.T
	e     *ft.Env
	scout string // the scout's agents row name
}

func rpSchNew(t *testing.T) *rpSch {
	rpPar(t)
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	rpPinTUI(t, e)
	b := &rpSch{t: t, e: e, scout: "scout"}
	b.ok("config", "scout_enabled", "1")
	b.ok("config", "retro_enabled", "0") // these tests own scout_tick, not retro_tick
	return b
}

func (b *rpSch) ok(args ...string) ft.Result { b.t.Helper(); return rpOK(b.t, b.e, "", nil, args...) }
func (b *rpSch) okAs(agent string, args ...string) ft.Result {
	b.t.Helper()
	return rpOK(b.t, b.e, agent, nil, args...)
}

func (b *rpSch) tick(now string) ft.Result {
	b.t.Helper()
	r := rpRun(b.e, "", rpNowEnv(now), "tick")
	if r.Exit != 0 {
		b.t.Fatalf("tick %s: exit %d\n%s\n%s", now, r.Exit, r.Stdout, r.Stderr)
	}
	return r
}

func (b *rpSch) sql(q string, args ...any) { b.t.Helper(); rpExec(b.t, b.e, q, args...) }

func (b *rpSch) runs() []map[string]any {
	return rpRows(b.t, b.e, "SELECT * FROM scout_runs ORDER BY id")
}

func (b *rpSch) starts() [][]string {
	var out [][]string
	for _, c := range b.e.Calls("herdr") {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			out = append(out, c.Argv)
		}
	}
	return out
}

// startNames is [a[2] for a in starts()].
func (b *rpSch) startNames() []string {
	var out []string
	for _, a := range b.starts() {
		out = append(out, a[2])
	}
	return out
}

func (b *rpSch) agent() map[string]any {
	b.t.Helper()
	rows := rpRows(b.t, b.e, "SELECT * FROM agents WHERE name=?", b.scout)
	if len(rows) != 1 {
		b.t.Fatalf("agents row %s: %v", b.scout, rows)
	}
	return rows[0]
}

// pin moves the stamps onto $FACTORY_NOW, written with the explicit +00:00 the Python run (TZ=UTC) means by a naive stamp:
// internal/scout reads a naive stamp in the zone of the test process, which is not UTC on every box.
func (b *rpSch) pin(started, queued string) {
	b.t.Helper()
	if started != "" {
		b.sql("UPDATE agents SET started=? WHERE name=?", started+"+00:00", b.scout)
	}
	if queued != "" {
		b.sql("UPDATE agents SET queued_at=? WHERE name=?", queued+"+00:00", b.scout)
	}
}

func (b *rpSch) finish() {
	b.t.Helper()
	b.okAs(b.scout, "inbox") // factory done refuses while orders are unread
	b.okAs(b.scout, "done", "scanned")
}

func (b *rpSch) events(action string) []map[string]any {
	return rpRows(b.t, b.e, "SELECT * FROM events WHERE action=?", action)
}

func (b *rpSch) usageLog(session string, in, out, cacheWrite, cacheRead int) {
	b.t.Helper()
	d := filepath.Join(b.e.UserHome, ".claude", "projects", "-zz-proj")
	if err := os.MkdirAll(d, 0o755); err != nil {
		b.t.Fatal(err)
	}
	line, err := json.Marshal(map[string]any{"type": "assistant", "uuid": "u1", "requestId": "r1", "timestamp": "2026-10-26T09:05:00Z",
		"cwd": "/zz", "message": map[string]any{"id": "m1", "model": "claude-sonnet-5-5", "usage": map[string]any{
			"input_tokens": in, "output_tokens": out, "cache_creation_input_tokens": cacheWrite, "cache_read_input_tokens": cacheRead}}})
	if err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, session+".jsonl"), append(line, '\n'), 0o644); err != nil {
		b.t.Fatal(err)
	}
}

func (b *rpSch) spawned() map[string]any {
	b.t.Helper()
	b.tick(rpAt("09:00"))
	b.pin(rpAt("09:00"), "")
	return b.agent()
}

func (b *rpSch) cand(n int, firstSeen string) {
	b.t.Helper()
	b.sql("INSERT INTO scout_candidates(id,key,first_seen,last_seen) VALUES(?,?,?,?)", fmt.Sprintf("C-%04d", n), fmt.Sprintf("gh:o/r%d", n), firstSeen, firstSeen)
}

func (b *rpSch) triage(cid, created string) {
	b.t.Helper()
	b.sql("INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created) VALUES(?,?,?,?,?,?,?)",
		cid, "p", "coder", "none", "watch", "r", created)
}

// ---- a ScheduleAPI fake over the home's database (the Go form of load_factory + mock.patch) ---------------------

type rpAPI struct {
	clock    time.Time
	home     string // FACTORY_HOME's user home: the usage logs
	mu       sync.Mutex
	spawns   int32
	spawnErr error
	stopFn   func(db *sql.DB, name, reason string) (string, string, error)
	stops    []string
	tokens   func(session string) *int64
}

func rpClock(t testing.TB, now string) time.Time {
	t.Helper()
	c, err := time.Parse("2006-01-02T15:04:05", now) // $FACTORY_NOW is naive: local time, and TZ=UTC
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (a *rpAPI) Clock() time.Time { return a.clock }
func (a *rpAPI) Now() string      { return sc.Stamp(a.clock) }
func (a *rpAPI) Send(db *sql.DB, to, subject, body, kind, sender string) (int64, error) {
	res, err := db.Exec("INSERT INTO messages(sender,recipient,kind,subject,body,created) VALUES(?,?,?,?,?,?)", sender, to, kind, subject, body, sc.Stamp(a.clock))
	if err != nil {
		return 0, fmt.Errorf("send: %w", err)
	}
	return res.LastInsertId()
}
func (a *rpAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	if _, err := db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)", sc.Stamp(a.clock), "tick", entity, id, action, detail); err != nil {
		return fmt.Errorf("event %s: %w", action, err)
	}
	return nil
}
func (a *rpAPI) Cfg(db *sql.DB, key string) (string, error) {
	var v string
	if err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v); err != nil {
		return "", fmt.Errorf("config %s: %w", key, err)
	}
	return v, nil
}
func (a *rpAPI) SessionTokens(session string) (*int64, error) {
	if a.tokens != nil {
		return a.tokens(session), nil
	}
	return nil, nil
}
func (a *rpAPI) ForceStop(db *sql.DB, name, reason string) (string, string, error) {
	a.mu.Lock()
	a.stops = append(a.stops, name+":"+reason)
	a.mu.Unlock()
	return a.stopFn(db, name, reason)
}
func (a *rpAPI) CancelQueued(*sql.DB, string, string, string) (bool, error) { return false, nil }
func (a *rpAPI) Spawn(db *sql.DB, role, name, org, prompt string) error {
	atomic.AddInt32(&a.spawns, 1)
	if a.spawnErr != nil {
		return a.spawnErr
	}
	if _, err := db.Exec("INSERT INTO agents(name,role,status,started) VALUES(?,?,'live',?)", name, role, sc.Stamp(a.clock)); err != nil {
		return fmt.Errorf("spawn %s: %w", name, err)
	}
	return nil
}
func (a *rpAPI) AgentName(*sql.DB) string { return "" }

func rpOpenDB(t testing.TB, e *ft.Env) *factorydb.DB {
	t.Helper()
	d, err := factorydb.Open(e.DBPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

// ---- SlotTests ------------------------------------------------------------------------------------------------

func TestScoutScheduleFirstTickSpawnsOnceAndSecondTickInTheSlotDoesNothing(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	eqStrings(t, "starts", b.startNames(), []string{"scout"})
	runs := b.runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	run := runs[0]
	rpEq(t, "slot/status/agent", []string{rpStr(run["slot"]), rpStr(run["status"]), rpStr(run["agent"])}, []string{rpSD + "T09", "running", "scout"})
	rpEq(t, "started", rpStr(run["started"])[:16], rpAt("09:01")[:16])
	rpEq(t, "role", b.agent()["role"], "scout")
	ev := b.events("scout-slot")
	if len(ev) != 1 || rpStr(ev[0]["detail"]) != rpSD+"T09" {
		t.Fatalf("scout-slot events = %v", ev)
	}
	b.pin(rpAt("09:01"), "")
	b.tick(rpAt("09:02"))
	if len(b.starts()) != 1 || len(b.runs()) != 1 {
		t.Fatalf("starts = %d runs = %d, want 1 and 1", len(b.starts()), len(b.runs()))
	}
	out := b.okAs("owner", "scout", "runs").Stdout
	contains(t, "scout runs", out, rpSD+"T09 running")
	contains(t, "scout runs", out, "started=")
}

func TestScoutScheduleTwoConnectionsRacingForOneSlotSpawnOnce(t *testing.T) {
	e := ft.New(t) // the scheduling library only: no Python needed
	if _, err := e.RawDB().Exec("UPDATE config SET value='1' WHERE key='scout_enabled'"); err != nil {
		t.Fatal(err)
	}
	api := &rpAPI{clock: rpClock(t, rpAt("09:01"))}
	dbs := []*factorydb.DB{rpOpenDB(t, e), rpOpenDB(t, e)} // two connections of their own
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, len(dbs))
	for i, d := range dbs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs[i] = sc.Tick(d.DB, api)
		}()
	}
	close(start)
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatalf("Tick: %v", err)
		}
	}
	if n := atomic.LoadInt32(&api.spawns); n != 1 {
		t.Fatalf("spawns = %d, want 1", n)
	}
	runs := rpRows(t, e, "SELECT slot, status FROM scout_runs")
	if len(runs) != 1 || rpStr(runs[0]["slot"]) != rpSD+"T09" || rpStr(runs[0]["status"]) != "running" {
		t.Fatalf("runs = %v", runs)
	}
	if n := e.Count("SELECT count(*) FROM events WHERE action='scout-slot'"); n != 1 {
		t.Fatalf("scout-slot events = %d, want 1", n)
	}
}

func TestScoutScheduleGateClosedQueuesThenTheNormalDequeueStartsIt(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "max_live_agents", "0")
	b.tick(rpAt("09:01"))
	if len(b.starts()) != 0 {
		t.Fatalf("starts = %v", b.starts())
	}
	rpEq(t, "agent status", b.agent()["status"], "queued")
	if r := b.runs(); len(r) != 1 || rpStr(r[0]["status"]) != "queued" {
		t.Fatalf("runs = %v", r)
	}
	b.pin("", rpAt("09:01"))
	b.ok("config", "max_live_agents", "6")
	b.tick(rpAt("09:02")) // the existing tick dequeue starts it
	eqStrings(t, "starts", b.startNames(), []string{"scout"})
	rpEq(t, "agent status", b.agent()["status"], "live")
	b.pin(rpAt("09:02"), "")
	b.tick(rpAt("09:03")) // ...and the next tick's sync sees it live
	r := b.runs()
	if len(r) != 1 || rpStr(r[0]["slot"]) != rpSD+"T09" || rpStr(r[0]["status"]) != "running" || len(b.starts()) != 1 {
		t.Fatalf("runs = %v starts = %d", r, len(b.starts()))
	}
}

func TestScoutScheduleScoutStillLiveAtTheNextSlotRecordsASkippedSlot(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.pin(rpAt("17:55"), "") // inside its budget: only a stop that failed leaves a scout live this long
	b.tick(rpAt("18:00"))
	if len(b.starts()) != 1 {
		t.Fatalf("starts = %d, want 1", len(b.starts()))
	}
	rows := map[string]map[string]any{}
	for _, r := range b.runs() {
		rows[rpStr(r["slot"])] = r
	}
	rpEq(t, "T18 status", rows[rpSD+"T18"]["status"], "skipped")
	rpEq(t, "T18 stop_reason", rows[rpSD+"T18"]["stop_reason"], "previous run active")
	rpEq(t, "T09 status", rows[rpSD+"T09"]["status"], "running")
}

func TestScoutScheduleABoxThatWasDownRunsOnlyTheLatestSlot(t *testing.T) {
	b := rpSchNew(t)
	b.sql("INSERT INTO scout_runs(slot, agent, status, started, ended) VALUES(?, 'scout', 'done', ?, ?)",
		rpPrior+"T18", rpAt("18:01", rpPrior), rpAt("18:09", rpPrior))
	b.tick(rpAt("20:00"))
	var slots []string
	for _, r := range b.runs() {
		slots = append(slots, rpStr(r["slot"]))
	}
	if strings.Join(sortedCopy(slots), ",") != rpPrior+"T18,"+rpSD+"T18" || len(b.starts()) != 1 {
		t.Fatalf("slots = %v starts = %d", slots, len(b.starts()))
	}
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func TestScoutScheduleBeforeTheFirstSlotOfTheDayTheSlotIsYesterdaysLast(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("08:30"))
	r := b.runs()
	if len(r) != 1 || rpStr(r[0]["slot"]) != rpPrior+"T18" {
		t.Fatalf("runs = %v", r)
	}
}

func TestScoutScheduleQueuedLongerThanTheLimitIsCancelledAndSkipped(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "max_live_agents", "0")
	b.tick(rpAt("09:01"))
	b.pin("", rpAt("09:01"))
	b.tick(rpAt("14:59")) // 5 h 58 min: still waiting
	rpEq(t, "agent status", b.agent()["status"], "queued")
	b.tick(rpAt("15:02"))
	rpEq(t, "agent status", b.agent()["status"], "cancelled")
	r := b.runs()
	if len(r) != 1 || rpStr(r[0]["status"]) != "skipped" || rpStr(r[0]["stop_reason"]) != "gate closed > 6h" {
		t.Fatalf("runs = %v", r)
	}
	if len(b.starts()) != 0 || len(b.events("dequeued")) != 1 {
		t.Fatalf("starts = %v dequeued = %v", b.starts(), b.events("dequeued"))
	}
}

func TestScoutScheduleDisabledDoesNothing(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "scout_enabled", "0")
	b.tick(rpAt("09:01"))
	if len(b.runs()) != 0 || len(b.starts()) != 0 {
		t.Fatalf("runs = %v starts = %v", b.runs(), b.starts())
	}
}

func TestScoutScheduleHerdrUnreachableStartsNothing(t *testing.T) {
	b := rpSchNew(t)
	b.e.HerdrFail(true)
	b.tick(rpAt("09:01"))
	if len(b.runs()) != 0 {
		t.Fatalf("runs = %v", b.runs())
	}
}

func TestScoutScheduleAFinishedScoutEndsItsRun(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.pin(rpAt("09:01"), "")
	b.finish()
	b.tick(rpAt("09:20"))
	r := b.runs()[0]
	rpEq(t, "status", r["status"], "done")
	rpEq(t, "ended", rpStr(r["ended"])[:16], rpAt("09:20")[:16])
}

func TestScoutScheduleAFinishedRunStoresNewAndTriagedCountsOfItsWindow(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.pin(rpAt("09:01"), "")
	b.sql("UPDATE scout_runs SET started=?", rpAt("09:01")+"+01:00")
	b.cand(1, rpAt("08:00")+"+01:00") // before the window
	for _, c := range []struct {
		n int
		t string
	}{{2, rpAt("09:05") + "+01:00"}, {3, rpAt("09:10") + "+01:00"}, {5, rpAt("09:12") + "+01:00"}, {4, "2026-10-26T08:15:00-02:00"}} { // 10:15Z, after the window
		b.cand(c.n, c.t)
	}
	for _, c := range [][2]string{{"C-0002", rpAt("09:06") + "+01:00"}, {"C-0003", rpAt("09:07") + "+01:00"}, {"C-0001", rpAt("08:30") + "+01:00"}} { // 2 inside the window
		b.triage(c[0], c[1])
	}
	b.finish()
	b.tick(rpAt("09:20"))
	r := b.runs()[0]
	if rpStr(r["status"]) != "done" || rpStr(r["new_candidates"]) != "3" || rpStr(r["triaged"]) != "2" {
		t.Fatalf("run = %v", r)
	}
	body := rpRows(t, b.e, "SELECT body FROM messages WHERE subject LIKE 'skill scout%'")
	if len(body) == 0 {
		t.Fatal("no skill scout message")
	}
	contains(t, "body", rpStr(body[0]["body"]), "This run: 3 new candidates, 2 triaged.")
}

func TestScoutScheduleTheWindowOpensAtTheAgentsStartWhenTheRunWasStampedLate(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.pin(rpAt("09:01"), "")
	b.sql("UPDATE scout_runs SET started=?", rpAt("09:30")+"+01:00") // queued, dequeued and stamped late
	b.cand(1, rpAt("09:10")+"+01:00")                                // inside [agent started, ended], before the run's own started
	b.sql("UPDATE agents SET started=? WHERE name='scout'", rpAt("09:01")+"+01:00")
	b.finish()
	b.tick(rpAt("09:40"))
	r := b.runs()[0]
	if rpStr(r["status"]) != "done" || rpStr(r["new_candidates"]) != "1" {
		t.Fatalf("run = %v", r)
	}
}

func TestScoutScheduleASupersededRunWithoutItsOwnStartIgnoresAnOlderScoutAgentsStart(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.sql("UPDATE agents SET started=? WHERE name='scout'", rpAt("01:00", rpPrior))                 // an earlier scout run's start
	b.sql("UPDATE scout_runs SET started=NULL")                                                     // this run never got its own start
	b.sql("INSERT INTO scout_runs(slot,agent,status) VALUES(?,?,?)", rpSD+"T10", "scout", "queued") // supersedes it
	b.cand(1, rpAt("02:00", rpPrior))                                                               // after that agent started, before this run existed
	b.tick(rpAt("09:05"))
	old := b.runs()[0]
	if rpStr(old["status"]) != "failed" || rpStr(old["stop_reason"]) != "superseded by a newer run" || old["started"] != nil {
		t.Fatalf("old = %v", old)
	}
	if rpStr(old["new_candidates"]) != "0" || rpStr(old["triaged"]) != "0" {
		t.Fatalf("old = %v", old)
	}
}

func TestScoutScheduleAnAgentStartedBeforeTheRunsSlotEventIsIgnored(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.sql("UPDATE agents SET started=? WHERE name='scout'", rpAt("01:00", rpPrior)+"+01:00") // an earlier run's start
	b.sql("UPDATE scout_runs SET started=?", rpAt("09:01")+"+01:00")
	b.sql("UPDATE events SET ts=? WHERE action='scout-slot'", rpAt("09:00")+"+01:00") // the events use the real clock
	b.cand(1, rpAt("02:00", rpPrior)+"+01:00")                                        // after that agent started, before this run's slot event
	b.finish()
	b.tick(rpAt("09:20"))
	r := b.runs()[0]
	if rpStr(r["status"]) != "done" || rpStr(r["new_candidates"]) != "0" {
		t.Fatalf("run = %v", r)
	}
}

func TestScoutScheduleWithoutASlotEventTheAgentsEarlierStartStillOpensTheWindow(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.sql("UPDATE agents SET started=? WHERE name='scout'", rpAt("01:00", rpPrior)+"+01:00")
	b.sql("UPDATE scout_runs SET started=?", rpAt("09:01")+"+01:00")
	b.sql("DELETE FROM events WHERE action='scout-slot'")
	b.cand(1, rpAt("02:00", rpPrior)+"+01:00")
	b.finish()
	b.tick(rpAt("09:20"))
	r := b.runs()[0]
	if rpStr(r["status"]) != "done" || rpStr(r["new_candidates"]) != "1" {
		t.Fatalf("run = %v", r)
	}
}

func TestScoutScheduleAFailedRunThatWasNeverSpawnedKeepsNullCounts(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "max_live_agents", "0")
	b.tick(rpAt("09:01"))
	b.sql("UPDATE scout_runs SET status='claimed', started=NULL, ended=NULL, stop_reason=NULL")
	b.sql("DELETE FROM events WHERE action='scout-slot'")
	b.tick(rpAt("09:20")) // the claim is stale and never spawned
	runs := b.runs()
	if len(runs) != 1 {
		t.Fatalf("runs = %v", runs)
	}
	r := runs[0]
	if rpStr(r["status"]) != "failed" || rpStr(r["stop_reason"]) != "claimed but never spawned" {
		t.Fatalf("run = %v", r)
	}
	if r["new_candidates"] != nil || r["triaged"] != nil {
		t.Fatalf("counts = %v %v, want NULL NULL", r["new_candidates"], r["triaged"])
	}
}

func TestScoutScheduleScoutRunsCLIPrintsTheStoredValues(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.sql("UPDATE scout_runs SET status='done', new_candidates=7, triaged=4, tokens=1234, started=?, ended=?", rpAt("09:01"), rpAt("09:20"))
	contains(t, "scout runs", b.ok("scout", "runs").Stdout, "new=7 triaged=4 tokens=1234")
}

func TestScoutScheduleASkippedRunKeepsNullCounts(t *testing.T) {
	b := rpSchNew(t)
	b.tick(rpAt("09:01"))
	b.pin(rpAt("17:55"), "")
	b.tick(rpAt("18:00"))
	var skipped []map[string]any
	for _, r := range b.runs() {
		if rpStr(r["status"]) == "skipped" {
			skipped = append(skipped, r)
		}
	}
	if len(skipped) != 1 || skipped[0]["new_candidates"] != nil || skipped[0]["triaged"] != nil {
		t.Fatalf("skipped = %v", skipped)
	}
}

func TestScoutScheduleARefusedSpawnFailsTheSlotWithoutARetry(t *testing.T) {
	e := ft.New(t) // the scheduling library only: Spawn is the ScheduleAPI fake that refuses (Python: f.spawn raising SystemExit(2))
	if _, err := e.RawDB().Exec("UPDATE config SET value='1' WHERE key='scout_enabled'"); err != nil {
		t.Fatal(err)
	}
	api := &rpAPI{clock: rpClock(t, rpAt("09:01")), spawnErr: fmt.Errorf("exit 2")}
	d := rpOpenDB(t, e)
	for i := 0; i < 2; i++ {
		if err := sc.Tick(d.DB, api); err != nil {
			t.Fatalf("Tick %d: %v", i, err)
		}
	}
	runs := rpRows(t, e, "SELECT * FROM scout_runs")
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "failed" {
		t.Fatalf("runs = %v", runs)
	}
	contains(t, "stop_reason", rpStr(runs[0]["stop_reason"]), "spawn failed")
	if n := atomic.LoadInt32(&api.spawns); n != 1 {
		t.Fatalf("spawn calls = %d, want 1", n)
	}
}

// ---- BudgetTests ----------------------------------------------------------------------------------------------

func TestScoutScheduleWallBudgetExceededForceStopsTheScout(t *testing.T) {
	b := rpSchNew(t)
	b.spawned()
	b.tick(rpAt("09:19")) // 19 min: within 20
	rpEq(t, "status", b.runs()[0]["status"], "running")
	b.tick(rpAt("09:21"))
	rpEq(t, "agent status", b.agent()["status"], "stopped")
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != "wall" || runs[0]["ended"] == nil {
		t.Fatalf("runs = %v", runs)
	}
	var reasons []string
	for _, e := range b.events("stopped") {
		var d map[string]any
		if err := json.Unmarshal([]byte(rpStr(e["detail"])), &d); err != nil {
			t.Fatal(err)
		}
		reasons = append(reasons, rpStr(d["reason"]))
	}
	eqStrings(t, "stopped reasons", reasons, []string{"budget"})
}

func TestScoutScheduleForceStopIsCalledWithReasonBudget(t *testing.T) {
	b := rpSchNew(t)
	b.spawned()
	api := &rpAPI{clock: rpClock(t, rpAt("09:21")), stopFn: func(db *sql.DB, name, reason string) (string, string, error) {
		if _, err := db.Exec("UPDATE agents SET status='stopped' WHERE name=?", name); err != nil {
			return "", "", err
		}
		return "stopped", "", nil
	}}
	if err := sc.Tick(rpOpenDB(t, b.e).DB, api); err != nil {
		t.Fatal(err)
	}
	eqStrings(t, "force stops", api.stops, []string{"scout:budget"})
	rpEq(t, "run status", b.runs()[0]["status"], "stopped")
}

func TestScoutScheduleTokenBudgetExceededForceStopsAndStoresTheTokens(t *testing.T) {
	b := rpSchNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 100000, 100000, 100000, 999999)
	b.tick(rpAt("09:05")) // 300000: at the cap, not over; cache reads are not counted
	rpEq(t, "status", b.runs()[0]["status"], "running")
	b.usageLog(sid, 100000, 100000, 100001, 999999)
	b.tick(rpAt("09:06"))
	rpEq(t, "agent status", b.agent()["status"], "stopped")
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != "tokens" || rpStr(runs[0]["tokens"]) != "300001" {
		t.Fatalf("runs = %v", runs)
	}
}

// failedFirstStopKeepsReason: the budget stop fails once (a ForceStop fake); the tick's own retry stops the agent; the
// run keeps the budget reason. tokens answers the fake's SessionTokens (nil = unreadable).
func (b *rpSch) failedFirstStopKeepsReason(why, first, second string, tokens func(string) *int64) {
	b.t.Helper()
	api := &rpAPI{clock: rpClock(b.t, rpAt(first)), tokens: tokens, stopFn: func(*sql.DB, string, string) (string, string, error) {
		return "failed:close-failed", "", nil
	}}
	if err := sc.Tick(rpOpenDB(b.t, b.e).DB, api); err != nil {
		b.t.Fatal(err)
	}
	rpEq(b.t, "agent status after the failed stop", b.agent()["status"], "live")
	rpEq(b.t, "stop_reason after the failed stop", b.runs()[0]["stop_reason"], why)
	b.ok("config", "stop_timeout_s", "0") // the tick's stop fallback runs on the real clock
	b.tick(rpAt(second))                  // the stop fallback stops the agent; the next tick syncs the run
	rpEq(b.t, "agent status", b.agent()["status"], "stopped")
	b.tick(rpAt(second[:len(second)-1] + "9"))
	runs := b.runs()
	if len(runs) != 1 || rpStr(runs[0]["status"]) != "stopped" || rpStr(runs[0]["stop_reason"]) != why {
		b.t.Fatalf("runs = %v", runs)
	}
}

func TestScoutScheduleWallReasonSurvivesAFailedFirstStop(t *testing.T) {
	b := rpSchNew(t)
	b.spawned()
	b.failedFirstStopKeepsReason("wall", "09:21", "09:22", nil)
}

func TestScoutScheduleTokensReasonSurvivesAFailedFirstStop(t *testing.T) {
	b := rpSchNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 100000, 100000, 100001, 0)
	// the fake reads no log: it answers the 300001 tokens the usage log above holds (input + output + cache writes)
	b.failedFirstStopKeepsReason("tokens", "09:06", "09:07", func(session string) *int64 {
		if session != sid {
			return nil
		}
		n := int64(300001)
		return &n
	})
}

func TestScoutScheduleTokensAreStoredWhenTheScoutFinishesNormally(t *testing.T) {
	b := rpSchNew(t)
	sid := rpStr(b.spawned()["session_id"])
	b.usageLog(sid, 10, 20, 30, 0)
	b.finish()
	b.tick(rpAt("09:10"))
	r := b.runs()[0]
	if rpStr(r["status"]) != "done" || rpStr(r["tokens"]) != "60" {
		t.Fatalf("run = %v", r)
	}
}

func TestScoutScheduleBudgetConfigIsHonoured(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "scout_max_minutes", "5")
	b.spawned()
	b.tick(rpAt("09:06"))
	rpEq(t, "stop_reason", b.runs()[0]["stop_reason"], "wall")
}

// ---- FailureTests, ConfigAndSlotTests ---------------------------------------------------------------------------

func TestScoutScheduleAFailingScoutTickNeverBreaksTheTickAndLogsOnceADay(t *testing.T) {
	b := rpSchNew(t)
	b.ok("config", "scout_slots", "bad")
	b.tick(rpAt("09:01"))
	b.tick(rpAt("09:05"))
	ev := b.events("scout-failed")
	if len(ev) != 1 {
		t.Fatalf("scout-failed events = %v", ev)
	}
	contains(t, "detail", rpStr(ev[0]["detail"]), "scout_slots")
	if st, err := os.Stat(filepath.Join(b.e.Home, "exports")); err != nil || !st.IsDir() { // the tick went on to export
		t.Fatalf("exports dir: %v", err)
	}
	b.tick(rpAt("09:01", "2026-10-27"))
	if n := len(b.events("scout-failed")); n != 2 {
		t.Fatalf("scout-failed events = %d, want 2", n)
	}
	if len(b.runs()) != 0 {
		t.Fatalf("runs = %v", b.runs())
	}
}

func TestScoutScheduleDefaultsOnAFreshHome(t *testing.T) {
	e := ft.New(t)
	e.NeedPython()
	// the harness turns the scout off for unrelated tick tests, so the enabled default is read from the code
	if !regexp.MustCompile(`(?m)^\s*"scout_enabled": "1",`).MatchString(rpReadRepo(t, "bin/factory_cli.py")) {
		t.Fatal(`DEFAULT_CONFIG["scout_enabled"] is not "1" in bin/factory_cli.py`)
	}
	out := rpOK(t, e, "", nil, "config").Stdout
	for _, line := range []string{"scout_slots = 09,18", "scout_max_minutes = 20", "scout_max_tokens = 300000", "scout_queue_max_h = 6"} {
		contains(t, "config", out, line)
	}
}

func TestScoutScheduleSlotArithmetic(t *testing.T) {
	slot := func(s, h string) string {
		hours, err := sc.ParseSlots(h)
		if err != nil {
			t.Fatalf("ParseSlots(%q): %v", h, err)
		}
		return sc.CurrentSlot(rpClock(t, s), hours)
	}
	for _, c := range [][3]string{
		{"2026-10-26T09:00:00", "09,18", "2026-10-26T09"},
		{"2026-10-26T17:59:00", "09,18", "2026-10-26T09"},
		{"2026-10-26T23:59:00", "18,09", "2026-10-26T18"},
		{"2026-10-26T08:59:00", "09,18", "2026-10-25T18"},
	} {
		if got := slot(c[0], c[1]); got != c[2] {
			t.Fatalf("slot(%s, %s) = %s, want %s", c[0], c[1], got, c[2])
		}
	}
	for _, bad := range []string{"", "x", "9,24", "-1"} {
		if _, err := sc.ParseSlots(bad); err == nil {
			t.Fatalf("ParseSlots(%q) = nil error, want one", bad)
		}
	}
}
