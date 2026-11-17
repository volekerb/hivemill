package fsx

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func write(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func sha(t *testing.T, p string) [32]byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return sha256.Sum256(b)
}

func exists(p string) bool { _, err := os.Lstat(p); return err == nil }

func openRoot(t *testing.T, dir string) *Root {
	t.Helper()
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func TestOpenRootRefusals(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	tmp := t.TempDir()
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(tmp, link); err != nil {
		t.Fatal(err)
	}
	for name, p := range map[string]string{"slash": "/", "home": home, "relative": "some/dir", "symlink": link} {
		r, err := OpenRoot(p)
		if err == nil {
			r.Close()
			t.Errorf("%s: OpenRoot(%q) succeeded, want refusal", name, p)
			continue
		}
		if !errors.Is(err, ErrBadRoot) {
			t.Errorf("%s: err = %v, want ErrBadRoot", name, err)
		}
	}
}

func TestOpenRootHomeViaSymlinkRefused(t *testing.T) {
	real := t.TempDir()
	alias := filepath.Join(t.TempDir(), "h")
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", alias)
	if r, err := OpenRoot(real); err == nil {
		r.Close()
		t.Fatal("resolved home accepted as root")
	}
}

func TestOpenRootPlainDirAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	r, err := OpenRoot(dir)
	if err != nil {
		t.Fatalf("plain temp dir refused: %v", err)
	}
	defer r.Close()
	if r.Path() != dir {
		t.Errorf("Path = %q, want %q", r.Path(), dir)
	}
	// a subdirectory of home is fine
	sub := filepath.Join(os.Getenv("HOME"), "cache")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	r2, err := OpenRoot(sub)
	if err != nil {
		t.Fatalf("home subdir refused: %v", err)
	}
	r2.Close()
}

// symlinkFixture builds root/{keep.txt, link -> outside} and outside/secret.
func symlinkFixture(t *testing.T) (rootDir, outside string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	rootDir = t.TempDir()
	outside = t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "top secret outside content")
	write(t, filepath.Join(rootDir, "keep.txt"), "12345")
	if err := os.Symlink(outside, filepath.Join(rootDir, "link")); err != nil {
		t.Fatal(err)
	}
	return rootDir, outside
}

