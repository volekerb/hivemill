package skilleval1

// Port of tests/test_skilleval_guard_copy.py (12 cases): the evaluator's PreToolUse hook runs from a copy inside the
// trial because nsbox hides the live home.
//
// Group A of the Python module drives the Python evaluator_settings() (it writes <trial>/role/roles/hooks/worker-guard.py,
// the ci files and rewrites the hooks). The Go spawn planner hands that step to its caller (spawn.Start.EvaluatorSettings
// is "not ported here"), so those cases are top-level tests that skip with "not ported". The cases that exercise the copied
// guard itself (a Python script) copy the files exactly as evaluator_settings does and run them with python3; they skip
// where python3 is absent. Group B needs user namespaces and python3 and skips with the reason where unavailable.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"

	_ "modernc.org/sqlite"
)

var gcpCIFiles = []string{"public_text_guard.py", "scrub_scan.py"} // the guard loads the first by path; it loads the second by path

func gcpMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func gcpRepo(t testing.TB) string {
	t.Helper()
	r, err := ft.RepoRoot()
	gcpMust(t, err)
	return r
}

func gcpPython(t testing.TB) string {
	t.Helper()
	p, err := exec.LookPath("python3")
	if err != nil {
		t.Skipf("python3 is not on PATH: the copied guard is a Python script: %v", err)
	}
	return p
}

func gcpCopy(t testing.TB, src, dst string, mode os.FileMode) {
	t.Helper()
	b, err := os.ReadFile(src)
	gcpMust(t, err)
	gcpMust(t, os.MkdirAll(filepath.Dir(dst), 0o755))
	gcpMust(t, os.WriteFile(dst, b, 0o644))
	gcpMust(t, os.Chmod(dst, mode))
}

// gcpTrialCopy stands in for evaluator_settings(): the guard and the ci files copied read-only (0555) into <rdir>.
func gcpTrialCopy(t testing.TB, repo, rdir string) {
	t.Helper()
	gcpCopy(t, filepath.Join(repo, "roles", "hooks", "worker-guard.py"), filepath.Join(rdir, "roles", "hooks", "worker-guard.py"), 0o555)
	for _, n := range gcpCIFiles {
		gcpCopy(t, filepath.Join(repo, "ci", n), filepath.Join(rdir, "ci", n), 0o555)
	}
}

func gcpGit(t testing.TB, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "HOME=" + dir,
		"GIT_AUTHOR_NAME=zz-test", "GIT_AUTHOR_EMAIL=zz-test@example.invalid",
		"GIT_COMMITTER_NAME=zz-test", "GIT_COMMITTER_EMAIL=zz-test@example.invalid"}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

// ---- Group A ----

func TestNotPortedGuardCopy_SpawnCopiesTheGuardAndPointsEveryHookAtIt(t *testing.T) {
	t.Skip("not ported: evaluator_settings() (guard copy, hook rewrite to the trial copy) is Python-only; Go spawn.Plan leaves it to the caller (Start.EvaluatorSettings)")
}

func TestNotPortedGuardCopy_RelaunchRecopiesATamperedGuard(t *testing.T) {
	t.Skip("not ported: re-running the Python evaluator_settings() on a tampered trial copy; no Go counterpart")
}

func TestNotPortedGuardCopy_SpawnCopiesTheCiFilesThePublicTextCheckLoads(t *testing.T) {
	t.Skip("not ported: the ci copy step of the Python evaluator_settings(); no Go counterpart")
}

func TestNotPortedGuardCopy_RelaunchRecopiesTamperedCiFiles(t *testing.T) {
	t.Skip("not ported: re-running the Python evaluator_settings() on tampered ci copies; no Go counterpart")
}

func TestNotPortedGuardCopy_TheShShimFormAndThePythonFormBothBecomeTheTrialCopy(t *testing.T) {
	t.Skip("not ported: the hook command rewrite of the Python evaluator_settings(); no Go counterpart")
}

func TestNotPortedGuardCopy_AHookCommandRunningNeitherFormDies(t *testing.T) {
	t.Skip("not ported: the 'runs neither' refusal of the Python evaluator_settings(); no Go counterpart")
}

