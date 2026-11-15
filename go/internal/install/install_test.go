package install

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const leakText = "gh" + "p_abcdefghijklmnopqrstuvwxyzABCDEFGHIJ" // a classic GitHub token shape: gh + p_ + 36 letters

func runMain(t *testing.T, f *fakeHost, argv ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	rc := Main(argv, f, &out, &errb)
	return rc, out.String(), errb.String()
}

// ---------------------------------------------------------------------------------------------- arguments

func TestHelpListsFlags(t *testing.T) {
	rc, out, _ := runMain(t, newFake(t), "--help")
	if rc != 0 {
		t.Fatalf("rc=%d", rc)
	}
	for _, flag := range []string{"--check", "--dry-run", "--yes", "--ref", "--repo-url", "--project-id", "--project-repo"} {
		if !strings.Contains(out, flag) {
			t.Errorf("--help does not list %s", flag)
		}
	}
}

func TestUsageErrorsExitTwoAndNeverEchoValues(t *testing.T) {
	cases := [][]string{
		{},                                    // no mode
		{"--yes", "--dry-run"},                // two modes
		{"--dry-run", "--gh-token", leakText}, // unknown option with a token
		{"--dry-run", "--port", leakText},     // bad value
		{"--dry-run", "--port", "0"},
		{"--dry-run", "--port", "65536"},
		{"--dry-run", "--max-agents", "0"},
		{"--dry-run", "--ref", "-x"},
		{"--dry-run", "--repo-url", "-x"},
		{"--dry-run", "--project-id", "p"},
		{"--dry-run", "--project-repo", "/x"},
		{"--dry-run", "--project-name", "n"},
		{"--dry-run", "--factory-url", "http://x"},
		{"--check", "--herdr-app"},
		{"--dry-run", "--herdr-app", "--orgs", "bogus"},
		{"--dry-run", "--herdr-app", "--factory-url", "ftp://x"},
		{"--dry-run", "--herdr-app", "--factory-machine", "bad name"},
		{"--dry-run", "extra"},
		{"--dry-run", "--yes=1"},
		{"--dry-run", "--project-id"},
		{"--dry-run", "--project-id", "-x", "--project-repo", "/x"},
	}
	for _, argv := range cases {
		rc, out, errb := runMain(t, newFake(t), argv...)
		if rc != 2 {
			t.Errorf("%v: rc=%d, want 2 (out=%q err=%q)", argv, rc, out, errb)
		}
		if strings.Contains(errb+out, leakText) {
			t.Errorf("%v: the rejected value was echoed: %q", argv, errb)
		}
	}
}

func TestValidArgumentsAreAccepted(t *testing.T) {
	cases := [][]string{
		{"--check"},
		{"--dry-run", "--ref=v0.4.0", "--port", "9000", "--max-agents", "3", "--project-id", "p", "--project-repo", "/x",
			"--project-name", "n", "--project-gh", "a/b", "--no-systemd", "--linger", "--no-scout", "--resume"},
		{"--dry-run", "--herdr-app", "--factory-machine", "", "--orgs", "claude,cursor", "--factory-url", "http://127.0.0.1:1"},
	}
	for _, argv := range cases {
		if _, err := ParseArgs(argv); err != nil {
			t.Errorf("%v: %v", argv, err)
		}
	}
}

// Python int() accepts surrounding whitespace, underscores and non-ASCII digits: U+0663 is ARABIC-INDIC DIGIT THREE.
func TestUnicodeNumbersLikePythonInt(t *testing.T) {
	a, err := ParseArgs([]string{"--dry-run", "--port", "٣٣", "--max-agents", " 1_0 "})
	if err != nil {
		t.Fatal(err)
	}
	if a.Port != 33 || a.MaxAgents != "10" {
		t.Errorf("port=%d max=%q, want 33 and 10", a.Port, a.MaxAgents)
	}
	if _, ok := pyInt("1__0"); ok {
		t.Error("double underscore accepted")
	}
	if _, ok := pyInt("²"); ok {
		t.Error("superscript two accepted (int() rejects it)")
	}
}

// ---------------------------------------------------------------------------------------------- Unicode parity

