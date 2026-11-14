package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "x")
	cases := []struct {
		args []string
		code int
		errs string
	}{
		{[]string{"--repo", "/nonexistent-dir-xyz"}, 2, "git"},
		{[]string{"--bogus"}, 2, ""},
		{[]string{"extra"}, 2, "unexpected"},
		{[]string{"--repo", "../..", "--ref", "$(touch " + marker + ")"}, 2, "git"},
	}
	for _, c := range cases {
		var out, errb bytes.Buffer
		if got := run(c.args, &out, &errb); got != c.code {
			t.Errorf("%v: exit %d want %d", c.args, got, c.code)
		}
		if !strings.Contains(errb.String(), c.errs) {
			t.Errorf("%v: stderr %q lacks %q", c.args, errb.String(), c.errs)
		}
		if out.Len() != 0 {
			t.Errorf("%v: stdout not empty", c.args)
		}
	}
	if _, err := os.Stat(marker); err == nil {
		t.Errorf("injection created %s", marker)
	}
}

func TestRunOK(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, a := range [][]string{{"init", "-q"}, {"add", "-A"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", dir}, a...)...)
		cmd.Env = append(os.Environ()[:0:0], cleanEnv()...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	var out, errb bytes.Buffer
	if got := run([]string{"--repo", dir, "--json"}, &out, &errb); got != 0 {
		t.Fatalf("exit %d: %s", got, errb.String())
	}
	if !strings.HasPrefix(out.String(), `{"go":1,`) {
		t.Fatalf("bad json %q", out.String())
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
