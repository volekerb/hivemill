package sandbox1

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"factory/go/internal/sandbox"
)

// Port of tests/test_workerbox.py (82 cases; see ported_sandbox1.txt). The workerbox_*_test.go files hold the groups
// AgentDir (A), TokenFail (B1), Sandbox (B2), OwnerIdentity, RealPath (C) and the Go toolchain cases. The Python
// probe script and fake `factory` are the test binary itself (workerbox_helpers_test.go).
//
// Differences from the Python module, none of them a dropped case:
//   - the shim is `#!/bin/sh` + `exec <self> worker-shim '<json>'` instead of a Python script, so "starts with
//     #!<python> -I" is "starts with #!/bin/sh" and the baked cfg is read from that JSON;
//   - CLAUDE_JSON_MAX is the constant sandbox.ClaudeJSONMax (16 MiB) and cannot be patched, so the two cap cases use
//     files of exactly that size (one byte over, and exactly at the cap) instead of a 1024-byte cap;
//   - test_foreign_owner mocks os.getuid in Python; here a root-owned 0600 file stands in for the token (skipped when
//     the box has none), run through `<self> worker-shim`.
// not ported: none.

func wbShimNS(t testing.TB, dir string) map[string]any {
	t.Helper()
	ns, _ := wbShimJSON(t, dir)["ns"].(map[string]any)
	if ns == nil {
		t.Fatal("shim cfg has no ns")
	}
	return ns
}

func wbClaudeJSON(t testing.TB, path string) map[string]any {
	t.Helper()
	var m map[string]any
	wbMust(t, json.Unmarshal([]byte(wbRead(t, path)), &m))
	return m
}

// ---- Group A: PrepareAgent without any namespace ----------------------------------------------------------------

func TestWorkerboxAgentDirSandboxableRoles(t *testing.T) {
	w := wbNewWorld(t)
	var roles []string
	for r := range sandbox.Sandboxable {
		roles = append(roles, r)
	}
	if len(roles) != 4 || !sandbox.Sandboxable["reviewer"] || !sandbox.Sandboxable["qa"] || !sandbox.Sandboxable["coder"] || !sandbox.Sandboxable["researcher"] {
		t.Fatalf("Sandboxable = %v", roles)
	}
	p := sandbox.PathsFor(w.home, "", "")
	for _, r := range roles { // ROLE_GH has the same four keys
		if p.GhTokenFor(r) == "" {
			t.Errorf("no gh token for %s", r)
		}
	}
	for _, bad := range []string{"pm", "judge"} {
		if _, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Role = bad }); err == nil {
			t.Errorf("role %s was accepted", bad)
		}
	}
}

func TestWorkerboxAgentDirConstantsComeFromThePasswdHome(t *testing.T) {
	home := sandbox.RealHome()
	p := sandbox.PathsFor(home, "", "")
	wbEq(t, "workers", p.Workers, home+"/.local/state/factory/workers")
	wbEq(t, "oauth", p.OAuth, home+"/.config/factory/worker-oauth-token")
	wbEq(t, "gh ro", p.GhRO, home+"/.config/factory/gh-worker-ro-token")
	wbEq(t, "gh rw", p.GhRW, home+"/.config/factory/gh-worker-rw-token")
	for role, want := range map[string]string{"reviewer": p.GhRO, "qa": p.GhRO, "coder": p.GhRW, "researcher": p.GhRO} {
		wbEq(t, "token of "+role, p.GhTokenFor(role), want)
	}
}

func TestWorkerboxAgentDirBadAgentNamesAreRefused(t *testing.T) {
	w := wbNewWorld(t)
	for _, bad := range []string{"../x", "a/b", "", ".", "..", "a b", "x\n", "-x", strings.Repeat("a", 65)} {
		if _, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Agent = bad }); err == nil {
			t.Errorf("agent %q was accepted", bad)
		}
	}
	d := w.prepare(func(a *sandbox.PrepareAgentArgs) { a.Agent = "coder-t-0161" })
	wbEq(t, "agent dir name", filepath.Base(d), "coder-t-0161")
	if wbExists(w.home + "/.local/state/factory/x") {
		t.Error("a refused name created a directory")
	}
}