func TestUnicodeSpaceAndDigitSemantics(t *testing.T) {
	// Python 3: "  \v\x1c\x85".isspace() -> True; "٣".isdigit() and int("٣") == 3
	for _, r := range []rune{' ', ' ', '\v', 0x1c, 0x85, 0x2003, 0x3000} {
		if !isPySpace(r) {
			t.Errorf("%U is whitespace in Python", r)
		}
	}
	if isPySpace('a') || isPySpace(0x200b) {
		t.Error("a and U+200B are not whitespace in Python")
	}
	if got := pyStrip(" \v x 　"); got != "x" {
		t.Errorf("strip = %q", got)
	}
	// Python: "a\vb\x0cc\x1dd\x85e f\r\ng\rh\n".splitlines() -> ['a','b','c','d','e','f','g','h']
	got := pySplitLines("a\vb\fc\x1dd\u0085e f\r\ng\rh\n")
	if strings.Join(got, "|") != "a|b|c|d|e|f|g|h" {
		t.Errorf("splitlines = %q", got)
	}
	if d, ok := pyDigit('٣'); !ok || d != 3 {
		t.Errorf("U+0663 = %d,%v", d, ok)
	}
	if d, ok := pyDigit(0x1d7d8); !ok || d != 0 { // MATHEMATICAL DOUBLE-STRUCK DIGIT ZERO
		t.Errorf("U+1D7D8 = %d,%v", d, ok)
	}
}

// Python: re.match(r"^v(\d+)\.(\d+)\.(\d+)$", "v1.٣.0") matches with groups ('1','٣','0'), key (1, 3, 0),
// so it beats v1.2.9. A tag split by "\v" is two fields in Python's str.split(), so the line is skipped.
func TestResolveLatestUnicode(t *testing.T) {
	out := "aaaa\trefs/tags/v1.2.9\naaaa\trefs/tags/v1.٣.0\nbbbb\trefs/tags/v1.3.0^{}\n"
	if got := resolveLatest(out); got != "v1.٣.0" {
		t.Errorf("latest = %q, want the first of the two equal keys", got)
	}
	if got := resolveLatest("aaaa refs/tags/v2.0.0\naaaa\vrefs/tags/v3.0.0\n"); got != "v2.0.0" {
		// NBSP separates fields in Python's split(); \v ends the line in splitlines(), leaving one field.
		t.Errorf("latest = %q, want v2.0.0", got)
	}
	if got := resolveLatest(testTags); got != "v0.10.0" {
		t.Errorf("latest = %q, want v0.10.0 (numeric, not text, order; rc tags never match)", got)
	}
	if got := resolveLatest("x\trefs/heads/v9.9.9\n"); got != "" {
		t.Errorf("a branch matched: %q", got)
	}
}

func TestVersionAndPythonRegexAreUnicode(t *testing.T) {
	if m := versionRe.FindStringSubmatch("hivemill 1.٣.0"); m == nil || m[1] != "1.٣.0" {
		t.Errorf("version regexp = %v", m)
	}
	if _, ok := pyAtLeast("Python 3.١١.2", 3, 11); !ok { // U+0661 U+0661 is 11
		t.Error("Python 3.<arabic 11> should count as 3.11")
	}
	if _, ok := pyAtLeast("Python 3.9.1", 3, 11); ok {
		t.Error("3.9 is older than 3.11")
	}
}

func TestHTTPURLRegexpMatchesPython(t *testing.T) {
	// Python: re.match(r"^https?://[^\s]+$", s): "$" also matches before a final newline, \s covers NBSP and \v.
	for s, want := range map[string]bool{
		"http://x": true, "https://x/y?z=1": true, "http://x\n": true,
		"http://x y": false, "http://x y": false, "http://x\vy": false, "ftp://x": false, "http://": false,
	} {
		if got := httpURLRe.MatchString(s); got != want {
			t.Errorf("%q: %v, want %v", s, got, want)
		}
	}
}

// ---------------------------------------------------------------------------------------------- redaction

