package guard2

import (
	"path/filepath"
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_repocfg.py (T-0226): workers cannot write repo git config (every worktree shares the
// live checkout's .git/config). Accident prevention, not a security boundary.

var rcRoles = []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator"}
var rcExecKeys = []string{"filter.x.smudge cmd", "filter.x.clean cmd", "diff.x.textconv cmd", "core.hooksPath x",
	"core.fsmonitor cmd", "core.sshCommand cmd", "remote.origin.uploadpack cmd"}

type rcEnv struct {
	t                             *testing.T
	tmp, home, wt, scratch, clone string
}

func newRc(t *testing.T) *rcEnv {
	t.Helper()
	tmp := realTemp(t)
	e := &rcEnv{t: t, tmp: tmp, home: filepath.Join(tmp, "live"), scratch: filepath.Join(tmp, "scratch")}
	e.wt = filepath.Join(e.home, "worktrees", "T-1")
	e.clone = filepath.Join(e.scratch, "clone")
	mkdirs(t, e.scratch)
	git(t, tmp, "init", "-q", "-b", "main", e.home)
	git(t, e.home, "commit", "-q", "--allow-empty", "-m", "i")
	git(t, e.home, "worktree", "add", "-q", "-b", "factory/t-1", e.wt)
	git(t, tmp, "init", "-q", "-b", "main", e.clone)
	git(t, tmp, "init", "-q", "--bare", filepath.Join(e.scratch, "bare.git"))
	return e
}

func (e *rcEnv) hook(payload map[string]any, role string) res {
	e.t.Helper()
	return hookJSON(e.t, payload, hookOpts{args: roleArgs(role),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"},
		set:  map[string]string{"WORKER_GUARD_LIVE_HOME": e.home, "WORKER_GUARD_SCRATCH": e.scratch}})
}

func (e *rcEnv) bash(cmd, role, cwd string) res {
	e.t.Helper()
	if cwd == "" {
		cwd = e.wt
	}
	return e.hook(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}, "cwd": cwd}, role)
}

