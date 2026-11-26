package guard

// The guard2 cases of go/internal/factorytest (researcher, retro_mirror, retro, scratch_init, uv_wrapper; T-1921,
// guard port 2b): every TestG2<name> is the old Test<name> with the same inputs and the same expected answers, but it
// calls HookMain in-process (g2_helpers_test.go) instead of the built binary. The map is
// testdata/portmap/guard2b.txt.

import (
	"os"
	"os/user"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// g2bWrite writes content to path, creating the parent directories.
func g2bWrite(t testing.TB, path, content string) {
	t.Helper()
	g2Mkdirs(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---- from go/internal/factorytest/guard2/researcher_test.go ----

// Port of tests/test_worker_guard_researcher.py (G-029 T-0846): the researcher role through the real worker-guard hook.
// A researcher is a no-push role that may clone a public https repo into its research dir
// (<state>/factory/research/<name>) and write only there or in its scratchpad.

const g2bRsURL = "https://github.com/octocat/Hello-World.git"
const g2bRsAt = "@"

type g2bRsEnv struct {
	t       *testing.T
	tmp     string
	state   string
	rdir    string
	scratch string
	home    string
	live    string
}

func g2bNewRs(t *testing.T) *g2bRsEnv {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	e := &g2bRsEnv{t: t, tmp: g2RealTemp(t), home: u.HomeDir}
	e.live = e.home + "/factory"
	e.state = filepath.Join(e.tmp, "state")
	e.rdir = filepath.Join(e.state, "factory", "research", "T-9999")
	e.scratch = filepath.Join(e.tmp, "scratch")
	g2Mkdirs(t, e.rdir, e.scratch)
	return e
}

func (e *g2bRsEnv) hook(payload map[string]any, role string) g2Res {
	e.t.Helper()
	return g2HookJSON(e.t, payload, g2Opts{args: g2RoleArgs(role),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_HOME"},
		set:  map[string]string{"WORKER_GUARD_STATE": e.state, "WORKER_GUARD_SCRATCH": e.scratch}})
}

func (e *g2bRsEnv) bash(cmd, cwd, role string) g2Res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.rdir
	}
	if role == "" {
		role = "researcher"
	}
	return e.hook(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}, "cwd": cwd, "session_id": "s1"}, role)
}

func (e *g2bRsEnv) tool(name, path, cwd, key string) g2Res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.rdir
	}
	if key == "" {
		key = "file_path"
	}
	return e.hook(map[string]any{"tool_name": name, "tool_input": map[string]any{key: path}, "cwd": cwd, "session_id": "s1"}, "researcher")
}

func (e *g2bRsEnv) ok(what string, r g2Res) {
	e.t.Helper()
	if r.code != 0 {
		e.t.Errorf("%s: exit %d, want 0: %s", what, r.code, r.stderr)
	}
}

func (e *g2bRsEnv) deny(what string, r g2Res) {
	e.t.Helper()
	if r.code != 2 {
		e.t.Errorf("should block %s: exit %d (stderr %q)", what, r.code, r.stderr)
	}
}

func (e *g2bRsEnv) symlink(target, link string) {
	e.t.Helper()
	if err := os.Symlink(target, link); err != nil {
		e.t.Fatal(err)
	}
}

