package tier

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Test helpers: a hermetic temp git repo and the python3 reference scripts of the real checkout (ci/*.py).
// Parity tests skip with a message when python3 or the reference script is absent.

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

// needPython skips the test unless python3 and the reference script (relative to the repo root) exist.
func needPython(t *testing.T, script string) string {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not found: parity tests compare against the Python reference and need it")
	}
	p := filepath.Join(repoRoot(t), script)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("reference script %s not found: parity tests need the Python original", script)
	}
	return p
}

// isolate makes the process environment hermetic: no GIT_* variables, a temp HOME and FACTORY_HOME (no DB there).
func isolate(t *testing.T) {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_HOME", filepath.Join(home, "factory"))
	t.Setenv("PYTHONDONTWRITEBYTECODE", "1")
	t.Setenv("CI", "true")
}

type fixture struct {
	t   *testing.T
	dir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	isolate(t)
	f := &fixture{t: t, dir: t.TempDir()}
	f.git("init", "-q", "-b", "main")
	return f
}

func (f *fixture) git(args ...string) string {
	f.t.Helper()
	full := append([]string{"-C", f.dir, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *fixture) write(rel, text string) {
	f.t.Helper()
	p := filepath.Join(f.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) remove(rel string) {
	f.t.Helper()
	if err := os.Remove(filepath.Join(f.dir, rel)); err != nil {
		f.t.Fatal(err)
	}
}

// commit stages everything, commits and returns the new HEAD sha.
func (f *fixture) commit(msg string) string {
	f.t.Helper()
	f.git("add", "-A")
	f.git("commit", "-q", "--allow-empty", "-m", msg)
	return f.git("rev-parse", "HEAD")
}

// runPython runs python3 script args in dir; returns stdout, stderr and the exit code.
func pyRun(t *testing.T, dir string, args ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command("python3", args...)
	cmd.Dir = dir
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return so.String(), se.String(), exitCode(t, err)
}

func exitCode(t *testing.T, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("cannot run the command: %v", err)
	return -1
}

// runGo runs a Main function in process.
func runGo(main func([]string, io.Writer, io.Writer) int, args ...string) (string, string, int) {
	var so, se bytes.Buffer
	code := main(args, &so, &se)
	return so.String(), se.String(), code
}
