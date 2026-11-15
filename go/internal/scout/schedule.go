package scout

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Port of scout/schedule.py: tick scheduling of the skill scout, one run per slot, idempotent, with a wall and
// token budget (docs/scout-tick.md). bin/factory owns spawn, force_stop, the dequeue and the config; they reach
// this package as ScheduleAPI. Every clock read goes through api.Clock(), except the claimed-stale check in Sync,
// which compares the scout-slot event stamp with api.Now() on purpose. The db must be a single-connection pool
// (factorydb.Open): Claim and report sending use BEGIN IMMEDIATE on it.

const (
	ClaimStaleS   = 600 // a run still 'claimed' this long after its scout-slot event lost its spawn (a crash)
	AgentNameBase = "scout"
	Superseded    = "superseded by a newer run"
)

// ScheduleOrg is the org of the scheduled scout: a claude-org agent, so spawn() must not inherit a legacy row's
// or the caller's org (T-0851).
const ScheduleOrg = DefaultOrg

var NeverSpawned = []string{"spawn failed", "claimed but never spawned", "scout agent row missing"}

// ScheduleAPI is what the tick needs from bin/factory.
type ScheduleAPI interface {
	ReportAPI
	// Now is the wall-clock stamp (isoformat), api.now().
	Now() string
	// Cfg reads a config value (scout_enabled, scout_slots, scout_max_minutes, scout_max_tokens, scout_queue_max_h).
	Cfg(db *sql.DB, key string) (string, error)
	// SessionTokens returns the tokens used by a session; nil = log unreadable (stored as NULL, never 0).
	SessionTokens(session string) (*int64, error)
	// ForceStop stops an agent; res "stopped" means it is gone.
	ForceStop(db *sql.DB, name, reason string) (res, note string, err error)
	CancelQueued(db *sql.DB, name, by, why string) (bool, error)
	Spawn(db *sql.DB, role, name, org, prompt string) error
	// AgentName is the scout's agents-row name (org-prefixed once org_prefix_default is on); "" = the legacy name.
	AgentName(db *sql.DB) string
}

func agentName(db *sql.DB, api ScheduleAPI) string {
	if n := api.AgentName(db); n != "" {
		return n
	}
	return AgentNameBase
}