func TestWorkerboxAgentDirLayoutAndShim(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	wbEq(t, "agent dir", d, w.home+"/.local/state/factory/workers/coder-t-1")
	wbEq(t, "shim mode", wbMode(t, d+"/bin/claude"), os.FileMode(0o755))
	text := wbRead(t, d+"/bin/claude")
	if !strings.HasPrefix(text, "#!/bin/sh\nexec ") {
		t.Errorf("shim = %q", text)
	}
	for _, tok := range []string{wbOAuth, wbGhRO, wbGhRW} {
		wbHasNot(t, "shim", text, tok)
	}
	wbEq(t, "claude dir mode", wbMode(t, d+"/claude"), os.FileMode(0o700))
	wbEq(t, "entries", wbNames(t, d), []string{"bin", "claude", "gitconfig"})
}

func TestWorkerboxAgentDirCfgEmbeddedInShimIsTheAllowView(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(func(a *sandbox.PrepareAgentArgs) { a.RwCwd = false })
	sc := wbShimJSON(t, d)
	ns := sc["ns"].(map[string]any)
	wbEq(t, "mode", ns["mode"], "allow")
	wbEq(t, "net", ns["net"], true)
	wbEq(t, "pid", ns["pid"], false)
	if ns["forward_sock"] != nil {
		t.Errorf("forward_sock = %v", ns["forward_sock"])
	}
	wbEq(t, "trial", ns["trial"], d+"/claude")
	scrub := wbStrs(ns["scrub"])
	if wbIn(scrub, "CLAUDE_CODE_OAUTH_TOKEN") || wbIn(scrub, "GH_TOKEN") {
		t.Errorf("scrub = %v", scrub)
	}
	wbEq(t, "rw", wbStrs(ns["rw"]), []string{w.fh + "/db", d + "/claude"})
	ro := wbStrs(ns["ro"])
	for _, p := range []string{w.own, w.fh, w.home + "/.local/bin/factory"} {
		if !wbIn(ro, p) {
			t.Errorf("%s not in ro %v", p, ro)
		}
	}
	wbEq(t, "masks", wbStrs(ns["masks"]), []string{w.fh + "/worktrees"})
	wbEq(t, "tmpfs_sizes", ns["tmpfs_sizes"], map[string]any{wbRealpath(t, "/tmp"): "512m", w.home: "64m"})
	if _, has := ns["tmpfs_size"]; has {
		t.Errorf("tmpfs_size = %v, want it absent", ns["tmpfs_size"])
	}
	wbEq(t, "gh_token", sc["gh_token"], w.tokens+"/gh-worker-rw-token")
	wbEq(t, "oauth_token", sc["oauth_token"], w.tokens+"/worker-oauth-token")
}

func TestWorkerboxAgentDirTermsFileIsTheOneExtraRoPath(t *testing.T) {
	w := wbNewWorld(t)
	terms := w.tokens + "/" + sandbox.TermsName
	ro := wbStrs(wbShimNS(t, w.prepare(func(a *sandbox.PrepareAgentArgs) { a.RwCwd = false }))["ro"])
	if !wbIn(ro, terms) || wbIn(ro, w.tokens) {
		t.Errorf("ro = %v", ro)
	}
	wbMust(t, os.Remove(terms))
	ro = wbStrs(wbShimNS(t, w.prepare(func(a *sandbox.PrepareAgentArgs) { a.RwCwd = false }))["ro"])
	if wbIn(ro, terms) {
		t.Errorf("a missing terms file is still in ro: %v", ro)
	}
}

func TestWorkerboxAgentDirSymlinkedTermsFileIsRefused(t *testing.T) {
	w := wbNewWorld(t)
	terms := w.tokens + "/" + sandbox.TermsName
	wbMust(t, os.Remove(terms))
	wbMust(t, os.Symlink(w.fh+"/roles/settings.json", terms))
	_, err := w.prepareWith(nil)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("err = %v, want a symlink refusal", err)
	}
}

func wbGitList(t testing.TB, file string) []string {
	t.Helper()
	out, err := exec.Command("git", "config", "-f", file, "--list").Output()
	wbMust(t, err)
	return strings.Split(strings.TrimRight(string(out), "\n"), "\n")
}

func wbKeys(lines []string) []string {
	var k []string
	for _, l := range lines {
		k = append(k, strings.SplitN(l, "=", 2)[0])
	}
	return k
}

