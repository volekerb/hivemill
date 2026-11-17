package installer

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"

	ft "factory/go/internal/factorytest"
)

var (
	cmdMu    sync.Mutex
	cmdBuilt = map[string]string{}
	cmdDir   string
)

// TestMain removes the binaries buildCmd made.
func TestMain(m *testing.M) {
	code := m.Run()
	if cmdDir != "" {
		os.RemoveAll(cmdDir)
	}
	os.Exit(code)
}

// buildCmd builds go/cmd/<name> once per test binary and returns the executable path.
func buildCmd(t testing.TB, name string) string {
	t.Helper()
	cmdMu.Lock()
	defer cmdMu.Unlock()
	if p, ok := cmdBuilt[name]; ok {
		return p
	}
	mod, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	if cmdDir == "" {
		if cmdDir, err = os.MkdirTemp("", "installer-bin-"); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(cmdDir, name)
	c := exec.Command("go", "build", "-o", out, "./cmd/"+name)
	c.Dir = mod
	c.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=-mod=readonly", "GOPROXY=off")
	if b, err := c.CombinedOutput(); err != nil {
		t.Fatalf("go build ./cmd/%s: %v\n%s", name, err, b)
	}
	cmdBuilt[name] = out
	return out
}

// hermeticGit runs git in dir with no global/system config and a fixed identity (the Python _gitenv).
func hermeticGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-c", "user.email=t@example.invalid", "-c", "user.name=t", "-c", "commit.gpgsign=false"}, args...)...)
	c.Dir = dir
	c.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid",
		"GIT_AUTHOR_DATE=2026-06-01T12:00:00+00:00", "GIT_COMMITTER_DATE=2026-06-01T12:00:00+00:00")
	b, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, b)
	}
	return string(b)
}

var _ = fmt.Sprintf
