package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
)

// The rules of section 4 (Edit/Write persist rules, Read/Grep terms rule, the decisions that read the factory DB and
// are the Python hook's), through the real binary. Each payload is also run through the Python hook and must give
// the same exit and stderr.

const pyHook = "../../../roles/hooks/worker-guard.py"

func hasEnv(env []string, name string) bool {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return true
		}
	}
	return false
}

func absHook(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs(pyHook)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the Python hook is needed for the delegation tests: %v", err)
	}
	return p
}

func passwdHome(t *testing.T) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return u.HomeDir
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func callPython(t *testing.T, hook string, stdin []byte, env []string, args ...string) (int, string) {
	t.Helper()
	if !hasEnv(env, "FACTORY_HOME") {
		env = append([]string{"FACTORY_HOME=" + t.TempDir()}, env...)
	}
	if !hasEnv(env, "PATH") {
		env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	}
	cmd := exec.Command("python3", append([]string{hook}, args...)...)
	cmd.Env = env
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

// seedDB creates a factory DB in a new temp home with the given statements (sqlite is Python's: the Go binary
// opens none) and returns the home.
func seedDB(t *testing.T, stmts ...string) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "import sqlite3, sys\ncon = sqlite3.connect(sys.argv[1])\nfor s in sys.argv[2:]:\n    con.execute(s)\ncon.commit()\ncon.close()\n"
	out, err := exec.Command("python3", append([]string{"-I", "-c", script, filepath.Join(home, "db", "factory.db")}, stmts...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("seeding the DB: %v\n%s", err, out)
	}
	return home
}

func bashPayload(cmd string) []byte {
	return []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(cmd) + `}}`)
}

// withTerms adds an exported terms file to env, as spawn does for the workers: the terms roles are then decided by
// Go itself instead of being delegated for the DB's scrub_terms_file key.
func withTerms(t *testing.T, env ...string) []string {
	t.Helper()
	f := filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(f, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return append(env, "SCRUB_TERMS_"+"FILE="+f)
}

// same runs payload through the Go binary (with --py-hook) and through the Python hook and requires the same exit
// and stderr; it returns the exit.
func same(t *testing.T, hook string, payload []byte, env []string, role string) (int, string) {
	t.Helper()
	code, stderr := call(t, payload, env, "--role", role, "--py-hook", hook)
	pyCode, pyStderr := callPython(t, hook, payload, env, "--role", role)
	if pyCode != code || pyStderr != stderr {
		t.Errorf("%s as %s: go exit %d %q, python exit %d %q", payload, role, code, stderr, pyCode, pyStderr)
	}
	return code, stderr
}

// Edit/Write payloads: a write to a shell init file is refused, a write under the worktree is allowed (acceptance 5).
func TestEditWriteThroughBinary(t *testing.T) {
	home := passwdHome(t)
	wt, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, tool, path string
		code             int
	}{
		{"bashrc", "Write", home + "/.bashrc", 2},
		{"gitconfig by Edit", "Edit", home + "/.gitconfig", 2},
		{"tilde", "Write", "~/.zshrc", 2},
		{"HOME variable", "Write", "$HOME/.profile", 2},
		{"git config tree", "MultiEdit", home + "/.config/git/config", 2},
		{"dotdot", "Write", home + "/x/../.bashrc", 2},
		{"repo config of a clone", "Write", wt + "/.git/config", 2},
		{"under the worktree", "Write", wt + "/new_file.txt", 0},
		{"relative path", "Edit", "docs/x.md", 0},
		{"notebook path", "NotebookEdit", home + "/.bashrc", 2},
	}
	hook := absHook(t)
	for _, c := range cases {
		payload := `{"tool_name":"` + c.tool + `","tool_input":{"file_path":` + jsonString(c.path) + `,"content":"x"}}`
		if c.tool == "NotebookEdit" {
			payload = `{"tool_name":"NotebookEdit","tool_input":{"notebook_path":` + jsonString(c.path) + `}}`
		}
		if code, stderr := same(t, hook, []byte(payload), nil, "coder"); code != c.code {
			t.Errorf("%s: exit %d %q, want %d", c.name, code, stderr, c.code)
		}
	}
}

