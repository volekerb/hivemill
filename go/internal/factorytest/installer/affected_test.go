package installer

// Port of tests/test_affected.py (ci/affected.py) onto go/cmd/ci-affected, driven as a subprocess in temp git repos.
//
// Adaptation: the Go tool runs --run through ci/affected.py and tests/_slot.py of the repo given by --root (the
// Python tests ran the script from the real checkout), so those two real files are copied into the temp repo's
// base commit. They are not tests/test_*.py modules, so the module counts the cases assert are unchanged.
//
// not ported: (none)

import (
	"bytes"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/citools/affected"
	ft "factory/go/internal/factorytest"

	_ "modernc.org/sqlite"
)

var affSmoke = []string{"test_db_layout.py", "test_inbox.py", "test_judge.py"}

func affBinBase() string {
	var hubs []string
	for i := 0; i < 16; i++ {
		hubs = append(hubs, fmt.Sprintf("hub(%d)", i))
	}
	return `SCHEMA = "a"
DEFAULT_CONFIG = {"k": "v"}

def db():
    return 1

def helper(x):
    return x

def cmd_judge(a):
    return helper(a)

def cmd_inbox(a):
    return 2

def hub(x):
    return x

def use_hub():
    return [` + strings.Join(hubs, ", ") + `]

def main():
    return 0
`
}

func affSorted(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	sort.Strings(out)
	return out
}

func affEq(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("selected %v, want %v", got, want)
	}
}

func affHas(t *testing.T, list []string, name string, want bool) {
	t.Helper()
	for _, x := range list {
		if x == name {
			if !want {
				t.Fatalf("%s selected in %v", name, list)
			}
			return
		}
	}
	if want {
		t.Fatalf("%s missing from %v", name, list)
	}
}

func contains(t *testing.T, s, sub string) {
	t.Helper()
	if !strings.Contains(s, sub) {
		t.Fatalf("%q does not contain %q", s, sub)
	}
}

func notContains(t *testing.T, s, sub string) {
	t.Helper()
	if strings.Contains(s, sub) {
		t.Fatalf("%q contains %q", s, sub)
	}
}

type affRes struct {
	out, err string
	code     int
}

type affFix struct {
	t    *testing.T
	repo string
	home string
	bin  string
}

const okTest = "import unittest\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertTrue(True)\n"

