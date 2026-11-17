package guard2

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_push_pin.py (T-0689, T-0719, T-0743, T-0755, T-0893): a coder may only
// `git push origin HEAD:factory/<own task id>`; the no-push roles never push and may run only read-only git.

const ppTask = "t-0689"
const ppOwn = "HEAD:factory/" + ppTask

type ppEnv struct {
	t     *testing.T
	tmp   string
	home  string
	wt    string
	task  string // lower-case task id
	agent string // default FACTORY_AGENT
}

// newPP is setUp: a factory home with a DB row, a real clone under worktrees/ with a local bare origin.
func newPP(t *testing.T, task, group, agent string) *ppEnv {
	t.Helper()
	tmp := realTemp(t)
	e := &ppEnv{t: t, tmp: tmp, home: filepath.Join(tmp, "factory"), task: task, agent: agent}
	e.wt = filepath.Join(e.home, "worktrees", group+"-"+strings.ToUpper(task))
	mkdirs(t, filepath.Join(e.home, ".git"), filepath.Join(e.home, "roles"))
	e.writeDB(e.wt, strings.ToUpper(task))
	origin := filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "-q", "--bare", origin)
	git(t, tmp, "init", "-q", "-b", "main", e.wt)
	git(t, e.wt, "commit", "-q", "--allow-empty", "-m", "i")
	git(t, e.wt, "remote", "add", "origin", origin)
	git(t, e.wt, "push", "-q", "origin", "main")
	git(t, e.wt, "fetch", "-q", "origin")
	git(t, e.wt, "checkout", "-q", "-b", "factory/"+task)
	return e
}

// writeDB replaces the DB (and the guard export built from it) with one tasks row.
func (e *ppEnv) writeDB(worktree, task string) {
	e.t.Helper()
	for _, p := range []string{filepath.Join(e.home, "db", "factory.db"), filepath.Join(e.home, "run", "guard-export.json")} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			e.t.Fatal(err)
		}
	}
	guardDB(e.t, e.home, map[string]string{task: worktree})
}

