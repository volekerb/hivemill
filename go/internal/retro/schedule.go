package retro

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"factory/go/internal/scout"
)

// Port of retro/schedule.py: the daily retro job (runtime from retro_org), one run per local day from retro_hour. bin/factory owns
// spawn, force_stop, the config and the clock; they reach this package as API. The db must be a single-connection
// pool (factorydb.Open): Claim uses BEGIN IMMEDIATE on it.

const (
	Role = "retro"

	nonTerminal = "status NOT IN ('done','stopped','failed','skipped')"
)

// Orgs are the runtimes retro_org may name; the agent is <org>-retro.
var Orgs = []string{"cursor", "claude"}

// NameOf is the agent name of the retro run on org.
func NameOf(org string) string { return org + "-" + Role }

// ParseOrg is parse_org: cursor or claude.
func ParseOrg(text string) (string, error) {
	org := strings.TrimSpace(text)
	for _, o := range Orgs {
		if org == o {
			return org, nil
		}
	}
	return "", fmt.Errorf("retro_org must be cursor or claude, got '%s'; fix it with `factory config retro_org cursor`", text)
}

// SpawnExit is returned by API.Spawn when the spawner exited (Python SystemExit); Code is its exit code.
type SpawnExit struct{ Code int }

func (e *SpawnExit) Error() string { return fmt.Sprintf("spawn exited with code %d", e.Code) }

// API is what the tick needs from bin/factory.
type API interface {
	Clock() time.Time
	// Now is the wall-clock stamp (isoformat), api.now().
	Now() string
	// Cfg reads a config value (retro_enabled, retro_hour, retro_max_minutes).
	Cfg(db *sql.DB, key string) (string, error)
	Event(db *sql.DB, entity, entityID, action, detail string) error
	Spawn(db *sql.DB, role, name, org, model, prompt string) error
	// ForceStop stops an agent; res "stopped" means it is gone.
	ForceStop(db *sql.DB, name, reason string) (res, note string, err error)
}

func pyRepr(s string) string { return "'" + strings.ReplaceAll(s, "'", "\\'") + "'" }

func stamp(t time.Time) string { return scout.Stamp(t) }

// ParseHour is parse_hour: an integer 0-23.
func ParseHour(text string) (int, error) {
	h, ok := PyInt(text)
	if !ok || h < 0 || h > 23 {
		return 0, fmt.Errorf("retro_hour must be an integer 0-23, got %s; fix it with `factory config retro_hour 6`", pyRepr(text))
	}
	return int(h), nil
}

// ParseMaxMinutes is parse_max_minutes: an integer >= 1 (values beyond int64 are refused; Python has no bound).
func ParseMaxMinutes(text string) (int, error) {
	m, ok := PyInt(text)
	if !ok || m < 1 {
		return 0, fmt.Errorf("retro_max_minutes must be an integer >= 1, got %s", pyRepr(text))
	}
	return int(m), nil
}

