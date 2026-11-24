package sandbox1

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// ---- Go toolchain in the worker sandbox (T-1395): offline go/gofmt/vet/test -race inside --------------------------

var wbGoKeys = []string{"GOROOT", "GOCACHE", "GOTMPDIR", "GOMODCACHE", "GOPATH", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOFLAGS", "GOENV", "GOWORK"}

// wbGoBox is a fake home with tokens, a probe in place of claude and a fake <factory> with one task dir.
type wbGoBox struct {
	t                            *testing.T
	base, home, fh, runtime, cwd string
	claude                       string
}

func wbNewGoBox(t *testing.T) *wbGoBox {
	b := &wbGoBox{t: t, base: wbBase(t, "zz-test-wbgo-")}
	b.home, b.runtime = b.base+"/home", b.base+"/run"
	b.fh = b.home + "/factory"
	wbMust(t, os.Mkdir(b.runtime, 0o755))
	wbMust(t, os.MkdirAll(b.fh+"/db", 0o755))
	b.cwd = b.fh + "/worktrees/G-001-T-0001"
	wbMust(t, os.MkdirAll(b.cwd, 0o755))
	real := b.home + "/.local/share/claude/versions/1"
	wbLink(t, real)
	b.claude = b.home + "/.local/bin/claude"
	wbMust(t, os.MkdirAll(filepath.Dir(b.claude), 0o755))
	wbMust(t, os.Symlink(real, b.claude))
	for _, name := range []string{"worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"} {
		wbPut(t, b.home+"/.config/factory/"+name, "tok-"+name+"\n", 0o600)
	}
	return b
}

func (b *wbGoBox) seams() sandbox.Opts {
	return sandbox.Opts{UID: os.Getuid(), GID: os.Getgid(), Runtime: b.runtime}
}

func (b *wbGoBox) prepareWith(mod func(*sandbox.PrepareAgentArgs)) (string, error) {
	id := wbIdent
	a := sandbox.PrepareAgentArgs{Agent: "coder-t-1", Role: "coder", Cwd: b.cwd, FactoryHome: b.fh, DBDir: b.fh + "/db", RealClaude: b.claude,
		RwCwd: true, Home: b.home, Runtime: b.runtime, Identity: &id, GhSlugs: wbSlugs, Seams: b.seams()}
	if mod != nil {
		mod(&a)
	}
	return sandbox.PrepareAgent(a)
}

func (b *wbGoBox) prepare(mod func(*sandbox.PrepareAgentArgs)) string {
	b.t.Helper()
	d, err := b.prepareWith(mod)
	wbMust(b.t, err)
	return d
}

func (b *wbGoBox) run(agentDir string, ops []map[string]any, agent, role, cwd string) []wbRes {
	b.t.Helper()
	if cwd == "" {
		cwd = b.cwd
	}
	js, err := json.Marshal(ops)
	wbMust(b.t, err)
	env := []string{"PATH=" + b.home + "/.local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm", "FACTORY_AGENT=" + agent, "FACTORY_ROLE=" + role,
		"FACTORY_HOME=" + b.fh}
	r := wbRunCmd(b.t, exec.Command(agentDir+"/bin/claude", string(js)), cwd, env, "", nil)
	if r.Code != 0 || r.Res == nil {
		b.t.Fatalf("probe: exit %d\n%s%s", r.Code, r.Stdout, r.Stderr)
	}
	return r.Res
}

// workerCfg is workerbox.make_cfg(agent_dir=base/agent, ...) with the given Go info.
func (b *wbGoBox) cfg(g *sandbox.GoInfo) (*sandbox.Cfg, string) {
	b.t.Helper()
	agentDir := b.base + "/agent"
	c, err := sandbox.WorkerCfg(sandbox.WorkerCfgArgs{AgentDir: agentDir, Cwd: b.cwd, FactoryHome: b.fh, DBDir: b.fh + "/db", RealClaude: b.claude,
		RwCwd: true, Home: b.home, Runtime: b.runtime, Go: g, Seams: b.seams()})
	wbMust(b.t, err)
	return c, agentDir
}

// wbFakeGo makes a fake GOROOT whose bin/go is a tiny script.
func wbFakeGo(t testing.TB, root string) string {
	t.Helper()
	wbPut(t, root+"/bin/go", "#!/bin/sh\necho fake go\n", 0o755)
	return root
}

// goWithMod makes the default fake toolchain and module cache of the box and returns its Go info, root and cache.
func (b *wbGoBox) goInfo() (*sandbox.GoInfo, string, string) {
	root := wbRealpath(b.t, wbFakeGo(b.t, b.home+"/.local/go"))
	mod := b.home + "/.cache/go/path/pkg/mod"
	wbMust(b.t, os.MkdirAll(mod, 0o755))
	return sandbox.GoPaths(b.home, b.base+"/agent", "", ""), root, wbRealpath(b.t, mod)
}

func TestWorkerboxGoUnitGorootInRoAndNeverInRwAndAgentGoInRwAndModcacheInRo(t *testing.T) {
	b := wbNewGoBox(t)
	g, root, mod := b.goInfo()
	c, agentDir := b.cfg(g)
	for _, p := range []string{root, mod} {
		if !wbIn(c.Ro, p) || wbIn(c.Rw, p) {
			t.Errorf("%s: ro=%v rw=%v", p, c.Ro, c.Rw)
		}
	}
	if !wbIn(c.Rw, agentDir+"/go") || !c.Net {
		t.Errorf("rw=%v net=%v", c.Rw, c.Net)
	}
}

func TestWorkerboxGoUnitOnlyNewRwPathIsTheAgentGoDir(t *testing.T) {
	b := wbNewGoBox(t)
	g, _, _ := b.goInfo()
	with, agentDir := b.cfg(g)
	without, _ := b.cfg(nil)
	var extra []string
	for _, p := range with.Rw {
		if !wbIn(without.Rw, p) {
			extra = append(extra, p)
		}
	}
	wbEq(t, "new rw paths", extra, []string{agentDir + "/go"})
	wbEq(t, "hide_dirs", with.HideDirs, without.HideDirs)
	wbEq(t, "masks", with.Masks, without.Masks)
}

func TestWorkerboxGoUnitMissingBinGoGivesNoneAndACfgEqualToTheNoGoCfg(t *testing.T) {
	b := wbNewGoBox(t)
	wbMust(t, os.MkdirAll(b.home+"/.local/go/bin", 0o755))
	g := sandbox.GoPaths(b.home, b.base+"/agent", "", "")
	if g != nil {
		t.Fatalf("go = %+v", g)
	}
	a, _ := b.cfg(g)
	n, _ := b.cfg(nil)
	if !reflect.DeepEqual(a, n) {
		t.Errorf("cfgs differ:\n%+v\n%+v", a, n)
	}
}

func TestWorkerboxGoUnitNonExecutableBinGoGivesNone(t *testing.T) {
	b := wbNewGoBox(t)
	wbPut(t, b.home+"/.local/go/bin/go", "x", 0o644)
	if g := sandbox.GoPaths(b.home, b.base+"/agent", "", ""); g != nil {
		t.Fatalf("go = %+v", g)
	}
}

func TestWorkerboxGoUnitSymlinkedGoRootIsBoundByItsRealpath(t *testing.T) {
	b := wbNewGoBox(t)
	real := wbRealpath(t, wbFakeGo(t, b.home+"/opt/setup-go/1.27"))
	wbMust(t, os.MkdirAll(b.home+"/.local", 0o755))
	wbMust(t, os.Symlink(real, b.home+"/.local/go"))
	g := sandbox.GoPaths(b.home, b.base+"/agent", "", "")
	wbEq(t, "root", g.Root, real)
	c, _ := b.cfg(g)
	if !wbIn(c.Ro, real) || wbIn(c.Ro, b.home+"/.local/go") || wbIn(c.Rw, real) {
		t.Errorf("ro=%v rw=%v", c.Ro, c.Rw)
	}
}

func TestWorkerboxGoUnitGoRootOutsideHomeIsNotBoundAtAll(t *testing.T) {
	b := wbNewGoBox(t)
	real := wbRealpath(t, wbFakeGo(t, b.base+"/system-go"))
	g := sandbox.GoPaths(b.home, b.base+"/agent", real, "")
	c, _ := b.cfg(g)
	if wbIn(c.Ro, real) || wbIn(c.Rw, real) {
		t.Errorf("ro=%v rw=%v", c.Ro, c.Rw)
	}
}

func TestWorkerboxGoUnitModcacheFirstExistingCandidateWinsAndMayBeNone(t *testing.T) {
	b := wbNewGoBox(t)
	wbFakeGo(t, b.home+"/.local/go")
	wbEq(t, "no cache", sandbox.GoPaths(b.home, "/a", "", "").Modcache, "")
	second := b.home + "/.local/lib/factory/go/mod"
	wbMust(t, os.MkdirAll(second, 0o755))
	wbEq(t, "second", sandbox.GoPaths(b.home, "/a", "", "").Modcache, wbRealpath(t, second))
	first := b.home + "/.cache/go/path/pkg/mod"
	wbMust(t, os.MkdirAll(first, 0o755))
	wbEq(t, "first", sandbox.GoPaths(b.home, "/a", "", "").Modcache, wbRealpath(t, first))
}

func TestWorkerboxGoUnitGoEnvHasExactlyTheElevenKeysWithTheExactValues(t *testing.T) {
	b := wbNewGoBox(t)
	g, root, mod := b.goInfo()
	env := sandbox.GoEnvFor(g)
	ad := b.base + "/agent/go"
	wbEq(t, "env", env, map[string]string{"GOROOT": root, "GOCACHE": ad + "/cache", "GOTMPDIR": ad + "/tmp", "GOMODCACHE": mod, "GOPATH": ad + "/path",
		"GOTOOLCHAIN": "local", "GOPROXY": "off", "GOSUMDB": "off", "GOFLAGS": "-mod=readonly", "GOENV": "off", "GOWORK": "off"})
	wbEq(t, "key count", len(env), len(wbGoKeys))
}

func TestWorkerboxGoUnitGoEnvOmitsGomodcacheWithoutAModcache(t *testing.T) {
	b := wbNewGoBox(t)
	wbFakeGo(t, b.home+"/.local/go")
	env := sandbox.GoEnvFor(sandbox.GoPaths(b.home, "/a", "", ""))
	wbEq(t, "key count", len(env), len(wbGoKeys)-1)
	if _, has := env["GOMODCACHE"]; has {
		t.Error("GOMODCACHE is set without a module cache")
	}
}

func TestWorkerboxGoUnitBuildEnvPathStartsWithTheCallerPathAndEndsWithGorootBin(t *testing.T) {
	b := wbNewGoBox(t)
	g, root, _ := b.goInfo()
	genv := sandbox.GoEnvFor(g)
	env := sandbox.BuildEnv(map[string]string{"PATH": "/agent/bin:/usr/bin", "GOPROXY": "https://evil"}, "/h", "/c", "/g", "o", "g", genv)
	wbEq(t, "PATH", env["PATH"], "/agent/bin:/usr/bin:"+root+"/bin")
	wbEq(t, "GOPROXY", env["GOPROXY"], "off") // HQ's value, not the caller's
	env = sandbox.BuildEnv(map[string]string{}, "/h", "/c", "/g", "o", "g", genv)
	wbEq(t, "PATH without caller", env["PATH"], root+"/bin:/usr/bin:/bin")
}

func TestWorkerboxGoUnitBuildEnvWithoutGoEnvIsAsBeforeAndDropsCallerGoVars(t *testing.T) {
	env := sandbox.BuildEnv(map[string]string{"PATH": "/usr/bin", "GOPROXY": "x", "GOFLAGS": "y"}, "/h", "/c", "/g", "o", "g", nil)
	wbEq(t, "PATH", env["PATH"], "/usr/bin")
	for _, k := range wbGoKeys {
		if _, has := env[k]; has {
			t.Errorf("%s passed through", k)
		}
		if wbIn(sandbox.WorkerEnvPass, k) {
			t.Errorf("%s is in WorkerEnvPass", k)
		}
	}
}

func TestWorkerboxGoUnitPrepareAgentStoresGoEnvAndCreatesPrivateDirs(t *testing.T) {
	b := wbNewGoBox(t)
	root := wbRealpath(t, wbFakeGo(t, b.home+"/.local/go"))
	d := b.prepare(nil)
	shim := wbRead(t, d+"/bin/claude")
	wbHas(t, "shim", shim, "go_env")
	for _, sub := range []string{"go", "go/cache", "go/tmp"} {
		wbEq(t, sub, wbMode(t, d+"/"+sub), os.FileMode(0o700))
	}
	wbHas(t, "shim", shim, root)
}

func TestWorkerboxGoUnitPrepareAgentWithoutGoHasNoGoEnvAndNoGoDir(t *testing.T) {
	b := wbNewGoBox(t)
	d := b.prepare(nil)
	wbHasNot(t, "shim", wbRead(t, d+"/bin/claude"), "go_env")
	if wbExists(d + "/go") {
		t.Error("a go dir was created without a toolchain")
	}
}

func TestWorkerboxGoUnitPrepareAgentRefusesASymlinkedGoDirAndWritesNothingThroughIt(t *testing.T) {
	b := wbNewGoBox(t)
	wbFakeGo(t, b.home+"/.local/go")
	d := b.prepare(nil)
	target := b.base + "/elsewhere"
	wbMust(t, os.Mkdir(target, 0o755))
	wbMust(t, os.RemoveAll(d+"/go"))
	wbMust(t, os.Symlink(target, d+"/go"))
	if _, err := b.prepareWith(nil); err == nil {
		t.Fatal("a symlinked go dir was accepted")
	}
	wbEq(t, "target", wbNames(t, target), []string{})
}

func TestWorkerboxGoUnitPrepareAgentRefusesADanglingSymlinkAtGoTmp(t *testing.T) {
	b := wbNewGoBox(t)
	wbFakeGo(t, b.home+"/.local/go")
	d := b.prepare(nil)
	wbMust(t, os.RemoveAll(d+"/go/tmp"))
	wbMust(t, os.Symlink(b.base+"/nowhere", d+"/go/tmp"))
	if _, err := b.prepareWith(nil); err == nil {
		t.Fatal("a dangling symlink at go/tmp was accepted")
	}
	if wbExists(b.base + "/nowhere") {
		t.Error("the dangling target was created")
	}
}

func TestWorkerboxGoUnitPrepareAgentRefusesASymlinkedGoCacheDir(t *testing.T) {
	b := wbNewGoBox(t)
	wbFakeGo(t, b.home+"/.local/go")
	d := b.prepare(nil)
	target := b.base + "/elsewhere"
	wbMust(t, os.Mkdir(target, 0o755))
	wbMust(t, os.RemoveAll(d+"/go/cache"))
	wbMust(t, os.Symlink(target, d+"/go/cache"))
	if _, err := b.prepareWith(nil); err == nil {
		t.Fatal("a symlinked go/cache was accepted")
	}
	wbEq(t, "target", wbNames(t, target), []string{})
}

func TestWorkerboxGoNamespaceGorootVisibleExecutableReadonlyCacheWritableRestOfHomeHidden(t *testing.T) {
	wbNeedUserns(t)
	b := wbNewGoBox(t)
	root := wbRealpath(t, wbFakeGo(t, b.home+"/.local/go"))
	mod := wbRealpath(t, func() string {
		wbPut(t, b.home+"/.cache/go/path/pkg/mod/m.txt", "m", 0)
		return b.home + "/.cache/go/path/pkg/mod"
	}())
	wbPut(t, b.home+"/.local/owner-file", "secret", 0)
	d := b.prepare(nil)
	res := b.run(d, []map[string]any{
		{"op": "runraw", "argv": []string{root + "/bin/go"}},
		wbOp("write", "path", root+"/bin/planted"),
		wbOp("write", "path", mod+"/planted"),
		wbOp("write", "path", d+"/go/cache/ok"),
		wbOp("exists", "path", b.home+"/.local/owner-file"),
		wbOp("env", "name", "GOPROXY"),
		{"op": "runraw", "argv": []string{"sh", "-c", "command -v go"}}}, "coder-t-1", "coder", "")
	v0 := wbVals(res[0])
	wbEq(t, "fake go", []any{v0[0], v0[1]}, []any{float64(0), "fake go\n"})
	wbFail(t, "write to the go root", res[1], "EROFS")
	wbFail(t, "write to the module cache", res[2], "EROFS")
	if !res[3].Ok {
		t.Errorf("write to the agent go cache failed: %s", res[3].Err)
	}
	wbEq(t, "owner file visible", res[4].Val, false)
	wbEq(t, "GOPROXY", res[5].Val, "off")
	wbEq(t, "command -v go", wbVals(res[6])[1], root+"/bin/go\n")
}

// wbRealGo is the owner's real toolchain and first module cache (nil root when there is no ~/.local/go/bin/go).
func wbRealGo() (goRoot, modcache string) {
	home := sandbox.RealHome()
	if fi, err := os.Stat(home + "/.local/go/bin/go"); err != nil || !fi.Mode().IsRegular() {
		return "", ""
	}
	for _, rel := range sandbox.GoModcacheRel {
		if fi, err := os.Stat(home + "/" + rel); err == nil && fi.IsDir() {
			return home + "/.local/go", home + "/" + rel
		}
	}
	return home + "/.local/go", ""
}

func (b *wbGoBox) runIn(ops []map[string]any, role string, rwCwd bool, goRoot, goMod string) []wbRes {
	b.t.Helper()
	agent := role + "-t-1"
	d := b.prepare(func(a *sandbox.PrepareAgentArgs) {
		a.Agent, a.Role, a.RwCwd, a.GoRoot, a.GoModcache = agent, role, rwCwd, goRoot, goMod
	})
	return b.run(d, ops, agent, role, "")
}

func TestWorkerboxGoRealToolchainGoGofmtVetAndRaceTestWorkInsideOffline(t *testing.T) {
	wbNeedUserns(t)
	goRoot, goMod := wbRealGo()
	if goRoot == "" {
		t.Skip("no ~/.local/go/bin/go on this host")
	}
	b := wbNewGoBox(t)
	wbPut(t, b.cwd+"/go.mod", "module example.invalid/tiny\n\ngo 1.21\n", 0)
	wbPut(t, b.cwd+"/a.go", "package tiny\n\nfunc Add(a, b int) int { return a + b }\n", 0)
	wbPut(t, b.cwd+"/a_test.go", "package tiny\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n", 0)
	run := func(argv ...string) map[string]any { return map[string]any{"op": "runraw", "argv": argv} }
	res := b.runIn([]map[string]any{run("go", "version"), run("go", "env", "GOPROXY", "GOTOOLCHAIN"), run("gofmt", "-l", "a.go"),
		run("go", "vet", "./..."), run("go", "test", "-race", "./...")}, "coder", true, goRoot, goMod)
	var vals [][]any
	for _, r := range res {
		if !r.Ok {
			t.Fatalf("probe op failed: %s", r.Err)
		}
		vals = append(vals, wbVals(r))
	}
	if !strings.HasPrefix(vals[0][1].(string), "go version go1.") {
		t.Errorf("go version = %v", vals[0])
	}
	wbEq(t, "go env", strings.Fields(vals[1][1].(string)), []string{"off", "local"})
	wbEq(t, "gofmt", vals[2][:2], []any{float64(0), ""})
	wbEq(t, "vet code", vals[3][0], float64(0))
	wbEq(t, "test -race code", vals[4][0], float64(0))
}

func wbGoHeavy(t *testing.T, role string, rwCwd bool) {
	t.Helper()
	if os.Getenv("FACTORY_GO_SANDBOX_HEAVY") != "1" {
		t.Skip("set FACTORY_GO_SANDBOX_HEAVY=1")
	}
	wbNeedUserns(t)
	goRoot, goMod := wbRealGo()
	if goRoot == "" {
		t.Skip("no ~/.local/go/bin/go on this host")
	}
	b := wbNewGoBox(t)
	repo, err := ft.RepoRoot()
	wbMust(t, err)
	// plain local clone (HEAD only, not --shared): the Go tests also read ci/ and roles/ files outside go/
	if out, err := exec.Command("git", "clone", "--quiet", repo, b.cwd+"/repo").CombinedOutput(); err != nil {
		t.Fatalf("git clone: %v\n%s", err, out)
	}
	gd := b.cwd + "/repo/go"
	live := map[string]string{"WORKER_GUARD_LIVE_HOME": b.fh}
	res := b.runIn([]map[string]any{
		{"op": "runraw", "argv": []string{"go", "vet", "./..."}, "cwd": gd, "env": live},
		{"op": "runraw", "argv": []string{"sh", "-c", "go test -race ./..."}, "cwd": gd, "env": live}}, role, rwCwd, goRoot, goMod)
	for i, what := range []string{"vet", "test -race"} {
		if !res[i].Ok {
			t.Fatalf("go %s: %s", what, res[i].Err)
		}
		v := wbVals(res[i])
		if v[0] != float64(0) {
			t.Errorf("go %s: %v\n%v", what, v[1], v[2])
		}
	}
}

func TestWorkerboxGoHeavyCoderVetAndRaceTestOfGoModuleOffline(t *testing.T) {
	wbGoHeavy(t, "coder", true)
}

func TestWorkerboxGoHeavyQaVetAndRaceTestOfGoModuleOffline(t *testing.T) { wbGoHeavy(t, "qa", true) }

func TestWorkerboxGoHeavyReviewerVetAndRaceTestOfGoModuleOffline(t *testing.T) {
	wbGoHeavy(t, "reviewer", false)
}