func TestSymlinkNeverFollowed(t *testing.T) {
	rootDir, outside := symlinkFixture(t)
	secret := filepath.Join(outside, "secret.txt")
	before := sha(t, secret)
	r := openRoot(t, rootDir)

	var sawLink bool
	var seen []string
	err := Walk(r, ".", func(e Entry) error {
		seen = append(seen, e.Rel)
		if e.Rel == "link" {
			sawLink = e.IsSymlink && e.Info.Mode()&fs.ModeSymlink != 0
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !sawLink {
		t.Error("link not reported as symlink leaf")
	}
	for _, s := range seen {
		if s == "link/secret.txt" {
			t.Errorf("Walk descended through symlink: %v", seen)
		}
	}

	sz, err := Size(r, ".")
	if err != nil {
		t.Fatal(err)
	}
	lfi, _ := os.Lstat(filepath.Join(rootDir, "link"))
	want := int64(5) + lfi.Size()
	if sz.Bytes != want || sz.Files != 2 {
		t.Errorf("Size = %+v, want bytes %d files 2 (outside file must not count)", sz, want)
	}

	id, err := r.Identity("link")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r, "link", id); err != nil {
		t.Fatal(err)
	}
	if exists(filepath.Join(rootDir, "link")) {
		t.Error("link still present")
	}
	if !exists(outside) || sha(t, secret) != before {
		t.Error("outside dir or file damaged by removing the link")
	}
}

func TestSymlinkDirComponentNotTraversed(t *testing.T) {
	rootDir, outside := symlinkFixture(t)
	r := openRoot(t, rootDir)
	if _, err := Size(r, "link/secret.txt"); err == nil {
		t.Error("Size traversed a symlinked directory component")
	}
	if _, err := Remove(r, "link/secret.txt", Identity{}); err == nil {
		t.Error("Remove traversed a symlinked directory component")
	}
	if !exists(filepath.Join(outside, "secret.txt")) {
		t.Error("outside file deleted")
	}
}

func TestEscapeRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	parent := t.TempDir()
	rootDir := filepath.Join(parent, "root")
	write(t, filepath.Join(rootDir, "a"), "a")
	victim := filepath.Join(parent, "x")
	write(t, victim, "x")
	r := openRoot(t, rootDir)
	for _, rel := range []string{"../x", "/etc", "a/../../x", victim} {
		if _, err := Remove(r, rel, Identity{}); !errors.Is(err, ErrEscape) {
			t.Errorf("Remove(%q) err = %v, want ErrEscape", rel, err)
		}
		if err := Walk(r, rel, func(Entry) error { return nil }); !errors.Is(err, ErrEscape) {
			t.Errorf("Walk(%q) err = %v, want ErrEscape", rel, err)
		}
		if _, err := Size(r, rel); !errors.Is(err, ErrEscape) {
			t.Errorf("Size(%q) err = %v, want ErrEscape", rel, err)
		}
	}
	if !exists(victim) || !exists("/etc") {
		t.Error("something outside the root was deleted")
	}
	if _, err := Remove(r, ".", Identity{}); err == nil {
		t.Error("Remove of the root itself succeeded")
	}
	if !exists(filepath.Join(rootDir, "a")) {
		t.Error("root content deleted")
	}
}

func TestDeviceNotCrossed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	write(t, filepath.Join(rootDir, "plain", "f"), "1234")
	write(t, filepath.Join(rootDir, "mnt", "big"), "123456789")
	orig := devOf
	t.Cleanup(func() { devOf = orig })
	r := openRoot(t, rootDir)
	devOf = func(fi fs.FileInfo) (uint64, bool) {
		d, ok := orig(fi)
		if fi.Name() == "mnt" {
			return d + 1, ok
		}
		return d, ok
	}
	var skipped, inside []string
	if err := Walk(r, ".", func(e Entry) error {
		if e.Skipped != "" {
			if e.Skipped != "mount point" {
				t.Errorf("reason = %q", e.Skipped)
			}
			skipped = append(skipped, e.Rel)
		}
		inside = append(inside, e.Rel)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(skipped) != 1 || skipped[0] != "mnt" {
		t.Errorf("skipped = %v, want [mnt]", skipped)
	}
	for _, s := range inside {
		if s == "mnt/big" {
			t.Error("descended into mount point")
		}
	}
	sz, err := Size(r, ".")
	if err != nil {
		t.Fatal(err)
	}
	if sz.Bytes != 4 || sz.Files != 1 {
		t.Errorf("Size = %+v, want 4 bytes, 1 file (mount excluded)", sz)
	}
}

func TestChangedItemRefused(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	r := openRoot(t, rootDir)
	base := time.Unix(1_700_000_000, 0)

	mk := func(name, content string) Identity {
		p := filepath.Join(rootDir, name)
		write(t, p, content)
		if err := os.Chtimes(p, base, base); err != nil {
			t.Fatal(err)
		}
		id, err := r.Identity(name)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}

	idM := mk("mtime", "abc")
	if err := os.Chtimes(filepath.Join(rootDir, "mtime"), base, base.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	idS := mk("size", "abc")
	write(t, filepath.Join(rootDir, "size"), "abcdef")
	os.Chtimes(filepath.Join(rootDir, "size"), base, base)

	for name, id := range map[string]Identity{"mtime": idM, "size": idS} {
		if _, err := Remove(r, name, id); !errors.Is(err, ErrChanged) {
			t.Errorf("%s: err = %v, want ErrChanged", name, err)
		}
		if !exists(filepath.Join(rootDir, name)) {
			t.Errorf("%s was deleted despite change", name)
		}
	}

	idOK := mk("same", "hello")
	if !Same(idOK, idOK) {
		t.Error("Same(a,a) false")
	}
	freed, err := Remove(r, "same", idOK)
	if err != nil {
		t.Fatal(err)
	}
	if freed != 5 || exists(filepath.Join(rootDir, "same")) {
		t.Errorf("freed=%d exists=%v", freed, exists(filepath.Join(rootDir, "same")))
	}
}

func TestRemoveTreeCountsBytes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	write(t, filepath.Join(rootDir, "d", "a"), "12")
	write(t, filepath.Join(rootDir, "d", "sub", "b"), "345")
	r := openRoot(t, rootDir)
	id, _ := r.Identity("d")
	freed, err := Remove(r, "d", id)
	if err != nil || freed != 5 {
		t.Fatalf("freed=%d err=%v", freed, err)
	}
	if exists(filepath.Join(rootDir, "d")) {
		t.Error("dir not removed")
	}
}

func TestLedger(t *testing.T) {
	l := NewLedger(100, map[string]int64{"small": 10})
	if err := l.Reserve("a", 60); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve("a", 50); !errors.Is(err, ErrCap) {
		t.Fatalf("err = %v, want ErrCap", err)
	}
	if l.Total() != 60 {
		t.Errorf("Total = %d, want 60 (no partial reservation)", l.Total())
	}
	if err := l.Reserve("small", 11); !errors.Is(err, ErrCap) {
		t.Errorf("rule cap: err = %v", err)
	}
	if l.Total() != 60 {
		t.Errorf("Total after refused rule = %d", l.Total())
	}
	if err := l.Reserve("small", 10); err != nil {
		t.Errorf("rule cap exactly 10 refused: %v", err)
	}
	if err := l.Reserve("small", 1); !errors.Is(err, ErrCap) {
		t.Errorf("cumulative rule cap not enforced: %v", err)
	}
	if l.Total() != 70 {
		t.Errorf("Total = %d, want 70", l.Total())
	}
	if err := l.Reserve("a", -1); err == nil {
		t.Error("negative reservation accepted")
	}
}

func TestNeverRoot(t *testing.T) {
	orig := geteuid
	t.Cleanup(func() { geteuid = orig })
	geteuid = func() int { return 0 }
	err := NeverRoot()
	if err == nil || err.Error() != "hm-clean: refusing to run as root" {
		t.Fatalf("err = %v", err)
	}
	geteuid = func() int { return 1000 }
	if err := NeverRoot(); err != nil {
		t.Errorf("euid 1000 refused: %v", err)
	}
}

func TestLogWriteAndPrune(t *testing.T) {
	state := t.TempDir()
	fixed := time.Date(2026, 11, 17, 12, 0, 0, 0, time.UTC)
	orig := now
	now = func() time.Time { return fixed }
	t.Cleanup(func() { now = orig })

	for kind, sub := range map[string]string{"plan": "plans", "apply": "logs"} {
		p, err := WriteJSON(state, kind, map[string]int{"n": 1})
		if err != nil {
			t.Fatal(err)
		}
		if filepath.Dir(p) != filepath.Join(state, sub) {
			t.Errorf("%s written to %s", kind, p)
		}
		fi, _ := os.Stat(p)
		di, _ := os.Stat(filepath.Dir(p))
		if fi.Mode().Perm() != 0o600 || di.Mode().Perm() != 0o700 {
			t.Errorf("modes file %v dir %v", fi.Mode().Perm(), di.Mode().Perm())
		}
		b, _ := os.ReadFile(p)
		var got map[string]int
		if err := json.Unmarshal(b, &got); err != nil || got["n"] != 1 {
			t.Errorf("bad json %q: %v", b, err)
		}
		ents, _ := os.ReadDir(filepath.Dir(p))
		if len(ents) != 1 {
			t.Errorf("temp file left behind: %v", ents)
		}
	}
	if _, err := WriteJSON(state, "bogus", 1); err == nil {
		t.Error("unknown kind accepted")
	}

	logs := filepath.Join(state, "logs")
	age := func(p string, days int) {
		write(t, p, "{}")
		ts := fixed.Add(-time.Duration(days) * 24 * time.Hour)
		if err := os.Chtimes(p, ts, ts); err != nil {
			t.Fatal(err)
		}
	}
	old, young := filepath.Join(logs, "old.json"), filepath.Join(logs, "young.json")
	oldTxt := filepath.Join(logs, "old.txt")
	outside := filepath.Join(state, "other", "old.json")
	age(old, 31)
	age(young, 29)
	age(oldTxt, 31)
	age(outside, 31)
	n, err := PruneLogs(state, LogRetention)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || exists(old) {
		t.Errorf("old json not pruned (n=%d)", n)
	}
	for _, keep := range []string{young, oldTxt, outside} {
		if !exists(keep) {
			t.Errorf("%s wrongly pruned", keep)
		}
	}
}

func TestLogPruneMissingDirs(t *testing.T) {
	if n, err := PruneLogs(t.TempDir(), LogRetention); err != nil || n != 0 {
		t.Errorf("n=%d err=%v", n, err)
	}
}

func TestDiskFree(t *testing.T) {
	total, free, err := DiskFree(t.TempDir())
	if err != nil || total == 0 || free > total {
		t.Errorf("total=%d free=%d err=%v", total, free, err)
	}
	if _, _, err := DiskFree(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("missing path accepted")
	}
}

func TestDeviceRemoveRefusesMount(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	rootDir := t.TempDir()
	write(t, filepath.Join(rootDir, "d", "mnt", "big"), "123456789")
	write(t, filepath.Join(rootDir, "d", "f"), "1")
	orig := devOf
	t.Cleanup(func() { devOf = orig })
	r := openRoot(t, rootDir)
	devOf = func(fi fs.FileInfo) (uint64, bool) {
		d, ok := orig(fi)
		if fi.Name() == "mnt" {
			return d + 1, ok
		}
		return d, ok
	}
	for _, rel := range []string{"d", "d/mnt"} {
		id, err := r.Identity(rel)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Remove(r, rel, id); !errors.Is(err, ErrMount) {
			t.Errorf("Remove(%s) err = %v, want ErrMount", rel, err)
		}
	}
	if !exists(filepath.Join(rootDir, "d", "mnt", "big")) || !exists(filepath.Join(rootDir, "d", "f")) {
		t.Error("files under a mount point were deleted")
	}
}

func TestLedgerOverflow(t *testing.T) {
	l := NewLedger(100, nil)
	if err := l.Reserve("a", 60); err != nil {
		t.Fatal(err)
	}
	if err := l.Reserve("a", int64(^uint64(0)>>1)); !errors.Is(err, ErrCap) {
		t.Errorf("huge reservation: err = %v, want ErrCap", err)
	}
	if l.Total() != 60 {
		t.Errorf("Total = %d", l.Total())
	}
}

func TestLogSymlinkedDirRefused(t *testing.T) {
	state := t.TempDir()
	target := t.TempDir()
	write(t, filepath.Join(target, "old.json"), "{}")
	old := time.Now().Add(-90 * 24 * time.Hour)
	os.Chtimes(filepath.Join(target, "old.json"), old, old)
	if err := os.Symlink(target, filepath.Join(state, "logs")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteJSON(state, "apply", 1); err == nil {
		t.Error("WriteJSON followed symlinked logs/")
	}
	if _, err := PruneLogs(state, LogRetention); err == nil {
		t.Error("PruneLogs followed symlinked logs/")
	}
	ents, _ := os.ReadDir(target)
	if len(ents) != 1 || ents[0].Name() != "old.json" {
		t.Errorf("target dir modified: %v", ents)
	}
}

func TestLogSameInstantNoOverwrite(t *testing.T) {
	state := t.TempDir()
	orig := now
	now = func() time.Time { return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC) }
	t.Cleanup(func() { now = orig })
	a, err := WriteJSON(state, "plan", "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := WriteJSON(state, "plan", "b")
	if err != nil {
		t.Fatal(err)
	}
	if a == b || sha(t, a) == sha(t, b) {
		t.Errorf("second write replaced the first: %s %s", a, b)
	}
}
