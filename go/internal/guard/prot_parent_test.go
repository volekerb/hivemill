package guard

import (
	"os/user"
	"strings"
	"testing"
)

// T-0983: ~name targets, copy into a parent, find that deletes, git init / worktree add. tests/test_worker_guard_prot_trees.py
// is the behaviour contract (it drives both implementations through the hook); this runs the Go port on the same shapes.

func protCases(t *testing.T, live, wt, other string, blocked []string, allowed []string, cwds map[string][]string) {
	t.Helper()
	u, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	fill := func(c string) string {
		return strings.ReplaceAll(strings.ReplaceAll(c, "@L", live), "~@U", "~"+u.Username)
	}
	for _, c := range blocked {
		for _, cwd := range []string{other, live, wt} {
			if err := Check(fill(c), cwd, "coder", false); err == nil {
				t.Errorf("%q from %s: expected a block", c, cwd)
			}
		}
	}
	for _, c := range allowed {
		for _, cwd := range cwds[c] {
			if err := Check(fill(c), cwd, "coder", false); err != nil {
				t.Errorf("%q from %s: expected allow, got %v", c, cwd, err)
			}
		}
	}
}

func TestProtTildeNameAndCopyIntoParentAndFind(t *testing.T) {
	live, wt, other := protFixture(t)
	blocked := []string{
		"echo x > ~@U/.local/bin/git", "cp /tmp/x ~@U/.local/bin/git", "echo x > ~@U/.local/lib/factory/x",
		"echo x > ~@U/.config/factory/x", "echo x > ~@U/.local/go/go.env", "curl -o ~@U/.local/lib/factory/x URL",
		"cd ~@U/.local/lib/factory && tar -xf a.tar", "echo x > ~nosuchuser123/.local/bin/git",
		"cd ~ && echo x > ~+/.local/bin/git", "echo x > ~-/.local/bin/git",
		"cp /tmp/bin ~/.local/", "cp -r /tmp/bin ~/.local", "cp -r /tmp/go ~/.local/", "mv /tmp/bin ~/.local/",
		"rsync -a /tmp/bin ~/.local/", "cp -r /tmp/factory ~/.local/lib", "cp -r /tmp/factory ~/.local/lib/",
		"cd ~/.local && cp -r /tmp/bin .", "tar -C ~/.local -xf a.tar", "cd ~/.local && tar -xf a.tar",
		"cp -r /tmp/x/. ~/.local/", "cp -rT /tmp/x ~/.local", "rsync -a /tmp/x/ ~/.local/", "cp /tmp/* ~/.local/",
		"unzip a.zip -d ~/.local", "cp --parents /tmp/x ~/.local/", "ln -s /tmp/bin ~/.local/", "install -t ~/.local /tmp/bin",
		"cp -r /tmp/bin @L/", "cp -r /tmp/hooks @L/roles/", "cp -r /tmp/settings @L/roles/", "tar -C @L -xf a.tar",
		"find ~/.local/bin -name git -delete", "find ~/.local/lib/factory -delete", "find ~/.local/go -name go.env -delete",
		"find @L/bin -name x -delete", "find @L/run/settings -delete", "find ~/.local/bin -name git -exec rm {} +",
		"find ~/.local -name git -delete", "find ~/.local/bin -fprint ~/.local/bin/x", "cd ~/.local/bin && find -delete",
		"find @L/go -delete", "find @L/roles/hooks -delete", "find @L/roles/settings -name x -exec rm {} +",
		"find @L -name x -delete", "find -H ~/.local/bin -delete", "find ~/.config/go -fls ~/.config/go/x",
		"git init ~/.local/go/x", "git worktree add ~/.local/go/x", "git init ~/.config/factory/x", "git init @L/bin/x",
		"git worktree add -b nb @L/roles/settings/x", "git -C ~/.local/go init", "cd ~/.local/go && git init",
		"git init --separate-git-dir ~/.local/go/g /tmp/z",
		"find ~/.local/go -maxdepth 0 -exec git init {} +", "find ~/.local/bin -name x -exec zip -m /tmp/a.zip {} +",
		"find ~/.local/lib/factory -exec ./x.sh {} +", "find ~ -name x -exec make {} +",
		"rsync -a /tmp/bin ~/.local/ --exclude x", "install /tmp/bin ~/.local/ -m 755", "cp -r /tmp/bin ~/.local/ -S .bak",
		"cp -r /tmp/bin ~/.{local,x}/",
	}
	allowed := map[string][]string{
		"ls ~@U/.local/bin": {other}, "cat ~@U/.config/factory/x": {other}, "cp ~@U/.local/go/go.env /tmp/x": {other},
		"echo x > ~@U/.local/gox/y": {other}, "echo x > /tmp/ok": {other}, "echo x > ~nosuchuser123/y": {other},
		"cp /tmp/x ~/.local/": {other}, "cp /tmp/x ~/.local/share/": {other}, "cp -r /tmp/bin /tmp/y/": {other},
		"cp /tmp/x @L/roles/": {other}, "cp -r /tmp/bin .": {wt}, "tar -xf a.tar": {wt}, "tar -C . -xf a.tar": {wt},
		"find /tmp/x -delete": {other}, "find ~/.local/bin -name git": {other}, "find ~/.local/lib/factory -type f": {other},
		"find ~/.cache/x -delete": {other}, "find . -name '*.pyc' -delete": {wt}, "find bin -name x -delete": {wt},
		"find ~/.local/bin -name git -exec ls {} \\;": {other},
		"find ~/.local/bin -name x -exec ls {} +":     {other}, "find ~/.local/lib/factory -type f -exec grep y {} +": {other},
		"find /tmp/x -exec git init {} +": {other}, "find . -name '*.py' -exec sed -i s/a/b/ {} +": {wt},
		"rsync -a /tmp/bin /tmp/y/ --exclude x": {other}, "cp -r /tmp/x ~/.local/ -S .bak": {other},
		"git init /tmp/x": {other}, "git worktree add /tmp/x": {other},
	}
	var keys []string
	for c := range allowed {
		keys = append(keys, c)
	}
	protCases(t, live, wt, other, blocked, keys, allowed)
}

