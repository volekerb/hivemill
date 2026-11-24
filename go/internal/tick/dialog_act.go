package tick

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"

	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

// auto_clear_dialog and its helpers (bin/factory_cli.py:17324-17600). The ONLY key this file ever sends to a pane is
// escKey (L-021: never an option key; TestAutoClearDialog asserts it on the stub log).
const (
	escKey                = "esc"
	settingsEscCooldownS  = 300 // SETTINGS_ESC_COOLDOWN_S (factory_cli.py:17354)
	settingsEscMaxPerDay  = 3   // SETTINGS_ESC_MAX_PER_DAY (factory_cli.py:17355)
	defaultOrg            = "claude"
	paneReadLines         = "60"
	paneReadSourceRecent  = "recent"
	paneReadSourceUnwrap  = "recent-unwrapped"
	paneReadLinesAlert    = "25"
	blockedAlertTailRunes = 2500
)

// workerRoles is WORKER_ROLES (factory_cli.py:888).
var workerRoles = map[string]bool{"pm": true, "coder": true, "qa": true, "reviewer": true, "judge": true,
	"cleaner": true, "staff-engineer": true, "researcher": true, "scout": true, "retro": true, "evaluator": true}

// dlgAgent is the part of an agents row the reap loop and auto_clear_dialog read.
type dlgAgent struct {
	name, role, status                         string
	goal, team, task, pane, started, spawnArgs sql.NullString
	session, stopRequested                     sql.NullString
	blockedSince                               sql.NullFloat64
	blockedAlerted                             sql.NullInt64
}

const dlgAgentCols = "name, role, status, goal_id, team_id, task_id, pane_id, started, spawn_args, session_id, stop_requested_at, blocked_since, blocked_alerted"

func (a *dlgAgent) dests() []any {
	return []any{&a.name, &a.role, &a.status, &a.goal, &a.team, &a.task, &a.pane, &a.started, &a.spawnArgs, &a.session,
		&a.stopRequested, &a.blockedSince, &a.blockedAlerted}
}

func readDlgAgent(db *sql.DB, name string) (*dlgAgent, error) {
	a := &dlgAgent{}
	var role sql.NullString
	d := a.dests()
	d[1] = &role
	err := db.QueryRow("SELECT "+dlgAgentCols+" FROM agents WHERE name=?", name).Scan(d...)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read agent %s: %w", name, err)
	}
	a.role = role.String
	return a, nil
}

func (r *reapDB) tickSend(to, subject, body, kind string, goal, task *string, org string) error {
	b := body
	_, _, err := r.Send(store.Msg{To: to, Subject: subject, Body: &b, Kind: kind, Goal: goal, Task: task, Sender: tickActor, Org: org})
	return err
}

// goalAlertTo is goal_alert_to(con, goal, lead=lead): the lead when it is of the goal's org, else "director".
func goalAlertTo(r *reapDB, goal, lead string) (string, error) {
	if lead == "" {
		return "director", nil
	}
	lo, err := r.AgentOrg(lead)
	if err != nil {
		return "", err
	}
	g, err := r.GoalOrg(goal)
	if err != nil {
		return "", err
	}
	if g == "" {
		g = defaultOrg
	}
	if lo == g {
		return lead, nil
	}
	return "director", nil
}

// blockedAlertTarget is blocked_alert_target: (recipient, body hint).
func blockedAlertTarget(r *reapDB, a *dlgAgent) (to, hint string, err error) {
	if a.role == "director" {
		return "boss", "", nil
	}
	if !workerRoles[a.role] {
		return "director", "", nil
	}
	hint = "Permission dialog? Never pick an option that changes user settings (L-021). Answer: factory unblock " +
		a.name + " --reason \"...\" (deny once, worker continues) or factory stop " + a.name + ". Skill: blocked-worker.\n"
	lead := ""
	if a.team.Valid && a.team.String != "" {
		err := r.DB.QueryRow("SELECT a.name FROM teams t JOIN agents a ON a.name=t.lead_agent "+
			"WHERE t.id=? AND (a.status='live' OR t.parked_at IS NOT NULL)", a.team.String).Scan(&lead)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return "", "", fmt.Errorf("read lead of team %s: %w", a.team.String, err)
		}
	}
	to, err = goalAlertTo(r, a.goal.String, lead)
	return to, hint, err
}

