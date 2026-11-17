package tick

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// LockName is the tick/up lock file under FACTORY_HOME/run, shared with the Python side.
const LockName = "tick.lock"

// TakeLock is tick_lock: mkdir run; open run/tick.lock O_WRONLY|O_CREATE|O_TRUNC 0666 (before umask); flock
// LOCK_EX|LOCK_NB. busy=true: another tick or up holds it. The fd is close-on-exec (Go's default), so a Python child
// never inherits it; the lock is held until the returned file is closed or the process exits.
func TakeLock(home string) (f *os.File, busy bool, err error) {
	run := filepath.Join(home, "run")
	if err := os.Mkdir(run, 0o777); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, false, fmt.Errorf("cannot create %s: %w; check FACTORY_HOME permissions", run, err)
	}
	f, err = os.OpenFile(filepath.Join(run, LockName), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return nil, false, fmt.Errorf("cannot open the tick lock: %w; check FACTORY_HOME/run permissions", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("cannot lock %s: %w", f.Name(), err)
	}
	return f, false, nil
}
