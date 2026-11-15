package freshexport

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	termsEnv = "SCRUB_" + "TERMS_FILE"
	author   = "Example Dev <dev@example.com>"
	date     = "2026-03-04T00:00:00+0000"
)

// pyWrap runs a ci/*.py script with the passwd home faked, so the reference never reads the live home.
const pyWrap = `import sys, runpy, pwd
fake, script = sys.argv[1], sys.argv[2]
sys.argv = [script] + sys.argv[3:]
orig = pwd.getpwuid
def getpwuid(uid):
    e = orig(uid)
    return pwd.struct_passwd((e.pw_name, e.pw_passwd, e.pw_uid, e.pw_gid, e.pw_gecos, fake, e.pw_shell))
pwd.getpwuid = getpwuid
runpy.run_path(script, run_name="__main__")
`

func cleanEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, termsEnv+"=") && !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

func write(t *testing.T, p, s string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), mode); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Src", "-c", "user.email=src@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Env = cleanEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

type env struct {
	root, src, ci, home string
}

// newEnv makes a source repo, a copy of the Python reference in ci/, and a fake passwd home.
func newEnv(t *testing.T, extra map[string]string) env {
	t.Helper()
	root := t.TempDir()
	e := env{root: root, src: filepath.Join(root, "src"), ci: filepath.Join(root, "ci"), home: filepath.Join(root, "home")}
	repo, err := filepath.Abs("../../../../ci")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"fresh_export.py", "scrub_scan.py", "scrub_history_allowlist.txt", "scrub_allowlist.txt"} {
		data, err := os.ReadFile(filepath.Join(repo, f))
		if err != nil {
			t.Skipf("ci/%s not readable (%v): parity not checked", f, err)
		}
		write(t, filepath.Join(e.ci, f), string(data), 0o644)
	}
	if err := os.MkdirAll(e.home, 0o755); err != nil {
		t.Fatal(err)
	}
	HomeOverride = e.home
	t.Cleanup(func() { HomeOverride = "" })
	gitIn(t, root, "init", "-q", "-b", "main", "src")
	write(t, filepath.Join(e.src, "a.txt"), "hello\n", 0o644)
	write(t, filepath.Join(e.src, "bin", "run.sh"), "#!/bin/sh\necho hi\n", 0o755)
	write(t, filepath.Join(e.src, "docs", "slug.md"), "repo: "+TokenRepo+" owner: "+TokenOwner+"\n", 0o644)
	write(t, filepath.Join(e.src, "ci", "public-overlay", "README.md"), "# public readme\n", 0o644)
	if err := os.Symlink("a.txt", filepath.Join(e.src, "link")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(e.src, "blob.bin"), "a\x00b", 0o644)
	for p, s := range extra {
		write(t, filepath.Join(e.src, p), s, 0o644)
	}
	gitIn(t, e.src, "add", "-A")
	gitIn(t, e.src, "commit", "-q", "-m", "first")
	return e
}

type result struct {
	rc  int
	out string
	err string
}

func (e env) python(t *testing.T, args ...string) result {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/fresh_export.py not checked")
	}
	cmd := exec.Command(py, append([]string{"-I", "-c", pyWrap, e.home, filepath.Join(e.ci, "fresh_export.py")}, args...)...)
	cmd.Env = cleanEnv()
	return finish(t, cmd)
}

func finish(t *testing.T, cmd *exec.Cmd) result {
	var o, er bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &er
	rc := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		rc = ee.ExitCode()
	}
	return result{rc, o.String(), er.String()}
}

func (e env) goRun(t *testing.T, args ...string) result {
	t.Helper()
	t.Setenv(termsEnv, "")
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	var o, er bytes.Buffer
	rc := Main(append([]string{"--ci-dir", e.ci}, args...), &o, &er)
	return result{rc, o.String(), er.String()}
}

func common(e env, out string, extra ...string) []string {
	return append([]string{"--out", out, "--author", author, "--ref", "main", "--date", date, "--repo", e.src}, extra...)
}

func norm(s, out string) string { return strings.ReplaceAll(s, out, "<out>") }