// run: agent "-" means FACTORY_AGENT unset; "" means the default; cwd "" means the worktree.
func (e *ppEnv) run(cmd, role, agent, cwd string) res {
	e.t.Helper()
	if role == "" {
		role = "coder"
	}
	if agent == "" {
		agent = e.agent
	}
	if cwd == "" {
		cwd = e.wt
	}
	set := map[string]string{"HOME": e.tmp, "WORKER_GUARD_LIVE_HOME": e.home, "FACTORY_HOME": e.home}
	if agent != "-" {
		set["FACTORY_AGENT"] = agent
	}
	return bash(e.t, cmd, hookOpts{args: roleArgs(role), cwd: cwd, drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT"}, set: set})
}

// pinned is true when the pin itself refused (its message names the exact allowed push).
func (e *ppEnv) pinned(cmd, agent string) bool {
	r := e.run(cmd, "", agent, "")
	if r.code != 2 {
		return false
	}
	for _, w := range []string{"coders push exactly", "git push:", "before push", "environment overrides", "by another name", "whole command"} {
		if strings.Contains(r.stderr, w) {
			return true
		}
	}
	return false
}

func (e *ppEnv) mustExit(what string, r res, code int) {
	e.t.Helper()
	wantExit(e.t, what, r, code)
}

func TestPushPinRefusedShapes(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	own := ppOwn
	for _, cmd := range []string{
		"git push", "git push origin", "git push origin HEAD", "git push -u origin HEAD",
		"git push origin " + own + " HEAD:main", "git push origin '" + own + " HEAD:main'",
		"git push origin '" + own + "\tHEAD:main'", "git push origin '" + own + "\nHEAD:main'",
		"git push origin " + own + " HEAD:factory/t-0001",
		"git push origin HEAD:factory/t-0001", "git push origin HEAD:main", "git push origin HEAD:refs/heads/main",
		"git push --force origin " + own, "git push -f origin " + own, "git push --force-with-lease origin " + own,
		"git push origin +" + own, "git push origin " + own + " --force", "git push origin " + own + " -f",
		"git push --all origin", "git push --mirror origin", "git push --tags origin", "git push --tags origin " + own,
		"git push origin --delete factory/" + ppTask, "git push --delete origin factory/" + ppTask,
		"git push origin :factory/" + ppTask,
		"git push other " + own, "git push https://example.com/x.git " + own, "git push git@example.com:x/y.git " + own,
		"git push --repo=https://example.com/x.git " + own,
		"git -c push.default=matching push origin " + own, "git -c remote.origin.pushurl=/x push origin " + own,
		"git --git-dir=/x push origin " + own, "GIT_CONFIG_COUNT=1 git push origin " + own, "GIT_DIR=/x git push origin " + own,
		"GIT_SSH_COMMAND=true git push origin " + own, "GIT_EXEC_PATH=/x git push origin " + own,
		`export GIT_CONFIG_PARAMETERS="'remote.origin.url=/x'"; git push origin ` + own,
		"export GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=remote.origin.url GIT_CONFIG_VALUE_0=/x; git push origin " + own,
		"export GIT_DIR=/x; git push origin " + own, "export GIT_WORK_TREE=/x; git push origin " + own,
		"GIT_DIR=/x; git push origin " + own,
		"printf -v GIT_DIR /x; export GIT_DIR; git push origin " + own,
		"read GIT_DIR </dev/null; export GIT_DIR; git push origin " + own,
		"for GIT_DIR in /x; do export GIT_DIR; git push origin " + own + "; done",
		"mapfile -t GIT_DIR < /dev/null; export GIT_DIR; git push origin " + own,
		"GIT_DIR+=/x; export GIT_DIR; git push origin " + own,
		"x=1; export x; git push origin " + own, "declare -x y=1; git push origin " + own,
		"git push origin " + own + " # GIT_DIR",
		"git subtree push --prefix=x origin main",
		"git send-pack /x HEAD:main", "git http-push http://x/ HEAD:main",
		"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0001",
		"git push -o x origin " + own,
		`set -a; n=GI; printf -v "${n}T_CONFIG_PARAMETERS" '%s' "'remote.origin.pushurl=/x'"; git push origin ` + own,
		"f(){ local -x ${1}IT_CONFIG_PARAMETERS=x; git push origin " + own + "; }; f G",
		"BASH_ENV=/tmp/benv bash -c 'git push origin " + own + "'",
		"HOME=/tmp/h git push origin " + own,
		`set -a; a=GI; b=T_DIR; printf -v "$a$b" /x; git push origin ` + own,
		"true && git push origin " + own, "git push origin " + own + "; true", "git push origin '" + own + "'",
		"git -C $HOME push origin " + own,
	} {
		if !e.pinned(cmd, "") {
			r := e.run(cmd, "", "", "")
			t.Errorf("pin did not refuse %q: exit %d, stderr %q", cmd, r.code, r.stderr)
		}
	}
}

func TestPushPinUnknownTaskIDRefuses(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, agent := range []string{"-", "coder", "qa-t-0689", "coder-t-0689x"} {
		if !e.pinned("git push origin "+ppOwn, agent) {
			t.Errorf("agent %q: the pin did not refuse", agent)
		}
	}
}

func TestPushPinOtherRolesNotPinned(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	r := e.run("git push origin HEAD:factory/t-0001", "qa", "", "")
	if strings.Contains(r.stderr, "coders push exactly") {
		t.Errorf("qa got the coder pin message: %q", r.stderr)
	}
}

func TestPushPinAllowedFormIsAllowed(t *testing.T) {
	// The exact push exits 0 (real clone + local bare origin) and the pin names no refusal.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, cmd := range []string{"git push origin " + ppOwn, "git -C " + e.wt + " push origin " + ppOwn, "git -C . push origin " + ppOwn} {
		e.mustExit(cmd, e.run(cmd, "", "", ""), 0)
	}
}

func TestPushPinDashCMustBeOwnWorktree(t *testing.T) {
	// T-0719: -C into a scratch clone (another origin) is refused, also via a symlink to it; a symlink to the own one passes.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	scratch := filepath.Join(e.tmp, "scratch")
	git(t, e.tmp, "init", "-q", "-b", "main", scratch)
	link := filepath.Join(e.tmp, "link")
	if err := os.Symlink(scratch, link); err != nil {
		t.Fatal(err)
	}
	for _, tgt := range []string{scratch, link, "..", filepath.Join(e.home, "worktrees", "G-018-T-0001")} {
		cmd := "git -C " + tgt + " push origin " + ppOwn
		r := e.run(cmd, "", "", "")
		e.mustExit(cmd, r, 2)
		if !strings.Contains(r.stderr, "own worktree") {
			t.Errorf("%s: stderr lacks 'own worktree': %q", cmd, r.stderr)
		}
	}
	// cwd in a scratch clone: even `-C .` is refused; cwd in a worktree subdir: -C <worktree root> passes
	for _, tgt := range []string{".", scratch} {
		cmd := "git -C " + tgt + " push origin " + ppOwn
		e.mustExit(cmd+" (cwd scratch)", e.run(cmd, "", "", scratch), 2)
	}
	sub := filepath.Join(e.wt, "sub")
	mkdirs(t, sub)
	e.mustExit("-C worktree from subdir", e.run("git -C "+e.wt+" push origin "+ppOwn, "", "", sub), 0)
	linkOwn := filepath.Join(e.tmp, "ownlink")
	if err := os.Symlink(e.wt, linkOwn); err != nil {
		t.Fatal(err)
	}
	e.mustExit("-C symlink to own", e.run("git -C "+linkOwn+" push origin "+ppOwn, "", "", ""), 0)
}

func TestPushPinPaneInAnotherWorktreeRefused(t *testing.T) {
	// T-0743: a pane cd'd into another worktree cannot push, with or without -C, even naming its own worktree.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	other := filepath.Join(e.home, "worktrees", "G-018-T-0001")
	git(t, e.tmp, "init", "-q", "-b", "main", other)
	for _, cmd := range []string{"git push origin " + ppOwn, "git -C . push origin " + ppOwn, "git -C " + other + " push origin " + ppOwn,
		"git -C " + e.wt + " push origin " + ppOwn} {
		r := e.run(cmd, "", "", other)
		e.mustExit(cmd, r, 2)
		if !strings.Contains(r.stderr, "own worktree") {
			t.Errorf("%s: stderr lacks 'own worktree': %q", cmd, r.stderr)
		}
	}
}

func TestPushPinUnresolvableWorktreeRefuses(t *testing.T) {
	// T-0743: no DB, no task row or an empty worktree column refuses (fail closed).
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	push := "git push origin " + ppOwn
	if err := os.Remove(filepath.Join(e.home, "db", "factory.db")); err != nil {
		t.Fatal(err)
	}
	r := e.run(push, "", "", "")
	e.mustExit("no DB", r, 2)
	if !strings.Contains(r.stderr, "cannot resolve the worktree") {
		t.Errorf("no DB: stderr lacks 'cannot resolve the worktree': %q", r.stderr)
	}
	e.writeDB("", "T-0689")
	e.mustExit("empty worktree column", e.run(push, "", "", ""), 2)
	e.writeDB(e.wt, "T-0001") // a DB without this task's row
	e.mustExit("no row", e.run(push, "", "", ""), 2)
}

func TestPushPinDBWorktreeNotCwdDecides(t *testing.T) {
	// T-0743: the DB path is the pin; a worktree-looking cwd that is not the recorded one is refused, the recorded one passes.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	other := filepath.Join(e.home, "worktrees", "G-018-T-0002")
	git(t, e.tmp, "init", "-q", "-b", "main", other)
	e.writeDB(other, "T-0689")
	push := "git push origin " + ppOwn
	e.mustExit("cwd wt, DB other", e.run(push, "", "", ""), 2)
	e.mustExit("cwd other, DB other", e.run(push, "", "", other), 0)
}

var ppNoPushRoles = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"}
var ppRoles = append(append([]string{}, ppNoPushRoles...), "researcher")

func TestPushPinNonCoderRolesNeverPush(t *testing.T) {
	// T-0755: every push shape is refused, even --dry-run.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	shapes := []string{
		"git push", "git push origin", "git push origin " + ppOwn, "git push origin HEAD:main", "git push origin +HEAD:main",
		"git push --dry-run origin HEAD:main", "git push -n origin main", "git push --all origin", "git push --mirror origin",
		"git push --tags origin", "git push --force origin main", "git -C " + e.wt + " push origin " + ppOwn,
		"git -c remote.origin.url=https://example.com/x.git push origin main", "git -c push.default=current push",
		"git push https://example.com/x.git main", "git send-pack origin main", "git subtree push --prefix=a origin main",
		"git -c alias.p=push p origin main",
	}
	for _, role := range ppNoPushRoles {
		for _, cmd := range shapes {
			r := e.run(cmd, role, role+"-t-0689", "")
			e.mustExit(role+": "+cmd, r, 2)
			if !strings.Contains(r.stderr, "may not run git push") && !strings.Contains(r.stderr, "alias") {
				t.Errorf("%s: %s: stderr names neither push nor alias: %q", role, cmd, r.stderr)
			}
		}
	}
}