func runePrefix(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

func recordConfigFail(db *sql.DB, api API, msg string) error {
	today := api.Clock().Format("2006-01-02")
	var v string
	err := db.QueryRow("SELECT value FROM config WHERE key='last_retro_config_fail_date'").Scan(&v)
	if err == nil && v == today {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read last_retro_config_fail_date: %w", err)
	}
	if err := api.Event(db, "retro", "-", "retro-failed", runePrefix(msg, 300)); err != nil {
		return fmt.Errorf("record retro-failed: %w", err)
	}
	if _, err := db.Exec("INSERT INTO config(key,value) VALUES('last_retro_config_fail_date',?) "+
		"ON CONFLICT(key) DO UPDATE SET value=excluded.value", today); err != nil {
		return fmt.Errorf("write last_retro_config_fail_date: %w", err)
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func finish(db *sql.DB, api API, runID int64, status, reason string) error {
	_, err := db.Exec("UPDATE retro_runs SET status=?, stop_reason=coalesce(stop_reason, ?), ended=coalesce(ended, ?) "+
		"WHERE id=? AND "+nonTerminal, status, nullable(reason), stamp(api.Clock()), runID)
	if err != nil {
		return fmt.Errorf("finish retro run %d: %w", runID, err)
	}
	return nil
}

type agentRow struct {
	Name, Status                      string
	SessionID, Started, StopRequested sql.NullString
}

func loadAgent(db *sql.DB, name string) (*agentRow, error) {
	var a agentRow
	err := db.QueryRow("SELECT name, status, session_id, started, stop_requested_at FROM agents WHERE name=?", name).
		Scan(&a.Name, &a.Status, &a.SessionID, &a.Started, &a.StopRequested)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load agent %s: %w", name, err)
	}
	return &a, nil
}

// requestStop marks a live agent as stopping; false when it was no longer live or already marked.
func requestStop(db *sql.DB, api API, name string) (bool, error) {
	res, err := db.Exec("UPDATE agents SET stop_requested_at=?, stop_attempt_at=NULL, stop_failed_kind=NULL "+
		"WHERE name=? AND status='live' AND stop_requested_at IS NULL", api.Now(), name)
	if err != nil {
		return false, fmt.Errorf("request stop of %s: %w", name, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("count stop request of %s: %w", name, err)
	}
	return n == 1, nil
}

func stopLiveRetro(db *sql.DB, api API, runID int64, agent *agentRow, eventKind, stopReason string) error {
	if agent == nil || agent.Status != "live" || (agent.StopRequested.Valid && agent.StopRequested.String != "") {
		return nil
	}
	if _, err := db.Exec("UPDATE retro_runs SET stop_reason=? WHERE id=?", stopReason, runID); err != nil {
		return fmt.Errorf("set stop_reason of retro run %d: %w", runID, err)
	}
	ok, err := requestStop(db, api, agent.Name)
	if err != nil || !ok {
		return err
	}
	if err := api.Event(db, "retro", "-", eventKind, stopReason); err != nil {
		return fmt.Errorf("record %s: %w", eventKind, err)
	}
	res, _, err := api.ForceStop(db, agent.Name, "config")
	if err != nil {
		return fmt.Errorf("force_stop %s: %w", agent.Name, err)
	}
	if res == "stopped" {
		return finish(db, api, runID, "stopped", stopReason)
	}
	return nil
}

func abortLiveOnConfigError(db *sql.DB, api API) error {
	var id int64
	var runAgent sql.NullString
	err := db.QueryRow("SELECT id, agent FROM retro_runs WHERE "+nonTerminal+" ORDER BY id DESC").Scan(&id, &runAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("find open retro run: %w", err)
	}
	agent, err := loadAgent(db, runAgent.String)
	if err != nil || agent == nil || agent.Status != "live" {
		return err
	}
	return stopLiveRetro(db, api, id, agent, "retro-config", "config")
}

type openRun struct {
	ID          int64
	Day, Status string
	StopReason  sql.NullString
	Agent       sql.NullString
}

func budget(db *sql.DB, api API, clock time.Time, run openRun, agent *agentRow) error {
	if agent.StopRequested.Valid && agent.StopRequested.String != "" {
		return nil
	}
	cfg, err := api.Cfg(db, "retro_max_minutes")
	if err != nil {
		return fmt.Errorf("read retro_max_minutes: %w", err)
	}
	limitM, perr := ParseMaxMinutes(cfg)
	if perr != nil {
		if err := recordConfigFail(db, api, perr.Error()); err != nil {
			return err
		}
		return stopLiveRetro(db, api, run.ID, agent, "retro-config", "config")
	}
	started, ok := scout.TS(agent.Started.String)
	if !agent.Started.Valid || !ok {
		return nil
	}
	if clock.Sub(started).Seconds() <= float64(limitM)*60 {
		return nil
	}
	if _, err := db.Exec("UPDATE retro_runs SET stop_reason=? WHERE id=?", "wall", run.ID); err != nil {
		return fmt.Errorf("set stop_reason of retro run %d: %w", run.ID, err)
	}
	won, err := requestStop(db, api, agent.Name)
	if err != nil || !won {
		return err
	}
	if err := api.Event(db, "retro", run.Day, "retro-budget", "wall"); err != nil {
		return fmt.Errorf("record retro-budget: %w", err)
	}
	res, _, err := api.ForceStop(db, agent.Name, "budget")
	if err != nil {
		return fmt.Errorf("force_stop %s: %w", agent.Name, err)
	}
	if res == "stopped" {
		return finish(db, api, run.ID, "stopped", "wall")
	}
	return nil
}

// Sync aligns the open retro run with its agent row and enforces the wall budget.
func Sync(db *sql.DB, api API, clock time.Time) error {
	rows, err := db.Query("SELECT id, day, status, stop_reason, agent FROM retro_runs WHERE " + nonTerminal + " ORDER BY id")
	if err != nil {
		return fmt.Errorf("list open retro runs: %w", err)
	}
	var runs []openRun
	for rows.Next() {
		var r openRun
		if err := rows.Scan(&r.ID, &r.Day, &r.Status, &r.StopReason, &r.Agent); err != nil {
			rows.Close()
			return fmt.Errorf("scan retro run: %w", err)
		}
		runs = append(runs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("read retro runs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close retro runs: %w", err)
	}
	if len(runs) == 0 {
		return nil
	}
	run := runs[len(runs)-1]
	for _, old := range runs[:len(runs)-1] {
		if err := finish(db, api, old.ID, "failed", "superseded"); err != nil {
			return err
		}
	}
	agent, err := loadAgent(db, run.Agent.String)
	if err != nil {
		return err
	}
	if run.Status == "claimed" {
		return nil
	}
	if agent == nil {
		return finish(db, api, run.ID, "failed", "retro agent row missing")
	}
	switch agent.Status {
	case "queued":
		if _, err := db.Exec("UPDATE retro_runs SET status='queued' WHERE id=? AND status='claimed'", run.ID); err != nil {
			return fmt.Errorf("queue retro run %d: %w", run.ID, err)
		}
	case "starting":
		return nil
	case "live":
		if _, err := db.Exec("UPDATE retro_runs SET status='running', started=coalesce(started, ?) WHERE id=? "+
			"AND status IN ('queued','running')", stamp(clock), run.ID); err != nil {
			return fmt.Errorf("mark retro run %d running: %w", run.ID, err)
		}
		return budget(db, api, clock, run, agent)
	case "done", "exited":
		return finish(db, api, run.ID, "done", "")
	case "stopped":
		reason := run.StopReason.String
		if reason == "" {
			reason = "stopped"
		}
		return finish(db, api, run.ID, "stopped", reason)
	case "lost":
		return finish(db, api, run.ID, "failed", "agent lost")
	case "cancelled":
		return finish(db, api, run.ID, "skipped", "dequeued")
	}
	return nil
}

// Claim decides the day in one IMMEDIATE transaction: "claimed" (the caller spawns), "skipped", or "" (already
// recorded).
func Claim(db *sql.DB, api API, day, org string) (status string, err error) {
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil {
		return "", fmt.Errorf("begin claim of retro day %s: %w", day, err)
	}
	open := true
	defer func() {
		if open {
			if _, rerr := db.Exec("ROLLBACK"); rerr != nil && err == nil {
				err = fmt.Errorf("roll back claim of retro day %s: %w", day, rerr)
			}
		}
	}()
	var one int
	switch qerr := db.QueryRow("SELECT 1 FROM retro_runs WHERE day=?", day).Scan(&one); {
	case qerr == nil:
		return "", nil
	case !errors.Is(qerr, sql.ErrNoRows):
		return "", fmt.Errorf("check retro day %s: %w", day, qerr)
	}
	busy := true
	switch qerr := db.QueryRow("SELECT 1 FROM agents WHERE name IN (?,?) AND status IN ('live','starting','queued')",
		NameOf(Orgs[0]), NameOf(Orgs[1])).Scan(&one); {
	case errors.Is(qerr, sql.ErrNoRows):
		busy = false
	case qerr != nil:
		return "", fmt.Errorf("check retro agent: %w", qerr)
	}
	reason := ""
	var agent, ended any = NameOf(org), nil
	status = "claimed"
	if busy {
		status, reason, agent, ended = "skipped", "previous run active", nil, stamp(api.Clock())
	}
	if _, ierr := db.Exec("INSERT INTO retro_runs(day, status, agent, stop_reason, ended) VALUES(?,?,?,?,?)",
		day, status, agent, nullable(reason), ended); ierr != nil {
		if strings.Contains(ierr.Error(), "UNIQUE constraint failed") { // another connection won
			return "", nil
		}
		return "", fmt.Errorf("insert retro run %s: %w", day, ierr)
	}
	detail := day
	if status != "claimed" {
		detail = fmt.Sprintf("%s skipped: %s", day, reason)
	}
	if err := api.Event(db, "retro", day, "retro-day", detail); err != nil {
		return "", fmt.Errorf("record retro-day: %w", err)
	}
	if _, err := db.Exec("COMMIT"); err != nil {
		return "", fmt.Errorf("commit claim of retro day %s: %w", day, err)
	}
	open = false
	return status, nil
}

// Start spawns the claimed run: queued if the gate is closed, else running. A refused spawn fails the run (no retry).
func Start(db *sql.DB, api API, day, org string) error {
	name := NameOf(org)
	model, err := scout.Model(org, Role)
	if err != nil {
		return fmt.Errorf("model of %s/%s: %w", org, Role, err)
	}
	serr := api.Spawn(db, Role, name, org, model,
		fmt.Sprintf("Scheduled daily retro for %s. Run factory retro packet, investigate, then factory retro file.", day))
	if serr != nil {
		why := serr.Error()
		var ex *SpawnExit
		if errors.As(serr, &ex) {
			why = fmt.Sprintf("exit %d; see the tick's stderr", ex.Code)
		}
		if _, uerr := db.Exec("UPDATE retro_runs SET status='failed', stop_reason=?, ended=? WHERE day=? AND status='claimed'",
			runePrefix("spawn failed: "+why, 300), stamp(api.Clock()), day); uerr != nil {
			return fmt.Errorf("fail retro run %s: %w", day, uerr)
		}
		if eerr := api.Event(db, "retro", day, "retro-spawn-failed", runePrefix(why, 300)); eerr != nil {
			return fmt.Errorf("record retro-spawn-failed: %w", eerr)
		}
		return nil
	}
	agent, err := loadAgent(db, name)
	if err != nil {
		return err
	}
	status := "queued"
	if agent != nil && agent.Status == "live" {
		status = "running"
	}
	if _, err := db.Exec("UPDATE retro_runs SET status=?, started=CASE WHEN ?='running' THEN ? END WHERE day=? AND status='claimed'",
		status, status, stamp(api.Clock()), day); err != nil {
		return fmt.Errorf("mark retro run %s %s: %w", day, status, err)
	}
	return nil
}

// Tick is one scheduling pass: sync the open run, then claim and start today's run from retro_hour.
func Tick(db *sql.DB, api API) error {
	clock := api.Clock()
	if err := Sync(db, api, clock); err != nil {
		return err
	}
	enabled, err := api.Cfg(db, "retro_enabled")
	if err != nil {
		return fmt.Errorf("read retro_enabled: %w", err)
	}
	if enabled != "1" {
		return nil
	}
	hourCfg, err := api.Cfg(db, "retro_hour")
	if err != nil {
		return fmt.Errorf("read retro_hour: %w", err)
	}
	minCfg, err := api.Cfg(db, "retro_max_minutes")
	if err != nil {
		return fmt.Errorf("read retro_max_minutes: %w", err)
	}
	orgCfg, err := api.Cfg(db, "retro_org")
	if err != nil {
		return fmt.Errorf("read retro_org: %w", err)
	}
	hour, herr := ParseHour(hourCfg)
	if herr == nil {
		_, herr = ParseMaxMinutes(minCfg)
	}
	var org string
	if herr == nil {
		org, herr = ParseOrg(orgCfg)
	}
	if herr != nil {
		if err := recordConfigFail(db, api, herr.Error()); err != nil {
			return err
		}
		return abortLiveOnConfigError(db, api)
	}
	if clock.Hour() < hour {
		return nil
	}
	// T-1135: do not claim or spawn while other rows are still queued; the tick dequeue owns their starts.
	var one int
	switch qerr := db.QueryRow("SELECT 1 FROM agents WHERE status IN ('queued','starting') AND role != ? LIMIT 1", Role).Scan(&one); {
	case qerr == nil:
		return nil
	case !errors.Is(qerr, sql.ErrNoRows):
		return fmt.Errorf("check queued agents: %w", qerr)
	}
	day := clock.Format("2006-01-02")
	switch qerr := db.QueryRow("SELECT 1 FROM retro_runs WHERE day=?", day).Scan(&one); {
	case qerr == nil:
		return nil
	case !errors.Is(qerr, sql.ErrNoRows):
		return fmt.Errorf("check retro day %s: %w", day, qerr)
	}
	status, err := Claim(db, api, day, org)
	if err != nil {
		return err
	}
	if status == "claimed" {
		return Start(db, api, day, org)
	}
	return nil
}
