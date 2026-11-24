// Package guardtools holds the Go ports of the Python guard-tooling test modules (T-1688, SC4 P6f-10):
// tests/test_priv_guard.py, tests/test_guard_build.py, tests/test_guard_diff.py and
// tests/test_guard_impl_coverage.py. The tests drive the Go worker-guard binary built from go/cmd/worker-guard,
// exactly as tests/guard_impl.py hook_run does under GUARD_IMPL=go, against a throwaway FACTORY_HOME.
package guardtools

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// hookTimeout is the tests/guard_impl.py hook_run timeout (30 s): a hung hook ends the run, never the suite.
const hookTimeout = 30 * time.Second

var (
	guardBin  string // the built worker-guard
	guardHome string // the empty FACTORY_HOME every run uses unless a test brings its own (setUpModule of the Python module)
	buildErr  error
)

// TestMain builds worker-guard once, the way tests/guard_impl.py go_bin() does (GOTOOLCHAIN=local, -mod=readonly,
// GOPROXY=off, a build failure is fatal, never a skip), and makes the empty temp FACTORY_HOME the hook runs
// against: the hook reads public_text_repos from the home's DB, so a caller's live home must not leak in (T-0636).
func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "guardtools-")
	if err != nil {
		fmt.Fprintln(os.Stderr, "guardtools:", err)
		os.Exit(2)
	}
	code := func() int {
		defer os.RemoveAll(tmp)
		guardHome = filepath.Join(tmp, "home")
		if err := os.MkdirAll(guardHome, 0o755); err != nil {
			fmt.Fprintln(os.Stderr, "guardtools:", err)
			return 2
		}
		guardBin, buildErr = buildGuard(tmp)
		return m.Run()
	}()
	os.Exit(code)
}

func goTool() (string, error) {
	if p, err := exec.LookPath("go"); err == nil {
		return p, nil
	}
	p := filepath.Join(runtime.GOROOT(), "bin", "go")
	if _, err := os.Stat(p); err == nil {
		return p, nil
	}
	return "", fmt.Errorf("go not found on PATH or in GOROOT; install Go (a guard test never skips)")
}

func buildGuard(dir string) (string, error) {
	goBin, err := goTool()
	if err != nil {
		return "", err
	}
	mod, err := ft.ModuleRoot()
	if err != nil {
		return "", err
	}
	out := filepath.Join(dir, "worker-guard")
	cmd := exec.Command(goBin, "build", "-o", out, "./cmd/worker-guard")
	cmd.Dir = mod
	cmd.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	if b, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build ./cmd/worker-guard failed: %v\n%s\nfix the Go build first", err, b)
	}
	return out, nil
}

func needGuard(t testing.TB) {
	t.Helper()
	if buildErr != nil {
		t.Fatalf("guardtools: %v", buildErr)
	}
}

// result is one hook run.
type result struct {
	exit   int
	stderr string
}

// runOpts tunes a hook run: the home (default guardHome), the working directory and extra environment.
type runOpts struct {
	home string
	dir  string
	env  map[string]string
}

// hookRun runs the worker-guard binary with payload on stdin and args as its arguments, in a clean environment
// (nothing inherited but PATH): no FACTORY_AGENT, no GIT_*, no live home.
func hookRun(t testing.TB, payload string, args []string, o runOpts) result {
	t.Helper()
	needGuard(t)
	home := o.home
	if home == "" {
		home = guardHome
	}
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	env := map[string]string{"PATH": path, "HOME": home, "FACTORY_HOME": home, "LC_ALL": "C.UTF-8"}
	for k, v := range o.env {
		env[k] = v
	}
	cmd := exec.Command(guardBin, args...)
	cmd.Dir = o.dir
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = strings.NewReader(payload)
	var se bytes.Buffer
	cmd.Stderr = &se
	if err := cmd.Start(); err != nil {
		t.Fatalf("guardtools: start %s: %v", guardBin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(hookTimeout):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("guardtools: worker-guard %v timed out after %s on %q", args, hookTimeout, payload)
	}
	if err == nil {
		return result{0, se.String()}
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return result{ee.ExitCode(), se.String()}
	}
	t.Fatalf("guardtools: run worker-guard %v: %v", args, err)
	return result{}
}

func bashPayload(cmd string) string {
	b, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// Modes are the two hook modes of test_priv_guard.MODES.
var modes = map[string][]string{"worker": {"--role", "coder"}, "hq": {"--hq"}}

var modeNames = []string{"worker", "hq"}

// runCmd is run_hook(cmd, mode, role): a Bash payload under mode ("" = none) and role ("" = none; "-" never).
func runCmd(t testing.TB, cmd, mode, role string) result {
	t.Helper()
	return runCmdOpts(t, cmd, mode, role, runOpts{})
}

func runCmdOpts(t testing.TB, cmd, mode, role string, o runOpts) result {
	t.Helper()
	var args []string
	if mode != "" {
		args = append(args, modes[mode]...)
	}
	if role != "" {
		args = append(args, "--role", role)
	}
	return hookRun(t, bashPayload(cmd), args, o)
}

// blocked fails the test unless the hook exits 2 for cmd; it returns the result for message checks.
func blocked(t testing.TB, cmd, mode, role string) result {
	t.Helper()
	r := runCmd(t, cmd, mode, role)
	if r.exit != 2 {
		t.Errorf("should block (mode=%q role=%q): %q: exit %d\n%s", mode, role, cmd, r.exit, r.stderr)
	}
	return r
}

// allowed fails the test unless the hook exits 0 for cmd.
func allowed(t testing.TB, cmd, mode, role string) {
	t.Helper()
	if r := runCmd(t, cmd, mode, role); r.exit != 0 {
		t.Errorf("should allow (mode=%q role=%q): %q: exit %d\n%s", mode, role, cmd, r.exit, r.stderr)
	}
}
