package tick

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode"

	"factory/go/internal/cursorharness"
	"factory/go/internal/identity"
	"factory/go/internal/store"
)

// Unit 25 flagless (_tick_u_flagless, flagless_scan with throttle=True, flagless_state, notify_flagless in the Python
// CLI): a live agent whose claude process lacks its role --settings flags (herdr resumed a bare `claude --resume`) is
// flagged. Only agents.flagless / flagless_since change; nothing is killed or closed.

const (
	flaglessUnit     = "flagless"
	flaglessCheckS   = 3600 // per-agent throttle "flagless_check:<name>"
	flaglessThrottle = "flagless_check:"
)

func init() { registerGo(flaglessUnit, runFlagless) }

// reapTime is time.time(): the wall clock of the throttles (Python ignores FACTORY_NOW here). Tests replace it.
var reapTime = func() float64 { return float64(time.Now().UnixNano()) / 1e9 }

// agentRow is the part of an agents row the two units read.
type agentRow struct {
	Name, Role, SpawnArgs, Cwd string
	Pane, Session              sql.NullString
	Task, Goal                 sql.NullString
	Flagless                   bool
}

// liveAgentRows is `SELECT * FROM agents WHERE status='live' AND session_id IS NOT NULL`, read fully first (fetchall).
func liveAgentRows(db *sql.DB) ([]agentRow, error) {
	rs, err := db.Query("SELECT name, role, spawn_args, cwd, pane_id, session_id, task_id, goal_id, flagless " +
		"FROM agents WHERE status='live' AND session_id IS NOT NULL")
	if err != nil {
		return nil, fmt.Errorf("read live agents: %w", err)
	}
	defer rs.Close()
	var out []agentRow
	for rs.Next() {
		var r agentRow
		var role, sa, cwd sql.NullString
		var fl sql.NullFloat64
		if err := rs.Scan(&r.Name, &role, &sa, &cwd, &r.Pane, &r.Session, &r.Task, &r.Goal, &fl); err != nil {
			return nil, fmt.Errorf("read live agents: %w", err)
		}
		r.Role, r.SpawnArgs, r.Cwd, r.Flagless = role.String, sa.String, cwd.String, fl.Valid && fl.Float64 != 0
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, fmt.Errorf("read live agents: %w", err)
	}
	return out, nil
}

func nullAny(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}

// throttleFloat is the ts of a throttle row as float(ts); has=false for no row. A value float() rejects is an error
// (Python ValueError); nan and inf parse like Python.
func throttleFloat(db *sql.DB, key string) (ts float64, has bool, err error) {
	var raw sql.NullString
	e := db.QueryRow("SELECT ts FROM throttle WHERE key=?", key).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		return 0, false, nil
	}
	if e != nil {
		return 0, false, fmt.Errorf("read throttle %s: %w", key, e)
	}
	if !raw.Valid {
		return 0, false, fmt.Errorf("throttle %s has a NULL ts", key)
	}
	f, ok := parsePyFloat(raw.String)
	if !ok {
		return 0, false, fmt.Errorf("could not convert string to float: %s", pyRepr(raw.String)) // Python's ValueError text
	}
	return f, true, nil
}

// parsePyFloat is float(str): the finite values of pyFloatStr plus nan and inf, which Python accepts.
func parsePyFloat(s string) (float64, bool) {
	if f, ok := pyFloatStr(s); ok {
		return f, true
	}
	trimmed := strings.TrimFunc(s, unicode.IsSpace)
	switch strings.ToLower(strings.TrimLeft(trimmed, "+-")) {
	case "nan", "inf", "infinity":
		if f, err := strconv.ParseFloat(trimmed, 64); err == nil {
			return f, true
		}
	}
	return 0, false
}

// ownerEvent is Python event(): the actor is the identity the Go tick runs as, the owner (the tick hands every other
// identity to Python). reapDB.event is event_as(.., "tick", ..), which the host helpers use.
func (r *reapDB) ownerEvent(entity, entityID, action string, detail any) error {
	return r.EventAs(pruneActor, entity, entityID, action, detail, "")
}

