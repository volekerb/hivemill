package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

// taskupdRun runs argv as agent on a fresh taskupd golden DB with the statement recorder on and git replaced by a
// fixed ls-files list. It returns the recorded statements, the exit code and stderr.
func taskupdRun(t *testing.T, agent string, argv ...string) ([]Stmt, int, string) {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "taskupd"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
	var trace []Stmt
	var out, errb bytes.Buffer
	code := Run(Deps{Args: append([]string{"task", "update"}, argv...), Stdout: &out, Stderr: &errb, Trace: &trace,
		Stdin: strings.NewReader(""), Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		RunCmd: func(name string, a []string, dir string, env []string, to time.Duration) (string, string, int, error) {
			return "go/internal/cli/a.go\ngo/internal/cli/b.go\n", "", 0, nil
		},
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return trace, code, errb.String()
}

// TestStmtOrderTaskUpdate: the (verb, table) order of every write, transcribed from cmd_task "update" in
// bin/factory_cli.py: UPDATE tasks (3825), event update (3837), [UPDATE tasks sandbox, event] (3838-3840),
// [UPDATE tasks judge hold, event] (3841-3848), overlap event (3849-3850, warn_file_overlap), [UPDATE goals, event]
// (3851-3852, goal_auto_active 3206). Every die() of the ported forms comes before the first write, so each refusal
// must leave the trace empty.
func TestStmtOrderTaskUpdate(t *testing.T) {
	ins, upd := func(table string) Stmt { return Stmt{"INSERT", table} }, func(table string) Stmt { return Stmt{"UPDATE", table} }
	for _, tc := range []struct {
		name  string
		agent string
		argv  []string
		code  int
		want  []Stmt
	}{
		{"plain", "claude-lead", []string{"T-0001", "--title", "x"}, 0, []Stmt{upd("tasks"), ins("events")}},
		{"sandbox", "claude-lead", []string{"T-0001", "--sandbox-required", "1"}, 0, []Stmt{upd("tasks"), ins("events"), upd("tasks"), ins("events")}},
		{"sandbox unchanged", "claude-lead", []string{"T-0014", "--sandbox-required", "1"}, 0, []Stmt{upd("tasks"), ins("events")}},
		{"judge hold set", "claude-lead", []string{"T-0001", "--judge-hold", "w"}, 0, []Stmt{upd("tasks"), ins("events"), upd("tasks"), ins("events")}},
		{"judge hold clear", "claude-lead", []string{"T-0011", "--judge-hold-clear"}, 0, []Stmt{upd("tasks"), ins("events"), upd("tasks"), ins("events")}},
		{"judge hold clear when empty", "claude-lead", []string{"T-0001", "--judge-hold-clear"}, 0, []Stmt{upd("tasks"), ins("events")}},
		{"spec overlap", "claude-lead", []string{"T-0001", "--spec", "go/internal/cli/a.go"}, 0, []Stmt{upd("tasks"), ins("events"), ins("events")}},
		{"all in order", "claude-director", []string{"T-0020", "--status", "in_progress", "--sandbox-required", "1", "--judge-hold", "w", "--spec", "go/internal/cli/a.go"}, 0,
			[]Stmt{upd("tasks"), ins("events"), upd("tasks"), ins("events"), upd("tasks"), ins("events"), ins("events"), upd("goals"), ins("events")}},
		{"in_progress on an active goal", "claude-lead", []string{"T-0001", "--status", "in_progress"}, 0, []Stmt{upd("tasks"), ins("events"), upd("goals")}},
		{"unknown task", "claude-lead", []string{"T-9999", "--status", "done"}, 1, nil},
		{"nothing to update", "claude-lead", []string{"T-0001"}, 2, nil},
		{"reviewer status", "claude-reviewer", []string{"T-0001", "--status", "done"}, 3, nil},
		{"pm without note", "claude-pm", []string{"T-0002", "--status", "done"}, 2, nil},
		{"priority range", "claude-lead", []string{"T-0001", "--priority", "9"}, 2, nil},
		{"role holder", "claude-lead", []string{"T-0010", "--role", "pm"}, 2, nil},
		{"sandbox on pm role", "claude-lead", []string{"T-0002", "--sandbox-required", "1"}, 2, nil},
		{"hold both", "claude-lead", []string{"T-0001", "--judge-hold", "w", "--judge-hold-clear"}, 2, nil},
		{"team of another goal", "claude-director", []string{"T-0001", "--team", "TM-3"}, 2, nil},
		{"goal with dependents", "claude-director", []string{"T-0001", "--goal", "G-3"}, 2, nil},
		{"waits without blocked", "claude-lead", []string{"T-0001", "--waits", "owner"}, 2, nil},
		{"live worker spec", "claude-lead", []string{"T-0010", "--spec", "x"}, 2, nil},
		{"after cycle", "claude-lead", []string{"T-0001", "--after", "T-0007"}, 2, nil},
		{"bad status after a spec change", "claude-lead", []string{"T-0001", "--spec", "go/internal/cli/a.go", "--status", "finished"}, 1, nil},
		{"coder done with PR", "claude-lead", []string{"T-0015", "--status", "done"}, 3, nil},
		{"pr clear by worker", "claude-coder", []string{"T-0015", "--pr", ""}, 3, nil},
	} {
		got, code, stderr := taskupdRun(t, tc.agent, tc.argv...)
		if code != tc.code || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: exit %d trace %v, want exit %d trace %v (stderr %q)", tc.name, code, got, tc.code, tc.want, stderr)
		}
	}
}

func TestTaskUpdateParse(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		ok   bool
	}{
		{"id and flags", []string{"T-1", "--status", "done", "--note", "x"}, true},
		{"id after flags", []string{"--status", "done", "T-1"}, true},
		{"equals form", []string{"T-1", "--status=done", "--waits=owner"}, true},
		{"empty value", []string{"T-1", "--after", ""}, true},
		{"empty equals value", []string{"T-1", "--after="}, true},
		{"dash value is stdin", []string{"T-1", "--spec", "-"}, true},
		{"hold clear", []string{"T-1", "--judge-hold-clear"}, true},
		{"priority", []string{"T-1", "--priority", "3"}, true},
		{"no id", []string{"--status", "done"}, false},
		{"two ids", []string{"T-1", "T-2"}, false},
		{"abbreviation", []string{"T-1", "--stat", "done"}, false},
		{"unknown flag", []string{"T-1", "--bogus", "x"}, false},
		{"declared elsewhere", []string{"T-1", "--follow-up-of", "T-2"}, false},
		{"merged externally", []string{"T-1", "--merged-externally"}, false},
		{"help", []string{"T-1", "-h"}, false},
		{"double dash", []string{"T-1", "--"}, false},
		{"repeat", []string{"T-1", "--note", "a", "--note", "b"}, false},
		{"repeat mixed forms", []string{"T-1", "--note", "a", "--note=b"}, false},
		{"dash value", []string{"T-1", "--note", "-x"}, false},
		{"dash equals value", []string{"T-1", "--note=-x"}, false},
		{"missing value", []string{"T-1", "--note"}, false},
		{"bool with value", []string{"T-1", "--judge-hold-clear=1"}, false},
		{"bool repeated", []string{"T-1", "--judge-hold-clear", "--judge-hold-clear"}, false},
		{"bad waits", []string{"T-1", "--waits", "x"}, false},
		{"bad sandbox", []string{"T-1", "--sandbox-required", "2"}, false},
		{"signed priority", []string{"T-1", "--priority", "+2"}, false},
		{"spaced priority", []string{"T-1", "--priority", " 2"}, false},
		{"underscore priority", []string{"T-1", "--priority", "1_0"}, false},
		{"non-ASCII digit priority", []string{"T-1", "--priority", "٣"}, false}, // Python int() reads Arabic-Indic digits
		{"fullwidth digit priority", []string{"T-1", "--priority", "２"}, false},
		{"huge priority", []string{"T-1", "--priority", "12345678901"}, false},
		{"positional dash", []string{"-"}, false},
	} {
		if _, ok := parseTaskUpdate(tc.args); ok != tc.ok {
			t.Errorf("%s: parse ok=%v, want %v", tc.name, ok, tc.ok)
		}
	}
}

// FuzzParseTaskUpdate: the parser never panics and an accepted argv only holds exact, non-dash values.
func FuzzParseTaskUpdate(f *testing.F) {
	for _, s := range []string{"T-1\x00--status\x00done", "T-1\x00--after=", "--priority\x003\x00T-1", "T-1\x00--spec\x00-", "T-1\x00--judge-hold-clear"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		args := strings.Split(s, "\x00")
		p, ok := parseTaskUpdate(args)
		if !ok {
			return
		}
		c := p.(taskUpdCmd)
		if strings.HasPrefix(c.id, "-") {
			t.Fatalf("accepted id %q from %q", c.id, args)
		}
		for _, v := range []*string{c.status, c.pr, c.branch, c.assignee, c.title, c.spec, c.acceptance, c.team, c.after, c.waits, c.note, c.reason, c.goal, c.sandbox, c.judgeHold, c.role} {
			if v != nil && strings.HasPrefix(*v, "-") && *v != "-" {
				t.Fatalf("accepted dash value %q from %q", *v, args)
			}
		}
		if c.priority != nil && (*c.priority < 0 || *c.priority > 999999999) {
			t.Fatalf("priority %d from %q", *c.priority, args)
		}
	})
}
