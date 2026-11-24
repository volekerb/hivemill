package installer

// Port of tests/test_ci_tier.py (ci/ci_tier.py) onto go/cmd/ci-tier, driven as a subprocess in temp git repos.
// The Python classify()/plan() calls become `ci-tier classify` (tier=, mode=, go=, modules=, reason= lines); the
// reason of classify() and of plan() is the same string. The workflow cases read the real .github/workflows/tests.yml.
//
// not ported: the TESTS_YML environment override inside test_docs_tier_required_jobs_skip_steps_not_the_job - it
// only let the Python test point at another file; the Go case always reads the real workflow.

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

const tierBinSrc = "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef gate_x():\n    return 1\n"

const tierGuardJobIf = "    if: ${{ github.event_name == 'workflow_dispatch' && needs.ci-tier.outputs.mode != 'docs' }}"

// docsTierJobSkipErrors returns errors when unittest-shard/go use job-level skip instead of step-level (T-1512).
func docsTierJobSkipErrors(text string) []string {
	var errs []string
	lines := strings.SplitAfter(text, "\n")
	jobBody := func(name string) string {
		start := -1
		for i, l := range lines {
			if l == "  "+name+":\n" || l == "  "+name+":" {
				start = i
				break
			}
		}
		if start < 0 {
			panic("no job " + name)
		}
		var body []string
		for _, l := range lines[start+1:] {
			if strings.HasPrefix(l, "  ") && !strings.HasPrefix(l, "   ") {
				break
			}
			body = append(body, l)
		}
		return strings.Join(body, "")
	}
	stepSplit := regexp.MustCompile(`(?m)^      - `)
	for _, job := range []string{"unittest-shard", "go"} {
		body := jobBody(job)
		if !strings.Contains(body, "    steps:\n") {
			errs = append(errs, job+": missing steps block")
			continue
		}
		parts := strings.SplitN(body, "    steps:\n", 2)
		for _, l := range strings.Split(parts[0], "\n") {
			if strings.HasPrefix(l, "    if:") {
				errs = append(errs, job+" has a job-level if")
				break
			}
		}
		chunks := stepSplit.Split(parts[1], -1)[1:]
		if len(chunks) != 8 {
			errs = append(errs, fmt.Sprintf("%s: expected 8 steps, got %d", job, len(chunks)))
			continue
		}
		for i, c := range chunks {
			if !strings.Contains(c, "github.event_name != 'pull_request'") {
				errs = append(errs, fmt.Sprintf("%s step %d missing pull_request gate", job, i+1))
			}
			if !strings.Contains(c, "needs.ci-tier.outputs.mode != 'docs'") {
				errs = append(errs, fmt.Sprintf("%s step %d missing ci-tier docs gate", job, i+1))
			}
		}
	}
	for _, job := range []string{"guard-unittest-shard", "guard-equiv"} {
		header := strings.SplitN(jobBody(job), "    steps:\n", 2)[0]
		if !strings.Contains(header, tierGuardJobIf) {
			errs = append(errs, job+" missing guard job-level if")
		} else {
			has := false
			for _, l := range strings.Split(header, "\n") {
				if strings.HasPrefix(l, "    if:") {
					has = true
				}
			}
			if !has {
				errs = append(errs, job+" has no job-level if line")
			}
		}
	}
	return errs
}

// tierRepo is a temp git repo with a base commit; change commits a head on top.
type tierRepo struct {
	t    *testing.T
	root string
	base string
	bin  string
}

