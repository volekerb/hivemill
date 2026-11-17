package tick

import (
	"database/sql"
	"fmt"

	"factory/go/internal/store"
)

// The reap units (20-26) share the DB helpers of internal/store (cfg, event_as, send with recipient/org resolution,
// Python's json.dumps). This file only fixes the tick's identity and event clock.

// Python's reap code writes with two actors. event_as(con, "tick", ...) and send(sender="tick") (finish_pending_nudges,
// reap_exited_panes, reap_idle_coders, reap_agent_tests, auto_clear_dialog) use tickActor; plain event() is
// actor(), which is "owner" because the Go tick runs only as the owner (the inline reap loop, close_named_pane,
// _hc_kill_terminal, tick_drift, flagless_scan, claude_outside_spawn).
const (
	tickActor  = "tick"
	ownerActor = pruneActor
)

// reapDB is the store with the tick's event clock.
type reapDB struct{ *store.Store }

// newReapDB returns the helper over db. The event clock is the S2 clock the prune unit uses: FACTORY_TEST_NOW on a
// non-live home, else the real clock; an invalid FACTORY_TEST_NOW is an error, as before any Python write.
func newReapDB(db *sql.DB) (*reapDB, error) {
	now, err := pruneEventClock(pruneIdentity())
	if err != nil {
		return nil, fmt.Errorf("event clock: %w", err)
	}
	return &reapDB{&store.Store{DB: db, Clock: now}}, nil
}

// eventTick is event_as(con, "tick", ...).
func (r *reapDB) eventTick(entity, entityID, action string, detail any) error {
	return r.EventAs(tickActor, entity, entityID, action, detail, "")
}

// eventOwner is Python's event(): the actor is actor(), "owner".
func (r *reapDB) eventOwner(entity, entityID, action string, detail any) error {
	return r.EventAs(ownerActor, entity, entityID, action, detail, "")
}

// strp is a *string for store.Msg fields.
func strp(s string) *string { return &s }
