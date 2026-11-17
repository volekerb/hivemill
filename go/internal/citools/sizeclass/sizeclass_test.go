package sizeclass

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type repo struct {
	t    *testing.T
	dir  string
	base string
}

// newRepo makes a hermetic temp git repo with one base commit (README.md, src/app.py, tests/test_x.py).
func newRepo(t *testing.T) *repo {
	t.Helper()
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "GIT_") {
			t.Setenv(name, "")
			os.Unsetenv(name)
		}
	}
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("FACTORY_HOME", filepath.Join(home, "factory"))
	r := &repo{t: t, dir: t.TempDir()}
	r.git("init", "-q", "-b", "main")
	r.write("README.md", "hi\n")
	r.write("docs/README.md", "index\n")
	r.write("src/app.py", "x = 1\n")
	r.write("tests/test_x.py", "import unittest\n")
	r.git("add", "-A")
	r.git("commit", "-q", "-m", "base")
	r.base = r.git("rev-parse", "HEAD")
	return r
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	full := append([]string{"-C", r.dir, "-c", "user.email=t@example.invalid", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (r *repo) write(rel, text string) {
	r.t.Helper()
	p := filepath.Join(r.dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) commit() {
	r.t.Helper()
	r.git("add", "-A")
	r.git("commit", "-q", "--allow-empty", "-m", "change")
}

func lines(n int) string { return strings.Repeat("line\n", n) }

// addLines commits a new file of n lines under src/.
func (r *repo) addLines(n int) {
	r.write("src/new.py", lines(n))
	r.commit()
}

func TestBoundaries(t *testing.T) {
	cases := []struct {
		n      int
		class  string
		budget int
	}{
		{40, "small", 10}, {41, "medium", 30}, {300, "medium", 30}, {301, "large", 40},
	}
	for _, c := range cases {
		t.Run(fmt.Sprint(c.n), func(t *testing.T) {
			r := newRepo(t)
			r.addLines(c.n)
			res := Select(r.dir, r.base, "HEAD")
			if res.Class != c.class || res.BudgetMin != c.budget || res.Lines != c.n {
				t.Fatalf("got %+v, want class %s budget %d lines %d", res, c.class, c.budget, c.n)
			}
			if c.class == "small" && res.Checks != "smoke" {
				t.Fatalf("small checks = %q, want smoke", res.Checks)
			}
			if c.class != "small" && res.Checks == "smoke" {
				t.Fatalf("%s checks must be the ci-tier mode, got smoke", c.class)
			}
		})
	}
}

func TestBinaryIsLarge(t *testing.T) {
	r := newRepo(t)
	r.write("src/blob.bin", "a\x00b\x00\xff")
	r.commit()
	res := Select(r.dir, r.base, "HEAD")
	if res.Class != "large" || res.Checks != "full" || res.BudgetMin != 40 {
		t.Fatalf("binary: got %+v, want large/full/40", res)
	}
}

func TestRenameCountsAddAndDelete(t *testing.T) {
	r := newRepo(t)
	r.write("src/big.py", lines(10))
	r.commit()
	r.base = r.git("rev-parse", "HEAD")
	r.git("mv", "src/big.py", "src/moved.py")
	r.commit()
	res := Select(r.dir, r.base, "HEAD")
	if res.Lines != 20 || res.Class != "small" {
		t.Fatalf("rename: got %+v, want lines=20 small", res)
	}
}

func TestEmptyDiffIsSmall(t *testing.T) {
	r := newRepo(t)
	r.commit()
	res := Select(r.dir, r.base, "HEAD")
	if res.Class != "small" || res.Lines != 0 || res.Checks != "smoke" {
		t.Fatalf("empty: got %+v", res)
	}
}

func TestBadRefFailsSafe(t *testing.T) {
	r := newRepo(t)
	var out, errb bytes.Buffer
	code := Main([]string{"--root", r.dir, "--base", "no-such-ref"}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit %d, want 0; stderr %s", code, errb.String())
	}
	got := out.String()
	for _, want := range []string{"class=large\n", "budget_min=40\n", "checks=full\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output %q lacks %q", got, want)
		}
	}
}

func TestDocsOnlySmall(t *testing.T) {
	r := newRepo(t)
	r.write("docs/note.md", "# note\n")
	r.commit()
	res := Select(r.dir, r.base, "HEAD")
	if res.Class != "small" || res.Checks != "docs" {
		t.Fatalf("docs: got %+v, want small/docs", res)
	}
}

func TestRiskyPathsAreLarge(t *testing.T) {
	paths := []string{
		"ci/public_text_guard.py", "ci/publish_repo.py", "ci/scrub_x.py", "ci/guard_x.py", "ci/ci_tier.py",
		"ci/affected.py", "ci/shard.py", "ci/test_count_gate.py", "roles/hooks/h.sh", "roles/x_settings.json",
		"scout/nsbox.py", "scout/gatebox.py", "scout/ghcred.py", "scout/egress.py", "scout/sandbox.py",
		"scout/workerbox.py", "systemd/a.service", ".github/workflows/t.yml",
	}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			r := newRepo(t)
			r.write(p, "x\n")
			r.commit()
			res := Select(r.dir, r.base, "HEAD")
			if res.Class != "large" || res.Checks != "full" || res.BudgetMin != 40 || res.Lines != 1 {
				t.Fatalf("%s: got %+v, want large/full/40 with 1 line", p, res)
			}
		})
	}
}

