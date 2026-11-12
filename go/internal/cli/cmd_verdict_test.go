package cli

import (
	"bytes"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
)

const verdictTestHead = "0123456789abcdef0123456789abcdef01234567"

// verdictRun runs `factory verdict argv...` as agent on a fresh verdict golden DB with the statement recorder on and gh
// replaced by a fixed PR view (code 0) or a failure (ghFail).
func verdictRun(t *testing.T, agent, stdin string, ghFail bool, argv ...string) ([]Stmt, int, string) {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "verdict"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
	var trace []Stmt
	var out, errb bytes.Buffer
	code := Run(Deps{Args: append([]string{"verdict"}, argv...), Stdout: &out, Stderr: &errb, Trace: &trace,
		Stdin: strings.NewReader(stdin), Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		RunCmd: func(name string, a []string, dir string, env []string, to time.Duration) (string, string, int, error) {
			if ghFail {
				return "", "boom\n", 1, nil
			}
			return `{"headRefOid": "` + verdictTestHead + `"}`, "", 0, nil
		},
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 12, 1, 2, 3, 0, time.UTC) }}})
	return trace, code, errb.String()
}

// TestStmtOrderVerdict: the (verb, table) order of every write, transcribed from cmd_verdict in bin/factory_cli.py:
// INSERT verdicts (4745), [INSERT rejection_incidents (4748), FAIL only], INSERT events (4750). Every die() of
// cmd_verdict (4709-4744) comes before the first write, so each refusal must leave the trace empty.
func TestStmtOrderVerdict(t *testing.T) {
	ins := func(table string) Stmt { return Stmt{"INSERT", table} }
	pass := []Stmt{ins("verdicts"), ins("events")}
	fail := []Stmt{ins("verdicts"), ins("rejection_incidents"), ins("events")}
	h, g := verdictTestHead, "## Gates\n- a exit=0 cmd=go test\n"
	for _, tc := range []struct {
		name, agent, stdin string
		ghFail             bool
		argv               []string
		code               int
		want               []Stmt
	}{
		{"qa pass", "claude-qa", g, false, []string{"T-0001", "--pass", "--summary", "-", "--sha", h}, 0, pass},
		{"reviewer pass", "claude-reviewer", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", h}, 0, pass},
		{"fail", "claude-reviewer", "", false, []string{"T-0001", "--fail", "--summary", "bad", "--cause", "bug"}, 0, fail},
		{"fail at another sha", "claude-reviewer", "", false, []string{"T-0001", "--fail", "--summary", "bad", "--cause", "bug", "--sha", "deadbeefdead"}, 0, fail},
		{"qa pass without gates", "claude-qa", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", h}, 5, nil},
		{"pass without sha", "claude-reviewer", "", false, []string{"T-0001", "--pass", "--summary", "ok"}, 5, nil},
		{"pass at another sha", "claude-reviewer", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", "deadbeefdead"}, 5, nil},
		{"bad hex", "claude-reviewer", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", "xyz"}, 5, nil},
		{"started mismatch", "claude-qa2", g, false, []string{"T-0002", "--pass", "--summary", "-", "--sha", h}, 5, nil},
		{"fail without cause", "claude-reviewer", "", false, []string{"T-0001", "--fail", "--summary", "bad"}, 2, nil},
		{"fail bad cause", "claude-reviewer", "", false, []string{"T-0001", "--fail", "--summary", "bad", "--cause", "nope"}, 2, nil},
		{"owner without --as", "owner", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", h}, 1, nil},
		{"no PR", "claude-qa", "", false, []string{"T-0005", "--pass", "--summary", "ok", "--sha", h}, 1, nil},
		{"unknown task", "claude-qa", "", false, []string{"T-9999", "--pass", "--summary", "ok", "--sha", h}, 1, nil},
		{"gh fails", "claude-reviewer", "", true, []string{"T-0001", "--pass", "--summary", "ok", "--sha", h}, 1, nil},
		{"coder", "claude-coder", "", false, []string{"T-0001", "--pass", "--summary", "ok", "--sha", h}, 3, nil},
	} {
		got, code, stderr := verdictRun(t, tc.agent, tc.stdin, tc.ghFail, tc.argv...)
		if code != tc.code || !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: exit %d trace %v, want exit %d trace %v (stderr %q)", tc.name, code, got, tc.code, tc.want, stderr)
		}
	}
}

func TestParseGates(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		required   bool
		want       string
	}{
		{"none, optional", "just text", false, ""},
		{"none, required", "just text", true, "missing '## Gates' section"},
		{"ok", "## Gates\n- unit exit=0 cmd=go test ./...\n- lint:x.y exit=0 cmd=go vet", true, ""},
		{"two sections", "## Gates\n- a exit=0 cmd=x\n## Gates\n- b exit=0 cmd=y", true, "2 '## Gates' sections (exactly one allowed)"},
		{"empty section", "## Gates\n\n# Next\n- a exit=0 cmd=x", true, "'## Gates' section has no gate lines"},
		{"nonzero and unknown", "## Gates\n- a exit=0 cmd=x\n- b exit=2 cmd=y\n- c exit=unknown cmd=z\n- d exit=-1 cmd=w", true, "gate b exit=2; gate c exit=unknown; gate d exit=-1"},
		{"malformed", "x\n## Gates\n- a exit=0 cmd=x\nnope\n- b exit=0 cmd=", true, "line 4 malformed: 'nope'; line 5 malformed: '- b exit=0 cmd='"},
		{"duplicate", "## Gates\n- a exit=0 cmd=x\n- a exit=0 cmd=y", true, "duplicate name a (line 3)"},
		{"unicode space cmd", "## Gates\n- a exit=0 cmd= x", true, "line 2 malformed: '- a exit=0 cmd=\\xa0x'"},
		{"unicode digit exit", "## Gates\n- a exit=٠ cmd=x", true, "gate a exit=٠"},
		{"optional but present and bad", "## Gates\n- a exit=1 cmd=x", false, "gate a exit=1"},
		{"section ends at any heading", "## Gates\n- a exit=0 cmd=x\n#notgates\nloose line", true, ""},
		{"trailing spaces and CRLF", "## Gates  \r\n- a exit=0 cmd=x  \r\n", true, ""},
	} {
		if got := verdictGatesRefusal(tc.text, tc.required); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestRejectionCausesMatchPython reads REJECTION_CAUSES out of bin/factory_cli.py.
func TestRejectionCausesMatchPython(t *testing.T) {
	src, err := os.ReadFile("../../../bin/factory_cli.py")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?s)\nREJECTION_CAUSES = \((.*?)\)\n`).FindSubmatch(src)
	if m == nil {
		t.Fatal("REJECTION_CAUSES not found in bin/factory_cli.py")
	}
	var want []string
	for _, q := range regexp.MustCompile(`"([a-z_]+)"`).FindAllSubmatch(m[1], -1) {
		want = append(want, string(q[1]))
	}
	if !reflect.DeepEqual(verdictRejectionCauses, want) {
		t.Errorf("Go causes %v, Python %v", verdictRejectionCauses, want)
	}
}

func TestVerdictNormalizeCause(t *testing.T) {
	for in, want := range map[string]string{" Missing-Test ": "missing_test", "stale or  conflicting-branch": "stale_or_conflicting_branch", "BUG": "bug", "": "", "  \t": ""} {
		in := in
		if got, err := verdictNormalizeCause(&in); err != nil || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, err, want)
		}
	}
	bad := "it's bad"
	if _, err := verdictNormalizeCause(&bad); err == nil || !strings.Contains(err.Error(), `unknown rejection cause "it's bad"; use one of: unclear_spec`) {
		t.Errorf("unknown cause error: %v", err)
	}
}

func TestParseVerdict(t *testing.T) {
	for _, tc := range []struct {
		name string
		argv []string
		ok   bool
	}{
		{"plain", []string{"T-1", "--pass", "--summary", "x"}, true},
		{"equals", []string{"--fail", "--summary=x", "T-1", "--cause=bug"}, true},
		{"stdin dash", []string{"T-1", "--pass", "--summary", "-"}, true},
		{"abbrev", []string{"T-1", "--pas", "--summary", "x"}, false},
		{"both", []string{"T-1", "--pass", "--fail", "--summary", "x"}, false},
		{"neither", []string{"T-1", "--summary", "x"}, false},
		{"no summary", []string{"T-1", "--pass"}, false},
		{"no task", []string{"--pass", "--summary", "x"}, false},
		{"two tasks", []string{"T-1", "T-2", "--pass", "--summary", "x"}, false},
		{"repeated", []string{"T-1", "--pass", "--summary", "x", "--summary", "y"}, false},
		{"dash value", []string{"T-1", "--pass", "--summary", "-x"}, false},
		{"bad as", []string{"T-1", "--pass", "--summary", "x", "--as", "judge"}, false},
		{"help", []string{"-h"}, false},
		{"dashdash", []string{"--", "T-1", "--pass", "--summary", "x"}, false},
		{"pass=value", []string{"T-1", "--pass=1", "--summary", "x"}, false},
		{"non-ascii cause", []string{"T-1", "--fail", "--summary", "x", "--cause", "büg"}, false},
		{"non-ascii summary", []string{"T-1", "--pass", "--summary", "bü"}, true},
	} {
		if _, ok := parseVerdict(tc.argv); ok != tc.ok {
			t.Errorf("%s: ok=%v, want %v", tc.name, ok, tc.ok)
		}
	}
}

func TestVerdictRepr(t *testing.T) {
	for in, want := range map[string]string{"nope": "'nope'", "it's": `"it's"`, `a"b'c`: `'a"b\'c'`, "a\tb\\": `'a\tb\\'`, " x": `'\xa0x'`, "é": "'é'", "\u200bz": `'\u200bz'`} {
		if got := verdictRepr(in); got != want {
			t.Errorf("repr(%q) = %s, want %s", in, got, want)
		}
	}
}