func TestWorkerboxAgentDirGitconfigHasOnlyIdentityAndTheCredentialHelper(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	out := wbGitList(t, d+"/gitconfig")
	wbEq(t, "keys", wbKeys(out), []string{"user.name", "user.email", "credential.usehttppath", "credential.https://github.com.helper"})
	wbEq(t, "name", out[0], "user.name=Test Owner")
	wbEq(t, "usehttppath", out[2], "credential.usehttppath=true")
	self, err := os.Executable()
	wbMust(t, err)
	helper, err := sandbox.WorkerGhHelper(self, wbSlugs)
	wbMust(t, err)
	wbEq(t, "helper", strings.SplitN(out[3], "=", 2)[1], helper)
	cmd := exec.Command("git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=github.com\npath=volekerb/hivemill\n\n")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_CONFIG_GLOBAL=" + d + "/gitconfig", "GH_TOKEN=tok-x",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0"}
	b, _ := cmd.Output() // a failing helper is caught by the assertions below
	wbHas(t, "credential fill", string(b), "password=tok-x")
	wbHas(t, "credential fill", string(b), "username=x-access-token")
}

func TestWorkerboxAgentDirNoScopedHelperWithoutValidRepoSlugs(t *testing.T) {
	w := wbNewWorld(t)
	for _, bad := range [][]string{nil, {}, {"bad slug"}, {"no-slash"}} {
		_, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.GhSlugs = bad })
		if err == nil || !strings.Contains(err.Error(), "worker_gh_repos") {
			t.Errorf("slugs %q: err = %v", bad, err)
		}
	}
	if wbExists(w.home + "/.local/state/factory/workers/coder-t-1/gitconfig") {
		t.Error("a gitconfig was written without a valid helper")
	}
}

func TestWorkerboxAgentDirGitconfigWithoutIdentityHasJustTheHelper(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(func(a *sandbox.PrepareAgentArgs) { a.Identity = &[2]string{"", ""} })
	wbEq(t, "keys", wbKeys(wbGitList(t, d+"/gitconfig")), []string{"credential.usehttppath", "credential.https://github.com.helper"})
}

func TestWorkerboxAgentDirClaudeJSONSeededOnceAndCwdEntryAdded(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	path := d + "/claude/.claude.json"
	seed := wbClaudeJSON(t, path)
	wbEq(t, "onboarding", seed["hasCompletedOnboarding"], true)
	wbEq(t, "theme", seed["theme"], "dark")
	wbEq(t, "projects", seed["projects"], map[string]any{w.own: map[string]any{"hasTrustDialogAccepted": true, "hasCompletedProjectOnboarding": true}})
	wbEq(t, "mode", wbMode(t, path), os.FileMode(0o600))
	seed["sessionNote"] = "written by claude"
	b, _ := json.Marshal(seed)
	wbMust(t, os.WriteFile(path, b, 0o600))
	w.prepare(nil) // same cwd: not touched
	wbEq(t, "note", wbClaudeJSON(t, path)["sessionNote"], "written by claude")
	w.prepare(func(a *sandbox.PrepareAgentArgs) { a.Cwd = w.fh + "/worktrees/other" }) // new cwd: entry added, the rest kept
	now := wbClaudeJSON(t, path)
	wbEq(t, "note kept", now["sessionNote"], "written by claude")
	proj := now["projects"].(map[string]any)
	if len(proj) != 2 || proj[w.own] == nil || proj[w.fh+"/worktrees/other"] == nil {
		t.Errorf("projects = %v", proj)
	}
}

// T-1291: the trust entry is under <CLAUDE_CONFIG_DIR>/.claude.json, key = the realpath cwd of the namespace.
func TestWorkerboxAgentDirTrustEntryIsUnderTheFileAndKeyClaudeReads(t *testing.T) {
	w := wbNewWorld(t)
	owner := wbPut(t, w.home+"/.claude.json", `{"projects": {}}`, 0)
	before := wbSha(t, owner)
	d := w.prepare(nil)
	env := sandbox.BuildEnv(map[string]string{}, w.home, d+"/claude", "g", "o", "t", nil)
	path := env["CLAUDE_CONFIG_DIR"] + "/.claude.json"
	key := wbShimNS(t, d)["cwd"].(string)
	wbEq(t, "cwd key", key, wbRealpath(t, w.own))
	proj := wbClaudeJSON(t, path)["projects"].(map[string]any)
	wbEq(t, "trust", proj[key].(map[string]any)["hasTrustDialogAccepted"], true)
	if len(proj) != 1 { // the pane cwd only: no wildcard, no parent
		t.Errorf("projects = %v", proj)
	}
	wbEq(t, "owner sha", wbSha(t, owner), before) // the owner's ~/.claude.json is not touched by the sandbox path
}

