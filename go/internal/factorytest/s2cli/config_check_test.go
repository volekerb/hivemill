package s2cli

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_config_check.py (7 cases; see ported_s2cli.txt).

func configCheckEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.SeedAgent(ft.AgentRow{Name: "claude-director", Role: "director"})
	sqlExec(t, e, "DELETE FROM config") // the harness pre-sets a few keys; start from the defaults
	return e
}

func TestConfigCheckChangedFreshHome(t *testing.T) {
	e := configCheckEnv(t)
	r := as(e, "claude-director", "config", "--changed")
	if r.Exit != 0 || strings.TrimSpace(r.Stdout) != "no changes from defaults" {
		t.Fatalf("exit %d stdout %q stderr %q", r.Exit, r.Stdout, r.Stderr)
	}
}

func TestConfigCheckChangedListsOneKey(t *testing.T) {
	e := configCheckEnv(t)
	e.OKWith(ft.Opts{Agent: "claude-director"}, "config", "digest_hour", "9")
	r := e.OK("config", "--changed")
	lines := strings.Split(strings.TrimSpace(r.Stdout), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %q, want one", r.Stdout)
	}
	contains(t, "line", lines[0], "digest_hour = 9 (default: 8)")
}

func TestConfigCheckCleanHome(t *testing.T) {
	e := configCheckEnv(t)
	r := e.OK("config", "--check")
	if r.Stdout != "" {
		t.Fatalf("stdout = %q, want empty", r.Stdout)
	}
}

func TestConfigCheckBadStoredValue(t *testing.T) {
	e := configCheckEnv(t)
	sqlExec(t, e, "INSERT INTO config(key,value) VALUES('guard_impl','bogus')")
	sqlExec(t, e, "INSERT INTO config(key,value) VALUES('task_clones','7')")
	r := e.Fail(2, "config", "--check")
	contains(t, "stdout", r.Stdout, "guard_impl")
	// does not stop at the first bad key
	contains(t, "stdout", r.Stdout, "task_clones: task_clones must be 0 or 1, not '7'")
}

func TestConfigCheckNullStoredValue(t *testing.T) {
	e := configCheckEnv(t)
	// the schema says NOT NULL, so model a damaged DB: rebuild the table without the constraint
	sqlExec(t, e, "DROP TABLE config")
	sqlExec(t, e, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)")
	sqlExec(t, e, "INSERT INTO config(key,value) VALUES('guard_impl',NULL)")
	r := e.Fail(2, "config", "--check")
	contains(t, "stdout", r.Stdout, "guard_impl: guard_impl is NULL")
	notContains(t, "stderr", r.Stderr, "Traceback")
	notContains(t, "stderr", r.Stderr, "panic")
}

func TestConfigCheckSetPathUnchanged(t *testing.T) {
	e := configCheckEnv(t)
	r := e.FailWith(ft.Opts{Agent: "claude-director"}, 2, "config", "guard_impl", "bogus")
	want := "factory: guard_impl must be one of python, go, shadow, not 'bogus': fix it with `factory config guard_impl python`"
	if got := strings.TrimSpace(r.Stderr); got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
	e.FailWith(ft.Opts{Agent: "claude-director"}, 1, "config", "opus_effort", "bogus") // this one has always exited 1
	e.OKWith(ft.Opts{Agent: "claude-director"}, "config", "guard_impl", " python ")
	if got := strings.TrimSpace(e.OK("config", "guard_impl").Stdout); got != "python" {
		t.Fatalf("guard_impl = %q, want python", got)
	}
}

func TestConfigCheckFlagsRefuseArguments(t *testing.T) {
	e := configCheckEnv(t)
	for _, args := range [][]string{{"--changed", "digest_hour"}, {"--check", "digest_hour", "9"}, {"--changed", "--check"}} {
		r := e.Fail(2, append([]string{"config"}, args...)...)
		contains(t, "stderr "+strings.Join(args, " "), r.Stderr, "usage:")
	}
}
