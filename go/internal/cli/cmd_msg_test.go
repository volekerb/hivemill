package cli

import (
	"bytes"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

// msgRun runs argv through Run as who on the msg golden database and returns the statement trace, stdout, stderr
// and the exit code. Delegating is reported through execs.
func msgRun(t *testing.T, who string, args ...string) (trace []Stmt, stdout, stderr string, code, execs int) {
	t.Helper()
	home, procDir := t.TempDir(), t.TempDir()
	if _, err := buildGoldenDB(t, home, "msg"); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_PROC_DIR": procDir, "FACTORY_AGENT": who,
		"TZ": "UTC", "FACTORY_TEST_NOW": goldenNow}
	var out, errb bytes.Buffer
	code = Run(Deps{
		Args: args, Argv0: "bin/factory", Environ: []string{"FACTORY_CLI_IMPL=go"},
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return home, nil },
			Now:       func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) },
		},
		Stdout: &out, Stderr: &errb, Trace: &trace, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { execs++; return nil },
	})
	return trace, out.String(), errb.String(), code, execs
}

// TestStmtOrderMsg: the (verb, table) order of each argv form, transcribed from cmd_msg (bin/factory_cli.py 4104:
// send's INSERT INTO messages at 3983, then warn_spec_by_msg's event() at 4093) and cmd_report (4205: send at 4220).
// Python never writes before a refusal, and neither handler writes twice to a table other than that.
func TestStmtOrderMsg(t *testing.T) {
	msgIns := Stmt{"INSERT", "messages"}
	evIns := Stmt{"INSERT", "events"}
	tests := []struct {
		name string
		who  string
		args []string
		want []Stmt
	}{
		{"msg plain", "owner", []string{"msg", "coder-a", "s", "b"}, []Stmt{msgIns}},
		{"msg spec words to a live worker", "lead-a", []string{"msg", "coder-a", "scope", "the spec"}, []Stmt{msgIns, evIns}},
		{"msg spec words as decision", "lead-a", []string{"msg", "coder-a", "scope", "x", "--kind", "decision"}, []Stmt{msgIns}},
		{"msg spec words from a worker", "coder-a", []string{"msg", "qa-a", "scope", "x"}, []Stmt{msgIns}},
		{"msg refused cross-org", "lead-a", []string{"msg", "cursor-director", "s"}, nil},
		{"msg refused escalation", "coder-a", []string{"msg", "director", "s", "--kind", "escalation"}, nil},
		{"msg unknown recipient", "owner", []string{"msg", "nobody", "s"}, nil},
		{"report default", "coder-a", []string{"report", "done"}, []Stmt{msgIns}},
		{"report --to", "owner", []string{"report", "done", "--to", "boss"}, []Stmt{msgIns}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trace, _, stderr, _, execs := msgRun(t, tc.who, tc.args...)
			if execs != 0 {
				t.Fatalf("handed over to python; stderr %q", stderr)
			}
			if !reflect.DeepEqual(trace, tc.want) {
				t.Fatalf("trace %v, want %v", trace, tc.want)
			}
		})
	}
}

// TestMsgStaffEngineerDelegates: every message to the staff-engineer runs in Python before any write.
func TestMsgStaffEngineerDelegates(t *testing.T) {
	for _, args := range [][]string{
		{"msg", "staff-engineer", "q", "b", "--kind", "request"},
		{"msg", "staff-engineer", "ack"},
		{"report", "x", "--to", "staff-engineer"},
	} {
		trace, _, stderr, _, execs := msgRun(t, "director", args...)
		if execs != 1 || len(trace) != 0 {
			t.Errorf("%v: execs %d trace %v stderr %q, want 1 exec and no write", args, execs, trace, stderr)
		}
	}
}

func TestParseMsgArgv(t *testing.T) {
	ok := [][]string{
		{"a", "b"}, {"a", "b", "c"}, {"a", "b", "-"}, {"a", "b", "--kind", "request"}, {"a", "b", "--kind=alert", "--goal=G-1"},
		{"a", "b", "c", "--task", "T-1"},
	}
	for _, a := range ok {
		if _, good := parseMsg(a); !good {
			t.Errorf("parseMsg(%q) delegated, want Go", a)
		}
	}
	bad := [][]string{
		{}, {"a"}, {"a", "b", "c", "d"}, {"a", "b", "--ki", "request"}, {"a", "b", "--bogus", "x"}, {"a", "b", "--goal", "1", "--goal", "2"},
		{"a", "b", "--kind", "nope"}, {"a", "b", "--goal", "-x"}, {"a", "b", "--goal"}, {"a", "b", "--goal", ""},
		{"a", "--kind", "request", "b"}, {"--", "a", "b"}, {"-h"}, {"a", "-b"}, {"a", "b", "\xff"},
	}
	for _, a := range bad {
		if _, good := parseMsg(a); good {
			t.Errorf("parseMsg(%q) accepted, want delegate", a)
		}
	}
	if p, _ := parseMsg([]string{"a", "b", "-"}); p.(msgCmd).body == nil || *p.(msgCmd).body != "-" {
		t.Errorf("a lone - must be the body")
	}
}

func TestParseReportArgv(t *testing.T) {
	for _, a := range [][]string{{"h"}, {"h", "b"}, {"h", "--to", "x"}, {"h", "b", "--goal=G-1", "--to=x"}} {
		if _, good := parseReport(a); !good {
			t.Errorf("parseReport(%q) delegated, want Go", a)
		}
	}
	for _, a := range [][]string{{}, {"h", "b", "c"}, {"h", "--go", "G"}, {"h", "--to", "a", "--to", "b"}, {"--to", "x"}, {"h", "--kind", "msg"}} {
		if _, good := parseReport(a); good {
			t.Errorf("parseReport(%q) accepted, want delegate", a)
		}
	}
}

func TestMsgReportOutput(t *testing.T) {
	_, out, _, code, _ := msgRun(t, "owner", "msg", "coder-a", "s", "b")
	if code != 0 || out != "M1 → coder-a\n" {
		t.Fatalf("msg: %d %q", code, out)
	}
	_, out, _, code, _ = msgRun(t, "coder-n", "report", "hi", "--goal", "G-9")
	if code != 0 || !strings.HasPrefix(out, "M1 report → director") {
		t.Fatalf("report: %d %q", code, out)
	}
}
