package tick

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
)

// AsidePyLockBusy reports whether the FACTORY_IMPL=diff aside tree is flock-held. The harness sets FACTORY_HOME to
// <tmpdir>/factory, renames <tmpdir> to <tmpdir>-py for Python's run, then copies the snapshot back to <tmpdir> for Go;
// tick.lock stays held on <tmpdir>-py/factory/run/tick.lock.
func AsidePyLockBusy(home string) bool {
	parent := filepath.Dir(home)
	asideRoot := filepath.Join(filepath.Dir(parent), filepath.Base(parent)+"-py")
	aside := filepath.Join(asideRoot, filepath.Base(home), "run", LockName)
	f, err := os.OpenFile(aside, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return false
	}
	return errors.Is(err, syscall.EWOULDBLOCK)
}