// hostExe is _agent_host_exe(con, row), with its legacy-TUI rule: a per-spawn host_backend wins; a row hosted on hc
// (pane_id == name) is hc; a row with a pane id that is not its name is a TUI pane; else the effective backend.
func hostExe(c *herdr.Client, name string, a *dlgAgent) string {
	if a == nil {
		_, e := c.TerminalBackendEffective()
		return e
	}
	sa, _ := store.SpawnArgsMap(a.spawnArgs.String)
	if hb, _ := sa["host_backend"].(string); hb == "hc" || hb == "herdr" {
		return hb
	}
	if a.pane.String == name && a.pane.Valid {
		return "hc"
	}
	if a.pane.String != "" && a.pane.String != name {
		return "herdr"
	}
	_, e := c.TerminalBackendEffective()
	return e
}

// hostListRow is _host_list_row: the agent's own host list entry, nil when unlisted or unreachable.
func hostListRow(r *reapDB, c *herdr.Client, name string) (herdr.Agent, error) {
	a, err := readDlgAgent(r.DB, name)
	if err != nil {
		return nil, err
	}
	listed, err := c.HostAgents(hostExe(c, name, a))
	if errors.Is(err, herdr.ErrUnreachable) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	for _, x := range listed {
		if x.Name() == name {
			return x, nil
		}
	}
	return nil, nil
}

// paneText reads the pane (`herdr agent read`, check=False) and returns tail.get("text") or tail.get("raw") or "". A
// result that is neither empty nor an object, or text that is not a string, is Python's AttributeError.
func paneText(c herdrCaller, name, source, lines string) (text string, tail any, err error) {
	res, err := c.Herdr(false, "agent", "read", name, "--source", source, "--lines", lines)
	if err != nil {
		return "", nil, fmt.Errorf("agent read %s: %w", name, err)
	}
	if !pyTruthy(res) {
		return "", map[string]any{}, nil
	}
	m, ok := res.(map[string]any)
	if !ok {
		return "", res, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(res))
	}
	for _, k := range []string{"text", "raw"} {
		if v := m[k]; pyTruthy(v) {
			s, isStr := v.(string)
			if !isStr {
				return "", m, fmt.Errorf("'%s' object has no attribute 'split'", pyTypeName(v))
			}
			return s, m, nil
		}
	}
	return "", m, nil
}

// dialogNow is dialog_now: the host still reports the agent blocked AND the pane still classifies as want.
func dialogNow(r *reapDB, c *herdr.Client, name, role, want string) (bool, error) {
	h, err := hostListRow(r, c, name)
	if err != nil || h == nil {
		return false, err
	}
	if s, _ := h["agent_status"].(string); s != "blocked" {
		return false, nil
	}
	text, _, err := paneText(c, name, paneReadSourceRecent, paneReadLines)
	if err != nil {
		return false, err
	}
	if role == "boss" {
		unsent, err := hasUnsentInput(text)
		if err != nil || unsent {
			return false, err
		}
	}
	k, err := classifyDialog(text)
	if err != nil {
		return false, err
	}
	return k.kind == want, nil
}

// autoClearClock is auto_clear_clock: $FACTORY_AUTO_CLEAR_NOW (tests only) or the tick's t and now().
func autoClearClock(r *reapDB, t float64) (float64, string, error) {
	fixed := os.Getenv("FACTORY_AUTO_CLEAR_NOW")
	if fixed == "" {
		return t, r.Now(), nil
	}
	loc := processZone()
	d, ok := pyFromISO(fixed, loc)
	if !ok {
		return 0, "", fmt.Errorf("FACTORY_AUTO_CLEAR_NOW must be an ISO timestamp, got %s", fixed)
	}
	return epochSeconds(d), d.In(loc).Format("2006-01-02T15:04:05-07:00"), nil
}