func newTierRepo(t *testing.T, dir string) *tierRepo {
	t.Helper()
	r := &tierRepo{t: t, root: dir, bin: buildCmd(t, "ci-tier")}
	r.git("init", "-q", "-b", "main")
	r.write("bin/factory", tierBinSrc)
	r.write("tests/test_alpha.py", "import unittest\n\nclass A(unittest.TestCase):\n    def test_a(self):\n        pass\n")
	r.write("tests/test_db_layout.py", "")
	r.write("tests/test_inbox.py", "")
	r.write("tests/test_judge.py", "")
	for i := 0; i < 10; i++ {
		r.write(fmt.Sprintf("tests/test_pad%d.py", i), "")
	}
	r.write("web/app.py", "x = 1\n")
	r.write("docs/a.md", "# a\n")
	r.write("README.md", "hi\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "base")
	r.base = r.rev()
	return r
}

func (r *tierRepo) git(args ...string) string { r.t.Helper(); return hermeticGit(r.t, r.root, args...) }
func (r *tierRepo) rev() string               { r.t.Helper(); return strings.TrimSpace(r.git("rev-parse", "HEAD")) }

func (r *tierRepo) write(rel, text string) {
	r.t.Helper()
	p := filepath.Join(r.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *tierRepo) change(files map[string]string) (string, string) {
	r.t.Helper()
	for rel, text := range files {
		r.write(rel, text)
	}
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "head")
	return r.base, r.rev()
}

func (r *tierRepo) run(env []string, args ...string) (string, string, int) {
	r.t.Helper()
	c := exec.Command(r.bin, args...)
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	c.Env = append(c.Env, env...)
	var so, se bytes.Buffer
	c.Stdout, c.Stderr = &so, &se
	code := 0
	if err := c.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			r.t.Fatal(err)
		}
		code = ee.ExitCode()
	}
	return so.String(), se.String(), code
}

// plan commits files and returns the parsed `ci-tier classify` output.
func (r *tierRepo) plan(files map[string]string) map[string]string {
	r.t.Helper()
	base, head := r.change(files)
	return r.classifyRange(base, head)
}

func (r *tierRepo) classifyRange(base, head string) map[string]string {
	r.t.Helper()
	so, se, code := r.run(nil, "classify", "--root", r.root, "--base", base, "--head", head)
	if code != 0 {
		r.t.Fatalf("classify exit %d: %s", code, se)
	}
	m := map[string]string{}
	for _, l := range strings.Split(strings.TrimRight(so, "\n"), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			if _, seen := m[k]; !seen {
				m[k] = v
			}
		}
	}
	return m
}

func tierEq(t *testing.T, got, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func tierSetup(t *testing.T) *tierRepo { t.Helper(); return newTierRepo(t, t.TempDir()) }

func TestCiTierDocsOnly(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"README.md": "changed\n", "docs/a.md": "# b\n", "docs/img/x.png": "bin"})["tier"], "docs")
}

func TestCiTierBinaryUnderDocsIsRiskyAndNamesPath(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"docs/x.bin": "\x00\x01"})
	tierEq(t, p["tier"], "risky")
	contains(t, p["reason"], "docs/x.bin")
	contains(t, p["reason"], "numstat")
}

func TestCiTierMarkdownOnlyUnderDocsStaysDocs(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"docs/note.md": "# only\n"})
	tierEq(t, p["tier"], "docs")
	contains(t, p["reason"], "only docs")
}

func TestCiTierRolesMarkdownIsNotDocs(t *testing.T) {
	r := tierSetup(t)
	r.write("roles/existing.md", "old\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "roles base")
	r.base = r.rev()
	tierEq(t, r.plan(map[string]string{"roles/coder.md": "x\n"})["tier"], "normal")
}

func TestCiTierNormal(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"web/app.py": "x = 2\n"})["tier"], "normal")
}

func TestCiTierNewTopLevelDirectoryIsRiskyAndNamesIt(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{".claude-plugin/manifest.json": "{}\n"})
	tierEq(t, p["tier"], "risky")
	contains(t, p["reason"], ".claude-plugin")
}

func TestCiTierNewTopLevelFileIsRiskyAndNamesIt(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"NEW_TOP.txt": "x\n"})
	tierEq(t, p["tier"], "risky")
	contains(t, p["reason"], "NEW_TOP.txt")
}

func TestCiTierChangeInsideExistingTopLevelNotNewEntryRule(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"web/nested/deep.py": "x = 1\n"})
	tierEq(t, p["tier"], "normal")
	notContains(t, p["reason"], "new top-level")
}