func setThrottle(db *sql.DB, key string, ts float64) error {
	if _, err := db.Exec("INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
		key, store.PyFloatRepr(ts)); err != nil {
		return fmt.Errorf("write throttle %s: %w", key, err)
	}
	return nil
}

// agentRuntime is _agent_runtime: the spawn_args runtime, "claude" when absent, empty or unreadable.
func agentRuntime(spawnArgs string) string {
	if rt := store.SpawnRuntime(spawnArgs); rt != "" {
		return rt
	}
	return "claude"
}

// agentCursorWorkspace is agent_cursor_workspace: the cursor_workspace of spawn_args, else the policy workspace of the
// row; "" when unknown (a bad XDG_STATE_HOME or an unreadable policy dir decides nothing).
func agentCursorWorkspace(home string, r agentRow) string {
	args, _ := store.SpawnArgsMap(r.SpawnArgs)
	if cw, _ := args["cursor_workspace"].(string); cw != "" {
		return identity.Realpath(cw)
	}
	if r.Name == "" {
		return ""
	}
	cwd := r.Cwd
	if cwd == "" {
		cwd = home
	}
	uh, _ := os.UserHomeDir() // an unknown home only matters when XDG_STATE_HOME is unset: the dir then fails below
	ws, err := cursorharness.PolicyWorkspace(home, r.Name, cwd, cursorharness.StateRoot(os.Getenv, uh))
	if err != nil {
		return ""
	}
	return identity.Realpath(ws)
}

// cursorHooksOK is cursor_hooks_ok: workspace/.cursor/hooks.json is a file, Cursor would load it from here, and every
// hook entry has failClosed true. A document of another shape is not ok.
func cursorHooksOK(workspace string) bool {
	hooks := workspace + "/.cursor/hooks.json"
	if st, err := os.Stat(hooks); err != nil || !st.Mode().IsRegular() {
		return false
	}
	if holder, ok := cursorharness.NearestGitHolder(workspace); ok && holder != identity.Realpath(workspace) {
		return false
	}
	b, err := os.ReadFile(hooks)
	if err != nil {
		return false
	}
	return hooksDocOK(b)
}

func hooksDocOK(b []byte) bool {
	var doc map[string]json.RawMessage
	if json.Unmarshal(b, &doc) != nil {
		return false
	}
	var entries map[string]json.RawMessage
	if raw, ok := doc["hooks"]; !ok || json.Unmarshal(raw, &entries) != nil || len(entries) == 0 {
		return false
	}
	for _, raw := range entries {
		var list []map[string]json.RawMessage
		if json.Unmarshal(raw, &list) != nil {
			return false
		}
		for _, item := range list {
			if item == nil || string(item["failClosed"]) != "true" {
				return false
			}
		}
	}
	return true
}

// hasRoleFlags is has_role_flags: a --settings value ending in /settings/<role>.json.
func hasRoleFlags(argv []string, role string) bool {
	for _, v := range flagValues(argv, "--settings") {
		if strings.HasSuffix(v, "/settings/"+role+".json") {
			return true
		}
	}
	return false
}

// flaglessState is flagless_state: "flagless"|"ok" with the pid, or "unknown" (no live process for the session).
func flaglessState(root, home string, r agentRow) (string, int) {
	if agentRuntime(r.SpawnArgs) == "cursor" {
		ws := agentCursorWorkspace(home, r)
		if ws == "" {
			return "unknown", 0
		}
		procs := cursorProcs(root, ws, identity.Realpath)
		if len(procs) == 0 {
			return "unknown", 0
		}
		for _, p := range procs {
			if w := argvWorkspace(p.Argv); w != "" && cursorHooksOK(w) {
				return "ok", p.Pid
			}
		}
		return "flagless", procs[0].Pid
	}
	if !r.Session.Valid {
		return "unknown", 0
	}
	procs := claudeProcs(root, r.Session.String, nil)
	if len(procs) == 0 {
		return "unknown", 0
	}
	for _, p := range procs {
		if hasRoleFlags(p.Argv, r.Role) {
			return "ok", p.Pid
		}
	}
	return "flagless", procs[0].Pid
}

