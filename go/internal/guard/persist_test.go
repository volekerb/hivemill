package guard

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Ported from tests/test_worker_guard_persist.py (T-0334: global/system git config, ~/.config/git, shell init
// files; Bash and the Edit/Write tools), tests/test_worker_guard_repocfg.py (T-0226: repo git config) and the
// persist cases of tests/test_worker_guard_data.py's neighbours. The command texts are verbatim; the expected exit is
// the one the Python test asserts. Everything runs through Check / PersistEdit, the entry points of the hook.

func pwHome(t *testing.T) string {
	t.Helper()
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Fatal(err)
	}
	return pyRealpath(u.HomeDir)
}

var protectedRel = []string{".gitconfig", ".config/git/config", ".config/git/attributes", ".config/git/ignore", ".bashrc", ".zshrc",
	".zshenv", ".profile", ".bash_profile", ".zprofile"}

var persistRoles = []string{"coder", "qa", "reviewer"}

type chk struct{ t *testing.T }

func (c chk) blocked(cmd, role, cwd string) {
	c.t.Helper()
	if err := Check(cmd, cwd, role, false); err == nil {
		c.t.Errorf("should block (%s): %q", role, cmd)
	} else if IsDelegate(err) {
		c.t.Errorf("%q: delegated (%v)", cmd, err)
	}
}

func (c chk) allowed(cmd, role, cwd string) {
	c.t.Helper()
	if err := Check(cmd, cwd, role, false); err != nil {
		c.t.Errorf("should allow (%s): %q: %v", role, cmd, err)
	}
}

func persistFix(t *testing.T) chk {
	t.Helper()
	clearTermsEnv(t)
	SessionID = ""
	unsetEnv(t, "WORKER_GUARD_LIVE_HOME")
	unsetEnv(t, "WORKER_GUARD_SCRATCH")
	return chk{t}
}

func TestPersistGitConfigGlobalAndSystemWritesBlocked(t *testing.T) {
	c := persistFix(t)
	for _, role := range persistRoles {
		for _, cmd := range []string{"git config --global user.name x", "git config --global --add core.fsmonitor /tmp/x",
			"git config --global --unset user.name", "git config --global --unset-all a.b",
			"git config --global --replace-all a.b c", "git config --global --edit",
			"git config --global -e", "git config --global --rename-section a b",
			"git config --global --remove-section a", "git config --system core.fsmonitor x",
			"git config --global set a.b c", "git config --global unset a.b",
			"git -c core.pager=cat config --global a.b c", "git -C /tmp config --global a.b c",
			"git config --glo a.b c", "git config a.b c --global",
			"env git config --global a.b c", "sh -c 'git config --global a.b c'"} {
			c.blocked(cmd, role, "")
		}
	}
}

func TestPersistGitConfigFileToProtectedPathBlocked(t *testing.T) {
	c := persistFix(t)
	pw := pwHome(t)
	for _, role := range persistRoles {
		for _, rel := range protectedRel {
			c.blocked("git config -f "+pw+"/"+rel+" core.fsmonitor x", role, "")
		}
	}
	for _, cmd := range []string{"git config --file ~/.gitconfig a.b c", "git config --file=$HOME/.config/git/config a.b c",
		"git config -f${HOME}/.gitconfig a.b c", "git config -f /etc/gitconfig a.b c",
		"git config -f ~/x/../.gitconfig a.b c", "git config --file ~/.gitconfig --unset a.b"} {
		c.blocked(cmd, "coder", "")
	}
}

func TestPersistAliasRoutesToGlobalConfigBlocked(t *testing.T) {
	c := persistFix(t)
	c.blocked("git -c alias.c=config c --global a.b c", "coder", "")
	c.blocked("git -c 'alias.c=!git config --global a.b c' c", "coder", "")
	c.blocked("git -c 'alias.c=!echo x >> ~/.bashrc' c", "coder", "")
}

