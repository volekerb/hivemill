package tier

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const binSrc = "def cmd_other():\n    return 1\n\n\ndef cmd_judge():\n    return 1\n\n\ndef judge_pick():\n    return 1\n\n\ndef gate_x():\n    return 1\n"

func baseFixture(t *testing.T) *fixture {
	f := newFixture(t)
	f.write("bin/factory", binSrc)
	f.write("bin/factory_cli.py", "def cmd_x():\n    return 1\n")
	f.write("tests/test_alpha.py", "import unittest\n\nclass A(unittest.TestCase):\n    def test_a(self):\n        pass\n")
	f.write("tests/test_db_layout.py", "")
	f.write("tests/test_inbox.py", "")
	f.write("tests/test_judge.py", "")
	f.write("tests/test_docs.py", "x = 'docs/a.md'\n")
	for i := 0; i < 10; i++ {
		f.write(fmt.Sprintf("tests/test_pad%d.py", i), "")
	}
	f.write("web/app.py", "x = 1\n")
	f.write("go/x.go", "package x\n")
	f.write("roles/role.md", "# role\n")
	f.write("roles/hooks/h.sh", "echo\n")
	f.write(".github/workflows/t.yml", "name: t\n")
	f.write("ci/foo.py", "z = 1\n")
	f.write("docs/a.md", "# a\n[b](b.md)\n")
	f.write("docs/b.md", "# b\n")
	f.write("README.md", "hi\n")
	f.commit("base")
	return f
}

type scenario struct {
	name   string
	mutate func(f *fixture)
	loose  bool
	want   map[string]string // facts that must hold whatever Python says (the gate is not weakened)
}

func many(n int) string { return strings.Repeat("x = 1\n", n) }

var scenarios = []scenario{
	{name: "docs-only change", mutate: func(f *fixture) { f.write("docs/a.md", "# a2\n[b](b.md)\n") }, want: map[string]string{"tier": "docs", "mode": "affected", "go": "false"}},
	{name: "docs change no test mentions", mutate: func(f *fixture) { f.write("README.md", "hello\n") }, want: map[string]string{"tier": "docs", "mode": "docs"}},
	{name: "go change", mutate: func(f *fixture) { f.write("go/x.go", "package x\n// c\n") }, want: map[string]string{"go": "true"}},
	{name: "roles change", mutate: func(f *fixture) { f.write("roles/role.md", "# role2\n") }, want: map[string]string{"tier": "normal"}},
	{name: "roles hook change is risky", mutate: func(f *fixture) { f.write("roles/hooks/h.sh", "echo 2\n") }, want: map[string]string{"tier": "risky", "mode": "full", "go": "true"}},
	{name: "tests change", mutate: func(f *fixture) { f.write("tests/test_alpha.py", "# e\n") }, want: map[string]string{"mode": "affected"}},
	{name: ".github change is risky", mutate: func(f *fixture) { f.write(".github/workflows/t.yml", "name: u\n") }, want: map[string]string{"tier": "risky", "mode": "full"}},
	{name: "bin/factory_cli.py change", mutate: func(f *fixture) { f.write("bin/factory_cli.py", "def cmd_x():\n    return 2\n") }, want: map[string]string{"tier": "normal"}},
	{name: "bin/factory sensitive def change", mutate: func(f *fixture) {
		f.write("bin/factory", strings.Replace(binSrc, "def cmd_judge():\n    return 1", "def cmd_judge():\n    return 2", 1))
	}, want: map[string]string{"tier": "risky", "reason": "cmd_judge changed in bin/factory"}},
	{name: "bin/factory prefix def change", mutate: func(f *fixture) {
		f.write("bin/factory", strings.Replace(binSrc, "def gate_x():\n    return 1", "def gate_x():\n    return 2", 1))
	}, want: map[string]string{"tier": "risky"}},
	{name: "bin/factory harmless def change", mutate: func(f *fixture) {
		f.write("bin/factory", strings.Replace(binSrc, "def cmd_other():\n    return 1", "def cmd_other():\n    return 2", 1))
	}, want: map[string]string{"tier": "normal"}},
	{name: "bin/factory does not parse", loose: true, mutate: func(f *fixture) { f.write("bin/factory", "x = 'abc\n") }, want: map[string]string{"tier": "risky"}},
	{name: "ci gate script is risky", mutate: func(f *fixture) { f.write("ci/shard.py", "x = 1\n") }, want: map[string]string{"tier": "risky"}},
	{name: "new top-level entry is risky", mutate: func(f *fixture) { f.write("newdir/a.txt", "a\n") }, want: map[string]string{"tier": "risky", "reason": "new top-level entry newdir"}},
	{name: "large diff is risky", mutate: func(f *fixture) { f.write("web/app.py", many(700)) }, want: map[string]string{"tier": "risky", "mode": "full"}},
	{name: "binary file is risky", mutate: func(f *fixture) { f.write("web/blob.bin", "\x00\x01\x02\x00binary\x00") }, want: map[string]string{"tier": "risky"}},
	{name: "binary file edited in docs", mutate: func(f *fixture) {
		f.write("docs/pic.png", "\x89PNG\x00\x00\x00")
	}, want: map[string]string{"tier": "risky"}},
	{name: "empty diff", mutate: func(f *fixture) {}, want: map[string]string{"tier": "normal", "reason": "empty diff"}},
	{name: "unmentioned python module", mutate: func(f *fixture) { f.write("web/app.py", "x = 3\n") }, want: map[string]string{"tier": "normal", "mode": "full"}},
	{name: "rename in docs", mutate: func(f *fixture) { f.git("mv", "docs/b.md", "docs/c.md") }},
}

