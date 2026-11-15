package main

import (
	"bytes"
	"strings"
	"testing"
)

var base = []string{"--owner-user", "a", "--owner-home", "/Users/a", "--factory-home", "/Users/a/factory"}

func TestPrintSubcommands(t *testing.T) {
	for sub, want := range map[string]string{"print-setup": "step sudoers:", "print-uninstall": "undo step sudoers:"} {
		var so, se bytes.Buffer
		if code := run(append([]string{sub}, base...), &so, &se); code != 0 || !strings.Contains(so.String(), want) {
			t.Errorf("%s: exit %d, stderr %q", sub, code, se.String())
		}
	}
}

func TestRefusals(t *testing.T) {
	for _, args := range [][]string{nil, {"run"}, {"print-setup"}, {"print-setup", "--owner-user", "a'b", "--owner-home", "/h", "--factory-home", "/f"}} {
		var so, se bytes.Buffer
		if code := run(args, &so, &se); code != 2 || so.Len() != 0 {
			t.Errorf("%v: exit %d, stdout %q", args, code, so.String())
		}
	}
}
