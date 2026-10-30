package guard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

// Decisions that read the factory DB (own_goal, the test_sandbox config, public_text_repos / frozen_repos, the
// coder's task worktree) are made by the Python hook itself: Go opens no sqlite (stdlib only). Where the DB file
// does not exist, Python's read fails and the rule takes its "no DB" branch, which Go takes too without
// delegating; where it exists, the whole payload is re-run through the Python hook and its exit code and stderr
// are returned unchanged (owngoal.go:RunPython). A failure to run it is exit 2.

// Delegate is the error that means "the Python hook decides this payload". It is never swallowed (HQ mode's
// soft() keeps it), so the Python answer replaces the Go one whatever the Go rules would have said later.
type Delegate struct{ Why string }

func (d *Delegate) Error() string { return "delegated to the Python hook: " + d.Why }

func delegate(why string) error { return &Delegate{Why: why} }

// IsDelegate reports whether err asks for the Python hook.
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
// DB read of the hook then takes its failure branch (None / False / {}), so Go decides without Python.
func dbMissing() bool { return !pathExists(dbPath()) }

// ownGoalKnown is true when Analyzer.own_goal() is known to be None without the DB: no FACTORY_AGENT, or no DB file.
// False means the goal must be read, so the decision is the Python hook's.
func ownGoalKnown() bool {
	return os.Getenv("FACTORY_AGENT") == "" || dbMissing()
}

// pyRunTimeout bounds one delegated run (the hook's own DB read waits 2 s, its git calls 10 s each).
const pyRunTimeout = 120 * time.Second

// RunPython runs the Python hook on the same arguments and stdin and returns its exit code, with stdout and
// stderr passed through. args is the binary's argv without `--py-hook <path>`. Any failure to run it (no hook
// path, unreadable file, no python3, timeout, killed) is exit 2 with a message that says what to do.
func RunPython(args []string, stdin []byte, stdout, stderr io.Writer) int {
	if PyHook == "" {
		fmt.Fprintln(stderr, "worker-guard(go): blocked (hook error, failing closed: this decision needs the Python hook but "+
			"--py-hook <path to roles/hooks/worker-guard.py> was not given; the shim must pass it)")
		return 2
	}
	f, err := os.Open(PyHook)
	if err != nil {
		fmt.Fprintf(stderr, "worker-guard(go): blocked (hook error, failing closed: cannot read the Python hook %s: %v)\n", PyHook, err)
		return 2
	}
	f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), pyRunTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", append([]string{PyHook}, args...)...)
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stdout, cmd.Stderr = stdout, stderr
	err = cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee) && ee.ExitCode() >= 0 && ctx.Err() == nil:
		if ee.ExitCode() != 2 {
			// the hook only ever exits 0 or 2; anything else (a crash is 1) would not block, so it becomes 2
			fmt.Fprintf(stderr, "worker-guard(go): blocked (hook error, failing closed: the Python hook exited %d)\n", ee.ExitCode())
		}
		return 2
	}
	fmt.Fprintf(stderr, "worker-guard(go): blocked (hook error, failing closed: running the Python hook failed: %v)\n", err)
	return 2
}
