package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"factory/go/internal/identity"
)

type srcEnv struct {
	*cliEnv
	repo, scratch, agentCwd, marker, globalHome string
}

// newSrcEnv: a live home, the caller claude-coder (cwd agentCwd, session sess1), a project repo with a .gitattributes
// that names a filter driver, and a global git config (the process HOME) that defines that driver to touch a marker
// file and sets a global attributes file that export-ignores *.txt.
func newSrcEnv(t *testing.T) *srcEnv {
	t.Helper()
	e := newCliEnv(t)
	root := t.TempDir()
	s := &srcEnv{cliEnv: e, repo: filepath.Join(root, "repo"), agentCwd: filepath.Join(root, "agent"), globalHome: filepath.Join(root, "ghome")}
	for _, d := range []string{s.repo, s.agentCwd, s.globalHome} {
		os.MkdirAll(d, 0o755)
	}
	s.marker = filepath.Join(root, "MARKER")
	attrs := filepath.Join(s.globalHome, "global-attributes")
	os.WriteFile(attrs, []byte("*.txt export-ignore\n"), 0o644)
	os.WriteFile(filepath.Join(s.globalHome, ".gitconfig"), []byte(fmt.Sprintf(
		"[filter \"evil\"]\n\tsmudge = touch %s; cat\n\tclean = cat\n\tprocess = touch %s\n[core]\n\tattributesFile = %s\n", s.marker, s.marker, attrs)), 0o644)
	t.Setenv("HOME", s.globalHome)
	t.Setenv("GIT_CONFIG_GLOBAL", filepath.Join(s.globalHome, ".gitconfig"))
	gitIn(t, s.repo, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(s.repo, "a.txt"), []byte("a\n"), 0o644)
	os.WriteFile(filepath.Join(s.repo, "b.go"), []byte("package b\n"), 0o644)
	os.MkdirAll(filepath.Join(s.repo, "sub"), 0o755)
	os.WriteFile(filepath.Join(s.repo, "sub", "c.go"), []byte("package c\n"), 0o644)
	os.WriteFile(filepath.Join(s.repo, ".gitattributes"), []byte("* filter=evil\n"), 0o644)
	gitIn(t, s.repo, "add", "-A")
	gitIn(t, s.repo, "commit", "-q", "-m", "x")
	oldBase := srcTmpBase
	srcTmpBase = filepath.Join(root, "tmp")
	t.Cleanup(func() { srcTmpBase = oldBase })
	slug := regexp.MustCompile(`[^A-Za-z0-9]`).ReplaceAllString(identity.Realpath(s.agentCwd), "-")
	s.scratch = filepath.Join(srcTmpBase, fmt.Sprintf("claude-%d", os.Getuid()), slug, "sess1", "scratchpad")
	os.MkdirAll(s.scratch, 0o755)
	e.sql("UPDATE agents SET cwd=?, session_id='sess1' WHERE name='claude-coder'", s.agentCwd)
	e.sql("UPDATE projects SET repo_path=? WHERE id='P-1'", s.repo)
	e.cwd = s.agentCwd
	return s
}

func (s *srcEnv) src(args ...string) (string, string, int) {
	return s.run("claude-coder", "", append([]string{"src"}, args...)...)
}

// TestSrcNoFilters: a ref whose .gitattributes names a filter driver (defined in the caller's global git config to
// touch a marker file) does not run it, and the caller's global attributes file does not apply.
func TestSrcNoFilters(t *testing.T) {
	s := newSrcEnv(t)
	// control: with the caller's own config a plain git archive runs the long-running filter driver and honours the
	// global attributes file, so the isolation below is what keeps both out
	ctl := exec.Command("git", "-C", s.repo, "archive", "--format=tar", "HEAD")
	ctl.Env = os.Environ()
	ctl.Output() // exits non-zero: the stub filter does not speak the filter protocol; the marker is what matters
	if _, err := os.Stat(s.marker); err != nil {
		t.Fatalf("control: a plain git archive with the caller's config should have run the filter driver: %v", err)
	}
	if err := os.Remove(s.marker); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(s.scratch, "out")
	out, errs, code := s.src("HEAD", dest)
	if code != 0 || out != "extracted HEAD to "+identity.Realpath(dest)+"\n" || errs != "" || s.execs != 0 {
		t.Fatalf("exit %d out %q err %q execs %d", code, out, errs, s.execs)
	}
	if _, err := os.Stat(s.marker); err == nil {
		t.Fatalf("the filter driver ran: %s exists", s.marker)
	}
	for _, f := range []string{"a.txt", "b.go", "sub/c.go", ".gitattributes"} {
		if _, err := os.Stat(filepath.Join(dest, f)); err != nil {
			t.Errorf("%s was not exported: %v", f, err)
		}
	}
	// limited to paths, after --
	out, _, code = s.src("HEAD", filepath.Join(s.scratch, "only"), "--", "b.go")
	if code != 0 || !strings.HasPrefix(out, "extracted HEAD to ") {
		t.Fatalf("paths: exit %d out %q", code, out)
	}
	if ents, _ := os.ReadDir(filepath.Join(s.scratch, "only")); len(ents) != 1 || ents[0].Name() != "b.go" {
		t.Errorf("paths export holds %v, want only b.go", ents)
	}
}

