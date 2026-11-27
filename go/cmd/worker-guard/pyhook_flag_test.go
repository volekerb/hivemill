package main

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-2112: `--py-hook <path>` is still accepted (the shim passes it) and only locates the checkout; nothing runs the
// file or python3. Each payload gives the same exit with and without the flag, whatever the path names (a missing
// file, a hook that would allow everything), the flag and its value never reach role parsing, and a python3 first on
// PATH that writes a marker file is never started.
func TestPyHookFlagIgnored(t *testing.T) {
	fake := t.TempDir()
	marker := filepath.Join(t.TempDir(), "python3-ran")
	if err := os.WriteFile(filepath.Join(fake, "python3"), []byte("#!/bin/sh\necho \"$@\" >> '"+marker+"'\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// a hook that would allow every payload if anything ran it; its checkout root is a temp dir of its own
	allowAll := filepath.Join(t.TempDir(), "roles", "hooks", "worker-guard.py")
	if err := os.MkdirAll(filepath.Dir(allowAll), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(allowAll, []byte("import sys\nsys.exit(0)\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "roles", "hooks", "no-such-hook.py")

	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "ok.tar"), oneFileTar(t, "f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scratch, "bad.tar"), bytes.Repeat([]byte("not an archive "), 64), 0o644); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(scratch, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	env := withTerms(t, "PATH="+fake+string(os.PathListSeparator)+os.Getenv("PATH"), "WORKER_GUARD_SCRATCH="+scratch)
	bash := func(cmd string) []byte {
		return []byte(`{"tool_name":"Bash","tool_input":{"command":` + jsonString(cmd) + `},"cwd":` + jsonString(scratch) + `}`)
	}
	for _, c := range []struct {
		name    string
		payload []byte
		role    string
		code    int // -1: only the same exit with and without the flag is required
	}{
		{"allowed command", bash("ls -la"), "coder", 0},
		{"blocked command", bash("sudo ls"), "coder", 2},
		{"broad read", bash("factory goal show G-005"), "coder", 2},
		// the archive extractions that used to go to the Python hook for the member list: a missing archive as the first
		// command extracts nothing (allowed, as the Python hook does), for a no-push role a corrupt one
		// cannot be listed (blocked)
		{"tar of a missing archive", bash("tar -xf " + filepath.Join(scratch, "missing.tar") + " -C " + out), "coder", 0},
		{"tar of a missing archive as qa", bash("tar -xf " + filepath.Join(scratch, "missing.tar") + " -C " + out), "qa", 0},
		{"unzip of a missing archive", bash("unzip " + filepath.Join(scratch, "missing.zip") + " -d " + out), "coder", 0},
		{"tar of a corrupt archive as qa", bash("tar -xf " + filepath.Join(scratch, "bad.tar") + " -C " + out), "qa", 2},
		{"unzip of a corrupt archive as qa", bash("unzip " + filepath.Join(scratch, "bad.tar") + " -d " + out), "qa", 2},
		{"tar of a clean archive", bash("tar -xf " + filepath.Join(scratch, "ok.tar") + " -C " + out), "coder", -1},
		{"read tool", []byte(`{"tool_name":"Read","tool_input":{"file_path":"/etc/hostname"}}`), "coder", 0},
	} {
		base, baseErr := call(t, c.payload, env, "--role", c.role)
		if c.code >= 0 && base != c.code {
			t.Errorf("%s without --py-hook: exit %d %q, want %d", c.name, base, baseErr, c.code)
		}
		for _, hook := range []string{missing, allowAll} {
			for _, args := range [][]string{
				{"--py-hook", hook, "--role", c.role},
				{"--role", c.role, "--py-hook", hook},
			} {
				code, stderr := call(t, c.payload, env, args...)
				if code != base {
					t.Errorf("%s %v: exit %d %q, without the flag %d %q", c.name, args, code, stderr, base, baseErr)
				}
				if strings.Contains(stderr, "Python hook") {
					t.Errorf("%s %v: stderr names the Python hook: %q", c.name, args, stderr)
				}
			}
		}
	}
	// the flag's value is taken out before role parsing: a value spelled like an argument is not one
	broad := bash("factory goal show G-005") // HQ mode allows it, a coder may not
	for _, args := range [][]string{
		{"--py-hook", "--hq", "--role", "coder"},
		{"--role", "coder", "--py-hook", "--hq"},
		{"--py-hook", "--role", "--role", "coder"},
	} {
		if code, stderr := call(t, broad, env, args...); code != 2 || !strings.Contains(stderr, "broad factory reads") {
			t.Errorf("%v: exit %d %q, want the coder's broad-read block (the flag value parsed as an argument?)", args, code, stderr)
		}
	}
	if code, stderr := call(t, broad, env, "--hq", "--py-hook", allowAll); code != 0 {
		t.Errorf("--hq with --py-hook: exit %d %q, want 0", code, stderr)
	}
	if b, err := os.ReadFile(marker); err == nil {
		t.Errorf("python3 was started: %q", b)
	}
}

// oneFileTar is a tar archive holding one small regular file called name.
func oneFileTar(t *testing.T, name string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	if err := w.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
