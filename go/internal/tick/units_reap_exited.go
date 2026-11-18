package tick

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"

	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

// Unit exited_panes (_tick_u_exited_panes -> reap_exited_panes in bin/factory_cli.py). At most every 10 minutes, per
// config exited_pane_reap_mode ('dry-run' by default, and for any unknown value): 'dry-run' logs one
// "exited-pane-would-close" event per (agent, pane); 'on' closes the pane, confirms the host no longer lists it and
// logs "exited-pane-closed"; a failed close logs one "exited-pane-close-failed" per (agent, pane). A pane qualifies only
// when the host lists it under the exact name of an agents row that, re-read just before, is still 'exited' for more
// than 10 minutes and has a worker role, no live/starting/queued/stopped row holds the host's pane id, and the host
// pane's agent_session, when it names one, is the row's session_id. With no session the pane closes only if its
// agent_status is done/idle and its cwd equals the row's recorded cwd and no other live/starting row has that cwd.

const (
	exitedPaneThrottleS = 600 // EXITED_PANE_THROTTLE_S
	exitedPaneMinAgeS   = 600 // EXITED_PANE_MIN_AGE_S
	exitedPaneReapKey   = "exitedpanereap"
)

var exitedPaneRoles = map[string]bool{"coder": true, "qa": true, "reviewer": true, "pm": true, "judge": true, "cleaner": true}

func init() { registerGo("exited_panes", exitedPanesUnit) }

func exitedPanesUnit(u *UnitCtx) error {
	agents, _, err := stateAgents(u.State)
	if err != nil {
		return err
	}
	db, closeDB, err := pruneOpen(u)
	if err != nil {
		return err
	}
	defer closeDB()
	r, err := newReapDB(db)
	if err != nil {
		return err
	}
	h, st := newHerdrClient(r)
	rerr := reapExitedPanes(r, h, agents)
	if rerr == nil {
		rerr = st.Err()
	}
	if rerr != nil {
		// housekeeping must never stop the tick: one failure event, the tick goes on
		if err := r.eventTick("tick", "exited-pane-reap", "exited-pane-reap-failed", rerr.Error()); err != nil {
			fmt.Fprintf(os.Stderr, "factory: exited-pane-reap-failed event not recorded: %v\n", err)
		}
	}
	return nil
}

// herdrCaller is the herdr client as the reap units use it.
type herdrCaller interface {
	Herdr(check bool, args ...string) (any, error)
}

var _ herdrCaller = (*herdr.Client)(nil)

// pyTruthy is Python truthiness for a decoded JSON value.
func pyTruthy(v any) bool {
	switch x := v.(type) {
	case nil:
		return false
	case bool:
		return x
	case string:
		return x != ""
	case float64:
		return x != 0
	case []any:
		return len(x) > 0
	case map[string]any:
		return len(x) > 0
	}
	return true
}

// pyTypeName is type(x).__name__ for a decoded JSON value, for the messages of Python's AttributeError.
func pyTypeName(v any) string {
	switch v.(type) {
	case nil:
		return "NoneType"
	case bool:
		return "bool"
	case string:
		return "str"
	case float64:
		return "float"
	case []any:
		return "list"
	}
	return "object"
}

// paneNorm is norm(): a " (deleted)" suffix is dropped, then os.path.normpath.
func paneNorm(c string) string { return pyNormpath(strings.TrimSuffix(c, " (deleted)")) }

// paneListMaps returns r["panes"] when r is an object with a list "panes", ok=false otherwise.
func paneListMaps(r any) (panes []any, ok bool) {
	m, isMap := r.(map[string]any)
	if !isMap {
		return nil, false
	}
	l, isList := m["panes"].([]any)
	return l, isList
}

