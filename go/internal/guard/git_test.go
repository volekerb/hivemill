package guard

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Section 3 tests: the cases of tests/test_worker_guard_git.py (plus the git cases of test_worker_guard.py and
// test_worker_guard_persist.py), run through Check. The expected exit is the Python one; where the decision reads the
// factory DB (git_push_pin, public text) and the DB file exists, Go delegates to the Python hook, which the tests
// accept as a decision (the delegation itself is tested with the real hook in cmd/worker-guard).

type gitFix struct {
	tmp, home, wt, scratch, link string
}

func newGitFix(t *testing.T) *gitFix {
	t.Helper()
	clearTermsEnv(t)
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := &gitFix{tmp: tmp, home: filepath.Join(tmp, "factory")}
	f.wt = filepath.Join(f.home, "worktrees", "G-007-T-x")
	for _, d := range []string{filepath.Join(f.home, ".git", "worktrees", "G-007-T-x"), filepath.Join(f.home, "roles"), f.wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeFile(t, f.wt, ".git", "gitdir: "+f.home+"/.git/worktrees/G-007-T-x\n")
	scratch, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.scratch = scratch // outside <tmp>/factory: the worker's scratchpad
	f.link = filepath.Join(scratch, "link")
	if err := os.Symlink(f.home, f.link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", tmp)
	t.Setenv("FACTORY_AGENT", "coder-t-7")
	t.Setenv("WORKER_GUARD_LIVE_HOME", f.home)
	t.Setenv("WORKER_GUARD_SCRATCH", f.scratch)
	SessionID = ""
	return f
}

// check runs the hook's worker mode for role in cwd.
func (f *gitFix) check(cmd, cwd, role string) error { return Check(cmd, cwd, role, false) }

// blocked asserts a real block (exit 2) that is not a stub of a later section.
func (f *gitFix) blocked(t *testing.T, cmd, cwd, role string) error {
	t.Helper()
	err := f.check(cmd, cwd, role)
	if err == nil || IsDelegate(err) {
		t.Errorf("should block: %q (cwd %s, role %s): err = %v", cmd, cwd, role, err)
	}
	return err
}

// allowed asserts exit 0, or the fail-closed stub of a rule another task owns; never a git/shared-checkout block.
func (f *gitFix) allowed(t *testing.T, cmd, cwd, role string) {
	t.Helper()
	if err := f.check(cmd, cwd, role); err != nil && !IsDelegate(err) {
		t.Errorf("should allow: %q (cwd %s, role %s): %v", cmd, cwd, role, err)
	}
}

func TestGitSharedCheckout(t *testing.T) {
	f := newGitFix(t)
	clone := filepath.Join(f.home, "worktrees", "G-007-T-clone")
	if err := os.MkdirAll(filepath.Join(clone, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	// T-0165: a clone under worktrees/ has a .git DIRECTORY but is not the live home
	f.allowed(t, "git commit -m x", clone, "coder")
	f.allowed(t, "git switch -c factory/t-x", clone, "coder")
	f.blocked(t, "git commit -m x", f.home, "coder")

	for _, cmd := range []string{"git commit -m x", "git stash", "git checkout -b foo", "git reset --hard", "git branch -D x",
		"git worktree add ../y"} {
		err := f.blocked(t, cmd, f.home, "coder")
		var gb *GitBlock
		if !errors.As(err, &gb) {
			t.Errorf("%q: err = %T %v, want a GitBlock", cmd, err, err)
			continue
		}
		msg := Explain(err)
		for _, want := range []string{"`" + cmd + "`", "own worktree", "shared checkout ~/factory (branch main)"} {
			if !strings.Contains(msg, want) {
				t.Errorf("%q: stderr %q lacks %q", cmd, msg, want)
			}
		}
	}
	for _, cmd := range []string{"git push origin main", "git merge origin/main", "git rebase main", "git add .", "git pull",
		"git switch x", "git restore f", "git clean -fd", "git tag v1", "git branch newbranch",
		"git branch -m a b", "git config user.name x", "git update-ref -d refs/x", "git gc",
		"git worktree prune", "git frobnicate"} {
		f.blocked(t, cmd, f.home, "coder")
	}
	for _, cmd := range []string{"git log --oneline -5", "git status", "git diff", "git fetch origin", "git branch",
		"git worktree list", "git branch --list", "git branch -a", "git branch -r -v",
		"git remote -v", "git config --get user.name", "git config --list", "git show HEAD",
		"git rev-parse HEAD", "git ls-files", "git blame f", "git grep x", "git --version"} {
		if err := f.check(cmd, f.home, "coder"); err != nil {
			t.Errorf("read-only %q: %v", cmd, err)
		}
	}
	// a subdirectory of the shared checkout is shared
	f.blocked(t, "git commit -m x", filepath.Join(f.home, "roles"), "coder")
	f.blocked(t, "git commit -m x", filepath.Join(f.home, "worktrees"), "coder")
}

func TestGitWithoutOverrideTheRealHomeIsTheTarget(t *testing.T) {
	f := newGitFix(t)
	t.Setenv("WORKER_GUARD_LIVE_HOME", "")
	// the fixture home is not the live home, so the fixture checkout is not guarded
	f.allowed(t, "git commit -m x", f.home, "coder")
}

func TestGitLiveHomeOverrideNamesTheSharedCheckout(t *testing.T) {
	f := newGitFix(t)
	if homes, err := gitHomes(); err != nil || len(homes) != 1 || homes[0] != f.home {
		t.Errorf("gitHomes with WORKER_GUARD_LIVE_HOME = %v, %v; want [%s]", homes, err, f.home)
	}
}

func TestGitArchiveRefused(t *testing.T) {
	f := newGitFix(t)
	h, s := f.home, f.scratch
	rel, _ := filepath.Rel(s, h)
	for _, cmd := range []string{
		"git archive -o " + h + "/x.tar origin/main",
		"git archive --output=" + h + "/x.tar origin/main",
		"git archive -o x.tar origin/main",
		"git archive -o " + s + "/" + rel + "/x.tar origin/main",
		"git archive -o " + f.link + "/x.tar origin/main",
		"git archive -o ~/nowhere/../factory/x.tar origin/main",
		"git archive -o '~' HEAD", "git archive --output=~ HEAD", "git archive -o '~/x' HEAD",
		"git -c tar.foo.command='tee roles/pwn' archive --format=foo HEAD",
		"git --config-env=tar.foo.command=FOO archive --format=foo HEAD",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee x' git archive HEAD",
		"GIT_EXEC_PATH=/tmp git archive HEAD",
		"git archive --remote=origin origin/main",
		"git archive --remote origin origin/main",
		"git archive --exec=/bin/true origin/main",
		"git archive --exec /bin/true origin/main",
		"git archive -o $SCRATCH/x.tar origin/main",
		"git archive -o `pwd`/x.tar origin/main",
		"git archive --frobnicate origin/main",
		"git archive -o " + s + "/x.tar",
		"git archive -o " + h + " HEAD", "git archive --output=" + h + " HEAD",
		"git archive -o",
		"git archive origin/main | tar -x -C " + s + "/d", "git archive HEAD -- roles",
		"git archive --format=tar --prefix=p/ -o " + s + "/x.tar origin/main",
		"git archive --output " + s + "/x.tar origin/main",
		"HOME=" + s + " git archive --format=foo HEAD",
		"XDG_CONFIG_HOME=" + s + " git archive --format=foo HEAD",
		"export HOME=" + s + "; git archive --format=foo HEAD",
	} {
		f.blocked(t, cmd, h, "coder")
	}
}

func TestGitInitScratchAllowedSharedRefused(t *testing.T) {
	f := newGitFix(t)
	s, h := f.scratch, f.home
	for _, cmd := range []string{"git init -q -b main " + s + "/r", "git init " + s + "/r", "git init --bare " + s + "/r"} {
		f.allowed(t, cmd, h, "coder")
		if err := f.check(cmd, h, "coder"); err != nil {
			t.Errorf("%q: %v (git init of a scratch dir has no stub)", cmd, err)
		}
	}
	for _, cmd := range []string{"git init '~'", "git init '~/r'", "export GIT_DIR=" + h + "/.git; git init " + s + "/r",
		"git init", "git init -q -b main", "git init " + h + "/sub", "git init r", "git init " + f.link,
		"git init --separate-git-dir=" + h + "/g " + s + "/r", "git init --template=" + s + "/t " + s + "/r",
		"git init " + s + "/a " + s + "/b", "git init $SCRATCH/r",
		// M5368: only the own scratchpad; ancestors, homes, linked worktrees, / and ~ are refused
		"git init " + f.tmp, "git init /", "git init ~", "git init " + h + "/..", "git init " + f.wt,
		"git init " + h + "/worktrees/W", "git init /etc/x", "git init " + s + "/../x",
		"git init " + s + "/link/r"} {
		f.blocked(t, cmd, h, "coder")
	}
}

func TestGitInitScratchpadFromSessionID(t *testing.T) {
	f := newGitFix(t)
	t.Setenv("WORKER_GUARD_SCRATCH", "")
	base := fmt.Sprintf("/tmp/claude-%d/-proj", os.Getuid())
	run := func(cmd, sid string) error {
		SessionID = sid
		defer func() { SessionID = "" }()
		return f.check(cmd, f.home, "coder")
	}
	if err := run("git init "+base+"/abc/scratchpad/r", "abc"); err != nil {
		t.Errorf("own scratchpad: %v", err)
	}
	for _, c := range [][2]string{
		{"git init " + base + "/abc/scratchpad/r", "other"}, {"git init " + base + "/abc/scratchpad/r", ""},
		{"git init " + base + "/abc/r", "abc"}, {"git init " + base + "/abc/scratchpad/../r", "abc"},
		{fmt.Sprintf("git init /tmp/claude-%d/abc/scratchpad/r", os.Getuid()+1), "abc"},
		{"git init " + base + "/../scratchpad/r", ".."}, {"git init " + base + "/a b/scratchpad/r", "a b"},
	} {
		if err := run(c[0], c[1]); err == nil || IsDelegate(err) {
			t.Errorf("%q with session %q: err = %v, want a block", c[0], c[1], err)
		}
	}
}

func TestGitEnvConfigAnywhereRefusesInit(t *testing.T) {
	f := newGitFix(t)
	s := f.scratch
	for _, env := range []string{
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee roles/pwn'",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=tar.foo.command GIT_CONFIG_VALUE_0='tee roles/pwn'",
		"export GIT_CONFIG_PARAMETERS=\"'tar.foo.command'='tee roles/pwn'\"",
		"export GIT_CONFIG_GLOBAL=" + s + "/gitconfig",
		"env GIT_CONFIG_GLOBAL=" + s + "/gitconfig true",
		"declare -x GIT_CONFIG_SYSTEM=" + s + "/gitconfig",
	} {
		for _, cmd := range []string{env + "; git init " + s + "/r", env + " && git init -q " + s + "/r"} {
			f.blocked(t, cmd, f.home, "coder")
		}
	}
	f.blocked(t, "git --git-dir="+f.home+"/.git archive origin/main", f.home, "coder")
	f.blocked(t, "GIT_DIR="+f.home+"/.git git archive origin/main", f.home, "coder")
}

func TestGitHQModeUnchanged(t *testing.T) {
	f := newGitFix(t)
	for _, cmd := range []string{"git archive -o " + f.home + "/x.tar origin/main", "git archive --remote=origin main",
		"git init " + f.scratch + "/r"} {
		if err := Check(cmd, f.home, "", true); err != nil {
			t.Errorf("hq %q: %v", cmd, err)
		}
	}
}

func TestGitLinkedWorktreeAllowed(t *testing.T) {
	f := newGitFix(t)
	f.allowed(t, "git commit -m x", f.wt, "coder")
	t.Setenv("FACTORY_HOME", dbHome(t)) // the pin reads the recorded worktree of the task: Python's decision
	f.allowed(t, "git push origin HEAD:factory/t-7", f.wt, "coder")
	t.Setenv("FACTORY_HOME", t.TempDir())
	if err := os.MkdirAll(filepath.Join(f.wt, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	f.allowed(t, "git commit -m x", filepath.Join(f.wt, "sub"), "coder")
}

func TestGitRedirections(t *testing.T) {
	f := newGitFix(t)
	h := f.home
	for _, cmd := range []string{"cd " + h + " && git commit -m x", "cd " + h + "; git commit -m x", "git -C " + h + " reset --hard",
		"git -C ../.. commit", "git -C ../../ -C . commit -m x",
		"cd $HOME/factory && git add .", "git -C $X commit", "bash -c 'cd " + h + " && git commit -m x'",
		"cd ~/factory && git commit -m x", "env GIT_DIR=" + h + "/.git git commit",
		"git --git-dir=" + h + "/.git commit", "git --work-tree=" + h + " checkout x",
		"GIT_WORK_TREE=" + h + " git checkout x", "timeout 5 git -C " + h + " commit -m x"} {
		f.blocked(t, cmd, f.wt, "coder")
	}
	for _, cmd := range []string{"cd " + f.wt + " && git commit -m x", "git -C . commit -m x",
		"bash -c 'cd " + h + " && git status'",
		"env GIT_DIR=x git status",
		"bash -c 'cd " + h + "' && git commit -m x", // a cd inside sh -c does not move the caller
		"cd " + h + " && cd " + f.wt + " && git commit -m x"} {
		f.allowed(t, cmd, f.wt, "coder")
	}
}

func TestGitCdForms(t *testing.T) {
	f := newGitFix(t)
	// pushd moves the directory, popd makes it unknown (dynamic), `cd -` too; plain `cd` goes home (HOME=tmp).
	f.blocked(t, "pushd "+f.home+" && git commit -m x", f.wt, "coder")
	f.blocked(t, "popd && git commit -m x", f.wt, "coder")
	f.blocked(t, "cd - && git commit -m x", f.wt, "coder")
	f.blocked(t, "cd $X && git commit -m x", f.wt, "coder")
	f.allowed(t, "cd -P "+f.wt+" && git commit -m x", f.wt, "coder")
	f.allowed(t, "cd && git commit -m x", f.home, "coder") // HOME is <tmp>, not a git checkout
}

func TestGitRoles(t *testing.T) {
	f := newGitFix(t)
	for _, role := range []string{"coder", "qa", "pm", "staff-engineer", "reviewer", "judge", "cleaner"} {
		f.blocked(t, "git commit -m x", f.home, role)
	}
	for _, cmd := range []string{"git worktree remove /tmp/x", "git worktree prune", "git branch -d factory/t-x"} {
		f.allowed(t, cmd, f.home, "cleaner")
		f.blocked(t, cmd, f.home, "coder")
	}
	for _, cmd := range []string{"git worktree remove --force x", "git worktree remove -f x", "git branch -D x",
		"git commit -m x", "git worktree add ../y", "git checkout main"} {
		f.blocked(t, cmd, f.home, "cleaner")
	}
}

func TestGitNoFalsePositives(t *testing.T) {
	f := newGitFix(t)
	cmds := []string{"git status", "git diff", "git log --oneline -5", "git add -A", "git commit -F /tmp/m",
		"git fetch origin", "git checkout --detach origin/x", "git rebase origin/main",
		"git merge origin/main", "git stash list", "git worktree list", "git branch -a", "git remote -v",
		"git config --get user.name", `grep "git commit" README.md`, "echo git reset --hard", "echo cd /x && echo git commit"}
	if len(cmds) < 12 {
		t.Fatal("table too short")
	}
	for _, cmd := range cmds {
		f.allowed(t, cmd, f.wt, "coder")
	}
	for _, cmd := range []string{`grep "git commit" README.md`, "echo git reset --hard"} {
		if err := f.check(cmd, f.home, "coder"); err != nil {
			t.Errorf("text-only mention %q from the shared checkout: %v", cmd, err)
		}
	}
}

func TestGitStatusAllowedCommitBlockedInLiveHome(t *testing.T) {
	f := newGitFix(t)
	// the live-home fixture: status allowed, commit blocked (the hook only inspects the text)
	if err := f.check("git status", f.home, "coder"); err != nil {
		t.Errorf("git status: %v", err)
	}
	f.blocked(t, "git commit -m x", f.home, "coder")
}

func TestGitBranchMutates(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false}, {[]string{"x"}, true}, {[]string{"--list"}, false}, {[]string{"-a"}, false}, {[]string{"-r", "-v"}, false},
		{[]string{"-D", "x"}, true}, {[]string{"-m", "a", "b"}, true}, {[]string{"--show-current"}, false},
		{[]string{"--contains", "HEAD"}, false}, {[]string{"--contains=HEAD"}, false}, {[]string{"--sort", "x", "--list"}, false},
		{[]string{"--list", "x*"}, false}, {[]string{"--frob"}, true}, {[]string{"-lx"}, true}, {[]string{"-q", "x"}, true},
		{[]string{"--"}, true}, {[]string{"-"}, true}, {[]string{"-v"}, false}, {[]string{"--verbose", "x"}, true},
	} {
		if got := branchMutates(c.args); got != c.want {
			t.Errorf("branchMutates(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

func TestGitConfigMutates(t *testing.T) {
	for _, c := range []struct {
		args       []string
		mut, write bool
	}{
		{[]string{"--get", "user.name"}, false, false}, {[]string{"--list"}, false, false}, {[]string{"user.name"}, false, false},
		{[]string{"user.name", "x"}, true, true}, {[]string{"--unset", "k"}, true, true}, {[]string{"--add", "k", "v"}, true, true},
		{[]string{"set", "k", "v"}, true, true}, {[]string{"unset", "k"}, true, true}, {[]string{"edit"}, true, true},
		{[]string{"get", "k"}, false, false}, {[]string{"list"}, false, false}, {[]string{"--global", "k", "v"}, true, true},
		// config_args_write knows value options; config_mutates does not
		{[]string{"--type", "bool", "k"}, true, false}, {[]string{"--default", "d", "--get", "k"}, false, false},
		{[]string{"--show-origin", "k", "v"}, false, false}, {[]string{"--edit"}, true, true}, {[]string{"-e"}, true, true},
	} {
		if got := configMutates(c.args); got != c.mut {
			t.Errorf("configMutates(%q) = %v, want %v", c.args, got, c.mut)
		}
		if got := configArgsWrite(c.args); got != c.write {
			t.Errorf("configArgsWrite(%q) = %v, want %v", c.args, got, c.write)
		}
	}
}

func TestGitMutatesTable(t *testing.T) {
	for _, c := range []struct {
		sub  string
		args []string
		want bool
	}{
		{"status", nil, false}, {"--version", nil, false}, {"commit", nil, true}, {"frobnicate", nil, true},
		{"worktree", nil, false}, {"worktree", []string{"list"}, false}, {"worktree", []string{"add", "x"}, true},
		{"remote", nil, false}, {"remote", []string{"-v"}, false}, {"remote", []string{"--verbose"}, false},
		{"remote", []string{"show", "o"}, false}, {"remote", []string{"get-url", "o"}, false}, {"remote", []string{"add", "o", "u"}, true},
		{"notes", nil, false}, {"notes", []string{"list"}, false}, {"notes", []string{"show"}, false}, {"notes", []string{"add"}, true},
		{"branch", []string{"x"}, true}, {"branch", nil, false}, {"config", []string{"k", "v"}, true}, {"config", []string{"--get", "k"}, false},
	} {
		if got := gitMutates(c.sub, c.args); got != c.want {
			t.Errorf("gitMutates(%q, %q) = %v, want %v", c.sub, c.args, got, c.want)
		}
	}
}

func TestGitCleanerOK(t *testing.T) {
	for _, c := range []struct {
		sub  string
		args []string
		want bool
	}{
		{"worktree", []string{"remove", "x"}, true}, {"worktree", []string{"remove", "--force", "x"}, false},
		{"worktree", []string{"remove", "-f", "x"}, false}, {"worktree", []string{"remove", "-fq", "x"}, false},
		{"worktree", []string{"remove", "-qf", "x"}, false}, {"worktree", []string{"remove", "-q", "x"}, true},
		{"worktree", []string{"prune"}, true}, {"worktree", []string{"add", "x"}, false}, {"worktree", nil, false},
		{"branch", []string{"-d", "x"}, true}, {"branch", []string{"-D", "x"}, false}, {"branch", []string{"-d"}, false},
		{"branch", []string{"-d", "x", "-f"}, false}, {"branch", []string{"x"}, false}, {"commit", nil, false},
	} {
		if got := gitCleanerOK(c.sub, c.args); got != c.want {
			t.Errorf("gitCleanerOK(%q, %q) = %v, want %v", c.sub, c.args, got, c.want)
		}
	}
}

func TestGitDeleteBranches(t *testing.T) {
	f := newGitFix(t)
	// every worker role is refused; the cleaner may delete factory/* branches only
	block := func(role string, cmds ...string) {
		for _, cmd := range cmds {
			f.blocked(t, cmd, f.wt, role)
		}
	}
	ok := func(role string, cmds ...string) {
		for _, cmd := range cmds {
			if err := f.check(cmd, f.wt, role); err != nil && !IsDelegate(err) {
				t.Errorf("%s %q: %v", role, cmd, err)
			}
		}
	}
	block("coder", "git branch -d factory/t-1", "git branch -D factory/t-1", "git branch --delete factory/t-1",
		"git branch -qd x", "git push origin --delete factory/t-1", "git push origin :factory/t-1", "git push -d origin x",
		"git push origin +:factory/t-1")
	ok("coder", "git branch", "git branch -a", "git branch --list")
	ok("cleaner", "git branch -d factory/t-1", "git branch -D factory/t-1 factory/t-2", "git push origin --delete factory/t-1",
		"git push origin :factory/t-1", "git push origin +:factory/a.b_c-1/x", "git push -d origin factory/t-1")
	block("cleaner", "git branch -d main", "git branch -D factory/t-1 main", "git branch -d factory/../x", "git branch -d",
		"git push origin --delete main", "git push origin :main", "git push origin :factory/t-1 :main",
		"git push origin HEAD:factory/t-1", "git push origin", "git push origin main :factory/t-1", "git push origin --delete",
		"git push origin -d factory/x y", "git -C . branch -d main", "git push origin :factory/x y")
	// the option value skip of `git push` (-o, --repo, ...) hides no target
	block("cleaner", "git push -o x origin --delete main", "git push --repo=r origin --delete main")
	// a trailing newline is accepted by Python's `$`
	ok("cleaner", "git branch -d 'factory/t-1\n'")
	// global options before the subcommand are skipped as in Python (two words for -C/-c/...)
	ok("cleaner", "git -C . branch -d factory/t-1")
	block("coder", "git -c x=y branch -d factory/t-1")
}

func TestGitPersistConfig(t *testing.T) {
	f := newGitFix(t)
	home := f.home
	// scope writes: refused (the --global / --system / abbreviations)
	for _, cmd := range []string{"git config --global user.name x", "git config --system k v", "git config --glo k v",
		"git config --sy k v", "git -c x=y config --global k v", "git config --global --unset k", "git config --global set k v",
		"git config --global --add k v"} {
		err := f.blocked(t, cmd, f.wt, "coder")
		if err != nil && !strings.Contains(Explain(err), "write") {
			t.Errorf("%q: stderr %q", cmd, Explain(err))
		}
	}
	// reads: allowed
	for _, cmd := range []string{"git config --global --get user.name", "git config --global --list", "git config --global -l",
		"git config --get core.editor", "git config --global get k", "git config --global list", "git config user.name",
		"git config --type bool core.x", "git config --global --type bool --get core.x", "git config --show-origin --list",
		"git -c user.name=x log", "git remote -v", "git remote show origin", "git remote get-url origin"} {
		if err := f.check(cmd, f.wt, "coder"); err != nil {
			t.Errorf("read %q: %v", cmd, err)
		}
	}
	// assigning GIT_CONFIG*/HOME/XDG_CONFIG_HOME in a command that writes config
	for _, cmd := range []string{"GIT_CONFIG=x git config k v", "HOME=/x git config k v", "XDG_CONFIG_HOME=/x git config k v",
		"export GIT_CONFIG_GLOBAL=" + f.scratch + "/c; git config k v"} {
		err := f.blocked(t, cmd, f.wt, "coder")
		if err != nil && !strings.Contains(Explain(err), "GIT_CONFIG*, HOME or XDG_CONFIG_HOME") {
			t.Errorf("%q: stderr %q", cmd, Explain(err))
		}
	}
	// repo-config writes: refused outside the scratchpad (T-0226), allowed with --file inside it
	for _, cmd := range []string{"git config user.name x", "git remote add o u", "git remote set-url o u",
		"git -c alias.x=config x k v", "git -c alias.x='config k v' x", "git config --file " + f.wt + "/c k v",
		"git -C " + f.home + " config k v", "git --git-dir=" + f.home + "/.git config k v"} {
		err := f.blocked(t, cmd, f.wt, "coder")
		var g *GitBlock
		if err != nil && !errors.As(err, &g) {
			t.Errorf("%q: err = %v, want a GitBlock", cmd, err)
		}
	}
	for _, cmd := range []string{"git config --file " + f.scratch + "/c k v", "git config -f " + f.scratch + "/c k v",
		"git config -f" + f.scratch + "/c k v", "git config --file=" + f.scratch + "/c k v"} {
		if err := f.check(cmd, f.wt, "coder"); err != nil {
			t.Errorf("%q: %v", cmd, err)
		}
	}
	// --file naming a protected file is refused as a persist write whatever the scope
	for _, cmd := range []string{"git config --file ~/.gitconfig k v", "git config -f ~/.config/git/config k v"} {
		f.blocked(t, cmd, f.wt, "coder")
	}
	// the alias forms: a `!` body is analysed as a script, a config alias recurses
	f.blocked(t, "git -c 'alias.x=!sudo id' x", f.wt, "coder")
	f.blocked(t, "git -c alias.x='config --global k v' x", f.wt, "coder")
	f.blocked(t, "git -c 'alias.x=config \"' x", f.wt, "coder")
	f.blocked(t, "git -calias.x='config --global k v' x", f.wt, "coder")
	_ = home
}

func TestGitPrivKeys(t *testing.T) {
	f := newGitFix(t)
	// git_priv: shell text in -c alias.x=!cmd, core.pager=cmd and `git config <key> <value>` (ported in T-0349)
	for _, cmd := range []string{"git -c core.pager='sudo less' log", "git -c 'alias.x=!sudo id' status",
		"git config core.editor 'sudo vi'", "git -c core.fsmonitor='docker ps' status", "git -c Core.Pager='sudo x' log"} {
		err := f.check(cmd, f.wt, "coder")
		if err == nil || !IsPrivBlock(err) {
			t.Errorf("%q: err = %v, want a PrivBlock", cmd, err)
		}
	}
	f.blocked(t, "git -c core.pager=herdr log", f.wt, "coder")
	if err := f.check("git config alias.x 'herdr pane list'", f.wt, "coder"); err == nil {
		t.Error("git config alias.x herdr allowed")
	}
	f.blocked(t, "git -c alias.x=herdr x", f.wt, "coder")
	f.blocked(t, "git -calias.x=herdr x", f.wt, "coder")
	f.allowed(t, "git log --grep herdr", f.wt, "coder")
}

func TestGitDelegations(t *testing.T) {
	f := newGitFix(t)
	t.Setenv("FACTORY_HOME", dbHome(t))
	// git_push_pin (a coder's push, after its own refusals) and the public-text rule (commit/tag -a/push/merge/
	// commit-tree/notes, -c alias.*) read the factory DB: with a DB file Python decides, only for what its rule looks at.
	const (
		deleg = "delegate"
		allow = "allow"
		block = "block"
	)
	for _, c := range []struct{ cmd, role, want string }{
		{"git push origin HEAD:factory/t-7", "coder", deleg}, {"git push origin x", "qa", deleg},
		{"git -C . send-pack x", "coder", block}, {"git push origin x", "coder", block},
		{"git commit -m x", "coder", deleg}, {"git tag v1", "qa", allow}, {"git tag -a v1 -m x", "qa", deleg},
		{"git merge x", "judge", deleg}, {"git notes add", "coder", deleg}, {"git commit-tree x", "coder", deleg},
		{"git -c alias.c=commit c", "coder", deleg}, {"git -C x commit -m y", "coder", deleg},
		{"git status", "coder", allow}, {"git add -A", "coder", allow}, {"git log commit", "coder", allow},
		{"git rebase main", "coder", allow}, {"git branch -a", "coder", allow}, {"git checkout x", "coder", allow},
		{"git --git-dir=x status", "coder", allow}, {"git stash", "pm", allow},
	} {
		err := f.check(c.cmd, f.wt, c.role)
		got := allow
		switch {
		case IsDelegate(err):
			got = deleg
		case err != nil:
			got = block
		}
		if got != c.want {
			t.Errorf("%q (%s): %s (%v), want %s", c.cmd, c.role, got, err, c.want)
		}
	}
	// HQ mode: only the PRIV pass and public text run; the rest of the git rules are not part of it
	if err := Check("git commit -m x", f.wt, "", true); !IsDelegate(err) {
		t.Errorf("hq git commit: %v, want a delegation", err)
	}
	for _, cmd := range []string{"git status", "git add -A", "git -C " + f.home + " reset --hard"} {
		if err := Check(cmd, f.wt, "", true); err != nil {
			t.Errorf("hq %q: %v", cmd, err)
		}
	}
	if err := Check("nohup git commit -m x", f.wt, "", true); !IsDelegate(err) {
		t.Errorf("hq nohup git commit: %v, want a delegation (wrapper peeled)", err)
	}
	// without a DB file the public-text rule is off and a push has no recorded worktree: Go decides
	t.Setenv("FACTORY_HOME", t.TempDir())
	if err := f.check("git commit -m x", f.wt, "coder"); err != nil {
		t.Errorf("commit without a DB file: %v", err)
	}
	err := f.check("git push origin HEAD:factory/t-7", f.wt, "coder")
	if err == nil || IsDelegate(err) || !strings.Contains(err.Error(), "cannot resolve the worktree recorded for t-7") {
		t.Errorf("push without a DB file: %v, want the unresolvable-worktree refusal", err)
	}
}

func TestGitHelpers(t *testing.T) {
	for _, c := range []struct {
		in   string
		want bool
	}{{"GIT_DIR=x", true}, {"a GIT_X=1", true}, {"xGIT_X=1", false}, {"GIT_X", false}, {"GIT_=", true}, {"éGIT_X=1", false}, {"GIT_é=1", true}} {
		if got := hasGitEnvAssign(c.in); got != c.want {
			t.Errorf("hasGitEnvAssign(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in   string
		want bool
	}{{"GIT_CONFIG=x", true}, {"GIT_CONFIG_GLOBAL=x", true}, {"HOME=x", true}, {"XDG_CONFIG_HOME=x", true}, {"MYHOME=x", false},
		{"_HOME=x", false}, {"GIT_CONFIG", false}, {"x GIT_CONFIG_X=1", true}, {"GIT_DIR=x", false}, {"HOME =x", false}} {
		if got := hasCfgEnvAssign(c.in); got != c.want {
			t.Errorf("hasCfgEnvAssign(%q) = %v, want %v", c.in, got, c.want)
		}
	}
	for _, c := range []struct {
		in string
		ok bool
	}{{"alias.x=y", true}, {"ALIAS.x=y", true}, {"alias.=y", false}, {"alias.x", false}, {"alias.x=", true}, {"alia.x=y", false}} {
		if _, ok := aliasAssign(c.in); ok != c.ok {
			t.Errorf("aliasAssign(%q) ok = %v, want %v", c.in, ok, c.ok)
		}
	}
	if !factoryBranch("factory/t-1") || factoryBranch("factory/") || factoryBranch("main") || factoryBranch("factory/a b") ||
		!factoryBranch("factory/t-1\n") || factoryBranch("factory/t-1\n\n") || factoryBranch("factory/é") {
		t.Error("factoryBranch")
	}
	if got := pyDirname("/a/b"); got != "/a" {
		t.Errorf("pyDirname(/a/b) = %q", got)
	}
	if pyDirname("/a") != "/" || pyDirname("/") != "/" {
		t.Error("pyDirname of the root")
	}
	if !dynamicDir("a*b") || !dynamicDir("$X") || !dynamicDir("a{b") || dynamicDir("a/b") {
		t.Error("dynamicDir")
	}
}

func TestGitTopSharedWalksForDotGit(t *testing.T) {
	f := newGitFix(t)
	for _, c := range []struct {
		path string
		want bool
	}{
		{f.home, true}, {filepath.Join(f.home, "roles"), true}, {filepath.Join(f.home, "roles", "nonexistent", "x"), true},
		{f.wt, false}, {filepath.Join(f.wt, "nonexistent"), false}, {f.tmp, false}, {f.link, true}, {"/", false},
	} {
		got, err := gitTopShared(c.path)
		if err != nil || got != c.want {
			t.Errorf("gitTopShared(%s) = %v, %v; want %v", c.path, got, err, c.want)
		}
	}
}

// port of worker-guard.py:PT_WRITE_HINT in HQ mode: the one HQ rule that blocks on parse trouble is the public-text
// rule, so an unparseable git/gh write is the Python hook's decision (delegate) and other parse trouble allows.
func TestHQParseTroubleGitWriteDelegates(t *testing.T) {
	f := newGitFix(t)
	t.Setenv("FACTORY_HOME", dbHome(t))
	for _, cmd := range []string{"git commit -m 'unterminated", "gh pr create --title 'x"} {
		if err := Check(cmd, f.wt, "", true); !IsDelegate(err) {
			t.Errorf("hq %q: %v, want a delegation", cmd, err)
		}
	}
	for _, cmd := range []string{"echo 'unterminated", "ls 'x"} {
		if err := Check(cmd, f.wt, "", true); err != nil {
			t.Errorf("hq %q: %v, want allow", cmd, err)
		}
	}
}