func TestPushPinNonCoderGitOptionAndEnvBypassesRefused(t *testing.T) {
	// T-0755 retry: value-taking git global options, unknown options and env/--config-env aliases hide no push.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	shapes := []string{
		"git --attr-source HEAD push origin main", "git --attr-source HEAD push --dry-run origin main",
		"git --attr-source=HEAD push origin main", "git --namespace ns push origin main",
		"git --git-dir /x/.git push origin main", "git --work-tree /x push origin main",
		"git --super-prefix p/ push origin main", "git --no-pager push origin main", "git -p push origin main",
		"git --future-option push origin main", "git --future-option value push origin main",
		"git --config-env=alias.p=PUSHVAR p origin main", "git --config-env alias.p=PUSHVAR p origin main",
		"git -c alias.s=send-pack s origin main", "git -c alias.s=subtree s push origin",
		"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
		`GIT_CONFIG_PARAMETERS="'alias.p=push'" git p origin main`,
		"env GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main",
		"export GIT_CONFIG_COUNT=1; git p origin main", "export GIT_CONFIG_KEY_0=alias.p",
		"GIT_CONFIG_VALUE_0=push git status", "GIT_CONFIG_PARAMETERS=x",
	}
	for _, role := range ppNoPushRoles {
		for _, cmd := range shapes {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
	}
}

