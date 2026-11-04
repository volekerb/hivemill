package guard

import (
	"reflect"
	"strings"
	"testing"
)

// T-1001 (tests/test_worker_guard_nopush_archive_git.py, T-0805): the Go mirror of parse_tar and the rule E extraction
// check for tar/gtar/bsdtar/unzip. The reason text is the Python text, byte for byte.

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
	// the forms T-0925 parked as coder-only in ci/guard_corpus.json: python=2, go was 0
	gap := []string{
		"tar -df ~/.local/go/pkg.tar", "tar -czf /tmp/x.tgz -C ~/.local/go .", "tar -tf~/.local/go/pkg.tar",
		"tar -tzf~/.local/go/pkg.tar", "tar --list -f~/.local/go/pkg.tar", "tar -tC/tmp/c -f ~/.local/go/pkg.tar",
		"tar ft ~/.local/go/pkg.tar", "tar -fc ~/.local/go/pkg.tar", "tar -tf/tmp/box.tar -C ~/.local/go",
		"cd ~/.local/go && tar -tf/tmp/box.tar",
	}
	a.expect(t, noPushRolesList, s, 2, gap...)
	a.expect(t, []string{"coder"}, s, 0, gap...)
	// refused in Go itself (grammar, env, target, stdin), before any delegation
	a.expect(t, noPushRolesList, s, 2,
		"tar -xf a.tar -C "+o+" -C "+o, "tar -xf a.tar f -C "+o, "tar -xf a.tar -- -C "+o, "tar -xf a.tar -C "+o+" --strip-components=1",
		"TAR_OPTIONS=x tar -xf a.tar -C "+o, "tar -xf a.tar -f b.tar -C "+o, "tar -xfC a.tar "+o, "tar -xf/tmp/a.tar -C "+o,
		"unzip a.zip", "unzip -x a.zip -d "+o, "unzip -d "+o+" -- a.zip", "tar -xf a.tar -C "+o+" --exclude=x",
		"tar --get -f a.tar -C "+o, "touch n.tar; tar -xf n.tar -C "+o, "tar -xf - -C "+o, "tar -xf $A -C "+o)
	for _, role := range noPushRolesList {
		for _, c := range gap {
			if err := a.check(c, s, role); err == nil || IsDelegate(err) {
				t.Errorf("role %s: %q must be refused in Go, got %v", role, c, err)
			}
		}
	}
	// listings are allowed in Go; a clean extraction passes every Go check and is delegated, not decided
	a.expect(t, noPushRolesList, s, 0,
		"tar -tf a.tar", "tar --list -f a.tar", "tar tvf a.tar", "unzip -l a.zip", "tar --diff -f a.tar")
	for _, role := range noPushRolesList {
		for _, c := range []string{
			"tar -xf a.tar -C " + o, "tar xf a.tar -C " + o, "tar -xzf a.tgz -C " + o, "tar -x -f a.tar -C " + o,
			"tar --extract --file=a.tar --directory=" + o, "tar -xvf a.tar -C " + o, "unzip a.zip -d " + o, "unzip -o a.zip -d " + o,
			"unzip a -d " + o, "tar -xf gone.tar -C " + o, "tar -xf bad.tar -C " + o,
			"unzip bare.zip -d " + o, "tar -xJf x.txz -C " + o, "noglob cat ok.tar; tar -xf n.tar -C " + o,
		} {
			if err := a.check(c, s, role); !IsDelegate(err) {
				t.Errorf("role %s: %q: want a Delegate, got %v", role, c, err)
			}
		}
	}
	a.expect(t, []string{"coder"}, s, 0, "tar -xf a.tar", "unzip a.zip")
}

// FuzzParseTar checks parseTar neither panics nor hangs and keeps its invariants for any argument list.
func FuzzParseTar(f *testing.F) {
	for _, s := range []string{
		"-tf a.tar", "tf a.tar", "--list -f a.tar", "-xf a.tar -C out", "--extract --file=a.tar --directory=out", "-df p.tar",
		"-tf~/p.tar", "ft p.tar", "-fc p.tar", "-xf a.tar -- -C out", "-xf a.tar f -C out", "-xf a.tar -C o -C p", "-xf", "-C", "--", "-", "",
		"-xf a.tar --strip-components=1", "--ext -f a.tar", "-xfC a.tar out", "-xvzjJf\n a.tar",
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		args := strings.Fields(s)
		within(t, s, func() {
			_, archives, dirs, problems := parseTar(args)
			if (len(archives) > 1 || len(dirs) > 1) && len(problems) == 0 {
				t.Errorf("parseTar(%q): %d archives, %d dirs, no problem", args, len(archives), len(dirs))
			}
		})
	})
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
