package cli

import (
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
)

// lockFree reports whether run/tick.lock is flock-locked by someone (it never keeps the lock itself).
func lockFree(t *testing.T, home string) bool {
	t.Helper()
	f, err := os.OpenFile(filepath.Join(home, "run", "tick.lock"), os.O_RDONLY, 0)
	if err != nil {
		return true // never created: nobody locked it
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return false
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return true
}

func TestTickDelegate(t *testing.T) {
	cases := []struct {
		name string
		args []string
		env  map[string]string
		prep func(r *rig)
	}{
		{"--help", []string{"tick", "--help"}, nil, nil},
		{"-h", []string{"tick", "-h"}, nil, nil},
		{"--phases", []string{"tick", "--phases", "x"}, nil, nil},
		{"extra arg", []string{"tick", "x"}, nil, nil},
		{"identity director", []string{"tick"}, map[string]string{"FACTORY_AGENT": "claude-director"}, nil},
		{"identity unknown", []string{"tick"}, nil, func(r *rig) { delete(r.env, "FACTORY_AGENT"); r.env["CLAUDECODE"] = "1"; r.env["HOME"] = t0(r) }},
		{"user_version mismatch", []string{"tick"}, nil, func(r *rig) { r.sql("PRAGMA user_version = 99") }},
		{"no database", []string{"tick"}, nil, func(r *rig) { os.Remove(r.dbPath) }},
		{"FACTORY_CLI_IMPL=python", []string{"tick"}, map[string]string{"FACTORY_CLI_IMPL": "python"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := map[string]string{"FACTORY_AGENT": "owner"}
			for k, v := range c.env {
				env[k] = v
			}
			r := newRig(t, env)
			if c.prep != nil {
				c.prep(r)
			}
			r.run(c.args...)
			if len(r.execs) != 1 {
				t.Fatalf("want exactly one exec, got %d (stdout %q stderr %q)", len(r.execs), r.out.String(), r.errb.String())
			}
			want := append([]string{"python3", filepath.Join(r.repo, "bin", "factory")}, c.args...)
			if x := r.execs[0]; x.path != "/usr/bin/python3" || !reflect.DeepEqual(x.argv, want) {
				t.Fatalf("exec %s %q, want python3 %q", x.path, x.argv, want)
			}
			if !lockFree(t, r.home) {
				t.Fatal("the tick lock was taken before delegating")
			}
			if _, err := os.Stat(filepath.Join(r.home, "run", "tick.lock")); err == nil {
				t.Fatal("run/tick.lock was created before delegating")
			}
			if r.out.Len() != 0 {
				t.Fatalf("a delegated tick printed %q", r.out.String())
			}
		})
	}
}

// The owner's bare `tick` is answered in Go: with python3 replaced by the test binary the whole tick is one child,
// started with the full unit list and the handoff env; a held lock prints the Python line and starts nothing.
func TestTickRunsAsOwner(t *testing.T) {
	r := newRig(t, map[string]string{"FACTORY_AGENT": "owner"})
	os.MkdirAll(filepath.Join(r.home, "run"), 0o755)
	f, err := os.OpenFile(filepath.Join(r.home, "run", "tick.lock"), os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if code := r.run("tick"); code != 0 || r.out.String() != "another tick/up is running; skipping\n" || len(r.execs) != 0 {
		t.Fatalf("busy lock: exit %d out %q execs %d", code, r.out.String(), len(r.execs))
	}
}

func t0(r *rig) string { r.pw = r.t.TempDir(); return r.pw }
