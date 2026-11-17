package sandbox2

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"factory/go/internal/sandbox"
	"factory/go/internal/spawn"
)

// Port of tests/test_researcher_sandbox.py (16 cases): the researcher always runs in the G-010 worker sandbox (fail
// closed, read-only gh token). Group A drives spawn and relaunch (still Python commands, so those cases skip without
// python3) and the Go ports of prepare_agent and the cwd rule; group B is the real namespace.
//
// not ported: the `_GH_KEY["researcher"] == "gh_ro"` assertion of test_researcher_is_sandboxable_with_the_read_only_token -
//   a Python-internal table; the Go equivalent (the role's token file) is asserted, and the config key is exercised by
//   the gh_token_ro cases of the sandbox spawn port.
// test_sandbox_wanted_unit and test_gh_rw_roles_never_gives_the_researcher_the_rw_token call Python module functions
//   (sandbox_wanted, sandbox_gh_token); they run against their Go ports spawn.SandboxWanted and WorkerPaths.GhTokenFor,
//   and the gh_rw_roles case also checks the token a real researcher spawn puts into its shim.

// ---- ConstantsTests ----

func TestResearcherSandboxResearcherIsSandboxableWithTheReadOnlyToken(t *testing.T) {
	spTrue(t, "researcher is sandboxable", sandbox.Sandboxable["researcher"])
	p := sandbox.PathsFor("/h", "", "")
	spEq(t, "researcher gh token", p.GhTokenFor("researcher"), p.GhRO)
	spEq(t, "ro token path", p.GhRO, "/h/.config/factory/gh-worker-ro-token")
}

// ---- ResearcherCase ----

type rEnv struct {
	*spEnv
	xdg map[string]string
}

