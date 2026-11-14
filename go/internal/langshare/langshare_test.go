package langshare

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func mkrepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, body := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, a...)...)
		cmd.Env = cleanEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	return dir
}

var base = map[string]string{
	"a.go":            "package a\n\nfunc A() {}\n\nvar x = 1\n",
	"a_test.go":       "package a\n\n// t\n",
	"b.py":            "a = 1\n\nb = 2\nc = 3\nd = 4\n",
	"tests/test_b.py": "1\n2\n3\n4\n5\n",
}

func TestCounts(t *testing.T) {
	c, err := Count(mkrepo(t, base), "")
	if err != nil {
		t.Fatal(err)
	}
	want := "go 5 (2 tests)\npython 9 (5 tests)\ngo share 35.7%\n"
	if c.Text() != want {
		t.Fatalf("got %q want %q", c.Text(), want)
	}
	wantJSON := `{"go":5,"go_tests":2,"python":9,"python_tests":5,"go_share":35.7}` + "\n"
	if c.JSON() != wantJSON {
		t.Fatalf("got %q want %q", c.JSON(), wantJSON)
	}
}

func TestShebang(t *testing.T) {
	files := map[string]string{
		"tool":  "#!/usr/bin/env python3\nx = 1\ny = 2\n",
		"shell": "#!/bin/sh\necho hi\n",
		"plain": "just text\n",
	}
	c, err := Count(mkrepo(t, files), "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if c.Python != 3 || c.Go != 0 {
		t.Fatalf("got %+v want python 3", c)
	}
}

func TestExclusions(t *testing.T) {
	files := map[string]string{}
	for k, v := range base {
		files[k] = v
	}
	files["go/x/testdata/f.go"] = "package f\nvar a = 1\n"
	files["testdata/g.py"] = "x = 1\n"
	files["skilleval/tasks/t1/h.py"] = "x = 1\n"
	files["go/vendor/m/v.go"] = "package v\n"
	c, err := Count(mkrepo(t, files), "")
	if err != nil {
		t.Fatal(err)
	}
	if c.Go != 5 || c.GoTests != 2 || c.Python != 9 || c.PythonTests != 5 {
		t.Fatalf("exclusions leaked: %+v", c)
	}
}

func TestBadRefAndRepo(t *testing.T) {
	dir := mkrepo(t, base)
	pwned := filepath.Join(dir, "pwned")
	for _, ref := range []string{"nope", "--all", "$(touch " + pwned + ")"} {
		if _, err := Count(dir, ref); err == nil {
			t.Fatalf("ref %q accepted", ref)
		}
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("ref was executed")
	}
	if _, err := Count("/nonexistent-dir-xyz", ""); err == nil || !strings.Contains(err.Error(), "git") {
		t.Fatalf("want git error, got %v", err)
	}
}

func FuzzNonBlank(f *testing.F) {
	for _, s := range []string{"", "\n", "a\n\nb", " \t\n x ", "\r\n\r\n"} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		n := nonBlank(data)
		if n < 0 || n > bytes.Count(data, []byte("\n"))+1 {
			t.Fatalf("nonBlank=%d out of range for %q", n, data)
		}
		_ = isPythonShebang(data)
	})
}

func FuzzParseTreeEntry(f *testing.F) {
	f.Add([]byte("100644 blob abc123\ta.go"))
	f.Add([]byte("garbage"))
	f.Fuzz(func(t *testing.T, ent []byte) {
		mode, typ, sha, p, ok := parseTreeEntry(ent)
		if ok && (mode == "" || typ == "" || sha == "") {
			t.Fatalf("ok with empty field: %q", ent)
		}
		_ = IsExcluded(p)
		_ = classify(p)
	})
}

func FuzzParseBatchHeader(f *testing.F) {
	f.Add("abc blob 12\n")
	f.Add("abc blob -1\n")
	f.Add("abc blob 99999999999999999999\n")
	f.Fuzz(func(t *testing.T, hdr string) {
		size, err := parseBatchHeader(hdr)
		if err == nil && (size < 0 || size > maxBlob) {
			t.Fatalf("accepted size %d for %q", size, hdr)
		}
	})
}

func TestParseBatchHeaderRejects(t *testing.T) {
	for _, h := range []string{"a blob -1\n", "a blob x\n", "a blob\n", "a blob 99999999999\n", ""} {
		if _, err := parseBatchHeader(h); err == nil {
			t.Errorf("%q accepted", h)
		}
	}
	if n, err := parseBatchHeader("a blob 7\n"); err != nil || n != 7 {
		t.Errorf("good header: %d %v", n, err)
	}
}

func TestGitTimeout(t *testing.T) {
	old := gitTimeout
	gitTimeout = time.Nanosecond
	defer func() { gitTimeout = old }()
	if _, err := Count(mkrepo(t, base), ""); err == nil {
		t.Fatal("expected timeout error")
	}
}

func cleanEnv() []string {
	var env []string
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "GIT_") {
			env = append(env, e)
		}
	}
	return env
}
