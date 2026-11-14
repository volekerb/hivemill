package tick

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestAsidePyLockBusy(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "factory")
	aside := filepath.Join(root+"-py", "factory", "run")
	if err := os.MkdirAll(aside, 0o755); err != nil {
		t.Fatal(err)
	}
	lock := filepath.Join(aside, LockName)
	f, err := os.OpenFile(lock, os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := os.MkdirAll(filepath.Dir(home), 0o755); err != nil {
		t.Fatal(err)
	}
	if !AsidePyLockBusy(home) {
		t.Fatal("want busy when aside holds the tick lock")
	}
}