func TestExportParityWithPython(t *testing.T) {
	e := newEnv(t, nil)
	pyOut, goOut := filepath.Join(e.root, "py-out"), filepath.Join(e.root, "go-out")
	want := e.python(t, common(e, pyOut, "--public-repo", "example-org/hivemill")...)
	got := e.goRun(t, common(e, goOut, "--public-repo", "example-org/hivemill")...)
	if want.rc != 0 || got.rc != want.rc || norm(want.out, pyOut) != norm(got.out, goOut) {
		t.Fatalf("outputs differ\npython: %+v\ngo:     %+v", want, got)
	}
	if !strings.Contains(got.out, "OK - 0 hits") || !strings.Contains(got.out, "binary_blobs=1") || !strings.Contains(got.out, "substituted 2 occurrences in 1 files") {
		t.Errorf("summary lines missing: %s", got.out)
	}
	// the same tree, author, date and message give the same commit id
	if p, g := gitIn(t, pyOut, "rev-parse", "HEAD"), gitIn(t, goOut, "rev-parse", "HEAD"); p != g {
		t.Fatalf("commit ids differ: python %s go %s", p, g)
	}
	if p, g := gitIn(t, pyOut, "ls-tree", "-r", "HEAD"), gitIn(t, goOut, "ls-tree", "-r", "HEAD"); p != g {
		t.Fatalf("trees differ:\n%s\n%s", p, g)
	}
	if got := gitIn(t, goOut, "ls-tree", "-r", "--name-only", "HEAD"); strings.Contains(got, "public-overlay") || !strings.Contains(got, "README.md") {
		t.Errorf("overlay not applied: %s", got)
	}
	if b, _ := os.ReadFile(filepath.Join(goOut, "docs", "slug.md")); string(b) != "repo: example-org/hivemill owner: example-org\n" {
		t.Errorf("slug.md: %q", b)
	}
	if st, err := os.Stat(filepath.Join(goOut, "bin", "run.sh")); err != nil || st.Mode().Perm()&0o100 == 0 {
		t.Errorf("exec bit lost: %v", err)
	}
	if l, err := os.Readlink(filepath.Join(goOut, "link")); err != nil || l != "a.txt" {
		t.Errorf("symlink: %q %v", l, err)
	}
	// the source repository is untouched: no remote, still one branch
	if r := gitIn(t, e.src, "remote"); r != "" {
		t.Errorf("remote added: %q", r)
	}
}

func TestHitsFailTheGateLikePython(t *testing.T) {
	lan := "192.168." + "0.9"
	e := newEnv(t, map[string]string{"notes.md": "server at " + lan + "\n"})
	pyOut, goOut := filepath.Join(e.root, "py-out"), filepath.Join(e.root, "go-out")
	want := e.python(t, common(e, pyOut, "--public-repo", "example-org/hivemill")...)
	got := e.goRun(t, common(e, goOut, "--public-repo", "example-org/hivemill")...)
	if want.rc != 1 || got.rc != 1 || norm(want.out, pyOut) != norm(got.out, goOut) {
		t.Fatalf("hits must exit 1 with equal output\npython: %+v\ngo:     %+v", want, got)
	}
	if !strings.Contains(got.out, "FAIL - do not publish this export") {
		t.Errorf("no FAIL line: %s", got.out)
	}
}

func TestRefusalsLeaveOutEmpty(t *testing.T) {
	e := newEnv(t, nil)
	cases := map[string]struct {
		args []string
		want string
	}{
		"token needs public-repo":  {nil, "pass --public-repo OWNER/NAME"},
		"bad author":               {[]string{"--author", "nobody", "--public-repo", "a/b"}, "is not of the form"},
		"bad public repo":          {[]string{"--public-repo", "a/b/c"}, "is not OWNER/NAME"},
		"ref looks like an option": {[]string{"--ref", "--output=x", "--public-repo", "a/b"}, "starts with '-'"},
		"unknown ref":              {[]string{"--ref", "nope", "--public-repo", "a/b"}, "git"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			out := filepath.Join(e.root, "out-"+strings.ReplaceAll(name, " ", "-"))
			args := common(e, out)
			args = append(args, tc.args...) // later flags win, like argparse
			want := e.python(t, args...)
			got := e.goRun(t, args...)
			if got.rc != 2 || want.rc != 2 {
				t.Fatalf("want exit 2 from both: python %+v go %+v", want, got)
			}
			if !strings.Contains(got.err, tc.want) {
				t.Errorf("stderr %q lacks %q", got.err, tc.want)
			}
			if ents, _ := os.ReadDir(out); len(ents) != 0 {
				t.Errorf("--out not left empty: %v", ents)
			}
		})
	}
	t.Run("out not empty", func(t *testing.T) {
		out := filepath.Join(e.root, "full")
		write(t, filepath.Join(out, "x"), "x", 0o644)
		if got := e.goRun(t, common(e, out, "--public-repo", "a/b")...); got.rc != 2 || !strings.Contains(got.err, "not empty") {
			t.Fatalf("%+v", got)
		}
	})
	t.Run("out inside the live home", func(t *testing.T) {
		out := filepath.Join(HomeOverride, "factory", "exp")
		if got := e.goRun(t, common(e, out, "--public-repo", "a/b")...); got.rc != 2 || !strings.Contains(got.err, "live factory home") {
			t.Fatalf("%+v", got)
		}
		if _, err := os.Stat(out); err == nil {
			t.Error("directory created inside the live home")
		}
	})
	t.Run("private slug in the tree", func(t *testing.T) {
		e2 := newEnv(t, map[string]string{"x.md": "see https://github.com/acme/secret-repo/issues\n"})
		gitIn(t, e2.src, "remote", "add", "origin", "https://github.com/acme/secret-repo.git")
		out := filepath.Join(e2.root, "out")
		got := e2.goRun(t, common(e2, out, "--public-repo", "example-org/hivemill")...)
		if got.rc != 2 || !strings.Contains(got.err, "name the private repository") {
			t.Fatalf("%+v", got)
		}
		if ents, _ := os.ReadDir(out); len(ents) != 0 {
			t.Errorf("--out not left empty: %v", ents)
		}
	})
}

