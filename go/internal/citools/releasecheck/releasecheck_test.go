package releasecheck

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
	sha     = "0123456789abcdef0123456789abcdef01234567"
	version = "0.6.0"
)

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitEnv() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
}

// pyRepo is a real git repo with Python files under some of the v0.6.0 rule directories.
func pyRepo(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		write(t, filepath.Join(root, f), "x = 1\n")
	}
	write(t, filepath.Join(root, "VERSION"), version+"\n")
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"add", "-A"},
		{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "x"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		cmd.Env = gitEnv()
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

func realExec(t *testing.T, argv ...string) (int, string, string) {
	t.Helper()
	var o, e bytes.Buffer
	rc := Exec(argv, RunCmd, ServeProbe, gitEnv(), &o, &e)
	return rc, o.String(), e.String()
}

func TestPythonUnderRule(t *testing.T) {
	root := pyRepo(t, "bin/a.py", "bin/b.py", "bin/factory", "roles/hooks/g.py", "web/sub/x.py", "web/y.js", "tests/t.py", "go/z.go")
	rc, out, errText := realExec(t, "--repo", root)
	if rc != 0 {
		t.Fatalf("default is a warning only: rc %d %s %s", rc, out, errText)
	}
	for _, want := range []string{"release-check: python under bin/: 2\n", "release-check: python under roles/hooks/: 1\n",
		"release-check: python under web/: 1\n", "release-check: python under scout/: 0\n", "release-check: python under retro/: 0\n",
		"release-check: python under plugins/hivemill/scripts/: 0\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	rc, out, _ = realExec(t, "--repo", root, "--require-no-python")
	if rc != 1 || !strings.Contains(out, "FAIL no-python: 4 Python file(s) remain") {
		t.Fatalf("--require-no-python must exit 1: rc %d\n%s", rc, out)
	}
	clean := pyRepo(t, "tests/t.py", "go/z.go", "bin/factory")
	rc, out, _ = realExec(t, "--repo", clean, "--require-no-python")
	if rc != 0 || strings.Contains(out, "FAIL") || !strings.Contains(out, "python under bin/: 0") {
		t.Fatalf("a tree without Python passes: rc %d\n%s", rc, out)
	}
}

func TestPythonCountsFallBackToAWalkOutsideGit(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "bin", "a.py"), "")
	write(t, filepath.Join(root, ".git", "bin", "no.py"), "")
	_, counts := PythonCounts(RunCmd, root, "")
	if counts[0] != 1 {
		t.Fatalf("bin/ count %d, want 1", counts[0])
	}
}

func TestPythonCountsNonASCIIPaths(t *testing.T) {
	root := pyRepo(t, "bin/caf\u00e9.py", "bin/\u65e5\u672c.py", "bin/plain.py")
	_, counts := PythonCounts(RunCmd, root, "")
	if counts[0] != 3 {
		t.Fatalf("bin/ count %d, want 3 (quoted non-ASCII paths must count)", counts[0])
	}
	sha := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	_, counts = PythonCounts(RunCmd, root, sha)
	if counts[0] != 3 {
		t.Fatalf("bin/ count at a commit %d, want 3", counts[0])
	}
}

