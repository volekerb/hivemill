package guard2

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Port of tests/test_worker_guard_uv_wrapper.py (T-0880): `uv run [opts] [--] CMD` and `uvx [opts] CMD` are checked as
// if CMD ran directly.

const uvTask = "t-0880"
const pyPush = `import subprocess; subprocess.run(["git","push"])`

var uvNoPush = []string{"qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher"}
var uvHQ = []string{"boss", "director"}

var uvDenied = []string{
	"uv run git push origin main",
	"uv run -- git push",
	"uv run --with requests python3 -c '" + pyPush + "'",
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
	"uv run --index-url http://x python3 -c '" + pyPush + "'",
	"uv run --foo bar git push",
	"uv run --foo bar /usr/bin/git push",
	"uv run --foo bar git -C /tmp push",
	"uv run --foo bar gh pr merge 1",
	"cd /x && uv run git push origin main",
	"cd /x && uvx --from gh gh pr merge 1",
	"bash -c 'uv run git push origin main'",
	"bash -c 'cd /x && uvx --from gh gh pr merge 1'",
	"bash -c \"uv run --with requests python3 -c '" + pyPush + "'\"",
}

var uvAllowed = []string{
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
	uvFirstWordDenied = []string{"uv run git config user.name x", "uv run herdr list", "uv run kill 1", "uv run su" + "do ls"}
	uvPinnedDenied    = []string{"uv run python3 x.py git push", "uv run pytest -k git push"}
	uvBothDenied      = []string{"uv run --foo bar git push", "uv run --foo bar /usr/bin/git push", "uv run --foo bar git -C /tmp push",
		"uv run --index-url http://x git push", "uv run --color never git push", "uv --color never run git push"}
	uvDataWords = []string{"uv run python3 x.py herdr", "uv run python3 script.py kill 1", "uvx --from foo python3 x.py herdr"}
)

func uvBare(cmd string) string {
	return strings.ReplaceAll(strings.ReplaceAll(cmd, "uvx --from foo ", ""), "uv run ", "")
}

func uvRun(t *testing.T, cmd, role string) res {
	t.Helper()
	return bash(t, cmd, hookOpts{args: roleArgs(role), cwd: repoRoot(t),
		drop: []string{"WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH"}})
}

func TestUvWrapperDeniedForEveryNoPushRole(t *testing.T) {
	for _, role := range uvNoPush {
		for _, cmd := range uvDenied {
			r := uvRun(t, cmd, role)
			wantExit(t, role+": "+cmd, r, 2)
		}
	}
}

func TestUvWrapperFirstWordRulesAndPinned(t *testing.T) {
	cmds := append(append(append([]string{}, uvFirstWordDenied...), "uv run git commit -m x"), uvPinnedDenied...)
	for _, role := range []string{"qa", "reviewer"} {
		for _, cmd := range cmds {
			wantExit(t, role+": "+cmd, uvRun(t, cmd, role), 2)
		}
	}
}

func TestUvWrapperDeniedReasonNamesTheRule(t *testing.T) {
	for _, c := range []struct{ cmd, role, word string }{
		{"uv run git push origin main", "qa", "push"},
		{"uv run -- git push", "reviewer", "push"},
		{"uv run --with requests python3 -c '" + `import os; os.system("gh pr merge 1")` + "'", "qa", "gh write"},
	} {
		r := uvRun(t, c.cmd, c.role)
		if !strings.Contains(r.stderr, c.word) {
			t.Errorf("%s %q: stderr %q lacks %q", c.role, c.cmd, r.stderr, c.word)
		}
	}
}

func TestUvWrapperAllowedForNoPushRoles(t *testing.T) {
	for _, role := range uvNoPush {
		for _, cmd := range uvAllowed {
			r := uvRun(t, cmd, role)
			wantExit(t, role+": "+cmd, r, 0)
		}
	}
}

func TestUvWrapperRealFilesExist(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range []string{"ci/affected.py", "tests/test_worker_guard_uv_wrapper.py", "roles/hooks/worker-guard.py", "bin/factory"} {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			t.Errorf("%s: %v", rel, err)
		}
	}
}

func TestUvWrapperDataWordsMatchBareCommand(t *testing.T) {
	for _, role := range uvNoPush {
		for _, cmd := range uvDataWords {
			a, b := uvRun(t, cmd, role), uvRun(t, uvBare(cmd), role)
			if a.code != b.code {
				t.Errorf("%s: %q exit %d, bare %q exit %d", role, cmd, a.code, uvBare(cmd), b.code)
			}
		}
	}
}

func TestUvWrapperCleanerJudgeUnchanged(t *testing.T) {
	base := []string{"uv run pytest -q", "uvx ruff check .", "uv pip list", "uv run git status"}
	for _, role := range []string{"cleaner", "judge"} {
		for _, cmd := range append(append([]string{}, base...), uvDataWords...) {
			wantExit(t, role+": "+cmd, uvRun(t, cmd, role), 0)
		}
	}
	wantExit(t, "cleaner push", uvRun(t, "uv run git push origin main", "cleaner"), 0)
}

