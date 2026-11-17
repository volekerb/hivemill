package gowrap

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func prep(t *testing.T, fx *fixture, cwd string, env []string, args ...string) (*Plan, string, error) {
	t.Helper()
	var buf bytes.Buffer
	d := &Deps{Home: fx.Home, Cwd: cwd, Environ: env, Stderr: &buf}
	p, err := Prepare(args, d)
	return p, buf.String(), err
}

func isRefusal(err error) bool {
	var r *refusal
	return errors.As(err, &r)
}

// TestRefusedArgv: one row per probe of the T-1498 bypass list; every command must be refused.
func TestRefusedArgv(t *testing.T) {
	fx := newUnitFixture(t)
	for _, pr := range refusedProbes {
		for i, cm := range pr.cmds {
			cwd := fx.sub(cm.cwd)
			if _, err := os.Stat(cwd); err != nil {
				t.Fatalf("%s[%d]: fixture lacks cwd %s", pr.id, i, cwd)
			}
			_, _, err := prep(t, fx, cwd, cm.env, fx.subAll(cm.args)...)
			if !isRefusal(err) {
				t.Errorf("%s[%d] %v: want a refusal, got %v", pr.id, i, cm.args, err)
			}
		}
	}
}

// TestAllowedArgv: legitimate use passes, including values that merely look like banned flags' neighbours.
func TestAllowedArgv(t *testing.T) {
	fx := newUnitFixture(t)
	tmp, err := os.MkdirTemp("/tmp", "gowrap-allowed-")
	if err != nil {
		t.Skip("/tmp not writable")
	}
	defer os.RemoveAll(tmp)
	cases := []struct {
		name string
		cwd  string
		args []string
	}{
		{"test with -run and -tags", "{WT}", []string{"go", "test", "-count=1", "-run", "TestX|TestY", "-tags", "netgo", pkgShlex}},
		{"build -o in /tmp", "{WT}", []string{"go", "build", "-o", tmp + "/x", pkgMain}},
		{"build -o new file in worktree", "{WT}", []string{"go", "build", "-o", "out.bin", pkgMain}},
		{"-mod=readonly", "{WT}", []string{"go", "build", "-mod=readonly", pkgMain}},
		{"-mod readonly", "{WT}", []string{"go", "build", "-mod", "readonly", pkgMain}},
		{"vet", "{WT}", []string{"go", "vet", pkgMain}},
		{"go fmt relative", "{WT}", []string{"go", "fmt", "./..."}},
		{"go fmt dot", "{WT}", []string{"go", "fmt", "."}},
		{"go fmt abs in worktree", "{WT}", []string{"go", "fmt", "{WT}/cmd"}},
		{"gofmt -l", "{WT}", []string{"gofmt", "-l", "./cmd"}},
		{"gofmt -w in worktree", "{WT}", []string{"gofmt", "-w", "./a.go"}},
		{"gofmt -d on a path outside is read-only", "{WT}", []string{"gofmt", "-d", "{G}/src/fmt/print.go"}},
		{"-C into /tmp", "{WT}", []string{"go", "list", "-C", tmp, "./..."}},
		{"cpuprofile in /tmp", "{WT}", []string{"go", "test", "-cpuprofile", tmp + "/p.out", pkgShlex}},
		{"version from /", "/", []string{"go", "version"}},
		{"env from /etc", "/etc", []string{"go", "env", "GOFLAGS"}},
		{"run in /tmp", tmp, []string{"go", "run", "."}},
		{"test args after -args", "{WT}", []string{"go", "test", pkgShlex, "-args", "-test.v"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _, err := prep(t, fx, fx.sub(tc.cwd), nil, fx.subAll(tc.args)...)
			if err != nil {
				t.Fatalf("want allowed, got %v", err)
			}
			if p.Path != fx.G+"/bin/"+tc.args[0] || p.Argv[0] != p.Path {
				t.Errorf("exec target %q argv %v", p.Path, p.Argv)
			}
		})
	}
}

func TestRefusalMessage(t *testing.T) {
	fx := newUnitFixture(t)
	var buf bytes.Buffer
	code := Run([]string{"sh", "-c", "id"}, Deps{Home: fx.Home, Cwd: fx.WT, Environ: nil, Stderr: &buf,
		ABI:  func() (int, error) { return 7, nil },
		Exec: func(string, []string, []string) error { t.Fatal("exec reached"); return nil }})
	if code != 2 || !strings.HasPrefix(buf.String(), "gowrap: refused:") {
		t.Fatalf("code %d stderr %q", code, buf.String())
	}
}