func gitOut(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	cmd.Env = gitEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

func TestUsageErrors(t *testing.T) {
	for _, argv := range [][]string{nil, {"--bogus"}, {"a", "b"}, {"--repo"}, {"--repo", "o/n"}} {
		if rc, _, _ := realExec(t, argv...); rc != 2 {
			t.Errorf("%v: rc %d, want 2", argv, rc)
		}
	}
}

// fake is a scripted process runner: the answers are keyed by the leading words of argv.
type fake struct {
	t     *testing.T
	calls [][]string
	envs  [][]string
	over  map[string]func() (int, string, string)
	notes string
}

func (f *fake) run(argv []string, cwd string, env []string, timeout time.Duration) (int, string, string) {
	f.calls = append(f.calls, argv)
	f.envs = append(f.envs, env)
	key := strings.Join(argv, " ")
	for k, fn := range f.over {
		if strings.Contains(key, k) {
			return fn()
		}
	}
	switch {
	case argv[0] == "git" && argv[1] == "clone":
		src := argv[len(argv)-1]
		write(f.t, filepath.Join(src, "VERSION"), version+"\n")
		write(f.t, filepath.Join(src, "ci", "release-notes", version+".md"), f.notes)
		write(f.t, filepath.Join(src, "CHANGELOG.md"), "## ["+version+"] - x\n")
		return 0, "", ""
	case strings.Contains(key, "rev-parse --verify"):
		return 0, sha + "\n", ""
	case strings.Contains(key, "rev-list --first-parent origin/main"):
		return 0, "ffff\n" + sha + "\n", ""
	case strings.Contains(key, "remote get-url origin"):
		return 0, "https://github.com/example-org/hivemill.git\n", ""
	case strings.Contains(key, "ls-remote"):
		return 0, "", ""
	case strings.Contains(key, "gh run list"):
		return 0, `[{"status":"completed","conclusion":"success","databaseId":77}]`, ""
	case strings.Contains(key, "ls-tree") || strings.Contains(key, "ls-files"):
		return 0, "bin/factory\n", ""
	case strings.Contains(key, "show "+sha+":VERSION"):
		return 0, version + "\n", ""
	case strings.Contains(key, "--version"):
		return 0, "hivemill " + version + "\n", ""
	}
	return 0, "", ""
}

func (f *fake) exec(args ...string) (int, string, string) {
	var o, e bytes.Buffer
	probe := func(argv []string, env []string, port int) (bool, string) { return true, "HTTP 200 on /" }
	rc := Exec(args, f.run, probe, []string{"HOME=/h"}, &o, &e)
	return rc, o.String(), e.String()
}

const goodNotes = "Notes for the release.\n\n### Upgrade\n\nRun the installer.\n"

func newFake(t *testing.T) *fake {
	return &fake{t: t, notes: goodNotes, over: map[string]func() (int, string, string){}}
}

func TestAllGreenPrintsOwnerCommands(t *testing.T) {
	f := newFake(t)
	rc, out, errText := f.exec(sha, "--version", version, "--repo", "example-org/hivemill")
	if rc != 0 {
		t.Fatalf("rc %d\n%s\n%s", rc, out, errText)
	}
	for _, name := range []string{"on-main", "version", "ci-green", "notes", "fresh-install-smoke", "public-text", "tag-free"} {
		if !strings.Contains(out, "PASS "+name+": ") {
			t.Errorf("no PASS for %s:\n%s", name, out)
		}
	}
	for _, want := range []string{"Reminder: v0.6.0 is a MINOR (or MAJOR) release.", "--repo https://github.com/example-org/hivemill.git --ref " + sha,
		"git -C ~/factory tag -a v0.6.0 -m v0.6.0 " + sha, "git -C ~/factory push origin v0.6.0", "Do not run `gh release create`"} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	for _, call := range f.calls {
		if j := strings.Join(call, " "); strings.Contains(j, "git push") || strings.Contains(j, "tag -a") || strings.Contains(j, "release create") {
			t.Errorf("the gate must never mutate: %s", j)
		}
	}
}

// The checkout is a real clone of a tree with no scout/ dir; the other processes stay fake.
func TestPassesOnATreeWithoutScout(t *testing.T) {
	root := pyRepo(t, "bin/factory", "ci/demo_seed.py")
	write(t, filepath.Join(root, "CHANGELOG.md"), "## ["+version+"] - x\n")
	write(t, filepath.Join(root, "ci", "release-notes", version+".md"), goodNotes)
	for _, args := range [][]string{{"add", "-A"},
		{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "notes"},
		{"update-ref", "refs/remotes/origin/main", "HEAD"}} {
		gitOut(t, root, args...)
	}
	if _, err := os.Stat(filepath.Join(root, "scout")); !os.IsNotExist(err) {
		t.Fatalf("the tree must have no scout/ dir: %v", err)
	}
	f := newFake(t)
	run := func(argv []string, cwd string, env []string, timeout time.Duration) (int, string, string) {
		if argv[0] == "git" && !strings.Contains(strings.Join(argv, " "), "remote") {
			return RunCmd(argv, cwd, gitEnv(), timeout)
		}
		return f.run(argv, cwd, env, timeout)
	}
	probe := func(argv []string, env []string, port int) (bool, string) { return true, "HTTP 200 on /" }
	var o, e bytes.Buffer
	head := strings.TrimSpace(gitOut(t, root, "rev-parse", "HEAD"))
	rc := Exec([]string{head, "--version", version, "--repo", root}, run, probe, []string{"HOME=/h"}, &o, &e)
	if rc != 0 || strings.Contains(o.String(), "FAIL") {
		t.Fatalf("rc %d\n%s\n%s", rc, o.String(), e.String())
	}
}

func TestEachCheckCanFail(t *testing.T) {
	cases := map[string]struct {
		key  string
		fn   func() (int, string, string)
		want string
	}{
		"on-main":  {"rev-list --first-parent", func() (int, string, string) { return 0, "ffff\n", "" }, "FAIL on-main: "},
		"ci-green": {"gh run list", func() (int, string, string) { return 0, "[]", "" }, "FAIL ci-green: no tests.yml push run"},
		"tag-free": {"ls-remote", func() (int, string, string) { return 0, "abc\trefs/tags/v0.6.0\n", "" }, "FAIL tag-free: tag v0.6.0 already exists on origin"},
		"smoke":    {"--version", func() (int, string, string) { return 0, "hivemill 0.5.0\n", "" }, "FAIL fresh-install-smoke: "},
		"public":   {"public_text_guard.py", func() (int, string, string) { return 2, "", "no terms" }, "FAIL public-text: public_text_guard.py exit 2 (owner terms not loaded"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			f.over[tc.key] = tc.fn
			rc, out, _ := f.exec(sha, "--version", version, "--repo", "example-org/hivemill")
			if rc != 1 || !strings.Contains(out, tc.want) || strings.Contains(out, "Owner, run:") {
				t.Fatalf("rc %d, want %q and no owner commands:\n%s", rc, tc.want, out)
			}
		})
	}
	t.Run("version mismatch", func(t *testing.T) {
		rc, out, _ := newFake(t).exec(sha, "--version", "0.7.0", "--repo", "example-org/hivemill")
		if rc != 1 || !strings.Contains(out, "FAIL version: VERSION at 0123456789ab is '0.6.0', not '0.7.0'") {
			t.Fatalf("rc %d\n%s", rc, out)
		}
	})
}