func TestJudgeDefIsLarge(t *testing.T) {
	r := newRepo(t)
	r.write("bin/factory_cli.py", "x = 1\n")
	r.commit()
	r.base = r.git("rev-parse", "HEAD")
	r.write("bin/factory", "#!/usr/bin/env python3\n")
	r.commit()
	r.base = r.git("rev-parse", "HEAD")
	r.write("bin/factory", "#!/usr/bin/env python3\ndef judge_merge():\n    pass\n")
	r.commit()
	res := Select(r.dir, r.base, "HEAD")
	if res.Class != "large" || res.Checks != "full" || res.Lines > 40 || !strings.Contains(res.Reason, "judge_merge") {
		t.Fatalf("judge_ def: got %+v, want large/full on a small diff", res)
	}
}

func TestCLIOutputAndGithubOutput(t *testing.T) {
	r := newRepo(t)
	r.addLines(5)
	gh := filepath.Join(t.TempDir(), "out")
	t.Setenv("GITHUB_OUTPUT", gh)
	var out, errb bytes.Buffer
	if code := Main([]string{"--root", r.dir, "--base", r.base, "--head", "HEAD", "--github-output"}, &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	got := strings.Split(strings.TrimSpace(out.String()), "\n")
	prefixes := []string{"class=", "budget_min=", "checks=", "lines=", "reason="}
	if len(got) != len(prefixes) {
		t.Fatalf("want 5 lines, got %q", got)
	}
	for i, p := range prefixes {
		if !strings.HasPrefix(got[i], p) {
			t.Fatalf("line %d = %q, want prefix %s", i, got[i], p)
		}
	}
	if got[0] != "class=small" || got[1] != "budget_min=10" || got[2] != "checks=smoke" || got[3] != "lines=5" {
		t.Fatalf("values: %q", got)
	}
	b, err := os.ReadFile(gh)
	if err != nil || string(b) != out.String() {
		t.Fatalf("GITHUB_OUTPUT = %q (%v), want the stdout text", b, err)
	}
}

func TestCLIUsageErrors(t *testing.T) {
	for _, args := range [][]string{{}, {"--head", "HEAD"}, {"--base", "x", "extra"}, {"--nope"}} {
		var out, errb bytes.Buffer
		if code := Main(args, &out, &errb); code != 2 {
			t.Fatalf("args %v: exit %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Fatalf("args %v: stdout %q, want empty", args, out.String())
		}
	}
	t.Setenv("GITHUB_OUTPUT", "")
	var out, errb bytes.Buffer
	if code := Main([]string{"--base", "x", "--github-output"}, &out, &errb); code != 2 {
		t.Fatalf("missing GITHUB_OUTPUT: exit %d, want 2", code)
	}
}
