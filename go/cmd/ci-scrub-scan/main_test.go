package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockedText holds one hit of most kinds. Every secret-looking value is made up.
const blockedText = "ok line\n" +
	"owner Zorbulonic here\n" +
	"the word qux alone, but quxx is fine\n" +
	"path /ho\x6de/alice/project/file\n" +
	"lan 192\x2e168\x2e1\x2e20 and cgnat 100\x2e64\x2e3\x2e4\n" +
	"mail bob.builder\x40corp.test\n" +
	"token gh\x70_" + "0123456789abcdefghijklmnopqrstuvwxyzAB\n" +
	"api\x5fkey = \"abcdEFGH12345678abcdEFGH\"\n" +
	"\x2d----BEGIN RSA PRIVATE KEY-----\n" +
	"clean 10.x not an address, user\x40example.com is allowed\n"

// unicodeText holds the Python-vs-Go differences of \s, \d and \w: NBSP, \v and U+0663 next to the patterns.
const unicodeText = "/ho\x6de/a\u00a0b/x/ and /ho\x6de/c\vd/y/\n" +
	"addr 10.\u0663.1.1 and 192.168.\u0661.5\n" +
	"secr\x65t = abcdefghijklmnop\u00a0qrstuvwxyz1234\n" +
	"Zorbulonic\u00a0qux\u0663 qux\u00a0 \u0663qux\n" +
	"i\u0307 \u0130 Zorbul\u0130nic ZORBULONIC\n"

func setup(t *testing.T) (dir string, environ []string) {
	t.Helper()
	dir = t.TempDir()
	write(t, filepath.Join(dir, "terms.txt"), syntheticTerms)
	write(t, filepath.Join(dir, "allow.txt"), "")
	return dir, env(t, "SCRUB_TERMS_FILE="+filepath.Join(dir, "terms.txt"))
}

func TestScrubParityPath(t *testing.T) {
	py, bin := needPython(t, "ci/scrub_scan.py"), goBin(t, "ci-scrub-scan")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "blocked.txt"), blockedText)
	write(t, filepath.Join(dir, "clean.txt"), "nothing to see here\n1.2.3 and PR #309\n")
	write(t, filepath.Join(dir, "bin.dat"), "abc\x00 Zorbulonic")
	write(t, filepath.Join(dir, "allow.txt"), "blocked.txt:email:bob\\.builder  # reviewed synthetic\nblocked.txt:rfc1918:192\\.168  # synthetic\nnever.txt:email:x  # unused entry\nlegacy.txt:home-path  # legacy form\n")
	g := compare(t, py, bin, dir, e, "", true, "--path", "blocked.txt", "--allowlist", "allow.txt")
	if g.code != 1 || !strings.Contains(g.stdout, "owner-term#") || strings.Contains(g.stdout, "Zorbulonic") {
		t.Errorf("blocked fixture: want exit 1 with owner-term labels only, got %d:\n%s", g.code, g.stdout)
	}
	if g := compare(t, py, bin, dir, e, "", true, "--path", "clean.txt", "bin.dat", "--allowlist", "allow.txt"); g.code != 0 {
		t.Errorf("clean fixture: want exit 0, got %d: %s", g.code, g.stdout)
	}
	compare(t, py, bin, dir, e, "", true, "--path", filepath.Join(dir, "blocked.txt"), "--terms-file", "terms.txt", "--allowlist", "allow.txt")
	compare(t, py, bin, dir, e, "", true, "--allowlist=allow.txt", "--path=blocked.txt")
}

func TestScrubParityUnicode(t *testing.T) {
	py, bin := needPython(t, "ci/scrub_scan.py"), goBin(t, "ci-scrub-scan")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "u.txt"), unicodeText)
	g := compare(t, py, bin, dir, e, "", true, "--path", "u.txt", "--allowlist", "allow.txt")
	if g.code != 1 {
		t.Errorf("unicode fixture: want hits, got exit %d:\n%s", g.code, g.stdout)
	}
}

func TestScrubParityErrors(t *testing.T) {
	py, bin := needPython(t, "ci/scrub_scan.py"), goBin(t, "ci-scrub-scan")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "a.txt"), "x\n")
	for _, args := range [][]string{
		{"--path", "missing.txt", "--allowlist", "allow.txt"},
		{"--path", "a.txt", "--allowlist", "missing-allow.txt"},
		{"--path", "a.txt", "--terms-file", "missing-terms.txt", "--allowlist", "allow.txt"},
		{"--path", "a.txt", "--exclude-ref", "x", "--allowlist", "allow.txt"},
		{"--path", "a.txt", "--show-line-hash", "--allowlist", "allow.txt"},
		{"--tree", "/nonexistent-dir-for-test", "--allowlist", "allow.txt"},
	} {
		g := compare(t, py, bin, dir, e, "", false, args...)
		if g.code != 2 {
			t.Errorf("args %v: want exit 2, got %d", args, g.code)
		}
	}
	for _, args := range [][]string{{}, {"--tree", "--path", "a.txt"}, {"--bogus"}, {"--path"}} {
		if g := run(t, dir, e, "", append([]string{bin}, args...)...); g.code != 2 {
			t.Errorf("args %v: want exit 2 (usage), got %d: %s", args, g.code, g.err)
		}
	}
	bad := filepath.Join(dir, "bad-allow.txt")
	for _, line := range []string{"a.txt:email:x\n", "a.txt:owner-term:x  # no\n", "a.txt  # nope\n", "a.txt:email:(  # bad regex\n"} {
		write(t, bad, line)
		g := run(t, dir, e, "", bin, "--path", "a.txt", "--allowlist", bad)
		if g.code != 2 {
			t.Errorf("allowlist %q: want exit 2, got %d: %s", line, g.code, g.err)
		}
	}
}