func TestNotesChecks(t *testing.T) {
	f := newFake(t)
	f.notes = "Notes with a TODO and <placeholder>.\n\n### Upgrade\n"
	rc, out, _ := f.exec(sha, "--version", version, "--repo", "example-org/hivemill")
	if rc != 1 || !strings.Contains(out, "FAIL notes: placeholders in the release notes: line 1: TODO; line 1: <placeholder>") {
		t.Fatalf("rc %d\n%s", rc, out)
	}
	f = newFake(t)
	f.notes = "No upgrade heading.\n"
	rc, out, _ = f.exec(sha, "--version", version, "--repo", "example-org/hivemill")
	if rc != 1 || !strings.Contains(out, `has no "### Upgrade" line`) {
		t.Fatalf("rc %d\n%s", rc, out)
	}
}

func TestSmokeIsIsolatedAndTempRemoved(t *testing.T) {
	f := newFake(t)
	if rc, out, _ := f.exec(sha, "--version", version, "--repo", "example-org/hivemill"); rc != 0 {
		t.Fatal(out)
	}
	var smoke []string
	for i, c := range f.calls {
		if j := strings.Join(c, " "); strings.Contains(j, "--version") || strings.Contains(j, "demo_seed.py") {
			smoke = f.envs[i]
		}
	}
	if len(smoke) == 0 {
		t.Fatal("no smoke call saw an environment")
	}
	env := strings.Join(smoke, "\n")
	if strings.Contains(env, "HOME=/h") || !strings.Contains(env, "release-check-") {
		t.Errorf("smoke must run with an isolated HOME and FACTORY_HOME:\n%s", env)
	}
	for _, c := range f.calls {
		if len(c) > 2 && c[1] == "clone" {
			tmp := filepath.Dir(c[len(c)-1])
			if _, err := os.Stat(tmp); err == nil {
				t.Errorf("temp dir %s not removed", tmp)
			}
		}
	}
}

func TestUnresolvableShaExits2(t *testing.T) {
	f := newFake(t)
	f.over["rev-parse --verify"] = func() (int, string, string) { return 128, "", "bad object" }
	rc, _, errText := f.exec("deadbeef", "--version", version, "--repo", "example-org/hivemill")
	if rc != 2 || !strings.Contains(errText, "cannot resolve 'deadbeef' to a commit") {
		t.Fatalf("rc %d %s", rc, errText)
	}
}

func TestPatchReleaseHasNoFreshboxReminder(t *testing.T) {
	g := &Gate{SHA: sha, Version: "0.6.1", Repo: "o/n", Root: "/r r", Run: newFake(t).run}
	if got := strings.Join(g.OwnerCommands(), "\n"); strings.Contains(got, "Reminder") || !strings.Contains(got, "Release v0.6.1 is ready") {
		t.Fatalf("%s", got)
	}
	g.Version = "1.0.0"
	if got := strings.Join(g.OwnerCommands(), "\n"); !strings.Contains(got, "cd '/r r' && sudo python3 ci/freshbox_test.py") {
		t.Fatalf("the root must be shell-quoted: %s", got)
	}
}

// FuzzParseArgs: no argv panics, and a parse that succeeds never leaves a flag-looking word as the SHA.
func FuzzParseArgs(f *testing.F) {
	f.Add("--version", "1.0.0", "abc")
	f.Add("--repo=a/b", "--require-no-python", "")
	f.Add("-x", "", "")
	f.Fuzz(func(t *testing.T, a, b, c string) {
		got, err := parseArgs([]string{a, b, c})
		if err == nil && len(got.sha) > 0 && got.sha[0] == '-' {
			t.Fatalf("parseArgs(%q, %q, %q) took a flag as the sha: %q", a, b, c, got.sha)
		}
	})
}