func newAff(t *testing.T) *affFix {
	t.Helper()
	tmp := t.TempDir()
	f := &affFix{t: t, repo: filepath.Join(tmp, "repo"), home: filepath.Join(tmp, "home"), bin: buildCmd(t, "ci-affected")}
	if err := os.MkdirAll(f.home, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"tests/stubs", "ci", "roles", "docs", "bin"} {
		if err := os.MkdirAll(filepath.Join(f.repo, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	f.git("init", "-q", "-b", "main")
	for _, n := range append(append([]string{}, affSmoke...), "test_alpha.py", "test_beta.py", "test_gamma.py", "test_shard.py", "test_roles.py") {
		f.write("tests/"+n, "# "+n+"\n")
	}
	for i := 0; i < 30; i++ {
		f.write(fmt.Sprintf("tests/test_filler%02d.py", i), "# filler\n")
	}
	f.write("tests/test_alpha.py", "# uses helper and ci/tool and bin\n# helper\n# ci/tool\n")
	f.write("tests/test_beta.py", "# factory inbox call\n")
	f.write("tests/test_gamma.py", "# factory judge call\n")
	f.write("tests/test_roles.py", "# roles/coder.md\n")
	f.write("tests/harness.py", "# h\n")
	f.write("tests/stubs/herdr", "# s\n")
	f.write("ci/tool.py", "x = 1\n")
	f.write("ci/shard.py", "y = 1\n")
	f.write("roles/coder.md", "a\n")
	f.write("docs/a.md", "a\n")
	f.write("bin/factory_cli.py", affBinBase())
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"ci/affected.py", "tests/_slot.py"} { // see the header: --run needs them under --root
		b, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		f.write(rel, string(b))
	}
	f.git("add", "-A")
	f.git("commit", "-qm", "base")
	f.git("branch", "base")
	f.git("checkout", "-qb", "work")
	return f
}

func (f *affFix) git(args ...string) { f.t.Helper(); hermeticGit(f.t, f.repo, args...) }

func (f *affFix) write(rel, text string) {
	f.t.Helper()
	p := filepath.Join(f.repo, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *affFix) commit() { f.t.Helper(); f.git("add", "-A"); f.git("commit", "-qm", "change") }

func (f *affFix) env() []string {
	return append(os.Environ(), "FACTORY_HOME="+f.home, "FACTORY_LOADAVG=0", "PYTHONDONTWRITEBYTECODE=1",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

func (f *affFix) runArgs(args ...string) affRes {
	f.t.Helper()
	for _, a := range args {
		if a == "--run" || a == "--quick" { // these run the selected Python unittest modules
			if _, err := exec.LookPath("python3"); err != nil {
				f.t.Skip("python3 is not on PATH: --run/--quick execute Python unittest modules; it runs where python3 exists")
			}
		}
	}
	c := exec.Command(f.bin, args...)
	c.Env = f.env()
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	code := 0
	if err := c.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			f.t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return affRes{so.String(), se.String(), code}
}

func (f *affFix) affected(extra ...string) affRes {
	f.t.Helper()
	return f.runArgs(append([]string{"--root", f.repo, "--base", "base"}, extra...)...)
}

func (f *affFix) selected() []string {
	f.t.Helper()
	r := f.affected("--list")
	if r.code != 0 {
		f.t.Fatalf("exit %d: %s", r.code, r.err)
	}
	return strings.Fields(r.out)
}

func TestAffectedTestFileOnlyChange(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_gamma.py", "# edited\n")
	f.commit()
	affEq(t, f.selected(), affSorted(affSmoke, []string{"test_gamma.py"}))
}

// T-1017: any tests/ path selects the modules that scan every test file; never FULL by itself.
func TestAffectedTestsChangeSelectsEveryWholeTreeLint(t *testing.T) {
	f := newAff(t)
	lints := affected.TestTreeLints
	for _, m := range []string{"test_guard_impl_coverage.py", "test_git_env_hermetic.py", "test_no_duplicate_defs.py",
		"test_skilleval_vendor.py", "test_skilleval_tasks.py", "test_scrub_clean.py"} {
		affHas(t, lints, m, true)
	}
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range lints {
		if _, err := os.Stat(filepath.Join(root, "tests", m)); err != nil {
			t.Errorf("%s: %v", m, err)
		}
	}
	for _, m := range lints {
		f.write("tests/"+m, "# lint\n")
	}
	f.git("add", "-A")
	f.git("commit", "-qm", "lints on base")
	f.git("branch", "-f", "base")
	f.write("tests/test_gamma.py", "# edited\n")
	f.commit()
	got := f.selected()
	affEq(t, got, affSorted(affSmoke, []string{"test_gamma.py"}, lints))
	affHas(t, got, "FULL", false)
}

func TestAffectedNoTestsPathDoesNotSelectWholeTreeLints(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_guard_impl_coverage.py", "# lint\n")
	f.write("tests/test_git_env_hermetic.py", "# lint\n")
	f.git("add", "-A")
	f.git("commit", "-qm", "lints on base")
	f.git("branch", "-f", "base")
	f.write("ci/tool.py", "x = 2\n")
	f.commit()
	got := f.selected()
	affHas(t, got, "test_guard_impl_coverage.py", false)
	affHas(t, got, "test_git_env_hermetic.py", false)
}

// #260: a deleted tests/test_X.py leaves a stale ci/shard_timings.json entry; test_shard.py must run.
func TestAffectedDeletedTestModuleSelectsTestShard(t *testing.T) {
	f := newAff(t)
	f.git("rm", "-q", "tests/test_gamma.py")
	f.commit()
	affHas(t, f.selected(), "test_shard.py", true)
}

func TestAffectedDeletedTestModuleCliListsTestShard(t *testing.T) {
	f := newAff(t)
	f.git("rm", "-q", "tests/test_gamma.py")
	f.commit()
	r := f.affected("--head", "HEAD", "--list")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	affHas(t, strings.Split(strings.TrimRight(r.out, "\n"), "\n"), "test_shard.py", true)
}

func TestAffectedRenamedTestModuleSelectsTestShardAndNewName(t *testing.T) {
	f := newAff(t)
	f.git("mv", "tests/test_gamma.py", "tests/test_delta.py")
	f.commit()
	got := f.selected()
	affHas(t, got, "test_shard.py", true)
	affHas(t, got, "test_delta.py", true)
}

func TestAffectedShardTimingsChangeSelectsTestShard(t *testing.T) {
	f := newAff(t)
	f.git("checkout", "-q", "base")
	f.write("ci/shard_timings.json", "{}\n")
	f.write("tests/test_shard.py", "# ci/shard_timings.json\n") // as in the repo: test_shard.py mentions it
	f.commit()
	f.git("checkout", "-q", "work")
	f.git("rebase", "-q", "base")
	f.write("ci/shard_timings.json", "{\"x\": 1}\n")
	f.commit()
	affHas(t, f.selected(), "test_shard.py", true)
}

func TestAffectedEditedTestModuleDoesNotSelectTestShard(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_gamma.py", "# edited again\n")
	f.commit()
	affHas(t, f.selected(), "test_shard.py", false)
}

func TestAffectedCiChangeSelectsMentioningModulesAndOwnTest(t *testing.T) {
	f := newAff(t)
	f.write("ci/shard.py", "y = 2\n")
	f.commit()
	got := f.selected()
	affHas(t, got, "test_shard.py", true)
	affHas(t, got, "test_alpha.py", false)
	f.write("ci/tool.py", "x = 2\n")
	f.commit()
	affHas(t, f.selected(), "test_alpha.py", true) // mentions ci/tool
}

func TestAffectedBinFactoryChangeInsideOneCmdSelectsItsTestsOnly(t *testing.T) {
	f := newAff(t)
	f.write("bin/factory_cli.py", strings.Replace(affBinBase(), "def cmd_inbox(a):\n    return 2", "def cmd_inbox(a):\n    return 3", 1))
	f.commit()
	got := f.selected()
	affHas(t, got, "test_beta.py", true) // "inbox" subcommand
	affHas(t, got, "test_gamma.py", false)
	affHas(t, got, "test_alpha.py", false)
}

func TestAffectedBinFactoryHelperChangeSelectsByDefName(t *testing.T) {
	f := newAff(t)
	f.write("bin/factory_cli.py", strings.Replace(affBinBase(), "def helper(x):\n    return x", "def helper(x):\n    return x + 1", 1))
	f.commit()
	got := f.selected()
	affHas(t, got, "test_alpha.py", true)
	affHas(t, got, "test_beta.py", false)
}

func TestAffectedSchemaChangeIsFull(t *testing.T) {
	f := newAff(t)
	f.write("bin/factory_cli.py", strings.Replace(affBinBase(), `SCHEMA = "a"`, `SCHEMA = "b"`, 1))
	f.commit()
	r := f.affected()
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	if !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
	contains(t, r.out, "SCHEMA")
}

func TestAffectedDefaultConfigMainAndDbAreFull(t *testing.T) {
	f := newAff(t)
	for _, c := range [][2]string{{`{"k": "v"}`, `{"k": "w"}`}, {"def main():\n    return 0", "def main():\n    return 1"},
		{"def db():\n    return 1", "def db():\n    return 2"}} {
		f.git("checkout", "-q", "work")
		f.git("reset", "-q", "--hard", "base")
		f.write("bin/factory_cli.py", strings.Replace(affBinBase(), c[0], c[1], 1))
		f.commit()
		if r := f.affected(); !strings.HasPrefix(r.out, "FULL") {
			t.Fatalf("%s: %q", c[0], r.out)
		}
	}
}

func TestAffectedHubDefChangeIsFull(t *testing.T) {
	f := newAff(t)
	f.write("bin/factory_cli.py", strings.Replace(affBinBase(), "def hub(x):\n    return x", "def hub(x):\n    return -x", 1))
	f.commit()
	r := f.affected()
	if !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
	contains(t, r.out, "16 call sites")
}

func TestAffectedHarnessAndStubChangesAreFull(t *testing.T) {
	f := newAff(t)
	f.write("tests/harness.py", "# changed\n")
	f.commit()
	if r := f.affected(); !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
	f.git("reset", "-q", "--hard", "base")
	f.write("tests/stubs/herdr", "# changed\n")
	f.commit()
	if r := f.affected(); !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
}

func TestAffectedDocsOnlyIsTheSmokeSet(t *testing.T) {
	f := newAff(t)
	f.write("docs/a.md", "changed\n")
	f.write("README.md", "new\n")
	f.commit()
	affEq(t, f.selected(), affSorted(affSmoke))
}

func TestAffectedRolesChangeSelectsMentioningModules(t *testing.T) {
	f := newAff(t)
	f.write("roles/coder.md", "b\n")
	f.commit()
	affHas(t, f.selected(), "test_roles.py", true)
}

func TestAffectedSmokeSetComesFromConfig(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil { // the Go tool reads the factory config through the Python CLI
		t.Skip("python3 is not on PATH: the config lookup is handed to Python; it runs where python3 exists")
	}
	f := newAff(t)
	if err := os.MkdirAll(filepath.Join(f.home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", filepath.Join(f.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)", "INSERT INTO config VALUES ('affected_smoke', 'test_beta.py')"} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	f.write("docs/a.md", "changed\n")
	f.commit()
	affEq(t, f.selected(), []string{"test_beta.py"})
}

func TestAffectedOver40PercentIsFull(t *testing.T) {
	f := newAff(t)
	for i := 0; i < 30; i++ {
		f.write(fmt.Sprintf("tests/test_filler%02d.py", i), "# edited\n")
	}
	f.commit()
	r := f.affected()
	if !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
	contains(t, r.out, "rule 5")
}

func TestAffectedDeletedTestModuleNotSelected(t *testing.T) {
	f := newAff(t)
	f.git("rm", "-q", "tests/test_gamma.py")
	f.commit()
	got := f.selected()
	affHas(t, got, "test_gamma.py", false)
	affEq(t, got, affSorted(affSmoke, []string{"test_shard.py"})) // T-0916 rule 1b
}

func TestAffectedRenamedTestModuleSelectsNewName(t *testing.T) {
	f := newAff(t)
	f.git("mv", "tests/test_gamma.py", "tests/test_delta.py")
	f.commit()
	got := f.selected()
	affHas(t, got, "test_delta.py", true)
	affHas(t, got, "test_gamma.py", false)
}

func TestAffectedUnknownPathNobodyMentionsIsFull(t *testing.T) {
	f := newAff(t)
	f.write("web/app.py", "x\n")
	f.commit()
	r := f.affected()
	if !strings.HasPrefix(r.out, "FULL") {
		t.Fatal(r.out)
	}
	contains(t, r.out, "web/app.py")
}

func TestAffectedUnknownPathSelectsModulesThatMentionIt(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_beta.py", "# imports web.app and checks web/app.py\n")
	f.git("add", "-A")
	f.git("commit", "-qm", "mention")
	f.git("branch", "-f", "base")
	f.write("web/app.py", "x\n")
	f.commit()
	affEq(t, f.selected(), affSorted(affSmoke, []string{"test_beta.py"}))
}

// T-0665: `from web.views import home` mentions neither the path web/views/home.py nor the dotted web.views.home.
func TestAffectedViewChangeSelectsModulesThatImportIt(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_util.py", "from web.views import home  # noqa\n")
	f.write("tests/test_multi.py", "from web.views import (\n    agents,\n    home as h,\n)\n")
	f.write("tests/test_dotted.py", "import web.views.home\n")
	f.write("tests/test_other.py", "from web.views import homepage, agents\n")
	f.git("add", "-A")
	f.git("commit", "-qm", "importers")
	f.git("branch", "-f", "base")
	f.write("web/views/home.py", "x = 1\n")
	f.commit()
	affEq(t, f.selected(), affSorted(affSmoke, []string{"test_util.py", "test_multi.py", "test_dotted.py"}))
}

func TestAffectedViewChangeSelectsParenthesisedImportOnOneLine(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_util.py", "from web.views import (agents, home)\n")
	f.git("add", "-A")
	f.git("commit", "-qm", "importer")
	f.git("branch", "-f", "base")
	f.write("web/views/home.py", "x = 1\n")
	f.commit()
	affHas(t, f.selected(), "test_util.py", true)
}

// T-0665: a dirty worktree must survive an affected --run byte for byte.
func TestAffectedRunLeavesUncommittedAndUntrackedEditsInTheCallerWorktree(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_beta.py", okTest)
	f.commit()
	f.write("ci/tool.py", "x = 2  # uncommitted\n")
	f.write("scratch.txt", "untracked\n")
	f.write("docs/a.md", "staged\n")
	f.git("add", "docs/a.md")
	snap := func() string {
		var keep []string
		for _, l := range strings.SplitAfter(hermeticGit(t, f.repo, "status", "--porcelain=v1", "-uall"), "\n") {
			if !strings.Contains(l, "__pycache__") {
				keep = append(keep, l)
			}
		}
		for _, p := range []string{"ci/tool.py", "scratch.txt", "docs/a.md"} {
			b, err := os.ReadFile(filepath.Join(f.repo, p))
			if err != nil {
				t.Fatal(err)
			}
			keep = append(keep, "\x00"+p+"\x00"+string(b))
		}
		return strings.Join(keep, "")
	}
	before := snap()
	r := f.affected("--run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	contains(t, r.out, "Ran 1 tests")
	if after := snap(); after != before {
		t.Fatalf("worktree changed:\n%s\n---\n%s", before, after)
	}
	contains(t, before, "\x00ci/tool.py\x00x = 2  # uncommitted\n")
}

func TestAffectedBadBaseFailsWithHint(t *testing.T) {
	f := newAff(t)
	r := f.runArgs("--root", f.repo, "--base", "nope")
	if r.code != 2 {
		t.Fatalf("exit %d", r.code)
	}
	contains(t, r.err, "git fetch")
}

func TestAffectedRunExecutesSelectionAndReportsCount(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_gamma.py", okTest)
	f.commit()
	r := f.affected("--run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	contains(t, r.out, "affected: Ran 1 tests in 4 modules: OK")
}

func TestAffectedRunFailsOnFailingTest(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_gamma.py", "import unittest\nclass T(unittest.TestCase):\n    def test_bad(self):\n        self.fail('x')\n")
	f.commit()
	r := f.affected("--run")
	if r.code != 1 {
		t.Fatalf("exit %d", r.code)
	}
	contains(t, r.out, "FAILED")
}

func TestAffectedQuickRunsAffectedInOneProcessAndPrintsElapsed(t *testing.T) {
	f := newAff(t)
	pid := "import os\nimport unittest\nclass T(unittest.TestCase):\n    def test_pid(self):\n        print('PID', os.getpid())\n"
	f.write("tests/test_gamma.py", pid)
	f.write("tests/test_beta.py", pid)
	f.commit()
	r := f.affected("--quick", "--run")
	if r.code != 0 {
		t.Fatalf("exit %d: %s", r.code, r.err)
	}
	contains(t, r.out, "affected: Ran 2 tests in 5 modules: OK")
	if !regexp.MustCompile(`affected: elapsed [\d.]+ s of 120 s budget`).MatchString(r.out) {
		t.Fatal(r.out)
	}
	pids := map[string]bool{}
	for _, m := range regexp.MustCompile(`PID (\d+)`).FindAllStringSubmatch(r.out+r.err, -1) {
		pids[m[1]] = true
	}
	if len(pids) != 2 {
		t.Fatalf("quick mode: one child process per module, none the parent; got %v", pids)
	}
}

func TestAffectedQuickRefusesFullAndRunsNothing(t *testing.T) {
	f := newAff(t)
	f.write("tests/harness.py", "raise SystemExit('must not be imported')\n")
	f.commit()
	for _, extra := range [][]string{{"--quick"}, {"--quick", "--run"}, {"--budget", "30", "--run"}} {
		r := f.affected(extra...)
		if r.code != 0 {
			t.Fatalf("%v: exit %d: %s", extra, r.code, r.err)
		}
		contains(t, r.out, "FULL needed: rely on CI")
		notContains(t, r.out, "Ran ")
	}
}

func TestAffectedWithoutQuickFullStillRunsEverything(t *testing.T) {
	f := newAff(t)
	f.write("tests/harness.py", "# edited\n")
	f.commit()
	r := f.affected("--run")
	contains(t, r.out, "FULL (")
}

func TestAffectedBudgetStopsStartingModulesAndSaysRelyOnCi(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_beta.py", okTest)
	f.write("tests/test_gamma.py", "import time\nimport unittest\nclass T(unittest.TestCase):\n    def test_slow(self):\n        time.sleep(60)\n")
	f.commit()
	r := f.affected("--budget", "3", "--run")
	if r.code != 4 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	if !regexp.MustCompile(`Ran 1 tests in \d+ modules: OK`).MatchString(r.out) { // beta ran; gamma was killed at the cap
		t.Fatal(r.out)
	}
	contains(t, r.out, "elapsed")
	contains(t, r.err, "rely on CI")
	contains(t, r.err, "test_inbox.py")
}

func TestAffectedQuickZeroTestsWithSkippedModuleExits1(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_guard_diff.py", "# skipped\n")
	f.commit()
	r := f.affected("--quick", "--run")
	if r.code != 1 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	contains(t, r.err, "ran 0 tests")
	contains(t, r.err, "slow: test_guard_diff.py")
}

func TestAffectedBudgetKillsAModuleRunningPastIt(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_beta.py", okTest)
	f.write("tests/test_gamma.py", "import time\nimport unittest\nclass T(unittest.TestCase):\n    def test_slow(self):\n        time.sleep(60)\n")
	f.commit()
	t0 := time.Now() // upper bound only; the assertion is a generous kill-deadline, not a data input
	r := f.affected("--budget", "2", "--run")
	if time.Since(t0) >= 7*time.Second {
		t.Fatalf("took %v", time.Since(t0))
	}
	if r.code != 4 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	if !regexp.MustCompile(`(?s)budget spent, not run: test_gamma\.py.*rely on CI`).MatchString(r.err) {
		t.Fatal(r.err)
	}
}

func TestAffectedQuickNeverStartsQuickSkipModules(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_beta.py", okTest)
	f.write("tests/test_guard_diff.py", "raise SystemExit('must not be started')\n")
	f.commit()
	r := f.affected("--quick", "--run")
	if r.code != 4 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	contains(t, r.err, "slow: test_guard_diff.py; rely on CI")
	notContains(t, r.out+r.err, "must not be started")
}

func TestAffectedQuickFailingFastModuleExits1(t *testing.T) {
	f := newAff(t)
	f.write("tests/test_gamma.py", "import unittest\nclass T(unittest.TestCase):\n    def test_bad(self):\n        self.fail('x')\n")
	f.write("tests/test_guard_diff.py", "# skipped\n")
	f.commit()
	r := f.affected("--quick", "--run")
	if r.code != 1 {
		t.Fatalf("exit %d: %s%s", r.code, r.out, r.err)
	}
	contains(t, r.out, "FAILED")
}

func TestAffectedBudgetMustBePositive(t *testing.T) {
	f := newAff(t)
	if r := f.affected("--budget", "0"); r.code != 2 {
		t.Fatalf("exit %d", r.code)
	}
}