func TestG2ResearcherGhApiNonGetDenied(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"gh api -XPOST repos/a/b/issues", "gh api --method=POST x", "gh api -X=post x", "gh api x -f title=t",
		"gh api x -F a=1", "gh api x --input f.json", "gh api -X PATCH x", "gh api -X DELETE x",
		"gh api x --field a=1", "gh api x --raw-field a=b", "gh api -iXPOST x", "gh api -if a=1 x",
		"gh api --method=get x -f a=1", "gh api -X GET -X POST x", "gh api --method put x",
		"gh -X GET api x", "gh --method GET api repos/a/b", "gh -H 'Accept: x' api repos/a/b"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherGhApiGetAllowed(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"gh api repos/a/b", "gh api -X GET repos/a/b", "gh api --method=GET repos/a/b/pulls?state=open",
		"gh api -XGET x", "gh api -X=get x", "gh api --method get x", "gh api x --jq .name"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherCloneAllowedPublicClone(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"git clone --depth 1 " + g2bRsURL + " hello",
		"git clone --depth=1 --quiet " + g2bRsURL + " hello",
		"git clone --branch v1 --single-branch --filter=blob:none --no-tags " + g2bRsURL + " hello",
		"git clone -q -b main -- " + g2bRsURL + " sub/hello",
		"git clone " + g2bRsURL + " " + e.rdir + "/abs",
		"cd sub && git clone " + g2bRsURL + " hello"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherCloneAllowedIntoScratchpad(t *testing.T) {
	e := g2bNewRs(t)
	c1 := "git clone " + g2bRsURL + " here"
	e.ok(c1, e.bash(c1, e.scratch, ""))
	c2 := "git clone " + g2bRsURL + " " + e.scratch + "/x"
	e.ok(c2, e.bash(c2, "", ""))
}

func TestG2ResearcherCloneAllowedReadOnlyGitInClone(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"git -C hello log -1", "git -C hello status", "git log -1", "git fetch origin"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherCloneBlockedAbuse(t *testing.T) {
	e := g2bNewRs(t)
	const g = "git clone https://github.com/a/b "
	for _, cmd := range []string{"git clone ext::sh -c x d", "git clone file:///home x", "git clone /srv/other/repo x",
		"git clone ssh://github.com/a/b x", "git clone git" + g2bRsAt + "github.com:a/b x",
		"git clone https://u:p" + g2bRsAt + "github.com/a/b x",
		"git clone http://github.com/a/b x", "git clone https://127.0.0.1/a/b x",
		"git clone https://localhost/a/b x",
		"git clone -c core.hooksPath=x https://github.com/a/b d",
		"git clone --config core.hooksPath=x https://github.com/a/b d",
		"git clone --template=/t https://github.com/a/b d",
		"git clone --upload-pack=x https://github.com/a/b d",
		"git clone -u x https://github.com/a/b d",
		"git clone --recurse-submodules https://github.com/a/b d",
		"git clone --separate-git-dir=/x https://github.com/a/b d",
		"git clone --reference /x https://github.com/a/b d",
		"git clone --shared https://github.com/a/b d",
		"git clone --dep 1 https://github.com/a/b d",
		g + "../../escape",
		g + "sub/../../escape",
		g + e.live + "/x",
		g + "/tmp/x",
		g + "~/x",
		g + "$D",
		"git clone https://github.com/a/b",
		g + "d e", g + "d*",
		"git clone --branch -x https://github.com/a/b d",
		"git -C " + e.live + " clone https://github.com/a/b d",
		"git -c alias.clone=push clone https://github.com/a/b d"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherCloneBlockedWrappersAndVariants(t *testing.T) {
	e := g2bNewRs(t)
	c := "git clone -c core.hooksPath=x " + g2bRsURL + " d"
	for _, cmd := range []string{c, "true; " + c, "true && " + c, "echo $(" + c + ")", "env " + c, "nohup " + c, "timeout 5 " + c,
		"/usr/bin/" + c, "./" + c, "bash -c '" + c + "'", `zsh -c "` + c + `"`, "'gi" + "t' clone ext::sh d",
		"git clone " + g2bRsURL + " ../x", "nohup git clone " + g2bRsURL + " /tmp/x", "env git clone ext::sh d"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherCloneBlockedOutsideResearchCwd(t *testing.T) {
	e := g2bNewRs(t)
	for _, cwd := range []string{e.tmp, e.live, e.home, filepath.Join(e.state, "factory", "research"), "/tmp"} {
		e.deny(cwd, e.bash("git clone "+g2bRsURL+" hello", cwd, ""))
	}
}

func TestG2ResearcherCloneBlockedSymlinkEscape(t *testing.T) {
	e := g2bNewRs(t)
	e.symlink(e.home, filepath.Join(e.rdir, "link"))
	e.deny("link/x", e.bash("git clone "+g2bRsURL+" link/x", "", ""))
}

func TestG2ResearcherCloneBlockedAfterFsChangeOnSameLine(t *testing.T) {
	e := g2bNewRs(t)
	// B1: a symlink/move/clone planted earlier on the line relocates the clone outside the research dir
	for _, cmd := range []string{"ln -s " + e.live + "/worktrees d && git clone " + g2bRsURL + " d/x",
		"ln -s " + e.home + "/.claude/skills d && git clone " + g2bRsURL + " d/evil",
		"mv x d && git clone " + g2bRsURL + " d/x",
		"git clone " + g2bRsURL + " d && git clone " + g2bRsURL + " d/x"} {
		e.deny(cmd, e.bash(cmd, "", ""))
		e.deny("reviewer: "+cmd, e.bash(cmd, "", "reviewer"))
	}
}

func TestG2ResearcherCloneBlockedOtherRolesCannotCloneHere(t *testing.T) {
	e := g2bNewRs(t)
	for _, role := range []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"} {
		e.deny(role, e.bash("git clone "+g2bRsURL+" hello", "", role))
	}
}

func TestG2ResearcherWritesGitAndSpawnBlocked(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"git push origin HEAD", "git commit -m x", "git -C hello push", "git merge x", "gh pr merge 1",
		"gh pr create", "factory spawn coder", "factory judge T-1", "claude -p hi", "herdr pane split",
		"gh api -X POST repos/a/b", "gh api repos/a/b -f x=1", "gh pr view 1; gh pr merge 1", "gh issue create", "gh pr -R view merge 5",
		"gh repo delete a/b", "factory task update T-1 --status done", "factory pr create", "factory goal add x",
		"git add .", "git checkout -b x", "git config user.name x", "git push --force origin main",
		"git remote add x https://github.com/a/b"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherWritesSecretBash(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"cat ~/.config/factory/gh-worker-rw-token", "cat ~/.config/factory/worker-oauth-token",
		"cat ~/.ssh/id_ed25519", "cat $HOME/.claude/.credentials.json", "grep x ~/.netrc",
		"cat ~/.config/gh/hosts.yml", "cp ~/.aws/credentials .", "cat '~/.ssh'/id_rsa"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherWritesOrdinaryBashAllowed(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"ls", "echo hi > notes.md", "python3 -V", "git status", "curl -sI https://example.com",
		"factory whoami", "factory inbox", "factory msg lead:g-1 s b", "factory done ok", "factory report h b",
		"factory task show T-1", "factory task note T-1 x", "gh pr view 1",
		"gh repo view a/b", "gh search repos x", "gh api repos/a/b"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestG2ResearcherEditWriteAllowedPaths(t *testing.T) {
	e := g2bNewRs(t)
	for _, name := range []string{"Edit", "Write", "NotebookEdit", "MultiEdit"} {
		key := "file_path"
		if name == "NotebookEdit" {
			key = "notebook_path"
		}
		for _, p := range []string{filepath.Join(e.rdir, "notes.md"), "rel/notes.md", filepath.Join(e.scratch, "a.txt")} {
			e.ok(name+" "+p, e.tool(name, p, "", key))
		}
	}
}

func TestG2ResearcherEditWriteBlockedPaths(t *testing.T) {
	e := g2bNewRs(t)
	e.symlink(e.home, filepath.Join(e.rdir, "link"))
	e.symlink(e.tmp, filepath.Join(e.rdir, "up"))
	for _, name := range []string{"Edit", "Write"} {
		for _, p := range []string{e.live + "/roles/x.md", e.live + "/db/factory.db", e.home + "/.claude/settings.json",
			"~/.claude/settings.json", "~/.bashrc", e.home + "/.config/git/config",
			e.rdir + "/link/x", e.rdir + "/up/x",
			e.rdir + "/../../x", e.rdir + "/../T-1/x", // not cleaned: the guard must resolve the dot-dot itself
			filepath.Join(e.state, "factory", "research", "x.md"),
			filepath.Join(e.state, "factory", "research", "T-1", "x.md"),
			e.rdir + "/$X", filepath.Join(e.tmp, "x"), "/etc/passwd"} {
			e.deny(name+" "+p, e.tool(name, p, "", ""))
		}
	}
}

func TestG2ResearcherEditWriteBlockedWithoutPathOrOutsideCwd(t *testing.T) {
	e := g2bNewRs(t)
	e.deny("no path", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{}, "cwd": e.rdir}, "researcher"))
	e.deny("cwd tmp", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "x"}, "cwd": e.tmp}, "researcher"))
	e.deny("cwd live", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "x"}, "cwd": e.live}, "researcher"))
}

func TestG2ResearcherEditWriteGitConfigInCloneStillBlocked(t *testing.T) {
	e := g2bNewRs(t)
	e.deny(".git/config", e.tool("Write", filepath.Join(e.rdir, "hello", ".git", "config"), "", ""))
	e.deny(".git/hooks", e.tool("Write", filepath.Join(e.rdir, "hello", ".git", "hooks", "pre-commit"), "", ""))
}

func TestG2ResearcherEditWriteOtherRoleUnchanged(t *testing.T) {
	e := g2bNewRs(t)
	r := e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(e.tmp, "x")},
		"cwd": e.tmp, "session_id": "s1"}, "coder")
	e.ok("coder write", r)
}

