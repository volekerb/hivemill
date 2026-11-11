package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false"}, args...)...)
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func commit(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for n, c := range files {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "c")
}

func newRepo(t *testing.T) (string, string) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	commit(t, dir, map[string]string{"README": "base\n"})
	return dir, gitIn(t, dir, "rev-parse", "HEAD")
}

func TestRunExitCodes(t *testing.T) {
	dir, base := newRepo(t)
	var out, errb bytes.Buffer
	if code := run([]string{"--repo", dir, "--base", base, "--head", "HEAD"}, &out, &errb); code != 0 ||
		!strings.Contains(out.String(), "no-new-python: ok (0 changed files, 0 new Python)") {
		t.Fatalf("clean: code %d out %q err %q", code, out.String(), errb.String())
	}

	commit(t, dir, map[string]string{"x/new.py": "print(1)\n"})
	out.Reset()
	if code := run([]string{"--repo", dir, "--base", base, "--head", "HEAD"}, &out, &errb); code != 1 ||
		!strings.Contains(out.String(), "no-new-python: new Python file x/new.py (status A); all new code is Go (G-041 SC4)") {
		t.Fatalf("added: code %d out %q", code, out.String())
	}

	errb.Reset()
	zero := strings.Repeat("0", 40)
	if code := run([]string{"--repo", dir, "--base", zero, "--head", "HEAD"}, &out, &errb); code != 2 ||
		!strings.Contains(errb.String(), "git diff") {
		t.Fatalf("bad base: code %d err %q", code, errb.String())
	}
	if code := run([]string{"--repo", dir}, &out, &errb); code != 2 {
		t.Fatalf("missing --base: code %d", code)
	}
}

// TestBinaryExitCode runs the compiled binary as a subprocess, as CI does.
func TestBinaryExitCode(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "no-new-python")
	if out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	dir, base := newRepo(t)
	commit(t, dir, map[string]string{"tool": "#!/usr/bin/env -S python3 -u\nprint(1)\n"})
	cmd := exec.Command(bin, "--repo", dir, "--base", base, "--head", "HEAD")
	out, err := cmd.Output()
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 1 || !strings.Contains(string(out), "new Python file tool (status A)") {
		t.Fatalf("want exit 1 naming tool, got err %v out %q", err, out)
	}
}