// paneIdleInCwd is _pane_idle_in_cwd: host agent h is done or idle (never working) and its pane's cwd equals the row's
// recorded cwd. Anything unknown is false: keep the pane.
func paneIdleInCwd(c herdrCaller, h herdr.Agent, pane, rowCwd string) (bool, error) {
	if s, _ := h["agent_status"].(string); (s != "done" && s != "idle") || rowCwd == "" {
		return false, nil
	}
	res, err := c.Herdr(false, "pane", "list")
	if err != nil {
		return false, err
	}
	panes, ok := paneListMaps(res)
	if !ok {
		return false, nil
	}
	want := paneNorm(rowCwd)
	for _, x := range panes {
		p, isMap := x.(map[string]any)
		if !isMap {
			return false, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(x))
		}
		for _, k := range []string{"cwd", "foreground_cwd"} {
			if id, _ := p["pane_id"].(string); id != pane {
				continue
			}
			if s, isStr := p[k].(string); isStr && s != "" && paneNorm(s) == want {
				return true, nil
			}
		}
	}
	return false, nil
}

// paneGone is pane_gone(pane): gone=true when herdr no longer lists the pane, false when it does; known=false when
// herdr cannot be asked.
func paneGone(c herdrCaller, pane string) (gone, known bool, err error) {
	res, err := c.Herdr(false, "pane", "list")
	if err != nil {
		return false, false, err
	}
	panes, ok := paneListMaps(res)
	if !ok {
		if m, isMap := res.(map[string]any); isMap {
			if _, has := m["panes"]; has {
				return false, false, errors.New("herdr pane list: panes is not a list")
			}
		}
		wsRes, err := c.Herdr(false, "workspace", "list")
		if err != nil {
			return false, false, err
		}
		wm, isMap := wsRes.(map[string]any)
		if !isMap {
			return false, false, nil
		}
		wl, has := wm["workspaces"]
		if !has {
			return false, false, nil
		}
		wsl, isList := wl.([]any)
		if !isList {
			return false, false, errors.New("herdr workspace list: workspaces is not a list")
		}
		panes = nil
		for _, w := range wsl {
			wmap, isMap := w.(map[string]any)
			if !isMap {
				return false, false, fmt.Errorf("'%s' object is not subscriptable", pyTypeName(w))
			}
			id, has := wmap["workspace_id"]
			if !has {
				return false, false, errors.New("'workspace_id'")
			}
			ids, _ := id.(string)
			pr, err := c.Herdr(false, "pane", "list", "--workspace", ids)
			if err != nil {
				return false, false, err
			}
			pl, ok := paneListMaps(pr)
			if !ok {
				return false, false, nil
			}
			panes = append(panes, pl...)
		}
	}
	for _, x := range panes {
		p, isMap := x.(map[string]any)
		if !isMap {
			return false, false, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(x))
		}
		if id, _ := p["pane_id"].(string); id == pane {
			return false, true, nil
		}
	}
	return true, true, nil
}

