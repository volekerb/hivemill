package main

import (
	"os"
	"testing"
)

// The command is a thin wrapper: logic and its tests live in internal/lanecheck. This proves the wiring and
// the fail-closed exit code.
func TestRunUsageIsExit2(t *testing.T) {
	old := os.Args
	t.Cleanup(func() { os.Args = old })
	for _, args := range [][]string{{"lane-check"}, {"lane-check", "nosuchcommand"}, {"lane-check", "pair", "onlyone"}} {
		os.Args = args
		if code := run(); code != 2 {
			t.Fatalf("%v: exit %d, want 2", args, code)
		}
	}
}
