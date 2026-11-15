package main

import (
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// blockedText: a synthetic owner term, a forbidden public phrase shape (private host, address) and secrets, all made up.
const blockedText = "fine line\n" +
	"owner Zorbulonic here and qux alone\n" +
	"host build01\x2ecorp\x2elan and 8\x2e8\x2e4\x2e4 and 10\x2e0\x2e0\x2e7\n" +
	"doc address 192\x2e0\x2e2\x2e5 and 127\x2e0\x2e0\x2e1 are allowed\n" +
	"path /ho\x6de/alice/x/ and mail dave\x40corp.test\n" +
	"token gh\x70_" + "0123456789abcdefghijklmnopqrstuvwxyzAB\n"

const cleanText = "plain words\nversion 1.2.3 and PR #309 and T-0515\nuser\x40example.com\n"

// unicodeText: NBSP, \v and U+0663 where Python's \s, \d and \w differ from RE2's.
const unicodeText = "build\u00a001\x2ecorp\x2elan and host\u0663\x2einternal\n" +
	"addr \u0661.\u0662.\u0663.\u0664 and 10.\u0663.1.1\n" +
	"/ho\x6de/a\u00a0b/x/ /ho\x6de/c\vd/y/\n" +
	"Zorbulonic\u00a0qux\u0663 qux\u00a0 \u0663qux\n"

func setup(t *testing.T) (dir string, environ []string) {
	t.Helper()
	dir = t.TempDir()
	write(t, filepath.Join(dir, "terms.txt"), syntheticTerms)
	write(t, filepath.Join(dir, "allow.txt"), "")
	return dir, env(t, "SCRUB_TERMS_FILE="+filepath.Join(dir, "terms.txt"))
}

func TestPublicTextParityText(t *testing.T) {
	py, bin := needPython(t, "ci/public_text_guard.py"), goBin(t, "ci-public-text-guard")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "blocked.txt"), blockedText)
	write(t, filepath.Join(dir, "clean.txt"), cleanText)
	g := compare(t, py, bin, dir, e, "", true, "text", "blocked.txt")
	if g.code != 1 || !strings.Contains(g.stdout, "owner-term#") || !strings.Contains(g.stdout, "ipv4 ") ||
		!strings.Contains(g.stdout, "private-host ") || strings.Contains(g.stdout, "Zorbulonic") {
		t.Errorf("blocked fixture: want exit 1 with masked hits, got %d:\n%s", g.code, g.stdout)
	}
	if g := compare(t, py, bin, dir, e, "", true, "text", "clean.txt"); g.code != 0 {
		t.Errorf("clean fixture: want exit 0, got %d:\n%s", g.code, g.stdout)
	}
	compare(t, py, bin, dir, e, blockedText, true, "text")
	compare(t, py, bin, dir, e, cleanText, true, "text", "-")
	compare(t, py, bin, dir, e, "", true, "--terms-file", "terms.txt", "text", "clean.txt")
	compare(t, py, bin, dir, e, blockedText, true, "--no-terms", "text")
	compare(t, py, bin, dir, e, blockedText, true, "text", "--no-terms", "--terms-file", "terms.txt")
}

func TestPublicTextParityUnicode(t *testing.T) {
	py, bin := needPython(t, "ci/public_text_guard.py"), goBin(t, "ci-public-text-guard")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "u.txt"), unicodeText)
	if g := compare(t, py, bin, dir, e, "", true, "text", "u.txt"); g.code != 1 {
		t.Errorf("unicode fixture: want hits, got exit %d:\n%s", g.code, g.stdout)
	}
	compare(t, py, bin, dir, e, "", true, "ref", "feature/Zorbulonic\u00a0x", "build\u00a001\x2ecorp\x2elan", "ok-branch")
}

func TestPublicTextParityRef(t *testing.T) {
	py, bin := needPython(t, "ci/public_text_guard.py"), goBin(t, "ci-public-text-guard")
	dir, e := setup(t)
	g := compare(t, py, bin, dir, e, "", true, "ref", "factory/t-1485", "feature/zorbulonic-x", "host\x2elan-branch", "qux")
	if g.code != 1 || strings.Contains(strings.ToLower(g.stdout), "zorbulonic") {
		t.Errorf("ref: want exit 1, labels only, got %d:\n%s", g.code, g.stdout)
	}
	if g := compare(t, py, bin, dir, e, "", true, "ref", "factory/t-1485", "main"); g.code != 0 {
		t.Errorf("clean refs: want exit 0, got %d", g.code)
	}
}