func TestPersistBashWritesToEveryProtectedPathBlocked(t *testing.T) {
	c := persistFix(t)
	pw := pwHome(t)
	for _, role := range persistRoles {
		rels := protectedRel
		if role != "coder" {
			rels = protectedRel[:1]
		}
		for _, rel := range rels {
			for _, spell := range []string{"~/" + rel, "$HOME/" + rel, pw + "/" + rel, pw + "/x/../" + rel} {
				for _, tmpl := range []string{"echo x > {p}", "echo x >> {p}", "echo x | tee {p}", "cp /tmp/a {p}",
					"mv /tmp/a {p}", "ln -sf /tmp/a {p}", "install -m644 /tmp/a {p}",
					"sed -i s/a/b/ {p}", "dd if=/tmp/a of={p}", "bash -c 'echo x > {p}'"} {
					c.blocked(strings.ReplaceAll(tmpl, "{p}", spell), role, "")
				}
			}
		}
	}
	for _, cmd := range []string{"cp /tmp/a /etc/gitconfig", "echo x > /etc/gitconfig", "install /tmp/a /etc/gitconfig",
		"cp /tmp/a ~/.config/git/", "cp -t ~/.config/git /tmp/a", "mv ~/.bashrc /tmp/gone",
		"ln /tmp/a ~/.bashrc", "ln ~/.bashrc /tmp/hard", "echo x > ~/.config/git/sub/new"} {
		c.blocked(cmd, "coder", "")
	}
}

func TestPersistRelativeAndCdSpellingsBlocked(t *testing.T) {
	c := persistFix(t)
	pw := pwHome(t)
	c.blocked("cd ~ && echo x > .bashrc", "coder", "")
	c.blocked("cd && echo x >> .zshrc", "coder", "")
	c.blocked("cd ~/.config/git && echo x > config", "coder", "")
	c.blocked("echo x > .bashrc", "coder", pw)
	c.blocked("echo x > ../.zshrc", "coder", filepath.Join(pw, "factory"))
	c.blocked("cd $SOMEWHERE && echo x > .bashrc", "coder", "") // unknown cwd: the name alone decides
	c.blocked("echo x > ~/.bash*", "coder", "")
	c.blocked("echo x > ~/.{bashrc,motd}", "coder", "")
	c.blocked("echo x > $X/.bashrc", "coder", "")
}

func TestPersistSelfReviewBypassesBlocked(t *testing.T) {
	c := persistFix(t)
	for _, cmd := range []string{"cp /tmp/.bashrc ~/", "cp /tmp/.bashrc ~", "cp -t ~ /tmp/.gitconfig", "mv /tmp/.bashrc ~",
		"cp /tmp/config ~/.config/git/", "f=~/.bashrc; echo x > $f", "f=.bashrc; echo x > ~/$f",
		"export F=.bashrc; echo x > ~/$F", "echo x > ~/.$(echo bashrc)", "echo x > $(echo ~)/.bashrc",
		"echo x > ~/.bash$'r'c", "git -c alias.c='!git config --global' c a b",
		"echo x > ~/.config/g*/config", "echo x > ~/.config/*/config", "ln -sfn /tmp/e ~/.config"} {
		c.blocked(cmd, "coder", "")
	}
	for _, cmd := range []string{"cp /tmp/a.txt ~/", "f=/tmp/o; echo x > $f", "echo x > $(mktemp -d)/a", "echo x > $OUT/a",
		"cp .bashrc.sample /tmp/"} {
		c.allowed(cmd, "coder", "")
	}
}

