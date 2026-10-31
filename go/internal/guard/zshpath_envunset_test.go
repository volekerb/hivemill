package guard

import (
	"strings"
	"testing"
)

// T-0830: the Go mirror of T-0788 (PR 234). NO_PUSH_ROLES may not assign the zsh array `path` (tied to PATH) and may not
// remove a protected name (env -u/--unset/-i/-/-C/-S, unset). The command lists are the Python tests' lists
// (tests/test_worker_guard_nopush_zsh_path.py, tests/test_worker_guard_nopush_env_unset.py), run through Check as the hook is.

var zshPathDenied = []string{
	"path=(/evil)", "path=(/evil $path)", "path+=/evil", "path+=(/evil)", "path[1]=/evil",
	"read -A path", "read -a path", "typeset -g path=(/evil)", "typeset path=(/evil)", "declare -g path=(/evil)",
	"f() { local path=(/evil); }", "export path", "readonly path=(/x)", "print -v path /evil", "zparseopts -a path x",
	": $" + "{path::=/evil}", ": $" + "{path:=/evil}", ": $" + "{path=/evil}", "print -rv path x", "set -A path /x", "read -Apath",
	"for path in /evil; do :; done",
}

var zshPathAllowed = []string{
	"echo $path", "print -l $path", "for p in $path; do echo $p; done", "mypath=/x", "path_x=1", "PATHX=1",
	"filepath=(a b)", "grep path file.txt", "git log -- path",
}

var envUnsetDenied = []string{
	"env -u GIT_ASKPASS git status", "env -u GIT_CONFIG_GLOBAL git status", "env -u GIT_CONFIG_COUNT -u GIT_CONFIG_KEY_0 git log",
	"env --unset=GIT_CONFIG_PARAMETERS git status", "env --unset GIT_ASKPASS ls", "env -u PATH ls", "unset GIT_ASKPASS",
	"unset -v GIT_CONFIG_GLOBAL", "unset PATH", "unset path", "env -i git status", "env --ignore-environment ls", "env - ls",
	"env -uGIT_ASKPASS ls", "env -u HOME ls", "env -C /x -u PATH ls", "env --chdir /x -u HOME ls",
	"env -S\"-u PATH\" ls", "env --split-string=-i ls", "env -iu FOO ls",
	"env -S '-u PATH' ls", "env -S-uPATH ls", "env -u 'GIT_$x' ls", "env -u '$n' ls", "env -u PATH[0] ls",
}

var envUnsetAllowed = []string{
	"env", "env | grep GIT", "env FOO=1 ls", "env -u FOO ls", "unset FOO", "unset -v myvar", "env -C /x ls", "env -C /x -u FOO ls",
	"env -S 'ls -l'", "env --split-string='-u FOO' ls", "env -u FOO -u BAR ls", "env FOO=1 -u BAR ls",
}

func TestZshPathAndEnvUnsetNoPushRoles(t *testing.T) {
	cwd := t.TempDir()
	forms := func(cmd string) []string {
		return []string{cmd, "cd /x && " + cmd, "zsh -c '" + strings.ReplaceAll(cmd, "'", `'\''`) + "'", "bash -c '" + strings.ReplaceAll(cmd, "'", `'\''`) + "'"}
	}
	for _, role := range noPushRolesList {
		for _, cmd := range append(append([]string(nil), zshPathDenied...), envUnsetDenied...) {
			for _, form := range forms(cmd) {
				if err := Check(form, cwd, role, false); err == nil {
					t.Errorf("%s: %q: want a block, got allowed", role, form)
				}
			}
		}
		for _, cmd := range append(append([]string(nil), zshPathAllowed...), envUnsetAllowed...) {
			if err := Check(cmd, cwd, role, false); err != nil {
				t.Errorf("%s: %q: want allowed, got %v", role, cmd, err)
			}
		}
	}
}

// coder, cleaner and judge, and HQ, are not no-push roles: the same commands stay allowed for them.
func TestZshPathAndEnvUnsetOtherRolesUnchanged(t *testing.T) {
	cwd := t.TempDir()
	cmds := []string{"path=(/evil)", "path+=/evil", "path[1]=/evil", "read -A path", "set -A path /x", "zparseopts -a path x",
		"for path in /evil; do :; done", "env -u GIT_ASKPASS git status", "env -i ls", "env - ls", "env -S'-u PATH' ls",
		"unset path", "unset GIT_ASKPASS"}
	for _, role := range []string{"coder", "cleaner", "judge"} {
		for _, cmd := range cmds {
			if err := Check(cmd, cwd, role, false); err != nil {
				t.Errorf("%s: %q: want allowed, got %v", role, cmd, err)
			}
		}
	}
	for _, role := range []string{"boss", "director", "lead"} {
		for _, cmd := range cmds {
			if err := Check(cmd, cwd, role, true); err != nil {
				t.Errorf("hq %s: %q: %v", role, cmd, err)
			}
		}
	}
}