// A Read/Grep aimed at the owner-terms file is refused for the terms roles.
func TestTermsToolThroughBinary(t *testing.T) {
	dir := t.TempDir()
	terms := filepath.Join(dir, "terms.txt")
	if err := os.WriteFile(terms, []byte("secret\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	hook := absHook(t)
	env := []string{"SCRUB_TERMS_FILE=" + terms}
	for _, c := range []struct {
		payload string
		role    string
		code    int
	}{
		{`{"tool_name":"Read","tool_input":{"file_path":` + jsonString(terms) + `}}`, "coder", 2},
		{`{"tool_name":"Read","tool_input":{"file_path":` + jsonString(terms) + `}}`, "scout", 0},
		{`{"tool_name":"Grep","tool_input":{"pattern":"s","path":` + jsonString(dir) + `}}`, "qa", 2},
		{`{"tool_name":"Grep","tool_input":{"pattern":"s","path":"/nonexistent-dir"}}`, "qa", 0},
		{`{"tool_name":"Read","tool_input":{"file_path":"$SCRUB_TERMS_FILE"}}`, "pm", 2},
		{`{"tool_name":"Read","tool_input":{}}`, "coder", 2},
		{`{"tool_name":"Read","tool_input":{"file_path":"/etc/hostname"}}`, "coder", 0},
	} {
		if code, stderr := same(t, hook, []byte(c.payload), env, c.role); code != c.code {
			t.Errorf("%s as %s: exit %d %q, want %d", c.payload, c.role, code, stderr, c.code)
		}
	}
}

// own_goal is read by the Python hook: a payload that needs it gets the Python exit and stderr (acceptance 4), and a
// hook that cannot be run fails closed.
func TestOwnGoalDelegatesToPython(t *testing.T) {
	hook := absHook(t)
	home := seedDB(t,
		"CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT, goal_id TEXT)",
		"INSERT INTO agents VALUES ('coder-t-9', 'coder', 'G-005')",
		"INSERT INTO agents VALUES ('nogoal', 'coder', NULL)")
	for _, c := range []struct {
		cmd, agent string
		code       int
	}{
		{"factory goal show G-005", "coder-t-9", 0},
		{"factory goal show G-006", "coder-t-9", 2},
		{"factory goal show G-005", "nogoal", 2},
		{"factory goal show G-005", "ghost", 2},
		{"factory goal list", "coder-t-9", 2},
		{"factory task show T-1", "coder-t-9", 0},
	} {
		env := withTerms(t, "FACTORY_HOME="+home, "FACTORY_AGENT="+c.agent)
		if code, stderr := same(t, hook, bashPayload(c.cmd), env, "coder"); code != c.code {
			t.Errorf("%q as %s: exit %d %q, want %d", c.cmd, c.agent, code, stderr, c.code)
		}
	}
	// the Python hook made unreadable (or not named at all): exit 2, never an allow
	env := withTerms(t, "FACTORY_HOME="+home, "FACTORY_AGENT=coder-t-9")
	for _, args := range [][]string{
		{"--role", "coder", "--py-hook", filepath.Join(t.TempDir(), "missing.py")},
		{"--role", "coder"},
		{"--role", "coder", "--py-hook", t.TempDir()},
	} {
		code, stderr := call(t, bashPayload("factory goal show G-005"), env, args...)
		if code != 2 || !strings.Contains(stderr, "failing closed") {
			t.Errorf("%v: exit %d %q, want 2 failing closed", args, code, stderr)
		}
	}
	// a hook that crashes (exit 1) is a block too
	bad := filepath.Join(t.TempDir(), "hook.py")
	if err := os.WriteFile(bad, []byte("import sys\nsys.exit(1)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stderr := call(t, bashPayload("factory goal show G-005"), env, "--role", "coder", "--py-hook", bad)
	if code != 2 || !strings.Contains(stderr, "exited 1") {
		t.Errorf("crashing hook: exit %d %q, want 2", code, stderr)
	}
}

// The test_sandbox config is read by the Python hook: the blocked uv/uvx runner forms exit 2 with test_sandbox=1 and
// 0 without it (acceptance 8; forms from tests/test_worker_guard_uv_runner.py, expectations from the Python hook).
func TestUvRunnersWithSeededTestSandbox(t *testing.T) {
	hook := absHook(t)
	on := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('test_sandbox', '1')")
	off := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)")
	const url = "https://pypi.example.org/simple"
	blocked := []string{
		"uvx pytest", "uvx --from x pytest", "uvx pytest==8.0", "uv tool run pytest", "uv --quiet tool run py.test",
		"uvx --from x unittest", "uvx 'pytest >=8'", "uvx 'pytest 8'", "uvx --index-url " + url + " pytest",
		"uv -q tool run pytest", "uv tool -q run pytest", "uv tool uvx pytest", "uvx 'pytest[testing]'",
	}
	allowed := []string{"uvx ruff", "uv tool run black", "uvx pytest-cov", "uvx 'ruff >=0.5'"}
	for _, role := range []string{"coder"} { // qa runs the same code: internal/guard/uvseed_test.go covers both roles
		for _, c := range blocked {
			for _, w := range []struct {
				home string
				want int
			}{{on, 2}, {off, 0}} {
				env := withTerms(t, "FACTORY_HOME="+w.home)
				if code, stderr := same(t, hook, bashPayload(c), env, role); code != w.want {
					t.Errorf("%s %q (home %s): exit %d %q, want %d", role, c, w.home, code, stderr, w.want)
				}
			}
		}
		for _, c := range allowed {
			env := withTerms(t, "FACTORY_HOME="+on)
			if code, stderr := same(t, hook, bashPayload(c), env, role); code != 0 {
				t.Errorf("%s %q: exit %d %q, want 0", role, c, code, stderr)
			}
		}
	}
	// other roles are not routed
	env := withTerms(t, "FACTORY_HOME="+on)
	if code, stderr := same(t, hook, bashPayload("uvx pytest"), env, "pm"); code != 0 {
		t.Errorf("pm uvx pytest: exit %d %q, want 0", code, stderr)
	}
}

// The public-text rule is on only when the DB says so, and a coder's push is pinned to the worktree recorded in the
// DB: with a DB file both go to Python, which decides.
func TestPublicTextAndPushPinDelegate(t *testing.T) {
	hook := absHook(t)
	wt, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	off := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)",
		"CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)", "INSERT INTO tasks VALUES ('T-9', '"+wt+"')")
	on := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('frozen_repos', 'o/frozen')")
	for _, home := range []string{off, on} {
		for _, cmd := range []string{"git commit -m x", "git tag -a v1 -m x", "gh pr create --title t --body b",
			"gh api repos/o/r/issues -f title=t", "git status", "git push origin HEAD:factory/t-9"} {
			env := withTerms(t, "FACTORY_HOME="+home, "FACTORY_AGENT=coder-t-9")
			payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(cmd) + `},"cwd":` + jsonString(wt) + `}`)
			same(t, hook, payload, env, "coder")
		}
	}
}