func floorDiv60(n int) int {
	q := n / 60
	if n%60 != 0 && n < 0 {
		q--
	}
	return q
}

// autoClearDialog is auto_clear_dialog. It returns true when it sent a key and never returns an error: a failure is one
// "auto-clear-failed" event.
func autoClearDialog(r *reapDB, c *herdr.Client, u *UnitCtx, a *dlgAgent, t float64) bool {
	sent, err := autoClear(r, c, u, a, t)
	if err == nil {
		return sent
	}
	if eerr := r.eventTick("agent", a.name, "auto-clear-failed", err.Error()); eerr != nil {
		fmt.Fprintf(os.Stderr, "factory: auto-clear-failed event for %s not recorded: %v\n", a.name, eerr)
	}
	return false
}

func autoClear(r *reapDB, c *herdr.Client, u *UnitCtx, a *dlgAgent, t float64) (bool, error) {
	name := a.name
	text, _, err := paneText(c, name, paneReadSourceRecent, paneReadLines)
	if err != nil {
		return false, err
	}
	kind, err := classifyDialog(text)
	if err != nil {
		return false, err
	}
	if kind.kind == "unknown" {
		return false, nil
	}
	if a.role == "boss" {
		unsent, err := hasUnsentInput(text)
		if err != nil || unsent {
			return false, err
		}
	}
	goal, task := optStr(a.goal), optStr(a.task)
	if kind.kind == "settings-warning" {
		return clearSettingsWarning(r, c, a, kind, t, goal, task)
	}
	hq := !workerRoles[a.role]
	key := "blocked_alert_s"
	if hq {
		key = "hq_unblock_s"
	}
	raw, err := r.Cfg(key)
	if err != nil {
		return false, err
	}
	limit, err := pyIntStr(raw)
	if err != nil {
		return false, err
	}
	if !a.blockedSince.Valid || a.blockedSince.Float64 == 0 || t-a.blockedSince.Float64 <= float64(limit) {
		return false, nil
	}
	ok, err := dialogNow(r, c, name, a.role, "permission")
	if err != nil || !ok {
		return false, err
	}
	if _, err := r.DB.Exec("UPDATE agents SET blocked_since=? WHERE name=?", t, name); err != nil {
		return false, fmt.Errorf("re-arm blocked_since of %s: %w", name, err)
	}
	if err := r.eventTick("agent", name, "unblock", store.Ordered{{K: "by", V: "tick"}, {K: "reason", V: "auto-deny after timeout"}}); err != nil {
		return false, err
	}
	mins := floorDiv60(limit)
	if hq {
		if _, err := c.Herdr(false, "agent", "send-keys", name, escKey); err != nil {
			return false, err
		}
		body := fmt.Sprintf("%s sat on a permission dialog for over %d min; the tick denied it once (Esc). "+
			"Do not retry it and never pick an option that changes user settings (L-021).", name, mins)
		if err := r.tickSend(name, "UNBLOCK (auto)", body, "decision", goal, task, ""); err != nil {
			return false, err
		}
		to := "director"
		if a.role == "director" {
			to = "boss"
		}
		org, err := r.AgentOrg(name)
		if err != nil {
			return false, err
		}
		return true, r.tickSend(to, name+" permission dialog auto-denied", body, "alert", nil, nil, org)
	}
	order := fmt.Sprintf("Your permission request was denied once (Esc) by the tick after %d min. Do not retry it and never "+
		"pick an option that changes user settings (L-021). Stay in your working directory and scratchpad; continue your "+
		"task, or ask your lead.", mins)
	if err := r.tickSend(name, "UNBLOCK", order, "decision", goal, task, ""); err != nil {
		return false, err
	}
	to, _, err := blockedAlertTarget(r, a)
	if err != nil {
		return false, err
	}
	if err := r.tickSend(to, name+" permission dialog auto-denied",
		fmt.Sprintf("%s: denied once (Esc) by the tick after %d min with no answer.", name, mins), "alert", goal, task, ""); err != nil {
		return false, err
	}
	err = escWaitNudgeDeferred(r, c, u, name, "📬 UNBLOCK from tick: run factory inbox now and continue your task.", "UNBLOCK order")
	return true, err
}