func TestPushPinNonCoderGitOptionFormsStillAllowed(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, cmd := range []string{"git --no-pager log -1", "git -P log -1", "git --no-optional-locks status", "git --version"} {
		e.mustExit(cmd, e.run(cmd, "reviewer", "reviewer-t-0689", ""), 0)
	}
}

func TestPushPinNonCoderNonPushGitAllowed(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, cmd := range []string{"git status", "git log -1", "git -C " + e.wt + " diff", "git fetch origin"} {
		e.mustExit(cmd, e.run(cmd, "qa", "qa-t-0689", ""), 0)
	}
}

// T-0755 attempt 3: the allow-list. Each of these exited 0 for a read-only role at f792b8f93 (the push deny-list).
var ppAllowlistRefused = []string{
	"p=push; git $p origin main", "p=push; git ${p} x", `p=push; git "$p" origin main`, "git `echo push` origin main",
	"p=push; git $p --dry-run origin main",
	"GIT_CONFIG_GLOBAL=f git p origin main", "GIT_CONFIG_SYSTEM=f git p origin main", "GIT_CONFIG=f git p origin main",
	"HOME=d git p", "env HOME=d git p", "export HOME=d; git p", "export GIT_CONFIG_GLOBAL=f; git p",
	"unset HOME; git p", "HOME=d; git p", "PATH=/x:$PATH git status",
	"git -c include.path=f p", "git -c includeIf.x.path=f p", "git -c core.pager=cat log", "git p", "git p origin main",
	"git fetch --upload-pack='x' .", "git fetch --upload-pack=x origin", "git fetch -u x origin",
	"git fetch 'ext::sh -c x'", "git fetch origin 'ext::sh -c x'", "git fetch https://example.com/x.git",
	"git fetch /tmp/other", "git ls-remote --upload-pack=x origin", "git ls-remote git@example.com:x/y.git",
	"git fetch --server-option=x origin", "git fetch $r", "git ls-remote $r",
	"o=--output=y; git diff $o", "o=--ext-diff; git log -1 $o", "git log -1 --format=x $(echo --output=y)",
	"git fetch origin main:refs/heads/x", "git fetch origin +main:main",
	"git grep -O x", "git grep -nO x", "git grep --open-files-in-pager=x y", "git grep --open-f=x y", "git grep $pat",
	"git log --ext-diff", "git diff --ext-diff", "git show --output=f", "git log --output f", "git diff --outp=f",
	"git -p log", "git --paginate log",
	"echo '[alias] p=push' >> .git/config", "tee -a .git/config < /dev/null", "cp /dev/null .git/config",
	"sed -i s/a/b/ .git/config", "echo x > .git/hooks/pre-push", "echo x > .git/info/attributes",
	"echo '[alias]' >> ~/.gitconfig", "echo x > ~/.config/git/config",
	"git config --file .git/config alias.p push",
	"printf push | xargs git", "echo push | xargs -n1 git", "find . -maxdepth 0 -exec git push \\;",
	"find . -maxdepth 0 -execdir git p \\;", "find . -ok git p \\;",
	"git-push origin main", "/usr/lib/git-core/git-push", "/usr/lib/git-core/git push origin main", "./git push",
	"$G push", "\\git p", "git rebase --exec 'git push' HEAD~1", "git rebase -x p HEAD~1", "git bisect run p",
	"git submodule foreach git push", "git filter-branch -f", "git difftool", "git archive --remote=x HEAD",
	"git push origin main", "git stash", "git commit -m x", "git checkout -b x", "git reset --hard", "git add .",
	"git branch -D x", "git branch new", "git remote add x y", "git remote set-url origin y", "git worktree add ../x",
	"git config alias.p push", "git config --unset alias.p", "git config --list --edit", "git config --global alias.p push",
	"git send-pack /x HEAD:main", "git subtree push --prefix=a origin main", "git daemon", "git var GIT_PAGER",
	"git remote update",
}

