// Package cursor holds the Go ports of the cursor-harness Python test modules (T-1686, plan P6f-8):
// test_cursor_attribution, test_cursor_mcp, test_cursor_pilot, test_cursor_policy_outside_git,
// test_cursor_proc_gate, test_cursor_workspace, test_cursor_worktree_policy, test_ci_cursor_guard,
// test_spawn_strict_mcp and test_herdr_app_dashboard. The Python modules stay as parity oracles.
//
// Commands the Go CLI still hands to bin/factory (spawn, relaunch, init, cursor-pilot prepare/probe) and the Python
// hook adapter roles/hooks/cursor-guard-adapter.py are driven through the real entry point; those tests skip
// visibly when python3 is not on PATH (Env.NeedPython / needPython).
package cursor

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/scout"
)

const director = "zz-test-director"

func repoRoot(t testing.TB) string {
	t.Helper()
	r, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func hooksDir(t testing.TB) string { return filepath.Join(repoRoot(t), "roles", "hooks") }

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func notContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

func eq[T comparable](t testing.TB, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func ok(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func readFile(t testing.TB, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func writeFile(t testing.TB, p, text string, mode os.FileMode) {
	t.Helper()
	ok(t, os.MkdirAll(filepath.Dir(p), 0o755))
	ok(t, os.WriteFile(p, []byte(text), mode))
}

func isFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func mkdir(t testing.TB, p string) string {
	t.Helper()
	ok(t, os.MkdirAll(p, 0o755))
	return p
}

// realTemp is a temp dir with symlinks resolved (Python's Path(tmp).resolve()).
func realTemp(t testing.TB) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	ok(t, err)
	return d
}

func parseJSON(t testing.TB, text string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, text)
	}
	return m
}

func readJSON(t testing.TB, p string) map[string]any { t.Helper(); return parseJSON(t, readFile(t, p)) }

func sub(t testing.TB, m map[string]any, keys ...string) map[string]any {
	t.Helper()
	for _, k := range keys {
		v, isMap := m[k].(map[string]any)
		if !isMap {
			t.Fatalf("key %q is not an object in %v", k, m)
		}
		m = v
	}
	return m
}

func list(t testing.TB, m map[string]any, key string) []any {
	t.Helper()
	l, isList := m[key].([]any)
	if !isList {
		t.Fatalf("key %q is not a list in %v", key, m)
	}
	return l
}

func strs(l []any) []string {
	out := make([]string, len(l))
	for i, v := range l {
		out[i], _ = v.(string)
	}
	return out
}

// hooksOf is cursor_hooks_json(HOOKS, role) as a generic JSON value.
func hooksOf(t testing.TB, role string, hq bool) map[string]any {
	t.Helper()
	return parseJSON(t, scout.Dumps(ch.HooksJSON(hooksDir(t), role, hq), 2, false))
}

// hookCommand is data["hooks"]["beforeShellExecution"][0]["command"] of a materialised hooks.json.
func hookCommand(t testing.TB, hooksPath string) string {
	t.Helper()
	entries := list(t, sub(t, readJSON(t, hooksPath), "hooks"), "beforeShellExecution")
	cmd, _ := entries[0].(map[string]any)["command"].(string)
	return cmd
}

// failClosedOK is fail_closed_ok: every hook entry sets failClosed true.
func failClosedOK(t testing.TB, hooksPath string) bool {
	t.Helper()
	for _, v := range sub(t, readJSON(t, hooksPath), "hooks") {
		for _, it := range v.([]any) {
			if fc, _ := it.(map[string]any)["failClosed"].(bool); !fc {
				return false
			}
		}
	}
	return true
}

// materialize is materialize_cursor_policy with the empty claude_settings of the Python tests.
func materialize(t testing.TB, dir, role string, o ch.MaterializeOpts) ch.Prepared {
	t.Helper()
	if o.HooksDir == "" {
		o.HooksDir = hooksDir(t)
	}
	if o.SystemPrompt == "" {
		o.SystemPrompt = "# " + role + "\n"
	}
	p, err := ch.MaterializeCursorPolicy(dir, role, o)
	if err != nil {
		t.Fatalf("materialize %s: %v", dir, err)
	}
	return p
}

// passwdHome is the passwd home of the current user (the guard uses it, not $HOME).
func passwdHome(t testing.TB) string {
	t.Helper()
	u, err := user.Current()
	ok(t, err)
	return u.HomeDir
}

func needPython(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH: this test drives the Python hook adapter or installer; it runs where python3 exists")
	}
	return p
}

type adapterRun struct {
	Stdout, Stderr string
	Code           int
}

// runAdapter runs roles/hooks/cursor-guard-adapter.py as Cursor does: argv flags, the payload on stdin, a 60 s limit
// (the Python tests' timeout=60). env is added to a clean copy of the process environment.
func runAdapter(t testing.TB, stdin string, env map[string]string, args ...string) adapterRun {
	t.Helper()
	return runPython(t, stdin, env, filepath.Join(hooksDir(t), ch.AdapterName), args...)
}