func TestBuiltinAssignZshPathAndEnv(t *testing.T) {
	cases := []struct {
		name  string
		words []string
		block string // substring of the refusal; "" = allowed
	}{
		{"path subscript assignment", []string{"path[1]=/x"}, "subscript assignment"},
		{"PATH subscript append", []string{"PATH[2]+=/x"}, "subscript assignment"},
		{"plain subscript assignment", []string{"arr[1]=x"}, ""},
		{"read -A path", []string{"read", "-A", "path"}, "read"},
		{"read -Apath", []string{"read", "-Apath"}, "read"},
		{"read -A plain", []string{"read", "-A", "arr"}, ""},
		{"print -v path", []string{"print", "-v", "path", "x"}, "print"},
		{"print -rv path", []string{"print", "-rv", "path", "x"}, "print"},
		{"print -r path is output, not assignment", []string{"print", "-r", "path"}, ""},
		{"print -vpath", []string{"print", "-vpath", "x"}, "print"},
		{"set -A path", []string{"set", "-A", "path", "/x"}, "set"},
		{"set +A path", []string{"set", "+A", "path"}, "set"},
		{"set -a path", []string{"set", "-a", "path"}, "set"},
		{"set -e", []string{"set", "-e"}, ""},
		{"zparseopts -a path", []string{"zparseopts", "-a", "path", "x"}, "zparseopts"},
		{"zparseopts -A path", []string{"zparseopts", "-A", "path", "x"}, "zparseopts"},
		{"zparseopts -apath", []string{"zparseopts", "-apath", "x"}, "zparseopts"},
		{"zparseopts -Apath", []string{"zparseopts", "-Apath", "x"}, "zparseopts"},
		{"zparseopts plain", []string{"zparseopts", "-a", "opts", "x"}, ""},
		{"for path", []string{"for", "path", "in", "a"}, "for"},
		{"default :: path", []string{"echo", "$" + "{path::=x}"}, "default"},
		{"default := path", []string{"echo", "$" + "{path:=x}"}, "default"},
		{"default = path", []string{"echo", "$" + "{path=x}"}, "default"},
		{"default :: plain", []string{"echo", "$" + "{other::=x}"}, ""},
		{"env -u PATH", []string{"env", "-u", "PATH", "ls"}, "unset"},
		{"env -uPATH", []string{"env", "-uPATH", "ls"}, "unset"},
		{"env -u path", []string{"env", "-u", "path", "ls"}, "unset"},
		{"env --unset PATH", []string{"env", "--unset", "PATH", "ls"}, "unset"},
		{"env --unset=PATH", []string{"env", "--unset=PATH", "ls"}, "unset"},
		{"env -u dynamic", []string{"env", "-u", "$n", "ls"}, "dynamic target name"},
		{"env -u plain", []string{"env", "-u", "FOO", "ls"}, ""},
		{"env -i", []string{"env", "-i", "ls"}, "clear or unset"},
		{"env -", []string{"env", "-", "ls"}, "clear or unset"},
		{"env --ignore-environment", []string{"env", "--ignore-environment", "ls"}, "clear or unset"},
		{"env -iu FOO", []string{"env", "-iu", "FOO", "ls"}, "clear or unset"},
		{"env -C skips its directory", []string{"env", "-C", "-i", "ls"}, ""},
		{"env --chdir then -u", []string{"env", "--chdir", "/x", "-u", "HOME", "ls"}, "unset"},
		{"env -S splits", []string{"env", "-S", "-u PATH ls"}, "unset"},
		{"env -S glued", []string{"env", "-S-i ls"}, "clear or unset"},
		{"env --split-string=", []string{"env", "--split-string=-u HOME ls"}, "unset"},
		{"env --split-string separate", []string{"env", "--split-string", "-i"}, "clear or unset"},
		{"env stops at the command word", []string{"env", "ls", "-u", "PATH"}, ""},
		{"env after assignment", []string{"env", "A=1", "-u", "PATH", "ls"}, "unset"},
		{"env other long option", []string{"env", "--debug", "-u", "PATH"}, "unset"},
		{"env no args", []string{"env"}, ""},
		{"env -u missing name", []string{"env", "-u"}, ""},
		{"absolute env", []string{"/usr/bin/env", "-i", "ls"}, "clear or unset"},
		{"unset PATH", []string{"unset", "PATH"}, "unset"},
		{"unset -v GIT_X", []string{"unset", "-v", "GIT_X"}, "unset"},
		{"unset path", []string{"unset", "path"}, "unset"},
		{"unset dynamic", []string{"unset", "$n"}, "dynamic target name"},
		{"unset plain", []string{"unset", "FOO"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := &Analyzer{Role: "qa"}
			err := a.builtinAssign(nil, c.words, nil)
			if c.block == "" {
				if err != nil {
					t.Fatalf("want allowed, got %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.block) {
				t.Fatalf("want a block mentioning %q, got %v", c.block, err)
			}
		})
	}
}

// The refusal text is the Python hook's, byte for byte (GUARD_IMPL=diff compares stderr).
func TestEnvUnsetRefusalText(t *testing.T) {
	a := &Analyzer{Role: "qa"}
	err := a.builtinAssign(nil, []string{"env", "-u", "PATH", "ls"}, nil)
	want := "qa may not unset HOME, XDG_CONFIG_HOME, PATH, PAGER or GIT_* (or zsh path) with env -u ('PATH': it selects the git config, helpers or pager); use a plain variable name, or ask your lead"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v, want %q", err, want)
	}
}
