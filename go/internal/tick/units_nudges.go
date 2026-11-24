package tick

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"factory/go/internal/herdr"
	"factory/go/internal/store"
)

// nudgePendingTTLS is NUDGE_PENDING_TTL_S (factory_cli.py:14628): a pending nudge nobody could deliver is dropped
// after this many seconds.
const nudgePendingTTLS = 600

func init() { registerGo("pending_nudges", pendingNudgesUnit) }

// pendingNudgesUnit is _tick_u_pending_nudges (factory_cli.py:18686). by_name and by_pane are NOT written to the
// handoff state: unit 21 recomputes them with reapIndex.
func pendingNudgesUnit(u *UnitCtx) error {
	byName, _, err := reapIndex(u.State)
	if err != nil {
		return err
	}
	t := epochSeconds(reapNow())
	setStateFloat(u.State, "t", t)
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
	if err := finishPendingNudges(r, h, byName, t); err != nil {
		return err
	}
	return st.Err()
}

type pendingNudge struct {
	id       int64
	name, ts string
	detail   sql.NullString
}

// finishPendingNudges is finish_pending_nudges (factory_cli.py:14631): at most one host call per pending nudge.
func finishPendingNudges(r *reapDB, c herdrCaller, byName map[string]herdr.Agent, t float64) error {
	rs, err := r.DB.Query("SELECT id, entity_id, ts, detail FROM events WHERE action='nudge-pending' AND NOT EXISTS " +
		"(SELECT 1 FROM events d WHERE d.action='nudge-done' AND d.entity_id=events.entity_id AND " +
		"json_extract(d.detail, '$.pending')=events.id)")
	if err != nil {
		return fmt.Errorf("read pending nudges: %w", err)
	}
	var pending []pendingNudge
	for rs.Next() {
		var p pendingNudge
		if err := rs.Scan(&p.id, &p.name, &p.ts, &p.detail); err != nil {
			rs.Close()
			return fmt.Errorf("read pending nudges: %w", err)
		}
		pending = append(pending, p)
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return fmt.Errorf("read pending nudges: %w", err)
	}
	if err := rs.Close(); err != nil {
		return fmt.Errorf("read pending nudges: %w", err)
	}
	for _, p := range pending {
		if err := finishNudge(r, c, byName, t, p); err != nil {
			return err
		}
	}
	return nil
}

func finishNudge(r *reapDB, c herdrCaller, byName map[string]herdr.Agent, t float64, p pendingNudge) error {
	d := map[string]any{}
	age := float64(nudgePendingTTLS + 1)
	if p.detail.Valid {
		var v any
		if err := json.Unmarshal([]byte(p.detail.String), &v); err == nil {
			if ts, ok := pyFromISO(p.ts, processZone()); ok {
				m, isMap := v.(map[string]any)
				if !isMap {
					return fmt.Errorf("nudge-pending event %d: detail is not a JSON object", p.id)
				}
				d, age = m, t-epochSeconds(ts)
			}
		}
	}
	var status, pane, started sql.NullString
	liveRow := true
	err := r.DB.QueryRow("SELECT status, pane_id, started FROM agents WHERE name=?", p.name).Scan(&status, &pane, &started)
	if errors.Is(err, sql.ErrNoRows) {
		liveRow = false
	} else if err != nil {
		return fmt.Errorf("read agent %s: %w", p.name, err)
	}
	st, _ := byName[p.name]["agent_status"].(string)
	nudge, _ := d["nudge"].(string)
	outcome := "dropped"
	switch {
	case !liveRow || status.String != "live" || age > nudgePendingTTLS || !pyTruthy(d["nudge"]):
	case !pyTruthy(d["pane_id"]) || !sameStr(d["pane_id"], pane) || !pyTruthy(d["started"]) || !sameStr(d["started"], started):
		if err := r.eventTick("agent", p.name, "nudge-mismatch", store.Ordered{
			{K: "pending", V: p.id}, {K: "pane_id", V: d["pane_id"]}, {K: "started", V: d["started"]},
			{K: "live_pane_id", V: nullable(pane)}, {K: "live_started", V: nullable(started)}}); err != nil {
			return err
		}
	case st == "idle" || st == "done":
		if _, err := c.Herdr(false, "agent", "prompt", p.name, nudge); err != nil {
			return fmt.Errorf("agent prompt %s: %w", p.name, err)
		}
		outcome = "sent"
	default:
		return nil // still working or blocked: try again next tick
	}
	return r.eventTick("agent", p.name, "nudge-done", store.Ordered{{K: "pending", V: p.id}, {K: "outcome", V: outcome}})
}

// sameStr is `d.get(k) == row[k]` for a stored TEXT column.
func sameStr(v any, s sql.NullString) bool {
	x, ok := v.(string)
	return ok && s.Valid && x == s.String
}
