package cursor

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"factory/go/internal/citools/shard"
	"factory/go/internal/citools/tier"
)

// Port of tests/test_ci_cursor_guard.py (7 cases; see ../ported_cursor.txt).
// The workflow checks read the real .github/workflows/tests.yml (the merge-gate tree carries it). The two cases that
// cover Python CI helpers with no Go port (ci/guard_impl_unittest.py, tests/guard_impl.py hook_run) run them through
// python3 and skip visibly without it. The tier case uses the Go ci_tier (internal/citools/tier).

const block = `{"tool_name": "Bash", "tool_input": {"command": "pkill x"}}`

func workflowText(t *testing.T) string {
	t.Helper()
	return readFile(t, filepath.Join(repoRoot(t), ".github", "workflows", "tests.yml"))
}

// workflowJobBlock is workflow_job_block: the lines of `  <job>:` up to the next two-space-indented key.
func workflowJobBlock(text, job string) (string, bool) {
	start := regexp.MustCompile(`(?m)^  ` + regexp.QuoteMeta(job) + `:\n`).FindStringIndex(text)
	if start == nil {
		return "", false
	}
	rest := text[start[1]:]
	if end := regexp.MustCompile(`(?m)^  [a-z]`).FindStringIndex(rest); end != nil {
		return rest[:end[0]], true
	}
	return rest, true
}

// continueOnError is Python's (?m)^\s+continue-on-error:\s*true. Python's str \s is Unicode (NBSP, \v, U+001C..U+001F,
// U+0085), Go's is not, so the class is spelled out (G8).
var continueOnError = regexp.MustCompile(`(?m)^[\p{Z}\s\v\x1c-\x1f\x85]+continue-on-error:[\p{Z}\s\v\x1c-\x1f\x85]*true`)

// TestCiCursorGuardUnicodeSpaceInContinueOnError: the Python results for the same lines (quoted from
// re.search(r"(?m)^\s+continue-on-error:\s*true", s)): NBSP, \v, U+001C and U+0085 match; U+0663 (an Arabic-Indic
// digit, which is not whitespace) does not.
func TestCiCursorGuardUnicodeSpaceInContinueOnError(t *testing.T) {
	for line, want := range map[string]bool{
		" continue-on-error: true":      true,
		"\vcontinue-on-error:\vtrue":    true,
		"    continue-on-error:true":    true,
		"\x1ccontinue-on-error: true":   true,
		"\u0085continue-on-error: true": true,
		"٣continue-on-error: true":      false,
	} {
		if got := continueOnError.MatchString(line); got != want {
			t.Errorf("%q: match = %v, Python says %v", line, got, want)
		}
	}
}

var guardJobs = []string{"guard-unittest-shard", "guard-unittest", "guard-diff", "guard-self-compare", "guard-evaluator-home-loosen"}

// assertWorkflowGuardDiff is assert_workflow_guard_diff: GUARD_IMPL=diff runs in guard-unittest-shard and none of
// the guard jobs may use continue-on-error: true.
func assertWorkflowGuardDiff(text string) error {
	unit, found := workflowJobBlock(text, "guard-unittest-shard")
	if !found {
		return errNew("tests.yml has no guard-unittest-shard job")
	}
	if !strings.Contains(unit, "GUARD_IMPL=diff") {
		return errNew("tests.yml must set GUARD_IMPL=diff in the guard-unittest-shard job")
	}
	for _, job := range guardJobs {
		b, found := workflowJobBlock(text, job)
		if !found {
			return errNew("tests.yml has no " + job + " job")
		}
		if continueOnError.MatchString(b) {
			return errNew(job + " job must not use continue-on-error: true")
		}
	}
	return nil
}

type strErr string

func (e strErr) Error() string { return string(e) }
func errNew(s string) error    { return strErr(s) }

func pyCode(t *testing.T, env map[string]string, code string, args ...string) adapterRun {
	t.Helper()
	py := needPython(t)
	return runCmd(t, py, "", env, append([]string{"-c", code}, args...)...)
}

func TestCiCursorGuardGuardImplUnittestMatchesDocsGuardDiffModuleSet(t *testing.T) {
	root := repoRoot(t)
	r := pyCode(t, nil, "import sys, json\nsys.path.insert(0, sys.argv[1] + '/tests')\nsys.path.insert(0, sys.argv[1] + '/ci')\n"+
		"import guard_impl_unittest\nprint(json.dumps(guard_impl_unittest.diff_module_names()))\n", root)
	eq(t, "exit", r.Code, 0)
	eq(t, "modules", strings.TrimSpace(r.Stdout),
		`["test_worker_guard.py", "test_priv_guard.py", "test_guard_diff.py", "test_worker_guard_public_text.py"]`)
}

