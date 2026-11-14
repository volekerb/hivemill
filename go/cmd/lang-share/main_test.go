package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunExitCodes(t *testing.T) {
	cases := []struct {
		args []string
		code int
		errs string
	}{
		{[]string{"--repo", "/nonexistent-dir-xyz"}, 2, "git"},
		{[]string{"--bogus"}, 2, ""},
		{[]string{"extra"}, 2, "unexpected"},
		{[]string{"--repo", "../..", "--ref", "$(touch /tmp/lang-share-x)"}, 2, "git"},
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
}

func TestRunOK(t *testing.T) {
	var out, errb bytes.Buffer
	if got := run([]string{"--repo", "../..", "--json"}, &out, &errb); got != 0 {
		t.Fatalf("exit %d: %s", got, errb.String())
	}
	if !strings.HasPrefix(out.String(), `{"go":`) {
		t.Fatalf("bad json %q", out.String())
	}
}