type flaglessFound struct {
	Name string
	New  bool
}

// flaglessScan is flagless_scan(throttle=True).
func flaglessScan(r *reapDB, home string, rows []agentRow) ([]flaglessFound, error) {
	var out []flaglessFound
	root := ""
	for _, ar := range rows {
		if ar.Role == "evaluator" { // eval sandbox agents run without role flags by design
			continue
		}
		key := flaglessThrottle + ar.Name
		last, has, err := throttleFloat(r.DB, key)
		if err != nil {
			return nil, err
		}
		now := reapTime()
		if has && now-last < flaglessCheckS {
			continue
		}
		if err := setThrottle(r.DB, key, now); err != nil {
			return nil, err
		}
		if root == "" {
			root = reapProcRoot(r)
		}
		state, pid := flaglessState(root, home, ar)
		switch {
		case state == "ok" && ar.Flagless:
			if _, err := r.DB.Exec("UPDATE agents SET flagless=0, flagless_since=NULL WHERE name=?", ar.Name); err != nil {
				return nil, fmt.Errorf("clear flagless of %s: %w", ar.Name, err)
			}
			if err := r.ownerEvent("agent", ar.Name, "flagless-cleared", store.Ordered{{K: "pid", V: pid}}); err != nil {
				return nil, err
			}
		case state == "flagless":
			isNew := !ar.Flagless
			if isNew {
				if _, err := r.DB.Exec("UPDATE agents SET flagless=1, flagless_since=? WHERE name=?", r.Now(), ar.Name); err != nil {
					return nil, fmt.Errorf("flag %s flagless: %w", ar.Name, err)
				}
				d := store.Ordered{{K: "name", V: ar.Name}, {K: "pane", V: nullAny(ar.Pane)}, {K: "pid", V: pid}}
				if err := r.ownerEvent("agent", ar.Name, "flagless", d); err != nil {
					return nil, err
				}
			}
			out = append(out, flaglessFound{ar.Name, isNew})
		}
	}
	return out, nil
}

// notifyFlagless is notify_flagless: one director alert naming the agents.
func notifyFlagless(r *reapDB, found []flaglessFound) error {
	if len(found) == 0 {
		return nil
	}
	names := make([]string, len(found))
	for i, f := range found {
		names[i] = f.Name
	}
	body := "flagless agents after restore: " + strings.Join(names, ", ") + ". " +
		"Relaunch needs owner approval: close pane + factory up (or `factory relaunch <name>`)."
	_, _, err := r.Send(store.Msg{To: "director", Subject: "flagless agents after restore", Body: &body, Kind: "alert", Sender: tickActor})
	if err != nil {
		return fmt.Errorf("flagless alert: %w", err)
	}
	return nil
}

// runFlagless is _tick_u_flagless. Unlike the audit it has no try/except in Python: an error ends the tick.
func runFlagless(u *UnitCtx) error {
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return fmt.Errorf("flagless: %w", err)
	}
	defer closeDB()
	r, err := newReapDB(db)
	if err != nil {
		return fmt.Errorf("flagless: %w", err)
	}
	rows, err := liveAgentRows(db)
	if err != nil {
		return fmt.Errorf("flagless: %w", err)
	}
	found, err := flaglessScan(r, u.Home, rows)
	if err != nil {
		return fmt.Errorf("flagless: %w", err)
	}
	var fresh []flaglessFound
	for _, f := range found {
		if f.New {
			fresh = append(fresh, f)
		}
	}
	if err := notifyFlagless(r, fresh); err != nil {
		return fmt.Errorf("flagless: %w", err)
	}
	return nil
}
