package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

const runMainEnv = "WORKER_GUARD_TEST_RUN_MAIN"

// TestMain lets the test binary stand in for the worker-guard binary: with runMainEnv set it runs main()
// (exit code and streams are the real ones); the tests below call it as a subprocess.
func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		main()
		return
	}
	os.Exit(m.Run())
}

func call(t *testing.T, stdin []byte, env []string, args ...string) (int, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	cmd.Env = append([]string{runMainEnv + "=1"}, env...)
	cmd.Stdin = bytes.NewReader(stdin)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), stderr.String()
	}
	t.Fatal(err)
	return -1, ""
}

// The expected exit codes below were taken from roles/hooks/worker-guard.py on origin/main (the same payload
// piped into the Python hook): bad JSON, a non-object, an unhashable tool_name, a non-string command and
// invalid UTF-8 all fail closed (2); other tools and a missing tool_name are allowed (0).
func TestIOContract(t *testing.T) {
	coder := []string{"--role", "coder"}
	cases := []struct {
		name   string
		in     string
		args   []string
		code   int
		stderr string
	}{
		{"plain command allowed", `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`, coder, 0, ""},
		{"plain command allowed in hq mode", `{"tool_name":"Bash","tool_input":{"command":"ls -la"}}`, []string{"--hq"}, 0, ""},
		{"sudo blocked", `{"tool_name":"Bash","tool_input":{"command":"sudo ls"}}`, coder, 2, "privilege escalation (`sudo`)"},
		{"sudo blocked in hq mode", `{"tool_name":"Bash","tool_input":{"command":"sudo ls"}}`, []string{"--hq"}, 2, "privilege escalation (`sudo`)"},
		{"direct herdr blocked", `{"tool_name":"Bash","tool_input":{"command":"herdr server stop"}}`, coder, 2, "direct herdr invocation"},
		{"git is a stub of a later section", `{"tool_name":"Bash","tool_input":{"command":"git status"}}`, coder, 2, "not ported"},
		{"cwd not a string", `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":5}`, coder, 2, "cwd is not a string"},
		{"cwd null", `{"tool_name":"Bash","tool_input":{"command":"ls"},"cwd":null}`, coder, 0, ""},
		{"bad json", `not json`, coder, 2, "failing closed"},
		{"empty stdin", ``, coder, 2, "failing closed"},
		{"array", `[]`, coder, 2, "failing closed"},
		{"null", `null`, coder, 2, "failing closed"},
		{"trailing data", `{"tool_name":"Task"} x`, coder, 2, "failing closed"},
		{"invalid utf-8", "\xff", coder, 2, "UTF-8"},
		{"invalid utf-8 in a value", "{\"tool_name\":\"Task\",\"x\":\"\xff\"}", coder, 2, "UTF-8"},
		{"tool_name list", `{"tool_name":["a"]}`, coder, 2, "tool_name"},
		{"other tool allowed", `{"tool_name":"Task"}`, coder, 0, ""},
		{"numeric tool_name allowed", `{"tool_name":5}`, coder, 0, ""},
		{"no tool_name allowed", `{}`, coder, 0, ""},
		{"command not a string", `{"tool_name":"Bash","tool_input":{"command":5}}`, coder, 2, "no command string"},
		{"tool_input not an object", `{"tool_name":"Bash","tool_input":"x"}`, coder, 2, "no command string"},
		{"unbalanced quote", `{"tool_name":"Bash","tool_input":{"command":"echo 'a"}}`, coder, 2, "unbalanced quote"},
		{"edit not ported", `{"tool_name":"Edit","tool_input":{"file_path":"/x"}}`, coder, 2, "not ported"},
		{"write not ported", `{"tool_name":"Write","tool_input":{}}`, coder, 2, "not ported"},
		{"edit in hq mode allowed", `{"tool_name":"Edit","tool_input":{}}`, []string{"--hq"}, 0, ""},
		{"read by a terms role not ported", `{"tool_name":"Read","tool_input":{}}`, coder, 2, "not ported"},
		{"read by another role allowed", `{"tool_name":"Read","tool_input":{}}`, []string{"--role", "scout"}, 0, ""},
		{"read without a role allowed", `{"tool_name":"Read","tool_input":{}}`, nil, 0, ""},
		{"--role as the last argument is no role", `{"tool_name":"Read","tool_input":{}}`, []string{"--role"}, 0, ""},
		{"read in hq mode allowed", `{"tool_name":"Read","tool_input":{}}`, []string{"--hq", "--role", "coder"}, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			code, stderr := call(t, []byte(c.in), nil, c.args...)
			if code != c.code || !strings.Contains(stderr, c.stderr) {
				t.Errorf("exit %d stderr %q, want exit %d containing %q", code, stderr, c.code, c.stderr)
			}
		})
	}
}

func TestInputSizeCap(t *testing.T) {
	const overhead = len(`{"tool_name":"Task","pad":""}`)
	pad := func(n int) []byte {
		return []byte(`{"tool_name":"Task","pad":"` + strings.Repeat("a", n) + `"}`)
	}
	if code, stderr := call(t, pad(maxInput-overhead), nil, "--role", "coder"); code != 0 {
		t.Errorf("payload of exactly 1 MiB: exit %d %q, want 0", code, stderr)
	}
	code, stderr := call(t, pad(maxInput-overhead+1), nil, "--role", "coder")
	if code != 2 || !strings.Contains(stderr, "1 MiB") {
		t.Errorf("payload above 1 MiB: exit %d %q, want 2 mentioning 1 MiB", code, stderr)
	}
}

func TestPanicFailsClosed(t *testing.T) {
	code, stderr := call(t, []byte(`{"tool_name":"Task"}`), []string{testPanicEnv + "=1"}, "--role", "coder")
	if code != 2 || !strings.Contains(stderr, "worker-guard(go): internal error") {
		t.Errorf("exit %d stderr %q, want 2 with the internal error line", code, stderr)
	}
	// without the injected panic the same payload is allowed, so the test cannot pass by accident
	if code, _ := call(t, []byte(`{"tool_name":"Task"}`), nil, "--role", "coder"); code != 0 {
		t.Errorf("same payload without the panic: exit %d, want 0", code)
	}
}

func TestParseArgs(t *testing.T) {
	cases := []struct {
		args []string
		role string
		hq   bool
	}{
		{nil, "", false},
		{[]string{"--role", "qa"}, "qa", false},
		{[]string{"--hq"}, "", true},
		{[]string{"--hq", "--role", "judge"}, "judge", true},
		{[]string{"--role"}, "", false},
		{[]string{"--role", "a", "--role", "b"}, "a", false},
		{[]string{"--role", "--hq"}, "--hq", true},
	}
	for _, c := range cases {
		if role, hq := parseArgs(c.args); role != c.role || hq != c.hq {
			t.Errorf("parseArgs(%q) = (%q, %v), want (%q, %v)", c.args, role, hq, c.role, c.hq)
		}
	}
}
