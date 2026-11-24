//go:build linux

package gowrap

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type e2e struct {
	fx  *fixture
	bin string
}

func cleanEnv(extra ...string) []string {
	env := []string{"PATH=/usr/bin:/bin"}
	return append(env, extra...)
}

// newE2E builds the real wrapper (with the test-only home override) and lays out a fake home with a linked
// worktree made by git, the real GOROOT symlinked at .local/go, and the caches. It skips, naming the missing
// piece, when git, the GOROOT or Landlock is not there.
func newE2E(t *testing.T) *e2e {
	t.Helper()
	if abi, _ := landlockABI(); abi < 1 {
		t.Skip("landlock unavailable on this host")
	}
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not found")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not found in PATH")
	}
	realGo, err := filepath.EvalSymlinks(goBin)
	if err != nil {
		t.Skip("cannot resolve go binary: " + err.Error())
	}
	goroot := filepath.Dir(filepath.Dir(realGo))
	if _, err := os.Stat(filepath.Join(goroot, "src", "fmt")); err != nil {
		t.Skip("GOROOT " + goroot + " has no src/fmt")
	}
	h := scratchBase(t)
	fx := &fixture{Home: h, Main: h + "/factory", WT: h + "/factory/worktrees/w", G: h + "/.local/go", L: h + "/.local/lib/factory"}
	mkdirs(t, fx.Main, fx.L, h+"/.local", h+"/.cache/go/path/pkg/mod", h+"/.cache/go/build", fx.Main+"/worktrees")
	if err := os.Symlink(goroot, fx.G); err != nil {
		t.Fatal(err)
	}
	git := func(dir string, args ...string) {
		cmd := exec.Command(gitBin, append([]string{"-c", "user.email=t@t", "-c", "user.name=t", "-c", "init.defaultBranch=main"}, args...)...)
		cmd.Dir = dir
		cmd.Env = cleanEnv("HOME=" + h)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git(fx.Main, "init", "-q")
	mkdirs(t, fx.Main+"/tiny/internal/shlex", fx.Main+"/tiny/cmd/gowrap")
	write(t, fx.Main+"/tiny/go.mod", "module p\n\ngo 1.24\n")
	write(t, fx.Main+"/tiny/internal/shlex/s.go", "package shlex\n\nfunc F() int { return 1 }\n")
	write(t, fx.Main+"/tiny/internal/shlex/s_test.go", "package shlex\n\nimport \"testing\"\n\nfunc TestX(t *testing.T) {\n\tif F() != 1 {\n\t\tt.Fatal(1)\n\t}\n}\n")
	write(t, fx.Main+"/tiny/cmd/gowrap/main.go", "package main\n\nfunc main() {}\n")
	git(fx.Main, "add", "tiny")
	git(fx.Main, "commit", "-q", "-m", "init")
	git(fx.Main, "worktree", "add", "-q", "-b", "w", fx.WT)
	// The worktree is the module root, like go/ in the repo: move tiny/ up.
	for _, f := range []string{"go.mod", "internal", "cmd"} {
		if err := os.Rename(fx.WT+"/tiny/"+f, fx.WT+"/"+f); err != nil {
			t.Fatal(err)
		}
	}

	// Build the wrapper with the home override.
	_, thisFile, _, _ := runtime.Caller(0)
	modRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	out := t.TempDir() + "/gowrap"
	build := exec.Command(goBin, "build", "-ldflags=-X factory/go/internal/gowrap.homeOverride="+h, "-o", out, "./cmd/gowrap")
	build.Dir = modRoot
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build gowrap: %v\n%s", err, b)
	}
	return &e2e{fx: fx, bin: out}
}

type result struct {
	code           int
	stdout, stderr string
}

func (e *e2e) run(t *testing.T, cwd string, extraEnv []string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, e.bin, args...)
	cmd.Dir = cwd
	cmd.Env = cleanEnv(append([]string{"HOME=" + e.fx.Home}, extraEnv...)...)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("run %v: %v", args, err)
	}
	return result{code, so.String(), se.String()}
}

func (e *e2e) mustRefuse(t *testing.T, cwd string, env []string, args ...string) {
	t.Helper()
	r := e.run(t, cwd, env, args...)
	if r.code != 2 || !strings.HasPrefix(r.stderr, "gowrap: refused:") {
		t.Errorf("%v in %s: want exit 2 + refused, got %d %q", args, cwd, r.code, r.stderr)
	}
}