// ParseSlots turns "09,18" into [9 18] (sorted, unique). Anything else is an error, which the tick wrapper records.
func ParseSlots(text string) ([]int, error) {
	seen := map[int]bool{}
	bad := false
	for _, x := range strings.Split(text, ",") {
		if x = pyTrim(x); x == "" {
			continue
		}
		n, err := strconv.Atoi(x)
		if err != nil {
			bad = true
			break
		}
		seen[n] = true
	}
	var hours []int
	for h := range seen {
		hours = append(hours, h)
	}
	sortInts(hours)
	if bad || len(hours) == 0 || hours[0] < 0 || hours[len(hours)-1] > 23 {
		return nil, fmt.Errorf("scout_slots must be a comma list of hours 0-23, got %s; fix it with `factory config scout_slots 09,18`", pyRepr(text))
	}
	return hours, nil
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// CurrentSlot is the latest slot hour <= now as "YYYY-MM-DDTHH" (local); before today's first slot, yesterday's last.
func CurrentSlot(clock time.Time, hours []int) string {
	var due []int
	for _, h := range hours {
		if h <= clock.Hour() {
			due = append(due, h)
		}
	}
	day, hour := clock, 0
	if len(due) > 0 {
		hour = due[len(due)-1]
	} else {
		day, hour = clock.AddDate(0, 0, -1), hours[len(hours)-1]
	}
	return fmt.Sprintf("%sT%02d", day.Format("2006-01-02"), hour)
}

func tsOf(s sql.NullString) (time.Time, bool) {
	if !s.Valid {
		return time.Time{}, false
	}
	return TS(s.String)
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func finish(db *sql.DB, api ScheduleAPI, runID int64, status, reason, session string) error {
	var tokens any
	if session != "" {
		if n, err := api.SessionTokens(session); err == nil && n != nil {
			tokens = *n
		}
	}
	if _, err := db.Exec("UPDATE scout_runs SET status=?, stop_reason=coalesce(stop_reason, ?), ended=coalesce(ended, ?), "+
		"tokens=coalesce(?, tokens) WHERE id=? AND status NOT IN ('done','stopped','failed','skipped')",
		status, nullable(reason), Stamp(api.Clock()), tokens, runID); err != nil {
		return err
	}
	if status != "skipped" {
		return countRun(db, api, runID)
	}
	return nil
}

// countRun stores the counts of candidates first seen and triage rows created in the run's [started, ended]
// window (see schedule.py _count for why the agent's started is sometimes ignored). A skipped run, and a failed
// run that was never spawned, keep NULL.
func countRun(db *sql.DB, api ScheduleAPI, runID int64) error {
	var slot string
	var started, ended, stopReason sql.NullString
	err := db.QueryRow("SELECT slot, started, ended, stop_reason FROM scout_runs WHERE id=? AND status IN ('done','stopped','failed') "+
		"AND new_candidates IS NULL", runID).Scan(&slot, &started, &ended, &stopReason)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	} else if err != nil {
		return err
	}
	end, ok := tsOf(ended)
	if !ok {
		return nil
	}
	if !started.Valid && hasAnyPrefix(stopReason.String, NeverSpawned) {
		return nil
	}
	superseded := !started.Valid && strings.HasPrefix(stopReason.String, Superseded)
	var astart time.Time
	haveAgent := false
	if !superseded {
		var as sql.NullString
		err := db.QueryRow("SELECT started FROM agents WHERE name=?", agentName(db, api)).Scan(&as)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			astart, haveAgent = tsOf(as)
		}
	}
	if haveAgent {
		rows, err := db.Query("SELECT ts FROM events WHERE entity='scout' AND entity_id=? AND action='scout-slot'", slot)
		if err != nil {
			return err
		}
		var latest time.Time
		any := false
		for rows.Next() {
			var s sql.NullString
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				return err
			}
			if t, ok := tsOf(s); ok && !t.After(end) && (!any || t.After(latest)) {
				latest, any = t, true
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		if any && astart.Before(latest) {
			haveAgent = false // an earlier run's start
		}
	}
	start := end
	if t, ok := tsOf(started); ok && !t.After(end) && t.Before(start) {
		start = t
	}
	if haveAgent && !astart.After(end) && astart.Before(start) {
		start = astart
	}
	const layout = "2006-01-02T15:04:05.999999-07:00"
	lo, hi := start.UTC().Format(layout), end.UTC().Format(layout)
	var n, tr int64
	if err := db.QueryRow("SELECT COUNT(*) FROM scout_candidates WHERE julianday(first_seen) BETWEEN julianday(?) AND julianday(?) "+
		"AND key<>'local:skilleval-control'", lo, hi).Scan(&n); err != nil {
		return err
	}
	if err := db.QueryRow("SELECT COUNT(*) FROM scout_triage WHERE julianday(created) BETWEEN julianday(?) AND julianday(?)", lo, hi).Scan(&tr); err != nil {
		return err
	}
	_, err = db.Exec("UPDATE scout_runs SET new_candidates=?, triaged=? WHERE id=?", n, tr, runID)
	return err
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

func setStatus(db *sql.DB, runID int64, status string, clock time.Time) error {
	_, err := db.Exec("UPDATE scout_runs SET status=?, started=coalesce(started, ?) WHERE id=? "+
		"AND status NOT IN ('done','stopped','failed','skipped')", status, Stamp(clock), runID)
	return err
}

type agentRow struct {
	Name, Status                                  string
	SessionID, QueuedAt, Started, StopRequestedAt sql.NullString
}

func loadAgent(db *sql.DB, name string) (*agentRow, error) {
	var a agentRow
	err := db.QueryRow("SELECT name, status, session_id, queued_at, started, stop_requested_at FROM agents WHERE name=?", name).
		Scan(&a.Name, &a.Status, &a.SessionID, &a.QueuedAt, &a.Started, &a.StopRequestedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

func cfgInt(db *sql.DB, api ScheduleAPI, key string) (int64, error) {
	v, err := api.Cfg(db, key)
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(pyTrim(v), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("config %s is %s, expected an integer: fix it with `factory config %s <n>`", key, pyRepr(v), key)
	}
	return n, nil
}

// budget stops a live scout over its wall or token cap. The stop reason is stored first, so a stop the fallback
// finishes later still ends the run as 'stopped' with it.
func budget(db *sql.DB, api ScheduleAPI, clock time.Time, runID int64, slot string, agent *agentRow) error {
	if agent.StopRequestedAt.String != "" {
		return nil // a stop is already in progress: the tick's own stop fallback retries it
	}
	why := ""
	started, hasStart := tsOf(agent.Started)
	maxMin, err := cfgInt(db, api, "scout_max_minutes")
	if err != nil {
		return err
	}
	if hasStart && clock.Sub(started).Seconds() > float64(maxMin*60) {
		why = "wall"
	} else if agent.SessionID.String != "" {
		used, err := api.SessionTokens(agent.SessionID.String)
		if err != nil {
			return err
		}
		if used != nil {
			maxTok, err := cfgInt(db, api, "scout_max_tokens")
			if err != nil {
				return err
			}
			if *used > maxTok {
				why = "tokens" // an unreadable log (nil) cannot prove the cap exceeded; the wall cap still stops the run
			}
		}
	}
	if why == "" {
		return nil
	}
	if _, err := db.Exec("UPDATE scout_runs SET stop_reason=? WHERE id=?", why, runID); err != nil {
		return err
	}
	res, err := db.Exec("UPDATE agents SET stop_requested_at=?, stop_attempt_at=NULL, stop_failed_kind=NULL "+
		"WHERE name=? AND status='live' AND stop_requested_at IS NULL", api.Now(), agent.Name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil // it finished meanwhile
	}
	if err := api.Event(db, "scout", slot, "scout-budget", why); err != nil {
		return err
	}
	out, _, err := api.ForceStop(db, agent.Name, "budget")
	if err != nil {
		return err
	}
	if out == "stopped" {
		return finish(db, api, runID, "stopped", why, agent.SessionID.String)
	}
	return nil
}

const nonTerminal = "status NOT IN ('done','stopped','failed','skipped')"

// Sync brings the latest non-terminal run in line with the scout's agents row and enforces its budgets.
func Sync(db *sql.DB, api ScheduleAPI, clock time.Time) error {
	rows, err := db.Query("SELECT id, slot, status FROM scout_runs WHERE " + nonTerminal + " ORDER BY id")
	if err != nil {
		return err
	}
	type open struct {
		id           int64
		slot, status string
	}
	var runs []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.slot, &o.status); err != nil {
			rows.Close()
			return err
		}
		runs = append(runs, o)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(runs) == 0 {
		return nil
	}
	for _, old := range runs[:len(runs)-1] {
		if err := finish(db, api, old.id, "failed", Superseded, ""); err != nil {
			return err
		}
	}
	run := runs[len(runs)-1]
	agent, err := loadAgent(db, agentName(db, api))
	if err != nil {
		return err
	}
	if run.status == "claimed" {
		// normally spawned in the same call that claimed it; one older than ClaimStaleS lost its spawn (a crash)
		var ev sql.NullString
		err := db.QueryRow("SELECT ts FROM events WHERE entity='scout' AND entity_id=? AND action='scout-slot' ORDER BY id DESC LIMIT 1", run.slot).Scan(&ev)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		at, ok := tsOf(ev)
		stale := !ok
		if ok {
			now, nok := TS(api.Now())
			if !nok {
				return fmt.Errorf("api.Now() returned an unparsable stamp %s", pyRepr(api.Now()))
			}
			stale = now.Sub(at).Seconds() > ClaimStaleS
		}
		if stale {
			return finish(db, api, run.id, "failed", "claimed but never spawned", "")
		}
		return nil
	}
	if agent == nil {
		return finish(db, api, run.id, "failed", "scout agent row missing", "")
	}
	sid := agent.SessionID.String
	switch agent.Status {
	case "queued":
		if err := setStatus(db, run.id, "queued", clock); err != nil {
			return err
		}
		limitH, err := cfgInt(db, api, "scout_queue_max_h")
		if err != nil {
			return err
		}
		if queued, ok := tsOf(agent.QueuedAt); ok && clock.Sub(queued).Seconds() > float64(limitH*3600) {
			cancelled, err := api.CancelQueued(db, agent.Name, "tick", fmt.Sprintf("scout queued > %dh", limitH))
			if err != nil {
				return err
			}
			if cancelled {
				return finish(db, api, run.id, "skipped", fmt.Sprintf("gate closed > %dh", limitH), "")
			}
		}
	case "starting":
		return nil // claimed by the dequeue; live on a later tick
	case "live":
		if _, err := db.Exec("UPDATE scout_runs SET status='running', started=coalesce(started, ?) WHERE id=? AND status IN ('queued','running')",
			Stamp(clock), run.id); err != nil {
			return err
		}
		return budget(db, api, clock, run.id, run.slot, agent)
	case "done", "exited":
		return finish(db, api, run.id, "done", "", sid)
	case "stopped":
		return finish(db, api, run.id, "stopped", "stopped", sid)
	case "lost":
		return finish(db, api, run.id, "failed", "agent lost", sid)
	case "cancelled":
		return finish(db, api, run.id, "skipped", "dequeued", "")
	}
	return nil
}

// Claim decides the slot in one IMMEDIATE transaction: "claimed" (the caller spawns), "skipped", or "" (already recorded).
func Claim(db *sql.DB, api ScheduleAPI, slot string) (string, error) {
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return "", err
	}
	done := false
	defer rollback(db, &done)
	var one int
	err := db.QueryRow("SELECT 1 FROM scout_runs WHERE slot=?", slot).Scan(&one)
	if err == nil {
		return "", nil // the deferred rollback ends the transaction
	} else if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	name := agentName(db, api)
	busy := true
	err = db.QueryRow("SELECT 1 FROM agents WHERE name=? AND status IN ('live','starting','queued')", name).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		busy = false
	} else if err != nil {
		return "", err
	}
	status, reason := "claimed", ""
	var agent, ended any = name, nil
	if busy {
		status, reason, agent, ended = "skipped", "previous run active", nil, Stamp(api.Clock())
	}
	if _, err := db.Exec("INSERT INTO scout_runs(slot, agent, status, stop_reason, ended) VALUES(?,?,?,?,?)",
		slot, agent, status, nullable(reason), ended); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint failed") { // another connection won
			return "", nil
		}
		return "", err
	}
	detail := slot
	if status != "claimed" {
		detail = fmt.Sprintf("%s skipped: %s", slot, reason)
	}
	if err := api.Event(db, "scout", slot, "scout-slot", detail); err != nil {
		return "", err
	}
	if _, err := db.Exec("COMMIT"); err != nil {
		return "", err
	}
	done = true
	return status, nil
}

