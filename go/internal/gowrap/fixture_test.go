package gowrap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixture is a fake home laid out like the box: a main checkout (.git dir) at factory/, a linked worktree (.git
// file) at factory/worktrees/w, a lib dir, the go caches and a GOROOT-like .local/go. It lives under /var/tmp (or
// /dev/shm), never /tmp: /tmp is a writable root, so a path in it would never be refused.
type fixture struct{ Home, Main, WT, G, L string }

func scratchBase(t *testing.T) string {
	t.Helper()
	for _, b := range []string{"/var/tmp", "/dev/shm"} {
		dir, err := os.MkdirTemp(b, "gowrap-test-")
		if err != nil {
			continue
		}
		t.Cleanup(func() { os.RemoveAll(dir) })
		real, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatal(err)
		}
		return real
	}
	t.Skip("neither /var/tmp nor /dev/shm is writable: the fixture must live outside /tmp")
	return ""
}

func mkdirs(t *testing.T, paths ...string) {
	t.Helper()
	for _, p := range paths {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

// newUnitFixture builds the layout with dummy files; Prepare only stats the toolchain binary.
func newUnitFixture(t *testing.T) *fixture {
	t.Helper()
	h := scratchBase(t)
	fx := &fixture{Home: h, Main: h + "/factory", WT: h + "/factory/worktrees/w", G: h + "/.local/go", L: h + "/.local/lib/factory"}
	mkdirs(t, fx.Main+"/.git", fx.WT, fx.G+"/src/fmt", fx.G+"/src/cmd/gofmt", fx.G+"/pkg", fx.G+"/bin", fx.L,
		h+"/.cache/go/path/pkg/mod", h+"/.cache/go/build")
	write(t, fx.WT+"/.git", "gitdir: "+fx.Main+"/.git/worktrees/w\n")
	write(t, fx.WT+"/go.mod", "module p\ngo 1.24\n")
	write(t, fx.G+"/bin/go", "#!/bin/sh\n")
	write(t, fx.G+"/bin/gofmt", "#!/bin/sh\n")
	return fx
}

func (fx *fixture) sub(s string) string {
	return strings.NewReplacer("{WT}", fx.WT, "{MAIN}", fx.Main, "{G}", fx.G, "{L}", fx.L, "{H}", fx.Home).Replace(s)
}

func (fx *fixture) subAll(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = fx.sub(s)
	}
	return out
}

// probe is one refused command (the T-1498 N/B ids). args are the post-expansion argv after gowrap.
type probeCmd struct {
	cwd    string
	args   []string
	env    []string // extra caller env
	absent []string // paths that must not exist afterwards
}
type probe struct {
	id   string
	cmds []probeCmd
}

const (
	pkgShlex = "./internal/shlex"
	pkgMain  = "./cmd/gowrap"
)

func c(cwd string, absent []string, args ...string) probeCmd {
	return probeCmd{cwd: cwd, args: args, absent: absent}
}

// refusedProbes: every command must be refused (exit 2, "gowrap: refused:"). N4 is a positive probe, tested
// separately.
var refusedProbes = []probe{
	{"N1", []probeCmd{
		c("{WT}", nil, "gofmt", "-w=1", "{G}/src/fmt/print.go"),
		c("{WT}", nil, "gofmt", "-w=t", "{G}/src/fmt/print.go"),
		c("{WT}", nil, "gofmt", "-w", "{L}"),
	}},
	{"N2", []probeCmd{
		c("{WT}", nil, "go", "-C", "/home", "fmt"),
		c("{WT}", nil, "go", "-C", "{G}/src", "fmt"),
		c("{WT}", nil, "go", "fmt", "-C", "{G}/src"),
	}},
	{"N3", []probeCmd{
		{cwd: "{G}/src/cmd/gofmt", args: []string{"go", "test"}, env: []string{"GOFLAGS=-c"}, absent: []string{"{G}/src/cmd/gofmt/gofmt.test"}},
		c("{G}/src/fmt", nil, "go", "build"),
		c("{MAIN}", nil, "go", "build", "./go/cmd/gowrap"),
	}},
	{"N5", []probeCmd{
		c("{WT}", []string{"{L}/x.test"}, "go", "test", "-c", "-run", "-o", "{L}/x.test", pkgShlex),
		c("{WT}", []string{"{L}/x"}, "go", "build", "-tags", "-o", "{L}/x", pkgMain),
		c("{WT}", nil, "go", "test", "-c", pkgShlex, "-args", "-C", "/"),
		c("{WT}", []string{"{L}/y.test"}, "go", "test", "-c", "-o", "{L}/y.test", pkgShlex),
	}},
	{"N6", []probeCmd{
		c("{WT}", nil, "go", "build", "-mod", "mod", pkgMain),
		c("{WT}", nil, "go", "build", "--mod=mod", pkgMain),
		c("{WT}", nil, "go", "build", "-mod=vendor", pkgMain),
	}},
	{"N7", []probeCmd{
		c("{WT}", nil, "go", "build", "-tags", "--", "-pkgdir", "{G}/pkg", pkgMain),
		c("{WT}", nil, "go", "build", "-tags", "--", "-toolexec=/tmp/x", pkgMain),
		c("{WT}", nil, "go", "test", "-run", "--", "-exec", "/tmp/x", pkgShlex),
		c("{WT}", []string{"/etc/evilout"}, "go", "test", "-c", "-run", "--", "-o", "/etc/evilout", pkgShlex),
	}},
	{"N8", []probeCmd{
		c("{G}/src/fmt", []string{"{G}/src/fmt/fmt.test"}, "go", "test", "-cpuprofile", "/dev/null", "."),
		c("{G}/src/fmt", nil, "go", "test", "-memprofile", "/dev/null", "."),
		c("{G}/src/fmt", nil, "go", "test", "-blockprofile", "/dev/null", "."),
		c("{G}/src/fmt", nil, "go", "test", "-mutexprofile", "/dev/null", "."),
		c("{WT}", []string{"{L}/p.out"}, "go", "test", "-cpuprofile", "{L}/p.out", pkgShlex),
	}},
	// N9: bash word splitting of an unquoted $X, already expanded in the real argv.
	{"N9", []probeCmd{
		c("{WT}", nil, "go", "test", pkgShlex, "-exec", "/tmp/x"),
		c("{WT}", nil, "go", "build", "-tags", "-toolexec=/tmp/x", pkgMain),
		c("{WT}", []string{"/etc/evil"}, "go", "test", "-c", "-run", "-o", "/etc/evil", pkgShlex),
	}},
	// N10: zsh globs that matched files named like flags, already expanded.
	{"N10", []probeCmd{
		c("{WT}", []string{"{L}/x.test"}, "go", "test", "-c", ".", "-o", "{L}/x.test"),
		c("{WT}", nil, "go", "test", ".", "-exec", "/tmp/x"),
		c("{WT}", nil, "go", "build", "-pkgdir", "{G}/pkg", "."),
		c("{WT}", nil, "go", "build", "-toolexec=x", "."),
	}},
	{"N11", []probeCmd{c("{WT}", nil, "go", "")}},
	{"N12", []probeCmd{
		c("{WT}", nil, "go", "run", "golang.org/x/tools/cmd/stringer@latest"),
		c("{WT}", nil, "go", "get", "golang.org/x/tools"),
		c("{WT}", nil, "go", "install", "./..."),
		c("{WT}", nil, "go", "mod", "download"),
		c("{WT}", nil, "go", "env", "-w", "GOFLAGS=-mod=mod"),
		c("{WT}", nil, "go", "env", "-u", "GOFLAGS"),
	}},
	{"B1", []probeCmd{
		c("{WT}", nil, "go", "fmt", "{G}/src/fmt"),
		c("{WT}", nil, "go", "fmt", "fmt"),
	}},
	{"B2", []probeCmd{
		c("{WT}", []string{"{L}/c.out"}, "go", "test", "-test.coverprofile={L}/c.out", pkgShlex),
		c("{WT}", nil, "go", "build", "-pkgdir", "{G}/pkg", pkgMain),
		c("{WT}", nil, "go", "build", "--test.pkgdir={G}/pkg", pkgMain),
	}},
	{"B3", []probeCmd{
		c("/", nil, "go", "build", "./..."),
		c("/etc", nil, "go", "fmt"),
		c("{G}/src/fmt", nil, "go", "build"),
		c("{MAIN}", nil, "go", "build", "./go/cmd/gowrap"),
	}},
	{"unknown-tool", []probeCmd{
		c("{WT}", nil, "sh", "-c", "id"),
		c("{WT}", nil, "go1", "version"),
		c("{WT}", nil),
		c("{WT}", nil, "go"),
		c("{WT}", nil, "go", "generate", "./..."),
		c("{WT}", nil, "go", "tool", "cover"),
		c("{WT}", nil, "go", "clean", "-cache"),
		c("{WT}", nil, "go", "work", "init"),
	}},
}
