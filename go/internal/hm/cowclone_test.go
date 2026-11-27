package hm

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// cowTestRunner: a runner whose git records calls and succeeds, with one task row to clone for.
func cowTestRunner(t *testing.T) (*Runner, *[]string, *Task) {
	r, s := testRunner(t, "owner")
	var calls []string
	r.Tools.Git = func(repo string, args ...string) (int, string, string) {
		calls = append(calls, repo+" :: "+strings.Join(args, " "))
		if len(args) > 1 && args[0] == "remote" && args[1] == "get-url" {
			return 0, "https://github.com/zz-test/www.git", ""
		}
		if args[0] == "rev-parse" {
			return 1, "", ""
		}
		return 0, "", ""
	}
	task := &Task{ID: "T-0001", Goal: "G-001", Project: "www", Base: "main", RepoPath: t.TempDir()}
	_ = s
	return r, &calls, task
}

func TestDarwinWorktreeUsesClonefile(t *testing.T) {
	r, calls, task := cowTestRunner(t)
	old, oldTree := GOOS, cloneTree
	t.Cleanup(func() { GOOS, cloneTree = old, oldTree })
	GOOS = "darwin"
	var copied [2]string
	cloneTree = func(src, dst string) error {
		copied = [2]string{src, dst}
		return os.MkdirAll(filepath.Join(dst, ".git"), 0o755)
	}
	_, _ = r.Clone(task, "factory/t-0001")
	wt := filepath.Join(r.Env.Home, "worktrees", "G-001-T-0001")
	if copied[1] != wt || !strings.HasSuffix(copied[0], filepath.Join("base", "www-main")) {
		t.Fatalf("clonefile copy = %v, want base www-main -> %s", copied, wt)
	}
	for _, c := range *calls {
		if strings.Contains(c, " clone ") && strings.HasSuffix(c, wt) {
			t.Fatalf("full clone into the worktree despite the copy: %s", c)
		}
	}
	joined := strings.Join(*calls, "\n")
	for _, want := range []string{"checkout -q -f --detach origin/main", "update-index -q --refresh", "checkout -q -f -B factory/t-0001 origin/main"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing git %q in\n%s", want, joined)
		}
	}
}

func TestWorktreeFallbackWithoutClonefile(t *testing.T) {
	r, calls, task := cowTestRunner(t)
	old, oldTree := GOOS, cloneTree
	t.Cleanup(func() { GOOS, cloneTree = old, oldTree })
	GOOS = "darwin"
	cloneTree = func(src, dst string) error { return os.ErrPermission }
	_, _ = r.Clone(task, "factory/t-0001")
	wt := filepath.Join(r.Env.Home, "worktrees", "G-001-T-0001")
	full := false
	for _, c := range *calls {
		if strings.Contains(c, " clone ") && strings.HasSuffix(c, wt) {
			full = true
		}
	}
	if !full {
		t.Fatalf("no full clone after clonefile failed:\n%s", strings.Join(*calls, "\n"))
	}
}

func TestLinuxWorktreeNeverUsesClonefile(t *testing.T) {
	r, _, task := cowTestRunner(t)
	old, oldTree := GOOS, cloneTree
	t.Cleanup(func() { GOOS, cloneTree = old, oldTree })
	GOOS = "linux"
	used := false
	cloneTree = func(src, dst string) error { used = true; return nil }
	_, _ = r.Clone(task, "factory/t-0001")
	if used {
		t.Fatal("clonefile used on linux")
	}
}
