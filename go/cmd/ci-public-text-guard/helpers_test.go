package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// Test helpers: the Go tool is built once and run as a subprocess; the Python original (ci/*.py of the checkout) is
// the parity reference and its tests skip with a message when python3 or the script is absent.

// syntheticTerms are made-up owner terms: a long one (substring match) and a short one (whole word only).
const syntheticTerms = "Zorbulonic\nqux\n"

var (
	buildOnce sync.Once
	builtBin  string
	buildErr  error
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

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

func goBin(t *testing.T, name string) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "ci-tool-test-")
		if err != nil {
			buildErr = err
			return
		}
		builtBin = filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-o", builtBin, "factory/go/cmd/"+name)
		cmd.Dir = filepath.Join(repoRoot(t), "go")
		if out, err := cmd.CombinedOutput(); err != nil {
			buildErr = &buildFailure{err, string(out)}
		}
	})
	if buildErr != nil {
		t.Fatalf("go build %s: %v", name, buildErr)
	}
	return builtBin
}

type buildFailure struct {
	err error
	out string
}

func (b *buildFailure) Error() string { return b.err.Error() + ": " + b.out }

// env is a hermetic process environment: no GIT_* and no factory variables, a fixed identity and clock for git.
func env(t *testing.T, extra ...string) []string {
	t.Helper()
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "FACTORY_") || strings.HasPrefix(kv, "SCRUB_") {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+t.TempDir(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=Test Author", "GIT_AUTHOR_EMAIL=author\x40example.com", "GIT_AUTHOR_DATE=2026-01-01T00:00:00Z",
		"GIT_COMMITTER_NAME=Test Committer", "GIT_COMMITTER_EMAIL=committer\x40example.com", "GIT_COMMITTER_DATE=2026-01-01T00:00:00Z")
	return append(out, extra...)
}

type result struct {
	code        int
	stdout, err string
}

func run(t *testing.T, dir string, environ []string, stdin string, argv ...string) result {
	t.Helper()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir, cmd.Env = dir, environ
	cmd.Stdin = strings.NewReader(stdin)
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", argv, err)
	}
	return result{code, o.String(), e.String()}
}

func git(t *testing.T, dir string, environ []string, args ...string) string {
	t.Helper()
	r := run(t, dir, environ, "", append([]string{"git"}, args...)...)
	if r.code != 0 {
		t.Fatalf("git %v: exit %d: %s", args, r.code, r.err)
	}
	return r.stdout
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// compare runs the Python script and the Go tool with the same arguments and requires equal exit code and stdout
// (stderr too when sameStderr: it holds the term count and the warnings).
func compare(t *testing.T, py, bin, dir string, environ []string, stdin string, sameStderr bool, args ...string) result {
	t.Helper()
	p := run(t, dir, environ, stdin, append([]string{"python3", "-I", py}, args...)...)
	g := run(t, dir, environ, stdin, append([]string{bin}, args...)...)
	if p.code != g.code || p.stdout != g.stdout || (sameStderr && p.err != g.err) {
		t.Errorf("args %v differ:\npython: exit %d\nstdout:\n%s\nstderr:\n%s\ngo: exit %d\nstdout:\n%s\nstderr:\n%s",
			args, p.code, p.stdout, p.err, g.code, g.stdout, g.err)
	}
	return g
}