func clearSettingsWarning(r *reapDB, c *herdr.Client, a *dlgAgent, kind dialogClass, t float64, goal, task *string) (bool, error) {
	name, sfile, warning := a.name, kind.file, kind.warning
	t, stamp, err := autoClearClock(r, t)
	if err != nil {
		return false, err
	}
	day := truncRunes(stamp, 10)
	rows, err := r.DB.Query("SELECT ts FROM events WHERE entity='agent' AND entity_id=? AND "+
		"action='settings-warning-dismissed' AND ts LIKE ? ORDER BY id DESC", name, day+"%")
	if err != nil {
		return false, fmt.Errorf("read settings-warning-dismissed events of %s: %w", name, err)
	}
	var prior []string
	for rows.Next() {
		var ts string
		if err := rows.Scan(&ts); err != nil {
			rows.Close()
			return false, fmt.Errorf("read settings-warning-dismissed event of %s: %w", name, err)
		}
		prior = append(prior, ts)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return false, fmt.Errorf("read settings-warning-dismissed events of %s: %w", name, err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("close settings-warning-dismissed query of %s: %w", name, err)
	}
	if len(prior) > 0 {
		last, ok := pyFromISO(prior[0], processZone())
		if !ok {
			return false, fmt.Errorf("Invalid isoformat string: %s", pyReprStr(prior[0]))
		}
		if t-epochSeconds(last) < settingsEscCooldownS {
			return false, nil
		}
	}
	if len(prior) >= settingsEscMaxPerDay {
		seen, err := exists(r.DB, "SELECT 1 FROM events WHERE entity='agent' AND entity_id=? AND "+
			"action='settings-warning-stuck' AND ts LIKE ?", name, day+"%")
		if err != nil || seen {
			return false, err
		}
		if err := r.EventAs(tickActor, "agent", name, "settings-warning-stuck", store.Ordered{{K: "file", V: sfile}}, stamp); err != nil {
			return false, err
		}
		body := fmt.Sprintf("The tick sent Esc %d times today and the dialog is still there; it sends no more. Clear it by hand: %s\n%s",
			len(prior), sfile, warning)
		return false, r.tickSend("director", "Settings Warning stuck on "+name+": "+sfile, body, "alert", goal, task, "")
	}
	ok, err := dialogNow(r, c, name, a.role, "settings-warning")
	if err != nil || !ok {
		return false, err
	}
	if _, err := c.Herdr(false, "agent", "send-keys", name, escKey); err != nil {
		return false, err
	}
	if err := r.EventAs(tickActor, "agent", name, "settings-warning-dismissed",
		store.Ordered{{K: "file", V: sfile}, {K: "warning", V: warning}}, stamp); err != nil {
		return false, err
	}
	sum := sha256.Sum256([]byte(sfile + "|" + kind.key))
	digest := hex.EncodeToString(sum[:])[:16]
	seen, err := exists(r.DB, "SELECT 1 FROM events WHERE entity='settings-warning' AND entity_id=? AND ts LIKE ?", digest, day+"%")
	if err != nil || seen {
		return true, err
	}
	if err := r.EventAs(tickActor, "settings-warning", digest, "alerted",
		store.Ordered{{K: "agent", V: name}, {K: "file", V: sfile}}, stamp); err != nil {
		return true, err
	}
	body := "Claude Code skipped settings values; the tick dismissed the dialog (Esc). Fix the root cause: " + sfile + "\n" + warning
	return true, r.tickSend("director", "Settings Warning on "+name+": "+sfile, body, "alert", goal, task, "")
}