func TestG2ResearcherSecretReadsReadGrepSecretsBlocked(t *testing.T) {
	e := g2bNewRs(t)
	for _, p := range []string{"~/.config/factory/worker-oauth-token", e.home + "/.ssh/id_ed25519", "~/.claude/.credentials.json",
		e.home + "/.config/factory/gh-worker-rw-token", e.home + "/.config/gh/hosts.yml", "~/.netrc", "~/.git-credentials", "~/.kube/config", "/proc/self/environ"} {
		e.deny("read "+p, e.tool("Read", p, "", ""))
	}
	for _, p := range []string{e.home + "/.ssh", e.home, e.home + "/.config", e.home + "/.config/factory"} {
		e.deny("grep "+p, e.tool("Grep", p, "", "path"))
	}
}

func TestG2ResearcherSecretReadsOrdinaryAllowed(t *testing.T) {
	e := g2bNewRs(t)
	e.ok("read notes", e.tool("Read", filepath.Join(e.rdir, "notes.md"), "", ""))
	e.ok("grep rdir", e.tool("Grep", e.rdir, "", "path"))
	e.ok("read settings", e.tool("Read", e.home+"/.claude/settings.json", "", ""))
}

func TestG2ResearcherNoPushRolePushPinStyleLoopholes(t *testing.T) {
	e := g2bNewRs(t)
	for _, cmd := range []string{"GIT_DIR=x git status", "export GIT_CONFIG_COUNT=1",
		"unset PATH", "git -c alias.s=push s", "git --git-dir=/x status"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

// ---- from go/internal/factorytest/guard2/retro_mirror_test.go ----

// Port of tests/test_worker_guard_retro_mirror.py (G-036 SC4): retro Write cwd/tilde, unicode-space redirect stderr,
// FuzzCheck retro parsers.

const g2bRmWhy = "the retro agent may not edit files under <live>/roles/; role prompts and hooks are changed only by the owner and the tick"
const g2bRmTail = ". Workers may not run herdr or manage factory/herdr systemd units directly; use the factory CLI (factory done, factory msg, ...). Tell your lead if this is wrong.\n"

var (
	g2bRmRedirectBlock = "worker-guard: blocked (a redirect built by a substitution into a role file: " + g2bRmWhy + ")" + g2bRmTail
	g2bRmU001cBlock    = "worker-guard: blocked (a redirect into a protected config/init file: it would persist config " +
		"into the live box (git config keys like core.fsmonitor or diff.external, and shell init files, " +
		"run later as the tick and other agents); only the owner changes ~/.gitconfig, ~/.config/git " +
		"and shell rc files)" + g2bRmTail
)

func g2bRmWriteBlock(path string) string {
	return "worker-guard: blocked (" + path + "): " + g2bRmWhy + ". Tell your lead if this is wrong.\n"
}

func g2bRmWrite(t *testing.T, filePath, payloadCwd, hookCwd string, set map[string]string) g2Res {
	t.Helper()
	p := map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filePath, "content": "x"}}
	if payloadCwd != "" {
		p["cwd"] = payloadCwd
	}
	return g2HookJSON(t, p, g2Opts{args: g2RoleArgs("retro"), cwd: hookCwd, set: set})
}

func TestG2RetroMirrorWriteUnresolvedCwd(t *testing.T) {
	tmp := g2RealTemp(t)
	// The ~+ case needs the hook cwd to be the real live factory dir (the guard resolves the live home from the
	// account, not from HOME). A subprocess cwd must exist; like the Python test, create it only when missing.
	live := g2bLiveHome(t)
	g2Mkdirs(t, live)
	var set map[string]string

	r := g2bRmWrite(t, "~nosuchuser123/factory/roles/coder.md", "/tmp", tmp, set)
	g2WantExit(t, "~user", r, 2)
	if r.stderr != g2bRmWriteBlock("'~nosuchuser123/factory/roles/coder.md'") {
		t.Errorf("~user stderr = %q", r.stderr)
	}
	r = g2bRmWrite(t, "~+/roles/coder.md", "", live, set)
	g2WantExit(t, "~+", r, 2)
	if r.stderr != g2bRmWriteBlock("'~+/roles/coder.md'") {
		t.Errorf("~+ stderr = %q", r.stderr)
	}
	for _, c := range []struct{ path, cwd string }{
		{"roles/coder.md", "/no/such/retro-cwd-unresolved"},
		{"/tmp/zz-retro-mirror-ok.txt", "/tmp"},
	} {
		r = g2bRmWrite(t, c.path, c.cwd, tmp, set)
		g2WantExit(t, c.path, r, 0)
		if r.stderr != "" {
			t.Errorf("%s: stderr %q, want empty", c.path, r.stderr)
		}
	}
}

func TestG2RetroMirrorUnicodeSpaceRedirect(t *testing.T) {
	rolesTarget := g2bLiveHome(t) + "/roles/coder.md"
	run := func(cmd string) g2Res {
		return g2HookJSON(t, g2bBashPayload(cmd), g2Opts{args: g2RoleArgs("retro")})
	}
	for _, sep := range []string{"\u001c", "\u00a0"} {
		allowed := "echo x >" + sep + ` "$(printf %s /tmp/zz-retro-note.txt)"`
		r := run(allowed)
		g2WantExit(t, "allowed sep", r, 0)
		if r.stderr != "" {
			t.Errorf("%q allowed: stderr %q", sep, r.stderr)
		}
		blocked := "echo x >" + sep + ` "$(printf %s ` + rolesTarget + `)"`
		r = run(blocked)
		g2WantExit(t, "blocked sep", r, 2)
		if r.stderr != g2bRmRedirectBlock {
			t.Errorf("%q blocked: stderr %q", sep, r.stderr)
		}
	}
	r := run("echo x >\u001c$(printf %s /tmp/zz-retro-note.txt)")
	g2WantExit(t, "unquoted U+001C", r, 2)
	if r.stderr != g2bRmU001cBlock {
		t.Errorf("unquoted: stderr %q", r.stderr)
	}
}

var g2bRmParserRE = regexp.MustCompile(`(?m)^func (retro(?:PathExpr|QuoteUnquote|SplitShellWords|PrintfSegment|ResolvePathExpr|SubstRedirectFrags))\(`)
var g2bRmFuzzRE = regexp.MustCompile(`func FuzzCheck\(f \*testing\.F\) \{`)

func g2bRmFuzzBody(src string) string {
	loc := g2bRmFuzzRE.FindStringIndex(src)
	if loc == nil {
		return ""
	}
	depth := 0
	for j := loc[1] - 1; j < len(src); j++ {
		switch src[j] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[loc[0] : j+1]
			}
		}
	}
	return src[loc[0]:]
}