func TestTierParity(t *testing.T) {
	script := needPython(t, "ci/ci_tier.py")
	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			f := baseFixture(t)
			base := f.git("rev-parse", "HEAD")
			sc.mutate(f)
			head := f.commit("head")
			for _, args := range [][]string{{"classify"}, {"docs-check"}} {
				full := append(append([]string{}, args...), "--root", f.dir, "--base", base, "--head", head)
				pso, pse, pcode := pyRun(t, f.dir, append([]string{script}, full...)...)
				gso, gse, gcode := runGo(Main, full...)
				if sc.loose {
					pso, gso = cutAt(pso, "does not parse"), cutAt(gso, "does not parse")
				}
				if pso != gso || pcode != gcode || pse != gse {
					t.Errorf("%v differs\npython: exit %d %q %q\ngo:     exit %d %q %q", args, pcode, pso, pse, gcode, gso, gse)
				}
				if args[0] == "classify" {
					got := parseKV(gso)
					for k, v := range sc.want {
						if got[k] != v {
							t.Errorf("%s = %q, want %q (full output %q)", k, got[k], v, gso)
						}
					}
				}
			}
		})
	}
}

func cutAt(s, marker string) string {
	if i := strings.Index(s, marker); i >= 0 {
		return s[:i+len(marker)]
	}
	return s
}

func parseKV(s string) map[string]string {
	out := map[string]string{}
	for _, l := range strings.Split(s, "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			out[k] = v
		}
	}
	return out
}

func TestTierParityDocsCheckProblems(t *testing.T) {
	script := needPython(t, "ci/ci_tier.py")
	f := baseFixture(t)
	base := f.git("rev-parse", "HEAD")
	f.write("docs/bad.md", "# bad\n[missing](nope.md)\n[ok](b.md#x)\n[web](https://x.invalid/a)\n```\n[in fence](zzz.md)\n```\n~~~\nopen fence\n")
	f.write("docs/bad2.md", "```\nnever closed\n[x](y.md)\n")
	head := f.commit("head")
	full := []string{"docs-check", "--root", f.dir, "--base", base, "--head", head}
	pso, pse, pcode := pyRun(t, f.dir, append([]string{script}, full...)...)
	gso, gse, gcode := runGo(Main, full...)
	if pso != gso || pse != gse || pcode != gcode {
		t.Errorf("differs\npython: exit %d %q %q\ngo:     exit %d %q %q", pcode, pso, pse, gcode, gso, gse)
	}
	if gcode != 1 || !strings.Contains(gse, "link target nope.md does not exist") || !strings.Contains(gse, "unbalanced code fence") {
		t.Errorf("docs-check did not report the problems: exit %d %q", gcode, gse)
	}
}