func TestRedact(t *testing.T) {
	for in, want := range map[string]string{
		leakText:                          "[REDACTED]",
		"gho_abcdefghij":                  "[REDACTED]",
		"github_pat_abcdefghij12":         "[REDACTED]",
		"sk-ant-abcdefghij-12":            "[REDACTED]",
		"Authorization: Bearer abc\nnext": "[REDACTED]\nnext",
		"https://user:pw@example.com/x":   "https://[REDACTED]example.com/x",
		"https://example.com/x@y":         "https://example.com/x@y",
		"git@host":                        "git@host",
		"https://a b@c":                   "https://a b@c", // NBSP is \s in Python, so the pattern does not span it
	} {
		if got := redact(in); got != want {
			t.Errorf("redact(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestEmitRedactsEnvTokens(t *testing.T) {
	f := newFake(t)
	f.Env["GH_TOKEN"] = "plain-secret-value-123456"
	var out bytes.Buffer
	c := NewCtx(f, &Args{}, &out, true)
	c.emit("leak plain-secret-value-123456 here")
	if strings.Contains(out.String(), "plain-secret") || !strings.Contains(out.String(), "[REDACTED]") {
		t.Errorf("not redacted: %q", out.String())
	}
}

// ---------------------------------------------------------------------------------------------- check / refusals

func TestCheckAllPresentExitsZero(t *testing.T) {
	f := newFake(t)
	f.login(t)
	rc, out, _ := runMain(t, f, "--check")
	if rc != 0 {
		t.Fatalf("rc=%d out=%s", rc, out)
	}
	for _, want := range []string{"ok python3>=3.11 Python 3.13.5", "ok git ", "ok gh-auth logged in", "ok systemd-user", "ok ram 16.0 GiB", "ok cpu 8 CPU\n", "suggested-max-agents 6"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
}

func TestCheckMissingExitsThree(t *testing.T) {
	f := newFake(t)
	f.tools = map[string]bool{"git": true}
	f.systemd = false
	f.mem, f.cpus = 1.5, 1
	rc, out, _ := runMain(t, f, "--check")
	if rc != 3 {
		t.Fatalf("rc=%d", rc)
	}
	for _, want := range []string{"missing gh not found", "missing gh-auth gh is not installed", "missing herdr", "missing claude-auth", "missing systemd-user", "warn ram 1.5 GiB (below 4 GB)", "warn cpu 1 CPU (below 2)", "suggested-max-agents 2"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in\n%s", want, out)
		}
	}
	f2 := newFake(t)
	f2.login(t)
	f2.systemd = false
	if rc, out, _ := runMain(t, f2, "--check", "--no-systemd"); rc != 0 || !strings.Contains(out, "warn systemd-user") {
		t.Errorf("--no-systemd: rc=%d\n%s", rc, out)
	}
}

func TestRefusals(t *testing.T) {
	t.Run("root", func(t *testing.T) {
		f := newFake(t)
		f.euid = 0
		rc, out, _ := runMain(t, f, "--dry-run")
		if rc != 1 || !strings.Contains(out, "refusing to run as root") || strings.Contains(out, "RESULT") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("agent pane", func(t *testing.T) {
		f := newFake(t)
		f.Env["FACTORY_AGENT"] = "x"
		if rc, out, _ := runMain(t, f, "--dry-run"); rc != 1 || !strings.Contains(out, "FACTORY_AGENT is set") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("missing git", func(t *testing.T) {
		f := newFake(t)
		f.tools = map[string]bool{}
		rc, out, _ := runMain(t, f, "--dry-run")
		if rc != 3 || !strings.Contains(out, "missing: git") || !strings.Contains(out, "sudo apt-get install -y git python3") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("old python", func(t *testing.T) {
		f := newFake(t)
		f.rules = func(a []string) (int, string, bool) {
			if a[0] == "python3" {
				return 0, "Python 3.9.1\n", true
			}
			return 0, "", false
		}
		if rc, out, _ := runMain(t, f, "--dry-run"); rc != 3 || !strings.Contains(out, "python3 3.11 or newer") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("foreign clone", func(t *testing.T) {
		f := newFake(t)
		f.origin = "https://example.com/other.git"
		mustMkdir(t, filepath.Join(f.HomeDir, "factory"))
		if rc, out, _ := runMain(t, f, "--dry-run"); rc != 1 || !strings.Contains(out, "is not a clone of") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("already installed", func(t *testing.T) {
		f := newFake(t)
		mustMkdir(t, filepath.Join(f.HomeDir, "factory", "db"))
		mustWrite(t, filepath.Join(f.HomeDir, "factory", "db", "factory.db"), "x")
		if rc, out, _ := runMain(t, f, "--dry-run"); rc != 4 || !strings.Contains(out, "already installed") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("partial install", func(t *testing.T) {
		f := newFake(t)
		mustMkdir(t, filepath.Join(f.HomeDir, "factory"))
		if rc, out, _ := runMain(t, f, "--dry-run", "--repo-url", "https://github.com/VolekErb/hivemill/"); rc != 4 || !strings.Contains(out, "partial install") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("no ref found", func(t *testing.T) {
		f := newFake(t)
		f.tags = "aaaa\trefs/tags/v1.0.0-rc1\n"
		rc, out, _ := runMain(t, f, "--dry-run")
		if rc != 1 || !strings.Contains(out, "RESULT FAIL step=resolve-ref") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("no systemd session", func(t *testing.T) {
		f := newFake(t)
		f.login(t)
		f.systemd = false
		if rc, out, _ := runMain(t, f, "--yes"); rc != 3 || !strings.Contains(out, "--no-systemd") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
	t.Run("not logged in", func(t *testing.T) {
		f := newFake(t)
		f.ghAuth = false
		rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
		if rc != 3 || !strings.Contains(out, "gh auth login") || !strings.Contains(out, "/login") {
			t.Errorf("rc=%d out=%s", rc, out)
		}
	})
}

// ---------------------------------------------------------------------------------------------- dry run

func TestDryRunPrintsPlanAndChangesNothing(t *testing.T) {
	f := newFake(t)
	f.tools = map[string]bool{"git": true, "python3": true}
	rc, out, _ := runMain(t, f, "--dry-run", "--no-systemd", "--port", "9000", "--max-agents", "3", "--no-scout",
		"--project-id", "p", "--project-repo", "/x", "--project-gh", "a/b")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	home := f.HomeDir
	for _, want := range []string{
		"[plan] 1 prereqs\n", "[plan] 10 smoke\n",
		"    download " + HerdrAssets["x86_64"].URL + " -> <tmpdir>/herdr, then verify size 30285672 and sha256 14d9e8bc",
		"    install it as " + home + "/.local/bin/herdr\n",
		"    extract gh_2.103.3_linux_amd64/bin/gh from the tarball and install it as " + home + "/.local/bin/gh\n",
		"    download https://claude.ai/install.sh -> <tmpdir>/claude-install.sh (to a file, sha256 logged, never piped into a shell)\n",
		"    $ bash <tmpdir>/claude-install.sh\n",
		"    $ git clone -- " + testRepo + " " + home + "/factory.installing\n",
		"    $ git -C " + home + "/factory.installing checkout --detach refs/tags/v0.10.0\n",
		"    $ mv " + home + "/factory.installing " + home + "/factory\n",
		"    $ " + home + "/.local/bin/factory config serve_bind 127.0.0.1\n",
		"    $ " + home + "/.local/bin/factory config serve_port 9000\n",
		"    $ " + home + "/.local/bin/factory config max_live_agents 3\n",
		"    $ " + home + "/.local/bin/factory config scout_enabled 0\n",
		"    $ " + home + "/.local/bin/factory project add p --name p --repo /x --gh a/b\n",
		"    $ (background) herdr server\n",
		"WARNING: no tick timer",
		"GET http://127.0.0.1:9000/ and /goals",
		"plan only: nothing was changed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "RESULT") {
		t.Error("a plan prints no RESULT line")
	}
	if len(f.muts) != 0 || len(f.downs) != 0 || len(f.popens) != 0 {
		t.Errorf("dry run mutated: muts=%v downs=%v popens=%v", f.muts, f.downs, f.popens)
	}
	if ents, _ := os.ReadDir(home); len(ents) != 0 {
		t.Errorf("dry run created files in HOME: %v", ents)
	}
}

func TestDryRunPinnedRefAndSystemd(t *testing.T) {
	f := newFake(t)
	rc, out, _ := runMain(t, f, "--dry-run", "--ref", "abc123", "--linger")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	for _, want := range []string{"fetch origin -- abc123", "checkout --detach FETCH_HEAD", "$ bash " + f.HomeDir + "/factory/systemd/install.sh --with-serve", "$ loginctl enable-linger", "$ systemctl --user start herdr-server.service factory-tick.timer factory-serve.service factory-up.service"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q\n%s", want, out)
		}
	}
	for _, c := range f.calls {
		if c[0] == "git" && c[1] == "ls-remote" {
			t.Error("a pinned ref needs no ls-remote")
		}
	}
}

// ---------------------------------------------------------------------------------------------- a real run

func TestYesRunPasses(t *testing.T) {
	f := newFake(t)
	f.login(t)
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd", "--port", "9100", "--project-id", "p", "--project-repo", "/x")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if last := lines[len(lines)-1]; last != "RESULT PASS url=http://127.0.0.1:9100/ version=0.4.0" {
		t.Errorf("last line = %q", last)
	}
	for _, want := range []string{"[ok] 4 clone: cloned " + testRepo + " at v0.10.0", "[ok] 10 smoke: version 0.4.0; boss live, director live; UI answers 200", "[ok] 8 project: project p added"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q\n%s", want, out)
		}
	}
	link := filepath.Join(f.HomeDir, ".local", "bin", "factory")
	if tgt, err := os.Readlink(link); err != nil || tgt != filepath.Join(f.HomeDir, "factory", "bin", "factory") {
		t.Errorf("link = %q, %v", tgt, err)
	}
	if _, err := os.Stat(filepath.Join(f.HomeDir, "factory")); err != nil {
		t.Errorf("clone not moved into place: %v", err)
	}
	if len(f.popens) != 2 {
		t.Errorf("popens = %v, want herdr server and factory serve", f.popens)
	}
	if !f.mutated("config serve_port 9100") || f.mutated("config max_live_agents") {
		t.Errorf("config calls wrong: %v", f.muts)
	}
}

func TestYesRunFailsWhenInitIsSilent(t *testing.T) {
	f := newFake(t)
	f.login(t)
	f.rules = func(a []string) (int, string, bool) {
		if len(a) == 2 && a[1] == "init" {
			return 0, "nothing\n", true
		}
		return 0, "", false
	}
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 1 || !strings.Contains(out, "RESULT FAIL step=init") || !strings.Contains(out, "did not print an `initialised` line") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(f.HomeDir, "factory.installing")); err == nil {
		t.Error("the temp clone was left behind")
	}
}

func TestStepFailureRedactsTail(t *testing.T) {
	f := newFake(t)
	f.login(t)
	f.rules = func(a []string) (int, string, bool) {
		if len(a) > 2 && a[0] == "git" && a[1] == "clone" {
			return 128, "fatal: could not read from https://user:pw@example.com/x with " + leakText + "\n", true
		}
		return 0, "", false
	}
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 1 || !strings.Contains(out, "RESULT FAIL step=clone") || strings.Contains(out, leakText) || strings.Contains(out, "user:pw") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
}

func TestSmokeFailsWhenAgentsNeverLive(t *testing.T) {
	f := newFake(t)
	f.login(t)
	f.rules = func(a []string) (int, string, bool) {
		if len(a) == 2 && a[1] == "agents" {
			return 0, "NAME ORG ROLE STATUS\nboss claude boss stopped\n", true
		}
		return 0, "", false
	}
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 1 || !strings.Contains(out, "RESULT FAIL step=smoke") || !strings.Contains(out, "not live or queued after 60 s") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
	n := 0
	for _, c := range f.calls {
		if len(c) == 2 && c[1] == "agents" {
			n++
		}
	}
	if n != 30 {
		t.Errorf("polled %d times, want 30", n)
	}
}

func TestSmokeFailsWhenUIIsDown(t *testing.T) {
	f := newFake(t)
	f.login(t)
	f.httpRC["http://127.0.0.1:8765/goals"] = 500
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 1 || !strings.Contains(out, "GET http://127.0.0.1:8765/goals answered 500, expected 200") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
}

func TestResumeKeepsServeBindAndPort(t *testing.T) {
	f := newFake(t)
	f.login(t)
	mustMkdir(t, filepath.Join(f.HomeDir, "factory", "db"))
	mustWrite(t, filepath.Join(f.HomeDir, "factory", "db", "factory.db"), "x")
	f.rules = func(a []string) (int, string, bool) {
		if len(a) == 3 && a[1] == "config" && a[2] == "serve_bind" {
			return 0, "192.0.2.7\n", true
		}
		if len(a) == 3 && a[1] == "config" && a[2] == "serve_port" {
			return 0, "9444\n", true
		}
		return 0, "", false
	}
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd", "--resume")
	if rc != 0 || !strings.Contains(out, "--resume: serve_bind is already '192.0.2.7', kept") || !strings.Contains(out, "http://127.0.0.1:9444/") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
	if f.mutated("config serve_bind") {
		t.Error("serve_bind was overwritten")
	}
}

// ---------------------------------------------------------------------------------------------- pinned downloads

func TestFetchVerifiedRefusesWrongSizeAndHash(t *testing.T) {
	f := newFake(t)
	url := "https://example.test/asset"
	f.assets[url] = []byte("hello")
	c := NewCtx(f, &Args{}, &bytes.Buffer{}, false)
	c.tmp = t.TempDir()
	good := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if p, err := fetchVerified(c, "a", Asset{url, 5, good}); err != nil || p == "" {
		t.Fatalf("good asset refused: %v", err)
	}
	if _, err := fetchVerified(c, "a", Asset{url, 6, good}); err == nil || !strings.Contains(err.Error(), "size mismatch: got 5, pinned 6; nothing installed") {
		t.Errorf("size: %v", err)
	}
	if _, err := fetchVerified(c, "a", Asset{url, 5, strings.Repeat("0", 64)}); err == nil || !strings.Contains(err.Error(), "sha256 mismatch; nothing installed") {
		t.Errorf("sha: %v", err)
	}
}

func TestToolsAreInstalledOnlyWhenVerified(t *testing.T) {
	f := newFake(t)
	f.login(t)
	f.tools = map[string]bool{"git": true, "python3": true, "claude": true}
	f.assets[HerdrAssets["x86_64"].URL] = []byte("tampered")
	rc, out, _ := runMain(t, f, "--yes", "--no-systemd")
	if rc != 1 || !strings.Contains(out, "RESULT FAIL step=tools") || !strings.Contains(out, "size mismatch") {
		t.Errorf("rc=%d\n%s", rc, out)
	}
	if _, err := os.Stat(filepath.Join(f.HomeDir, ".local", "bin", "herdr")); err == nil {
		t.Error("an unverified herdr was installed")
	}
}

func TestPinnedTablesMatchTheReference(t *testing.T) {
	want := map[string]string{
		"x86_64":  "14d9e8bc8b4a9e0118d77c32266f76d461f09b40342eb23752960bd89f59929d",
		"aarch64": "17d7536f2da50859523680ef279dffcac8e4a235f4e46fe8da6c2ec44141897c",
	}
	for k, sha := range want {
		if HerdrAssets[k].SHA256 != sha {
			t.Errorf("herdr %s sha drifted", k)
		}
	}
	if !regexp.MustCompile(`^https://github\.com/cli/cli/releases/download/v2\.103\.3/gh_2\.103\.3_linux_(amd64|arm64)\.tar\.gz$`).MatchString(GHAssets["aarch64"].URL) {
		t.Error("gh url drifted")
	}
	for _, as := range []Asset{HerdrAssets["x86_64"], HerdrAssets["aarch64"], GHAssets["x86_64"], GHAssets["aarch64"]} {
		if len(as.SHA256) != 64 || as.Size <= 0 || !strings.HasPrefix(as.URL, "https://") {
			t.Errorf("bad pin %+v", as)
		}
	}
}

func TestAgentStatusParsing(t *testing.T) {
	got := agentStatus("NAME ORG ROLE STATUS\nboss claude boss live\nshort\ndirector claude director queued\n")
	if got["boss"] != "live" || got["director"] != "queued" || len(got) != 2 {
		t.Errorf("%v", got)
	}
	if len(agentStatus("A B\nx y\n")) != 0 || len(agentStatus("")) != 0 {
		t.Error("a table without NAME and STATUS has no rows")
	}
	// NBSP separates fields in Python's split(); a Go strings.Fields would keep "boss live" as one field
	if got := agentStatus("NAME STATUS\nboss live\n"); got["boss"] != "live" {
		t.Errorf("NBSP: %v", got)
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ---------------------------------------------------------------------------------------------- macOS (G-049 M1)

func newDarwin(t *testing.T) *fakeHost {
	t.Helper()
	f := newFake(t)
	f.goos, f.machine, f.memsize = "darwin", "arm64", "17179869184" // 16 GiB
	return f
}

// pinTest swaps one pin for the duration of the test; the real pins are checked against the downloads separately.
func pinTest(t *testing.T, table map[string]Asset, key string, as Asset) {
	t.Helper()
	old := table[key]
	table[key] = as
	t.Cleanup(func() { table[key] = old })
}

func zipWith(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func TestDarwinPinsAreAppleSilicon(t *testing.T) {
	h, g := HerdrAssets["darwin/aarch64"], GHAssets["darwin/aarch64"]
	if h.URL != herdrRelease+"/herdr-macos-aarch64" || h.Size != 22536176 {
		t.Errorf("pin: %+v", h)
	}
	if g.URL != ghRelease+"/gh_2.103.3_macOS_arm64.zip" {
		t.Errorf("gh pin: %+v", g)
	}
	for _, as := range []Asset{h, g} {
		if len(as.SHA256) != 64 || as.Size <= 0 {
			t.Errorf("incomplete pin %+v", as)
		}
	}
}

func TestDarwinIntelAndOtherOSRefused(t *testing.T) {
	for _, tc := range []struct{ goos, machine, want string }{
		{"darwin", "x86_64", "hivemill-install: no pinned release for darwin/x86_64 (Apple silicon only on macOS)"},
		{"freebsd", "x86_64", "hivemill-install: no pinned release for freebsd/x86_64 (Apple silicon only on macOS)"},
	} {
		for _, mode := range []string{"--dry-run", "--check"} {
			f := newFake(t)
			f.goos, f.machine = tc.goos, tc.machine
			rc, out, errw := runMain(t, f, mode)
			if rc != 2 || !strings.Contains(out+errw, tc.want) {
				t.Errorf("%s %s: rc=%d\n%s%s", tc.goos, mode, rc, out, errw)
			}
		}
	}
}

func TestLinuxUnknownArchNotRefusedUpFront(t *testing.T) {
	f := newFake(t)
	f.machine = "riscv64"
	rc, out, errw := runMain(t, f, "--check")
	if strings.Contains(out+errw, "no pinned release for") || rc == 2 {
		t.Errorf("rc=%d\n%s%s", rc, out, errw)
	}
}

func TestDarwinCheck(t *testing.T) {
	f := newDarwin(t)
	f.login(t)
	rc, out, _ := runMain(t, f, "--check")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	for _, want := range []string{"ok python3>=3.11 ", "ok git ", "ok ram 16.0 GiB", "boot units: launchd step not available yet (G-049 M2)\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("check lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "systemd") {
		t.Errorf("systemd reported on macOS\n%s", out)
	}
	for _, c := range f.calls {
		if c[0] == "systemctl" || c[0] == "loginctl" {
			t.Errorf("probed %v", c)
		}
	}
}

func TestDarwinCheckRAMReadFailureFails(t *testing.T) {
	for _, bad := range []string{"fail", "", "lots"} {
		f := newDarwin(t)
		f.memsize = bad
		rc, out, _ := runMain(t, f, "--check")
		if rc != 3 || !strings.Contains(out, "missing ram ") {
			t.Errorf("memsize %q: rc=%d\n%s", bad, rc, out)
		}
	}
}

func TestDarwinDryRun(t *testing.T) {
	f := newDarwin(t)
	f.tools = map[string]bool{"git": true, "python3": true}
	rc, out, _ := runMain(t, f, "--dry-run", "--linger")
	if rc != 0 {
		t.Fatalf("rc=%d\n%s", rc, out)
	}
	for _, want := range []string{
		"    download " + HerdrAssets["darwin/aarch64"].URL + " -> <tmpdir>/herdr, then verify size 22536176 and sha256 fb77c223",
		"herdr-macos-aarch64",
		"    download " + GHAssets["darwin/aarch64"].URL + " -> <tmpdir>/gh.zip, then verify size",
		"    extract gh_2.103.3_macOS_arm64/bin/gh from the zip and install it as " + f.HomeDir + "/.local/bin/gh\n",
		"boot units: launchd step not available yet (G-049 M2)",
		"--linger: not applicable on macOS (launchd user agents run while you are logged in)",
		"plan only: nothing was changed\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plan lacks %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "systemd") || strings.Contains(out, "enable-linger") {
		t.Errorf("systemd text in a macOS plan\n%s", out)
	}
}

func TestDarwinMissingGitHintsXcode(t *testing.T) {
	f := newDarwin(t)
	f.tools = map[string]bool{"python3": true}
	rc, out, errw := runMain(t, f, "--dry-run")
	if rc != 3 || !strings.Contains(out+errw, "xcode-select --install") || strings.Contains(out+errw, "apt-get") {
		t.Errorf("rc=%d\n%s%s", rc, out, errw)
	}
}

func darwinToolsFake(t *testing.T, zb []byte) (*fakeHost, *Ctx) {
	t.Helper()
	f := newDarwin(t)
	f.tools = map[string]bool{"git": true, "python3": true, "claude": true, "herdr": true}
	f.assets[GHAssets["darwin/aarch64"].URL] = zb
	c := NewCtx(f, &Args{}, &bytes.Buffer{}, false)
	c.tmp = t.TempDir()
	return f, c
}

func TestDarwinInstallsVerifiedZip(t *testing.T) {
	zb := zipWith(t, "gh_2.103.3_macOS_arm64/bin/gh", "gh-binary")
	pinTest(t, GHAssets, "darwin/aarch64", Asset{GHAssets["darwin/aarch64"].URL, int64(len(zb)), shaHex(zb)})
	f, c := darwinToolsFake(t, zb)
	f.tools["gh"] = false
	detail, err := stepTools(c)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(detail, "gh 2.103.3 (size+sha256 verified)") {
		t.Errorf("detail %q", detail)
	}
	got, err := os.ReadFile(filepath.Join(f.HomeDir, ".local", "bin", "gh"))
	if err != nil || string(got) != "gh-binary" {
		t.Errorf("gh: %q %v", got, err)
	}
}

func TestDarwinTamperedZipShaSizeRefused(t *testing.T) {
	zb := zipWith(t, "gh_2.103.3_macOS_arm64/bin/gh", "gh-binary")
	evil := zipWith(t, "gh_2.103.3_macOS_arm64/bin/gh", "evil-binary")
	url := GHAssets["darwin/aarch64"].URL
	for _, tc := range []struct {
		name string
		pin  Asset
		want string
	}{
		{"Sha", Asset{url, int64(len(evil)), shaHex(zb)}, "sha256 mismatch; nothing installed"},
		{"Size", Asset{url, int64(len(zb)) + 1, shaHex(evil)}, "size mismatch"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinTest(t, GHAssets, "darwin/aarch64", tc.pin)
			f, c := darwinToolsFake(t, evil)
			f.tools["gh"] = false
			_, err := stepTools(c)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err %v", err)
			}
			if _, err := os.Stat(filepath.Join(f.HomeDir, ".local", "bin", "gh")); err == nil {
				t.Error("an unverified gh was installed")
			}
		})
	}
}

func TestDarwinZipMemberMissingFails(t *testing.T) {
	zb := zipWith(t, "other/gh", "x")
	pinTest(t, GHAssets, "darwin/aarch64", Asset{GHAssets["darwin/aarch64"].URL, int64(len(zb)), shaHex(zb)})
	f, c := darwinToolsFake(t, zb)
	f.tools["gh"] = false
	if _, err := stepTools(c); err == nil || !strings.Contains(err.Error(), "not found in gh.zip") {
		t.Errorf("err %v", err)
	}
}