func TestG2RetroMirrorFuzzcheckRoleRetro(t *testing.T) {
	mod := filepath.Join(g2RepoRoot(t), "go")
	fuzz, err := os.ReadFile(filepath.Join(mod, "internal/guard/fuzz_test.go"))
	if err != nil {
		t.Fatalf("read fuzz_test.go: %v", err)
	}
	retro, err := os.ReadFile(filepath.Join(mod, "internal/guard/retro.go"))
	if err != nil {
		t.Fatalf("read retro.go: %v", err)
	}
	body := g2bRmFuzzBody(string(fuzz))
	if !strings.Contains(body, "retroRole") {
		t.Error("FuzzCheck does not mention retroRole")
	}
	for _, m := range g2bRmParserRE.FindAllStringSubmatch(string(retro), -1) {
		if !strings.Contains(body, m[1]) {
			t.Errorf("FuzzCheck does not mention %s", m[1])
		}
	}
}

// ---- from go/internal/factorytest/guard2/retro_test.go ----

// Port of tests/test_worker_guard_retro.py (G-036 SC3): retro role guard — no push/merge, no role-file edits, no gate
// or config changes.

func g2bLiveHome(t testing.TB) string {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatalf("current user: %v", err)
	}
	return u.HomeDir + "/factory"
}

func g2bRetroRC(t *testing.T, expect int, role string, payload map[string]any) g2Res {
	t.Helper()
	r := g2HookJSON(t, payload, g2Opts{args: g2RoleArgs(role)})
	if r.code != expect {
		t.Errorf("%v: exit %d, want %d (stderr %q)", payload["tool_input"], r.code, expect, r.stderr)
	}
	return r
}

func g2bBashPayload(cmd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}}
}

func g2bExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func TestG2RetroGuardBlockedBash(t *testing.T) {
	live := g2bLiveHome(t)
	liveStem := live[:len(live)-7]
	cmds := []string{
		"git push origin HEAD",
		"gh pr merge 1",
		"factory judge T-1 --merge",
		"factory config retro_enabled 0",
		"factory project update factory --gate-cmd true",
		"factory gate run T-1",
		"cp /tmp/zz-retro-src.txt " + live + "/roles/retro.md",
		"cp /tmp/zz-retro-src.txt " + live + "/rol*/coder.md",
		"echo x > " + live + "/rol*/coder.md",
		`echo x > "$(printf %s ` + liveStem + `f; printf %s tory/roles)/coder.md"`,
		`echo x > "$(printf %s ` + live + `)/roles/coder.md"`,
		`echo x > "$(printf %s ` + live + `)/roles/retro.md"`,
		`echo x > "$(printf %s ` + live + `/rol; printf %s es)/coder.md"`,
		`echo x > "$(printf %s ` + live + `/r; printf %s oles)/coder.md"`,
		`echo x > "$(printf %s ` + live + `/; printf %s roles/coder.md)"`,
		`echo x > "$(printf '%s/%s' ` + live + ` roles/retro.md)"`,
		`echo x > "$(printf '%s/roles/coder.md' ` + live + `)"`,
		`echo x > "$(printf %s /tmp/zz-out); touch /tmp/zz-retro-hook-exec-semi"`,
		`echo x > "$(touch /tmp/zz-retro-hook-exec-block; printf '%s/%s' ` + live + ` roles/coder.md)"`,
		`echo x > "$(printf %s /tmp/zz-ok.txt)" > $(printf %s ` + live + `/roles/coder.md)`,
		`echo x > $(printf %s ` + live + `/roles/coder.md) > "$(printf %s /tmp/zz-ok.txt)"`,
		"cd " + live + ` && echo x > "$(printf %s roles/coder.md)"`,
		"cd " + live + ` && echo x > "$(printf %s ./roles/coder.md)"`,
		"ZZ=$(printf '%s/%s' " + live + ` roles/coder.md); echo x > "$ZZ"`,
		"cd $HOME/factory && echo x > roles/coder.md",
		"cd ${HOME}/factory && echo x > roles/retro.md",
		"cd $HOME/factory && cp /tmp/zz-retro-src.txt roles/coder.md",
		`cd $HOME/factory && echo x > "$(printf %s roles/coder.md)"`,
		"cd $HOME/factory/ && echo x > roles/coder.md",
		"cd ${HOME}/factory/ && echo x > roles/retro.md",
		"cd $HOME/factory/ && cp /tmp/zz-retro-src.txt roles/coder.md",
		"cd $HOME/factory/. && echo x > roles/coder.md",
		"cd $HOME//factory && echo x > roles/coder.md",
		"cd $HOME/./factory && echo x > roles/coder.md",
		`echo x > "$(printf '%s %s %s' a b c)"`,
		"X=$(printf %s hi > " + live + "/run/guard-export.json)",
		"X=$(mktemp > " + live + "/run/guard-export.json)",
		"git push origin main  # please merge",
	}
	markers := []string{"/tmp/zz-retro-hook-exec-semi", "/tmp/zz-retro-hook-exec-block"}
	for _, cmd := range cmds {
		for _, m := range markers {
			if strings.Contains(cmd, m) {
				if err := os.Remove(m); err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
			}
		}
		g2bRetroRC(t, 2, "retro", g2bBashPayload(cmd))
		for _, m := range markers {
			if strings.Contains(cmd, m) && g2bExists(m) {
				t.Errorf("%s: the guard executed the command: marker %s exists", cmd, m)
			}
		}
	}
}

func TestG2RetroGuardBlockedEditTools(t *testing.T) {
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit"} {
		g2bRetroRC(t, 2, "retro", map[string]any{"tool_name": tool,
			"tool_input": map[string]any{"file_path": g2bLiveHome(t) + "/roles/coder.md", "content": "x"}})
	}
}

func TestG2RetroGuardAllowedBash(t *testing.T) {
	const marker = "/tmp/zz-only-printf"
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	g2bRetroRC(t, 0, "retro", g2bBashPayload(`echo x > "$(printf %s /tmp/zz-only-printf)"`))
	if g2bExists(marker) {
		t.Errorf("%s was created by the guard", marker)
	}
	for _, cmd := range []string{
		`echo x > "$(printf %s /tmp/; printf %s roles/coder.md)"`,
		"factory retro packet",
		"factory whoami",
		"factory inbox",
		"factory done x",
		"git log -1",
		"git diff",
		"gh api repos/volekerb/hivemill/pulls/1/comments",
	} {
		g2bRetroRC(t, 0, "retro", g2bBashPayload(cmd))
	}
}

func TestG2RetroGuardAllowedWriteScratch(t *testing.T) {
	g2bRetroRC(t, 0, "retro", map[string]any{"tool_name": "Write",
		"tool_input": map[string]any{"file_path": "/tmp/zz-retro-note.txt", "content": "x"}})
}

func TestG2RetroGuardOtherRolesUnchanged(t *testing.T) {
	live := g2bLiveHome(t)
	g2bRetroRC(t, 0, "coder", map[string]any{"tool_name": "Write",
		"tool_input": map[string]any{"file_path": live + "/roles/coder.md", "content": "x"}})
	g2bRetroRC(t, 0, "coder", g2bBashPayload("factory config retro_enabled 0"))
	g2bRetroRC(t, 0, "coder", g2bBashPayload("git log -1"))
	g2bRetroRC(t, 2, "coder", g2bBashPayload("X=$(printf %s hi > "+live+"/run/guard-export.json)"))
	g2bRetroRC(t, 2, "reviewer", g2bBashPayload("X=$(mktemp > "+live+"/run/guard-export.json)"))
}

