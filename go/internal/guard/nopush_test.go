package guard

import (
	"strings"
	"testing"
)

// T-0755 (tests/test_worker_guard_push_pin.py: test_non_coder_*): the read-only roles run only allow-listed git. The
// cases are the Python lists verbatim (plus the non-ASCII whitespace forms); the expected exit is what the Python hook
// returned for each, role qa and evaluator alike.

var noPushRolesList = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator"}

var noPushCases = []struct {
	cmd  string
	exit int
}{
	{"p=push; git $p origin main", 2},
	{"p=push; git ${p} x", 2},
	{"p=push; git \"$p\" origin main", 2},
	{"git `echo push` origin main", 2},
	{"p=push; git $p --dry-run origin main", 2},
	{"GIT_CONFIG_GLOBAL=f git p origin main", 2},
	{"GIT_CONFIG_SYSTEM=f git p origin main", 2},
	{"GIT_CONFIG=f git p origin main", 2},
	{"HOME=d git p", 2},
	{"env HOME=d git p", 2},
	{"export HOME=d; git p", 2},
	{"export GIT_CONFIG_GLOBAL=f; git p", 2},
	{"unset HOME; git p", 2},
	{"HOME=d; git p", 2},
	{"PATH=/x:$PATH git status", 2},
	{"git -c include.path=f p", 2},
	{"git -c includeIf.x.path=f p", 2},
	{"git -c core.pager=cat log", 2},
	{"git p", 2},
	{"git p origin main", 2},
	{"git fetch --upload-pack='x' .", 2},
	{"git fetch --upload-pack=x origin", 2},
	{"git fetch -u x origin", 2},
	{"git fetch 'ext::sh -c x'", 2},
	{"git fetch origin 'ext::sh -c x'", 2},
	{"git fetch https://example.com/x.git", 2},
	{"git fetch /tmp/other", 2},
	{"git ls-remote --upload-pack=x origin", 2},
	{"git ls-remote git@example.com:x/y.git", 2},
	{"git fetch --server-option=x origin", 2},
	{"git fetch $r", 2},
	{"git ls-remote $r", 2},
	{"o=--output=y; git diff $o", 2},
	{"o=--ext-diff; git log -1 $o", 2},
	{"git log -1 --format=x $(echo --output=y)", 2},
	{"git fetch origin main:refs/heads/x", 2},
	{"git fetch origin +main:main", 2},
	{"git grep -O x", 2},
	{"git grep -nO x", 2},
	{"git grep --open-files-in-pager=x y", 2},
	{"git grep --open-f=x y", 2},
	{"git grep $pat", 2},
	{"git log --ext-diff", 2},
	{"git diff --ext-diff", 2},
	{"git show --output=f", 2},
	{"git log --output f", 2},
	{"git diff --outp=f", 2},
	{"git -p log", 2},
	{"git --paginate log", 2},
	{"echo '[alias] p=push' >> .git/config", 2},
	{"tee -a .git/config < /dev/null", 2},
	{"cp /dev/null .git/config", 2},
	{"sed -i s/a/b/ .git/config", 2},
	{"echo x > .git/hooks/pre-push", 2},
	{"echo x > .git/info/attributes", 2},
	{"echo '[alias]' >> ~/.gitconfig", 2},
	{"echo x > ~/.config/git/config", 2},
	{"git config --file .git/config alias.p push", 2},
	{"printf push | xargs git", 2},
	{"echo push | xargs -n1 git", 2},
	{"find . -maxdepth 0 -exec git push \\;", 2},
	{"find . -maxdepth 0 -execdir git p \\;", 2},
	{"find . -ok git p \\;", 2},
	{"git-push origin main", 2},
	{"/usr/lib/git-core/git-push", 2},
	{"/usr/lib/git-core/git push origin main", 2},
	{"./git push", 2},
	{"$G push", 2},
	{"\\git p", 2},
	{"git rebase --exec 'git push' HEAD~1", 2},
	{"git rebase -x p HEAD~1", 2},
	{"git bisect run p", 2},
	{"git submodule foreach git push", 2},
	{"git filter-branch -f", 2},
	{"git difftool", 2},
	{"git archive --remote=x HEAD", 2},
	{"git push origin main", 2},
	{"git stash", 2},
	{"git commit -m x", 2},
	{"git checkout -b x", 2},
	{"git reset --hard", 2},
	{"git add .", 2},
	{"git branch -D x", 2},
	{"git branch new", 2},
	{"git remote add x y", 2},
	{"git remote set-url origin y", 2},
	{"git worktree add ../x", 2},
	{"git config alias.p push", 2},
	{"git config --unset alias.p", 2},
	{"git config --list --edit", 2},
	{"git config --global alias.p push", 2},
	{"git send-pack /x HEAD:main", 2},
	{"git subtree push --prefix=a origin main", 2},
	{"git daemon", 2},
	{"git var GIT_PAGER", 2},
	{"git remote update", 2},
	{"git --attr-source HEAD push origin main", 2},
	{"git --attr-source=HEAD push origin main", 2},
	{"git --namespace ns push origin main", 2},
	{"git --git-dir /x/.git push origin main", 2},
	{"git --work-tree /x push origin main", 2},
	{"git -p push origin main", 2},
	{"git --future-option push origin main", 2},
	{"git --config-env=alias.p=PUSHVAR p origin main", 2},
	{"git -c alias.s=send-pack s origin main", 2},
	{"GIT_CONFIG_COUNT=1 GIT_CONFIG_KEY_0=alias.p GIT_CONFIG_VALUE_0=push git p origin main", 2},
	{"GIT_CONFIG_PARAMETERS=\"'alias.p=push'\" git p origin main", 2},
	{"export GIT_CONFIG_COUNT=1; git p origin main", 2},
	{"export GIT_CONFIG_KEY_0=alias.p", 2},
	{"GIT_CONFIG_PARAMETERS=x", 2},
	{"git push", 2},
	{"git push origin HEAD:main", 2},
	{"git push --dry-run origin HEAD:main", 2},
	{"git -c alias.p=push p origin main", 2},
	{"git send-pack origin main", 2},
	{"echo push | xargs\u00a0git", 0},
	{"echo p | xargs 'a\u2003git p'", 2},
	{"echo x | xargs 'a\u3000git p'", 2},
	{"find . -exec 'a\u00a0git p' \\;", 2},
	{"git\u00a0push origin main", 0},
	{"git remote -v set-url origin y", 2},
	{"git remote --verbose add x y", 2},
	{"git remote show origin extra", 2},
	{"cat y > .git/info/exclude", 2},
	{"git remote set-url origin https://example.com/evil.git", 2},
	{"git remote -v set-url origin https://example.com/evil.git", 2},
	{"git remote --verbose set-url origin https://example.com/evil.git", 2},
	{"git remote add x https://example.com/evil.git", 2},
	{"git remote -v add x https://example.com/evil.git", 2},
	{"git remote --verbose add x https://example.com/evil.git", 2},
	{"git remote rename origin y", 2},
	{"git remote -v rename origin y", 2},
	{"git remote --verbose rename origin y", 2},
	{"git remote remove origin", 2},
	{"git remote -v remove origin", 2},
	{"git remote --verbose remove origin", 2},
	{"git remote rm origin", 2},
	{"git remote -v rm origin", 2},
	{"git remote --verbose rm origin", 2},
	{"git remote set-head origin -a", 2},
	{"git remote -v set-head origin -a", 2},
	{"git remote --verbose set-head origin -a", 2},
	{"git remote set-branches origin main", 2},
	{"git remote -v set-branches origin main", 2},
	{"git remote --verbose set-branches origin main", 2},
	{"git remote prune origin", 2},
	{"git remote -v prune origin", 2},
	{"git remote --verbose prune origin", 2},
	{"git remote -v update", 2},
	{"git remote --verbose update", 2},
	{"git remote set-url --push origin https://example.com/evil.git", 2},
	{"git remote -v set-url --push origin https://example.com/evil.git", 2},
	{"git remote --verbose set-url --push origin https://example.com/evil.git", 2},
	{"git remote bogus", 2},
	{"git remote -v bogus", 2},
	{"git remote --verbose bogus", 2},
	{"git remote show", 2},
	{"git remote -v show", 2},
	{"git remote --verbose show", 2},
	{"git remote get-url", 2},
	{"git remote -v get-url", 2},
	{"git remote --verbose get-url", 2},
	{"git remote -v show origin extra", 2},
	{"git remote --verbose show origin extra", 2},
	{"git remote get-url origin extra", 2},
	{"git remote -v get-url origin extra", 2},
	{"git remote --verbose get-url origin extra", 2},
	{"git log --oneline -5", 0},
	{"git -C @WT@ diff origin/main...HEAD", 0},
	{"git show origin/x:f", 0},
	{"git fetch origin", 0},
	{"git fetch origin main", 0},
	{"git status", 0},
	{"git branch -a", 0},
	{"git config --get user.name", 0},
	{"git blame f", 0},
	{"git grep -n x", 0},
	{"git merge-tree --write-tree origin/main HEAD", 0},
	{"git rev-parse HEAD", 0},
	{"git ls-remote origin", 0},
	{"git fetch origin refs/heads/main:refs/remotes/origin/main", 0},
	{"git fetch --prune origin", 0},
	{"git ls-remote --heads origin", 0},
	{"git diff --stat HEAD~1", 0},
	{"git log --no-ext-diff -1", 0},
	{"git remote -v", 0},
	{"git remote get-url origin", 0},
	{"git remote", 0},
	{"git remote show origin", 0},
	{"git worktree list", 0},
	{"git config --list", 0},
	{"git config --get-regexp remote", 0},
	{"git branch --list 'f*'", 0},
	{"git cat-file -p HEAD", 0},
	{"git show-ref", 0},
	{"git describe --tags --always", 0},
	{"git --version", 0},
	{"git --no-pager log -1", 0},
	{"git -P log -1", 0},
	{"git --no-optional-locks status", 0},
	{"git remote -v show -n origin", 0},
	{"git remote --verbose get-url --push origin", 2},
	{"echo hi", 0},
	{"git status\u00a0", 2},
	{"git remote --verbose", 0},
	{"git remote get-url --push origin", 0},
	{"git remote get-url --all origin", 0},
	{"git remote show -n origin", 0},
	{"git remote -v show origin", 0},
	{"git remote --verbose show -n origin", 0},
}