func TestDefaultDateIsTodayUTC(t *testing.T) {
	e := newEnv(t, nil)
	Now = func() time.Time { return time.Date(2031, 5, 6, 23, 59, 0, 0, time.FixedZone("x", -7*3600)) }
	t.Cleanup(func() { Now = time.Now })
	out := filepath.Join(e.root, "out")
	args := []string{"--out", out, "--author", author, "--ref", "main", "--repo", e.src, "--public-repo", "example-org/hivemill"}
	got := e.goRun(t, args...)
	if got.rc != 0 || !strings.Contains(got.out, "date=2031-05-07T00:00:00+0000") {
		t.Fatalf("%+v", got)
	}
}

func TestNamesPrivateSlug(t *testing.T) {
	for text, want := range map[string]bool{
		"acme/private":                      true,
		"https://github.com/ACME/Private/x": true,
		"acme%2Fprivate":                    true,
		"acme/private.git":                  true,
		"acme/private.":                     true,
		"acme/private-public":               false,
		"xacme/private":                     false,
		"my-acme/private":                   false,
		"acme/private_two":                  false,
		"x%2Facme/private":                  true,
		"nothing here":                      false,
	} {
		if got := NamesPrivateSlug([]byte(text), "acme/private"); got != want {
			t.Errorf("%q: got %v want %v", text, got, want)
		}
	}
}

// SplitLines and LiveHomeLine read live_home like Python's text mode plus splitlines; a CRLF must never hide the live home.
func TestLiveHomeLineLikePython(t *testing.T) {
	for in, want := range map[string]string{
		"/h\n": "/h", "/h\r\n": "/h", "/h\r": "/h", "/h": "/h", "/h ": "/h", "/h\x0b": "/h", "/h\u0085": "/h",
		"": "", "\n": "", "/a\n/b\n": "", "/a\r\n/b": "", "rel\r\n": "", "/h\x00\r\n": "", "/h\n\n": "", "\xff/h\n": "",
	} {
		got, _ := LiveHomeLine([]byte(in))
		if got != want {
			t.Errorf("LiveHomeLine(%q) = %q, want %q", in, got, want)
		}
	}
	if got := SplitLines("a\r\nb\rc\n\nd "); len(got) != 5 || got[1] != "b" || got[3] != "" || got[4] != "d" {
		t.Errorf("SplitLines = %q", got)
	}
}

func TestLiveHomesAtCRLF(t *testing.T) {
	base := t.TempDir()
	cfg := filepath.Join(base, ".config", "factory")
	live := filepath.Join(base, "elsewhere")
	if err := os.MkdirAll(cfg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(cfg, "live_home"), live+"\r\n", 0o644)
	if !liveHomesAt(base, os.Getuid())[realpath(live)] {
		t.Fatal("a CRLF live_home line was not treated as the live home")
	}
}

// FuzzSplitLines: no line holds a separator and the parsers never panic.
func FuzzSplitLines(f *testing.F) {
	for _, s := range []string{"", "a\r\nb", "\r", "/h\r\n", "a b\x85c", "\xff\r\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, l := range SplitLines(s) {
			if strings.ContainsAny(l, "\r\n\v\f\x1c\x1d\x1e\u0085  ") {
				t.Fatalf("line %q of %q holds a separator", l, s)
			}
		}
		LiveHomeLine([]byte(s))
		originRe.FindStringSubmatch(s)
	})
}