func TestPersistSymlinkSpellingBlocked(t *testing.T) {
	c := persistFix(t)
	pw := pwHome(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{".bashrc", ".gitconfig", ".config/git"} {
		name := "l" + strings.ReplaceAll(strings.ReplaceAll(rel, "/", "_"), ".", "")
		link := filepath.Join(tmp, name)
		if err := os.Symlink(filepath.Join(pw, rel), link); err != nil {
			t.Fatal(err)
		}
		c.blocked("echo x > "+link, "coder", "")
		if rel == ".config/git" {
			c.blocked("echo x > "+link+"/config", "coder", "")
		} else {
			c.blocked("cp /tmp/a "+link, "coder", "")
		}
	}
}

func TestPersistInterpreterAndEditorNamingProtectedFileBlocked(t *testing.T) {
	c := persistFix(t)
	for _, cmd := range []string{"python3 -c \"open('/x/.bashrc','a')\"", "python3 -c \"open('~/.gi'+'tconfig','w')\"",
		"vim ~/.zshrc", "perl -pi -e 's/a/b/' ~/.profile", "python3 - <<'EOF'\nopen('/h/.bashrc','a').write('x')\nEOF",
		"awk 'BEGIN{print 1 > \"/h/.zshenv\"}'"} {
		c.blocked(cmd, "coder", "")
	}
}

func TestPersistReadOnlyAndRepoLocalFormsAllowed(t *testing.T) {
	c := persistFix(t)
	for _, role := range persistRoles {
		for _, cmd := range []string{"git config --global --get user.name", "git config --global user.name",
			"git config --global --list", "git config --global -l", "git config --global --get-all a.b",
			"git config --global --get-regexp user", "git config --global get user.name",
			"git config --global list", "git config --global --type bool --get a.b",
			"git config --system --list", "git config -f ~/.gitconfig --get user.name",
			"git config --file ~/.gitconfig --list", "git config --get user.name",
			"git config user.name", "git config --local --get a.b",
			"git -c alias.c=config c --get a.b",
			"cat ~/.bashrc", "grep PATH ~/.zshrc", "cat ~/.gitconfig ~/.config/git/config",
			"cp ~/.bashrc /tmp/bashrc.copy", "cp ~/.config/git/config /tmp/c", "ls ~/.config/git",
			"echo x > .bashrc.sample", "echo x > notes.profile", "echo x > ~/.bashrc.d.txt",
			"echo x > /tmp/gitconfig", "echo x > ~/.config/gitx", "echo x > ~/.config/gitkraken/c",
			"sed -i s/a/b/ src/.profile.in", "echo x >> out.txt", "tee notes.txt"} {
			c.allowed(cmd, role, "")
		}
	}
}

// layout builds the git directories the repo-config rules look at: a stand-in live checkout with a linked worktree,
// a clone and a bare repository in the worker's scratchpad.
type repoFix struct {
	tmp, home, wt, scratch, clone, bare string
}

func mkRepo(t *testing.T, dir string) {
	t.Helper()
	for _, d := range []string{filepath.Join(dir, "objects"), filepath.Join(dir, "info")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, dir, "HEAD", "ref: refs/heads/main\n")
}

func newRepoFix(t *testing.T) (*repoFix, chk) {
	t.Helper()
	c := persistFix(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &repoFix{tmp: tmp, home: filepath.Join(tmp, "live"), scratch: filepath.Join(tmp, "scratch")}
	f.wt = filepath.Join(f.home, "worktrees", "T-1")
	f.clone = filepath.Join(f.scratch, "clone")
	f.bare = filepath.Join(f.scratch, "bare.git")
	mkRepo(t, filepath.Join(f.home, ".git"))
	if err := os.MkdirAll(filepath.Join(f.home, ".git", "worktrees", "T-1"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(f.wt, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, f.wt, ".git", "gitdir: "+f.home+"/.git/worktrees/T-1\n")
	writeFile(t, filepath.Join(f.home, ".git", "worktrees", "T-1"), "commondir", "../..\n")
	mkRepo(t, filepath.Join(f.clone, ".git"))
	mkRepo(t, f.bare)
	t.Setenv("WORKER_GUARD_LIVE_HOME", f.home)
	t.Setenv("WORKER_GUARD_SCRATCH", f.scratch)
	return f, c
}

func TestPersistRepoLocalConfigInOwnWorktreeAllowed(t *testing.T) {
	c := persistFix(t)
	tmp := t.TempDir()
	mkRepo(t, filepath.Join(tmp, ".git"))
	for _, cmd := range []string{"echo x > .gitconfig", "echo x > .bashrc", "git config --get user.name", "git config --list"} {
		c.allowed(cmd, "coder", tmp)
	}
}

func TestPersistGitConfigWriteWithEnvAssignmentBlocked(t *testing.T) {
	c := persistFix(t)
	for _, role := range persistRoles {
		for _, cmd := range []string{"GIT_CONFIG=~/.gitconfig git config core.fsmonitor x",
			"export GIT_CONFIG=~/.gitconfig; git config core.fsmonitor x",
			"GIT_CONFIG=$HOME/.config/git/config git config a.b c",
			"env GIT_CONFIG_GLOBAL=~/.gitconfig git config a.b c",
			"declare -x GIT_CONFIG=~/.gitconfig; git config a.b c",
			"HOME=/tmp git config --file .gitconfig a.b c",
			"export XDG_CONFIG_HOME=/tmp/x && git config -f x/git/config a.b c"} {
			c.blocked(cmd, role, "")
		}
	}
}

func TestPersistRepoLocalConfigAndReadsStayAllowed(t *testing.T) {
	c := persistFix(t)
	for _, cmd := range []string{"git config --get a.b", "git -C . config --list", "git config --get user.name",
		"awk 1 ~/.zshrc", "awk '{print $1}' ~/.gitconfig", "echo HOME=1; git status"} {
		c.allowed(cmd, "coder", "")
	}
	c.blocked("awk -i inplace 1 ~/.zshrc", "coder", "")
}

func TestPersistHQModeUnchanged(t *testing.T) {
	persistFix(t)
	pw := pwHome(t)
	for _, cmd := range []string{"git config --global user.name x", "echo x > ~/.bashrc", "cp /tmp/a ~/.gitconfig"} {
		if err := Check(cmd, "", "", true); err != nil {
			t.Errorf("hq %q: %v", cmd, err)
		}
	}
	// HQ settings do not route the Edit tools to the hook (main skips PersistEdit with --hq), the rule itself blocks
	if code, _ := PersistEdit(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": pw + "/.bashrc"}}); code != 2 {
		t.Errorf("PersistEdit on the rc file: exit %d, want 2", code)
	}
}

func editCall(tool, key, path, cwd string) (int, string) {
	return PersistEdit(map[string]any{"tool_name": tool, "cwd": cwd, "tool_input": map[string]any{key: path}})
}

func TestPersistEditWritePayloadsToProtectedPathsBlocked(t *testing.T) {
	persistFix(t)
	pw := pwHome(t)
	for _, role := range persistRoles {
		_ = role // the Edit rule is the same for every worker role (the settings route it, see main)
		for _, tk := range [][2]string{{"Edit", "file_path"}, {"Write", "file_path"}, {"MultiEdit", "file_path"}, {"NotebookEdit", "notebook_path"}} {
			rels := protectedRel[:3]
			if tk[0] == "Write" {
				rels = protectedRel
			}
			for _, rel := range rels {
				for _, spell := range []string{pw + "/" + rel, "~/" + rel, "$HOME/" + rel, pw + "/x/../" + rel} {
					if code, msg := editCall(tk[0], tk[1], spell, "/tmp"); code != 2 {
						t.Errorf("%s %s: exit %d %q, want 2", tk[0], spell, code, msg)
					}
				}
			}
		}
		if code, _ := editCall("Write", "file_path", "/etc/gitconfig", "/tmp"); code != 2 {
			t.Error("/etc/gitconfig allowed")
		}
	}
	if code, _ := editCall("Edit", "file_path", ".bashrc", pw); code != 2 {
		t.Error("relative .bashrc in the home allowed")
	}
	if code, _ := editCall("Write", "file_path", "../.zshrc", filepath.Join(pw, "factory")); code != 2 {
		t.Error("../.zshrc from ~/factory allowed")
	}
}

func TestPersistEditSymlinkPayloadBlocked(t *testing.T) {
	persistFix(t)
	pw := pwHome(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(tmp, "rc")
	if err := os.Symlink(filepath.Join(pw, ".zshrc"), link); err != nil {
		t.Fatal(err)
	}
	if code, _ := editCall("Write", "file_path", link, "/tmp"); code != 2 {
		t.Error("write through a symlink to the rc file allowed")
	}
}

func TestPersistEditOtherPathsAllowedAndUnreadablePayloadFailsClosed(t *testing.T) {
	persistFix(t)
	pw := pwHome(t)
	for _, path := range []string{"/tmp/x.txt", pw + "/factory/README.md", pw + "/.config/gitx", pw + "/.bashrc.bak", "notes.md"} {
		if code, msg := editCall("Write", "file_path", path, "/tmp"); code != 0 {
			t.Errorf("%s: exit %d %q, want 0", path, code, msg)
		}
	}
	for _, data := range []map[string]any{
		{"tool_name": "Write", "tool_input": map[string]any{}},
		{"tool_name": "Edit", "tool_input": map[string]any{"file_path": float64(7)}},
		{"tool_name": "Edit", "tool_input": "x"},
		{"tool_name": "Edit"},
		{"tool_name": "Edit", "tool_input": map[string]any{"file_path": "", "notebook_path": ""}},
	} {
		if code, msg := PersistEdit(data); code != 2 || !strings.Contains(msg, "without a readable file_path") {
			t.Errorf("%v: exit %d %q, want 2 failing closed", data, code, msg)
		}
	}
	// the falsy file_path falls through to notebook_path, as `or` does
	if code, _ := PersistEdit(map[string]any{"tool_input": map[string]any{"file_path": "", "notebook_path": pw + "/.bashrc"}}); code != 2 {
		t.Error("empty file_path with a protected notebook_path allowed")
	}
}

// ── tests/test_worker_guard_repocfg.py ──

func TestRepoCfgExecCapableKeysBlockedFromAWorktreeForEveryRole(t *testing.T) {
	f, c := newRepoFix(t)
	for _, role := range []string{"coder", "qa", "reviewer", "judge", "cleaner", "pm", "scout", "evaluator"} {
		for _, kv := range []string{"filter.x.smudge cmd", "filter.x.clean cmd", "diff.x.textconv cmd", "core.hooksPath x",
			"core.fsmonitor cmd", "core.sshCommand cmd", "remote.origin.uploadpack cmd"} {
			c.blocked("git config "+kv, role, f.wt)
		}
	}
}

func TestRepoCfgEveryWriteFormBlocked(t *testing.T) {
	f, c := newRepoFix(t)
	for _, cmd := range []string{"git config --add a.b c", "git config --unset a.b", "git config --unset-all a.b",
		"git config --replace-all a.b c", "git config --rename-section a b", "git config --remove-section a",
		"git config --edit", "git config -e", "git config --local a.b c", "git config --worktree a.b c",
		"git config set a.b c", "git config unset a.b", "git config edit", "git config a.b c",
		"git config --type bool a.b true"} {
		c.blocked(cmd, "qa", f.wt)
	}
}

func TestRepoCfgOtherDirectoriesAndShapesBlocked(t *testing.T) {
	f, c := newRepoFix(t)
	for _, role := range []string{"coder", "qa"} {
		c.blocked("git -C "+f.wt+" config core.hooksPath x", role, f.tmp)
		c.blocked("git -C "+f.home+" config core.hooksPath x", role, f.tmp)
		c.blocked("git --git-dir="+f.home+"/.git config core.hooksPath x", role, f.wt)
		c.blocked("git --git-dir "+f.home+"/.git config core.hooksPath x", role, f.wt)
		c.blocked("git --work-tree "+f.wt+" config core.hooksPath x", role, f.wt)
		c.blocked("cd "+f.tmp+" ; git config core.hooksPath x", role, f.wt)
		c.blocked("cd "+f.home+" && git config core.hooksPath x", role, f.wt)
		c.blocked("cd /nonexistent ; git config core.hooksPath x", role, f.wt)
		c.blocked("cd $SOMEWHERE && git config core.hooksPath x", role, f.wt)
		c.blocked("GIT_DIR=/x/.git git config core.hooksPath x", role, f.wt)
		c.blocked("env git config core.hooksPath x", role, f.wt)
		c.blocked("sh -c 'git config core.hooksPath x'", role, f.wt)
		c.blocked("git -c core.pager=cat config core.hooksPath x", role, f.wt)
		c.blocked("git -c alias.c='config core.hooksPath x' c", role, f.wt)
		c.blocked("git config --file "+f.home+"/.git/config core.hooksPath x", role, f.wt)
		c.blocked("git config --file /tmp/elsewhere core.hooksPath x", role, f.wt)
	}
}

func TestRepoCfgGitRemoteWritesBlockedReadsAllowed(t *testing.T) {
	f, c := newRepoFix(t)
	for _, role := range []string{"coder", "qa"} {
		for _, sub := range []string{"add up ~/factory", "set-url origin x", "rename origin o", "remove origin", "rm origin",
			"set-head origin main", "set-branches origin main"} {
			c.blocked("git remote "+sub, role, f.wt)
		}
		c.blocked("cd "+f.tmp+" ; git remote add up "+f.home, role, f.wt)
		c.blocked("git -C "+f.home+" remote add up x", role, f.wt)
		for _, cmd := range []string{"git remote", "git remote -v", "git remote show origin", "git remote get-url origin"} {
			c.allowed(cmd, role, f.wt)
		}
	}
}

func TestRepoCfgReadsAllowed(t *testing.T) {
	f, c := newRepoFix(t)
	for _, cmd := range []string{"git config --get user.name", "git config --get-all a.b", "git config --get-regexp user",
		"git config --list", "git config -l", "git config get user.name", "git config list",
		"git config --local --get a.b", "git config --show-origin --list", "git config user.name",
		"git -C " + f.home + " config --get remote.origin.url", "git --git-dir=" + f.home + "/.git config --list"} {
		for _, role := range []string{"coder", "qa"} {
			c.allowed(cmd, role, f.wt)
		}
	}
}

func TestRepoCfgFileInsideScratchpadAllowed(t *testing.T) {
	f, c := newRepoFix(t)
	for _, role := range []string{"coder", "qa"} {
		c.allowed("git config --file "+f.scratch+"/x.cfg k v", role, f.wt)
		c.allowed("git config -f "+f.scratch+"/x.cfg --add k v", role, f.wt)
		c.allowed("cd "+f.scratch+" && git config --file x.cfg k v", role, f.wt)
	}
}

func TestRepoCfgCloneInsideScratchpadAllowedLinkedWorktreeNot(t *testing.T) {
	f, c := newRepoFix(t)
	for _, role := range []string{"coder", "qa"} {
		c.allowed("git -C "+f.clone+" config user.name x", role, f.wt)
		c.allowed("git -C "+f.clone+" remote add o ../elsewhere", role, f.wt)
		c.allowed("cd "+f.clone+" && git config core.autocrlf false", role, f.wt)
		c.allowed("git config core.autocrlf false", role, f.clone)
		c.allowed("git --git-dir "+f.clone+"/.git config a.b c", role, f.wt)
		c.allowed("git -C "+f.scratch+"/bare.git config a.b c", role, f.wt)
	}
	// a linked worktree in the scratchpad shares the live checkout's config
	lw := filepath.Join(f.scratch, "lw")
	if err := os.MkdirAll(filepath.Join(f.home, ".git", "worktrees", "lw"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(f.home, ".git", "worktrees", "lw"), "commondir", "../..\n")
	if err := os.MkdirAll(lw, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, lw, ".git", "gitdir: "+f.home+"/.git/worktrees/lw\n")
	c.blocked("git -C "+lw+" config core.hooksPath x", "coder", f.wt)
	c.blocked("git config core.hooksPath x", "coder", lw)
}

func TestRepoCfgEditWriteAndRedirectsToRepoConfigBlocked(t *testing.T) {
	f, c := newRepoFix(t)
	gitdir := filepath.Join(f.home, ".git")
	targets := []string{gitdir + "/config", f.wt + "/.git/config", f.wt + "/.git/config.worktree",
		gitdir + "/config.worktree", gitdir + "/worktrees/T-1/config.worktree", gitdir + "/info/attributes",
		f.scratch + "/bare.git/../../live/.git/config"}
	for _, role := range []string{"coder", "qa"} {
		for _, target := range targets {
			for _, tool := range []string{"Edit", "Write", "MultiEdit"} {
				code, msg := PersistEdit(map[string]any{"tool_name": tool, "tool_input": map[string]any{"file_path": target}})
				if code != 2 || !strings.Contains(msg, "accident prevention") {
					t.Errorf("%s %s: exit %d %q, want 2 with the repo-config reason", tool, target, code, msg)
				}
			}
			for _, cmd := range []string{"echo x >> " + target, "echo x > " + target, "tee -a " + target, "cp /tmp/f " + target,
				"mv /tmp/f " + target, "ln -sf /tmp/f " + target, "install /tmp/f " + target, "sed -i s/a/b/ " + target,
				"dd if=/tmp/f of=" + target} {
				c.blocked(cmd, role, f.wt)
			}
		}
		c.blocked("echo x >> .git/config", role, f.wt)
		c.blocked("echo x >> ../../.git/config", role, f.wt)
		c.blocked("echo x >> .git/info/attributes", role, f.home)
	}
}

func TestRepoCfgBareRepoConfigBlockedOutsideScratchpadOnly(t *testing.T) {
	f, c := newRepoFix(t)
	bare := filepath.Join(f.tmp, "bare.git")
	mkRepo(t, bare)
	c.blocked("echo x >> "+bare+"/config", "coder", f.wt)
	c.blocked("echo x >> "+bare+"/info/attributes", "coder", f.wt)
	c.allowed("echo x >> "+f.scratch+"/bare.git/config", "coder", f.wt)
	c.allowed("echo x >> "+f.clone+"/.git/config", "coder", f.wt)
	if code, msg := PersistEdit(map[string]any{"tool_name": "Write", "tool_input": map[string]any{"file_path": f.clone + "/.git/config"}}); code != 0 {
		t.Errorf("Write into the scratchpad clone: exit %d %q, want 0", code, msg)
	}
}

func TestRepoCfgReadingAndUnrelatedConfigFilesAllowed(t *testing.T) {
	f, c := newRepoFix(t)
	gitdir := filepath.Join(f.home, ".git")
	for _, cmd := range []string{"cat " + gitdir + "/config", "grep url " + gitdir + "/config", "cp " + gitdir + "/config /tmp/c",
		"echo x > config", "echo x > config.json", "echo x > " + f.wt + "/config", "echo x > app/.git/hooks-x",
		"echo x > " + f.wt + "/docs/config.md", "echo x > ./info/attributes"} {
		c.allowed(cmd, "coder", f.wt)
	}
}

func TestRepoCfgMessageExplainsTheAlternative(t *testing.T) {
	f, _ := newRepoFix(t)
	err := Check("git config core.hooksPath x", f.wt, "coder", false)
	if err == nil {
		t.Fatal("allowed")
	}
	msg := Explain(err)
	for _, want := range []string{"--file", "scratchpad", "not a security boundary"} {
		if !strings.Contains(msg, want) {
			t.Errorf("stderr %q lacks %q", msg, want)
		}
	}
}

// ── helpers of the persist rules ──

func TestPersistHelpers(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"a", []string{"a"}},
		{"~/.{bashrc,motd}", []string{"~/.bashrc", "~/.motd"}},
		{"{a,b}{c,d}", []string{"ac", "ad", "bc", "bd"}},
		{"{a}", []string{"{a}"}},
		{"{a,{b,c}}", []string{"a", "b", "a", "c"}},
		{"x{,y}", []string{"x", "xy"}},
	} {
		if got := braceExpand(c.in); strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("braceExpand(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{
		{"$A/x", "1/x"}, {"${A}x", "1x"}, {"$B", "$B"}, {"$", "$"}, {"${", "${"}, {"${A", "1"}, {"$é", "$é"},
	} {
		if got := substVars(c.in, map[string]string{"A": "1"}); got != c.want {
			t.Errorf("substVars(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
