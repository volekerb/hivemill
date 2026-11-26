package hm

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/gitx"
)

func sh(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=1+t@users.noreply.github.com", "GIT_COMMITTER_NAME=t",
		"GIT_COMMITTER_EMAIL=1+t@users.noreply.github.com", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestAutoRevertEndToEnd: a throwaway bare repo stands in for GitHub, a fake gh for the API. Red main on the merge commit
// of a runner PR -> that run is found -> a revert commit is pushed on a new task branch -> a revert PR is opened, named
// the red-main fix task, and recorded. A second pass changes nothing.
func TestAutoRevertEndToEnd(t *testing.T) {
	t.Setenv("GIT_AUTHOR_EMAIL", "other@example.com")
	t.Setenv("GIT_COMMITTER_EMAIL", "other@example.com")
	t.Setenv("GIT_AUTHOR_NAME", "other")
	t.Setenv("GIT_COMMITTER_NAME", "other")
	r, s := testRunner(t, "")
	tmp := t.TempDir()
	bare := filepath.Join(tmp, "origin.git")
	sh(t, tmp, "init", "-q", "--bare", "-b", "main", bare)
	seed := filepath.Join(tmp, "seed")
	sh(t, tmp, "clone", "-q", bare, seed)
	_ = os.WriteFile(filepath.Join(seed, "a.txt"), []byte("good\n"), 0o644)
	sh(t, seed, "add", "a.txt")
	sh(t, seed, "commit", "-q", "-m", "base")
	_ = os.WriteFile(filepath.Join(seed, "b.txt"), []byte("breaks main\n"), 0o644)
	sh(t, seed, "add", "b.txt")
	sh(t, seed, "commit", "-q", "-m", "T-0001: the merged runner PR")
	bad := sh(t, seed, "rev-parse", "HEAD")
	sh(t, seed, "push", "-q", "origin", "HEAD:main")
	proj := filepath.Join(tmp, "proj")
	sh(t, tmp, "clone", "-q", bare, proj)

	o := gitx.Options{Home: r.Env.Home, Timeout: time.Minute}
	r.Tools.Git = func(repo string, args ...string) (int, string, string) { return gitx.RC(repo, o, args...) }
	var created []string
	r.Tools.GH = func(input string, args ...string) (int, string, string) {
		if len(args) > 1 && args[0] == "pr" && args[1] == "create" {
			created = append(created, strings.Join(args, " "))
			return 0, "https://github.com/zz/test/pull/2\n", ""
		}
		return 0, "", ""
	}
	_ = os.WriteFile(filepath.Join(r.Env.UserHome, ".claude.json"), []byte(`{}`), 0o600)
	_, _ = s.db.Exec(`UPDATE projects SET repo_path=?, gh_repo='zz/test' WHERE id='www'`, proj)
	_, _ = s.db.Exec(`INSERT INTO config(key,value) VALUES('noreply_email','1+t@users.noreply.github.com')`)
	red, _ := json.Marshal(map[string]any{"run": 42, "sha": bad, "url": "", "since": "t"})
	_, _ = s.db.Exec(`INSERT INTO config(key,value) VALUES('red_main:zz/test', ?)`, string(red))
	_ = s.PutRun(&Run{Task: "T-0001", Goal: "G-001", Project: "www", Repo: "zz/test", Base: "main", State: Merged, MergeSHA: bad,
		Merged: "2026-11-24T20:00:00+01:00", PRURL: "https://github.com/zz/test/pull/1"})

	if err := r.RevertCheck(); err != nil || len(created) != 0 {
		t.Fatalf("auto_revert off must do nothing: %v %v", err, created)
	}
	_ = s.SetCfg("auto_revert", "on")
	for i := 0; i < 2; i++ { // the second pass finds the run reverted and does nothing
		if err := r.RevertCheck(); err != nil {
			t.Fatal(err)
		}
	}
	if len(created) != 1 || !strings.Contains(created[0], "--head factory/t-0002") {
		t.Fatalf("revert PRs opened: %v", created)
	}
	orig, _ := s.Run("T-0001")
	if orig.State != Reverted || orig.RevertedBy != "T-0002" {
		t.Errorf("original run: %s by %q", orig.State, orig.RevertedBy)
	}
	rev, _ := s.Run("T-0002")
	if rev == nil || rev.State != Merging || rev.Kind != "revert" {
		t.Fatalf("revert run %+v", rev)
	}
	tip := sh(t, tmp, "--git-dir", bare, "log", "-1", "--format=%s%n%ae", "factory/t-0002")
	if !strings.HasPrefix(tip, "Revert \"T-0001: the merged runner PR\"") || !strings.Contains(tip, "1+t@users.noreply.github.com") {
		t.Errorf("revert commit on the pushed branch: %q", tip)
	}
	if files := sh(t, tmp, "--git-dir", bare, "ls-tree", "--name-only", "factory/t-0002"); strings.Contains(files, "b.txt") {
		t.Errorf("the revert must drop b.txt: %q", files)
	}
	if st, _ := s.RedMain("zz/test"); st["task"] != "T-0002" {
		t.Errorf("red-main fix task %v", st["task"])
	}
	if v, ok, _ := s.LaneVerdict("T-0002", rev.Head); !ok || v != "pass" {
		t.Errorf("revert verdict %q %v", v, ok)
	}
	evs, _ := r.Log.Read()
	if evs[len(evs)-1].Event != "pr.reverted" {
		t.Errorf("last event %s", evs[len(evs)-1].Event)
	}
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM tasks WHERE title LIKE 'Revert T-0001%'`).Scan(&n)
	if n != 1 {
		t.Errorf("%d revert tasks, want 1", n)
	}
}
