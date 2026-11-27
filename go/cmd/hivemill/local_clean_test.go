package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestLocalCleanRefusals(t *testing.T) {
	for _, c := range []struct {
		actor, spec, want string
		code              int
	}{
		{"claude-task-t-0001", `{"Home": "/nonexistent"}`, "refused", 1},
		{"", `{"Home": "relative"}`, "not a factory home", 1},
		{"", `{`, "clean spec", 2},
	} {
		t.Setenv("FACTORY_AGENT", c.actor)
		var out, errs bytes.Buffer
		if code := cmdLocal([]string{"clean", c.spec}, t.TempDir(), &out, &errs); code != c.code ||
			!strings.Contains(errs.String(), c.want) || out.Len() != 0 {
			t.Errorf("%q %s: exit %d, stderr %q, stdout %q", c.actor, c.spec, code, errs.String(), out.String())
		}
	}
}