var ppAllowlistAllowed = []string{
	"git log --oneline -5", "git -C {wt} diff origin/main...HEAD", "git show origin/x:f", "git fetch origin",
	"git fetch origin main", "git status", "git branch -a", "git config --get user.name", "git blame f",
	"git grep -n x", "git merge-tree --write-tree origin/main HEAD", "git rev-parse HEAD", "git ls-remote origin",
	"git fetch origin refs/heads/main:refs/remotes/origin/main", "git fetch --prune origin", "git ls-remote --heads origin",
	"git diff --stat HEAD~1", "git log --no-ext-diff -1", "git remote -v", "git remote get-url origin", "git remote", "git remote show origin",
	"git worktree list", "git config --list", "git config --get-regexp remote", "git branch --list 'f*'",
	"git cat-file -p HEAD", "git show-ref", "git describe --tags --always", "git --version",
}

var ppRemoteWrites = []string{
	"set-url origin https://example.com/evil.git", "add x https://example.com/evil.git", "rename origin y",
	"remove origin", "rm origin", "set-head origin -a", "set-branches origin main", "prune origin", "update",
	"set-url --push origin https://example.com/evil.git", "bogus", "show", "get-url", "show origin extra",
	"get-url origin extra",
}

var ppRemoteReads = []string{
	"", "-v", "--verbose", "get-url origin", "get-url --push origin", "get-url --all origin", "show origin",
	"show -n origin", "-v show origin", "--verbose show -n origin",
}

