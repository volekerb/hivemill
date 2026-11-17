package tick

import (
	"database/sql"
	"fmt"

	"factory/go/internal/store"
)

// The reap units (20-26) share the DB helpers of internal/store (cfg, event_as, send with recipient/org resolution,
// Python's json.dumps). This file only fixes the tick's identity and event clock.

// reapActor is the actor of every row the reap units write: Python passes sender="tick" and event_as(con, "tick", ...).
const reapActor = "tick"

// reapDB is the store with the tick as the actor of its events.
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

// event is event_as with the tick as actor.
func (r *reapDB) event(entity, entityID, action string, detail any) error {
	return r.EventAs(reapActor, entity, entityID, action, detail, "")
}

// strp is a *string for store.Msg fields.
func strp(s string) *string { return &s }
