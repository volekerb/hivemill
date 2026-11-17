package tick

import (
	"math"
	"strings"
	"testing"
)

// Python parity tables for the helpers of units 5-8. Every expected value is the output of a one-off `python3 -I` run
// of the Python code (re.sub, _repo_slug, repr, float, re.search), see the PR body. The inputs hold NBSP (U+00A0), \v,
// \x1c, \x85 and the Arabic-Indic digit U+0663, where Go's \s and \d differ from Python's.

func TestUnicodeStripCreds(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"https://tok" + atGitHub + "/o/r.git", "https://github.com/o/r.git"},
		{"https://a:b@h/x", "https://h/x"},
		{"x://@y", "x://y"},
		{"https://u\u00a0v@h/p", "https://u\u00a0v@h/p"}, // NBSP is whitespace to Python \s: the class stops there
		{"https://u\x0bv@h/p", "https://u\x0bv@h/p"},     // \v too
		{"https://u\x1cv@h", "https://u\x1cv@h"},
		{"https://u\u0085v@h", "https://u\u0085v@h"},
		{"https://\u0663@h", "https://h"}, // a non-ASCII digit is an ordinary character
		{"a://b://c@d", "a://b://d"},
		{"ssh://u@h/p@q", "ssh://h/p@q"},
		{"plain", "plain"},
		{"://x@y", "://y"},
		{"https://a@b@c", "https://b@c"},
		{"https://a@b://c@d", "https://b://d"},
	} {
		if got := stripCreds(c.in); got != c.want {
			t.Errorf("stripCreds(%q) = %q, Python %q", c.in, got, c.want)
		}
	}
}

func TestUnicodeRepoSlug(t *testing.T) {
	for _, c := range []struct {
		in, want string
	}{
		{"https://github.com/Acme/Hive.GIT", "acme/hive"},
		{"git" + atGitHub + ":Acme/Hive.git", "acme/hive"},
		{"\u00a0acme/hive\u00a0", "acme/hive"}, // str.strip() removes NBSP
		{"\x0bacme/hive\x0b", "acme/hive"},
		{"\u0663x/\u0663y", "\u0663x/\u0663y"},
		{"acme/hive?x=1", "acme/hive"},
		{"a/..", ""},
		{"/a/b/", "a/b"},
		{"\u00c9COLE/R\u00e9.git", "\u00e9cole/r\u00e9"},
		{"acme/hive#frag", "acme/hive"},
		{"https://h/x/y/z.git/", "y/z"},
		{"\x1cacme/hive\x1c", "acme/hive"},
	} {
		got, ok := repoSlug(c.in)
		if !ok || got != c.want {
			t.Errorf("repoSlug(%q) = %q, %v; Python %q", c.in, got, ok, c.want)
		}
	}
	if _, ok := repoSlug("a/b\nc"); ok {
		t.Error("a line break inside must be refused, not read differently from Python's regexps")
	}
}

func TestUnicodePyRepr(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"it's", `"it's"`},
		{"a\"b'c", `'a"b\'c'`},
		{"tab\t\n\\", `'tab\t\n\\'`},
		{"\x00\x7f", `'\x00\x7f'`},
		{"é\u00a0\u2028", `'é\xa0\u2028'`}, // NBSP and the line separator are not printable
		{"\U0001F600\U000e0001", `'😀\U000e0001'`},
		{"'\"", `'\'"'`},
	} {
		if got := pyRepr(c.in); got != c.want {
			t.Errorf("pyRepr(%q) = %s, Python %s", c.in, got, c.want)
		}
	}
}

func TestUnicodePyFloat(t *testing.T) {
	for _, c := range []struct {
		in   string
		want float64
		ok   bool
	}{
		{"1", 1, true}, {"1.5", 1.5, true}, {" 2 ", 2, true}, {"1e3", 1000, true}, {"1_0", 10, true},
		{"_1", 0, false}, {"1__0", 0, false}, {"inf", math.Inf(1), true}, {"0x10", 0, false}, {"1e999", math.Inf(1), true},
		{".5", 0.5, true}, {"5.", 5, true}, {"\u0661", 1, true}, // Python float() reads U+0661 as 1
		{"\u0663.\u0665", 3.5, true}, {"", 0, false}, {"+3", 3, true}, {"1_000.5", 1000.5, true}, {"1e_3", 0, false},
		{"\u00a05\u00a0", 5, true}, {"\v7\v", 7, true}, // whitespace around the number is stripped (NBSP, \v)
	} {
		got, ok := pyFloat(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("pyFloat(%q) = %v, %v; Python %v, ok=%v", c.in, got, ok, c.want, c.ok)
		}
	}
	if f, ok := pyFloat("nan"); !ok || !math.IsNaN(f) {
		t.Errorf("nan: %v %v", f, ok)
	}
}

