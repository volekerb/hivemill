package changelog

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const termsEnv = "SCRUB_" + "TERMS_FILE"

func envWithoutTerms() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, termsEnv+"=") && !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return env
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Env = append(envWithoutTerms(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	cmd.Env = append(cmd.Env, env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// fixture is a factory checkout (VERSION, ci/ with the Python reference copied in, release notes) whose git
// history holds squash-style commits; the history is the same repo, so Python and Go read the same thing.
func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	src := repoRoot(t)
	for _, f := range []string{"changelog.py", "scrub_scan.py", "scrub_allowlist.txt"} {
		data, err := os.ReadFile(filepath.Join(src, "ci", f))
		if err != nil {
			t.Skipf("ci/%s not readable (%v): parity not checked", f, err)
		}
		mustWrite(t, filepath.Join(root, "ci", f), string(data))
	}
	mustWrite(t, filepath.Join(root, "VERSION"), "0.4.0\n")
	mustWrite(t, filepath.Join(root, "ci", "release-notes", "0.4.0.md"), "Curated notes.\n\n### Upgrade\n\nRun the installer.\n")
	mustWrite(t, filepath.Join(root, "ci", "release-notes", "0.3.0.md"), "Old notes.\n")
	gitIn(t, root, nil, "init", "-q", "-b", "main")
	subjects := []string{
		"T-1: Fix the tick race (#10)",
		"T-2: Add a new status page (#11)",
		"T-3: Harden the guard against wrappers (#12)",
		"T-4: Rework the inbox (2026-11-10) (#13)",
		"T-5: Scrub 192.168." + "1.20 from logs (#14)",
		"Release v0.4.0 (#15)",
		"not a squash commit",
		"T-6: Bug in parser, fixed (#16)",
	}
	for i, s := range subjects {
		mustWrite(t, filepath.Join(root, "f.txt"), strings.Repeat("x", i+1))
		gitIn(t, root, nil, "add", "-A")
		gitIn(t, root, []string{"GIT_AUTHOR_DATE=2026-11-1" + string(rune('0'+i%10)) + "T00:00:00+0000", "GIT_COMMITTER_DATE=2026-11-1" + string(rune('0'+i%10)) + "T00:00:00+0000"},
			"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false", "commit", "-q", "-m", s)
	}
	mustWrite(t, filepath.Join(root, "CHANGELOG.md"), "stale\n")
	return root
}

type result struct {
	rc  int
	out string
	err string
}

func runPython(t *testing.T, root string, args ...string) result {
	t.Helper()
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not found: parity with ci/changelog.py not checked")
	}
	cmd := exec.Command(py, append([]string{"-I", filepath.Join(root, "ci", "changelog.py")}, args...)...)
	cmd.Dir = root
	cmd.Env = envWithoutTerms()
	var o, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &o, &e
	rc := 0
	if err := cmd.Run(); err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatal(err)
		}
		rc = ee.ExitCode()
	}
	return result{rc, o.String(), e.String()}
}

func runGo(t *testing.T, root string, args ...string) result {
	t.Helper()
	t.Setenv(termsEnv, "")
	cwd, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(cwd)
	var o, e bytes.Buffer
	rc := Main(args, &o, &e)
	return result{rc, o.String(), e.String()}
}

// TestChangelogParity runs ci/changelog.py and the Go port over the same fixture history and compares stdout,
// stderr and the exit code for every mode.
func TestChangelogParity(t *testing.T) {
	root := fixture(t)
	cases := [][]string{
		{"--repo", root, "--ref", "HEAD"},
		{"--repo", root, "--ref", "HEAD", "--check", "--file", filepath.Join(root, "CHANGELOG.md")},
		{"--release-notes", "0.4.0"},
		{"--release-notes", "9.9.9"},
		{"--prerelease", "0.4.0"},
		{"--prerelease", "1.0.0"},
		{"--prerelease", "1.0.0-rc1"},
		{"--check-version", "0.4.0"},
		{"--check-version", "0.5.0"},
		{"--repo", root, "--ref", "no-such-ref"},
	}
	for _, args := range cases {
		t.Run(strings.Join(args[len(args)-min(len(args), 2):], " "), func(t *testing.T) {
			want, got := runPython(t, root, args...), runGo(t, root, args...)
			if want != got {
				t.Fatalf("args %v differ\npython: %+v\ngo:     %+v", args, want, got)
			}
		})
	}
	// --write then --check: both tools agree the file is current.
	file := filepath.Join(root, "CHANGELOG.md")
	if r := runGo(t, root, "--repo", root, "--ref", "HEAD", "--write", "--file", file); r.rc != 0 {
		t.Fatalf("write: %+v", r)
	}
	for _, args := range [][]string{{"--repo", root, "--ref", "HEAD", "--check", "--file", file}, {"--check-version", "0.4.0"}} {
		if w, g := runPython(t, root, args...), runGo(t, root, args...); w != g || g.rc != 0 {
			t.Fatalf("after write, args %v: python %+v go %+v", args, w, g)
		}
	}
}

func TestChangelogContent(t *testing.T) {
	root := fixture(t)
	r := runGo(t, root, "--repo", root, "--ref", "HEAD")
	if r.rc != 0 {
		t.Fatalf("%+v", r)
	}
	for _, want := range []string{
		"#### Fixed\n\n- T-6: Bug in parser, fixed\n- T-1: Fix the tick race\n",
		"#### Security\n\n- T-3: Harden the guard against wrappers\n",
		"#### Added\n\n- T-2: Add a new status page\n",
		"- T-4: Rework the inbox\n", // the date and PR number are dropped
		"Curated notes.",
		"## [0.3.0] - 2026-11-05\n\nOld notes.",
	} {
		if !strings.Contains(r.out, want) {
			t.Errorf("output lacks %q:\n%s", want, r.out)
		}
	}
	for _, bad := range []string{"192.168", "Release v0.4.0", "not a squash", "(#1"} {
		if strings.Contains(r.out, bad) {
			t.Errorf("output must not contain %q", bad)
		}
	}
	if !strings.Contains(r.err, "1 subject(s) dropped by scrub patterns") {
		t.Errorf("stderr: %q", r.err)
	}
}

func TestCheckFailsOnStaleFileAndMissingNotes(t *testing.T) {
	root := fixture(t)
	if r := runGo(t, root, "--repo", root, "--ref", "HEAD", "--check", "--file", filepath.Join(root, "CHANGELOG.md")); r.rc != 1 ||
		!strings.Contains(r.err, "out of date") {
		t.Fatalf("stale file must exit 1: %+v", r)
	}
	if r := runGo(t, root, "--release-notes", "9.9.9"); r.rc != 2 || !strings.Contains(r.err, "No such file or directory") {
		t.Fatalf("missing notes must exit 2: %+v", r)
	}
	if r := runGo(t, root, "--write", "--check"); r.rc != 2 {
		t.Fatalf("--write with --check is a usage error: %+v", r)
	}
}