// ---- from go/internal/factorytest/guard2/scratch_init_test.go ----

// Port of tests/test_worker_guard_scratch_init.py (T-0331): a coder or qa may run `factory init` and
// `bash systemd/install.sh --dry-run` only when HOME and FACTORY_HOME both resolve inside the worker's scratchpad.

const (
	g2bSiH = "HOME=@S@/h FACTORY_HOME=@S@/f"
)

var (
	g2bSiAllowed = []string{"factory init", "bash systemd/install.sh --dry-run"}
	g2bSiProject = []string{"factory project add demo /tmp/x", "factory project --gh add update demo"}
)

func g2bSiBuild(t *testing.T) string {
	t.Helper()
	root := g2RealTemp(t)
	for _, d := range []string{"scratch/h", "scratch/f", "live/systemd", "wt/systemd", "wt2/systemd", "outside", "other/scratchpad/f"} {
		g2Mkdirs(t, filepath.Join(root, d))
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "scratch", "esc")); err != nil {
		t.Fatal(err)
	}
	install, err := os.ReadFile(filepath.Join(g2RepoRoot(t), "systemd", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	for _, d := range []string{"live", "wt", "wt2"} {
		g2bWrite(t, filepath.Join(root, d, "systemd", "install.sh"), string(install))
	}
	f, err := os.OpenFile(filepath.Join(root, "wt2", "systemd", "install.sh"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\necho edited\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

type g2bScratchInit struct {
	t    *testing.T
	root string
}

func g2bNewScratchInit(t *testing.T) *g2bScratchInit {
	return &g2bScratchInit{t: t, root: g2bSiBuild(t)}
}

func (s *g2bScratchInit) run(cmd, role, cwd string, extra map[string]string) g2Res {
	s.t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		s.t.Fatal(err)
	}
	cmd = strings.NewReplacer("@S@", s.root+"/scratch", "@R@", s.root, "@L@", filepath.Join(home, "factory")).Replace(cmd)
	set := map[string]string{"WORKER_GUARD_SCRATCH": s.root + "/scratch", "WORKER_GUARD_LIVE_HOME": s.root + "/live"}
	for k, v := range extra {
		set[k] = v
	}
	return g2HookJSON(s.t, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd},
		"cwd": filepath.Join(s.root, cwd)}, g2Opts{args: g2RoleArgs(role), cwd: s.root, set: set})
}

func (s *g2bScratchInit) r(cmd, role string) g2Res { return s.run(cmd, role, "wt", nil) }

func (s *g2bScratchInit) blocked(cmds []string, roles ...string) {
	s.t.Helper()
	if len(roles) == 0 {
		roles = []string{"coder", "qa"}
	}
	for _, role := range roles {
		for _, c := range cmds {
			r := s.r(c, role)
			if r.code != 2 {
				s.t.Errorf("%s: expected a block for %q, got %d (stderr %q)", role, c, r.code, r.stderr)
			}
		}
	}
}

// noPushRoles is NO_PUSH_ROLES as the Python hook defines it, so a role added there is covered by default.
func g2bNoPushRoles(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(g2RepoRoot(t), "roles", "hooks", "worker-guard.py"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	m := regexp.MustCompile(`(?m)^NO_PUSH_ROLES = \{([^}]*)\}`).FindSubmatch(b)
	if m == nil {
		t.Fatal("NO_PUSH_ROLES not found in roles/hooks/worker-guard.py")
	}
	var roles []string
	for _, r := range regexp.MustCompile(`"([a-z-]+)"`).FindAllSubmatch(m[1], -1) {
		roles = append(roles, string(r[1]))
	}
	sort.Strings(roles)
	return roles
}

func TestG2ScratchInitAllowedTwoCommandsInScratchForCoderAndQA(t *testing.T) {
	s := g2bNewScratchInit(t)
	for _, role := range []string{"coder", "qa"} {
		for _, c := range g2bSiAllowed {
			r := s.r(g2bSiH+" "+c, role)
			g2WantExit(t, role+": "+c, r, 0)
		}
	}
}

func TestG2ScratchInitAllowedExportedHomesForCoder(t *testing.T) {
	s := g2bNewScratchInit(t)
	g2WantExit(t, "export+init", s.r("export HOME=@S@/h FACTORY_HOME=@S@/f; factory init", "coder"), 0)
	g2WantExit(t, "export+install", s.r("export HOME=@S@/h; export FACTORY_HOME=@S@/f; bash systemd/install.sh --dry-run", "coder"), 0)
}

func TestG2ScratchInitAllowedOtherCommandsUnchanged(t *testing.T) {
	s := g2bNewScratchInit(t)
	for _, c := range []string{"factory inbox", "factory whoami", "factory task show T-0331", "ls -l"} {
		g2WantExit(t, c, s.r(c, "qa"), 0)
	}
}

func TestG2ScratchInitBlockedHomesNotBothInScratch(t *testing.T) {
	s := g2bNewScratchInit(t)
	c := "factory init"
	s.blocked([]string{
		c,
		"HOME=@S@/h " + c,
		"FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=@L@ " + c,
		"HOME=@L@ FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=f " + c,
		"HOME=@S@/h FACTORY_HOME=./f " + c,
		"HOME=@S@/h FACTORY_HOME=@S@/../outside " + c,
		"HOME=@S@/h FACTORY_HOME=@S@/esc/f " + c,
		"HOME=@S@/esc FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=@R@/other/scratchpad/f " + c,
		"HOME=@S@/h FACTORY_HOME=$X " + c,
		"HOME=@S@/h FACTORY_HOME=~/f " + c,
	})
}

func TestG2ScratchInitBlockedInitLiveHomeAndUnlistedCommands(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{g2bSiH + " factory init --live-home @S@/x", g2bSiH + " factory init --live",
		"HOME=@S@/h factory up", "HOME=@S@/h factory spawn pm", "factory tick"})
}

func TestG2ScratchInitBlockedUnsetEnvUAndSubshellForms(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{"export HOME=@S@/h FACTORY_HOME=@S@/f; unset FACTORY_HOME; factory init",
		g2bSiH + " env -u FACTORY_HOME factory init",
		g2bSiH + " env -i factory init",
		"(export HOME=@S@/h FACTORY_HOME=@S@/f); factory init",
		"export HOME=@S@/h FACTORY_HOME=@S@/f; (unset FACTORY_HOME; factory init)",
		"export HOME=@S@/h FACTORY_HOME=@S@/f; unset FACTORY_HOME; bash systemd/install.sh --dry-run"})
}

