package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// T-0767: the DB-reading decisions come from run/guard-export.json. Each payload below gets the Python hook's exit and
// stderr on a seeded temp FACTORY_HOME, and runs with an unreadable --py-hook path: a delegation would exit 2 with
// "failing closed", so a matching answer proves no Python was started.
func TestDecisionsFromExportMatchPythonWithoutSpawn(t *testing.T) {
	hook := absHook(t)
	wt, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	terms := filepath.Join(t.TempDir(), "owner-terms.txt")
	if err := os.WriteFile(terms, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := seedDB(t,
		"CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)",
		"INSERT INTO config VALUES ('test_sandbox', '1')",
		"INSERT INTO config VALUES ('scrub_terms_file', '"+terms+"')",
		"CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT, goal_id TEXT)",
		"INSERT INTO agents VALUES ('coder-t-9', 'coder', 'G-005')",
		"CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)",
		"INSERT INTO tasks VALUES ('T-9', '"+wt+"')")
	other := t.TempDir()
	noPy := filepath.Join(t.TempDir(), "no-such-hook.py")
	cases := []struct {
		name, cmd, role, cwd string
		code                 int
	}{
		{"own_goal ok", "factory goal show G-005", "coder", wt, 0},
		{"own_goal other goal", "factory goal show G-006", "coder", wt, 2},
		{"git_push_pin ok", "git push origin HEAD:factory/t-9", "coder", wt, 0},
		{"git_push_pin -C own worktree", "git -C " + wt + " push origin HEAD:factory/t-9", "coder", wt, 0},
		{"git_push_pin wrong cwd", "git push origin HEAD:factory/t-9", "coder", other, 2},
		{"git_push_pin wrong -C", "git -C " + other + " push origin HEAD:factory/t-9", "coder", wt, 2},
		{"test_sandbox on blocks pytest", "pytest -q", "coder", wt, 2},
		{"test_sandbox on blocks unittest", "python3 -m unittest", "qa", wt, 2},
		{"test_sandbox on allows other", "ls -la", "coder", wt, 0},
		{"public_text off: commit", "git commit -m x", "coder", wt, 0},
		{"public_text off: gh pr create", "gh pr create --title t --body b", "coder", wt, 0},
		{"scrub_terms_file cat", "cat " + terms, "coder", wt, 2},
		{"scrub_terms_file ls", "ls -la", "pm", wt, 0},
	}
	env := []string{"FACTORY_HOME=" + home, "FACTORY_AGENT=coder-t-9"}
	for _, c := range cases {
		payload := []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(c.cmd) + `},"cwd":` + jsonString(c.cwd) + `}`)
		if code, stderr := same(t, hook, payload, env, c.role); code != c.code {
			t.Errorf("%s: exit %d %q, want %d", c.name, code, stderr, c.code)
		}
		code, stderr := call(t, payload, env, "--role", c.role, "--py-hook", noPy)
		if code != c.code || strings.Contains(stderr, "Python hook") {
			t.Errorf("%s without a Python hook: exit %d %q, want %d and no delegation", c.name, code, stderr, c.code)
		}
	}
	// the Read/Grep tools of the terms roles use the row as well
	for _, p := range []string{`{"tool_name":"Read","tool_input":{"file_path":` + jsonString(terms) + `}}`,
		`{"tool_name":"Read","tool_input":{"file_path":"/etc/hostname"}}`} {
		want := 0
		if strings.Contains(p, "owner-terms") {
			want = 2
		}
		if code, stderr := same(t, hook, []byte(p), env, "coder"); code != want {
			t.Errorf("%s: exit %d %q, want %d", p, code, stderr, want)
		}
		if code, stderr := call(t, []byte(p), env, "--role", "coder", "--py-hook", noPy); code != want {
			t.Errorf("%s without a Python hook: exit %d %q, want %d", p, code, stderr, want)
		}
	}
}

// Fail closed: a missing, unparsable or stale export is exit 2 for any decision that needs it, with a message that
// says how to fix it; a payload that needs nothing from the DB still passes.
func TestExportMissingUnparsableStaleExitsTwo(t *testing.T) {
	home := seedDB(t, "CREATE TABLE agents (name TEXT PRIMARY KEY, role TEXT, goal_id TEXT)",
		"INSERT INTO agents VALUES ('coder-t-9', 'coder', 'G-005')")
	file := filepath.Join(home, "run", "guard-export.json")
	good, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	stale := regexp.MustCompile(`"generated_at": ?"[^"]*"`).ReplaceAllString(string(good), `"generated_at": "2001-01-01T00:00:00+00:00"`)
	env := withTerms(t, "FACTORY_HOME="+home, "FACTORY_AGENT=coder-t-9")
	need := bashPayload("factory goal show G-005")
	for _, c := range []struct {
		name, content string
		remove        bool
		want          string
	}{
		{"missing", "", true, "does not exist"},
		{"unparsable", "{oops", false, "not valid JSON"},
		{"empty", "", false, "not valid JSON"},
		{"stale", stale, false, "is stale"},
	} {
		if c.remove {
			os.Remove(file)
		} else if err := os.WriteFile(file, []byte(c.content), 0o600); err != nil {
			t.Fatal(err)
		}
		code, stderr := call(t, need, env, "--role", "coder")
		if code != 2 || !strings.Contains(stderr, c.want) || !strings.Contains(stderr, "factory tick") {
			t.Errorf("%s: exit %d %q, want 2 with %q and the fix", c.name, code, stderr, c.want)
		}
		// a terms role reads scrub_terms_file from the export even with the terms variable set (union, round 2 b)
		if code, stderr := call(t, bashPayload("factory task show T-9"), env, "--role", "coder"); code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: coder factory task show: exit %d %q, want 2", c.name, code, stderr)
		}
		// a role that is not a terms role needs nothing from the export for its own tool calls
		if code, stderr := call(t, bashPayload("factory task show T-9"), env, "--role", "cleaner"); code != 0 {
			t.Errorf("%s: cleaner factory task show: exit %d %q, want 0", c.name, code, stderr)
		}
		// the Read/Grep terms tools need scrub_terms_file when no terms variable is exported
		terms := `{"tool_name":"Read","tool_input":{"file_path":"/etc/hostname"}}`
		if code, stderr := call(t, []byte(terms), []string{"FACTORY_HOME=" + home}, "--role", "coder"); code != 2 || !strings.Contains(stderr, c.want) {
			t.Errorf("%s: Read as coder without a terms variable: exit %d %q, want 2", c.name, code, stderr)
		}
	}
	if err := os.WriteFile(file, good, 0o600); err != nil {
		t.Fatal(err)
	}
	if code, stderr := call(t, need, env, "--role", "coder"); code != 0 {
		t.Errorf("fresh export: exit %d %q, want 0", code, stderr)
	}
}