func TestUnicodeQATaskID(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"qa-T-12-x", "T-12"}, {"qa-xT-12", ""}, {"qa-t-7", "T-7"}, {"qa-T-", ""}, {"qa-aT-3-T-5", "T-5"},
		{"qa-T-\u0663", "T-\u0663"}, {"qa-1-T-44", "T-44"}, {"qa-T-12T-13", "T-12"}, {"qa-_T-9", "T-9"}, {"qa-\u00e9T-9", "T-9"},
	} {
		if got := qaTaskID(c.in); got != c.want {
			t.Errorf("qaTaskID(%q) = %q, Python %q", c.in, got, c.want)
		}
	}
}

func TestPyRound1(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{0.25, 0.2}, {0.35, 0.3}, {2.5, 2.5}, {0.05, 0.1}, {0.15, 0.1}, {1234.56, 1234.6}} {
		if got := pyRound1(c.in); got != c.want {
			t.Errorf("round(%v, 1) = %v, Python %v", c.in, got, c.want)
		}
	}
}

func TestProtectionFromJSON(t *testing.T) {
	for _, c := range []struct {
		in, want, err string
	}{
		{`{"enforce_admins": {"enabled": true}, "required_pull_request_reviews": {}}`, "pull requests are not required", ""},
		{`{"enforce_admins": {"enabled": true}, "required_pull_request_reviews": {"a": 1}}`, "", ""},
		{`{"enforce_admins": null, "required_pull_request_reviews": {"a": 1}}`, "enforce_admins is off (admins, including the owner's login, can push to main)", ""},
		{`{}`, "enforce_admins is off (admins, including the owner's login, can push to main); pull requests are not required", ""},
		{``, "", "Expecting value: line 1 column 1 (char 0)"},
		{"  \n", "", "Expecting value: line 2 column 1 (char 3)"},
		{`{"a": 1} x`, "", "Extra data: line 1 column 10 (char 9)"},
		{`nope`, "", "Expecting value: line 1 column 1 (char 0)"},
	} {
		got, err := protectionFromJSON(c.in)
		if c.err != "" {
			if err == nil || err.Error() != c.err || !isCaught(err) {
				t.Errorf("protectionFromJSON(%q) error %v, want caught %q", c.in, err, c.err)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("protectionFromJSON(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	// a non-object answer is Python's AttributeError: not caught, the unit fails
	if _, err := protectionFromJSON(`[1]`); err == nil || isCaught(err) {
		t.Errorf("a JSON list must be an uncaught error, got %v", err)
	}
}

// --- fuzz: every parser of the alert units must not panic -----------------------------------------------------------

func FuzzStripCreds(f *testing.F) {
	for _, s := range []string{"https://tok" + atGitHub + "/o/r.git", "a://b://c@d", "https://u\u00a0v@h", "://@", "\xff://\xfe@", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got := stripCreds(s); len(got) > len(s) || (!strings.Contains(s, "://") && got != s) {
			t.Fatalf("stripCreds(%q) = %q", s, got)
		}
	})
}

func FuzzRepoSlug(f *testing.F) {
	for _, s := range []string{"https://github.com/Acme/Hive.GIT", "git" + atGitHub + ":a/b.git", "\u00a0a/b\u00a0", "a/..", "/", "", "a/b\nc"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if slug, ok := repoSlug(s); ok && slug != "" && (!strings.Contains(slug, "/") || slug != strings.ToLower(slug)) {
			t.Fatalf("repoSlug(%q) = %q", s, slug)
		}
	})
}

func FuzzQATaskID(f *testing.F) {
	for _, s := range []string{"qa-T-12-x", "qa-t-7", "T-", "qa-T-\u0663", "\xffT-1"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if got := qaTaskID(s); got != "" && !strings.HasPrefix(got, "T-") {
			t.Fatalf("qaTaskID(%q) = %q", s, got)
		}
	})
}

func FuzzPyFloat(f *testing.F) {
	for _, s := range []string{"1", "1_0", "inf", "nan", "0x10", "\u0663", "1e999", "", " 5 ", "-.5e-3"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { pyFloat(s) })
}

func FuzzPyRepr(f *testing.F) {
	for _, s := range []string{"it's", "a\"b'c", "\x00", "\U0001F600", "\xff", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		got := pyRepr(s)
		if len(got) < 2 || got[0] != got[len(got)-1] || (got[0] != '\'' && got[0] != '"') {
			t.Fatalf("pyRepr(%q) = %q", s, got)
		}
	})
}

func FuzzProtectionJSON(f *testing.F) {
	for _, s := range []string{`{"enforce_admins": {"enabled": true}}`, `{}`, `[`, ``, `{"enforce_admins": 5}`, "\x00", `{"a":1} x`} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { protectionFromJSON(s) })
}

// atGitHub is the userinfo separator and host of the URLs in these tables, kept apart from the user part so no line of
// the tests reads as an e-mail address.
const atGitHub = "@github.com"