// Start spawns the claimed run through spawn() (and so the gate): queued if the gate is closed, else running.
func Start(db *sql.DB, api ScheduleAPI, slot string) error {
	name := agentName(db, api)
	if err := api.Spawn(db, AgentNameBase, name, ScheduleOrg, fmt.Sprintf("Scheduled scout run, slot %s.", slot)); err != nil {
		why := err.Error()
		if _, uerr := db.Exec("UPDATE scout_runs SET status='failed', stop_reason=?, ended=? WHERE slot=? AND status='claimed'",
			runePrefix("spawn failed: "+why, 300), Stamp(api.Clock()), slot); uerr != nil {
			return uerr
		}
		return api.Event(db, "scout", slot, "scout-spawn-failed", runePrefix(why, 300)) // no retry for the slot
	}
	agent, err := loadAgent(db, name)
	if err != nil {
		return err
	}
	status := "queued"
	if agent != nil && agent.Status == "live" {
		status = "running"
	}
	_, err = db.Exec("UPDATE scout_runs SET status=?, started=CASE WHEN ?='running' THEN ? END WHERE slot=? AND status='claimed'",
		status, status, Stamp(api.Clock()), slot)
	return err
}

// Tick is one scheduling pass (the cmd_tick step): sync the open run, then start the latest due slot if it has no run.
func Tick(db *sql.DB, api ScheduleAPI) error {
	clock := api.Clock()
	if err := Sync(db, api, clock); err != nil {
		return err
	}
	if _, err := SendPending(db, api); err != nil { // once per finished run, to the director (docs/scout-report.md)
		// a failed report must not stop the next slot; it is retried next tick
		if eerr := api.Event(db, "scout", "-", "scout-report-failed", runePrefix(err.Error(), 300)); eerr != nil {
			return eerr
		}
	}
	enabled, err := api.Cfg(db, "scout_enabled")
	if err != nil {
		return err
	}
	if enabled != "1" {
		return nil
	}
	slotsCfg, err := api.Cfg(db, "scout_slots")
	if err != nil {
		return err
	}
	hours, err := ParseSlots(slotsCfg)
	if err != nil {
		return err
	}
	slot := CurrentSlot(clock, hours)
	var one int
	err = db.QueryRow("SELECT 1 FROM scout_runs WHERE slot=?", slot).Scan(&one)
	if err == nil {
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	status, err := Claim(db, api, slot)
	if err != nil {
		return err
	}
	if status == "claimed" {
		return Start(db, api, slot)
	}
	return nil
}