// The scrub_terms_file config key is read by the Python hook: with no exported terms file the Go binary asks it, so a
// terms file known only to the DB is protected exactly as in Python (Bash, Read and Grep).
func TestTermsFileKnownOnlyToTheDB(t *testing.T) {
	hook := absHook(t)
	dir := t.TempDir()
	terms := filepath.Join(dir, "owner-terms.txt")
	if err := os.WriteFile(terms, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := seedDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO config VALUES ('scrub_terms_file', '"+terms+"')")
	env := []string{"FACTORY_HOME=" + home}
	for _, c := range []struct {
		payload string
		role    string
		code    int
	}{
		{string(bashPayload("cat " + terms)), "coder", 2},
		{string(bashPayload("ls -la")), "coder", 0},
		{string(bashPayload("cat " + terms)), "cleaner", 0},
		{`{"tool_name":"Read","tool_input":{"file_path":` + jsonString(terms) + `}}`, "coder", 2},
		{`{"tool_name":"Read","tool_input":{"file_path":"/etc/hostname"}}`, "qa", 0},
		{`{"tool_name":"Grep","tool_input":{"pattern":"x","path":` + jsonString(dir) + `}}`, "reviewer", 2},
	} {
		if code, stderr := same(t, hook, []byte(c.payload), env, c.role); code != c.code {
			t.Errorf("%s as %s: exit %d %q, want %d", c.payload, c.role, code, stderr, c.code)
		}
	}
}