// A chain whose every hop looks inside the root lexically but whose real target is outside is refused:
// up -> d/b/.. is lexically the root, while d/b -> .. makes it the parent of the root.
func TestSymlinkChainEscapeRefused(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.MkdirAll(filepath.Join(e.src, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(e.src, "d", "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d/b/..", filepath.Join(e.src, "up")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.src, "add", "-A")
	gitIn(t, e.src, "commit", "-q", "-m", "chain")
	out := filepath.Join(e.root, "chain-out")
	got := e.goRun(t, common(e, out, "--public-repo", "example-org/hivemill")...)
	if got.rc == 0 || !strings.Contains(got.err, "symlink member refused") {
		t.Fatalf("a 2-hop escaping chain must be refused: %+v", got)
	}
}

func TestSymlinkChainInsideRootAccepted(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.Symlink("link", filepath.Join(e.src, "link2")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.src, "add", "-A")
	gitIn(t, e.src, "commit", "-q", "-m", "chain")
	out := filepath.Join(e.root, "ok-out")
	if got := e.goRun(t, common(e, out, "--public-repo", "example-org/hivemill")...); got.rc != 0 {
		t.Fatalf("a chain staying inside the root is accepted: %+v", got)
	}
}

// A dangling chain: d/b -> .. and up -> d/b/../missing. Lexically up stays in the root; for real it lands
// in the parent of the root, and the missing final target must not let it through.
func TestSymlinkDanglingChainEscapeRefused(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.MkdirAll(filepath.Join(e.src, "d"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(e.src, "d", "b")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("d/b/../missing", filepath.Join(e.src, "up")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.src, "add", "-A")
	gitIn(t, e.src, "commit", "-q", "-m", "dangling chain")
	out := filepath.Join(e.root, "dangling-out")
	got := e.goRun(t, common(e, out, "--public-repo", "example-org/hivemill")...)
	if got.rc == 0 || !strings.Contains(got.err, "symlink member refused") {
		t.Fatalf("a dangling escaping chain must be refused: %+v", got)
	}
}

func TestSymlinkDanglingInsideRootAccepted(t *testing.T) {
	e := newEnv(t, nil)
	if err := os.Symlink("nowhere", filepath.Join(e.src, "dangling")); err != nil {
		t.Fatal(err)
	}
	gitIn(t, e.src, "add", "-A")
	gitIn(t, e.src, "commit", "-q", "-m", "dangling")
	out := filepath.Join(e.root, "dangling-ok-out")
	if got := e.goRun(t, common(e, out, "--public-repo", "example-org/hivemill")...); got.rc != 0 {
		t.Fatalf("a dangling link staying inside the root is accepted: %+v", got)
	}
}

// A symlink loop cannot be resolved, so it is refused (fail closed) whether it stays inside the export
// (a -> b, b -> a) or reaches outside (a -> b, b -> ../a).
func TestSymlinkLoopRefused(t *testing.T) {
	for name, links := range map[string][][2]string{
		"inside":  {{"b", "a"}, {"a", "b"}},
		"outside": {{"b", "a"}, {"../a", "b"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, nil)
			for _, l := range links {
				if err := os.Symlink(l[0], filepath.Join(e.src, l[1])); err != nil {
					t.Fatal(err)
				}
			}
			gitIn(t, e.src, "add", "-A")
			gitIn(t, e.src, "commit", "-q", "-m", "loop")
			out := filepath.Join(e.root, "loop-out-"+name)
			got := e.goRun(t, common(e, out, "--public-repo", "example-org/hivemill")...)
			if got.rc == 0 || !strings.Contains(got.err, "symlink member refused") {
				t.Fatalf("a symlink loop must be refused: %+v", got)
			}
		})
	}
}

func TestRealpathLoopErrorsAndIsClean(t *testing.T) {
	d := t.TempDir()
	if err := os.Symlink("b", filepath.Join(d, "a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a", filepath.Join(d, "b")); err != nil {
		t.Fatal(err)
	}
	r, err := realpathErr(d + "/x/../a/./")
	if err == nil {
		t.Fatalf("loop must error, got %q", r)
	}
	if r != filepath.Clean(r) {
		t.Fatalf("returned path not clean: %q", r)
	}
	if got := realpath(d + "/a"); got != filepath.Clean(got) {
		t.Fatalf("realpath not clean: %q", got)
	}
}
