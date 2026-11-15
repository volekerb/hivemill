package guard

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// Tests of the CI tool core (export_ci*.go). The expected values of TestCIUnicode are the output of
// ci/scrub_scan.py's Scanner (Python 3, Unicode str patterns) for the same lines; the CLI parity tests under
// go/cmd/ci-* run the Python originals against the Go tools.

var ciFixtureLines = []string{
	"ok line", "owner Zorbulonic here", "the word qux alone, but quxx is fine", "path /ho\x6de/alice/project/file",
	"lan 192\x2e168\x2e1\x2e20 and cgnat 100\x2e64\x2e3\x2e4", "mail bob.builder\x40corp.test", "user\x40example.com",
	"toke\x6e gh\x70_0123456789abcdefghijklmnopqrstuvwxyzAB", "api\x5fkey = \"abcdEFGH12345678abcdEFGH\"",
	"\x2d----BEGIN RSA PRIVATE KEY-----", "host build01\x2ecorp\x2elan and 8\x2e8\x2e4\x2e4 and 10\x2e0\x2e0\x2e7", "192\x2e0\x2e2\x2e5 127\x2e0\x2e0\x2e1",
	"/ho\x6de/a\u00a0b/x/", "/ho\x6de/c\vd/y/", "10.\u0663.1.1", "192.168.\u0661.5", "qux\u0663", "\u0663qux", "qux\u00a0",
	"secr\x65t = abcdefghijklmnop\u00a0qrstuvwxyz1234", "i\u0307 \u0130 Zorbul\u0130nic ZORBULONIC", "",
}

func TestCIUnicode(t *testing.T) {
	plain := newCIScanner(nil, false)
	termed := newCIScanner([]string{"qux"}, false)
	cases := []struct {
		sc   *ciScanner
		line string
		want string
	}{
		{plain, "/ho\x6de/a\u00a0b/x/", "[]"},
		{plain, "/ho\x6de/c\vd/y/", "[]"},
		{plain, "/ho\x6de/alice/x/", "[{home-path /hom***}]"},
		{plain, "10.\u0663.1.1", "[{rfc1918 10.\u0663***}]"},
		{plain, "192.168.\u0661.5", "[{rfc1918 192.***}]"},
		{plain, "secr\x65t = abcdefghijklmnop\u00a0qrstuvwxyz1234", "[{generic-secret secr***}]"},
		{termed, "qux\u0663", "[]"},
		{termed, "\u0663qux", "[]"},
		{termed, "qux\u00a0", "[{owner-term owner-term#1}]"},
		{termed, "a qux b", "[{owner-term owner-term#1}]"},
		{termed, "quxx", "[]"},
	}
	for _, c := range cases {
		if got := fmt.Sprint(c.sc.scanLine(c.line)); got != c.want {
			t.Errorf("scanLine(%q) = %s, want %s (Python's result)", c.line, got, c.want)
		}
	}
}

// The span view and the counting view of scrub.go are one engine: their hit lists must agree on every line.
func TestCISpansAgreeWithGuardCounts(t *testing.T) {
	terms := []string{"Zorbulonic", "qux"}
	ci, gd := newCIScanner(terms, true), newScrubScanner(terms)
	for _, line := range ciFixtureLines {
		var ids []string
		gd.scanLine(line, &ids)
		var got []string
		for _, m := range ci.matches(line) {
			got = append(got, m.id)
		}
		if a, b := strings.Join(ciSortedIDs(ids), ","), strings.Join(ciSortedIDs(got), ","); a != b {
			t.Errorf("line %q: guard ids %s, ci ids %s", line, a, b)
		}
	}
}

func TestCIMaskAndHide(t *testing.T) {
	sc := newCIScanner([]string{"Zorbulonic", "qux"}, true)
	for in, want := range map[string]string{"": "***", "a": "***", "abcd": "ab***", "abcdefgh": "abcd***", "\u0663\u0663\u0663\u0663\u0663": "\u0663\u0663***"} {
		if got := ciMask(in); got != want {
			t.Errorf("ciMask(%q) = %q, want %q", in, got, want)
		}
	}
	if got := sc.hideTerms("x ZORBULONIC y qux z"); got != "x owner-term#1 y owner-term#2 z" {
		t.Errorf("hideTerms = %q", got)
	}
	if got := sc.hideAll("dir/Zorbulonic/a\x40corp.test"); strings.Contains(got, "Zorbulonic") || !strings.Contains(got, "owner-term#1") {
		t.Errorf("hideAll leaks or misses the term: %q", got)
	}
	if got := sc.shown([]byte("a\x01b\x7f")); got != "a\\x01b\\x7f" {
		t.Errorf("shown = %q", got)
	}
}