func TestG2ScratchInitBlockedOnlyTheRealFactoryCLI(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{g2bSiH + " @S@/factory init", g2bSiH + " ./factory init", g2bSiH + " ../factory init", g2bSiH + " /usr/bin/factory init",
		g2bSiH + " python3 factory init", g2bSiH + " env factory init", g2bSiH + " command factory init"})
}

func TestG2ScratchInitBlockedInitMatchIsStrict(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{g2bSiH + " factory --x init", g2bSiH + " factory -C @S@ init"}, "qa")
}

func TestG2ScratchInitBlockedInstallShBashOnly(t *testing.T) {
	s := g2bNewScratchInit(t)
	var cmds []string
	for _, sh := range []string{"zsh", "sh", "dash", "ksh", "/bin/bash", "./bash"} {
		cmds = append(cmds, g2bSiH+" "+sh+" systemd/install.sh --dry-run")
	}
	s.blocked(cmds)
}

func TestG2ScratchInitBlockedOnlyHomeAndFactoryHomeAssignments(t *testing.T) {
	s := g2bNewScratchInit(t)
	for _, c := range []string{"factory init", "bash systemd/install.sh --dry-run"} {
		s.blocked([]string{g2bSiH + " PATH=@S@/bin " + c, "PATH=@S@/bin " + g2bSiH + " " + c, g2bSiH + " BASH_ENV=@S@/e " + c,
			g2bSiH + " ENV=@S@/e " + c, g2bSiH + " GIT_CONFIG_GLOBAL=@S@/g " + c, "export PATH=@S@/bin; " + g2bSiH + " " + c,
			"export BASH_ENV=@S@/e HOME=@S@/h FACTORY_HOME=@S@/f; " + c, "FOO=1; " + g2bSiH + " " + c})
	}
}

func TestG2ScratchInitBlockedNonASCIITextIsRefused(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{"export\u00a0HOME=@S@/h FACTORY_HOME=@S@/f; factory init",
		"export HOME=@S@/h FACTORY_HOME=@S@/f\u00a0; factory init", g2bSiH + " factory init \u00e9"})
}

func TestG2ScratchInitBlockedStatementFormStaysAllowed(t *testing.T) {
	s := g2bNewScratchInit(t)
	for _, c := range []string{"export HOME=@S@/h; export FACTORY_HOME=@S@/f; factory init", "HOME=@S@/h; FACTORY_HOME=@S@/f; factory init"} {
		g2WantExit(t, c, s.r(c, "coder"), 0)
	}
}

func TestG2ScratchInitBlockedInstallShForms(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{g2bSiH + " bash systemd/install.sh", g2bSiH + " bash systemd/install.sh --dry-run --with-serve",
		"HOME=@S@/h bash systemd/install.sh --dry-run",
		"HOME=@S@/h FACTORY_HOME=@L@ bash systemd/install.sh --dry-run",
		"HOME=@S@/h FACTORY_HOME=@S@/esc bash systemd/install.sh --dry-run"})
}

func TestG2ScratchInitBlockedInstallShBypasses(t *testing.T) {
	s := g2bNewScratchInit(t)
	s.blocked([]string{"echo x >> systemd/install.sh; " + g2bSiH + " bash systemd/install.sh --dry-run",
		"printf x | tee -a systemd/install.sh; " + g2bSiH + " bash systemd/install.sh --dry-run",
		"true && " + g2bSiH + " bash systemd/install.sh --dry-run",
		"echo evil | " + g2bSiH + " bash -s systemd/install.sh --dry-run",
		g2bSiH + " bash -x systemd/install.sh --dry-run"})
}

func TestG2ScratchInitBlockedEditedInstallShIsStillInspected(t *testing.T) {
	s := g2bNewScratchInit(t)
	r := s.run(g2bSiH+" bash systemd/install.sh --dry-run", "coder", "wt2", nil)
	g2WantExit(t, "edited install.sh", r, 2)
}

func TestG2ScratchInitBlockedInstallShWithoutALiveCopyIsInspected(t *testing.T) {
	s := g2bNewScratchInit(t)
	r := s.run(g2bSiH+" bash systemd/install.sh --dry-run", "coder", "wt", map[string]string{"WORKER_GUARD_LIVE_HOME": s.root + "/outside"})
	g2WantExit(t, "no live copy", r, 2)
}

func TestG2ScratchInitBlockedEvaluatorAndOtherRolesStayBlocked(t *testing.T) {
	s := g2bNewScratchInit(t)
	var roles []string
	for _, r := range g2bNoPushRoles(t) {
		if r != "qa" {
			roles = append(roles, r)
		}
	}
	found := false
	for _, r := range roles {
		found = found || r == "evaluator"
	}
	if !found {
		t.Fatalf("evaluator missing from %v", roles)
	}
	for _, role := range roles {
		for _, c := range g2bSiAllowed {
			g2WantExit(t, role+": "+c, s.r(g2bSiH+" "+c, role), 2)
		}
	}
	g2WantExit(t, "qa export HOME", s.r("export HOME=@S@/h; factory init", "qa"), 2)
}

func TestG2ScratchInitBlockedFactoryProjectIsNotExempt(t *testing.T) {
	s := g2bNewScratchInit(t)
	for _, c := range g2bSiProject {
		for _, pre := range []string{g2bSiH + " ", "HOME=@S@/h "} {
			g2WantExit(t, pre+c, s.r(pre+c, "qa"), 2)
		}
		g2WantExit(t, c, s.r(g2bSiH+" "+c, "evaluator"), 2)
	}
}

func g2bListing(t testing.TB, roots ...string) []string {
	t.Helper()
	var out []string
	for _, r := range roots {
		err := filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != r {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}
	sort.Strings(out)
	return out
}

func TestG2ScratchInitInstallDryRunWritesNothingListingUnchanged(t *testing.T) {
	s := g2bNewScratchInit(t)
	home, fh := filepath.Join(s.root, "scratch", "h"), filepath.Join(s.root, "scratch", "f")
	before := g2bListing(t, home, fh)
	install := filepath.Join(g2RepoRoot(t), "systemd", "install.sh")
	t.Chdir(s.root) // runCmd (adapter_cursor_test.go) runs in the test's cwd
	r := runCmd(t, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FACTORY_HOME=" + fh, "USER=tester"}, "",
		"bash", install, "--dry-run")
	if r.code != 0 {
		t.Fatalf("install.sh --dry-run: exit %d: %s", r.code, r.stderr)
	}
	out := r.stdout
	after := g2bListing(t, home, fh)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("listing changed: before %v after %v", before, after)
	}
	for _, want := range []string{"[dry-run] mkdir -p " + filepath.Join(home, ".config", "systemd", "user"),
		"[dry-run] systemctl --user enable hivemill.service"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Errorf("%s exists after a dry run (err %v)", filepath.Join(home, ".config"), err)
	}
}

