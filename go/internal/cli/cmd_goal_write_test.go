package cli

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

// gwTraceRun runs one command through Run on the seeded golden database of group and returns exit code, stdout,
// stderr and the (verb, table) of every write statement it ran.
func gwTraceRun(t *testing.T, group, who, stdin string, args ...string) (int, string, string, []Stmt) {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, group); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": who, "FACTORY_PROC_DIR": t.TempDir()}
	var trace []Stmt
	var out, errb bytes.Buffer
	code := Run(Deps{Args: args, Stdout: &out, Stderr: &errb, Trace: &trace, Stdin: strings.NewReader(stdin),
		Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return code, out.String(), errb.String(), trace
}

// TestStmtOrderGoal: the ordered (verb, table) list per argv form, transcribed from cmd_goal in bin/factory_cli.py.
// No branch here dies after a write (every refusal is before the first INSERT/UPDATE), so there is no partial-write case.
func TestStmtOrderGoal(t *testing.T) {
	ins := Stmt{"INSERT", "events"}
	cases := []struct {
		name  string
		who   string
		stdin string
		args  []string
		want  []Stmt
	}{
		// 2893-2928: goal new = INSERT goals, event
		{"new", "claude-boss", "", []string{"goal", "new", "--title", "t"}, []Stmt{{"INSERT", "goals"}, ins}},
		// 2951-2967: edit --token-limit 0 alone = UPDATE, event, return
		{"edit limit 0", "claude-director", "", []string{"goal", "edit", "G-1", "--token-limit", "0"}, []Stmt{{"UPDATE", "goals"}, ins}},
		// edit --token-limit 0 + --title = UPDATE, event, UPDATE, event
		{"edit limit 0 + title", "claude-director", "", []string{"goal", "edit", "G-1", "--token-limit", "0", "--title", "x"},
			[]Stmt{{"UPDATE", "goals"}, ins, {"UPDATE", "goals"}, ins}},
		{"edit title", "claude-director", "", []string{"goal", "edit", "G-1", "--title", "x"}, []Stmt{{"UPDATE", "goals"}, ins}},
		// 2978-3000 goal_status: UPDATE goals, status event; the owner override adds its event between them
		{"status", "claude-lead-1", "", []string{"goal", "status", "G-6", "planning"}, []Stmt{{"UPDATE", "goals"}, ins}},
		{"status override", "owner", "", []string{"goal", "status", "G-1", "done"}, []Stmt{{"UPDATE", "goals"}, ins, ins}},
		// 3057-3080 goal_audit: INSERT audits, event
		{"audit", "claude-director", "", []string{"goal", "audit", "G-4", "--gaps", "--report", "r"}, []Stmt{{"INSERT", "audits"}, ins}},
		// 3083-3108 goal_signoff: INSERT signoffs, event
		{"signoff", "claude-director", "", []string{"goal", "signoff", "G-4", "--audit", "2", "--reason", "r"}, []Stmt{{"INSERT", "signoffs"}, ins}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, out, errs, trace := gwTraceRun(t, "goalw", c.who, c.stdin, c.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s%s", code, out, errs)
			}
			if !reflect.DeepEqual(trace, c.want) {
				t.Fatalf("statements = %v, want %v", trace, c.want)
			}
		})
	}
}

// A refusal writes nothing: no statement runs before Python's first write.
func TestGoalWriteRefusalsWriteNothing(t *testing.T) {
	for _, c := range []struct {
		who  string
		args []string
		code int
	}{
		{"claude-director", []string{"goal", "new", "--title", "x"}, 3},
		{"claude-director", []string{"goal", "edit", "G-1"}, 1},
		{"claude-director", []string{"goal", "edit", "G-1", "--token-limit", "-1"}, 2},
		{"claude-director", []string{"goal", "status", "G-2", "active"}, 2},
		{"claude-director", []string{"goal", "audit", "G-5", "--ready", "--report", "x"}, 2},
		{"claude-director", []string{"goal", "signoff", "G-4", "--audit", "1", "--reason", "x"}, 2},
	} {
		code, _, _, trace := gwTraceRun(t, "goalw", c.who, "", c.args...)
		if code != c.code || len(trace) != 0 {
			t.Errorf("%v: exit %d (want %d), statements %v", c.args, code, c.code, trace)
		}
	}
}

func TestGoalWriteParse(t *testing.T) {
	ok := [][]string{
		{"--title", "x"}, {"--title=x"}, {"G-1", "--token-limit", "-1"}, {"G-1", "--note", "-"}, {"G-1", "--intent=-x"},
	}
	for _, a := range ok {
		if _, good := gwSplit(a, 2); !good {
			t.Errorf("%v should parse", a)
		}
	}
	bad := [][]string{
		{"--titl", "x"}, {"--title"}, {"--title", "x", "--title", "y"}, {"--priority", "x"}, {"--priority", "1.5"},
		{"--title", "--note"}, {"--ready=1"}, {"--"}, {"-h"}, {"G-1", "--note", "x", "active"}, {"a", "b", "c"}, {"-x"},
		{"--priority", "99999999999999999999"},
	}
	for _, a := range bad {
		if _, good := gwSplit(a, 2); good {
			t.Errorf("%v should delegate", a)
		}
	}
}

// Unicode digits are valid for Python's int() and argparse's negative-number check; the Go regexps are ASCII, so
// these must delegate (never be accepted with a different meaning).
func TestGoalWriteParseUnicodeDigitsDelegate(t *testing.T) {
	for _, a := range [][]string{
		{"--priority", "١"},    // Arabic-Indic one
		{"--priority=١٢"},      // two of them
		{"--title", "-١"},      // dash + Arabic-Indic digit as a value
		{"--token-limit", "１"}, // fullwidth one
		{"-١"},                 // dash-led "number" positional
	} {
		if _, ok := gwSplit(a, 2); ok {
			t.Errorf("%q should delegate", a)
		}
	}
}

func FuzzGwSplit(f *testing.F) {
	f.Add("--title", "x", "")
	f.Add("G-1", "--token-limit", "-1")
	f.Add("--priority", "١", "")
	f.Add("--title", "-١", "--ready")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		got, ok := gwSplit([]string{a, b, c}, 2)
		if !ok {
			return
		}
		if len(got.pos) > 2 {
			t.Fatalf("accepted %d positionals", len(got.pos))
		}
		for fl := range got.str {
			if gwFlags[fl] != 's' {
				t.Fatalf("string flag %s has kind %c", fl, gwFlags[fl])
			}
		}
		for fl := range got.num {
			if gwFlags[fl] != 'i' {
				t.Fatalf("int flag %s has kind %c", fl, gwFlags[fl])
			}
		}
		for fl := range got.bools {
			if gwFlags[fl] != 'b' {
				t.Fatalf("bool flag %s has kind %c", fl, gwFlags[fl])
			}
		}
	})
}

func FuzzGwParseFor(f *testing.F) {
	f.Add("status", "G-1", "active")
	f.Add("new", "--title", "x")
	f.Add("edit", "", "--title")
	f.Fuzz(func(t *testing.T, action, a, b string) {
		switch action {
		case "new", "edit", "status", "audit", "signoff":
		default:
			return
		}
		got, ok := gwParseFor(action)([]string{a, b})
		if !ok {
			return
		}
		ga := got.(gwArgs)
		if ga.action != action || len(ga.pos) > 2 {
			t.Fatalf("bad parse %+v", ga)
		}
		if action != "new" && (len(ga.pos) == 0 || ga.pos[0] == "") {
			t.Fatalf("%s accepted without a goal id", action)
		}
	})
}