// Attempt 2 findings (reviewer @daa62fec): N1 the find expression after an escaped semicolon is still judged, N2 the
// -exec read-only list compares the whole word, N3 an unresolvable `cd ~name` keeps the known cwd (a path relative to it).
func TestProtFindAttemptTwo(t *testing.T) {
	live, wt, other := protFixture(t)
	blocked := []string{
		"find ~/.local/bin -exec ls {} \\; -delete", "find ~/.local/bin -exec cat {} \\; -fprint /tmp/x",
		"find ~/.local/bin -exec ls {} \\; -exec rm {} +", "find ~/.local/go -name x -exec grep y {} \\; -exec git init {} +",
		"find ~/.local/bin -name x -exec ./cat {} +", "find ~/.local/bin -name x -exec /tmp/grep {} +",
		"find ~/.local/bin -exec ./ls {} \\;", "find ~/.local/bin -exec /usr/local/bin/cat {} \\;",
		"cd ~/.local/bin && cd ~nosuchuser123 && echo x > git", "cd ~/.local/go && cd ~- && cp /tmp/x y",
		"cd @L/bin && cd ~nosuchuser123 && tar -xf a.tar",
	}
	allowed := map[string][]string{
		"find ~/.local/bin -exec ls {} \\; -name y": {other}, "find /tmp/x -exec ls {} \\; -delete": {other},
		"find ~/.local/bin -exec /usr/bin/cat {} \\;": {other}, "find ~/.local/bin -exec /bin/ls {} +": {other},
		"find ~/.local/bin -exec cat {} \\; -print": {other},
	}
	var keys []string
	for c := range allowed {
		keys = append(keys, c)
	}
	protCases(t, live, wt, other, blocked, keys, allowed)
}