func TestNotPortedGuardCopy_MissingCiSourceDiesNamingTheFile(t *testing.T) {
	t.Skip("not ported: the missing-source refusal of the Python evaluator_settings(); no Go counterpart")
}

// gcpPublicTextHook runs the copied guard as a qa hook on a `gh pr create` in a repo whose origin is configured as public.
func gcpPublicTextHook(t *testing.T, py, rdir, tmp string) (code int, stderr string) {
	t.Helper()
	home := filepath.Join(tmp, "home")
	gcpMust(t, os.MkdirAll(filepath.Join(home, "db"), 0o755))
	con, err := sql.Open("sqlite", filepath.Join(home, "db", "factory.db"))
	gcpMust(t, err)
	defer con.Close()
	for _, q := range []string{"CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL)",
		"INSERT INTO config VALUES ('public_text_repos', 'acme/pub')"} {
		if _, err := con.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	repo := filepath.Join(tmp, "repo")
	gcpGit(t, tmp, "init", "-q", "-b", "main", repo)
	gcpGit(t, repo, "remote", "add", "origin", "https://github.com/acme/pub.git")
	var env []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "SCRUB_") || k == "FACTORY_AGENT" {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "FACTORY_HOME="+home)
	payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": "gh pr create --title t --body b"}, "cwd": repo})
	gcpMust(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, py, filepath.Join(rdir, "roles", "hooks", "worker-guard.py"), "--role", "qa")
	cmd.Env, cmd.Stdin = env, bytes.NewReader(payload)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), eb.String()
	}
	gcpMust(t, err)
	return 0, eb.String()
}

func TestSkillevalGuardCopy_CopiedGuardLoadsThePublicTextChecker(t *testing.T) {
	py := gcpPython(t)
	rdir := filepath.Join(sbxRealTemp(t), "trial", "role")
	gcpTrialCopy(t, gcpRepo(t), rdir)
	tmp := sbxRealTemp(t)
	gcpMust(t, os.MkdirAll(filepath.Join(tmp, "with"), 0o755))
	gcpMust(t, os.MkdirAll(filepath.Join(tmp, "without"), 0o755))
	_, stderr := gcpPublicTextHook(t, py, rdir, filepath.Join(tmp, "with"))
	if strings.Contains(stderr, "cannot be loaded") {
		t.Errorf("the checker was not loaded: %s", stderr)
	}
	if !strings.Contains(stderr, "no owner terms loaded") { // the loaded checker ran and refused
		t.Errorf("stderr %q", stderr)
	}
	gcpMust(t, os.RemoveAll(filepath.Join(rdir, "ci"))) // control: without the ci copy the same payload fails closed
	code, stderr := gcpPublicTextHook(t, py, rdir, filepath.Join(tmp, "without"))
	if code != 2 || !strings.Contains(stderr, "public-text-guard cannot be loaded") {
		t.Errorf("control: code %d stderr %q", code, stderr)
	}
}

func TestSkillevalGuardCopy_LiveHomesOfTheCopyIsTheTrialRoleDirNotTrials(t *testing.T) {
	py := gcpPython(t)
	trial := filepath.Join(sbxRealTemp(t), "trials", "t1")
	rdir := filepath.Join(trial, "role")
	gcpTrialCopy(t, gcpRepo(t), rdir)
	script := "import importlib.util, json, sys\n" +
		"spec = importlib.util.spec_from_file_location('guard_copy_mod', sys.argv[1])\n" +
		"mod = importlib.util.module_from_spec(spec)\n" +
		"spec.loader.exec_module(mod)\n" +
		"print(json.dumps(sorted(mod.Analyzer(sys.argv[2], 'evaluator').live_homes())))\n"
	out, err := exec.Command(py, "-I", "-c", script, filepath.Join(rdir, "roles", "hooks", "worker-guard.py"), rdir).Output()
	gcpMust(t, err)
	var homes []string
	gcpMust(t, json.Unmarshal(out, &homes))
	sort.Strings(homes)
	has := func(p string) bool {
		for _, h := range homes {
			if h == p {
				return true
			}
		}
		return false
	}
	if !has(rdir) {
		t.Errorf("live homes %v lack the trial role dir %s", homes, rdir)
	}
	if has(trial) || has(filepath.Dir(trial)) {
		t.Errorf("live homes %v hold the trial dir or the trials dir", homes)
	}
	// the Python half of this case asserts "mirrors the repo" in evaluator_settings.__doc__: a docstring, not ported
}

