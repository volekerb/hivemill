package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestHelpOnEverySubcommand(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"restore", "--help"}, {"run", "-h"}, {"config", "max_concurrent", "--help"}, {"eval", "--help"}} {
		var out, errb bytes.Buffer
		if code := Main(args, strings.NewReader(""), &out, &errb); code != 0 || !strings.HasPrefix(out.String(), "usage: hivemill") {
			t.Errorf("%v: exit %d, out %q, err %q", args, code, out.String(), errb.String())
		}
	}
}