func TestPublicTextParityErrors(t *testing.T) {
	py, bin := needPython(t, "ci/public_text_guard.py"), goBin(t, "ci-public-text-guard")
	dir, e := setup(t)
	write(t, filepath.Join(dir, "clean.txt"), cleanText)
	noTerms := env(t)
	// no owner terms loaded: fail closed (exit 2) unless --no-terms
	g := compare(t, py, bin, dir, noTerms, "", true, "text", "clean.txt")
	if g.code != 2 || !strings.Contains(g.err, "public-text-guard: no owner terms loaded") {
		t.Errorf("no terms: want exit 2 and the fail-closed message, got %d: %s", g.code, g.err)
	}
	if g := compare(t, py, bin, dir, noTerms, "", true, "--no-terms", "text", "clean.txt"); g.code != 0 {
		t.Errorf("--no-terms: want exit 0, got %d", g.code)
	}
	for _, args := range [][]string{
		{"text", "missing.txt"},
		{"--terms-file", "missing-terms.txt", "text", "clean.txt"},
		{"push", "--repo", "/nonexistent-dir-for-test", "--allowlist", "allow.txt", "a", "b"},
		{"push", "--allowlist", "missing-allow.txt", "a", "b"},
		{"push", "--allowlist", "allow.txt", "--", "a"},
	} {
		if g := compare(t, py, bin, dir, e, "", false, args...); g.code != 2 {
			t.Errorf("args %v: want exit 2, got %d: %s", args, g.code, g.err)
		}
	}
	for _, args := range [][]string{{}, {"nomode"}, {"ref"}, {"push", "onlyone"}, {"text", "a", "b"}, {"--bogus", "text"}} {
		if g := run(t, dir, e, "", append([]string{bin}, args...)...); g.code != 2 {
			t.Errorf("args %v: want exit 2 (usage), got %d: %s", args, g.code, g.err)
		}
	}
}

// pngWith is a well-formed PNG: a 1x1 image with one tEXt chunk holding text.
func pngWith(text string) []byte {
	chunk := func(name string, data []byte) []byte {
		b := make([]byte, 4, 12+len(data))
		binary.BigEndian.PutUint32(b, uint32(len(data)))
		b = append(b, name...)
		b = append(b, data...)
		return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(append([]byte(name), data...)))
	}
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", []byte{0, 0, 0, 1, 0, 0, 0, 1, 8, 0, 0, 0, 0})...)
	out = append(out, chunk("tEXt", []byte("Comment\x00"+text))...)
	out = append(out, chunk("IDAT", []byte{0x78, 0x9c, 0x63, 0, 0, 0, 1, 0, 1})...)
	return append(out, chunk("IEND", nil)...)
}

func TestPublicTextParityPush(t *testing.T) {
	py, bin := needPython(t, "ci/public_text_guard.py"), goBin(t, "ci-public-text-guard")
	dir, e := setup(t)
	repo := t.TempDir()
	git(t, repo, e, "init", "-q", "-b", "main")
	write(t, filepath.Join(repo, "README.md"), "base\n")
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", "base")
	base := strings.TrimSpace(git(t, repo, e, "rev-parse", "HEAD"))

	// a clean range
	write(t, filepath.Join(repo, "ok.txt"), cleanText)
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", "add clean file")
	clean := strings.TrimSpace(git(t, repo, e, "rev-parse", "HEAD"))
	if g := compare(t, py, bin, dir, e, "", true, "push", "--repo", repo, "--allowlist", filepath.Join(dir, "allow.txt"), base, clean); g.code != 0 {
		t.Errorf("clean push: want exit 0, got %d:\n%s", g.code, g.stdout)
	}

	// blocked: message, path name, added lines (one allowlisted), an image with a hit in a text chunk
	write(t, filepath.Join(repo, "Zorbulonic.txt"), blockedText)
	if err := os.WriteFile(filepath.Join(repo, "pic.png"), pngWith("made by Zorbulonic"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, repo, e, "add", "-A")
	git(t, repo, append(e, "GIT_AUTHOR_NAME=Zorbulonic Person"), "commit", "-q", "-m", "subject with 8\x2e8\x2e4\x2e4\n\nbody qux")
	git(t, repo, e, "checkout", "-q", "-b", "side", base)
	write(t, filepath.Join(repo, "side.txt"), "side branch line\n")
	git(t, repo, e, "add", "-A")
	git(t, repo, e, "commit", "-q", "-m", "side")
	git(t, repo, e, "merge", "-q", "--no-edit", "main")
	head := strings.TrimSpace(git(t, repo, e, "rev-parse", "HEAD"))
	write(t, filepath.Join(dir, "allow.txt"), "Zorbulonic.txt:email:dave  # synthetic\n")
	allow := filepath.Join(dir, "allow.txt")
	g := compare(t, py, bin, dir, e, "", true, "push", "--repo", repo, "--allowlist", allow, base, "main")
	if g.code != 1 || strings.Contains(g.stdout, "Zorbulonic") || !strings.Contains(g.stdout, "pic.png:tEXt") {
		t.Errorf("blocked push: want exit 1, labels only, image text chunk scanned, got %d:\n%s", g.code, g.stdout)
	}
	compare(t, py, bin, dir, e, "", true, "push", "--repo", repo, "--allowlist", allow, clean, head)
	compare(t, py, bin, dir, e, "", true, "push", "--allowlist", allow, "--repo", repo, base, head)
	// a revision that looks like an option, and a range that does not exist, are usage errors
	for _, args := range [][]string{{"-x", "main"}, {"nonexistent-rev", "main"}} {
		a := append([]string{"push", "--repo", repo, "--allowlist", allow}, args...)
		if r := compare(t, py, bin, dir, e, "", false, a...); r.code != 2 {
			t.Errorf("push %v: want exit 2, got %d", args, r.code)
		}
	}
}