func TestUvWrapperHQUnchanged(t *testing.T) {
	base := []string{"uv run git push origin main", "uvx --from gh gh pr merge 1", "uv run -- git push",
		"uv run pytest -q", "uv pip list", "uv run git push origin --delete factory/t-1"}
	for _, role := range uvHQ {
		for _, cmd := range append(append([]string{}, base...), uvDataWords...) {
			wantExit(t, role+": "+cmd, uvRun(t, cmd, role), 0)
		}
	}
}

// uvCoder is the CoderUvPushTests fixture: a factory home with a DB row and a worktree with a local bare origin.
type uvCoder struct {
	t    *testing.T
	home string
	wt   string
}

func newUvCoder(t *testing.T) *uvCoder {
	t.Helper()
	tmp := realTemp(t)
	home := filepath.Join(tmp, "factory")
	wt := filepath.Join(home, "worktrees", "G-010-T-0880")
	mkdirs(t, filepath.Join(home, ".git"), filepath.Join(home, "roles"))
	guardDB(t, home, map[string]string{strings.ToUpper(uvTask): wt})
	origin := filepath.Join(tmp, "origin.git")
	git(t, tmp, "init", "-q", "--bare", origin)
	git(t, tmp, "init", "-q", "-b", "main", wt)
	git(t, wt, "commit", "-q", "--allow-empty", "-m", "i")
	git(t, wt, "remote", "add", "origin", origin)
	return &uvCoder{t: t, home: home, wt: wt}
}

func (c *uvCoder) run(cmd string) res {
	c.t.Helper()
	return bash(c.t, cmd, hookOpts{args: roleArgs("coder"), cwd: c.wt,
		drop: []string{"WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT"},
		set: map[string]string{"HOME": filepath.Dir(c.home), "WORKER_GUARD_LIVE_HOME": c.home,
			"FACTORY_HOME": c.home, "FACTORY_AGENT": "coder-" + uvTask}})
}

func TestUvWrapperCoderDenied(t *testing.T) {
	c := newUvCoder(t)
	for _, cmd := range []string{"uv run git push origin main", "uv run -- git push", "uv run git push origin HEAD:factory/t-0001",
		"uvx git push origin main", "uv run git push --force origin HEAD:factory/" + uvTask} {
		wantExit(t, cmd, c.run(cmd), 2)
	}
}

func TestUvWrapperCoderItem11Forms(t *testing.T) {
	c := newUvCoder(t)
	denied := append(append(append([]string{}, uvBothDenied...), uvFirstWordDenied...), uvPinnedDenied...)
	for _, cmd := range denied {
		wantExit(t, cmd, c.run(cmd), 2)
	}
	for _, cmd := range []string{"uv run pytest -k git -q", "uv run pytest -k git tests/test_harness.py",
		"uv run --with git pytest tests/test_harness.py -q", "uv run pytest tests/git tests/test_harness.py",
		"uv run --color never git commit -m x", "python3 -m uv run git push"} {
		wantExit(t, cmd, c.run(cmd), 0)
	}
}

func TestUvWrapperCoderOwnPushAllowed(t *testing.T) {
	c := newUvCoder(t)
	wantExit(t, "own push", c.run("uv run git push origin HEAD:factory/"+uvTask), 0)
}

func TestUvWrapperCoderOwnPushAllowedWithSeparator(t *testing.T) {
	c := newUvCoder(t)
	wantExit(t, "own push --", c.run("uv run -- git push origin HEAD:factory/"+uvTask), 0)
}

func TestUvWrapperCoderDataWordsMatchBareCommand(t *testing.T) {
	c := newUvCoder(t)
	for _, cmd := range uvDataWords {
		a, b := c.run(cmd), c.run(uvBare(cmd))
		if a.code != b.code {
			t.Errorf("%q exit %d, bare %q exit %d", cmd, a.code, uvBare(cmd), b.code)
		}
	}
}

func TestUvWrapperCoderRealFileArgumentsAllowed(t *testing.T) {
	c := newUvCoder(t)
	root := repoRoot(t)
	for _, cmd := range []string{"uv run python3 " + root + "/ci/affected.py --help",
		"uv run pytest " + root + "/tests/test_worker_guard_uv_wrapper.py -q",
		"uvx ruff check " + root + "/roles/hooks/worker-guard.py", "uv run mypy " + root + "/bin/factory"} {
		wantExit(t, cmd, c.run(cmd), 0)
	}
}

func TestUvWrapperCoderBareUvWrappedPytestRefused(t *testing.T) {
	c := newUvCoder(t)
	for _, cmd := range []string{"uv run pytest -q", "uv run --with git pytest -q", "uvx pytest", "uv tool run pytest"} {
		wantExit(t, cmd, c.run(cmd), 2)
	}
}

func TestUvWrapperCoderOtherUvCommandsAllowed(t *testing.T) {
	c := newUvCoder(t)
	for _, cmd := range []string{"uv run pytest tests/test_harness.py -q", "uvx ruff check .", "uv pip list", "uv run python3 -m unittest x"} {
		wantExit(t, cmd, c.run(cmd), 0)
	}
}