func TestCiTierDocsPlusCodeIsNormal(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"README.md": "c\n", "web/app.py": "x = 3\n"})["tier"], "normal")
}

func TestCiTierEveryRiskyPath(t *testing.T) {
	paths := []string{"ci/public_text_guard.py", "ci/publish_repo.py", "ci/scrub_scan.py", "ci/guard_diff.py",
		"ci/ci_tier.py", "ci/affected.py", "ci/shard.py", "roles/hooks/guard.sh",
		"roles/settings/coder.json.in", "scout/nsbox.py", "scout/gatebox.py", "scout/ghcred.py", "scout/egress.py", "scout/sandbox.py", "scout/workerbox.py", "systemd/factory-tick.timer",
		".github/workflows/tests.yml", ".github/CODEOWNERS"}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			r := tierSetup(t)
			tierEq(t, r.plan(map[string]string{p: "x\n"})["tier"], "risky")
		})
	}
}

func TestCiTierEveryRiskyDefInBinFactory(t *testing.T) {
	defs := []string{"cmd_judge", "judge_pick", "gate_x", "verdict_carries", "head_has_main", "refresh_branch",
		"git_config_guard", "noreply_email", "refuse_worker", "sandbox_preflight", "_public_guard", "_push_policy", "OWNER_CAP_KEYS", "DEFAULT_CONFIG", "SCHEMA"}
	for _, name := range defs {
		t.Run(name, func(t *testing.T) {
			r := tierSetup(t)
			var src string
			if strings.Contains(tierBinSrc, name) { // an existing def: edit its body
				src = strings.Replace(tierBinSrc, "def "+name+"():\n    return 1", "def "+name+"():\n    return 2", 1)
			} else if name[0] >= 'a' && name[0] <= 'z' || name[0] == '_' { // a new unit
				src = tierBinSrc + "\n\ndef " + name + "():\n    return 3\n"
			} else {
				src = tierBinSrc + "\n\n" + name + " = 3\n"
			}
			p := r.plan(map[string]string{"bin/factory": src})
			if p["tier"] != "risky" {
				t.Fatalf("tier %q: %s", p["tier"], p["reason"])
			}
			contains(t, p["reason"], name)
		})
	}
}

func TestCiTierOtherBinFactoryDefIsNormal(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"bin/factory": strings.Replace(tierBinSrc, "def cmd_other():\n    return 1", "def cmd_other():\n    return 2", 1)})["tier"], "normal")
}

func TestCiTierOver600LinesIsRiskyAndExactly600IsNot(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 601; i++ {
		fmt.Fprintf(&b, "v%d = %d\n", i, i)
	}
	big := b.String()
	r := tierSetup(t)
	p := r.plan(map[string]string{"web/big.py": big})
	tierEq(t, p["tier"], "risky")
	contains(t, p["reason"], "601 changed lines")
	r2 := tierSetup(t)
	tierEq(t, r2.plan(map[string]string{"web/big.py": big[:strings.LastIndex(big, "v600")]})["tier"], "normal")
}

// A PR that edits the rules is itself risky, and CI runs the BASE copy of the script (see the workflow test).
func TestCiTierRulesComeFromTheScriptThatRuns(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"ci/ci_tier.py": "MAX_LINES = 10**9\n"})["tier"], "risky")
}

func TestCiTierDocsModeSkipsSuiteAndGo(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"README.md": "c\n"})
	tierEq(t, strings.Join([]string{p["tier"], p["mode"], p["go"], p["modules"]}, "|"), "docs|docs|false|")
}

func TestCiTierDocsFileThatATestReadsRunsThatTest(t *testing.T) {
	r := tierSetup(t)
	r.write("tests/test_readme.py", "x = 'README.md'\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "more")
	r.base = r.rev()
	p := r.plan(map[string]string{"README.md": "c\n"})
	tierEq(t, strings.Join([]string{p["tier"], p["mode"], p["modules"]}, "|"), "docs|affected|test_readme.py")
}

func TestCiTierNormalModeListsAffectedModules(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"tests/test_alpha.py": "import unittest\n"})
	tierEq(t, strings.Join([]string{p["tier"], p["mode"], p["go"]}, "|"), "normal|affected|false")
	got := strings.Fields(p["modules"])
	if !sort.StringsAreSorted(got) || strings.Join(got, " ") != "test_alpha.py test_db_layout.py test_inbox.py test_judge.py" {
		t.Fatalf("modules %v", got)
	}
}