func TestSrcRefusals(t *testing.T) {
	s := newSrcEnv(t)
	real := identity.Realpath(s.scratch)
	link := filepath.Join(s.scratch, "link")
	os.Symlink(s.repo, link)
	full := filepath.Join(s.scratch, "full")
	os.MkdirAll(full, 0o755)
	os.WriteFile(filepath.Join(full, "x"), []byte("x"), 0o644)
	for _, tc := range []struct {
		name string
		args []string
		code int
		err  string
	}{
		{"dest outside the scratchpad", []string{"HEAD", filepath.Join(s.agentCwd, "out")}, 2,
			fmt.Sprintf("factory: dest %s is not inside your scratchpad (%s); pick a subdirectory there\n", filepath.Join(identity.Realpath(s.agentCwd), "out"), real)},
		{"dest is the scratchpad", []string{"HEAD", s.scratch}, 2,
			fmt.Sprintf("factory: dest %s is not inside your scratchpad (%s); pick a subdirectory there\n", real, real)},
		{"dest symlink", []string{"HEAD", link}, 2, fmt.Sprintf("factory: dest %s is a symlink; pass a real directory inside your scratchpad\n", link)},
		{"dest not empty", []string{"HEAD", full}, 2, fmt.Sprintf("factory: dest %s exists and is not an empty directory; pick a new or empty one\n", full)},
		{"unknown ref", []string{"nope", filepath.Join(s.scratch, "bad")}, 1,
			fmt.Sprintf("factory: ref 'nope' is not a tree-ish in %s; check `git -C %s rev-parse` with a branch, tag or sha\n", identity.Realpath(s.repo), identity.Realpath(s.repo))},
	} {
		out, errs, code := s.src(tc.args...)
		if code != tc.code || errs != tc.err || out != "" || s.execs != 0 {
			t.Errorf("%s: exit %d out %q err %q execs %d", tc.name, code, out, errs, s.execs)
		}
	}
	if _, err := os.Stat(filepath.Join(s.scratch, "bad")); err == nil {
		t.Errorf("a failed export left its created destination behind")
	}
	s.cwd = s.repo
	if _, errs, code := s.src("HEAD", filepath.Join(s.scratch, "o2")); code != 2 || errs != fmt.Sprintf("factory: run factory src from your own working directory %s, not %s\n", identity.Realpath(s.agentCwd), identity.Realpath(s.repo)) {
		t.Errorf("wrong cwd: exit %d err %q", code, errs)
	}
	s.cwd = s.agentCwd
	// a destination that holds a shared checkout (a project repo) is refused
	s.sql("INSERT INTO projects(id,name,repo_path,created) VALUES('P-2','p2',?, '2026-11-01T10:00:00+01:00')", filepath.Join(s.scratch, "o3", "inner"))
	if _, errs, code := s.src("HEAD", filepath.Join(s.scratch, "o3")); code != 2 || !strings.Contains(errs, "overlaps the shared checkout "+filepath.Join(real, "o3", "inner")) {
		t.Errorf("overlap: exit %d err %q", code, errs)
	}
	if _, errs, code := s.run("owner", "", "src", "HEAD", filepath.Join(s.scratch, "o4")); code != 2 || errs != "factory: factory src runs as a registered agent: FACTORY_AGENT must name an agent with a registered cwd\n" {
		t.Errorf("owner: exit %d err %q", code, errs)
	}
	s.sql("UPDATE agents SET session_id='other' WHERE name='claude-coder'")
	if _, errs, code := s.src("HEAD", filepath.Join(s.scratch, "o5")); code != 2 || !strings.HasPrefix(errs, "factory: no scratchpad found for your session") {
		t.Errorf("no scratchpad: exit %d err %q", code, errs)
	}
	s.sql("UPDATE agents SET session_id='bad/../id' WHERE name='claude-coder'")
	if _, errs, code := s.src("HEAD", filepath.Join(s.scratch, "o6")); code != 2 || errs != "factory: registered session id 'bad/../id' is not a plain id\n" {
		t.Errorf("session id: exit %d err %q", code, errs)
	}
}