// TestLandlockUnavailable: ABI 0 fails closed with exit 126, before anything is exec'd.
func TestLandlockUnavailable(t *testing.T) {
	fx := newUnitFixture(t)
	var buf bytes.Buffer
	code := Run([]string{"go", "version"}, Deps{Home: fx.Home, Cwd: fx.WT, Environ: []string{}, Stderr: &buf,
		ABI:   func() (int, error) { return 0, nil },
		Apply: func(int, []string, []string) error { t.Fatal("apply reached"); return nil },
		Exec:  func(string, []string, []string) error { t.Fatal("exec reached"); return nil }})
	if code != 126 || buf.String() != "gowrap: landlock unavailable; refusing\n" {
		t.Fatalf("code %d stderr %q", code, buf.String())
	}
}

func TestToolchainMissing(t *testing.T) {
	fx := newUnitFixture(t)
	os.Remove(fx.G + "/bin/go")
	var buf bytes.Buffer
	code := Run([]string{"go", "version"}, Deps{Home: fx.Home, Cwd: fx.WT, Environ: []string{}, Stderr: &buf,
		ABI: func() (int, error) { return 7, nil }})
	if code != 127 || buf.String() != "gowrap: go toolchain missing\n" {
		t.Fatalf("code %d stderr %q", code, buf.String())
	}
}

// TestRunApplies: a good command applies Landlock with the writable roots and then execs the pinned binary.
func TestRunApplies(t *testing.T) {
	fx := newUnitFixture(t)
	var gotDirs, gotFiles []string
	var gotPath string
	var gotEnv []string
	var buf bytes.Buffer
	order := ""
	code := Run([]string{"go", "version"}, Deps{Home: fx.Home, Cwd: fx.WT + "/", Environ: []string{"PATH=/evil", "GOFLAGS=-toolexec=x", "KEEP=1"}, Stderr: &buf,
		ABI: func() (int, error) { return 5, nil },
		Apply: func(abi int, dirs, files []string) error {
			order += "apply;"
			if abi != 5 {
				t.Errorf("abi %d", abi)
			}
			gotDirs, gotFiles = dirs, files
			return nil
		},
		Exec: func(p string, argv, env []string) error {
			order += "exec;"
			gotPath, gotEnv = p, env
			return errors.New("stub")
		}})
	if order != "apply;exec;" {
		t.Fatalf("order %q", order)
	}
	if code != 126 { // the stub exec "failed"
		t.Fatalf("code %d", code)
	}
	if gotPath != fx.G+"/bin/go" {
		t.Errorf("path %s", gotPath)
	}
	want := map[string]bool{fx.WT: true, "/tmp": true, fx.Home + "/.cache/go/worker-build": true, fx.Home + "/.cache/go/tmp": true}
	for _, d := range gotDirs {
		delete(want, d)
	}
	if len(want) != 0 || len(gotDirs) != 4 {
		t.Errorf("write roots %v missing %v", gotDirs, want)
	}
	for _, d := range gotDirs {
		if strings.Contains(d, "/.local/") || d == fx.Main {
			t.Errorf("protected dir writable: %s", d)
		}
	}
	if len(gotFiles) != 1 || gotFiles[0] != "/dev/null" {
		t.Errorf("files %v", gotFiles)
	}
	if !strings.Contains(buf.String(), "gowrap: ignoring caller GOFLAGS") {
		t.Errorf("stderr %q", buf.String())
	}
	if !hasEnv(gotEnv, "KEEP=1") || hasEnv(gotEnv, "PATH=/evil") || !hasEnv(gotEnv, "GOFLAGS=-mod=readonly") {
		t.Errorf("env %v", gotEnv)
	}
}

func hasEnv(env []string, kv string) bool {
	for _, e := range env {
		if e == kv {
			return true
		}
	}
	return false
}