func TestCiTierGoChangeRunsGoJob(t *testing.T) {
	r := tierSetup(t)
	tierEq(t, r.plan(map[string]string{"go/main.go": "package main\n"})["go"], "true")
}

func TestCiTierRiskyIsFullWithGo(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{".github/CODEOWNERS": "x\n"})
	tierEq(t, p["mode"]+"|"+p["go"], "full|true")
}

func TestCiTierBinaryUnderDocsPlanIsFull(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"docs/y.bin": "\x00\x02"})
	tierEq(t, strings.Join([]string{p["tier"], p["mode"], p["go"]}, "|"), "risky|full|true")
	contains(t, p["reason"], "docs/y.bin")
}

func TestCiTierNewTopLevelDirectoryPlanIsFull(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"plugins/widget/README": "x\n"})
	tierEq(t, strings.Join([]string{p["tier"], p["mode"], p["go"]}, "|"), "risky|full|true")
	contains(t, p["reason"], "plugins")
}

func TestCiTierAffectedFullSelectionIsFullMode(t *testing.T) {
	r := tierSetup(t)
	p := r.plan(map[string]string{"tests/harness.py": ""})
	tierEq(t, p["tier"]+"|"+p["mode"], "normal|full")
}

func TestCiTierClassifyWritesOutputAndOnlyFile(t *testing.T) {
	r := tierSetup(t)
	base, head := r.change(map[string]string{"tests/test_alpha.py": "import unittest\n"})
	tmp := t.TempDir()
	out, only := filepath.Join(tmp, "gh_out"), filepath.Join(tmp, "only.txt")
	_, se, code := r.run([]string{"GITHUB_OUTPUT=" + out}, "classify", "--root", r.root, "--base", base, "--head", head,
		"--only-file", only, "--github-output")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, se)
	}
	ob, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	contains(t, string(ob), "mode=affected")
	mb, err := os.ReadFile(only)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range strings.Fields(string(mb)) {
		found = found || f == "test_alpha.py"
	}
	if !found {
		t.Fatalf("only file %q", mb)
	}
}

func TestCiTierUnknownBaseFallsBackToFull(t *testing.T) {
	r := tierSetup(t)
	so, se, code := r.run(nil, "classify", "--root", r.root, "--base", strings.Repeat("deadbeef", 5))
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	contains(t, so, "mode=full")
	contains(t, se, "::warning::")
}

func TestCiTierDocsCheckAcceptsGoodAndRejectsBrokenLinkAndFence(t *testing.T) {
	r := tierSetup(t)
	base, head := r.change(map[string]string{"docs/a.md": "# a\n[ok](../README.md) [web](https://x.example.test/y) [h](#top)\n"})
	so, se, code := r.run(nil, "docs-check", "--root", r.root, "--base", base, "--head", head)
	if code != 0 || strings.TrimSpace(so) != "docs-check: OK" {
		t.Fatalf("exit %d out %q err %q", code, so, se)
	}
	r.base = head
	_, head2 := r.change(map[string]string{"docs/a.md": "# a\n[bad](missing.md)\n```\nopen fence\n"})
	_, se, code = r.run(nil, "docs-check", "--root", r.root, "--base", head, "--head", head2)
	if code != 1 {
		t.Fatalf("exit %d", code)
	}
	contains(t, se, "missing.md does not exist")
	contains(t, se, "unbalanced code fence")
}