func TestCITermsFile(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(f, []byte("  Beta \n\nalpha\nBeta\r\nalpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	terms, err := ciLoadTerms([]string{f})
	if err != nil || fmt.Sprint(terms) != "[alpha Beta]" {
		t.Errorf("ciLoadTerms = %v, %v; want [alpha Beta] (stripped, unique, sorted by lower case)", terms, err)
	}
	if _, err := ciLoadTerms([]string{filepath.Join(dir, "missing")}); err == nil || strings.Contains(err.Error(), "alpha") {
		t.Errorf("a missing terms file must be a usage error naming the path: %v", err)
	}
	t.Setenv(termsVar, "")
	if _, err := ciTermsOrFail("", nil); err == nil || !strings.HasPrefix(err.Error(), noTermsMsg) {
		t.Errorf("no terms must fail closed with %q: %v", noTermsMsg, err)
	}
	t.Setenv(termsVar, f)
	if got, err := ciTermsOrFail("", nil); err != nil || len(got) != 2 {
		t.Errorf("terms from the environment variable: %v, %v", got, err)
	}
}

func TestCIAllowlist(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "allow.txt")
	body := "a.txt:email:bob  # reviewed\nb.txt:home-path  # legacy\n# comment\n\n"
	if err := os.WriteFile(f, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	var warn strings.Builder
	al, err := ciLoadAllowlist(f, &warn)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(warn.String(), "legacy 'path:regex' allowlist entry is deprecated") {
		t.Errorf("the legacy form must warn: %q", warn.String())
	}
	if !al.allows([]string{"a.txt"}, "mail bob\x40corp.test", "email") || al.allows([]string{"a.txt"}, "mail bob\x40corp.test", "ipv4") ||
		al.allows([]string{"a.txt"}, "mail bob\x40corp.test", idOwnerTerm) || al.allows([]string{"c.txt"}, "mail bob\x40corp.test", "email") {
		t.Errorf("allows must match path, regex and pattern id, and never an owner term")
	}
	if u := al.unused(); len(u) != 1 || u[0].path != "b.txt" {
		t.Errorf("unused = %v, want only b.txt", u)
	}
	for _, bad := range []string{"a:email:x\n", "a:owner-term:x  # r\n", "a  # r\n", "a:email:(  # r\n", "a:email:  # r\n"} {
		if err := os.WriteFile(f, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := ciLoadAllowlist(f, io.Discard); err == nil {
			t.Errorf("allowlist %q must be refused", bad)
		}
	}
}

func TestCIParseArgs(t *testing.T) {
	spec := ciArgSpec{"--one": ciArgOne, "--opt": ciArgOpt, "--many": ciArgMany, "--flag": ciArgFlag}
	p, _, err := ciParseArgs([]string{"--opt", "--many", "a", "b", "--one=x", "--opt", "d", "--flag", "pos"}, spec, false)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := p.last("--one"); v != "x" || fmt.Sprint(p.all("--many")) != "[a b]" || fmt.Sprint(p.occ["--opt"]) != "[[] [d]]" ||
		!p.has("--flag") || fmt.Sprint(p.pos) != "[pos]" {
		t.Errorf("parsed %+v", p)
	}
	for _, bad := range [][]string{{"--nope"}, {"--one"}, {"--many"}, {"--flag=1"}, {"--on", "x"}} {
		if _, _, err := ciParseArgs(bad, spec, false); err == nil {
			t.Errorf("args %v must be refused (no abbreviations, values required)", bad)
		}
	}
}

func TestCIRepoSlug(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/Acme/Widgets.git": "acme/widgets", "git\x40github.com:acme/widgets.git": "acme/widgets",
		"/srv/git/Acme/Widgets.GIT": "acme/widgets", "https://github.com/a/b?x=1#f": "a/b", "nonsense": "", "x/..": "", "": "",
	} {
		got, _ := CIRepoSlug(in)
		if got != want {
			t.Errorf("CIRepoSlug(%q) = %q, want %q", in, got, want)
		}
	}
}

func FuzzCIScanLine(f *testing.F) {
	for _, l := range ciFixtureLines {
		f.Add(l)
	}
	sc := newCIScanner([]string{"Zorbulonic", "qux", "\u0130x"}, true)
	f.Fuzz(func(t *testing.T, line string) {
		sc.scanLine(line)
		sc.hideAll(line)
		sc.hideTerms(line)
		sc.shown([]byte(line))
	})
}

func FuzzCILoadAllowlist(f *testing.F) {
	for _, s := range []string{"a.txt:email:bob  # r\n", "b:home-path  # legacy\n", "x:owner-term:y  # r\n", "a:email:(  # r\n", "\xff\xfe", ""} {
		f.Add([]byte(s))
	}
	dir := f.TempDir()
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(dir, "a.txt")
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if al, err := ciLoadAllowlist(p, io.Discard); err == nil {
			al.allows([]string{"a.txt"}, "line bob\x40corp.test", "email")
			al.unused()
		}
		if h, err := ciLoadHistoryAllowlist(p); err == nil {
			h.allows("a", "email", strings.Repeat("a", 64))
			h.unused()
		}
	})
}