func rNew(t *testing.T) *rEnv {
	t.Helper()
	e := spNewPrepped(t)
	e.SeedTask(taskResearcher())
	e.spExec("UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
	return &rEnv{e, map[string]string{"XDG_STATE_HOME": filepath.Join(e.UserHome, "xdg-state")}}
}

func (r *rEnv) spawn() ftResult {
	r.T.Helper()
	return r.RunWith(optsEnv(r.xdg), "spawn", "researcher", "--name", "zz-test-researcher", "--goal", "G-001", "--task", "T-0001")
}

func (r *rEnv) live() []spRow {
	r.T.Helper()
	return r.spRows("SELECT name FROM agents WHERE role='researcher' AND status='live'")
}

func (r *rEnv) dropOauth() { os.Remove(r.spTok("worker-oauth-token")) }

// ---- AlwaysSandboxedTests ----

func TestResearcherSandboxSandboxWantedUnit(t *testing.T) {
	spTrue(t, "researcher, sandbox_roles ''", spawn.SandboxWanted("researcher", false, ""))
	spTrue(t, "researcher optin, sandbox_roles ''", spawn.SandboxWanted("researcher", true, ""))
	for _, role := range []string{"pm", "evaluator", "staff-engineer", "coder", "reviewer", "qa"} {
		spTrue(t, role+" is not sandboxed with sandbox_roles ''", !spawn.SandboxWanted(role, false, ""))
	}
	spTrue(t, "researcher, sandbox_roles coder", spawn.SandboxWanted("researcher", false, "coder"))
	spTrue(t, "coder, sandbox_roles coder", spawn.SandboxWanted("coder", false, "coder"))
	spTrue(t, "pm, sandbox_roles coder", !spawn.SandboxWanted("pm", false, "coder"))
}

func TestResearcherSandboxSandboxWantedTrueForResearcherWithEmptySandboxRoles(t *testing.T) {
	// through the real entry point: a researcher spawn with sandbox_roles '' still goes through the sandbox
	r := rNew(t)
	r.OK("config", "sandbox_roles", "")
	p := r.spawn()
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	spTrue(t, "spawn_args.sandbox", spTruthy(r.spSpawnArgsOf("zz-test-researcher"), "sandbox"))
	agentDir := r.spAgentDir("zz-test-researcher")
	fi, err := os.Stat(filepath.Join(agentDir, "bin", "claude"))
	spTrue(t, "shim is a file", err == nil && fi.Mode().IsRegular())
	paths := r.spPanePaths()
	ok := len(paths) > 0
	for _, x := range paths {
		ok = ok && strings.HasPrefix(x, "PATH="+agentDir+"/bin:")
	}
	spTrue(t, fmt.Sprint("pane PATHs ", paths), ok)
}

func TestResearcherSandboxOtherRolesKeepTheirConfigDrivenBehaviour(t *testing.T) {
	// sandbox_roles '' -> a pm and a reviewer spawn is NOT sandboxed
	r := rNew(t)
	r.OK("config", "sandbox_roles", "")
	for _, role := range []string{"reviewer", "pm"} {
		args := []string{"spawn", role, "--name", "zz-x-" + role, "--goal", "G-001", "--team", "g-001-a"}
		if role == "reviewer" {
			args = append(args, "--cwd", r.spCwd("reviewer"))
		}
		p := r.Run(args, "")
		if p.Exit != 0 {
			t.Fatalf("%s: exit %d\n%s", role, p.Exit, p.Stderr)
		}
		spTrue(t, role+" is not sandboxed", !spTruthy(r.spSpawnArgsOf("zz-x-"+role), "sandbox"))
	}
}

func TestResearcherSandboxGhRwRolesNeverGivesTheResearcherTheRwToken(t *testing.T) {
	r := rNew(t)
	r.spExec("INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "gh_rw_roles", "coder,researcher")
	// the Go port of sandbox_gh_token's role -> file rule: gh_rw_roles is a CLI setting and never reaches it
	p := sandbox.PathsFor(r.UserHome, "", "")
	spTrue(t, "researcher token "+p.GhTokenFor("researcher"), strings.HasSuffix(p.GhTokenFor("researcher"), "gh-worker-ro-token"))
	spTrue(t, "coder token", strings.HasSuffix(p.GhTokenFor("coder"), "gh-worker-rw-token"))
	// and the real entry point: the shim of a spawned researcher reads the ro token
	if s := r.spawn(); s.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", s.Exit, s.Stdout, s.Stderr)
	}
	tok, _ := r.spShimCfg("zz-test-researcher")["gh_token"].(string)
	spTrue(t, "shim gh_token "+tok, strings.HasSuffix(tok, "gh-worker-ro-token"))
}

func TestResearcherSandboxBadCwdIsRefusedAndTheResearchDirAccepted(t *testing.T) {
	r := rNew(t)
	other := filepath.Join(r.UserHome, "xdg-state", "factory", "research", "t-0001", "sub")
	spMkdir(t, other)
	good := filepath.Dir(other)
	try := func(name, cwd string) ftResult {
		return r.RunWith(optsEnv(r.xdg), "spawn", "researcher", "--name", name, "--goal", "G-001", "--task", "T-0001", "--cwd", cwd)
	}
	if p := try("zz-test-researcher", good); p.Exit != 0 {
		t.Fatalf("good cwd %s: exit %d\n%s%s", good, p.Exit, p.Stdout, p.Stderr)
	}
	for i, bad := range []string{other, filepath.Dir(good), r.UserHome} {
		name := fmt.Sprintf("zz-test-researcher-%d", i)
		before := len(r.Calls("herdr"))
		p := try(name, bad)
		// the CLI checks an explicit --cwd against the task's research dir before the sandbox preflight (exit 4);
		// the preflight's own "cwd" reason (sandbox_preflight in-process in the original) is the Go CheckCwd rule below
		if p.Exit != 4 {
			t.Fatalf("bad cwd %s: exit %d, want 4\n%s%s", bad, p.Exit, p.Stdout, p.Stderr)
		}
		spHas(t, "stderr", p.Stderr, "researcher cwd must be its research dir")
		spEq(t, "rows for "+name, r.spAgents(name), []spRow(nil))
		research := filepath.Join(r.UserHome, "xdg-state", "factory", "research")
		err := sandbox.CheckCwd(name, bad, r.Home, r.UserHome, "", "", "", nil, research)
		if bad == other {
			spTrue(t, "Go CheckCwd accepts a dir below the research root (the task-dir rule is the CLI's)", err == nil)
		} else {
			spTrue(t, "Go CheckCwd refuses "+bad, err != nil)
		}
		for _, c := range r.Calls("herdr")[before:] {
			if len(c.Argv) >= 2 && c.Argv[1] == "start" {
				t.Fatalf("a refused cwd %s started an agent: %v", bad, c.Argv)
			}
		}
	}
}

func TestResearcherSandboxRolesListAcceptsResearcherAndStillRefusesPm(t *testing.T) {
	r := rNew(t)
	r.OK("config", "sandbox_roles", "reviewer,researcher")
	r.Fail(2, "config", "sandbox_roles", "pm")
}

// ---- FailClosedTests ----

func (r *rEnv) assertRefusedForOwnerSteps(p ftResult) {
	t := r.T
	t.Helper()
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	spHas(t, "stderr", p.Stderr, "worker-oauth-token")
	spHas(t, "stderr", p.Stderr, "docs/worker-sandbox.md")
	spNotHas(t, "stderr", p.Stderr, "sandbox_roles ''") // there is no "turn it off" for the researcher
}

func TestResearcherSandboxSpawnRefusedWhenPreflightFails(t *testing.T) {
	r := rNew(t)
	r.dropOauth()
	r.assertRefusedForOwnerSteps(r.spawn())
	spEq(t, "live", r.live(), []spRow(nil))
	spEq(t, "agent start", r.spMux("agent", "start"), [][]string{})
	spEq(t, "placements", len(r.spMux("tab", "create"))+len(r.spMux("pane", "split")), 0)
	var other [][]string
	for _, c := range r.Calls("herdr") {
		if len(c.Argv) < 2 || c.Argv[0] != "agent" || c.Argv[1] != "list" {
			other = append(other, c.Argv) // only the read-only list is allowed
		}
	}
	spEq(t, "herdr calls but the list", other, [][]string(nil))
}

func TestResearcherSandboxSpawnRefusedWhenUsernsIsMissing(t *testing.T) {
	r := rNew(t)
	r.spStubUnshare("#!/bin/sh\nexit 1\n")
	p := r.spawn()
	if p.Exit != 3 {
		t.Fatalf("exit %d, want 3\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	spHas(t, "stderr", p.Stderr, "docs/worker-sandbox.md")
	spEq(t, "live", r.live(), []spRow(nil))
	spEq(t, "agent start", r.spMux("agent", "start"), [][]string{})
}

func TestResearcherSandboxRelaunchRefusedBeforeTheOldPaneIsClosed(t *testing.T) {
	r := rNew(t)
	if p := r.spawn(); p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	spEq(t, "live", len(r.live()), 1)
	r.dropOauth()
	p := r.RunWith(optsEnv(r.xdg), "relaunch", "zz-test-researcher", "--force")
	r.assertRefusedForOwnerSteps(p)
	spEq(t, "pane close", r.spMux("pane", "close"), [][]string{})
	spEq(t, "agent start", len(r.spMux("agent", "start")), 1)
	spEq(t, "live", len(r.live()), 1)
}

// ---- PrepareAgentTests: the Go port of prepare_agent ----

const (
	rOauth = "oauth-A1b2A1b2A1b2A1b2A1b2A1b2A1b2A1b2"
	rGhRO  = "ghro-C3d4C3d4C3d4C3d4C3d4C3d4C3d4C3d4"
	rGhRW  = "ghrw-E5f6E5f6E5f6E5f6E5f6E5f6E5f6E5f6"
)

// rWorld is a fake home that mirrors the owner's layout, a fake factory home inside it, and tokens. It sits under
// /var/tmp: the worker profile hides /tmp.
type rWorld struct {
	base, home, runtime, fh, claude, tokens string
}

func rNewWorld(t *testing.T) *rWorld {
	t.Helper()
	if fi, err := os.Stat("/var/tmp"); err != nil || !fi.IsDir() {
		t.Skip("/var/tmp is not usable: the fixture must not sit below /tmp or /dev/shm")
	}
	base, err := os.MkdirTemp("/var/tmp", "zz-test-workerbox-")
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(base) })
	base = sandbox.Realpath(base)
	w := &rWorld{base: base, home: base + "/home", runtime: base + "/run"}
	w.fh = w.home + "/factory"
	spMkdir(t, w.runtime)
	spWrite(t, w.home+"/.claude/.credentials.json", `{"secret": "fake-creds"}`, 0)
	spWrite(t, w.home+"/.ssh/id_ed25519", "fake-private-key", 0)
	spWrite(t, w.home+"/.config/gh/hosts.yml", "github.com: fake", 0)
	spWrite(t, w.home+"/.zshrc", "# owner zshrc\n", 0)
	real := w.home + "/.local/share/claude/versions/1/claude"
	spWrite(t, real, "#!/bin/sh\nexit 0\n", 0o755)
	w.claude = w.home + "/.local/bin/claude"
	spMkdir(t, filepath.Dir(w.claude))
	if err := os.Symlink(real, w.claude); err != nil {
		t.Fatal(err)
	}
	spWrite(t, w.fh+"/bin/factory", "#!/bin/sh\nexit 0\n", 0o755)
	if err := os.Symlink(w.fh+"/bin/factory", w.home+"/.local/bin/factory"); err != nil {
		t.Fatal(err)
	}
	spWrite(t, w.fh+"/roles/settings.json", "{}", 0)
	spWrite(t, w.fh+"/db/factory.db", "ROWS", 0)
	spWrite(t, w.fh+"/worktrees/own/file.txt", "own", 0)
	spWrite(t, w.fh+"/worktrees/other/file.txt", "other", 0)
	w.tokens = w.home + "/.config/factory"
	for name, tok := range map[string]string{"worker-oauth-token": rOauth, "gh-worker-ro-token": rGhRO, "gh-worker-rw-token": rGhRW} {
		spWrite(t, w.tokens+"/"+name, tok+"\n", 0o600)
	}
	spWrite(t, w.tokens+"/"+sandbox.TermsName, "dummy-term-one\ndummy-term-two\n", 0o600)
	return w
}

// prepare is World.prepare for a researcher/coder: the agent dir, or the refusal.
func (w *rWorld) prepare(agent, role, cwd, researchRoot, tokenDir, self string) (string, error) {
	if self == "" {
		self = "/opt/factory"
	}
	return sandbox.PrepareAgent(sandbox.PrepareAgentArgs{Agent: agent, Role: role, Cwd: cwd, FactoryHome: w.fh, DBDir: w.fh + "/db",
		RealClaude: w.claude, RwCwd: true, Home: w.home, Runtime: w.runtime, TokenDir: tokenDir, Identity: &[2]string{"Test Owner", "owner@example.invalid"},
		GhSlugs: []string{"volekerb/hivemill"}, ResearchRoot: researchRoot, Self: self,
		Seams: sandbox.Opts{UID: os.Getuid(), GID: os.Getgid(), Runtime: w.runtime}})
}

// rShimCfg is the cfg the generated shim carries (the one quoted argument of `worker-shim`).
func rShimCfg(t *testing.T, agentDir string) *sandbox.ShimCfg {
	t.Helper()
	shim := spRead(t, filepath.Join(agentDir, "bin", "claude"))
	_, rest, ok := strings.Cut(shim, "worker-shim '")
	if !ok {
		t.Fatalf("shim has no cfg line:\n%s", shim)
	}
	var sc sandbox.ShimCfg
	if err := json.Unmarshal([]byte(strings.TrimSuffix(rest, "' \"$@\"\n")), &sc); err != nil {
		t.Fatalf("shim cfg: %v", err)
	}
	return &sc
}

func rContains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func rPrepWorld(t *testing.T) (*rWorld, string, string) {
	t.Helper()
	w := rNewWorld(t)
	research := w.home + "/.local/state/factory/research"
	cwd := research + "/t-1"
	spMkdir(t, cwd)
	return w, research, cwd
}

func TestResearcherSandboxRoTokenAndCwdBoundReadWrite(t *testing.T) {
	w, research, cwd := rPrepWorld(t)
	dir, err := w.prepare("researcher-t-1", "researcher", cwd, research, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sc := rShimCfg(t, dir)
	spEq(t, "gh_token", sc.GhToken, w.tokens+"/gh-worker-ro-token")
	b, _ := json.Marshal(sc)
	spNotHas(t, "cfg", string(b), "gh-worker-rw-token")
	spTrue(t, "cwd in ns.rw", rContains(sc.NS.Rw, cwd))
	spTrue(t, "cwd not in ns.ro", !rContains(sc.NS.Ro, cwd))
	spTrue(t, "ns.cwd set", sc.NS.Cwd != nil)
	spEq(t, "ns.cwd", *sc.NS.Cwd, cwd)
}

func TestResearcherSandboxTokenDirOverrideStillPicksTheRoFile(t *testing.T) {
	w, research, cwd := rPrepWorld(t)
	tdir := w.base + "/tok"
	for _, name := range []string{"worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"} {
		spWrite(t, tdir+"/"+name, "x\n", 0o600)
	}
	dir, err := w.prepare("researcher-t-1", "researcher", cwd, research, tdir, "")
	if err != nil {
		t.Fatal(err)
	}
	spEq(t, "gh_token", rShimCfg(t, dir).GhToken, tdir+"/gh-worker-ro-token")
}

func TestResearcherSandboxCwdMustBeBelowTheResearchRoot(t *testing.T) {
	w, research, _ := rPrepWorld(t)
	for _, bad := range []string{research, w.home, w.home + "/.ssh"} {
		t.Run(filepath.Base(bad), func(t *testing.T) {
			if dir, err := w.prepare("researcher-t-1", "researcher", bad, research, "", ""); err == nil {
				t.Fatalf("cwd %s accepted: %s", bad, dir)
			}
		})
	}
}

func TestResearcherSandboxResearchRootIsIgnoredForOtherRoles(t *testing.T) {
	w, research, cwd := rPrepWorld(t)
	if dir, err := w.prepare("coder-t-1", "coder", cwd, research, "", ""); err == nil {
		t.Fatalf("a coder was accepted in the research dir: %s", dir)
	}
}

func TestResearcherSandboxAgentDirIsNeverAValidResearchCwd(t *testing.T) {
	w, _, _ := rPrepWorld(t)
	workers := w.home + "/.local/state/factory/workers"
	spMkdir(t, workers+"/researcher-t-1")
	if dir, err := w.prepare("researcher-t-1", "researcher", workers+"/researcher-t-1", workers, "", ""); err == nil {
		t.Fatalf("the agent dir was accepted as a research cwd: %s", dir)
	}
}

// ---- RealNamespaceTests (acceptance 5) ----

// rSelfBinary builds the sandbox package's test binary, which is its own worker-shim and stage-1 entry point (its
// TestMain runs sandbox.Main): the Self of a real shim.
func rSelfBinary(t *testing.T) string {
	t.Helper()
	gobin, err := exec.LookPath("go")
	if err != nil {
		gobin = filepath.Join(runtime.GOROOT(), "bin", "go")
	}
	out := filepath.Join(t.TempDir(), "sandbox.test")
	cmd := exec.Command(gobin, "test", "-c", "-o", out, "factory/go/internal/sandbox")
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build the sandbox test binary that serves as worker-shim: %v\n%s", err, b)
	}
	return out
}

const rProbe = `#!%s
import errno, json, os, sys
print("RAN", flush=True)
def err(e):
    return errno.errorcode.get(e.errno, str(e.errno)) if isinstance(e, OSError) else type(e).__name__
def do(o):
    k = o["op"]
    if k == "open":
        with open(o["path"], "rb") as f:
            return f.read(0) == b""
    if k == "write":
        with open(o["path"], "w") as f:
            f.write(o.get("data", "x"))
        return True
    if k == "listdir":
        return sorted(os.listdir(o["path"]))
    if k == "cwd":
        return os.getcwd()
    raise ValueError(k)
res = []
for o in json.loads(sys.argv[1]) if len(sys.argv) > 1 else []:
    try:
        res.append({"ok": True, "val": do(o)})
    except Exception as e:
        res.append({"ok": False, "err": err(e)})
print("RESULT:" + json.dumps(res), flush=True)
`

func TestResearcherSandboxSecretsUnreachableAndOnlyTheCwdIsWritable(t *testing.T) {
	if ok, why := spUserns(); !ok {
		t.Skip("user namespaces unavailable: " + why)
	}
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is not on PATH: the probe claude is a Python script")
	}
	w, research, cwd := rPrepWorld(t)
	spWrite(t, w.home+"/.local/share/claude/versions/1/claude", fmt.Sprintf(rProbe, py), 0o755)
	self := rSelfBinary(t)
	dir, err := w.prepare("researcher-t-1", "researcher", cwd, research, "", self)
	if err != nil {
		t.Fatal(err)
	}
	ops := []map[string]string{
		{"op": "open", "path": w.tokens + "/worker-oauth-token"},
		{"op": "listdir", "path": w.home + "/.ssh"},
		{"op": "write", "path": w.fh + "/roles/new.txt"},
		{"op": "write", "path": w.fh + "/roles/settings.json"},
		{"op": "write", "path": cwd + "/out.txt", "data": "ok"},
		{"op": "cwd"},
	}
	js, _ := json.Marshal(ops)
	cmd := exec.Command(dir+"/bin/claude", string(js))
	cmd.Dir = cwd
	cmd.Env = []string{"PATH=" + w.home + "/.local/bin:/usr/bin:/bin", "LANG=C.UTF-8", "TERM=xterm", "LC_ALL=C.UTF-8",
		"FACTORY_AGENT=researcher-t-1", "FACTORY_ROLE=researcher", "FACTORY_HOME=" + w.fh, "FACTORY_DB=" + w.fh + "/db",
		"HOME=/nonexistent", "TMPDIR=/tmp"}
	out, err := cmd.Output()
	if err != nil {
		var se []byte
		if ee, ok := err.(*exec.ExitError); ok {
			se = ee.Stderr
		}
		t.Fatalf("shim run: %v\nstdout: %s\nstderr: %s", err, out, se)
	}
	type res struct {
		OK  bool            `json:"ok"`
		Err string          `json:"err"`
		Val json.RawMessage `json:"val"`
	}
	var got []res
	found := false
	for _, line := range strings.Split(string(out), "\n") {
		if rest, ok := strings.CutPrefix(line, "RESULT:"); ok {
			if err := json.Unmarshal([]byte(rest), &got); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	spTrue(t, "a RESULT line in "+string(out), found && len(got) == len(ops))
	spTrue(t, "the worker-oauth-token FILE is readable inside the researcher sandbox", !got[0].OK)
	spEq(t, "oauth open error", got[0].Err, "ENOENT")
	spTrue(t, "~/.ssh is absent or empty", !got[1].OK || string(got[1].Val) == "[]")
	for _, r := range got[2:4] {
		spTrue(t, "a write under factory/roles succeeded", !r.OK)
		spTrue(t, "write error "+r.Err, r.Err == "EROFS" || r.Err == "ENOENT")
	}
	spTrue(t, "write into the cwd", got[4].OK)
	spEq(t, "cwd file", spRead(t, cwd+"/out.txt"), "ok")
	spEq(t, "cwd", string(got[5].Val), `"`+cwd+`"`)
	spEq(t, "settings.json on the host", spRead(t, w.fh+"/roles/settings.json"), "{}") // untouched
	spTrue(t, "new.txt not created on the host", !spExists(w.fh+"/roles/new.txt"))
}