func TestG2ScratchInitRealFactoryInitCreatesTheDBUnderTheTempFactoryHome(t *testing.T) {
	if !rpPythonOnPath() {
		t.Skip("python3 is not on PATH: this test drives a command the Go CLI still hands to Python; it runs where python3 exists")
	}
	s := g2bNewScratchInit(t)
	home, fh := filepath.Join(s.root, "scratch", "h"), filepath.Join(s.root, "scratch", "f")
	factory := filepath.Join(g2RepoRoot(t), "bin", "factory")
	t.Chdir(s.root) // runCmd (adapter_cursor_test.go) runs in the test's cwd
	r := runCmd(t, []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FACTORY_HOME=" + fh, "USER=tester"}, "",
		"python3", factory, "init")
	if r.code != 0 {
		t.Fatalf("factory init: exit %d\n%s%s", r.code, r.stdout, r.stderr)
	}
	if _, err := os.Stat(filepath.Join(fh, "db", "factory.db")); err != nil {
		t.Errorf("db not created: %v (%v)", err, g2bListing(t, fh))
	}
}

// ---- from go/internal/factorytest/guard2/uv_wrapper_test.go ----

// Port of tests/test_worker_guard_uv_wrapper.py (T-0880): `uv run [opts] [--] CMD` and `uvx [opts] CMD` are checked as
// if CMD ran directly.

const g2bUvTask = "t-0880"
const g2bPyPush = `import subprocess; subprocess.run(["git","push"])`

var g2bUvNoPush = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher"}
var g2bUvHQ = []string{"boss", "director"}

var g2bUvDenied = []string{
	"uv run git push origin main",
	"uv run -- git push",
	"uv run --with requests python3 -c '" + g2bPyPush + "'",
	"uvx --from gh gh pr merge 1",
	"uv run --python 3.12 --directory /x git push",
	"uvx git push",
	"uv tool run gh pr create --fill",
	"uv --directory /x run git push",
	"uv run --env-file .env -p 3.12 gh pr merge 1",
	"uv run --with=x git push",
	"uv run --index-url http://x git push",
	"uv run --color never git push",
	"uv --color never run git push",
	"uvx --index-url http://x gh pr merge 1",
	"uv run --python-preference only-system git push",
	"uv run --index-url http://x python3 -c '" + g2bPyPush + "'",
	"uv run --foo bar git push",
	"uv run --foo bar /usr/bin/git push",
	"uv run --foo bar git -C /tmp push",
	"uv run --foo bar gh pr merge 1",
	"cd /x && uv run git push origin main",
	"cd /x && uvx --from gh gh pr merge 1",
	"bash -c 'uv run git push origin main'",
	"bash -c 'cd /x && uvx --from gh gh pr merge 1'",
	"bash -c \"uv run --with requests python3 -c '" + g2bPyPush + "'\"",
}

var g2bUvAllowed = []string{
	"uv run pytest tests/test_harness.py -q",
	"uv run python3 -m unittest x",
	"uvx ruff check .",
	"uv pip list",
	"uv run --with requests python3 script.py",
	"uv run git status",
	"uv run gh pr view 1",
	"uv sync",
	"cd /x && uv run python3 -c 'import json'",
	"uv run python3 ci/affected.py --base origin/main --quick --run",
	"uv run pytest tests/test_worker_guard_uv_wrapper.py -q",
	"uv run python3 -m unittest tests/test_worker_guard_uv_wrapper.py",
	"uvx ruff check roles/hooks/worker-guard.py",
	"uv run mypy bin/factory",
	"uv run --with requests pytest tests/test_worker_guard_uv_wrapper.py -q",
	"uv run pytest -k git -q",
	"uv run pytest -k git tests/test_harness.py",
	"uv run --with git pytest tests/test_harness.py -q",
	"uv run pytest tests/git tests/test_harness.py",
	"uv run --color never git commit -m x",
	"python3 -m uv run git push",
}

var (
	g2bUvFirstWordDenied = []string{"uv run git config user.name x", "uv run herdr list", "uv run kill 1", "uv run su" + "do ls"}
	g2bUvPinnedDenied    = []string{"uv run python3 x.py git push", "uv run pytest -k git push"}
	g2bUvBothDenied      = []string{"uv run --foo bar git push", "uv run --foo bar /usr/bin/git push", "uv run --foo bar git -C /tmp push",
		"uv run --index-url http://x git push", "uv run --color never git push", "uv --color never run git push"}
	g2bUvDataWords = []string{"uv run python3 x.py herdr", "uv run python3 script.py kill 1", "uvx --from foo python3 x.py herdr"}
)

func g2bUvBare(cmd string) string {
	return strings.ReplaceAll(strings.ReplaceAll(cmd, "uvx --from foo ", ""), "uv run ", "")
}