func TestWorkerboxAgentDirExistingUntrustedEntryForTheCwdGetsTrust(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	path := d + "/claude/.claude.json"
	cur := wbClaudeJSON(t, path)
	proj := cur["projects"].(map[string]any)
	proj[w.own] = map[string]any{"hasTrustDialogAccepted": false, "lastCost": 1}
	proj["/elsewhere"] = map[string]any{"hasTrustDialogAccepted": false}
	b, _ := json.Marshal(cur)
	wbMust(t, os.WriteFile(path, b, 0o600))
	w.prepare(nil)
	now := wbClaudeJSON(t, path)["projects"].(map[string]any)
	wbEq(t, "own entry", now[w.own], map[string]any{"hasTrustDialogAccepted": true, "lastCost": float64(1), "hasCompletedProjectOnboarding": true})
	wbEq(t, "other entry", now["/elsewhere"], map[string]any{"hasTrustDialogAccepted": false})
}

func TestWorkerboxAgentDirClaudeDirIsKeptAcrossCalls(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	wbPut(t, d+"/claude/session.jsonl", "kept", 0)
	w.prepare(nil)
	wbEq(t, "session", wbRead(t, d+"/claude/session.jsonl"), "kept")
}

func TestWorkerboxAgentDirSymlinkedClaudeJSONIsReplacedAndItsTargetUntouched(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	target := wbPut(t, w.base+"/outside.txt", "owner file\n", 0)
	before := wbSha(t, target)
	link := d + "/claude/.claude.json"
	wbMust(t, os.Remove(link))
	wbMust(t, os.Symlink(target, link))
	w.prepare(nil)
	wbEq(t, "target sha", wbSha(t, target), before)
	fi, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		t.Error(".claude.json is still a symlink")
	}
	wbEq(t, "onboarding", wbClaudeJSON(t, link)["hasCompletedOnboarding"], true)
}

func TestWorkerboxAgentDirSymlinkToAJSONFileIsNotFollowedEither(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	target := wbPut(t, w.base+"/outside.json", `{"projects": {}, "mine": 1}`, 0)
	before := wbSha(t, target)
	link := d + "/claude/.claude.json"
	wbMust(t, os.Remove(link))
	wbMust(t, os.Symlink(target, link))
	w.prepare(func(a *sandbox.PrepareAgentArgs) { a.Cwd = w.fh + "/worktrees/other" })
	wbEq(t, "target sha", wbSha(t, target), before)
	if _, has := wbClaudeJSON(t, link)["mine"]; has {
		t.Error("the symlink target's content was read")
	}
}

// wbBigClaudeJSON is a valid .claude.json of exactly n bytes.
func wbBigClaudeJSON(n int) string {
	head, tail := `{"projects": {}, "state": "`, `"}`
	return head + strings.Repeat("x", n-len(head)-len(tail)) + tail
}

func TestWorkerboxAgentDirOversizedClaudeJSONIsRefusedAndKept(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	path := d + "/claude/.claude.json"
	wbMust(t, os.WriteFile(path, []byte(wbBigClaudeJSON(sandbox.ClaudeJSONMax+1)), 0o600))
	before := wbSha(t, path)
	_, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Cwd = w.fh + "/worktrees/other" })
	if err == nil || !strings.Contains(err.Error(), "larger than "+strconv.Itoa(sandbox.ClaudeJSONMax)) {
		t.Fatalf("err = %v", err)
	}
	wbEq(t, "sha", wbSha(t, path), before)
}

func TestWorkerboxAgentDirClaudeJSONJustUnderTheCapIsRead(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	path := d + "/claude/.claude.json"
	wbMust(t, os.WriteFile(path, []byte(wbBigClaudeJSON(sandbox.ClaudeJSONMax)), 0o600))
	w.prepare(func(a *sandbox.PrepareAgentArgs) { a.Cwd = w.fh + "/worktrees/other" })
	st := wbClaudeJSON(t, path)["state"].(string)
	wbEq(t, "state kept", len(st) > 1<<20, true)
}

func TestWorkerboxAgentDirDirectoryAtClaudeJSONIsRefused(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	wbMust(t, os.Remove(d+"/claude/.claude.json"))
	wbMust(t, os.Mkdir(d+"/claude/.claude.json", 0o755))
	if _, err := w.prepareWith(nil); err == nil {
		t.Fatal("a directory at .claude.json was accepted")
	}
}

