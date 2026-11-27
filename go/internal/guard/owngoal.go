package guard

import (
	"errors"
	"os"
)

// Decisions that read the factory DB (own_goal, the test_sandbox config, the coder's task worktree, public_text_repos /
// frozen_repos) are made from the guard export (guardexport.go); Go opens no sqlite (stdlib only). Where the DB file
// does not exist, Python's read fails and the rule takes its "no DB" branch, which Go takes too; a DB value the export
// has no row for is absent, as Python's query returning no row is. The Go guard decides every payload itself and never
// runs the Python hook (T-2112).

// Delegate is the fail-closed refusal: the decision needs the guard export and the export is unusable, so the binary
// exits 2 (closedExit). It is never swallowed (HQ mode's soft() keeps it), so it replaces whatever the Go rules would
// have said later. Closed is always true; the name stays from the time a Delegate could hand a payload to Python.
type Delegate struct {
	Why    string
	Closed bool
}

func (d *Delegate) Error() string { return d.Why }

// IsClosed reports whether err is a Delegate that is really a fail-closed refusal (guard export unusable).
func IsClosed(err error) bool {
	var d *Delegate
	return errors.As(err, &d) && d.Closed
}

// IsDelegate reports whether err is a Delegate (every Delegate is a fail-closed refusal).
func IsDelegate(err error) bool {
	var d *Delegate
	return errors.As(err, &d)
}

// dbPath is a port of Analyzer.db_path: FACTORY_HOME/db/factory.db, else the legacy FACTORY_HOME/factory.db
// (no env var but FACTORY_HOME picks the DB), FACTORY_HOME defaulting to LIVE_HOME.
func dbPath() string {
	home := os.Getenv("FACTORY_HOME")
	if home == "" {
		home = mustHost().liveHome
	}
	nw, legacy := pyJoin(pyJoin(home, "db"), "factory.db"), pyJoin(home, "factory.db")
	if pathExists(nw) || !pathExists(legacy) {
		return nw
	}
	return legacy
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// dbMissing is true when sqlite3.connect(file:<db>?mode=ro) cannot succeed because the file is not there: every
// DB read of the hook then takes its failure branch (None / False / {}), and Go takes the same branch.
func dbMissing() bool { return !pathExists(dbPath()) }
