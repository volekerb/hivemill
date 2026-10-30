package guard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// T-0783 (tests/test_worker_guard_nopush_gitfile.py): no-push roles may not write a `.git` gitfile or extract an archive
// into a .git dir or a target the hook cannot bound. The cases mirror the Python module; coder, cleaner, judge and HQ are
// unchanged.

type archiveFix struct {
	*gitFix
	repo, out string
}

func newArchiveFix(t *testing.T) *archiveFix {
	t.Helper()
	f := newGitFix(t)
	a := &archiveFix{gitFix: f, repo: filepath.Join(f.scratch, "r"), out: filepath.Join(f.scratch, "out")}
	for _, d := range []string{filepath.Join(a.repo, ".git"), a.out, filepath.Join(f.scratch, "n")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return a
}

func (a *archiveFix) expect(t *testing.T, roles []string, cwd string, want int, cmds ...string) {
	t.Helper()
	for _, role := range roles {
		for _, c := range cmds {
			if got := exitOf(a.check(c, cwd, role)); got != want {
				t.Errorf("role %s: %q: exit %d, want %d", role, c, got, want)
			}
		}
	}
}

func TestNoPushGitFileAndExtractRefused(t *testing.T) {
	a := newArchiveFix(t)
	s, git, o := a.scratch, a.repo+"/.git", a.out
	writeFile(t, s, "gf", "")
	a.expect(t, noPushRolesList, s, 2,
		"printf 'gitdir: x\\n' > "+s+"/n/.git", "echo 'gitdir: x' | tee "+s+"/n/.git", "cp gf "+s+"/n/.git",
		"ln -s "+s+"/g "+s+"/n/.git", "touch "+s+"/n/.git", "sed -i s/a/b/ "+s+"/n/.git", "cp gf "+o+"/.git",
		"git --git-dir="+s+"/g status", "GIT_DIR="+s+"/g git status", "export GIT_DIR="+s+"/g",
		"tar -xf a.tar -C "+git, "tar xf a.tar --directory="+git, "bsdtar -xf a.tar -C "+git, "unzip -o a.zip -d"+git,
		"cpio -id -D "+git+" < a.cpio", "rsync -a src/ "+git+"/", "tar -xf a.tar -C "+a.repo, "unzip a.zip -d "+s,
		"tar -xf a.tar", "unzip a.zip", "cpio -id < a.cpio", "tar -xf a.tar -C $WHERE", "tar -xPf a.tar -C "+o,
		"tar -xf a.tar -C "+o+" --transform='s,^,../r/.git/,'", "7z x a.7z", "pax -rf a.tar", "jar xf a.jar",
		"python3 -m zipfile -e a.zip "+git, "python3 -Im zipfile -e a.zip "+git, "python3 -Imzipfile -e a.zip "+git,
		"python3 -W ignore -m zipfile -e a.zip "+git, "python3 -m tarfile --extract a.tar "+git, "python3 -m zipfile -e a.zip",
		"ar x --output="+git+" a.a", "ar --plugin x.so --output="+git+" x a.a", "ar --target elf64 --output="+git+" x a.a",
		"ar x a.a",
		"ln -s "+s+" "+o+"/l && tar -xf a.tar -C "+o, "echo x > "+o+"/f && tar -xf a.tar -C "+o,
		"tar -xf a.tar -C "+o+"; tar -xf b.tar -C "+o)
	a.expect(t, noPushRolesList, a.repo, 2, "tar -xf a.tar", "ar x a.a")
	a.expect(t, noPushRolesList, s, 0,
		"tar -tf a.tar", "tar -xf a.tar -C "+o, "unzip a.zip -d "+o, "cpio -id -D "+o+" < a.cpio", "rsync -a src/ "+o+"/",
		"unzip -l a.zip", "7z l a.7z", "jar tf a.jar", "python3 -m zipfile -e a.zip "+o, "python3 -I -m tarfile -e a.tar "+o,
		"python3 -m zipfile -l a.zip", "ar x --output="+o+" a.a", "ar --plugin x.so --output="+o+" x a.a", "ar t a.a",
		"tar -tf a.tar && tar -xf a.tar -C "+o, "cat "+git+"/HEAD", "echo x > "+s+"/.gitignore")
	a.expect(t, noPushRolesList, o, 0, "ar x a.a")
}

func TestNoPushExtractWalk(t *testing.T) {
	a := newArchiveFix(t)
	s := a.scratch
	deep := filepath.Join(s, "deep", "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, deep, ".git", "") // a gitfile below, not a dir
	link := filepath.Join(s, "linked")
	if err := os.MkdirAll(link, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(a.repo, filepath.Join(link, "l")); err != nil {
		t.Fatal(err)
	}
	a.expect(t, noPushRolesList, s, 2, "tar -xf a.tar -C "+s+"/deep", "tar -xf a.tar -C "+link, "unzip a.zip -d "+a.home)
	if os.Geteuid() != 0 {
		locked := filepath.Join(s, "locked")
		if err := os.MkdirAll(filepath.Join(locked, "sub"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(locked, 0o755) })
		a.expect(t, noPushRolesList, s, 2, "tar -xf a.tar -C "+locked)
	}
	many := filepath.Join(s, "many")
	if err := os.MkdirAll(many, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"f0", "f1", "f2", "f3", "f4"} {
		writeFile(t, many, n, "")
	}
	if hasGitBelow(many) {
		t.Errorf("a small clean tree must not be refused")
	}
	old := archiveWalkMax
	archiveWalkMax = 3
	t.Cleanup(func() { archiveWalkMax = old })
	if !hasGitBelow(many) {
		t.Errorf("a tree larger than archiveWalkMax must be refused")
	}
	if !hasGitBelow(filepath.Join(s, "missing")) {
		t.Errorf("a missing top is a walk failure and must be refused")
	}
}

func TestNoPushGitFileEditTools(t *testing.T) {
	a := newArchiveFix(t)
	for _, role := range noPushRolesList {
		for _, p := range []string{a.scratch + "/n/.git", a.repo + "/.git"} {
			data := map[string]any{"tool_name": "Write", "cwd": a.scratch, "tool_input": map[string]any{"file_path": p}}
			if code, msg := PersistEdit(data, role); code != 2 {
				t.Errorf("%s: Write %s: exit %d, want 2 (%s)", role, p, code, msg)
			}
		}
		ok := map[string]any{"tool_name": "Write", "cwd": a.scratch, "tool_input": map[string]any{"file_path": a.scratch + "/.gitignore"}}
		if code, msg := PersistEdit(ok, role); code != 0 {
			t.Errorf("%s: Write .gitignore: exit %d (%s)", role, code, msg)
		}
	}
	for _, role := range []string{"coder", "judge", "cleaner"} {
		data := map[string]any{"tool_name": "Write", "cwd": a.scratch, "tool_input": map[string]any{"file_path": a.scratch + "/n/.git"}}
		if code, msg := PersistEdit(data, role); code != 0 {
			t.Errorf("%s: Write n/.git: exit %d (%s)", role, code, msg)
		}
	}
}

func TestNoPushGitFileOtherRolesUnchanged(t *testing.T) {
	a := newArchiveFix(t)
	s := a.scratch
	cmds := []string{"echo 'gitdir: x' > " + s + "/n/.git", "tar -xf a.tar -C " + a.repo + "/.git", "tar -xf a.tar",
		"unzip a.zip -d " + a.home, "python3 -m zipfile -e a.zip " + a.home, "ar x --output=" + a.home + " a.a"}
	a.expect(t, []string{"coder", "cleaner", "judge"}, s, 0, cmds...)
	for _, c := range cmds[:3] {
		if err := Check(c, s, "qa", true); err != nil && strings.Contains(err.Error(), ".git") {
			t.Errorf("hq: %q: %v", c, err)
		}
	}
}
