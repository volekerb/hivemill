package hm

import (
	"strings"
	"testing"
)

const goodBody = "## Summary\nAdds a title.\n\n## Why\nT-1: the page had none.\n\n## Changes\n- title tag\n\n## Test evidence\n" +
	"`python3 -m unittest test_x` -> Ran 3 tests, OK\n\n## Risk / rollback\nNone; revert.\n\n" +
	"<details><summary>Acceptance walk</summary>\n\n## Acceptance walk\n1. title - grep -> 1\n</details>\n"

func TestBodyProblems(t *testing.T) {
	if p := BodyProblems(goodBody, false); len(p) != 0 {
		t.Fatalf("good body: %v", p)
	}
	cases := map[string]string{
		strings.Replace(goodBody, "## Why", "## Reason", 1):                                           "missing section",
		strings.Replace(goodBody, "`python3 -m unittest test_x` -> Ran 3 tests, OK", "tests pass", 1): "no command",
		strings.Replace(goodBody, " -> Ran 3 tests, OK", "", 1):                                       "no output line",
		strings.Replace(goodBody, "## Acceptance walk\n1. title - grep -> 1\n", "", 1):                "Acceptance walk",
		strings.Replace(goodBody, "## Changes\n", "## Changes\n```\n", 1):                             "never closed",
		strings.Replace(goodBody, "Adds a title.", strings.Repeat("word ", 320), 1):                   "words outside",
	}
	for body, want := range cases {
		if p := BodyProblems(body, false); !strings.Contains(strings.Join(p, ";"), want) {
			t.Errorf("want %q, got %v", want, p)
		}
	}
	if p := BodyProblems(goodBody, true); !strings.Contains(strings.Join(p, ";"), "Tests removed") {
		t.Errorf("test drop: %v", p)
	}
	if p := BodyProblems(strings.Replace(goodBody, "## Risk", "Tests removed: old flaky one\n\n## Risk", 1), true); len(p) != 0 {
		t.Errorf("test drop explained: %v", p)
	}
	byPath := strings.Replace(goodBody, "`python3 -m unittest test_x` -> Ran 3 tests, OK",
		"`cd go && GOTOOLCHAIN=local ~/.local/go/bin/go test -count=1 ./internal/hm` -> ok", 1)
	if p := BodyProblems(byPath, false); len(p) != 0 {
		t.Errorf("runner by path: %v", p)
	}
}

// TestIsCommandRunnerByPath: a runner given by path counts by its base name (T-1979).
func TestIsCommandRunnerByPath(t *testing.T) {
	for cmd, want := range map[string]bool{
		"cd go && GOTOOLCHAIN=local ~/.local/go/bin/go test ./...": true,
		"/usr/bin/python3 -m unittest x":                           true,
		"~/notes/readme.md here":                                   false,
		"go test ./...":                                            true,
	} {
		if got := isCommand(cmd); got != want {
			t.Errorf("isCommand(%q) = %v, want %v", cmd, got, want)
		}
	}
}