// reapExitedPanes is reap_exited_panes without its catch-all: the error is what the caller records.
func reapExitedPanes(r *reapDB, c herdrCaller, agents []herdr.Agent) error {
	v, err := r.Cfg("exited_pane_reap_mode")
	if err != nil {
		return err
	}
	mode := reapMode(v)
	if mode == "off" || len(agents) == 0 {
		return nil
	}
	t := epochSeconds(reapNow())
	var lastTS string
	switch err := r.DB.QueryRow("SELECT ts FROM throttle WHERE key=?", exitedPaneReapKey).Scan(&lastTS); {
	case err == nil:
		last, ok := pyFloatStr(lastTS)
		if !ok {
			return fmt.Errorf("could not convert string to float: %s", pyReprStr(lastTS))
		}
		if t-last < exitedPaneThrottleS {
			return nil
		}
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("read throttle: %w", err)
	}
	if _, err := r.DB.Exec("INSERT INTO throttle(key,ts) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET ts=excluded.ts",
		exitedPaneReapKey, store.PyFloatRepr(t)); err != nil {
		return fmt.Errorf("write throttle: %w", err)
	}
	for _, h := range agents {
		name, pane := h.Name(), h.PaneID()
		if name == "" || pane == "" {
			continue
		}
		var role, status string
		var session, cwd, ended sql.NullString
		// re-read in this tick, right before acting
		err := r.DB.QueryRow("SELECT role,status,session_id,cwd,ended FROM agents WHERE name=?", name).
			Scan(&role, &status, &session, &cwd, &ended)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("read agent %s: %w", name, err)
		}
		if status != "exited" || !exitedPaneRoles[role] {
			continue // match on the exact name only: the row's pane_id can be stale, so act on the host's pane id
		}
		sess, err := hostSession(h)
		if err != nil {
			return err
		}
		if pyTruthy(sess) {
			if s, isStr := sess.(string); !isStr || !session.Valid || s != session.String {
				continue // the pane runs another session: never ours to close
			}
		}
		branch := "cwd"
		if pyTruthy(sess) {
			branch = "session"
		}
		if !pyTruthy(sess) {
			idle, err := paneIdleInCwd(c, h, pane, cwd.String)
			if err != nil {
				return err
			}
			if !idle {
				continue // session unknown: only an idle/done pane in this row's own cwd is provably its leftover
			}
			found, err := exists(r.DB, "SELECT 1 FROM agents WHERE cwd=? AND name!=? AND status IN ('live','starting')", cwd.String, name)
			if err != nil {
				return err
			}
			if found {
				continue // T-1118: a live peer shares the cwd, so the cwd cannot tell the panes apart
			}
		}
		end, ok := pyFromISO(ended.String, processZone())
		if !ended.Valid || !ok || reapNow().Sub(end).Seconds() <= exitedPaneMinAgeS {
			continue
		}
		taken, err := exists(r.DB, "SELECT 1 FROM agents WHERE pane_id=? AND name!=? AND status IN ('live','starting','queued','stopped')", pane, name)
		if err != nil {
			return err
		}
		if taken {
			continue // the pane id was handed to a newer live agent
		}
		gone, known, err := paneGone(c, pane)
		if err != nil {
			return err
		}
		if !known || gone {
			continue // the tick's list is stale or the host cannot be asked
		}
		detail := store.Ordered{{K: "agent", V: name}, {K: "pane", V: pane}, {K: "role", V: role},
			{K: "ended", V: nullable(ended)}, {K: "branch", V: branch}}
		keyJSON, err := store.PyDumps(store.Ordered{{K: "agent", V: name}, {K: "pane", V: pane}})
		if err != nil {
			return fmt.Errorf("event key: %w", err)
		}
		key := keyJSON[:len(keyJSON)-1] + ","
		seen := func(action string) (bool, error) {
			return exists(r.DB, "SELECT 1 FROM events WHERE action=? AND substr(detail,1,?)=?", action, len([]rune(key)), key)
		}
		if mode == "dry-run" {
			was, err := seen("exited-pane-would-close")
			if err != nil {
				return err
			}
			if !was {
				if err := r.EventAs(tickActor, "agent", name, "exited-pane-would-close", detail, ""); err != nil {
					return err
				}
			}
			continue
		}
		if _, err := c.Herdr(false, "pane", "close", pane); err != nil {
			return err
		}
		gone, known, err = paneGone(c, pane)
		if err != nil {
			return err
		}
		if known && gone {
			if err := r.EventAs(tickActor, "agent", name, "exited-pane-closed", detail, ""); err != nil {
				return err
			}
			continue
		}
		was, err := seen("exited-pane-close-failed")
		if err != nil {
			return err
		}
		if !was {
			if err := r.EventAs(tickActor, "agent", name, "exited-pane-close-failed", detail, ""); err != nil {
				return err
			}
		}
	}
	return nil
}

// hostSession is `(h.get("agent_session") or {}).get("value")`: nil when the host names no session. A truthy
// agent_session that is not an object is Python's AttributeError.
func hostSession(h herdr.Agent) (any, error) {
	as := h["agent_session"]
	if !pyTruthy(as) {
		return nil, nil
	}
	m, ok := as.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("'%s' object has no attribute 'get'", pyTypeName(as))
	}
	return m["value"], nil
}

func exists(db *sql.DB, q string, args ...any) (bool, error) {
	var one int
	err := db.QueryRow(q, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("query %q: %w", q, err)
	}
	return true, nil
}

// nullable is a NullString as an event detail value (NULL is JSON null).
func nullable(s sql.NullString) any {
	if !s.Valid {
		return nil
	}
	return s.String
}
