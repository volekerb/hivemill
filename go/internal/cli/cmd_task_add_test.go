package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

// taskwRun runs argv as agent on a fresh taskw golden DB with the statement recorder on and git replaced by a fixed
// ls-files list. It returns the recorded statements, the exit code, stdout and stderr.
func taskwRun(t *testing.T, agent string, argv ...string) ([]Stmt, int, string, string) {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "taskw"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
	var trace []Stmt
	var out, errb bytes.Buffer
	code := Run(Deps{Args: argv, Stdout: &out, Stderr: &errb, Trace: &trace, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		RunCmd: func(name string, a []string, dir string, env []string, to time.Duration) (string, string, int, error) {
			return "go/internal/cli/a.go\ngo/internal/cli/b.go\n", "", 0, nil
		},
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return trace, code, out.String(), errb.String()
}

// TestStmtOrderTaskAdd: the (verb, table) order of every write, transcribed from cmd_task in bin/factory_cli.py:
// add 3576-3590 (INSERT tasks, event add, [UPDATE tasks sandbox, event], [event follow-up], overlap event), reopen 3609
// (UPDATE tasks, event) and note 3864 (event). No add/reopen/note refusal comes after a write (every check precedes the
// INSERT), so there is no partial-write case: a refusal must leave the trace empty.
func TestStmtOrderTaskAdd(t *testing.T) {
	ins := func(table string) Stmt { return Stmt{"INSERT", table} }
	for _, tc := range []struct {
		name  string
		agent string
		argv  []string
		code  int
		want  []Stmt
	}{
		{"minimal", "claude-lead", []string{"task", "add", "--goal", "G-1", "--title", "t"}, 0, []Stmt{ins("tasks"), ins("events")}},
		{"sandbox and follow-up", "claude-lead", []string{"task", "add", "--goal", "G-1", "--title", "t", "--sandbox-required", "1", "--follow-up-of", "T-0001"}, 0,
			[]Stmt{ins("tasks"), ins("events"), {"UPDATE", "tasks"}, ins("events"), ins("events")}},
		{"file overlap", "claude-lead", []string{"task", "add", "--goal", "G-1", "--title", "t", "--spec", "go/internal/cli/a.go"}, 0,
			[]Stmt{ins("tasks"), ins("events"), ins("events")}},
		{"reopen", "claude-lead", []string{"task", "reopen", "T-0001", "--reason", "r"}, 0, []Stmt{{"UPDATE", "tasks"}, ins("events")}},
		{"note", "claude-lead", []string{"task", "note", "T-0001", "hi"}, 0, []Stmt{ins("events")}},
		{"cycle refuses before writing", "claude-lead", []string{"task", "add", "--goal", "G-1", "--title", "t", "--after", "T-0006"}, 2, nil},
		{"worker refused before writing", "claude-coder", []string{"task", "add", "--goal", "G-1", "--title", "t"}, 3, nil},
		{"reopen with PR refuses before writing", "claude-lead", []string{"task", "reopen", "T-0002", "--reason", "r"}, 2, nil},
	} {
		got, code, _, stderr := taskwRun(t, tc.agent, tc.argv...)
		if code != tc.code || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: exit %d trace %v, want exit %d trace %v (stderr %q)", tc.name, code, got, tc.code, tc.want, stderr)
		}
	}
}

func TestTaskParsersDelegate(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func([]string) (any, bool)
		args  string
		ok    bool
	}{
		{"add ok", parseTaskAdd, "--goal G-1 --title x", true},
		{"add stdin spec", parseTaskAdd, "--goal G-1 --title x --spec -", true},
		{"add waits", parseTaskAdd, "--goal G-1 --title x --waits owner", false},
		{"add abbreviation", parseTaskAdd, "--goal G-1 --tit x", false},
		{"add equals", parseTaskAdd, "--goal=G-1 --title x", false},
		{"add positional", parseTaskAdd, "T-1 --goal G-1 --title x", false},
		{"add repeated", parseTaskAdd, "--goal G-1 --title x --title y", false},
		{"add dash value", parseTaskAdd, "--goal G-1 --title -x", false},
		{"add bad choice", parseTaskAdd, "--goal G-1 --title x --sandbox-required 2", false},
		{"add no title", parseTaskAdd, "--goal G-1", false},
		{"note two", parseTaskNote, "T-1 text", true},
		{"note dash text", parseTaskNote, "T-1 -", true},
		{"note help", parseTaskNote, "-h", false},
		{"note four", parseTaskNote, "T-1 a b c", false},
		{"note flag", parseTaskNote, "T-1 x --note y", false},
		{"reopen ok", parseTaskReopen, "T-1 --reason why", true},
		{"reopen dash reason", parseTaskReopen, "T-1 --reason -x", false},
		{"reopen help", parseTaskReopen, "-h", false},
		{"reopen two ids", parseTaskReopen, "T-1 T-2 --reason r", false},
	} {
		if _, ok := tc.parse(strings.Fields(tc.args)); ok != tc.ok {
			t.Errorf("%s: parse ok=%v, want %v", tc.name, ok, tc.ok)
		}
	}
}

func TestTaskPyRepr(t *testing.T) { // repr() values from python3
	for in, want := range map[string]string{"foo": `'foo'`, "it's": `"it's"`, `a"b'c`: `'a"b\'c'`, "a\tb": `'a\tb'`, `back\slash`: `'back\\slash'`} {
		if got, ok := taskPyRepr(in); !ok || got != want {
			t.Errorf("taskPyRepr(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := taskPyRepr("é"); ok {
		t.Error("non-ASCII must delegate")
	}
}