func TestProbes(t *testing.T) {
	e := newE2E(t)
	fx := e.fx
	for _, pr := range refusedProbes {
		t.Run(pr.id, func(t *testing.T) {
			for _, cm := range pr.cmds {
				e.mustRefuse(t, fx.sub(cm.cwd), cm.env, fx.subAll(cm.args)...)
				for _, a := range cm.absent {
					if _, err := os.Lstat(fx.sub(a)); err == nil {
						t.Errorf("%s exists after %v", a, cm.args)
					}
				}
			}
		})
	}

	t.Run("N11-no-hang", func(t *testing.T) {
		start := time.Now()
		r := e.run(t, fx.WT, nil, "go", "")
		if r.code != 2 || time.Since(start) > 5*time.Second {
			t.Errorf("go '' -> %d after %v", r.code, time.Since(start))
		}
	})

	t.Run("N4", func(t *testing.T) {
		r := e.run(t, fx.WT, []string{
			"GOMODCACHE=" + fx.L + "/go/mod", "GOCACHE=" + fx.L + "/go/cache", "GOPROXY=direct", "GOTOOLCHAIN=auto",
			"GOFLAGS=-toolexec=/tmp/x", "CGO_ENABLED=1", "CC=/tmp/cc",
		}, "go", "env", "GOMODCACHE", "GOCACHE", "GOPROXY", "GOTOOLCHAIN", "GOFLAGS", "CGO_ENABLED", "CC")
		lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
		want := []string{fx.Home + "/.cache/go/path/pkg/mod", fx.Home + "/.cache/go/worker-build", "off", "local", "-mod=readonly", "0"}
		if r.code != 0 || len(lines) != 7 {
			t.Fatalf("exit %d out %q err %q", r.code, r.stdout, r.stderr)
		}
		for i, w := range want {
			if lines[i] != w {
				t.Errorf("line %d = %q want %q", i, lines[i], w)
			}
		}
		if lines[6] == "/tmp/cc" {
			t.Error("caller CC reached go")
		}
		if !strings.Contains(r.stderr, "gowrap: ignoring caller GOFLAGS") {
			t.Errorf("stderr %q", r.stderr)
		}
		// A build with a caller proxy and mod cache must not touch the caller's cache dir.
		r = e.run(t, fx.WT, []string{"GOMODCACHE=" + fx.L + "/go/mod", "GOPROXY=https://proxy.golang.org"}, "go", "build", "./cmd/gowrap")
		if r.code != 0 {
			t.Errorf("build: %d %s", r.code, r.stderr)
		}
		if _, err := os.Stat(fx.L + "/go"); err == nil {
			t.Error("caller GOMODCACHE dir was created")
		}
	})

	t.Run("allowed-use", func(t *testing.T) {
		tmp, err := os.MkdirTemp("/tmp", "gowrap-e2e-")
		if err != nil {
			t.Skip("/tmp not writable")
		}
		defer os.RemoveAll(tmp)
		for _, c := range [][]string{
			{"go", "version"}, {"go", "vet", "./cmd/gowrap"}, {"go", "test", "-count=1", "-run", "TestX|TestY", "-tags", "netgo", "./internal/shlex"},
			{"gofmt", "-l", "./cmd"}, {"go", "build", "-o", tmp + "/x", "./cmd/gowrap"},
		} {
			r := e.run(t, fx.WT, nil, c...)
			if r.code != 0 {
				t.Errorf("%v: exit %d\n%s%s", c, r.code, r.stdout, r.stderr)
			}
		}
		if _, err := os.Stat(tmp + "/x"); err != nil {
			t.Errorf("build output missing: %v", err)
		}
		if r := e.run(t, "/etc", nil, "go", "version"); r.code != 0 || !strings.HasPrefix(r.stdout, "go version go") {
			t.Errorf("go version from /etc: %d %q %q", r.code, r.stdout, r.stderr)
		}
	})

	// Landlock blocks worker code: a test that writes into each protected dir must get a permission error.
	mkdirs(t, fx.WT+"/probe")
	write(t, fx.WT+"/probe/p.go", "package probe\n")
	write(t, fx.WT+"/probe/probe_test.go", "package probe\n\nimport (\n\t\"os\"\n\t\"testing\"\n)\n\nfunc TestW(t *testing.T) {\n\tp := os.Getenv(\"PROBE\")\n\tif err := os.WriteFile(p, nil, 0o600); err == nil {\n\t\tt.Fatal(\"WROTE \" + p)\n\t} else {\n\t\tt.Log(err)\n\t}\n}\n")
	for name, dir := range map[string]string{
		"lib-factory": fx.L, "main-checkout": fx.Main, "goroot": fx.G, "mod-cache": fx.Home + "/.cache/go/path/pkg/mod",
		"gate-build-cache": fx.Home + "/.cache/go/build", "claude-dir": fx.Home,
	} {
		t.Run("EACCES/"+name, func(t *testing.T) {
			probe := dir + "/gowrap-probe"
			defer os.Remove(probe)
			r := e.run(t, fx.WT, []string{"PROBE=" + probe}, "go", "test", "-v", "-count=1", "./probe")
			if r.code != 0 || !strings.Contains(r.stdout, "permission denied") {
				t.Errorf("want a PASS with permission denied: exit %d\n%s%s", r.code, r.stdout, r.stderr)
			}
			if _, err := os.Stat(probe); err == nil {
				t.Errorf("%s was written", probe)
			}
		})
	}
	t.Run("EACCES/writable-roots-still-work", func(t *testing.T) {
		tmp, err := os.MkdirTemp("/tmp", "gowrap-e2e-")
		if err != nil {
			t.Skip("/tmp not writable")
		}
		defer os.RemoveAll(tmp)
		for _, p := range []string{tmp + "/ok", fx.WT + "/probe/ok"} {
			r := e.run(t, fx.WT, []string{"PROBE=" + p}, "go", "test", "-v", "-count=1", "./probe")
			if r.code == 0 || !strings.Contains(r.stdout, "WROTE") {
				t.Errorf("%s should be writable: exit %d\n%s", p, r.code, r.stdout)
			}
		}
	})
}
