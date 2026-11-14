// Package testcount is the Go port of ci/test_count_gate.py: fail a PR whose test count drops below its base.
//
//	ci-test-count-gate count <tree> [--python-only]
//	    print the number of unittest tests discovered in <tree>/tests (exit 2 on load errors), then, unless
//	    --python-only, a second line "go-tests: N" with the Go tests (go test -list) of <tree>/go
//	ci-test-count-gate check --base N --pr M --body-file F [--base-go N --pr-go M]
//	    exit 0 if M >= N or the PR body has a "tests-removed: <reason>" line, else 1; with --base-go and --pr-go
//	    the Go count is held to the same rule
//
// The Python count must equal ci/test_count_gate.py, so it is made by the same unittest discovery (python3, an
// embedded copy of count_tests): importing the test modules is the only way to get the same number. The PR body is
// untrusted text: it is only ever read from a file.
package testcount

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// LoadError means test discovery hit import errors (each would count as one test) or a tool could not run.
type LoadError struct{ Msg string }

func (e *LoadError) Error() string { return e.Msg }

// pyCountSnippet is count_tests of ci/test_count_gate.py; argv[1] is the tree.
const pyCountSnippet = `import os, sys, unittest

def walk(suite):
    for t in suite:
        if isinstance(t, unittest.TestSuite):
            yield from walk(t)
        else:
            yield t

tests_dir = os.path.join(os.path.abspath(sys.argv[1]), "tests")
os.chdir(os.path.dirname(tests_dir))
sys.path.insert(0, os.path.dirname(tests_dir))
sys.path.insert(0, tests_dir)
loader = unittest.TestLoader()
suite = loader.discover(start_dir=tests_dir, top_level_dir=tests_dir)
bad = [t.id() for t in walk(suite) if isinstance(t, unittest.loader._FailedTest)]
if loader.errors or bad:
    sys.stderr.write("test discovery failed in %s: %s\n" % (tests_dir, ", ".join(bad) or "load errors"))
    sys.exit(3)
sys.stdout.write("%d\n" % suite.countTestCases())
`

// CountPython is the number of tests unittest discovers in <tree>/tests (0 if there is no such directory).
func CountPython(tree string) (int, error) {
	abs, err := filepath.Abs(tree)
	if err != nil {
		return 0, &LoadError{fmt.Sprintf("bad tree %q: %v", tree, err)}
	}
	if st, err := os.Stat(filepath.Join(abs, "tests")); err != nil || !st.IsDir() {
		return 0, nil
	}
	cmd := exec.Command("python3", "-c", pyCountSnippet, abs)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 3 {
			return 0, &LoadError{strings.TrimSpace(stderr.String())}
		}
		return 0, &LoadError{fmt.Sprintf("python3 test discovery in %s failed: %v %s; fix the tests or install python3", abs, err, strings.TrimSpace(stderr.String()))}
	}
	// test modules may print while importing: the count is the last line
	lines := strings.Fields(stdout.String())
	if len(lines) == 0 {
		return 0, &LoadError{"python3 test discovery printed no count; rerun it by hand in " + abs}
	}
	n, err := strconv.Atoi(lines[len(lines)-1])
	if err != nil {
		return 0, &LoadError{fmt.Sprintf("python3 test discovery printed %q, not a count", lines[len(lines)-1])}
	}
	return n, nil
}

var goTestName = regexp.MustCompile(`^(Test|Fuzz)\S*$`)

// CountGo is the number of Go tests and fuzz targets `go test -list` finds under <tree>/go (0 if there is no go.mod).
func CountGo(tree string) (int, error) {
	dir := filepath.Join(tree, "go")
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		return 0, nil
	}
	cmd := exec.Command("go", "test", "-list", ".", "./...")
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return 0, &LoadError{fmt.Sprintf("go test -list failed in %s: %v %s; fix the build or install go", dir, err, strings.TrimSpace(stderr.String()+" "+tail(stdout.String())))}
	}
	n := 0
	sc := bufio.NewScanner(&stdout)
	for sc.Scan() {
		if goTestName.MatchString(strings.TrimSpace(sc.Text())) {
			n++
		}
	}
	return n, nil
}

func tail(s string) string {
	if len(s) > 400 {
		return s[len(s)-400:]
	}
	return s
}

var bypass = regexp.MustCompile(`(?im)^[ \t]*tests-removed:[ \t]*(\S.*)$`)

// Decide returns (ok, message) for a base/PR test count and the PR body.
func Decide(base, pr int, body string) (bool, string) { return decide("", "test", base, pr, body) }

// DecideGo is Decide for the Go test count: the message keys read go-base, go-pr and "go test count".
func DecideGo(base, pr int, body string) (bool, string) {
	return decide("go-", "go test", base, pr, body)
}

func decide(key, what string, base, pr int, body string) (bool, string) {
	head := fmt.Sprintf("%sbase=%d %spr=%d", key, base, key, pr)
	if pr >= base {
		return true, head
	}
	m := bypass.FindStringSubmatch(body)
	drop := fmt.Sprintf("%s count dropped from %d to %d", what, base, pr)
	if m != nil {
		return true, fmt.Sprintf("%s\n%s; allowed by tests-removed: %s", head, drop, strings.TrimSpace(m[1]))
	}
	return false, fmt.Sprintf("%s\n%s; add 'tests-removed: <reason>' to the PR body if intended", head, drop)
}
