package guard

import (
	"os"
	"path/filepath"
	"testing"
)

// The guard reads files under ~ (a script a command runs, live_home, the guard export), so a test that resolves ~/...
// runs against a fixture home: its verdict must not depend on the machine's own home (T-2153).

// The two shapes ~/.local/bin/factory has on real boxes.
const (
	fxSymlinkFactory    = "symlink"           // a symlink to the CLI in ~/factory/bin
	fxForwardingFactory = "forwarding-script" // a small script that runs it
)

// fxHome is a temp passwd home (symlinks resolved) holding the files the guard may read under ~. It is not under /tmp
// (t.TempDir): commands name /tmp, and a home below it would make /tmp an ancestor of the protected trees.
func fxHome(t testing.TB, factory string) string {
	t.Helper()
	dir, err := os.MkdirTemp("/var/tmp", "guard-home-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	pw, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	g2Mkdirs(t, filepath.Join(pw, ".local", "bin"), filepath.Join(pw, ".local", "go", "bin"),
		filepath.Join(pw, ".config", "git"), filepath.Join(pw, ".config", "factory"),
		filepath.Join(pw, "factory", "bin"), filepath.Join(pw, "factory", "run"))
	write := func(rel, body string, mode os.FileMode) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(pw, rel), []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitconfig", "[user]\n\tname = fixture\n", 0o644)
	write(".config/git/config", "", 0o644)
	write(".local/go/bin/go", "\x7fELF", 0o755)
	write(".local/go/bin/gofmt", "\x7fELF", 0o755)
	write("factory/bin/factory", "#!/usr/bin/env python3\nimport sys\nprint(sys.argv)\n", 0o755)
	switch factory {
	case fxSymlinkFactory:
		if err := os.Symlink(filepath.Join(pw, "factory", "bin", "factory"), filepath.Join(pw, ".local", "bin", "factory")); err != nil {
			t.Fatal(err)
		}
	case fxForwardingFactory: // not `exec ... "$@"`: the guard refuses that as a dynamic command word
		write(".local/bin/factory", "#!/bin/sh\n\""+filepath.Join(pw, "factory", "bin", "factory")+"\" \"$@\"\n", 0o755)
	default:
		t.Fatalf("unknown factory fixture %q", factory)
	}
	return pw
}

// fxUseHome makes direct Check calls resolve ~ to pw: both $HOME (the scripts the guard reads) and the passwd home
// (the protected paths). Hook runs pass g2Opts.pw instead.
func fxUseHome(t *testing.T, pw string) {
	t.Helper()
	t.Setenv("HOME", pw)
	g2InstallHost(t, pw)
}
