package hm

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The test packages that build a binary leave nothing in the temp directory. They run with a private TMPDIR, so the
// leftovers of other processes in /tmp cannot fail or hide the check.
func TestNoTmpLeftoversFromBuildHelpers(t *testing.T) {
	tmp, err := os.MkdirTemp("", "nl-") // short: the nested tests make their own paths below it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	run := func(args []string, mustPass ...string) {
		t.Helper()
		cmd := exec.Command("go", append([]string{"test", "-count=1", "-v"}, args...)...)
		cmd.Dir = "../.." // the module root
		cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go test %v: %v\n%s", args, err, out)
		}
		for _, name := range mustPass { // a renamed test must not turn the check into a no-op
			if !strings.Contains(string(out), "--- PASS: "+name+" ") {
				t.Errorf("go test %v: %s did not run and pass", args, name)
			}
		}
	}
	// internal/guard is slow as a whole: one test for each of its two build helpers.
	run([]string{"./internal/guard", "-run", "^(TestCursorAdapterOutcomes|TestDockerPruneEntryBlocked)$"},
		"TestCursorAdapterOutcomes", "TestDockerPruneEntryBlocked")
	run([]string{"./cmd/ci-scrub-scan", "./cmd/ci-public-text-guard", "./cmd/ci-pre-push-public-text"})

	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		t.Errorf("left behind in the temp directory: %s", e.Name())
	}
}