func (e *rcEnv) blocked(cmd, role, cwd string) res {
	e.t.Helper()
	r := e.bash(cmd, role, cwd)
	if r.code != 2 {
		e.t.Errorf("should block (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
	return r
}

func (e *rcEnv) allowed(cmd, role, cwd string) {
	e.t.Helper()
	if r := e.bash(cmd, role, cwd); r.code != 0 {
		e.t.Errorf("should allow (%s): %q: exit %d\n%s", role, cmd, r.code, r.stderr)
	}
}

func TestRepoCfgExecCapableKeysBlockedFromAWorktreeForEveryRole(t *testing.T) {
	e := newRc(t)
	for _, role := range rcRoles {
		for _, kv := range rcExecKeys {
			e.blocked("git config "+kv, role, "")
		}
	}
}

func TestRepoCfgEveryWriteFormBlocked(t *testing.T) {
	e := newRc(t)
	for _, cmd := range []string{"git config --add a.b c", "git config --unset a.b", "git config --unset-all a.b",
		"git config --replace-all a.b c", "git config --rename-section a b", "git config --remove-section a",
		"git config --edit", "git config -e", "git config --local a.b c",
		"git config set a.b c", "git config unset a.b", "git config edit", "git config a.b c",
		"git config --type bool a.b true"} {
		e.blocked(cmd, "qa", "")
	}
}

func TestRepoCfgOtherDirectoriesAndShapesBlocked(t *testing.T) {
	e := newRc(t)
	for _, role := range []string{"coder", "qa"} {
		e.blocked("git -C "+e.wt+" config core.hooksPath x", role, e.tmp)
		e.blocked("git -C "+e.home+" config core.hooksPath x", role, e.tmp)
		e.blocked("git --git-dir="+e.home+"/.git config core.hooksPath x", role, "")
		e.blocked("git --git-dir "+e.home+"/.git config core.hooksPath x", role, "")
		e.blocked("git --work-tree "+e.wt+" config core.hooksPath x", role, "")
		e.blocked("cd "+e.tmp+" ; git config core.hooksPath x", role, "")
		e.blocked("cd "+e.home+" && git config core.hooksPath x", role, "")
		e.blocked("cd /nonexistent ; git config core.hooksPath x", role, "")
		e.blocked("cd $SOMEWHERE && git config core.hooksPath x", role, "")
		e.blocked("GIT_DIR=/x/.git git config core.hooksPath x", role, "")
		e.blocked("env git config core.hooksPath x", role, "")
		e.blocked("sh -c 'git config core.hooksPath x'", role, "")
		e.blocked("git -c core.pager=cat config core.hooksPath x", role, "")
		e.blocked("git -c alias.c='config core.hooksPath x' c", role, "")
		e.blocked("git config --file "+e.home+"/.git/config core.hooksPath x", role, "")
		e.blocked("git config --file /tmp/elsewhere core.hooksPath x", role, "")
	}
}

func TestRepoCfgGitRemoteWritesBlockedReadsAllowed(t *testing.T) {
	e := newRc(t)
	for _, role := range []string{"coder", "qa"} {
		for _, sub := range []string{"add up ~/factory", "set-url origin x", "rename origin o", "remove origin", "rm origin",
			"set-head origin main", "set-branches origin main"} {
			e.blocked("git remote "+sub, role, "")
		}
		e.blocked("cd "+e.tmp+" ; git remote add up "+e.home, role, "")
		e.blocked("git -C "+e.home+" remote add up x", role, "")
		for _, cmd := range []string{"git remote", "git remote -v", "git remote show origin", "git remote get-url origin"} {
			e.allowed(cmd, role, "")
		}
	}
}

func TestRepoCfgReadsAllowed(t *testing.T) {
	e := newRc(t)
	for _, cmd := range []string{"git config --get user.name", "git config --get-all a.b", "git config --get-regexp user",
		"git config --list", "git config -l", "git config get user.name", "git config list",
		"git config --local --get a.b", "git config --show-origin --list", "git config user.name",
		"git -C " + e.home + " config --get remote.origin.url", "git --git-dir=" + e.home + "/.git config --list"} {
		for _, role := range []string{"coder", "judge"} { // T-0755: qa/reviewer git is read-only (allow-list)
			e.allowed(cmd, role, "")
		}
	}
}

func TestRepoCfgFileInsideScratchpadAllowed(t *testing.T) {
	e := newRc(t)
	for _, role := range []string{"coder", "judge"} {
		e.allowed("git config --file "+e.scratch+"/x.cfg k v", role, "")
		e.allowed("git config -f "+e.scratch+"/x.cfg --add k v", role, "")
		e.allowed("cd "+e.scratch+" && git config --file x.cfg k v", role, "")
	}
}

func TestRepoCfgCloneInsideScratchpadAllowedLinkedWorktreeNot(t *testing.T) {
	e := newRc(t)
	for _, role := range []string{"coder", "judge"} {
		e.allowed("git -C "+e.clone+" config user.name x", role, "")
		e.allowed("git -C "+e.clone+" remote add o ../elsewhere", role, "")
		e.allowed("cd "+e.clone+" && git config core.autocrlf false", role, "")
		e.allowed("git config core.autocrlf false", role, e.clone)
		e.allowed("git --git-dir "+e.clone+"/.git config a.b c", role, "")
		e.allowed("git -C "+e.scratch+"/bare.git config a.b c", role, "")
	}
	git(t, e.home, "worktree", "add", "-q", "-b", "factory/t-2", e.scratch+"/lw")
	e.blocked("git -C "+e.scratch+"/lw config core.hooksPath x", "coder", "")
	e.blocked("git config core.hooksPath x", "coder", e.scratch+"/lw")
}

func TestRepoCfgEditWriteAndRedirectsToRepoConfigBlocked(t *testing.T) {
	e := newRc(t)
	gitdir := e.home + "/.git"
	targets := []string{gitdir + "/config", e.wt + "/.git/config", e.wt + "/.git/config.worktree",
		gitdir + "/config.worktree", gitdir + "/worktrees/T-1/config.worktree", gitdir + "/info/attributes",
		e.scratch + "/bare.git/../../live/.git/config"}
	for _, role := range []string{"coder", "qa"} {
		for _, tg := range targets {
			for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
				r := e.hook(map[string]any{"tool_name": tool, "tool_input": map[string]any{"file_path": tg}}, role)
				if r.code != 2 {
					t.Errorf("%s %s: exit %d\n%s", tool, tg, r.code, r.stderr)
				}
				if !strings.Contains(r.stderr, "accident prevention") {
					t.Errorf("%s %s: stderr lacks 'accident prevention': %q", tool, tg, r.stderr)
				}
			}
			for _, cmd := range []string{"echo x >> " + tg, "echo x > " + tg, "tee -a " + tg, "cp /tmp/f " + tg, "mv /tmp/f " + tg,
				"ln -sf /tmp/f " + tg, "install /tmp/f " + tg, "sed -i s/a/b/ " + tg, "dd if=/tmp/f of=" + tg} {
				e.blocked(cmd, role, "")
			}
		}
		e.blocked("echo x >> .git/config", role, e.wt)
		e.blocked("echo x >> ../../.git/config", role, e.wt)
		e.blocked("echo x >> .git/info/attributes", role, e.home)
	}
}

func TestRepoCfgBareRepoConfigBlockedOutsideScratchpadOnly(t *testing.T) {
	e := newRc(t)
	bare := filepath.Join(e.tmp, "bare.git")
	git(t, e.tmp, "init", "-q", "--bare", bare)
	e.blocked("echo x >> "+bare+"/config", "coder", "")
	e.blocked("echo x >> "+bare+"/info/attributes", "coder", "")
	e.allowed("echo x >> "+e.scratch+"/bare.git/config", "coder", "")
	e.allowed("echo x >> "+e.clone+"/.git/config", "coder", "")
	r := e.hook(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": e.clone + "/.git/config"}}, "coder")
	wantExit(t, "Write clone .git/config", r, 0)
}

func TestRepoCfgReadingAndUnrelatedConfigFilesAllowed(t *testing.T) {
	e := newRc(t)
	gitdir := e.home + "/.git"
	for _, cmd := range []string{"cat " + gitdir + "/config", "grep url " + gitdir + "/config", "cp " + gitdir + "/config /tmp/c",
		"echo x > config", "echo x > config.json", "echo x > " + e.wt + "/config", "echo x > app/.git/hooks-x",
		"echo x > " + e.wt + "/docs/config.md", "echo x > ./info/attributes"} {
		e.allowed(cmd, "coder", "")
	}
}

func TestRepoCfgMessageExplainsTheAlternative(t *testing.T) {
	e := newRc(t)
	err := e.blocked("git config core.hooksPath x", "coder", "").stderr
	for _, w := range []string{"--file", "scratchpad", "not a security boundary"} {
		if !strings.Contains(err, w) {
			t.Errorf("stderr lacks %q: %q", w, err)
		}
	}
}