func TestWorkerboxAgentDirSymlinkedClaudeDirIsRefused(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	other := w.base + "/elsewhere"
	wbMust(t, os.Mkdir(other, 0o755))
	wbMust(t, os.RemoveAll(d+"/claude"))
	wbMust(t, os.Symlink(other, d+"/claude"))
	if _, err := w.prepareWith(nil); err == nil {
		t.Fatal("a symlinked claude dir was accepted")
	}
	wbEq(t, "target", wbNames(t, other), []string{})
}

func TestWorkerboxAgentDirSymlinkPlantedAtShimOrGitconfigIsReplacedNotFollowed(t *testing.T) {
	w := wbNewWorld(t)
	d := w.prepare(nil)
	target := wbPut(t, w.base+"/victim.txt", "victim\n", 0)
	before := wbSha(t, target)
	for _, p := range []string{d + "/bin/claude", d + "/gitconfig"} {
		wbMust(t, os.Remove(p))
		wbMust(t, os.Symlink(target, p))
	}
	w.prepare(nil)
	wbEq(t, "victim sha", wbSha(t, target), before)
	for _, p := range []string{d + "/bin/claude", d + "/gitconfig"} {
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink != 0 {
			t.Errorf("%s is still a symlink (%v)", p, err)
		}
	}
}

func TestWorkerboxAgentDirCwdEqualOrAboveProtectedTreesIsRefused(t *testing.T) {
	w := wbNewWorld(t)
	for _, bad := range []string{w.home, w.fh, "/tmp", "/", "/dev/shm", filepath.Dir(w.home), w.fh + "/bin", w.fh + "/db",
		w.home + "/.ssh", w.fh + "/worktrees", w.home + "/other", "/tmp/anything-here"} {
		if _, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Cwd = bad }); err == nil {
			t.Errorf("cwd %s was accepted", bad)
		}
		if wbExists(w.home + "/.local/state/factory") {
			t.Fatalf("a refused cwd (%s) must not create anything", bad)
		}
	}
}

func TestWorkerboxAgentDirCwdRealHomeIsRefusedWithoutOverrides(t *testing.T) {
	w := wbNewWorld(t)
	for _, bad := range []string{sandbox.RealHome(), "/tmp"} {
		_, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Cwd, a.Home, a.Runtime, a.WorkersRoot = bad, "", "", w.base+"/wr" })
		if err == nil {
			t.Errorf("cwd %s was accepted", bad)
		}
	}
}

func TestWorkerboxAgentDirCwdUnderWorktreesOutsideAndSymlinksAreResolved(t *testing.T) {
	w := wbNewWorld(t)
	outside := w.base + "/plain-clone"
	wbMust(t, os.Mkdir(outside, 0o755))
	if d, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Cwd = outside }); err != nil || d == "" {
		t.Fatalf("outside clone: %q %v", d, err)
	}
	evil := w.base + "/evil-link"
	wbMust(t, os.Symlink(w.fh+"/bin", evil))
	if _, err := w.prepareWith(func(a *sandbox.PrepareAgentArgs) { a.Cwd = evil }); err == nil {
		t.Fatal("a symlink into the factory bin dir was accepted")
	}
}

// ---- Group B1: the shim's token rules -----------------------------------------------------------------------------

type wbTokWorld struct {
	*wbWorld
	tokdir string
}

func wbNewTokWorld(t *testing.T) *wbTokWorld {
	w := wbNewWorld(t)
	tw := &wbTokWorld{wbWorld: w, tokdir: w.base + "/tokens"}
	wbMust(t, os.Mkdir(tw.tokdir, 0o755))
	for name, tok := range map[string]string{"worker-oauth-token": wbOAuth, "gh-worker-ro-token": wbGhRO, "gh-worker-rw-token": wbGhRW} {
		wbPut(t, tw.tokdir+"/"+name, tok+"\n", 0o600)
	}
	return tw
}

func (tw *wbTokWorld) shim(role string) string {
	return tw.prepare(func(a *sandbox.PrepareAgentArgs) { a.Role, a.TokenDir = role, tw.tokdir })
}

func (tw *wbTokWorld) assertExit3(d, path string) {
	tw.t.Helper()
	r := tw.run(d, nil, "", nil)
	if r.Code != 3 {
		tw.t.Fatalf("exit %d, want 3: %s", r.Code, r.Stderr)
	}
	wbHas(tw.t, "stderr", r.Stderr, path)
	wbHasNot(tw.t, "stdout", r.Stdout, "RAN")
	if r.Res != nil {
		tw.t.Error("the probe ran")
	}
	for _, tok := range []string{wbOAuth, wbGhRO, wbGhRW} {
		wbHasNot(tw.t, "output", r.Stdout+r.Stderr, tok)
	}
}

