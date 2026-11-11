package nonewpy

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestIsPythonPath(t *testing.T) {
	for _, tc := range []struct {
		p    string
		want bool
	}{
		{"a.py", true}, {"x/y.pyi", true}, {"w.pyw", true}, {"c.pyx", true}, {"UP.PY", true}, {"Mixed.Py", true},
		{"a.go", false}, {"notes.txt", false}, {"py", false}, {"a.pyc", false}, {"dir.py/readme", false},
	} {
		t.Run(tc.p, func(t *testing.T) {
			if got := IsPythonPath(tc.p); got != tc.want {
				t.Fatalf("IsPythonPath(%q) = %v, want %v", tc.p, got, tc.want)
			}
		})
	}
}

func TestHasPythonShebang(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     bool
	}{
		{"env python3", "#!/usr/bin/env python3\nx", true},
		{"env -S", "#!/usr/bin/env -S python3 -u\n", true},
		{"direct", "#!/usr/bin/python\n", true},
		{"versioned", "#!/usr/bin/python3.12\n", true},
		{"sh", "#!/bin/sh\npython3 x\n", false},
		{"no shebang", "import os\n", false},
		{"not first line", "\n#!/usr/bin/python\n", false},
		{"pythonic arg", "#!/bin/sh -x pythonic\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasPythonShebang([]byte(tc.in)); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseNameStatus(t *testing.T) {
	got, err := ParseNameStatus([]byte("A\x00a.py\x00R087\x00old.txt\x00new.py\x00C100\x00s\x00d\x00D\x00gone\x00"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Change{{"A", "", "a.py"}, {"R", "old.txt", "new.py"}, {"C", "s", "d"}, {"D", "", "gone"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v want %+v", got, want)
	}
	if _, err := ParseNameStatus([]byte("R090\x00only-one\x00")); err == nil {
		t.Fatal("truncated rename must error")
	}
}

func TestAllowListEmpty(t *testing.T) {
	if len(allowList) != 0 {
		t.Fatalf("allowList must stay empty (needs a director decision), has %d entries", len(allowList))
	}
}

// --- real git repositories -------------------------------------------------

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.invalid",
		"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

const body = "def f():\n    return 1\n# padding so similarity detection is stable\nx = 1\ny = 2\n"

// scenario builds a repo with the base files, applies mutate, commits, and returns violations.
func scenario(t *testing.T, base map[string]string, mutate func(dir string)) ([]Violation, int) {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	for n, c := range base {
		write(t, dir, n, c)
	}
	write(t, dir, ".keep", "k")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "base")
	baseSHA := gitIn(t, dir, "rev-parse", "HEAD")
	mutate(dir)
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "head")
	bad, n, err := Run(dir, baseSHA, "HEAD")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return bad, n
}

func TestRunScenarios(t *testing.T) {
	two := map[string]string{"a.py": body, "b.py": body + "z = 3\n"}
	for _, tc := range []struct {
		name   string
		base   map[string]string
		mutate func(dir string)
		want   []Violation
	}{
		{"added .py blocked", nil, func(d string) { write(t, d, "x/new.py", body) }, []Violation{{"x/new.py", "A"}}},
		{"modify and delete allowed", two, func(d string) {
			write(t, d, "a.py", body+"more = 1\n")
			os.Remove(filepath.Join(d, "b.py"))
		}, nil},
		{"shebang env blocked", nil, func(d string) { write(t, d, "tool", "#!/usr/bin/env python3\nprint(1)\n") }, []Violation{{"tool", "A"}}},
		{"shebang env -S blocked", nil, func(d string) { write(t, d, "tool", "#!/usr/bin/env -S python3 -u\nprint(1)\n") }, []Violation{{"tool", "A"}}},
		{"modified file gains shebang blocked", map[string]string{"tool": "#!/bin/sh\necho hi\n"}, func(d string) {
			write(t, d, "tool", "#!/usr/bin/env python3\nprint(1)\n")
		}, []Violation{{"tool", "M"}}},
		{"modified python script stays allowed", map[string]string{"tool": "#!/usr/bin/env python3\nprint(1)\n"}, func(d string) {
			write(t, d, "tool", "#!/usr/bin/env python3\nprint(2)\n")
		}, nil},
		{"shell script added allowed", nil, func(d string) { write(t, d, "run.sh", "#!/bin/sh\necho hi\n") }, nil},
		{"rename py to py allowed", map[string]string{"a.py": body}, func(d string) {
			os.MkdirAll(filepath.Join(d, "lib"), 0o755)
			gitIn(t, d, "mv", "a.py", "lib/a.py")
		}, nil},
		{"rename txt to py blocked", map[string]string{"notes.txt": body}, func(d string) {
			gitIn(t, d, "mv", "notes.txt", "notes.py")
		}, []Violation{{"notes.py", "R"}}},
		{"copy py blocked", map[string]string{"a.py": body}, func(d string) { write(t, d, "c.py", body) }, []Violation{{"c.py", "C"}}},
		{"rename keeps shebang script allowed", map[string]string{"tool": "#!/usr/bin/env python3\n" + body}, func(d string) {
			gitIn(t, d, "mv", "tool", "tool2")
		}, nil},
		{"copy of shebang script blocked", map[string]string{"tool": "#!/usr/bin/env python3\n" + body}, func(d string) {
			write(t, d, "tool2", "#!/usr/bin/env python3\n"+body)
		}, []Violation{{"tool2", "C"}}},
		{"uppercase .PY blocked", nil, func(d string) { write(t, d, "X.PY", body) }, []Violation{{"X.PY", "A"}}},
		{".pyi blocked", nil, func(d string) { write(t, d, "s.pyi", body) }, []Violation{{"s.pyi", "A"}}},
		{".pyw blocked", nil, func(d string) { write(t, d, "s.pyw", body) }, []Violation{{"s.pyw", "A"}}},
		{".pyx blocked", nil, func(d string) { write(t, d, "s.pyx", body) }, []Violation{{"s.pyx", "A"}}},
		{"low-similarity rename fails closed", map[string]string{"a.py": body}, func(d string) {
			os.Remove(filepath.Join(d, "a.py"))
			write(t, d, "b.py", "completely different content\nnothing shared at all\n")
		}, []Violation{{"b.py", "A"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := scenario(t, tc.base, tc.mutate)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRunRejectsOptionLikeRevision(t *testing.T) {
	if _, _, err := Run(t.TempDir(), "--output=x", "HEAD"); err == nil || !strings.Contains(err.Error(), "starts with '-'") {
		t.Fatalf("want option-like revision rejected, got %v", err)
	}
}

func TestRunBadBaseFailsClosed(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a", "x")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "c")
	_, _, err := Run(dir, "0000000000000000000000000000000000000000", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "git diff") {
		t.Fatalf("want a git error, got %v", err)
	}
}

func TestReadErrorFailsClosed(t *testing.T) {
	boom := func(bool, string) ([]byte, bool, error) { return nil, false, os.ErrPermission }
	if _, err := Evaluate([]Change{{Status: "A", Path: "tool"}}, boom); err == nil {
		t.Fatal("want a read error to fail the check, got nil")
	}
}

func TestReadFirstBytesHeadMissingIsError(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "a", "x")
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "c")
	if _, _, err := readFirstBytes(dir, "HEAD", "nope", true); err == nil {
		t.Fatal("want error for a path missing at head")
	}
	if _, ok, err := readFirstBytes(dir, "HEAD", "nope", false); err != nil || ok {
		t.Fatalf("missing at merge base: ok=%v err=%v, want absent without error", ok, err)
	}
	if _, _, err := readFirstBytes(dir, "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef", "a", true); err == nil {
		t.Fatal("want error for an unreadable revision, not absent")
	}
}

func TestReadFirstBytesBounded(t *testing.T) {
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "big", "#!/usr/bin/env python3\n"+strings.Repeat("x", 100000))
	gitIn(t, dir, "add", "-A")
	gitIn(t, dir, "commit", "-q", "-m", "c")
	b, ok, err := readFirstBytes(dir, "HEAD", "big", true)
	if err != nil || !ok || len(b) != maxHead || !HasPythonShebang(b) {
		t.Fatalf("len=%d ok=%v err=%v, want %d bytes with shebang", len(b), ok, err, maxHead)
	}
}
