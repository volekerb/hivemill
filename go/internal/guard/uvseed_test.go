package guard

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// The blocked uv/uvx runner forms of tests/test_worker_guard_uv_runner.py, run with test_sandbox=1 seeded in a temp DB
// (exit 2) and without it (exit 0). Go finds the runner; the test_sandbox row is read by the Python hook, which the
// test runs exactly as the binary does (RunPython). The expected exits are those of the Python hook run directly on
// the same payload.

// seedFactoryDB creates <home>/db/factory.db with the statements (sqlite is Python's; Go opens none).
func seedFactoryDB(t *testing.T, stmts ...string) string {
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

func hookPath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("../../../roles/hooks/worker-guard.py")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("the Python hook is needed: %v", err)
	}
	return p
}

// exitThroughBinaryPath is what main does for a Bash payload: Check, and the Python hook for a delegation.
func exitThroughBinaryPath(t *testing.T, cmd, role string) int {
	t.Helper()
	err := Check(cmd, "", role, false)
	if !IsDelegate(err) {
		return exitOf(err)
	}
	payload, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
	return RunPython([]string{"--role", role}, payload, &bytes.Buffer{}, &bytes.Buffer{})
}

func pythonExit(t *testing.T, cmd, role string) int {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
	c := exec.Command("python3", hookPath(t), "--role", role)
	c.Stdin = bytes.NewReader(payload)
	c.Env = os.Environ()
	err := c.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		t.Fatal(err)
	}
	return 0
}

func TestUvRunnersSeededTestSandbox(t *testing.T) {
	exportTerms(t) // spawn exports it: only the test_sandbox row is left for the Python hook
	SetPyHook(hookPath(t))
	t.Cleanup(func() { SetPyHook("") })
	on := seedFactoryDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('test_sandbox', '1')")
	off := seedFactoryDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)")
	const url = "https://pypi.example.org/simple"
	blocked := []string{
		"uvx pytest", "uvx --from x pytest", "uvx pytest==8.0", "uv tool run pytest", "uv --quiet tool run py.test",
		"uvx --from x unittest", "uvx 'pytest >=8'", "uvx 'pytest 8'", "uvx --index-url " + url + " pytest",
		"uv -q tool run pytest", "uv tool -q run pytest", "uv tool uvx pytest", "uvx 'pytest[testing]'",
		"uvx pytest" + "@" + "8.0", "uvx 'pytest ; python_version>\"3\"'", "uvx path/to/py.test",
	}
	allowed := []string{"uvx ruff", "uv tool run black", "uvx pytest-cov", "uvx 'ruff >=0.5'", "uvx pytestx"}
	for _, role := range []string{"coder", "qa"} {
		for _, c := range blocked {
			t.Setenv("FACTORY_HOME", on)
			if got, want := exitThroughBinaryPath(t, c, role), pythonExit(t, c, role); got != 2 || want != 2 {
				t.Errorf("%s %q with test_sandbox=1: go path exit %d, python exit %d, want 2", role, c, got, want)
			}
			t.Setenv("FACTORY_HOME", off)
			if got, want := exitThroughBinaryPath(t, c, role), pythonExit(t, c, role); got != 0 || want != 0 {
				t.Errorf("%s %q without test_sandbox: go path exit %d, python exit %d, want 0", role, c, got, want)
			}
		}
		for _, c := range allowed {
			t.Setenv("FACTORY_HOME", on)
			if got := exitThroughBinaryPath(t, c, role); got != 0 {
				t.Errorf("%s %q with test_sandbox=1: exit %d, want 0", role, c, got)
			}
		}
	}
}

// With the Python hook made unreadable a payload that needs a DB decision exits 2 (acceptance 4).
func TestUvDelegationFailsClosedWithoutThePythonHook(t *testing.T) {
	clearTermsEnv(t)
	on := seedFactoryDB(t, "CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('test_sandbox', '1')")
	t.Setenv("FACTORY_HOME", on)
	for _, hook := range []string{"", filepath.Join(t.TempDir(), "missing.py"), t.TempDir()} {
		SetPyHook(hook)
		var stderr bytes.Buffer
		if code := RunPython([]string{"--role", "coder"}, []byte(`{"tool_name":"Bash","tool_input":{"command":"uvx ruff"}}`), &bytes.Buffer{}, &stderr); code != 2 || stderr.Len() == 0 {
			t.Errorf("hook %q: exit %d stderr %q, want 2 with a message", hook, code, stderr.String())
		}
	}
	SetPyHook("")
}