func TestTierParityOutputFiles(t *testing.T) {
	script := needPython(t, "ci/ci_tier.py")
	f := baseFixture(t)
	base := f.git("rev-parse", "HEAD")
	f.write("tests/test_alpha.py", "# e\n")
	head := f.commit("head")
	tmp := t.TempDir()
	pOnly, gOnly := filepath.Join(tmp, "p-only"), filepath.Join(tmp, "g-only")
	pOut, gOut := filepath.Join(tmp, "p-out"), filepath.Join(tmp, "g-out")
	common := []string{"classify", "--root", f.dir, "--base", base, "--head", head}
	t.Setenv("GITHUB_OUTPUT", pOut)
	pso, _, _ := pyRun(t, f.dir, append([]string{script}, append(common, "--only-file", pOnly, "--github-output")...)...)
	t.Setenv("GITHUB_OUTPUT", gOut)
	gso, _, gcode := runGo(Main, append(common, "--only-file", gOnly, "--github-output")...)
	if pso != gso || gcode != 0 {
		t.Fatalf("stdout differs: python %q go %q (exit %d)", pso, gso, gcode)
	}
	for _, pair := range [][2]string{{pOnly, gOnly}, {pOut, gOut}} {
		a, errA := os.ReadFile(pair[0])
		b, errB := os.ReadFile(pair[1])
		if errA != nil || errB != nil || string(a) != string(b) || len(a) == 0 {
			t.Errorf("file %s vs %s: %q (%v) vs %q (%v)", pair[0], pair[1], a, errA, b, errB)
		}
	}
}

// An undecidable diff gets the whole suite, never a skip (r1).
func TestTierFallbackIsFull(t *testing.T) {
	f := baseFixture(t)
	so, se, code := runGo(Main, "classify", "--root", f.dir, "--base", "no-such-ref", "--head", "HEAD")
	if code != 0 {
		t.Fatalf("exit %d, want 0 with a full-suite fallback", code)
	}
	for _, want := range []string{"tier=risky", "mode=full", "go=true", "reason=classifier error"} {
		if !strings.Contains(so, want) {
			t.Errorf("output %q lacks %q", so, want)
		}
	}
	if !strings.Contains(se, "::warning::ci_tier could not classify") {
		t.Errorf("stderr %q lacks the warning", se)
	}
}

func TestTierUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nope"}, {"classify"}, {"classify", "--base"}} {
		if _, _, code := runGo(Main, args...); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
	}
	f := baseFixture(t)
	if _, _, code := runGo(Main, "docs-check", "--root", f.dir, "--base", "HEAD", "--github-output"); code != 2 {
		t.Errorf("docs-check with a classify-only flag: exit %d, want 2", code)
	}
}

// ci-tier runs from the BASE commit in CI: the documented build set (go.mod, go.sum, cmd/ci-tier and the two packages
// it imports) must build on its own and give the same answer as the Python script.
func TestBuildFromBaseSubset(t *testing.T) {
	script := needPython(t, "ci/ci_tier.py")
	goDir := filepath.Join(repoRoot(t), "go")
	build := t.TempDir()
	for _, rel := range []string{"go.mod", "go.sum", "cmd/ci-tier", "internal/citools/affected", "internal/citools/tier"} {
		copyTree(t, filepath.Join(goDir, rel), filepath.Join(build, rel))
	}
	bin := filepath.Join(t.TempDir(), "ci-tier")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/ci-tier")
	cmd.Dir = build
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the base-commit build set does not build: %v\n%s", err, out)
	}
	f := baseFixture(t)
	base := f.git("rev-parse", "HEAD")
	f.write("roles/hooks/h.sh", "echo 2\n")
	head := f.commit("head")
	args := []string{"classify", "--root", f.dir, "--base", base, "--head", head}
	pso, _, _ := pyRun(t, f.dir, append([]string{script}, args...)...)
	out, err := exec.Command(bin, args...).Output()
	if err != nil || string(out) != pso || !strings.Contains(pso, "tier=risky") {
		t.Errorf("built binary: %q (%v), python %q", out, err, pso)
	}
}

func copyTree(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.Walk(from, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if info.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		if strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
