package guard

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ptHermeticGit points git at nothing outside the test (C6): no user or system config, a fixed identity.
func ptHermeticGit(t *testing.T) {
	t.Helper()
	for k, v := range map[string]string{
		"GIT_CONFIG_GLOBAL": os.DevNull, "GIT_CONFIG_SYSTEM": os.DevNull, "GIT_CONFIG_NOSYSTEM": "1",
		"GIT_AUTHOR_NAME": "t", "GIT_AUTHOR_EMAIL": "t@example.com", "GIT_COMMITTER_NAME": "t", "GIT_COMMITTER_EMAIL": "t@example.com",
	} {
		t.Setenv(k, v)
	}
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// ptRepo is a temp git repo with one commit, origin set to url and refs/remotes/origin/main at that commit, plus
// the branches factory/t-1 and factory/t-9. The tests run from inside it (t.Chdir).
func ptRepo(t *testing.T, url string) string {
	t.Helper()
	ptHermeticGit(t)
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-q", "-b", "main")
	runGit(t, dir, "remote", "add", "origin", url)
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "first")
	runGit(t, dir, "update-ref", "refs/remotes/origin/main", "HEAD")
	runGit(t, dir, "branch", "factory/t-1")
	runGit(t, dir, "branch", "factory/t-9")
	t.Chdir(dir)
	return dir
}

// ptHook makes the binary know the checkout it belongs to, as the shim's --py-hook does.
func ptHook(t *testing.T) {
	t.Helper()
	p, err := filepath.Abs("../../../roles/hooks/worker-guard.py")
	if err != nil {
		t.Fatal(err)
	}
	SetPyHook(p)
	t.Cleanup(func() { SetPyHook("") })
}