func initRepo(t *testing.T, e []string) string {
	t.Helper()
	repo := t.TempDir()
	git(t, repo, e, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "README.md"), "hello\nmail carol\x40corp.test\n")
	write(t, filepath.Join(repo, ".env"), "KEY=1\n")
	write(t, filepath.Join(repo, ".env.example"), "KEY=\n")
	write(t, filepath.Join(repo, "dir", "Zorbulonic notes.txt"), "line one\nZorbulonic in content\n")
	write(t, filepath.Join(repo, "bin.dat"), "a\x00b Zorbulonic")
	if err := os.Symlink("README.md", filepath.Join(repo, "link.md")); err != nil {
		t.Fatal(err)
	}
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", "first commit mentioning Zorbulonic\n\nbody 10\x2e1\x2e2\x2e3 here")
	write(t, filepath.Join(repo, "README.md"), "hello\nmail carol\x40corp.test\nsecr\x65t = abcdefghij1234567890zz\n")
	git(t, repo, e, "add", "-A")
	git(t, repo, append(e, "GIT_AUTHOR_NAME=Zorbulonic Person"), "commit", "-q", "-m", "second")
	git(t, repo, e, "tag", "-a", "v1", "-m", "release for qux")
	git(t, repo, e, "branch", "Zorbulonic-branch")
	return repo
}

func TestScrubParityTree(t *testing.T) {
	py, bin := needPython(t, "ci/scrub_scan.py"), goBin(t, "ci-scrub-scan")
	dir, e := setup(t)
	repo := initRepo(t, e)
	write(t, filepath.Join(dir, "allow.txt"), "README.md:email:carol  # synthetic\n.env:env-file:\\.env  # synthetic\nunused:email:x  # unused\n")
	g := compare(t, py, bin, dir, e, "", true, "--tree", repo, "--allowlist", "allow.txt")
	if g.code != 1 || !strings.Contains(g.stdout, "owner-term#") {
		t.Errorf("tree: want hits with owner-term labels, got %d:\n%s", g.code, g.stdout)
	}
	compare(t, py, bin, repo, e, "", true, "--tree", "--allowlist", filepath.Join(dir, "allow.txt"))
}

func TestScrubParityHistory(t *testing.T) {
	py, bin := needPython(t, "ci/scrub_scan.py"), goBin(t, "ci-scrub-scan")
	dir, e := setup(t)
	repo := initRepo(t, e)
	g := compare(t, py, bin, dir, e, "", true, "--history", repo)
	if g.code != 1 || !strings.Contains(g.stdout, "(path name)") || strings.Contains(g.stdout, "Zorbulonic") {
		t.Errorf("history: want hits, labels only, got %d:\n%s", g.code, g.stdout)
	}
	h := compare(t, py, bin, dir, e, "", true, "--history", repo, "--show-line-hash")
	// an allowlist built from the printed hashes suppresses exactly those blob lines
	var entries []string
	for _, ln := range strings.Split(h.stdout, "\n") {
		f := strings.Fields(ln)
		if len(f) >= 4 && f[0] == "email" && strings.HasPrefix(f[len(f)-1], "sha256=") {
			path := f[1][strings.Index(f[1], ":")+1:]
			entries = append(entries, path+":email:"+strings.TrimPrefix(f[len(f)-1], "sha256=")+"  # synthetic")
		}
	}
	if len(entries) == 0 {
		t.Fatalf("no email blob hit with a line hash in:\n%s", h.stdout)
	}
	entries = append(entries, "gone.txt:email:"+strings.Repeat("a", 64)+"  # unused")
	write(t, filepath.Join(dir, "hist-allow.txt"), strings.Join(entries, "\n")+"\n")
	compare(t, py, bin, dir, e, "", true, "--history", repo, "--history-allowlist", filepath.Join(dir, "hist-allow.txt"))
	compare(t, py, bin, dir, e, "", true, "--history", repo, "--exclude-ref", "refs/tags/*", "refs/heads/Zor*")
	bad := filepath.Join(dir, "bad-hist.txt")
	for _, line := range []string{"a:email:xyz  # r\n", "a:owner-term:" + strings.Repeat("a", 64) + "  # r\n", "a:nope:" + strings.Repeat("a", 64) + "  # r\n"} {
		write(t, bad, line)
		if r := compare(t, py, bin, dir, e, "", false, "--history", repo, "--history-allowlist", bad); r.code != 2 {
			t.Errorf("history allowlist %q: want exit 2, got %d", line, r.code)
		}
	}
	// a directory that is no repository fails the same way (exit 2)
	if r := compare(t, py, bin, dir, e, "", false, "--history", t.TempDir()); r.code != 2 {
		t.Errorf("history outside a repo: want exit 2, got %d", r.code)
	}
}