func TestBuildEnv(t *testing.T) {
	caller := []string{
		"HOME=/wrong", "PATH=/evil", "KEEP=1", "GOPATH=/x", "GOROOT=/x", "GOFLAGS=-mod=mod", "GOPROXY=direct", "GOTOOLCHAIN=auto",
		"GOMODCACHE=/x", "GOCACHE=/x", "CGO_ENABLED=1", "CGO_CFLAGS=-x", "CC=/tmp/cc", "CXX=x", "FC=x", "AR=x", "GCCGO=x",
		"PKG_CONFIG_PATH=x", "LD_PRELOAD=x", "LD_LIBRARY_PATH=x", "LD_AUDIT=x", "BASH_ENV=x", "ENV=x", "CCACHE=keep", "GOBIN=x",
	}
	env, ignored := buildEnv(caller, "/h")
	if !ignored {
		t.Error("GOFLAGS not reported as ignored")
	}
	got := map[string]string{}
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if _, dup := got[k]; dup {
			t.Errorf("duplicate %s", k)
		}
		got[k] = v
	}
	want := map[string]string{
		"HOME": "/h", "PATH": "/h/.local/go/bin:/usr/bin:/bin", "KEEP": "1", "CCACHE": "keep",
		"GOTOOLCHAIN": "local", "GOENV": "off", "GOWORK": "off", "GOPROXY": "off", "GOSUMDB": "off",
		"GOFLAGS": "-mod=readonly", "CGO_ENABLED": "0", "GOMODCACHE": "/h/.cache/go/path/pkg/mod",
		"GOCACHE": "/h/.cache/go/worker-build", "GOTMPDIR": "/h/.cache/go/tmp",
	}
	if len(got) != len(want) {
		t.Errorf("env %v", got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s=%q want %q", k, got[k], v)
		}
	}
	if _, ign := buildEnv([]string{"GOFLAGS="}, "/h"); ign {
		t.Error("empty GOFLAGS reported as ignored")
	}
}

func TestWorktreeRoots(t *testing.T) {
	fx := newUnitFixture(t)
	// A nested dir resolves to the worktree root; a symlink out of the worktree does not widen it.
	mkdirs(t, fx.WT+"/a/b")
	if err := os.Symlink(fx.G, fx.WT+"/link"); err != nil {
		t.Fatal(err)
	}
	p, _, err := prep(t, fx, fx.WT+"/a/b", nil, "go", "build", ".")
	if err != nil || !hasDir(p.WriteDir, fx.WT) {
		t.Fatalf("nested cwd: %v %v", p, err)
	}
	if _, _, err := prep(t, fx, fx.WT+"/link/src/fmt", nil, "go", "build"); !isRefusal(err) {
		t.Errorf("cwd through a symlink out of the worktree: %v", err)
	}
	if _, _, err := prep(t, fx, fx.WT, nil, "go", "build", "-o", "link/pkg/x", "."); !isRefusal(err) {
		t.Errorf("-o through a symlink out of the worktree: %v", err)
	}
	if _, _, err := prep(t, fx, fx.WT, nil, "go", "build", "-o", "link/../../x", "."); !isRefusal(err) {
		t.Errorf("-o through link/..: %v", err)
	}
	// A worktree outside ~/factory/worktrees|run and /tmp is not a root.
	other := fx.Home + "/elsewhere"
	mkdirs(t, other)
	write(t, other+"/.git", "gitdir: x\n")
	if _, _, err := prep(t, fx, other, nil, "go", "build"); !isRefusal(err) {
		t.Errorf("worktree under an unlisted parent: %v", err)
	}
	// ~/factory/run counts.
	run := fx.Home + "/factory/run/job"
	mkdirs(t, run)
	write(t, run+"/.git", "gitdir: x\n")
	if _, _, err := prep(t, fx, run, nil, "go", "build"); err != nil {
		t.Errorf("worktree under factory/run: %v", err)
	}
}

func hasDir(l []string, d string) bool {
	for _, x := range l {
		if x == d {
			return true
		}
	}
	return false
}

func TestCacheDirsCreated(t *testing.T) {
	fx := newUnitFixture(t)
	if _, _, err := prep(t, fx, fx.WT, nil, "go", "version"); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"worker-build", "tmp"} {
		if st, err := os.Stat(fx.Home + "/.cache/go/" + d); err != nil || !st.IsDir() {
			t.Errorf("%s not created: %v", d, err)
		}
	}
	if _, err := os.Stat(fx.Home + "/.cache/go/build"); err != nil {
		t.Errorf("gate cache dir touched: %v", err)
	}
}
