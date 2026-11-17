//go:build linux

package procx

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// fakeProc builds a /proc-shaped tree: pid -> cwd target.
func fakeProc(t *testing.T, cwds map[string]string) procfs {
	t.Helper()
	root := t.TempDir()
	for pid, cwd := range cwds {
		if err := os.MkdirAll(filepath.Join(root, pid, "fd"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(cwd, filepath.Join(root, pid, "cwd")); err != nil {
			t.Fatal(err)
		}
		os.Symlink("/data/held.tmp", filepath.Join(root, pid, "fd", "3"))
		os.Symlink("socket:[123]", filepath.Join(root, pid, "fd", "4"))
	}
	for pid := range cwds {
		os.WriteFile(filepath.Join(root, pid, "status"), []byte("Name:\tx\nUid:\t1000\t1000\t1000\t1000\n"), 0o644)
	}
	return procfs{root: root, readlink: os.Readlink, readDir: os.ReadDir, readFile: os.ReadFile, euid: 1000, owner: func(string) (int, error) { return 1000, nil }}
}

func TestUnknownReaderErrorLinux(t *testing.T) {
	p := fakeProc(t, map[string]string{"100": "/elsewhere", "101": "/elsewhere"})
	want(t, p.scan("/scratch", false), Pass)

	denied := p
	denied.readlink = func(s string) (string, error) {
		if filepath.Base(filepath.Dir(s)) == "101" {
			return "", &fs.PathError{Op: "readlink", Path: s, Err: fs.ErrPermission}
		}
		return os.Readlink(s)
	}
	want(t, denied.scan("/scratch", false), Unknown)
	// a proven hit still wins over an unreadable process
	want(t, denied.scan("/elsewhere", false), Fail)

	// another uid's process is out of sight for a non-root caller, but not for root
	foreign := denied
	foreign.euid = 2000
	want(t, foreign.scan("/scratch", false), Pass)
	nondump := denied
	nondump.owner = func(string) (int, error) { return 0, nil } // root-owned /proc/<pid>, our Uid
	want(t, nondump.scan("/scratch", false), Pass)
	foreign.euid = 0
	want(t, foreign.scan("/scratch", false), Unknown)

	other := p
	other.readlink = func(string) (string, error) { return "", errors.New("boom") }
	want(t, other.scan("/scratch", false), Unknown)
	want(t, other.scan("/scratch", true), Unknown)

	nodir := p
	nodir.readDir = func(string) ([]os.DirEntry, error) { return nil, errors.New("no /proc") }
	want(t, nodir.scan("/scratch", false), Unknown)
	if _, err := nodir.processes(); err == nil {
		t.Error("processes must error when /proc unreadable")
	}
}

func TestUnknownVanishedAndKernelThreadLinux(t *testing.T) {
	p := fakeProc(t, map[string]string{"100": "/elsewhere"})
	gone := p
	gone.readlink = func(s string) (string, error) { return "", &fs.PathError{Op: "readlink", Path: s, Err: fs.ErrNotExist} }
	want(t, gone.scan("/scratch", false), Pass) // exited mid-scan: ruled out

	// pid 2 is a kernel thread: permission denied is ruled out.
	kt := fakeProc(t, map[string]string{"2": "/"})
	kt.readlink = func(s string) (string, error) {
		return "", &fs.PathError{Op: "readlink", Path: s, Err: fs.ErrPermission}
	}
	want(t, kt.scan("/scratch", false), Pass)
}

func TestHeldOpenFakeProcLinux(t *testing.T) {
	p := fakeProc(t, map[string]string{"100": "/elsewhere"})
	want(t, p.scan("/data/held.tmp", true), Fail)
	want(t, p.scan("/data", true), Fail)
	want(t, p.scan("/dat", true), Pass) // prefix of a name is not "under"
}

func TestParseStatLinux(t *testing.T) {
	st, err := parseStat([]byte("42 (a b) c) S 7 1 1 0 -1 4194560 100 0 0 0 1 1 0 0 20 0 1 0 12345 1000 10 0\n"))
	if err != nil || st.comm != "a b) c" || st.ppid != 7 || st.ticks != 12345 {
		t.Fatalf("%v %+v", err, st)
	}
	if _, err := parseStat([]byte("garbage")); err == nil {
		t.Error("garbage accepted")
	}
}
