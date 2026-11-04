package guard

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// T-1001 (tests/test_worker_guard_nopush_archive_git.py, T-0805): the Go mirror of parse_tar and the rule E extraction
// check for tar/gtar/bsdtar/unzip. The reason text is the Python text, byte for byte.

func tarBytes(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	for _, n := range names {
		if err := w.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: 1, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
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

func zipBytes(t *testing.T, names ...string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, n := range names {
		f, err := w.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		f.Write([]byte("x"))
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestParseTarGrammar(t *testing.T) {
	cases := []struct {
		args     []string
		extract  bool
		archives []string
		dirs     []string
		problems int
	}{
		{[]string{"-tf", "a.tar"}, false, []string{"a.tar"}, nil, 1}, // t is outside the extraction allowlist, harmless unless x,
		{[]string{"tf", "a.tar"}, false, []string{"a.tar"}, nil, 1},
		{[]string{"--list", "-f", "a.tar"}, false, []string{"a.tar"}, nil, 1}, // --list is not in the extraction allowlist
		{[]string{"-xf", "a.tar", "-C", "out"}, true, []string{"a.tar"}, []string{"out"}, 0},
		{[]string{"--extract", "--file=a.tar", "--directory=out"}, true, []string{"a.tar"}, []string{"out"}, 0},
		{[]string{"--ext", "-f", "a.tar"}, true, []string{"a.tar"}, nil, 1},
		{[]string{"-xf", "a.tar", "-Cout"}, true, []string{"a.tar"}, []string{"out"}, 0},
		{[]string{"-df", "p.tar"}, true, nil, nil, 1}, // d is unclassifiable: extract mode, fail closed
		{[]string{"-tf~/p.tar"}, true, nil, nil, 1},   // glued value
		{[]string{"ft", "p.tar"}, true, nil, nil, 1},  // value letter not last
		{[]string{"-fc", "p.tar"}, true, nil, nil, 1}, // value letter not last
		{[]string{"-xf", "a.tar", "--", "-C", "out"}, true, []string{"a.tar"}, []string{"out"}, 1},
		{[]string{"-xf", "a.tar", "f", "-C", "out"}, true, []string{"a.tar"}, []string{"out"}, 1}, // member before -C
		{[]string{"-xf", "a.tar", "-C", "o", "-C", "p"}, true, []string{"a.tar"}, []string{"o", "p"}, 1},
		{[]string{"-xf", "a.tar", "-f", "b.tar"}, true, []string{"a.tar", "b.tar"}, nil, 1},
		{[]string{"-xf"}, true, nil, nil, 1},
		{[]string{"-xf", "a.tar", "--strip-components=1"}, true, []string{"a.tar"}, nil, 1},
	}
	for _, c := range cases {
		ex, ar, di, pr := parseTar(c.args)
		if ex != c.extract || !reflect.DeepEqual(ar, c.archives) || !reflect.DeepEqual(di, c.dirs) || (len(pr) > 0) != (c.problems > 0) {
			t.Errorf("parseTar(%q) = %v %v %v %v; want %v %v %v problems>0=%v", c.args, ex, ar, di, pr, c.extract, c.archives, c.dirs, c.problems > 0)
		}
	}
}

func TestNoPushTarUnzipRuleE(t *testing.T) {
	a := newArchiveFix(t)
	s, o := a.scratch, a.out
	for name, body := range map[string][]byte{
		"a.tar": tarBytes(t, "f", "d/g"), "a.tgz": gzBytes(t, tarBytes(t, "f")), "a.zip": zipBytes(t, "f"),
		"bad.tar": tarBytes(t, "x/.git/config"), "bare.zip": zipBytes(t, "r/config", "r/HEAD"), "x.txz": append([]byte{0xfd, '7', 'z', 'X', 'Z', 0}, make([]byte, 600)...),
	} {
		if err := os.WriteFile(filepath.Join(s, name), body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// the forms T-0925 parked as coder-only in ci/guard_corpus.json: python=2, go was 0
	gap := []string{
		"tar -df ~/.local/go/pkg.tar", "tar -czf /tmp/x.tgz -C ~/.local/go .", "tar -tf~/.local/go/pkg.tar",
		"tar -tzf~/.local/go/pkg.tar", "tar --list -f~/.local/go/pkg.tar", "tar -tC/tmp/c -f ~/.local/go/pkg.tar",
		"tar ft ~/.local/go/pkg.tar", "tar -fc ~/.local/go/pkg.tar", "tar -tf/tmp/box.tar -C ~/.local/go",
		"cd ~/.local/go && tar -tf/tmp/box.tar",
	}
	a.expect(t, noPushRolesList, s, 2, gap...)
	a.expect(t, []string{"coder"}, s, 0, gap...)
	a.expect(t, noPushRolesList, s, 2,
		"tar -xf a.tar -C "+o+" -C "+o, "tar -xf a.tar f -C "+o, "tar -xf a.tar -- -C "+o, "tar -xf a.tar -C "+o+" --strip-components=1",
		"TAR_OPTIONS=x tar -xf a.tar -C "+o, "tar -xf a.tar -f b.tar -C "+o, "tar -xfC a.tar "+o, "tar -xf/tmp/a.tar -C "+o,
		"unzip a.zip", "unzip -x a.zip -d "+o, "unzip -d "+o+" -- a.zip", "tar -xf a.tar -C "+o+" --exclude=x",
		"tar -xf bad.tar -C "+o, "unzip bare.zip -d "+o, "tar -xJf x.txz -C "+o, "tar --get -f a.tar -C "+o,
		"touch n.tar; tar -xf n.tar -C "+o, "tar -cf n.tar f; tar -xf n.tar -C "+o, "tar -xf - -C "+o, "tar -xf $A -C "+o)
	a.expect(t, noPushRolesList, s, 0,
		"tar -tf a.tar", "tar --list -f a.tar", "tar tvf a.tar", "unzip -l a.zip", "tar --diff -f a.tar",
		"tar -xf a.tar -C "+o, "tar xf a.tar -C "+o, "tar -xzf a.tgz -C "+o, "tar -x -f a.tar -C "+o,
		"tar --extract --file=a.tar --directory="+o, "tar -xvf a.tar -C "+o, "unzip a.zip -d "+o, "unzip -o a.zip -d "+o,
		"unzip a -d "+o, "tar -xf gone.tar -C "+o)
}

func TestNoPushTarUnzipReasonText(t *testing.T) {
	a := newArchiveFix(t)
	err := a.check("tar -df ~/.local/go/pkg.tar", a.scratch, "qa")
	want := "`tar` extraction with option '-df': names on disk may differ from the listing the hook read, so a .git/config member " +
		"cannot be ruled out; name a scratchpad target (tar -C, unzip -d), use only -x -f -v -z -j -J -C -o -p -k (unzip -d -o -n -q) " +
		"and an archive that has no .git member"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("reason text = %v; want it to contain %q", err, want)
	}
	err = a.check("unzip a.zip -x", a.scratch, "qa")
	if err == nil || !strings.Contains(err.Error(), "`unzip` extraction with option '-x': names on disk may differ") {
		t.Errorf("unzip reason = %v", err)
	}
}

func TestListingOnly(t *testing.T) {
	for cmd, want := range map[string]bool{
		"tar -tf a.tar": true, "tar tf a.tar": true, "tar --list -f a.tar": true, "tar -xf a.tar": false, "tar -cf a.tar x": false,
		"tar -tcf a.tar": false, "unzip -l a.zip": true, "unzip a.zip": false, "ls -l": true, "cat > f": false, "touch f": false,
	} {
		if got := listingOnly(strings.Fields(cmd)); got != want {
			t.Errorf("listingOnly(%q) = %v, want %v", cmd, got, want)
		}
	}
}