func TestNoPushRolesAllowList(t *testing.T) {
	f := newGitFix(t)
	for _, role := range noPushRolesList {
		for _, c := range noPushCases {
			cmd := strings.ReplaceAll(c.cmd, "@WT@", f.scratch)
			err := f.check(cmd, f.tmp, role)
			if got := exitOf(err); got != c.exit {
				t.Errorf("role %s: %q: exit %d, want %d (%v)", role, cmd, got, c.exit, err)
			}
		}
	}
}

// Other roles and HQ are not under the allow-list (the Python test_coder_cleaner_judge_not_under_allow_list).
func TestNoPushRulesSkipOtherRoles(t *testing.T) {
	f := newGitFix(t)
	for _, role := range []string{"coder", "cleaner", "judge"} {
		err := f.check("git p origin main", f.scratch, role)
		if err != nil && strings.Contains(err.Error(), "read-only allow-list") {
			t.Errorf("%s: %v", role, err)
		}
	}
	if err := Check("git p origin main", f.scratch, "qa", true); err != nil && strings.Contains(err.Error(), "read-only allow-list") {
		t.Errorf("hq: %v", err)
	}
}

func TestNoPushScriptFiles(t *testing.T) {
	f := newGitFix(t)
	writeFile(t, f.scratch, "push.sh", "git push origin main\n")
	writeFile(t, f.scratch, "py.py", "#!/usr/bin/env python3\nprint(1)\n")
	writeFile(t, f.scratch, "ok.sh", "git status\n")
	push, py, ok := f.scratch+"/push.sh", f.scratch+"/py.py", f.scratch+"/ok.sh"
	for _, role := range noPushRolesList {
		for _, cmd := range []string{push, "bash " + push, "source " + push, py} {
			if exitOf(f.check(cmd, f.scratch, role)) != 2 {
				t.Errorf("%s: %q not refused", role, cmd)
			}
		}
		if err := f.check(ok, f.scratch, role); err != nil {
			t.Errorf("%s: %q: %v", role, ok, err)
		}
	}
}