// adapterEvent runs the adapter for one hook event of role with a JSON payload and a temp FACTORY_HOME.
func adapterEvent(t testing.TB, event string, payload any, role string, hq bool, env map[string]string) adapterRun {
	t.Helper()
	b, err := json.Marshal(payload)
	ok(t, err)
	if env == nil {
		env = map[string]string{}
	}
	if _, set := env["FACTORY_HOME"]; !set {
		env["FACTORY_HOME"] = t.TempDir()
	}
	args := []string{"--event", event}
	if hq {
		args = append(args, "--hq")
	}
	args = append(args, "--role", role)
	return runAdapter(t, string(b), env, args...)
}

// lastJSON parses the last stdout line (the adapter's decision).
func lastJSON(t testing.TB, stdout string) map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	return parseJSON(t, lines[len(lines)-1])
}

// ---- spawn fixtures ------------------------------------------------------------------------------------------------

// cursorEnv is the setUp shared by the spawn tests: goal G-001, team g-001-a, the director, a fake `agent` binary and
// agent_runtimes=runtimes ("" leaves it empty).
func cursorEnv(t *testing.T, runtimes string) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()                                                                // spawn still runs in Python
	e.HerdrStateful()                                                             // the Python harness stub keeps state: tab create / agent start add rows
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'") // the harness pins the TUI backend
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(ft.AgentRow{Name: director, Role: "director", Goal: "G-001", Team: "g-001-a"})
	writeFile(t, filepath.Join(e.StubBin, "agent"), "#!/bin/sh\necho ok\n", 0o755)
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", runtimes)
	return e
}

func asDirector(e *ft.Env, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(ft.Opts{Agent: director}, args...)
}

// startsSince is the `agent start` herdr calls after the first n calls.
func startsSince(e *ft.Env, n int) [][]string {
	var out [][]string
	for _, c := range e.Calls("herdr")[n:] {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			out = append(out, c.Argv)
		}
	}
	return out
}

func indexOf(argv []string, want string) int {
	for i, a := range argv {
		if a == want {
			return i
		}
	}
	return -1
}

// flagValue is argv[argv.index(flag) + 1]; the flag must be there.
func flagValue(t testing.TB, argv []string, flag string) string {
	t.Helper()
	i := indexOf(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		t.Fatalf("%s missing in %v", flag, argv)
	}
	return argv[i+1]
}

// agentArgv is the part of an `agent start` argv after the "--" (the cursor agent's own argv).
func agentArgv(t testing.TB, argv []string) []string {
	t.Helper()
	i := indexOf(argv, "--")
	if i < 0 {
		t.Fatalf("no -- in %v", argv)
	}
	return argv[i+1:]
}

func addDirs(argv []string) []string { return ch.AddDirsFromArgv(argv) }

func stateCursorDir(e *ft.Env) string {
	return filepath.Join(e.UserHome, ".local", "state", "factory", "cursor")
}

// runPython runs python3 script args... with stdin, 60 s limit; env is added to the process environment.
func runPython(t testing.TB, stdin string, env map[string]string, script string, args ...string) adapterRun {
	t.Helper()
	py := needPython(t)
	return runCmd(t, py, stdin, env, append([]string{script}, args...)...)
}

func runCmd(t testing.TB, bin, stdin string, env map[string]string, args ...string) adapterRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "FACTORY_CURSOR_HOOK_LOG=") { // the log path is only ever what the test sets
			cmd.Env = append(cmd.Env, kv)
		}
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	err := cmd.Run()
	if ctx.Err() != nil {
		t.Fatalf("%s %v timed out after 60s", bin, args)
	}
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("%s %v: %v", bin, args, err)
		}
		code = ee.ExitCode()
	}
	return adapterRun{so.String(), se.String(), code}
}

// sqlExec runs one write statement on the env's DB.
func sqlExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// pyDriver loads bin/factory_cli.py the way the Python tests' load_factory does and runs a snippet against `mod`; the
// snippet fills `res`, which is printed as JSON. It is only for Python-internal functions that have no Go
// counterpart yet (cursor_hooks_ok, flagless_state, flagless_scan); each such test is marked as one.
const pyDriver = `import importlib.machinery, importlib.util, json, sys, os, io, contextlib
repo = sys.argv[1]
sys.path.insert(0, repo)
loader = importlib.machinery.SourceFileLoader("factory_cli_t1686", os.path.join(repo, "bin", "factory_cli.py"))
mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
with contextlib.redirect_stdout(io.StringIO()):
    loader.exec_module(mod)
res = {}
exec(sys.argv[2])
print(json.dumps(res, default=str))
`

// pyEval runs snippet in the Python factory module under the env's FACTORY_HOME and returns `res`.
func pyEval(t testing.TB, e *ft.Env, snippet string) map[string]any { return pyEvalArg(t, e, snippet) }

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	ok(t, err)
	return b
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

// pyEvalArg is pyEval with extra arguments (sys.argv[3:]).
func pyEvalArg(t testing.TB, e *ft.Env, snippet string, args ...string) map[string]any {
	t.Helper()
	py := needPython(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, append([]string{"-c", pyDriver, repoRoot(t), snippet}, args...)...)
	cmd.Env = e.Environ("", nil)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Run(); err != nil {
		t.Fatalf("python snippet failed: %v\n%s\n%s", err, so.String(), se.String())
	}
	return lastJSON(t, so.String())
}