// TestSrcHandsOver: a repo config Go cannot prove clean, a home that is not live, and shapes argparse reads differently
// run in Python, with nothing written.
func TestSrcHandsOver(t *testing.T) {
	s := newSrcEnv(t)
	dest := filepath.Join(s.scratch, "out")
	for _, cfg := range [][]string{
		{"config", "filter.evil.smudge", "touch x"}, {"config", "remote.origin.url", "https://github.com/acme/app.git"},
		{"config", "include.path", "/x"}, {"config", "user.email", "me@example.com"}, {"config", "core.sshCommand", "x"},
	} {
		gitIn(t, s.repo, cfg...)
		out, errs, code := s.src("HEAD", dest)
		if s.execs != 1 || out != "" || errs != "" || code != 0 {
			t.Errorf("%v: execs %d exit %d out %q err %q", cfg, s.execs, code, out, errs)
		}
		gitIn(t, s.repo, "config", "--unset", cfg[1])
		if _, err := os.Stat(dest); err == nil {
			t.Errorf("%v: the destination was created before the hand-over", cfg)
		}
	}
	gitIn(t, s.repo, "config", "user.email", "1+a@users.noreply.github.com")
	gitIn(t, s.repo, "config", "branch.main.remote", "origin")
	if _, errs, code := s.src("HEAD", dest); code != 0 || s.execs != 0 {
		t.Errorf("allow-listed keys are clean: exit %d err %q execs %d", code, errs, s.execs)
	}
	os.WriteFile(filepath.Join(s.repo, ".git", "info", "attributes"), []byte("* -text\n"), 0o644)
	s.src("HEAD", filepath.Join(s.scratch, "o2"))
	if s.execs != 1 {
		t.Errorf("a non-empty info/attributes must hand over (execs %d)", s.execs)
	}
	os.WriteFile(filepath.Join(s.repo, ".git", "info", "attributes"), nil, 0o644)
	s.env["FACTORY_HOME"] = filepath.Join(filepath.Dir(s.pw), "elsewhere")
	s.src("HEAD", filepath.Join(s.scratch, "o3"))
	if s.execs != 1 {
		t.Errorf("a home that is not live must hand over (execs %d)", s.execs)
	}
}

func TestParseSrc(t *testing.T) {
	good := map[string][]string{"HEAD d": nil, "HEAD d a b": {"a", "b"}, "HEAD d -- -x y": {"-x", "y"}, "HEAD d --": nil}
	for in, paths := range good {
		got, ok := parseSrc(strings.Fields(in))
		if !ok {
			t.Errorf("parseSrc(%q) handed over", in)
			continue
		}
		c := got.(srcCmd)
		if c.ref != "HEAD" || c.dest != "d" || len(c.paths) != len(paths) || (len(paths) > 0 && !reflect.DeepEqual(c.paths, paths)) {
			t.Errorf("parseSrc(%q) = %+v, want paths %v", in, c, paths)
		}
	}
	for _, in := range []string{"", "HEAD", "-x d", "HEAD -x", "HEAD d -p", "HEAD d -- --", "HEAD d a -- b"} {
		if _, ok := parseSrc(strings.Fields(in)); ok {
			t.Errorf("parseSrc(%q) should hand over", in)
		}
	}
	if _, ok := parseSrc([]string{"\xff", "d"}); ok {
		t.Errorf("invalid UTF-8 should hand over")
	}
}

func FuzzParseSrc(f *testing.F) {
	for _, s := range []string{"HEAD", "--", "-x", "a/b", "\xff", ""} {
		f.Add(s, "d", "--")
	}
	f.Fuzz(func(t *testing.T, a, b, c string) {
		parseSrc([]string{a, b})
		parseSrc([]string{a, b, c})
		parseSrc([]string{a, b, "--", c})
	})
}
