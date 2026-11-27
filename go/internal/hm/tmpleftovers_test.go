package hm

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// internal/guard and the three cmd/ci-* test packages leave nothing of their builds in the temp directory. They run with a private TMPDIR, so the
// leftovers of other processes in /tmp cannot fail or hide the check.
// The test cache does not see changes in those packages: run it with -count=1.
func TestNoTmpLeftoversFromBuildHelpers(t *testing.T) {
	tmp, err := os.MkdirTemp("", "nl-") // short: the nested tests make their own paths below it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })

	run := func(args []string, mustRun ...string) {
		t.Helper()
		cmd := exec.Command("go", append([]string{"test", "-count=1", "-v"}, args...)...)
		cmd.Dir = "../.." // the module root
		cmd.Env = append(os.Environ(), "TMPDIR="+tmp)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go test %v: %v\n%s", args, err, out)
		}
		for _, name := range mustRun { // a renamed test must not turn the check into a no-op
			switch {
			case strings.Contains(string(out), "--- PASS: "+name+" "):
			case strings.Contains(string(out), "--- SKIP: "+name+" "): // no python3 or no reference script
				t.Logf("go test %v: %s skipped, its build is not checked", args, name)
			default:
				t.Errorf("go test %v: %s did not run", args, name)
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
		if strings.HasPrefix(e.Name(), "xcrun_db") { // the cache of the macOS git and python3 shims
			continue
		}
		t.Errorf("left behind in the temp directory: %s", e.Name())
	}
}
