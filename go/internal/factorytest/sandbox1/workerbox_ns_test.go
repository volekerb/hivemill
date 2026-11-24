package sandbox1

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"

	"factory/go/internal/sandbox"
)

// ---- Group B2: the real namespaces against the fake home ---------------------------------------------------------

type wbNS struct {
	*wbWorld
	d string
}

func wbNewNS(t *testing.T) *wbNS {
	wbNeedUserns(t)
	w := wbNewWorld(t)
	return &wbNS{w, w.prepare(nil)}
}

func (n *wbNS) p(ops ...map[string]any) []wbRes { n.t.Helper(); return n.probe(n.d, ops...) }

func wbFail(t testing.TB, what string, r wbRes, errs ...string) {
	t.Helper()
	if r.Ok {
		t.Fatalf("%s: succeeded, want a failure %v", what, errs)
	}
	if !wbIn(errs, r.Err) {
		t.Fatalf("%s: err = %q, want one of %v", what, r.Err, errs)
	}
}

func wbConnectable(path string) bool {
	c, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

func TestWorkerboxSandboxHiddenCredentialsAndSockets(t *testing.T) {
	n := wbNewNS(t)
	paths := []string{n.home + "/.claude/.credentials.json", n.home + "/.ssh/id_ed25519", n.home + "/.config/gh/hosts.yml"}
	socks := []string{n.bus, n.control, n.agentSock}
	for _, s := range socks { // control: the sockets do accept connections outside
		if !wbConnectable(s) {
			t.Fatalf("%s does not accept outside", s)
		}
	}
	var ops []map[string]any
	var items []string
	for _, p := range paths {
		ops, items = append(ops, wbOp("open", "path", p)), append(items, p)
	}
	for _, s := range socks {
		ops, items = append(ops, wbOp("connect", "path", s)), append(items, s)
	}
	for i, r := range n.p(ops...) {
		wbFail(t, "reachable from inside: "+items[i], r, "ENOENT")
	}
}

// T-0673: the container-engine and ssh-unix-local sockets under the host /run are masked in the worker profile.
func TestWorkerboxSandboxHostRunSocketsUnreachable(t *testing.T) {
	n := wbNewNS(t)
	var socks []string
	var ops []map[string]any
	for _, p := range wbHostRunSockets {
		if wbConnectable(p) { // control: connectable outside, or not asserted
			socks, ops = append(socks, p), append(ops, wbOp("connect", "path", p))
		}
	}
	if len(socks) == 0 {
		t.Skip("no connectable host /run socket on this box")
	}
	for i, r := range n.p(ops...) {
		wbFail(t, "reachable from inside: "+socks[i], r, "ENOENT")
	}
}

func TestWorkerboxSandboxSSHAgentAndBusEnvUnset(t *testing.T) {
	n := wbNewNS(t)
	env := wbEnvOf(n.p(wbOp("environ"))[0])
	for _, k := range []string{"SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS"} {
		if _, has := env[k]; has {
			t.Errorf("%s is set inside", k)
		}
	}
}

func TestWorkerboxSandboxHomeIsPrivateAndZshrcWriteStaysInside(t *testing.T) {
	n := wbNewNS(t)
	zshrc := n.home + "/.zshrc"
	before := wbSha(t, zshrc)
	res := n.p(wbOp("read", "path", zshrc), wbOp("write", "path", zshrc, "data", "pwned\n"), wbOp("read", "path", zshrc),
		wbOp("listdir", "path", n.home))
	if res[0].Ok {
		t.Error("the owner's .zshrc was readable")
	}
	if !res[1].Ok {
		t.Errorf("write inside the private home failed: %s", res[1].Err)
	}
	wbEq(t, "read back", res[2].Val, "pwned\n")
	wbEq(t, "host sha", wbSha(t, zshrc), before)
	listing := wbStrs(res[3].Val)
	if wbIn(listing, ".ssh") || wbIn(listing, ".claude") {
		t.Errorf("home listing = %v", listing)
	}
}

func TestWorkerboxSandboxFactoryTreeIsReadOnly(t *testing.T) {
	n := wbNewNS(t)
	x := n.fh + "/bin/x"
	res := n.p(wbOp("write", "path", x), wbOp("write", "path", n.fh+"/roles/settings.json"), wbOp("unlink", "path", n.fh+"/bin/factory"))
	for i, r := range res {
		wbFail(t, "op "+strconv.Itoa(i), r, "EROFS")
	}
	if wbExists(x) {
		t.Error("a write reached the factory tree")
	}
}

func TestWorkerboxSandboxWorktreesListsOnlyTheOwnWorktree(t *testing.T) {
	n := wbNewNS(t)
	res := n.p(wbOp("listdir", "path", n.fh+"/worktrees"), wbOp("read", "path", n.fh+"/worktrees/own/file.txt"),
		wbOp("read", "path", n.fh+"/worktrees/other/file.txt"))
	wbEq(t, "worktrees", wbStrs(res[0].Val), []string{"own"})
	wbEq(t, "own file", res[1].Val, "own")
	if res[2].Ok {
		t.Error("another worktree was readable")
	}
}

func TestWorkerboxSandboxFactoryCLIResolvesViaPathAndWritesDB(t *testing.T) {
	n := wbNewNS(t)
	res := n.p(wbOp("which", "name", "factory"), map[string]any{"op": "run", "argv": []string{"factory", "whoami"}},
		map[string]any{"op": "run", "argv": []string{"factory", "write"}})
	wbEq(t, "which", res[0].Val, n.home+"/.local/bin/factory")
	v1 := wbVals(res[1])
	wbEq(t, "whoami code", v1[0], float64(0))
	wbHas(t, "whoami", v1[1].(string), "agent=coder-t-1 role=coder")
	wbHas(t, "whoami", v1[1].(string), "factory.db")
	wbEq(t, "write code", wbVals(res[2])[0], float64(0))
	wbEq(t, "db/written", wbRead(t, n.fh+"/db/written"), "from inside")
}

func TestWorkerboxSandboxDBDirIsWritableButDBDirParentIsNot(t *testing.T) {
	n := wbNewNS(t)
	res := n.p(wbOp("write", "path", n.fh+"/db/new.txt", "data", "r"), wbOp("write", "path", n.fh+"/newfile"))
	if !res[0].Ok {
		t.Errorf("db write failed: %s", res[0].Err)
	}
	wbFail(t, "write next to db", res[1], "EROFS")
	wbEq(t, "db/new.txt", wbRead(t, n.fh+"/db/new.txt"), "r")
}

func TestWorkerboxSandboxClaudeConfigDirPersistsBetweenTwoRuns(t *testing.T) {
	n := wbNewNS(t)
	cd, _ := wbEnvOf(n.p(wbOp("environ"))[0])["CLAUDE_CONFIG_DIR"].(string)
	wbEq(t, "CLAUDE_CONFIG_DIR", cd, n.d+"/claude")
	n.p(wbOp("write", "path", cd+"/session.jsonl", "data", "run one"))
	wbEq(t, "session", wbRead(t, n.d+"/claude/session.jsonl"), "run one")
	wbEq(t, "second run", n.p(wbOp("read", "path", cd+"/session.jsonl"))[0].Val, "run one")
	wbEq(t, "onboarding", wbClaudeJSON(t, n.d+"/claude/.claude.json")["hasCompletedOnboarding"], true)
}

func TestWorkerboxSandboxCwdRwWhenAskedAndReadOnlyOtherwise(t *testing.T) {
	n := wbNewNS(t)
	res := n.p(wbOp("cwd"), wbOp("write", "path", n.own+"/new.txt", "data", "w"))
	wbEq(t, "cwd", res[0].Val, n.own)
	if !res[1].Ok {
		t.Errorf("rw cwd write failed: %s", res[1].Err)
	}
	wbEq(t, "new.txt", wbRead(t, n.own+"/new.txt"), "w")
	n.d = n.prepare(func(a *sandbox.PrepareAgentArgs) { a.RwCwd = false })
	res = n.p(wbOp("cwd"), wbOp("write", "path", n.own+"/again.txt"), wbOp("read", "path", n.own+"/file.txt"))
	wbEq(t, "cwd", res[0].Val, n.own)
	wbFail(t, "ro cwd write", res[1], "EROFS")
	wbEq(t, "file.txt", res[2].Val, "own")
	if wbExists(n.own + "/again.txt") {
		t.Error("a write reached the read-only cwd")
	}
}

func TestWorkerboxSandboxEnvIsExactlyTheAllowList(t *testing.T) {
	n := wbNewNS(t)
	caller := map[string]string{}
	for _, kv := range n.callerEnv(nil) {
		k, v, _ := strings.Cut(kv, "=")
		caller[k] = v
	}
	env := wbEnvOf(n.p(wbOp("environ"))[0])
	passed := map[string]bool{}
	for k := range caller {
		if wbIn(sandbox.WorkerEnvPass, k) || strings.HasPrefix(k, "LC_") {
			passed[k] = true
		}
	}
	want := map[string]bool{}
	for k := range passed {
		want[k] = true
	}
	for _, k := range []string{"HOME", "CLAUDE_CONFIG_DIR", "GIT_CONFIG_GLOBAL", "DISABLE_AUTOUPDATER", "CLAUDE_CODE_OAUTH_TOKEN", "GH_TOKEN"} {
		want[k] = true
	}
	got := map[string]bool{}
	for k := range env {
		got[k] = true
	}
	wbEq(t, "env keys", got, want)
	for _, gone := range []string{"FOO_SECRET", "SSH_AUTH_SOCK", "DBUS_SESSION_BUS_ADDRESS", "TMPDIR", "HTTPS_PROXY", "ANTHROPIC_API_KEY", "FACTORY_DB"} {
		if _, has := env[gone]; has {
			t.Errorf("%s passed through", gone)
		}
	}
	for k := range passed {
		if k != "DISABLE_AUTOUPDATER" {
			wbEq(t, k, env[k], caller[k])
		}
	}
	wbEq(t, "HOME", env["HOME"], n.home)
	wbEq(t, "DISABLE_AUTOUPDATER", env["DISABLE_AUTOUPDATER"], "1")
	wbEq(t, "GIT_CONFIG_GLOBAL", env["GIT_CONFIG_GLOBAL"], n.d+"/gitconfig")
	wbEq(t, "oauth", env["CLAUDE_CODE_OAUTH_TOKEN"], wbOAuth)
	wbEq(t, "gh", env["GH_TOKEN"], wbGhRW)
}

func TestWorkerboxSandboxRolePicksItsGhToken(t *testing.T) {
	n := wbNewNS(t)
	for role, tok := range map[string]string{"reviewer": wbGhRO, "qa": wbGhRO, "coder": wbGhRW} {
		n.d = n.prepare(func(a *sandbox.PrepareAgentArgs) { a.Agent, a.Role, a.RwCwd = role+"-x", role, false })
		wbEq(t, role, wbEnvOf(n.p(wbOp("environ"))[0])["GH_TOKEN"], tok)
	}
}

func TestWorkerboxSandboxNoSecretLeak(t *testing.T) {
	n := wbNewNS(t)
	ops := []map[string]any{wbOp("cmdline"), wbOp("environ")}
	res := n.p(ops...)
	r := n.run(n.d, []map[string]any{wbOp("cmdline")}, "", nil)
	cmdline := wbStrs(res[0].Val)
	cj, _ := json.Marshal(cmdline)
	seen := []string{wbRead(t, n.d+"/bin/claude"), string(cj), r.Stdout, r.Stderr, wbRead(t, n.d+"/gitconfig"), wbRead(t, n.d+"/claude/.claude.json")}
	for _, tok := range []string{wbOAuth, wbGhRO, wbGhRW} {
		for _, text := range seen {
			wbHasNot(t, "seen text", text, tok)
		}
	}
	want, _ := json.Marshal(ops)
	wbEq(t, "last argument", cmdline[len(cmdline)-2], string(want)) // the last argument, then the trailing NUL
}

func TestWorkerboxSandboxNoCapabilitiesAndNoNewPrivs(t *testing.T) {
	n := wbNewNS(t)
	st := wbEnvOf(n.p(wbOp("status"))[0])
	capEff, err := strconv.ParseUint(strings.TrimSpace(st["CapEff"].(string)), 16, 64)
	wbMust(t, err)
	wbEq(t, "CapEff", capEff, uint64(0))
	wbEq(t, "NoNewPrivs", strings.TrimSpace(st["NoNewPrivs"].(string)), "1")
}

func TestWorkerboxSandboxAgentBinAndGitconfigAreImmutableFromInside(t *testing.T) {
	n := wbNewNS(t)
	d := n.d
	files := []string{d + "/bin/claude", d + "/gitconfig"}
	before := map[string]string{}
	for _, f := range files {
		before[f] = wbSha(t, f)
	}
	listing := wbNames(t, d)
	var ops []map[string]any
	for _, f := range files {
		third := wbOp("cwd")
		if strings.HasSuffix(strings.TrimSuffix(f, "/claude"), "/bin") {
			third = wbOp("write", "path", f+".new", "data", "evil")
		}
		ops = append(ops, wbOp("write", "path", f, "data", "evil"), wbOp("unlink", "path", f), third,
			wbOp("rename", "src", f, "dst", f+".old"))
	}
	ops = append(ops, wbOp("rename", "src", d+"/bin", "dst", d+"/bin2"), wbOp("rename", "src", d+"/gitconfig", "dst", d+"/gitconfig2"),
		wbOp("unlink", "path", d+"/gitconfig"))
	for i, r := range n.p(ops...) {
		if ops[i]["op"] == "cwd" {
			continue
		}
		b, _ := json.Marshal(ops[i])
		wbFail(t, string(b), r, "EROFS", "EBUSY", "EXDEV")
	}
	for _, f := range files {
		wbEq(t, "sha of "+f, wbSha(t, f), before[f])
	}
	wbEq(t, "agent dir", wbNames(t, d), listing)
	if fi, err := os.Stat(d + "/bin"); err != nil || !fi.IsDir() {
		t.Error("bin is not a directory any more")
	}
}

func TestWorkerboxSandboxTermsFileIsReadableAndImmutableAndSiblingsStayHidden(t *testing.T) {
	n := wbNewNS(t)
	terms := n.tokens + "/" + sandbox.TermsName
	before := wbSha(t, terms)
	res := n.p(wbOp("read", "path", terms), wbOp("write", "path", terms, "data", "evil"), wbOp("rename", "src", terms, "dst", terms+".old"),
		wbOp("unlink", "path", terms), wbOp("open", "path", n.tokens+"/gh-worker-ro-token"), wbOp("open", "path", n.tokens+"/worker-oauth-token"))
	if !res[0].Ok || res[0].Val != wbTermsBody {
		t.Errorf("terms read = %+v", res[0])
	}
	for _, r := range res[1:4] {
		wbFail(t, "terms write", r, "EROFS", "EBUSY", "EXDEV")
	}
	for _, r := range res[4:] {
		wbFail(t, "token sibling", r, "ENOENT")
	}
	wbEq(t, "terms sha", wbSha(t, terms), before)
	if fi, err := os.Stat(terms); err != nil || !fi.Mode().IsRegular() {
		t.Error("the terms file is gone")
	}
}

func TestWorkerboxSandboxMissingTermsFileStillStartsTheSandbox(t *testing.T) {
	n := wbNewNS(t)
	terms := n.tokens + "/" + sandbox.TermsName
	wbMust(t, os.Remove(terms))
	d := n.prepare(func(a *sandbox.PrepareAgentArgs) { a.Agent = "coder-t-2" })
	r := n.run(d, []map[string]any{wbOp("open", "path", terms)}, "", nil)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	wbFail(t, "open missing terms", r.Res[0], "ENOENT")
}

func TestWorkerboxSandboxNewFilesInAgentDirDoNotReachTheHost(t *testing.T) {
	n := wbNewNS(t)
	n.p(wbOp("write", "path", n.d+"/evil.sh"), wbOp("write", "path", n.d+"/bin/evil"))
	wbEq(t, "agent dir", wbNames(t, n.d), []string{"bin", "claude", "gitconfig"})
}

func TestWorkerboxSandboxPtyForegroundPidAndArgv(t *testing.T) {
	n := wbNewNS(t)
	argv := []string{"--session-id", "x", "--settings", "y"}
	out, pid := wbPty(t, append([]string{n.d + "/bin/claude"}, argv...), n.own, n.callerEnv(nil))
	var line string
	for _, l := range strings.Split(strings.ReplaceAll(out, "\r", ""), "\n") {
		if strings.HasPrefix(l, "PTY:") {
			line = l[4:]
			break
		}
	}
	if line == "" {
		t.Fatalf("no PTY line in %q", out)
	}
	var info struct {
		Pid, Pgid, Sid, Fg int
		Comm               string
		Argv               []string
	}
	wbMust(t, json.Unmarshal([]byte(line), &info))
	wbEq(t, "comm", info.Comm, "claude")
	wbEq(t, "pid", info.Pid, pid) // exec-only chain: claude keeps the shim's pid
	wbEq(t, "fg", info.Fg, info.Pgid)
	wbEq(t, "sid", info.Sid, pid) // setsid happened once, in the test: the shim chain did not start another session
	wbEq(t, "argv", info.Argv, argv)
}

// ---- OwnerIdentity: git identity resolved in the pane cwd with --includes -----------------------------------------

type wbIdentWorld struct{ tmp, home, repo string }

func wbNewIdentWorld(t *testing.T) wbIdentWorld {
	tmp := wbRealpath(t, t.TempDir())
	w := wbIdentWorld{tmp, tmp + "/home", tmp + "/tree/repo"}
	wbMust(t, os.MkdirAll(w.repo, 0o755))
	wbMust(t, os.Mkdir(w.home, 0o755))
	wbPut(t, w.home+"/.gitconfig", "[user]\n\tname = Default Test\n\temail = test@example.invalid\n"+
		"[includeIf \"gitdir:"+tmp+"/tree/repo/\"]\n\tpath = "+w.home+"/inc\n", 0)
	wbPut(t, w.home+"/inc", "[user]\n\tname = Noreply Test\n\temail = zz-include@example.invalid\n", 0)
	cmd := exec.Command("git", "init", "-q", w.repo)
	cmd.Env = []string{"HOME=" + w.home, "PATH=" + os.Getenv("PATH")}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	return w
}

func (w wbIdentWorld) owner(cwd string) [2]string {
	return [2]string{sandbox.GitValue(w.home, "user.name", cwd), sandbox.GitValue(w.home, "user.email", cwd)}
}

func TestWorkerboxOwnerIdentityCwdInsideIncludeifTreeGetsNoreply(t *testing.T) {
	w := wbNewIdentWorld(t)
	wbEq(t, "identity", w.owner(w.repo), [2]string{"Noreply Test", "zz-include@example.invalid"})
}

func TestWorkerboxOwnerIdentityCwdOutsideTreeGetsDefault(t *testing.T) {
	w := wbNewIdentWorld(t)
	wbEq(t, "identity", w.owner(w.tmp), [2]string{"Default Test", "test@example.invalid"})
}

func TestWorkerboxOwnerIdentityMissingOrNoneCwdFallsBackToRoot(t *testing.T) {
	w := wbNewIdentWorld(t)
	want := [2]string{"Default Test", "test@example.invalid"}
	wbEq(t, "none", w.owner(""), want)
	wbEq(t, "missing", w.owner(w.tmp+"/nope"), want)
}

// ---- Group C: the owner's real paths (read-only probes, nothing printed) -----------------------------------------

var (
	wbRealOnce  sync.Once
	wbRealRes   []wbRes
	wbRealOut   string
	wbRealErr   string
	wbRealSkip  string
	wbRealPaths map[string]string
)

// wbRealProbe is the Python setUpClass: one boxed run probing the owner's real paths, shared by the six cases.
func wbRealProbe(t *testing.T) {
	t.Helper()
	wbRealOnce.Do(func() {
		if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
			wbRealSkip = "user namespaces unavailable: " + why
			return
		}
		home := sandbox.RealHome()
		runtime := "/run/user/" + strconv.Itoa(os.Getuid())
		wbRealPaths = map[string]string{"creds": home + "/.claude/.credentials.json", "bus": runtime + "/bus",
			"control": home + "/" + wbControlRel, "agent": home + "/" + wbAgentRel}
		base := wbBase(t, "zz-test-workerbox-real-")
		wbPut(t, base+"/tokens/worker-oauth-token", wbOAuth, 0o600)
		wbPut(t, base+"/tokens/gh-worker-rw-token", wbGhRW, 0o600)
		wbLink(t, base+"/claude-bin/claude")
		wbLink(t, base+"/fh/bin/factory")
		wbMust(t, os.Mkdir(base+"/fh/db", 0o755))
		wbMust(t, os.MkdirAll(base+"/fh/worktrees/own", 0o755))
		id := wbIdent
		d, err := sandbox.PrepareAgent(sandbox.PrepareAgentArgs{Agent: "realpath-t", Role: "coder", Cwd: base + "/fh/worktrees/own",
			FactoryHome: base + "/fh", DBDir: base + "/fh/db", RealClaude: base + "/claude-bin/claude", RwCwd: false,
			TokenDir: base + "/tokens", WorkersRoot: base + "/workers", Identity: &id, GhSlugs: wbSlugs,
			Seams: sandbox.Opts{UID: os.Getuid(), GID: os.Getgid()}})
		wbMust(t, err)
		ops := []map[string]any{wbOp("open", "path", wbRealPaths["creds"])}
		for _, k := range []string{"bus", "control", "agent"} {
			ops = append(ops, wbOp("connect", "path", wbRealPaths[k]))
		}
		ops = append(ops, map[string]any{"op": "environ_of", "pid": os.Getpid()}, map[string]any{"op": "environ_of", "pid": os.Getppid()})
		js, _ := json.Marshal(ops)
		r := wbRunCmd(t, exec.Command(d+"/bin/claude", string(js)), base+"/fh/worktrees/own",
			[]string{"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "FACTORY_AGENT=realpath-t"}, "", nil)
		if r.Code != 0 || r.Res == nil {
			wbRealErr = "real-path probe: exit " + strconv.Itoa(r.Code) + ": " + r.Stderr
			return
		}
		wbRealRes, wbRealOut = r.Res, r.Stdout+"\x00"+r.Stderr
	})
	if wbRealSkip != "" {
		t.Skip(wbRealSkip)
	}
	if wbRealErr != "" {
		t.Fatal(wbRealErr)
	}
}

func wbRealRefused(t *testing.T, i int, allowed ...string) {
	t.Helper()
	if len(allowed) == 0 {
		allowed = []string{"ENOENT", "EACCES", "EPERM", "ECONNREFUSED"}
	}
	wbFail(t, "real path "+strconv.Itoa(i), wbRealRes[i], allowed...)
}

func TestWorkerboxRealPathRealCredentialsNotOpenable(t *testing.T) {
	wbRealProbe(t)
	if !wbExists(wbRealPaths["creds"]) {
		t.Skip("no real ~/.claude/.credentials.json on this box")
	}
	wbRealRefused(t, 0)
}

func TestWorkerboxRealPathRealUserBusRefusesConnect(t *testing.T) {
	wbRealProbe(t)
	if !wbExists(wbRealPaths["bus"]) {
		t.Skipf("no %s on this box", wbRealPaths["bus"])
	}
	wbRealRefused(t, 1)
}

func TestWorkerboxRealPathRealControlSocketRefusesConnect(t *testing.T) {
	wbRealProbe(t)
	if !wbExists(wbRealPaths["control"]) {
		t.Skip("no multiplexer control socket on this box")
	}
	wbRealRefused(t, 2)
}

func TestWorkerboxRealPathRealAgentProxySocketRefusesConnect(t *testing.T) {
	wbRealProbe(t)
	if !wbExists(wbRealPaths["agent"]) {
		t.Skip("no agent-proxy socket on this box")
	}
	wbRealRefused(t, 3)
}

func TestWorkerboxRealPathOutsideProcessEnvironIsDenied(t *testing.T) {
	wbRealProbe(t)
	if !wbExists("/proc/" + strconv.Itoa(os.Getpid()) + "/environ") {
		t.Fatal("the test runner has no /proc environ")
	}
	wbRealRefused(t, 4, "EACCES", "EPERM") // the test runner
	wbRealRefused(t, 5, "EACCES", "EPERM") // its parent
}

func TestWorkerboxRealPathOutputHasNoSecretContent(t *testing.T) {
	wbRealProbe(t)
	for _, text := range strings.Split(wbRealOut, "\x00") {
		for _, tok := range []string{wbOAuth, wbGhRW, "fake-credentials"} {
			wbHasNot(t, "probe output", text, tok)
		}
	}
}
