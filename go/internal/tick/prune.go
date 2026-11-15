package tick

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"time"
)

// Unit judge_ci_prune (tick_judge_ci_prune in bin/factory_cli.py + the try/except of _tick_u_judge_ci_prune): delete the
// config rows judge_ci:<pr_url> and prview:<pr_url> that nobody can still use. Same statements, same order, autocommit.

const (
	pruneAgeS     = 7 * 86400 // JUDGE_CI_PRUNE_AGE_S
	pruneActor    = "owner"   // the Go tick runs only as the owner (cmd_tick hands every other identity to Python)
	pruneErrorMax = 300       // Python str(e)[:300]
)

// pruneNow is the clock of the unit and pruneEventTime the one of its events rows; tests replace them.
var (
	pruneNow       = func() (float64, error) { return pyClockNow(os.Getenv, time.Now, nil) }
	pruneEventTime = time.Now
)

func init() { registerGo("judge_ci_prune", pruneUnitFn) }

func pruneUnitFn(u *UnitCtx) error {
	n, err := pruneRun(u.DB)
	if err != nil {
		// a bad cache row or a clock error must never stop the tick: one error event, the tick goes on
		return pruneEvent(u.DB, "judge-ci-prune-error", truncRunes(err.Error(), pruneErrorMax))
	}
	if n > 0 {
		return pruneEvent(u.DB, "judge-ci-pruned", fmt.Sprint(n))
	}
	return nil
}

func pruneEvent(db *sql.DB, action, detail string) error {
	_, err := db.Exec("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
		pruneEventTime().Format("2006-01-02T15:04:05-07:00"), pruneActor, "tick", "judge-ci-prune", action, detail)
	return err
}

func truncRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// pruneRun deletes the dead rows and returns how many. now comes first, as in Python: a bad FACTORY_NOW fails before
// any statement.
func pruneRun(db *sql.DB) (int, error) {
	now, err := pruneNow()
	if err != nil {
		return 0, err
	}
	open := map[string]bool{}
	rs, err := db.Query("SELECT DISTINCT pr_url FROM tasks WHERE pr_url IS NOT NULL AND status NOT IN ('done','cancelled')")
	if err != nil {
		return 0, err
	}
	for rs.Next() {
		var u string
		if err := rs.Scan(&u); err != nil {
			rs.Close()
			return 0, err
		}
		open[u] = true
	}
	if err := rs.Err(); err != nil {
		rs.Close()
		return 0, err
	}
	rs.Close()
	rows, err := db.Query("SELECT key, value FROM config WHERE substr(key, 1, 9) = 'judge_ci:' OR substr(key, 1, 7) = 'prview:'")
	if err != nil {
		return 0, err
	}
	var dead []string
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			rows.Close()
			return 0, err
		}
		url := key[indexColon(key)+1:]
		if pruneStale(value, now) || !open[url] {
			dead = append(dead, key)
		}
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close() // one connection: the DELETEs wait for the SELECT to finish
	for _, k := range dead {
		if _, err := db.Exec("DELETE FROM config WHERE key=?", k); err != nil {
			return 0, err
		}
	}
	return len(dead), nil
}

func indexColon(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ':' {
			return i
		}
	}
	return -1
}

// pruneStale: unreadable value, non-finite "at", or older than 7 days (exactly 7 days is kept).
func pruneStale(value string, now float64) bool {
	at, ok := pyFloatAt(value)
	return !ok || math.IsNaN(at) || now-at > pruneAgeS
}
