package guard2

import (
	"os"
	"os/user"
	"path/filepath"
	"testing"
)

// Port of tests/test_worker_guard_researcher.py (G-029 T-0846): the researcher role through the real worker-guard hook.
// A researcher is a no-push role that may clone a public https repo into its research dir
// (<state>/factory/research/<name>) and write only there or in its scratchpad.

const rsURL = "https://github.com/octocat/Hello-World.git"
const rsAt = "@"

type rsEnv struct {
	t       *testing.T
	tmp     string
	state   string
	rdir    string
	scratch string
	home    string
	live    string
}

func newRs(t *testing.T) *rsEnv {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	e := &rsEnv{t: t, tmp: realTemp(t), home: u.HomeDir}
	e.live = e.home + "/factory"
	e.state = filepath.Join(e.tmp, "state")
	e.rdir = filepath.Join(e.state, "factory", "research", "T-9999")
	e.scratch = filepath.Join(e.tmp, "scratch")
	mkdirs(t, e.rdir, e.scratch)
	return e
}

func (e *rsEnv) hook(payload map[string]any, role string) res {
	e.t.Helper()
	return hookJSON(e.t, payload, hookOpts{args: roleArgs(role),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_HOME"},
		set:  map[string]string{"WORKER_GUARD_STATE": e.state, "WORKER_GUARD_SCRATCH": e.scratch}})
}

func (e *rsEnv) bash(cmd, cwd, role string) res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.rdir
	}
	if role == "" {
		role = "researcher"
	}
	return e.hook(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}, "cwd": cwd, "session_id": "s1"}, role)
}

func (e *rsEnv) tool(name, path, cwd, key string) res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.rdir
	}
	if key == "" {
		key = "file_path"
	}
	return e.hook(map[string]any{"tool_name": name, "tool_input": map[string]any{key: path}, "cwd": cwd, "session_id": "s1"}, "researcher")
}

func (e *rsEnv) ok(what string, r res) {
	e.t.Helper()
	if r.code != 0 {
		e.t.Errorf("%s: exit %d, want 0: %s", what, r.code, r.stderr)
	}
}

func (e *rsEnv) deny(what string, r res) {
	e.t.Helper()
	if r.code != 2 {
		e.t.Errorf("should block %s: exit %d (stderr %q)", what, r.code, r.stderr)
	}
}

func (e *rsEnv) symlink(target, link string) {
	e.t.Helper()
	if err := os.Symlink(target, link); err != nil {
		e.t.Fatal(err)
	}
}

