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

// dgaRun runs argv through Run as who on the digestall_send golden database and returns the statement trace, stdout,
// stderr, the exit code and how often it handed over to Python.
func dgaRun(t *testing.T, who string, args ...string) (trace []Stmt, stdout, stderr string, code, execs int) {
	t.Helper()
	home, procDir := t.TempDir(), t.TempDir()
	if _, err := buildGoldenDB(t, home, "digestall_send"); err != nil {
		t.Fatal(err)
	}
	m := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_PROC_DIR": procDir, "FACTORY_AGENT": who,
		"TZ": "UTC", "FACTORY_TEST_NOW": goldenNow, "FACTORY_NOW": "2026-11-12T12:00:00+00:00"}
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

// TestStmtOrderDigestSend: the (verb, table) order of each argv form, transcribed from cmd_digest (bin/factory_cli.py
// 6797): require_role at 6800, build_digest (reads only) at 6802, then send's single INSERT INTO messages (3983) at 6807.
// Python never writes before a refusal, and the digest itself writes nothing.
func TestStmtOrderDigestSend(t *testing.T) {
	ins := []Stmt{{"INSERT", "messages"}}
	tests := []struct {
		name string
		who  string
		args []string
		want []Stmt
		code int
	}{
		{"send", "claude-director", []string{"digest", "--send"}, ins, 0},
		{"send --goal", "claude-director", []string{"digest", "--send", "--goal", "G-1"}, ins, 0},
		{"send --date --hours", "claude-director", []string{"digest", "--send", "--date", "2026-11-11", "--hours", "6"}, ins, 0},
		{"send as owner", "owner", []string{"digest", "--send"}, ins, 0},
		{"send as lead is refused", "claude-lead-a", []string{"digest", "--send"}, nil, 3},
		{"send as worker is refused", "claude-coder-14", []string{"digest", "--send", "--goal", "G-1"}, nil, 3},
		{"send, unknown goal writes nothing", "claude-director", []string{"digest", "--send", "--goal", "G-404"}, nil, 1},
		{"no --send writes nothing", "claude-lead-a", []string{"digest"}, nil, 0},
		{"--goal without --send writes nothing", "claude-lead-a", []string{"digest", "--goal", "G-1"}, nil, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			trace, _, stderr, code, execs := dgaRun(t, tc.who, tc.args...)
			if execs != 0 {
				t.Fatalf("handed over to python; stderr %q", stderr)
			}
			if code != tc.code {
				t.Fatalf("exit %d, want %d; stderr %q", code, tc.code, stderr)
			}
			if !reflect.DeepEqual(trace, tc.want) {
				t.Fatalf("trace %v, want %v", trace, tc.want)
			}
		})
	}
}

// TestDigestAllSendToCursorBoss: --send for a cursor goal reaches that org boss (subject and goal_id are in the send_goal db.diff goldens).
func TestDigestAllSendToCursorBoss(t *testing.T) {
	_, out, _, code, execs := dgaRun(t, "claude-director", "digest", "--send", "--goal", "G-7")
	if code != 0 || execs != 0 || out != "M1 digest → cursor-boss\n" {
		t.Fatalf("code %d execs %d out %q", code, execs, out)
	}
}

func TestParseDigestAllArgv(t *testing.T) {
	good := map[string]digestAllCmd{
		"":           {hours: "24"},
		"--send":     {hours: "24", send: true},
		"--goal G-1": {hours: "24", goal: "G-1"},
		"--send --goal G-1 --date 2026-11-12 --hours 6": {hours: "6", goal: "G-1", date: "2026-11-12", send: true},
		"--hours 6 --send": {hours: "6", send: true},
	}
	for in, want := range good {
		var args []string
		if in != "" {
			args = strings.Fields(in)
		}
		p, ok := parseDigestAll(args)
		if !ok || p.(digestAllCmd) != want {
			t.Errorf("%q: got %+v %v, want %+v", in, p, ok, want)
		}
	}
	for _, bad := range []string{"--se", "--send --send", "--goal", "--goal --send", "--hours -3", "--goal=G-1", "--", "-h", "G-1",
		"--goal G-1 --goal G-2", "--date --send"} {
		if _, ok := parseDigestAll(strings.Fields(bad)); ok {
			t.Errorf("%q must delegate", bad)
		}
	}
	if _, ok := parseDigestAll([]string{"--goal", ""}); ok {
		t.Error("an empty value must delegate")
	}
}

func TestIdentDigestAll(t *testing.T) {
	p, _ := parseDigestAll([]string{"--send"})
	if got := identDigestAll(p); got != (identity.Cmd{Name: "digest", Send: true}) {
		t.Errorf("send: %+v", got)
	}
	p, _ = parseDigestAll(nil)
	if got := identDigestAll(p); got != (identity.Cmd{Name: "digest"}) {
		t.Errorf("read: %+v", got)
	}
}

func FuzzParseDigestAll(f *testing.F) {
	for _, s := range []string{"", "--send", "--goal\x00G-1\x00--send", "--hours\x006\x00--date\x002026-11-12", "--se", "--send\x00--send",
		"--goal", "--hours\x00-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, "\x00")
		p, ok := parseDigestAll(args)
		if !ok {
			return
		}
		c := p.(digestAllCmd)
		// documented rule: only --goal/--date/--hours (a non-empty value not starting with "-") and --send, each once
		seen := map[string]bool{}
		for i := 0; i < len(args); i++ {
			a := args[i]
			if seen[a] {
				t.Fatalf("%q accepted with a repeated flag", args)
			}
			seen[a] = true
			switch a {
			case "--send":
			case "--goal", "--date", "--hours":
				i++
				if i >= len(args) || args[i] == "" || args[i][0] == '-' {
					t.Fatalf("%q accepted with a bad value for %s", args, a)
				}
			default:
				t.Fatalf("%q accepted with unlisted argument %q", args, a)
			}
		}
		if c.send != seen["--send"] || (c.goal != "") != seen["--goal"] {
			t.Fatalf("%q parsed to %+v", args, c)
		}
	})
}