func TestWorkerboxTokenFail0600OwnedFilesStartIt(t *testing.T) {
	wbNeedUserns(t)
	tw := wbNewTokWorld(t)
	r := tw.run(tw.shim("coder"), nil, "", nil)
	if r.Code != 0 {
		t.Fatalf("exit %d: %s", r.Code, r.Stderr)
	}
	wbHas(t, "stdout", r.Stdout, "RAN")
}

func TestWorkerboxTokenFailMissingOauthToken(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	wbMust(t, os.Remove(tw.tokdir+"/worker-oauth-token"))
	tw.assertExit3(d, tw.tokdir+"/worker-oauth-token")
}

func TestWorkerboxTokenFailMissingGhTokenOfTheRole(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	wbMust(t, os.Remove(tw.tokdir+"/gh-worker-rw-token"))
	tw.assertExit3(d, tw.tokdir+"/gh-worker-rw-token")
}

func TestWorkerboxTokenFailReviewerNeedsTheRoTokenNotTheRwOne(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("reviewer")
	wbMust(t, os.Remove(tw.tokdir+"/gh-worker-ro-token"))
	tw.assertExit3(d, tw.tokdir+"/gh-worker-ro-token")
}

func TestWorkerboxTokenFailMode0644(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	wbMust(t, os.Chmod(tw.tokdir+"/worker-oauth-token", 0o644))
	tw.assertExit3(d, tw.tokdir+"/worker-oauth-token")
}

func TestWorkerboxTokenFailGhTokenMode0640(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	wbMust(t, os.Chmod(tw.tokdir+"/gh-worker-rw-token", 0o640))
	tw.assertExit3(d, tw.tokdir+"/gh-worker-rw-token")
}

func TestWorkerboxTokenFailSymlink(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	real := wbPut(t, tw.base+"/real-token", wbOAuth, 0o600)
	wbMust(t, os.Remove(tw.tokdir+"/worker-oauth-token"))
	wbMust(t, os.Symlink(real, tw.tokdir+"/worker-oauth-token"))
	tw.assertExit3(d, tw.tokdir+"/worker-oauth-token")
}

func TestWorkerboxTokenFailEmptyTokenFile(t *testing.T) {
	tw := wbNewTokWorld(t)
	d := tw.shim("coder")
	wbPut(t, tw.tokdir+"/gh-worker-rw-token", "\n", 0o600)
	tw.assertExit3(d, tw.tokdir+"/gh-worker-rw-token")
}

// The Python test mocks os.getuid, which a Go process cannot do. The same refusal is reached through the real entry
// point with a 0600 regular file owned by another uid (root): a token file of a foreign owner.
func TestWorkerboxTokenFailForeignOwner(t *testing.T) {
	tw := wbNewTokWorld(t)
	var foreign string
	for _, pat := range []string{"/etc/ssh/ssh_host_*_key", "/etc/ssl/private/*", "/etc/*-", "/etc/sudoers"} {
		m, _ := filepath.Glob(pat)
		for _, p := range m {
			fi, err := os.Lstat(p)
			if st, ok := fi.Sys().(*syscall.Stat_t); err == nil && ok && fi.Mode().IsRegular() && fi.Mode().Perm() == 0o600 && int(st.Uid) != os.Getuid() {
				foreign = p
			}
		}
	}
	if foreign == "" {
		t.Skip("no 0600 regular file owned by another uid on this box")
	}
	self, err := os.Executable()
	wbMust(t, err)
	ns := &sandbox.Cfg{}
	sc := &sandbox.ShimCfg{NS: ns, Home: tw.home, ConfigDir: tw.home + "/c", Gitconfig: tw.home + "/g", OAuthToken: foreign,
		GhToken: tw.tokdir + "/gh-worker-rw-token", Role: "coder"}
	js, err := json.Marshal(sc)
	wbMust(t, err)
	r := wbRunCmd(t, exec.Command(self, "worker-shim", string(js)), "", nil, tw.own, tw.callerEnv(nil))
	if r.Code != 3 {
		t.Fatalf("exit %d, want 3: %s%s", r.Code, r.Stdout, r.Stderr)
	}
	wbHas(t, "stderr", r.Stderr, foreign)
	wbHasNot(t, "stdout", r.Stdout, "RAN")
	for _, tok := range []string{wbOAuth, wbGhRO, wbGhRW} {
		wbHasNot(t, "stderr", r.Stderr, tok)
	}
}