func TestCiCursorGuardWorkflowHasGuardDiffJobWithoutContinueOnError(t *testing.T) {
	if err := assertWorkflowGuardDiff(workflowText(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCiCursorGuardRemovingGuardImplDiffFromWorkflowFailsThisCheck(t *testing.T) {
	wf := workflowText(t)
	ok(t, assertWorkflowGuardDiff(wf))
	if err := assertWorkflowGuardDiff(strings.ReplaceAll(wf, "GUARD_IMPL=diff", "")); err == nil {
		t.Fatal("a workflow without GUARD_IMPL=diff must fail the check")
	}
}

func TestCiCursorGuardContinueOnErrorInGuardJobsFailsThisCheck(t *testing.T) {
	wf := workflowText(t)
	for _, job := range guardJobs {
		b, found := workflowJobBlock(wf, job)
		if !found {
			t.Fatalf("tests.yml has no %s job", job)
		}
		mutated := strings.Replace(wf, b, strings.TrimRight(b, "\n")+"\n    continue-on-error: true\n\n", 1)
		if mutated == wf {
			t.Fatalf("%s: the mutation changed nothing", job)
		}
		if err := assertWorkflowGuardDiff(mutated); err == nil {
			t.Errorf("continue-on-error in %s must fail the check", job)
		}
	}
}

func TestCiCursorGuardGuardDiffRunExitsNonzeroOnPythonGoDisagreement(t *testing.T) {
	root := repoRoot(t)
	td := t.TempDir()
	stub := filepath.Join(td, "worker-guard")
	writeFile(t, stub, "#!/bin/sh\ncat >/dev/null; exit 0\n", 0o755)
	env := map[string]string{"GUARD_IMPL": "diff", "GUARD_GO_BIN": stub}
	// hook_run must raise AssertionError naming both exit codes
	code := "import sys\nsys.path.insert(0, sys.argv[1] + '/tests')\nfrom guard_impl import hook_run\n" +
		"try:\n    hook_run(sys.argv[2], ['--role', 'coder'])\nexcept AssertionError as e:\n    print(e)\n    sys.exit(%s)\nsys.exit(0)\n"
	r := pyCode(t, env, strings.Replace(code, "%s", "3", 1), root, block)
	eq(t, "exit", r.Code, 3)
	contains(t, "assertion", r.Stdout, "python=2 go=0")
	// the CI run line: an uncaught disagreement ends the process non-zero (the AssertionError becomes exit 1)
	r = pyCode(t, env, strings.Replace(code, "%s", "1", 1), root, block)
	eq(t, "exit", r.Code, 1)
}

func TestCiCursorGuardCursorModulesAreNotExcludedFromShardsOrWorkflow(t *testing.T) {
	root := repoRoot(t)
	var exclude []string
	for _, ln := range strings.Split(workflowText(t), "\n") {
		if regexp.MustCompile(`(?i)exclude`).MatchString(ln) {
			exclude = append(exclude, ln)
		}
	}
	all, err := shard.AllModules(filepath.Join(root, "tests"))
	ok(t, err)
	have := map[string]bool{}
	for _, m := range all {
		have[m] = true
	}
	for _, name := range []string{"test_cursor_pilot.py", "test_cursor_harness.py", "test_cursor_guard_adapter.py"} {
		if !isFile(filepath.Join(root, "tests", name)) {
			t.Logf("absent: %s", name)
			continue
		}
		if !have[name] {
			t.Errorf("%s must be collected by the shard tool", name)
		}
		for _, ln := range exclude {
			if strings.Contains(ln, name) {
				t.Errorf("%s must not appear on a workflow exclude line: %s", name, ln)
			}
		}
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.email=t@example.invalid", "-c", "user.name=t"}, args...)...)
	cmd.Dir = dir
	// hermetic: nothing inherited that could point git at another repo (the tests/_gitenv.py rule)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func TestCiCursorGuardTierOnlyTestCursorPilotIsNotDocsTier(t *testing.T) {
	root := t.TempDir()
	gitIn(t, root, "init", "-q", "-b", "main")
	binSrc := "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef gate_x():\n    return 1\n"
	files := map[string]string{
		"bin/factory":         binSrc,
		"tests/test_alpha.py": "import unittest\n\nclass A(unittest.TestCase):\n    def test_a(self):\n        pass\n",
		"web/app.py":          "x = 1\n", "docs/a.md": "# a\n", "README.md": "hi\n",
		"tests/test_db_layout.py": "", "tests/test_inbox.py": "", "tests/test_judge.py": "",
	}
	for i := 0; i < 10; i++ {
		files["tests/test_pad"+string(rune('0'+i))+".py"] = ""
	}
	for p, text := range files {
		writeFile(t, filepath.Join(root, p), text, 0o644)
	}
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "base")
	writeFile(t, filepath.Join(root, "tests", "test_cursor_pilot.py"),
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_x(self): pass\n", 0o644)
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "cursor pilot")
	base := gitIn(t, root, "rev-parse", "HEAD")
	writeFile(t, filepath.Join(root, "tests", "test_cursor_pilot.py"),
		"import unittest\n\nclass T(unittest.TestCase):\n    def test_y(self): pass\n", 0o644)
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "head")
	head := gitIn(t, root, "rev-parse", "HEAD")
	plan, err := tier.Plan(root, base, head)
	ok(t, err)
	if plan.Mode == "docs" {
		t.Fatalf("a change to only tests/test_cursor_pilot.py must not be the docs tier: %+v", plan)
	}
}