func TestPushPinNonCoderGitRemoteWriteFormsRefused(t *testing.T) {
	// T-0755 attempt 4: every word after `remote` is checked; -v in front must not hide a write form.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, role := range ppRoles {
		for _, form := range ppRemoteWrites {
			for _, pre := range []string{"", "-v ", "--verbose "} {
				cmd := "git remote " + pre + form
				e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
			}
		}
	}
}

func TestPushPinNonCoderGitRemoteReadFormsAllowed(t *testing.T) {
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, role := range ppRoles {
		for _, form := range ppRemoteReads {
			cmd := strings.TrimSpace("git remote " + form)
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 0)
		}
	}
}

func TestPushPinNonCoderGitAllowListRefuses(t *testing.T) {
	// T-0755 acceptance 1: fail-closed allow-list (dynamic subcommand, config-file aliases, fetch helpers, pager,
	// config writes, xargs/find launchers, git-* helpers, unknown builtins).
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, role := range ppRoles {
		for _, cmd := range ppAllowlistRefused {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
	}
}

func TestPushPinNonCoderGitAllowListAllows(t *testing.T) {
	// T-0755 acceptance 2: the read-only forms stay allowed.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, role := range ppRoles {
		for _, cmd := range ppAllowlistAllowed {
			e.mustExit(role+": "+cmd, e.run(strings.ReplaceAll(cmd, "{wt}", e.wt), role, role+"-t-0689", ""), 0)
		}
	}
}

func TestPushPinNonCoderScriptFilesAreInspectedOrRefused(t *testing.T) {
	// T-0755 point 7: a script file run by a no-push role is read as sh (its body obeys the allow-list); one for
	// another interpreter is refused.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	script := func(name, body string) string {
		p := filepath.Join(e.tmp, name)
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, 0o755); err != nil {
			t.Fatal(err)
		}
		return p
	}
	push := script("push.sh", "git push origin main\n")
	py := script("py.py", "#!/usr/bin/env python3\nprint(1)\n")
	ok := script("ok.sh", "git status\n")
	for _, role := range ppRoles {
		for _, cmd := range []string{push, "bash " + push, "source " + push, py} {
			e.mustExit(role+": "+cmd, e.run(cmd, role, role+"-t-0689", ""), 2)
		}
		e.mustExit(role+": "+ok, e.run(ok, role, role+"-t-0689", ""), 0)
	}
}

func TestPushPinCoderCleanerJudgeNotUnderAllowList(t *testing.T) {
	// The allow-list is for the no-push roles only: coder, cleaner and judge never get its message.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	for _, role := range []string{"coder", "cleaner", "judge"} {
		r := e.run("git p origin main", role, role+"-t-0689", "")
		if strings.Contains(r.stderr, "read-only allow-list") {
			t.Errorf("%s got the allow-list message: %q", role, r.stderr)
		}
	}
}

func TestPushPinJudgeHQAndCleanerUnchanged(t *testing.T) {
	// Judge and HQ are not in the no-push set (no refusal from this rule); the cleaner still gets its own messages.
	e := newPP(t, ppTask, "G-018", "coder-"+ppTask)
	p := e.run("git push origin HEAD:main", "judge", "judge-1", "")
	if strings.Contains(p.stderr, "may not run git push") {
		t.Errorf("judge: %q", p.stderr)
	}
	h := bash(t, "git push origin HEAD:main", hookOpts{args: []string{"--hq"}, cwd: e.wt,
		set: map[string]string{"HOME": e.tmp, "WORKER_GUARD_LIVE_HOME": e.home, "FACTORY_HOME": e.home, "FACTORY_AGENT": "lead-g-1"}})
	if strings.Contains(h.stderr, "may not run git push") {
		t.Errorf("hq: %q", h.stderr)
	}
	c := e.run("git push origin HEAD:main", "cleaner", "cleaner-1", "")
	e.mustExit("cleaner push main", c, 2)
	if !strings.Contains(c.stderr, "cleaner may only delete") {
		t.Errorf("cleaner stderr lacks 'cleaner may only delete': %q", c.stderr)
	}
	c = e.run("git push origin --delete factory/t-0001", "cleaner", "cleaner-1", "")
	if strings.Contains(c.stderr, "may not run git push") {
		t.Errorf("cleaner delete: %q", c.stderr)
	}
}

