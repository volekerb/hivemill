package hm

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The test packages that build a binary leave nothing in the temp directory. They run with a private TMPDIR, so the
// leftovers of other processes in /tmp cannot fail or hide the check.
// The test cache does not see changes in those packages: run it with -count=1.
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
	for pkg, name := range map[string]string{ // each of these tests builds its tool
		"./cmd/ci-scrub-scan":           "TestScrubParityTree",
		"./cmd/ci-public-text-guard":    "TestPublicTextParityText",
		"./cmd/ci-pre-push-public-text": "TestPrePushParity",
	} {
		run([]string{pkg}, name)
	}

	left, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range left {
		t.Errorf("left behind in the temp directory: %s", e.Name())
	}
}