func tierWorkflow(t *testing.T) string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "tests.yml"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCiTierJobNamesUnchanged(t *testing.T) {
	wf := tierWorkflow(t)
	var got []string
	for _, m := range regexp.MustCompile(`(?m)^    name: (.+)$`).FindAllStringSubmatch(wf, -1) {
		got = append(got, m[1])
	}
	want := []string{"ci-tier", "docs-check", "unittest-shard (${{ matrix.shard }})", "go", "guard-unittest-shard (${{ matrix.shard }})", "guard-unittest", "guard-diff", "guard-self-compare", "guard-evaluator-home-loosen", "guard-equiv-shard (${{ matrix.shard }})", "guard-equiv", "guard-equiv-rest", "unittest"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("job names %q", got)
	}
}

func TestCiTierClassifierRunsFromTheBaseCommit(t *testing.T) {
	wf := tierWorkflow(t)
	if n := strings.Count(wf, `git archive "$BASE_SHA" ci/ci_tier.py ci/affected.py`); n != 4 {
		t.Fatalf("git archive count %d", n)
	}
	notContains(t, wf, "python ci/ci_tier.py")
	if n := strings.Count(wf, `python "$RUNNER_TEMP/base/ci/ci_tier.py" classify`); n != 3 {
		t.Fatalf("classify count %d", n)
	}
	contains(t, wf, "  ci-tier:\n")
}

func TestCiTierPushAndDispatchRunFull(t *testing.T) {
	contains(t, tierWorkflow(t), `if [ "$EVENT" != pull_request ]; then full "$EVENT"; exit 0; fi`)
}

func TestCiTierHeavyStepsAreGatedOnTier(t *testing.T) {
	wf := tierWorkflow(t)
	contains(t, wf, "steps.tier.outputs.go == 'true'")
	if n := strings.Count(wf, "steps.tier.outputs.mode != 'docs'"); n != 5 {
		t.Fatalf("mode != docs count %d", n)
	}
	contains(t, wf, "--only")
}

func TestCiTierDocsFastLaneSkipsHeavyJobs(t *testing.T) {
	wf := tierWorkflow(t)
	skip := "needs.ci-tier.outputs.mode != 'docs'"
	jobHeader := regexp.MustCompile(`^  [A-Za-z0-9_-]+:\s*$`)
	jobBlock := func(key string) string {
		parts := strings.SplitN(wf, "  "+key+":\n", 2)
		if len(parts) < 2 {
			t.Fatalf("no job %s", key)
		}
		var lines []string
		for _, l := range strings.Split(parts[1], "\n") {
			if jobHeader.MatchString(l) {
				break
			}
			lines = append(lines, l)
		}
		return strings.Join(lines, "\n")
	}
	for _, job := range []string{"unittest-shard", "go", "guard-unittest-shard", "guard-diff", "guard-self-compare",
		"guard-evaluator-home-loosen", "guard-equiv", "guard-equiv-rest"} {
		if !strings.Contains(jobBlock(job), skip) {
			t.Errorf("%s: job block lacks %q", job, skip)
		}
	}
	docs := strings.SplitN(strings.SplitN(wf, "  docs-check:\n", 2)[1], "\n  unittest-shard:\n", 2)[0]
	contains(t, docs, "needs.ci-tier.outputs.mode == 'docs'")
	contains(t, docs, `git archive "$BASE_SHA" ci/ci_tier.py ci/affected.py`)
	gate := strings.SplitN(strings.SplitN(wf, "      - name: Docs lane or every shard and guard job succeeded", 2)[1], "      - uses:", 2)[0]
	contains(t, gate, `if [ "$MODE" = "docs" ]; then`)
	contains(t, gate, "DOCS_CHECK_RESULT")
}

func TestCiTierRunnersAreFree(t *testing.T) {
	set := map[string]bool{}
	for _, m := range regexp.MustCompile(`runs-on: (\S+)`).FindAllStringSubmatch(tierWorkflow(t), -1) {
		set[m[1]] = true
	}
	if len(set) != 1 || !set["ubuntu-latest"] {
		t.Fatalf("runners %v", set)
	}
}

func TestCiTierDocsTierRequiredJobsSkipStepsNotTheJob(t *testing.T) {
	if errs := docsTierJobSkipErrors(tierWorkflow(t)); len(errs) != 0 {
		t.Fatal(errs)
	}
}
