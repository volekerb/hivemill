package hm

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// reviewRepo is a task clone with one reviewed commit on a task branch, and an origin/main that can move.
type reviewRepo struct {
	t        *testing.T
	r        *Runner
	dir      string
	reviewed string
}

func newReviewRepo(t *testing.T) *reviewRepo {
	t.Helper()
	r, _ := testRunner(t, "")
	r.Tools.Git = RealTools(r.Env).Git
	p := &reviewRepo{t: t, r: r, dir: t.TempDir()}
	p.git("init", "-q", "-b", "main")
	p.commit("shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\n9\n")
	p.git("update-ref", "refs/remotes/origin/main", "HEAD")
	p.git("checkout", "-q", "-b", "task")
	p.commit("shared.txt", "task\n2\n3\n4\n5\n6\n7\n8\n9\n")
	p.reviewed = p.git("rev-parse", "HEAD")
	return p
}

func (p *reviewRepo) git(args ...string) string {
	p.t.Helper()
	c := exec.Command("git", args...)
	c.Dir = p.dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=zz", "GIT_AUTHOR_EMAIL=zz@users.noreply.github.com",
		"GIT_COMMITTER_NAME=zz", "GIT_COMMITTER_EMAIL=zz@users.noreply.github.com")
	out, err := c.CombinedOutput()
	if err != nil {
		p.t.Fatalf("git %v: %v: %s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (p *reviewRepo) commit(file, text string) {
	p.t.Helper()
	if err := os.WriteFile(filepath.Join(p.dir, file), []byte(text), 0o644); err != nil {
		p.t.Fatal(err)
	}
	p.git("add", file)
	p.git("commit", "-q", "-m", "change "+file)
}

// advance adds a commit to branch (made from origin/main when new) and returns to the task branch.
func (p *reviewRepo) advance(branch, file, text string) {
	p.t.Helper()
	if p.git("branch", "--list", branch) == "" {
		p.git("branch", branch, "origin/main")
	}
	p.git("checkout", "-q", branch)
	p.commit(file, text)
	p.git("checkout", "-q", "task")
}

func (p *reviewRepo) moveBase(file, text string) {
	p.t.Helper()
	p.advance("upstream", file, text)
	p.git("update-ref", "refs/remotes/origin/main", "upstream")
}

func (p *reviewRepo) merge(ref string) { p.git("merge", "-q", "--no-ff", "--no-edit", ref) }

// stale reports whether the gate refuses findings.json written for head.
func (p *reviewRepo) stale(head string) bool {
	p.t.Helper()
	_ = os.MkdirAll(filepath.Join(p.dir, ".hivemill"), 0o755)
	doc := []byte(`{"head": "` + head + `", "findings": []}`)
	if err := os.WriteFile(filepath.Join(p.dir, ".hivemill", "findings.json"), doc, 0o600); err != nil {
		p.t.Fatal(err)
	}
	rec := p.r.Gate(&Run{Task: "T-0001", Project: "www", Repo: "zz-test/www", Base: "main", Worktree: p.dir, Branch: "task"})
	for _, x := range rec.Problems {
		if strings.HasPrefix(x, "review: findings.json is for head") {
			return true
		}
	}
	return false
}

func TestFindingsAcceptBaseMergeOnly(t *testing.T) {
	p := newReviewRepo(t)
	p.moveBase("other.txt", "base\n")
	p.merge("origin/main")
	if p.stale(p.reviewed) {
		t.Error("one merge of main: refused")
	}
	// an installed dependency's own .gitattributes, ignored by git
	p.git("config", "core.excludesFile", filepath.Join(p.dir, ".git", "zz-ignore"))
	for file, text := range map[string]string{".git/zz-ignore": "node_modules/\n", "node_modules/x/.gitattributes": "* text\n"} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(p.dir, file)), 0o755)
		if err := os.WriteFile(filepath.Join(p.dir, file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if p.stale(p.reviewed) {
		t.Error("ignored .gitattributes: refused")
	}
	// a second merge, of a change to a file the task changed too, that git merges without a conflict
	p.moveBase("shared.txt", "1\n2\n3\n4\n5\n6\n7\n8\nbase\n")
	p.moveBase("more.txt", "base\n") // main moves on after the commit the task merges
	p.merge("origin/main~1")
	if p.stale(p.reviewed) {
		t.Error("two merges of main: refused")
	}
}

func TestFindingsRefuseOwnCommitAfter(t *testing.T) {
	p := newReviewRepo(t)
	p.commit("own.txt", "own\n")
	if !p.stale(p.reviewed) {
		t.Error("own commit: accepted")
	}
	p.moveBase("other.txt", "base\n")
	p.merge("origin/main")
	if !p.stale(p.reviewed) {
		t.Error("own commit before a merge: accepted")
	}
	for _, head := range []string{"", "HEAD", "--all", strings.Repeat("a", 40)} {
		if !p.stale(head) {
			t.Errorf("head %q: accepted", head)
		}
	}

	// an edit slipped into the merge commit itself
	p = newReviewRepo(t)
	p.moveBase("other.txt", "base\n")
	p.git("merge", "-q", "--no-ff", "--no-commit", "origin/main")
	p.commit("own.txt", "own\n")
	if !p.stale(p.reviewed) {
		t.Error("edit inside the merge: accepted")
	}

	// a conflict the session resolved
	p = newReviewRepo(t)
	p.moveBase("shared.txt", "base\n2\n3\n4\n5\n6\n7\n8\n9\n")
	p.git("merge", "-q", "--no-ff", "--no-edit", "-X", "ours", "origin/main")
	if !p.stale(p.reviewed) {
		t.Error("resolved conflict: accepted")
	}
}

func TestFindingsRefuseMergeOfNonBase(t *testing.T) {
	p := newReviewRepo(t)
	p.advance("side", "side.txt", "side\n")
	p.merge("side")
	if !p.stale(p.reviewed) {
		t.Error("merge of another branch: accepted")
	}
	p.moveBase("other.txt", "base\n")
	p.merge("origin/main")
	if !p.stale(p.reviewed) {
		t.Error("merge of another branch, then of main: accepted")
	}
}

// The clone is the session's: nothing in it may make another commit pass for main, or another tree for a clean merge.
func TestFindingsRefuseCloneTricks(t *testing.T) {
	// a local branch that shadows the short name origin/main
	p := newReviewRepo(t)
	p.advance("side", "side.txt", "side\n")
	p.git("branch", "origin/main", "side")
	p.merge("side")
	if !p.stale(p.reviewed) {
		t.Error("branch named origin/main: accepted")
	}

	// a replace ref that shows an edited merge as the clean one
	p = newReviewRepo(t)
	p.moveBase("other.txt", "base\n")
	p.merge("origin/main")
	clean := p.git("rev-parse", "HEAD")
	p.commit("own.txt", "own\n")
	edited := p.git("commit-tree", "HEAD^{tree}", "-p", p.reviewed, "-p", "origin/main", "-m", "merge")
	p.git("reset", "-q", "--hard", edited)
	p.git("replace", edited, clean)
	if !p.stale(p.reviewed) {
		t.Error("replace ref: accepted")
	}

	// local merge rules that join both sides of a conflict
	for name, file := range map[string]string{"untracked .gitattributes": ".gitattributes", "info/attributes": ".git/info/attributes"} {
		p = newReviewRepo(t)
		if err := os.MkdirAll(filepath.Join(p.dir, ".git", "info"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(p.dir, file), []byte("*.txt merge=union\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		p.moveBase("shared.txt", "base\n2\n3\n4\n5\n6\n7\n8\n9\n")
		p.merge("origin/main")
		if !p.stale(p.reviewed) {
			t.Errorf("%s: accepted", name)
		}
	}
	p = newReviewRepo(t)
	p.moveBase("other.txt", "base\n")
	p.merge("origin/main")
	p.git("config", "merge.default", "union")
	if !p.stale(p.reviewed) {
		t.Error("merge.default: accepted")
	}
}

func TestTaskPromptMergesBeforeReview(t *testing.T) {
	asset, err := Asset("task.md")
	if err != nil {
		t.Fatal(err)
	}
	role, err := os.ReadFile("../../../roles/hivemill/task.md")
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"assets/task.md": string(asset), "roles/hivemill/task.md": string(role)} {
		merge := strings.Index(s, "\n4. Commit with your noreply identity. Run `git fetch origin && git merge origin/main`")
		review := strings.Index(s, "\n5. Run `/code-review`")
		if merge < 0 || review < merge || strings.Count(s, "git merge origin/main") != 1 {
			t.Errorf("%s: merge of main is not step 4, before /code-review", name)
		}
	}
}