func TestResearcherGhApiNonGetDenied(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"gh api -XPOST repos/a/b/issues", "gh api --method=POST x", "gh api -X=post x", "gh api x -f title=t",
		"gh api x -F a=1", "gh api x --input f.json", "gh api -X PATCH x", "gh api -X DELETE x",
		"gh api x --field a=1", "gh api x --raw-field a=b", "gh api -iXPOST x", "gh api -if a=1 x",
		"gh api --method=get x -f a=1", "gh api -X GET -X POST x", "gh api --method put x",
		"gh -X GET api x", "gh --method GET api repos/a/b", "gh -H 'Accept: x' api repos/a/b"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherGhApiGetAllowed(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"gh api repos/a/b", "gh api -X GET repos/a/b", "gh api --method=GET repos/a/b/pulls?state=open",
		"gh api -XGET x", "gh api -X=get x", "gh api --method get x", "gh api x --jq .name"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherCloneAllowedPublicClone(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"git clone --depth 1 " + rsURL + " hello",
		"git clone --depth=1 --quiet " + rsURL + " hello",
		"git clone --branch v1 --single-branch --filter=blob:none --no-tags " + rsURL + " hello",
		"git clone -q -b main -- " + rsURL + " sub/hello",
		"git clone " + rsURL + " " + e.rdir + "/abs",
		"cd sub && git clone " + rsURL + " hello"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherCloneAllowedIntoScratchpad(t *testing.T) {
	e := newRs(t)
	c1 := "git clone " + rsURL + " here"
	e.ok(c1, e.bash(c1, e.scratch, ""))
	c2 := "git clone " + rsURL + " " + e.scratch + "/x"
	e.ok(c2, e.bash(c2, "", ""))
}

func TestResearcherCloneAllowedReadOnlyGitInClone(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"git -C hello log -1", "git -C hello status", "git log -1", "git fetch origin"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherCloneBlockedAbuse(t *testing.T) {
	e := newRs(t)
	const g = "git clone https://github.com/a/b "
	for _, cmd := range []string{"git clone ext::sh -c x d", "git clone file:///home x", "git clone /srv/other/repo x",
		"git clone ssh://github.com/a/b x", "git clone git" + rsAt + "github.com:a/b x",
		"git clone https://u:p" + rsAt + "github.com/a/b x",
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

func TestResearcherCloneBlockedWrappersAndVariants(t *testing.T) {
	e := newRs(t)
	c := "git clone -c core.hooksPath=x " + rsURL + " d"
	for _, cmd := range []string{c, "true; " + c, "true && " + c, "echo $(" + c + ")", "env " + c, "nohup " + c, "timeout 5 " + c,
		"/usr/bin/" + c, "./" + c, "bash -c '" + c + "'", `zsh -c "` + c + `"`, "'gi" + "t' clone ext::sh d",
		"git clone " + rsURL + " ../x", "nohup git clone " + rsURL + " /tmp/x", "env git clone ext::sh d"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherCloneBlockedOutsideResearchCwd(t *testing.T) {
	e := newRs(t)
	for _, cwd := range []string{e.tmp, e.live, e.home, filepath.Join(e.state, "factory", "research"), "/tmp"} {
		e.deny(cwd, e.bash("git clone "+rsURL+" hello", cwd, ""))
	}
}

func TestResearcherCloneBlockedSymlinkEscape(t *testing.T) {
	e := newRs(t)
	e.symlink(e.home, filepath.Join(e.rdir, "link"))
	e.deny("link/x", e.bash("git clone "+rsURL+" link/x", "", ""))
}

func TestResearcherCloneBlockedAfterFsChangeOnSameLine(t *testing.T) {
	e := newRs(t)
	// B1: a symlink/move/clone planted earlier on the line relocates the clone outside the research dir
	for _, cmd := range []string{"ln -s " + e.live + "/worktrees d && git clone " + rsURL + " d/x",
		"ln -s " + e.home + "/.claude/skills d && git clone " + rsURL + " d/evil",
		"mv x d && git clone " + rsURL + " d/x",
		"git clone " + rsURL + " d && git clone " + rsURL + " d/x"} {
		e.deny(cmd, e.bash(cmd, "", ""))
		e.deny("reviewer: "+cmd, e.bash(cmd, "", "reviewer"))
	}
}

func TestResearcherCloneBlockedOtherRolesCannotCloneHere(t *testing.T) {
	e := newRs(t)
	for _, role := range []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"} {
		e.deny(role, e.bash("git clone "+rsURL+" hello", "", role))
	}
}

func TestResearcherWritesGitAndSpawnBlocked(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"git push origin HEAD", "git commit -m x", "git -C hello push", "git merge x", "gh pr merge 1",
		"gh pr create", "factory spawn coder", "factory judge T-1", "claude -p hi", "herdr pane split",
		"gh api -X POST repos/a/b", "gh api repos/a/b -f x=1", "gh pr view 1; gh pr merge 1", "gh issue create", "gh pr -R view merge 5",
		"gh repo delete a/b", "factory task update T-1 --status done", "factory pr create", "factory goal add x",
		"git add .", "git checkout -b x", "git config user.name x", "git push --force origin main",
		"git remote add x https://github.com/a/b"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherWritesSecretBash(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"cat ~/.config/factory/gh-worker-rw-token", "cat ~/.config/factory/worker-oauth-token",
		"cat ~/.ssh/id_ed25519", "cat $HOME/.claude/.credentials.json", "grep x ~/.netrc",
		"cat ~/.config/gh/hosts.yml", "cp ~/.aws/credentials .", "cat '~/.ssh'/id_rsa"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherWritesOrdinaryBashAllowed(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"ls", "echo hi > notes.md", "python3 -V", "git status", "curl -sI https://example.com",
		"factory whoami", "factory inbox", "factory msg lead:g-1 s b", "factory done ok", "factory report h b",
		"factory task show T-1", "factory task note T-1 x", "gh pr view 1",
		"gh repo view a/b", "gh search repos x", "gh api repos/a/b"} {
		e.ok(cmd, e.bash(cmd, "", ""))
	}
}

func TestResearcherEditWriteAllowedPaths(t *testing.T) {
	e := newRs(t)
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

func TestResearcherEditWriteBlockedPaths(t *testing.T) {
	e := newRs(t)
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

func TestResearcherEditWriteBlockedWithoutPathOrOutsideCwd(t *testing.T) {
	e := newRs(t)
	e.deny("no path", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{}, "cwd": e.rdir}, "researcher"))
	e.deny("cwd tmp", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "x"}, "cwd": e.tmp}, "researcher"))
	e.deny("cwd live", e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": "x"}, "cwd": e.live}, "researcher"))
}

func TestResearcherEditWriteGitConfigInCloneStillBlocked(t *testing.T) {
	e := newRs(t)
	e.deny(".git/config", e.tool("Write", filepath.Join(e.rdir, "hello", ".git", "config"), "", ""))
	e.deny(".git/hooks", e.tool("Write", filepath.Join(e.rdir, "hello", ".git", "hooks", "pre-commit"), "", ""))
}

func TestResearcherEditWriteOtherRoleUnchanged(t *testing.T) {
	e := newRs(t)
	r := e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": filepath.Join(e.tmp, "x")},
		"cwd": e.tmp, "session_id": "s1"}, "coder")
	e.ok("coder write", r)
}

func TestResearcherSecretReadsReadGrepSecretsBlocked(t *testing.T) {
	e := newRs(t)
	for _, p := range []string{"~/.config/factory/worker-oauth-token", e.home + "/.ssh/id_ed25519", "~/.claude/.credentials.json",
		e.home + "/.config/factory/gh-worker-rw-token", e.home + "/.config/gh/hosts.yml", "~/.netrc", "~/.git-credentials", "~/.kube/config", "/proc/self/environ"} {
		e.deny("read "+p, e.tool("Read", p, "", ""))
	}
	for _, p := range []string{e.home + "/.ssh", e.home, e.home + "/.config", e.home + "/.config/factory"} {
		e.deny("grep "+p, e.tool("Grep", p, "", "path"))
	}
}

func TestResearcherSecretReadsOrdinaryAllowed(t *testing.T) {
	e := newRs(t)
	e.ok("read notes", e.tool("Read", filepath.Join(e.rdir, "notes.md"), "", ""))
	e.ok("grep rdir", e.tool("Grep", e.rdir, "", "path"))
	e.ok("read settings", e.tool("Read", e.home+"/.claude/settings.json", "", ""))
}

func TestResearcherNoPushRolePushPinStyleLoopholes(t *testing.T) {
	e := newRs(t)
	for _, cmd := range []string{"GIT_DIR=x git status", "export GIT_CONFIG_COUNT=1",
		"unset PATH", "git -c alias.s=push s", "git --git-dir=/x status"} {
		e.deny(cmd, e.bash(cmd, "", ""))
	}
}
