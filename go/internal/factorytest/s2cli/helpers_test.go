package s2cli

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// as runs args as agent and returns the result without judging the exit code (harness env.run(agent=...)).
func as(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(ft.Opts{Agent: agent}, args...)
}

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func notContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

// sqlExec runs one write statement on the env's DB.
func sqlExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
