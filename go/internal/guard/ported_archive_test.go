package guard

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// T-2112: case (a) of the Go port, the archive member list of a no-push extraction (T-0805/T-1001), read in Go the way
// Analyzer.archive_members/archive_names read it with tarfile and zipfile.

type tarEnt struct {
	name, link string
	typ        byte
}

func tarBytes(t *testing.T, ents ...tarEnt) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, e := range ents {
		h := &tar.Header{Name: e.name, Linkname: e.link, Typeflag: e.typ, Mode: 0o644, Format: tar.FormatGNU}
		if e.typ == 0 {
			h.Typeflag = tar.TypeReg
			if strings.HasSuffix(e.name, "/") {
				h.Typeflag = tar.TypeDir
			}
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func zipBytes(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range names {
		if _, err := w.Create(n); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func gzBytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	if _, err := w.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// bz2Bytes compresses with python3's bz2 (Go has no bzip2 writer); nil when python3 is not installed.
func bz2Bytes(t *testing.T, raw []byte) []byte {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		return nil
	}
	cmd := exec.Command(py, "-I", "-c", "import bz2,sys; sys.stdout.buffer.write(bz2.compress(sys.stdin.buffer.read()))")
	cmd.Stdin = bytes.NewReader(raw)
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPortedArchiveMembers(t *testing.T) {
	a := newArchiveFix(t)
	s, o := a.scratch, a.out
	put := func(name string, b []byte) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(s, name), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	clean := tarBytes(t, tarEnt{name: "src/"}, tarEnt{name: "src/main.go"}, tarEnt{name: "README"})
	put("clean.tar", clean)
	put("clean.tgz", gzBytes(t, clean))
	put("clean.zip", zipBytes(t, "src/", "src/main.go", "config"))
	put("git.tar", tarBytes(t, tarEnt{name: "x/.git/config"}))
	put("gitup.tar", tarBytes(t, tarEnt{name: "./.GIT/config"}))
	put("gitbs.tar", tarBytes(t, tarEnt{name: "x\\.git\\hooks\\pre-commit"}))
	put("link.tar", tarBytes(t, tarEnt{name: "l", link: "../r/.git/hooks", typ: tar.TypeSymlink}))
	put("hard.tar", tarBytes(t, tarEnt{name: "README"}, tarEnt{name: "h", link: "a/.Git/config", typ: tar.TypeLink}))
	put("bare.tar", tarBytes(t, tarEnt{name: "b/config"}, tarEnt{name: "README"}, tarEnt{name: "b/HEAD"}))
	put("bare.zip", zipBytes(t, "config", "HEAD"))
	put("git.zip", zipBytes(t, "ok", ".git/config"))
	put("git.tgz", gzBytes(t, tarBytes(t, tarEnt{name: ".git/hooks/post-checkout"})))
	put("junk.tar", bytes.Repeat([]byte("junk"), 300))
	put("empty.tar", nil)
	put("zeros.tar", make([]byte, 10240))
	put("notzip.zip", []byte("PK\x03\x04 not a zip"))
	many := []tarEnt{}
	for i := 0; i < 6; i++ {
		many = append(many, tarEnt{name: "f" + string(rune('a'+i))})
	}
	put("many.tar", tarBytes(t, many...))
	// two archives with zero blocks between them: ignore_zeros lists the second one too
	cat := append(append(append([]byte{}, clean...), make([]byte, 4096)...), tarBytes(t, tarEnt{name: "docs/a.md"})...)
	put("cat.tar", cat)
	catgit := append(append(append([]byte{}, clean...), make([]byte, 4096)...), tarBytes(t, tarEnt{name: "docs/.git/config"})...)
	put("catgit.tar", catgit)
	if err := syscall.Mkfifo(filepath.Join(s, "fifo.tar"), 0o644); err != nil {
		t.Fatal(err)
	}
	if b := bz2Bytes(t, clean); b != nil {
		put("clean.tbz", b)
		put("git.tbz", bz2Bytes(t, tarBytes(t, tarEnt{name: ".git/config"})))
	}
	x := func(arch string) string { return "tar -xf " + arch + " -C " + o }
	cases := []struct {
		cmd, want string // want "" allows; otherwise a substring of the block message
	}{
		{x("clean.tar"), ""},
		{"tar -xzf clean.tgz -C " + o, ""},
		{"unzip clean.zip -d " + o, ""},
		{"unzip clean -d " + o, ""}, // unzip also opens clean.zip
		{x("gone.tar"), ""},         // the first command: the file does not exist, nothing is extracted
		{"tar -tf gone.tar && " + x("gone.tar"), ""},
		{x("cat.tar"), ""},
		{x("git.tar"), "has a member 'x/.git/config' under a .git path"},
		{x("gitup.tar"), "has a member './.GIT/config' under a .git path"},
		{x("gitbs.tar"), "under a .git path"},
		{x("link.tar"), "has a member 'l' under a .git path"},
		{x("hard.tar"), "has a member 'h' under a .git path"},
		{x("bare.tar"), "has config and HEAD in one directory"},
		{"unzip bare.zip -d " + o, "has config and HEAD in one directory"},
		{"unzip git.zip -d " + o, "has a member '.git/config' under a .git path"},
		{"unzip git -d " + o, "has a member '.git/config' under a .git path"},
		{"tar -xzf git.tgz -C " + o, "under a .git path"},
		{x("catgit.tar"), "has a member 'docs/.git/config'"},
		{x("junk.tar"), "cannot be listed (ValueError)"},
		{x("empty.tar"), "cannot be listed (ReadError)"},
		{x("zeros.tar"), "cannot be listed (ValueError)"},
		{"unzip notzip.zip -d " + o, "cannot be listed (BadZipFile)"},
		{x("fifo.tar"), "is not a regular file under 256 MB"},
		{"wc -l q; " + x("gone.tar"), "does not exist yet; an earlier command on this line may create it"},
		{x(s + "/n"), "is not a regular file under 256 MB"},
	}
	if _, err := os.Stat(filepath.Join(s, "clean.tbz")); err == nil {
		cases = append(cases, struct{ cmd, want string }{"tar -xjf clean.tbz -C " + o, ""},
			struct{ cmd, want string }{"tar -xjf git.tbz -C " + o, "has a member '.git/config' under a .git path"})
	}
	run := func(cmd, want string) {
		t.Helper()
		err := a.check(cmd, s, "qa")
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: want allowed, got %v", cmd, err)
		case want != "" && (err == nil || IsDelegate(err) || exitOf(err) != 2 || !strings.Contains(err.Error(), want)):
			t.Errorf("%q: want a block containing %q, got %v", cmd, want, err)
		}
	}
	for _, c := range cases {
		run(c.cmd, c.want)
	}
	// the exact Python text of one block
	want := "`tar` archive " + pyRepr(filepath.Join(s, "git.tar")) + " has a member 'x/.git/config' under a .git path: " +
		"its config/hooks would run as git (" + persistWhy + "); extract an archive you have listed (tar -tf, unzip -l) " +
		"that has no .git member and no config+HEAD pair"
	if err := a.check(x("git.tar"), s, "qa"); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("git.tar: got %v, want %q", err, want)
	}
	// too many members (ARCHIVE_WALK_MAX), lowered for the test
	old := archiveWalkMax
	archiveWalkMax = 5
	t.Cleanup(func() { archiveWalkMax = old })
	run(x("many.tar"), "cannot be listed (RuntimeError)")
	run(x("clean.tar"), "")
}

func TestArchiveListHelpers(t *testing.T) {
	for _, c := range []struct {
		s    string
		base int
		asc  bool
		want int64
		ok   bool
	}{
		{" 0o_7", 8, true, 7, true}, {"0_7", 8, true, 7, true}, {"+_1", 10, false, 0, false}, {"1__2", 10, false, 0, false},
		{"\x1c5", 10, false, 0, false}, {"\u00855", 10, false, 5, true}, {"٣", 10, false, 3, true}, {" 5\n", 10, true, 5, true},
		{"\v5", 10, true, 5, true}, {"8", 8, true, 0, false}, {"1_", 10, false, 0, false}, {"-12", 10, false, -12, true},
	} {
		if v, ok := pyInt(c.s, c.base, c.asc); v != c.want || ok != c.ok {
			t.Errorf("pyInt(%q, %d) = %d %v, want %d %v", c.s, c.base, v, ok, c.want, c.ok)
		}
	}
	if got := pyReprSE("a\xffb'"); got != `"a\udcffb'"` {
		t.Errorf("pyReprSE = %s", got)
	}
	if got := cp437([]byte{0x80, 0xff, 'a'}); got != "Ç a" {
		t.Errorf("cp437 = %q", got)
	}
	for _, c := range []struct {
		w    []string
		want bool
	}{
		{[]string{"tar", "-tf", "a"}, true}, {[]string{"tar", "tvf", "a"}, true}, {[]string{"tar", "-xtf", "a"}, false},
		{[]string{"tar", "-f", "a", "--list"}, true}, {[]string{"cat", "a"}, true}, {[]string{"echo", "x", ">", "f"}, false},
		{[]string{"touch", "a"}, false}, {[]string{"unzip", "-l", "a"}, true}, {[]string{"/bin/ls"}, true},
	} {
		if got := listingOnly(c.w); got != c.want {
			t.Errorf("listingOnly(%q) = %v", c.w, got)
		}
	}
}

// TestArchiveNamesMutated feeds byte-mutated tar, tgz and zip archives to the readers: none may panic or hang.
func TestArchiveNamesMutated(t *testing.T) {
	base := tarBytes(t, tarEnt{name: "src/"}, tarEnt{name: "src/main.go"}, tarEnt{name: "l", link: "x", typ: tar.TypeSymlink})
	seeds := [][]byte{base, gzBytes(t, base), zipBytes(t, "a", "b/c"), []byte("PK\x05\x06" + strings.Repeat("\x00", 18))}
	r := rand.New(rand.NewSource(2112))
	p := filepath.Join(t.TempDir(), "a")
	for i := 0; i < 3000; i++ {
		b := append([]byte{}, seeds[i%len(seeds)]...)
		for k := r.Intn(8) + 1; k > 0; k-- {
			switch j := r.Intn(len(b)); r.Intn(3) {
			case 0:
				b[j] = byte(r.Intn(256))
			case 1:
				b = b[:j]
			default:
				b[j] = "0123456789 \x00\xff\x80Lxg5S"[r.Intn(17)]
			}
			if len(b) == 0 {
				break
			}
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = archiveNames(p, false)
		_, _ = archiveNames(p, true)
	}
}

// FuzzArchiveNames checks the tar and zip readers never panic or hang on arbitrary bytes.
func FuzzArchiveNames(f *testing.F) {
	f.Add([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	f.Add(make([]byte, 1024))
	f.Add([]byte("\x1f\x8b\x08\x08\x00\x00\x00\x00\x00\x03a\x00"))
	f.Fuzz(func(t *testing.T, b []byte) {
		p := filepath.Join(t.TempDir(), "a")
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
		_, _ = archiveNames(p, false)
		_, _ = archiveNames(p, true)
	})
}