// T-0893: FACTORY_AGENT may end with -aN; the push ref still uses the task id before that suffix.
const apTask = "t-0817"
const apDefaultAgent = "cursor-coder-t-0817-a2"

func TestPushPinAttemptSuffixAllowed(t *testing.T) {
	e := newPP(t, apTask, "G-027", apDefaultAgent)
	push := "git push origin HEAD:factory/t-0817"
	for _, agent := range []string{"cursor-coder-t-0817-a2", "cursor-coder-t-0817", "coder-t-0817-a2",
		"claude-coder-t-0817-a2", "cursor-coder-t-0817-a10"} {
		e.mustExit(agent, e.run(push, "", agent, ""), 0)
	}
	e.mustExit("-C own", e.run("git -C "+e.wt+" push origin HEAD:factory/t-0817", "", "cursor-coder-t-0817-a2", ""), 0)
	d, err := sql.Open("sqlite", filepath.Join(e.home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var id, wt string
	if err := d.QueryRow("SELECT id, worktree FROM tasks WHERE id = ?", "T-0817").Scan(&id, &wt); err != nil {
		t.Fatalf("task row: %v", err)
	}
	if id != "T-0817" || wt != e.wt {
		t.Errorf("row = (%q, %q), want (T-0817, %q)", id, wt, e.wt)
	}
}

func TestPushPinAttemptSuffixRefused(t *testing.T) {
	e := newPP(t, apTask, "G-027", apDefaultAgent)
	push := "git push origin HEAD:factory/t-0817"
	for _, agent := range []string{"cursor-coder-t-0817-b2", "cursor-coder-t-0817-a", "cursor-coder-t-0817-a2x",
		"cursor-coder-t-0817-a2-a3", "cursor-coder-t-0817-A2", "cursor-coder-t-0817-attempt2",
		"evil-coder-t-0817-a2", "cursor-claude-coder-t-0817-a2",
		"cursor-coder-t-a2", "coder-t-a2", "claude-coder-t-a9", "cursor-coder-t--a2"} {
		r := e.run(push, "", agent, "")
		e.mustExit(agent, r, 2)
		if !strings.Contains(r.stderr, "cannot tell this pane's task id") {
			t.Errorf("%s: stderr lacks the task-id message: %q", agent, r.stderr)
		}
	}
}

func TestPushPinAttemptSuffixPushUnchanged(t *testing.T) {
	e := newPP(t, apTask, "G-027", apDefaultAgent)
	for _, cmd := range []string{"git push other HEAD:factory/t-0817", "git push origin HEAD:factory/t-0817-a2",
		"git push --force origin HEAD:factory/t-0817", "git push -u origin HEAD:factory/t-0817",
		"FACTORY_AGENT=coder-t-0001 git push origin HEAD:factory/t-0817", "GIT_DIR=/x git push origin HEAD:factory/t-0817",
		"true && git push origin HEAD:factory/t-0817", "git push origin HEAD:factory/t-0817; true"} {
		if !e.pinned(cmd, "") {
			t.Errorf("pin did not refuse %q", cmd)
		}
	}
	r := e.run("git push origin HEAD:factory/t-0817-a2", "", "", "")
	e.mustExit("-a2 ref", r, 2)
	if !strings.Contains(r.stderr, "HEAD:factory/t-0817") {
		t.Errorf("stderr lacks the allowed ref: %q", r.stderr)
	}
}