func g2bUvRun(t *testing.T, cmd, role string) g2Res {
	t.Helper()
	return g2Bash(t, cmd, g2Opts{args: g2RoleArgs(role), cwd: g2RepoRoot(t),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"}})
}

func TestG2UvWrapperDeniedForEveryNoPushRole(t *testing.T) {
	for _, role := range g2bUvNoPush {
		for _, cmd := range g2bUvDenied {
			r := g2bUvRun(t, cmd, role)
			g2WantExit(t, role+": "+cmd, r, 2)
		}
	}
}

func TestG2UvWrapperFirstWordRulesAndPinned(t *testing.T) {
	cmds := append(append(append([]string{}, g2bUvFirstWordDenied...), "uv run git commit -m x"), g2bUvPinnedDenied...)
	for _, role := range []string{"qa", "reviewer"} {
		for _, cmd := range cmds {
			g2WantExit(t, role+": "+cmd, g2bUvRun(t, cmd, role), 2)
		}
	}
}

func TestG2UvWrapperDeniedReasonNamesTheRule(t *testing.T) {
	for _, c := range []struct{ cmd, role, word string }{
		{"uv run git push origin main", "qa", "push"},
		{"uv run -- git push", "reviewer", "push"},
		{"uv run --with requests python3 -c '" + `import os; os.system("gh pr merge 1")` + "'", "qa", "gh write"},
	} {
		r := g2bUvRun(t, c.cmd, c.role)
		if !strings.Contains(r.stderr, c.word) {
			t.Errorf("%s %q: stderr %q lacks %q", c.role, c.cmd, r.stderr, c.word)
		}
	}
}

func TestG2UvWrapperAllowedForNoPushRoles(t *testing.T) {
	for _, role := range g2bUvNoPush {
		for _, cmd := range g2bUvAllowed {
			r := g2bUvRun(t, cmd, role)
			g2WantExit(t, role+": "+cmd, r, 0)
		}
	}
}

func TestG2UvWrapperRealFilesExist(t *testing.T) {
	root := g2RepoRoot(t)
	for _, rel := range []string{"ci/affected.py", "tests/test_worker_guard_uv_wrapper.py", "roles/hooks/worker-guard.py", "bin/factory"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestG2UvWrapperDataWordsMatchBareCommand(t *testing.T) {
	for _, role := range g2bUvNoPush {
		for _, cmd := range g2bUvDataWords {
			a, b := g2bUvRun(t, cmd, role), g2bUvRun(t, g2bUvBare(cmd), role)
			if a.code != b.code {
				t.Errorf("%s: %q exit %d, bare %q exit %d", role, cmd, a.code, g2bUvBare(cmd), b.code)
			}
		}
	}
}

func TestG2UvWrapperCleanerJudgeUnchanged(t *testing.T) {
	base := []string{"uv run pytest -q", "uvx ruff check .", "uv pip list", "uv run git status"}
	for _, role := range []string{"cleaner", "judge"} {
		for _, cmd := range append(append([]string{}, base...), g2bUvDataWords...) {
			g2WantExit(t, role+": "+cmd, g2bUvRun(t, cmd, role), 0)
		}
	}
	g2WantExit(t, "cleaner push", g2bUvRun(t, "uv run git push origin main", "cleaner"), 0)
}

func TestG2UvWrapperHQUnchanged(t *testing.T) {
	base := []string{"uv run git push origin main", "uvx --from gh gh pr merge 1", "uv run -- git push",
		"uv run pytest -q", "uv pip list", "uv run git push origin --delete factory/t-1"}
	for _, role := range g2bUvHQ {
		for _, cmd := range append(append([]string{}, base...), g2bUvDataWords...) {
			g2WantExit(t, role+": "+cmd, g2bUvRun(t, cmd, role), 0)
		}
	}
}

// uvCoder is the CoderUvPushTests fixture: a factory home with a DB row and a worktree with a local bare origin.
type g2bUvCoder struct {
	t    *testing.T
	home string
	wt   string
}

func g2bNewUvCoder(t *testing.T) *g2bUvCoder {
	t.Helper()
	tmp := g2RealTemp(t)
	home := filepath.Join(tmp, "factory")
	wt := filepath.Join(home, "worktrees", "G-010-T-0880")
	g2Mkdirs(t, filepath.Join(home, ".git"), filepath.Join(home, "roles"))
	g2GuardDB(t, home, map[string]string{strings.ToUpper(g2bUvTask): wt})
	origin := filepath.Join(tmp, "origin.git")
	g2Git(t, tmp, "init", "-q", "--bare", origin)
	g2Git(t, tmp, "init", "-q", "-b", "main", wt)
	g2Git(t, wt, "commit", "-q", "--allow-empty", "-m", "i")
	g2Git(t, wt, "remote", "add", "origin", origin)
	return &g2bUvCoder{t: t, home: home, wt: wt}
}

func (c *g2bUvCoder) run(cmd string) g2Res {
	c.t.Helper()
	return g2Bash(c.t, cmd, g2Opts{args: g2RoleArgs("coder"), cwd: c.wt,
		drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT"},
		set: map[string]string{"HOME": filepath.Dir(c.home), "WORKER_GUARD_LIVE_HOME": c.home,
			"FACTORY_HOME": c.home, "FACTORY_AGENT": "coder-" + g2bUvTask}})
}

func TestG2UvWrapperCoderDenied(t *testing.T) {
	c := g2bNewUvCoder(t)
	for _, cmd := range []string{"uv run git push origin main", "uv run -- git push", "uv run git push origin HEAD:factory/t-0001",
		"uvx git push origin main", "uv run git push --force origin HEAD:factory/" + g2bUvTask} {
		g2WantExit(t, cmd, c.run(cmd), 2)
	}
}

func TestG2UvWrapperCoderItem11Forms(t *testing.T) {
	c := g2bNewUvCoder(t)
	denied := append(append(append([]string{}, g2bUvBothDenied...), g2bUvFirstWordDenied...), g2bUvPinnedDenied...)
	for _, cmd := range denied {
		g2WantExit(t, cmd, c.run(cmd), 2)
	}
	for _, cmd := range []string{"uv run pytest -k git -q", "uv run pytest -k git tests/test_harness.py",
		"uv run --with git pytest tests/test_harness.py -q", "uv run pytest tests/git tests/test_harness.py",
		"uv run --color never git commit -m x", "python3 -m uv run git push"} {
		g2WantExit(t, cmd, c.run(cmd), 0)
	}
}

func TestG2UvWrapperCoderOwnPushAllowed(t *testing.T) {
	c := g2bNewUvCoder(t)
	g2WantExit(t, "own push", c.run("uv run git push origin HEAD:factory/"+g2bUvTask), 0)
}

func TestG2UvWrapperCoderOwnPushAllowedWithSeparator(t *testing.T) {
	c := g2bNewUvCoder(t)
	g2WantExit(t, "own push --", c.run("uv run -- git push origin HEAD:factory/"+g2bUvTask), 0)
}

func TestG2UvWrapperCoderDataWordsMatchBareCommand(t *testing.T) {
	c := g2bNewUvCoder(t)
	for _, cmd := range g2bUvDataWords {
		a, b := c.run(cmd), c.run(g2bUvBare(cmd))
		if a.code != b.code {
			t.Errorf("%q exit %d, bare %q exit %d", cmd, a.code, g2bUvBare(cmd), b.code)
		}
	}
}

func TestG2UvWrapperCoderRealFileArgumentsAllowed(t *testing.T) {
	c := g2bNewUvCoder(t)
	root := g2RepoRoot(t)
	for _, cmd := range []string{"uv run python3 " + root + "/ci/affected.py --help",
		"uv run pytest " + root + "/tests/test_worker_guard_uv_wrapper.py -q",
		"uvx ruff check " + root + "/roles/hooks/worker-guard.py", "uv run mypy " + root + "/bin/factory"} {
		g2WantExit(t, cmd, c.run(cmd), 0)
	}
}

func TestG2UvWrapperCoderBareUvWrappedPytestRefused(t *testing.T) {
	c := g2bNewUvCoder(t)
	for _, cmd := range []string{"uv run pytest -q", "uv run --with git pytest -q", "uvx pytest", "uv tool run pytest"} {
		g2WantExit(t, cmd, c.run(cmd), 2)
	}
}

func TestG2UvWrapperCoderOtherUvCommandsAllowed(t *testing.T) {
	c := g2bNewUvCoder(t)
	for _, cmd := range []string{"uv run pytest tests/test_harness.py -q", "uvx ruff check .", "uv pip list", "uv run python3 -m unittest x"} {
		g2WantExit(t, cmd, c.run(cmd), 0)
	}
}
