package s2tasks

import (
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_task_update_empty_stdin.py (5 of 5 cases). Complete.

func tuesEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	seedTask(e, ft.Task{Status: "todo"}, true)
	return e
}

func tuesRow(e *ft.Env) string {
	return strings.Join(sql(e, "SELECT status,title,spec,acceptance,updated FROM tasks WHERE id='T-0001'")[0], "|")
}

func tuesRefuse(t *testing.T, e *ft.Env, flag, stdin string, extra ...string) {
	t.Helper()
	before := tuesRow(e)
	args := append([]string{"task", "update", "T-0001"}, extra...)
	args = append(args, flag, "-")
	p := runO(e, ft.Opts{Stdin: stdin}, args...)
	if p.Exit == 0 {
		t.Fatalf("%v succeeded: %s", args, p.Stdout)
	}
	contains(t, "stderr", p.Stderr, flag)
	eq(t, "row", tuesRow(e), before)
}

func TestUpdEmptyStdinSpecRefusedAndUnchanged(t *testing.T) {
	tuesRefuse(t, tuesEnv(t), "--spec", "")
}

func TestUpdEmptyStdinAcceptanceRefusedAndUnchanged(t *testing.T) {
	tuesRefuse(t, tuesEnv(t), "--acceptance", "")
}

func TestUpdEmptyStdinWhitespaceRefusedForBothFields(t *testing.T) {
	e := tuesEnv(t)
	tuesRefuse(t, e, "--spec", "\n  \n")
	tuesRefuse(t, e, "--acceptance", "\n  \n")
}

func TestUpdEmptyStdinRefusalIsWholeCall(t *testing.T) {
	tuesRefuse(t, tuesEnv(t), "--spec", "", "--status", "blocked", "--title", "new")
}

func TestUpdEmptyStdinNonemptyStdinAndInlineAreStored(t *testing.T) {
	e := tuesEnv(t)
	okS(e, "", "from stdin\n", "task", "update", "T-0001", "--spec", "-")
	okS(e, "", "1. acc\n", "task", "update", "T-0001", "--acceptance", "-")
	r := sql(e, "SELECT spec, acceptance FROM tasks WHERE id='T-0001'")[0]
	eqS(t, "spec/acceptance", r, []string{"from stdin", "1. acc"})
	ok(e, "", "task", "update", "T-0001", "--spec", "inline")
	eq(t, "spec", val(e, "SELECT spec FROM tasks WHERE id='T-0001'"), "inline")
}