func FuzzCIParseArgs(f *testing.F) {
	f.Add("--tree\x00--path\x00a")
	f.Add("text\x00--terms-file\x00x\x00y")
	f.Add("push\x00--repo=r\x00a\x00b")
	f.Add("--exclude-ref\x00--history")
	f.Fuzz(func(t *testing.T, joined string) {
		args := strings.Split(joined, "\x00")
		if _, _, err := ciParseArgs(args, ciArgSpec(ciScrubSpec), false); err == nil {
			t.Log("accepted")
		}
		if _, err := ciPTGParse(args); err == nil {
			t.Log("accepted")
		}
	})
}

func FuzzCIRepoSlug(f *testing.F) {
	for _, s := range []string{"https://github.com/Acme/Widgets.git", "git\x40github.com:a/b.git", "a/b.git\n/", "///a///b///", "", " x \n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, url string) { CIRepoSlug(url) })
}

func FuzzCIHistoryLines(f *testing.F) {
	const mark = "\x01deadbeef\x01"
	f.Add(mark + "Habc\n" + mark + "AName <a\x40corp.test>\n" + mark + "CName <c\x40example.com>\n" + mark + "M\nsubject 10\x2e1\x2e2\x2e3\n" + mark + "E\n" +
		"diff --git a/f.txt b/f.txt\n--- a/f.txt\n+++ b/f.txt\n\x40\x40 -0,0 +1 \x40\x40\n+secr\x65t = abcdefghij1234567890zz\n")
	f.Add("diff --git \"a/q\\303\\251\" \"b/q\\303\\251\"\n\x40\x40\n-x")
	sc := newCIScanner([]string{"Zorbulonic"}, false)
	f.Fuzz(func(t *testing.T, s string) {
		_ = ciHistoryLines(bufio.NewReader(strings.NewReader(s)), mark, sc, func(ciHistoryHit) {}) // a malformed header is an error, never a panic
	})
}

func ciSortedIDs(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}

// hangingGit puts a fake git on PATH that never answers.
func hangingGit(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\nsleep 30\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCIPushGitTimeout(t *testing.T) {
	hangingGit(t)
	t.Setenv("FACTORY_PT_GIT_TIMEOUT", "0.3")
	t.Setenv(termsVar, "")
	dir := t.TempDir()
	terms := filepath.Join(dir, "t.txt")
	allow := filepath.Join(dir, "a.txt")
	for _, f := range []string{terms, allow} {
		if err := os.WriteFile(f, []byte("qux\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(allow, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	var out, errb strings.Builder
	start := time.Now()
	code := CIPublicTextGuardMain([]string{"--terms-file", terms, "push", "--allowlist", allow, "--repo", dir, "a", "b"}, strings.NewReader(""), &out, &errb)
	if code != 2 || !strings.Contains(errb.String(), "did not finish in 0.3s") {
		t.Errorf("a stalled git must fail closed with exit 2: code %d, stderr %q", code, errb.String())
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("the stalled git was not cut off by the timeout")
	}
}

func TestCIPrePushGitTimeout(t *testing.T) {
	hangingGit(t)
	old := ciPrePushGitTimeout
	ciPrePushGitTimeout = 300 * time.Millisecond
	t.Cleanup(func() { ciPrePushGitTimeout = old })
	dir := t.TempDir()
	terms := filepath.Join(dir, "t.txt")
	if err := os.WriteFile(terms, []byte("qux\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	read := func(string) (map[string]string, error) {
		return map[string]string{"public_text_repos": "acme/widgets", "scrub_terms_file": terms}, nil
	}
	sha := strings.Repeat("a", 40)
	var errb strings.Builder
	code := CIPrePushPublicTextMain([]string{"--home", dir, "origin", "https://github.com/acme/widgets"},
		strings.NewReader("refs/heads/f "+sha+" refs/heads/f "+sha+"\n"), &errb, read)
	if code != 1 || !strings.Contains(errb.String(), "did not finish in") {
		t.Errorf("a stalled git must refuse the push: code %d, stderr %q", code, errb.String())
	}
}