// ---- Group B: the copy inside a real allow-list sandbox ----

type gcpBox struct {
	t          *testing.T
	py         string
	root, home string
	oldGuard   string // where the hook used to point
	newGuard   string
	trial      string
}

func gcpBoxFix(t *testing.T) *gcpBox {
	t.Helper()
	sbxNeedUserns(t)
	py := gcpPython(t)
	repo := gcpRepo(t)
	b := &gcpBox{t: t, py: py, root: sbxRealTemp(t)}
	b.home = filepath.Join(b.root, "home")
	b.oldGuard = filepath.Join(b.home, "factory", "roles", "hooks", "worker-guard.py")
	gcpCopy(t, filepath.Join(repo, "roles", "hooks", "worker-guard.py"), b.oldGuard, 0o644)
	b.trial = filepath.Join(b.root, "trials", "t1")
	b.newGuard = filepath.Join(b.trial, "role", "roles", "hooks", "worker-guard.py")
	gcpCopy(t, filepath.Join(repo, "roles", "hooks", "worker-guard.py"), b.newGuard, 0o555)
	gcpMust(t, os.MkdirAll(filepath.Join(b.trial, "fh"), 0o755))
	gcpMust(t, os.MkdirAll(filepath.Join(b.trial, "work"), 0o755))
	return b
}

// hook runs the guard script as the evaluator's hook inside the sandbox and returns its exit code and stderr.
func (b *gcpBox) hook(script, command string) (int, string) {
	b.t.Helper()
	o := sandbox.NewOpts(b.trial)
	o.Home, o.TrialsRoot, o.Mode, o.Unshare = b.home, filepath.Join(b.root, "trials"), "allow", "unshare"
	o.HideDirs, o.HideFiles, o.Cwd = []string{b.home}, nil, filepath.Join(b.trial, "work")
	cfg, err := sandbox.MakeCfg(o)
	gcpMust(b.t, err)
	payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}, "cwd": filepath.Join(b.trial, "work")})
	gcpMust(b.t, err)
	argv, err := sandbox.ComposeStage1(cfg, []string{b.py, script, "--role", "evaluator"})
	gcpMust(b.t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "FACTORY_ROLE=evaluator", "FACTORY_AGENT=zz-ev",
		"FACTORY_HOME=" + filepath.Join(b.trial, "fh"), "HOME=" + filepath.Join(b.trial, "home")}
	cmd.Stdin = bytes.NewReader(payload)
	var eb bytes.Buffer
	cmd.Stderr = &eb
	err = cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode(), eb.String()
	}
	gcpMust(b.t, err)
	return 0, eb.String()
}

func TestSkillevalGuardCopy_OldPathIsHiddenSoTheHookCannotStart(t *testing.T) {
	b := gcpBoxFix(t)
	code, stderr := b.hook(b.oldGuard, "factory whoami")
	if code == 0 || !strings.Contains(stderr, "can't open file") {
		t.Fatalf("code %d stderr %q", code, stderr)
	}
}

func TestSkillevalGuardCopy_CopyAllowsAnAllowedCommand(t *testing.T) {
	b := gcpBoxFix(t)
	for _, cmd := range []string{"factory whoami", "factory task show T-0001", "git status", "python3 -m unittest discover -s tests"} {
		if code, stderr := b.hook(b.newGuard, cmd); code != 0 {
			t.Errorf("%s: code %d: %s", cmd, code, stderr)
		}
	}
}

func TestSkillevalGuardCopy_CopyStillRefuses(t *testing.T) {
	b := gcpBoxFix(t)
	for _, cmd := range []string{"factory config git_push 1", "factory spawn coder", "herdr pane list"} {
		code, stderr := b.hook(b.newGuard, cmd)
		if code != 2 || !strings.Contains(stderr, "blocked") {
			t.Errorf("%s: rc=%d %s", cmd, code, stderr)
		}
	}
}
