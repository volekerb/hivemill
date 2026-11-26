package guard2

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_worker_guard_scratch_init.py (T-0331): a coder or qa may run `factory init` and
// `bash systemd/install.sh --dry-run` only when HOME and FACTORY_HOME both resolve inside the worker's scratchpad.

const (
	siH = "HOME=@S@/h FACTORY_HOME=@S@/f"
)

var (
	siAllowed = []string{"factory init", "bash systemd/install.sh --dry-run"}
	siProject = []string{"factory project add demo /tmp/x", "factory project --gh add update demo"}
)

func siBuild(t *testing.T) string {
	t.Helper()
	root := realTemp(t)
	for _, d := range []string{"scratch/h", "scratch/f", "live/systemd", "wt/systemd", "wt2/systemd", "outside", "other/scratchpad/f"} {
		mkdirs(t, filepath.Join(root, d))
	}
	if err := os.Symlink(filepath.Join(root, "outside"), filepath.Join(root, "scratch", "esc")); err != nil {
		t.Fatal(err)
	}
	install, err := os.ReadFile(filepath.Join(repoRoot(t), "systemd", "install.sh"))
	if err != nil {
		t.Fatalf("read install.sh: %v", err)
	}
	for _, d := range []string{"live", "wt", "wt2"} {
		write(t, filepath.Join(root, d, "systemd", "install.sh"), string(install))
	}
	f, err := os.OpenFile(filepath.Join(root, "wt2", "systemd", "install.sh"), os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\necho edited\n"); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return root
}

type scratchInit struct {
	t    *testing.T
	root string
}

func newScratchInit(t *testing.T) *scratchInit { return &scratchInit{t: t, root: siBuild(t)} }

func (s *scratchInit) run(cmd, role, cwd string, extra map[string]string) res {
	s.t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		s.t.Fatal(err)
	}
	cmd = strings.NewReplacer("@S@", s.root+"/scratch", "@R@", s.root, "@L@", filepath.Join(home, "factory")).Replace(cmd)
	set := map[string]string{"WORKER_GUARD_SCRATCH": s.root + "/scratch", "WORKER_GUARD_LIVE_HOME": s.root + "/live"}
	for k, v := range extra {
		set[k] = v
	}
	return hookJSON(s.t, map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd},
		"cwd": filepath.Join(s.root, cwd)}, hookOpts{args: roleArgs(role), cwd: s.root, set: set})
}

func (s *scratchInit) r(cmd, role string) res { return s.run(cmd, role, "wt", nil) }

func (s *scratchInit) blocked(cmds []string, roles ...string) {
	s.t.Helper()
	if len(roles) == 0 {
		roles = []string{"coder", "qa"}
	}
	for _, role := range roles {
		for _, c := range cmds {
			r := s.r(c, role)
			if r.code != 2 {
				s.t.Errorf("%s: expected a block for %q, got %d (stderr %q)", role, c, r.code, r.stderr)
			}
		}
	}
}

// noPushRoles is NO_PUSH_ROLES as the Python hook defines it, so a role added there is covered by default.
func noPushRoles(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repoRoot(t), "roles", "hooks", "worker-guard.py"))
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	m := regexp.MustCompile(`(?m)^NO_PUSH_ROLES = \{([^}]*)\}`).FindSubmatch(b)
	if m == nil {
		t.Fatal("NO_PUSH_ROLES not found in roles/hooks/worker-guard.py")
	}
	var roles []string
	for _, r := range regexp.MustCompile(`"([a-z-]+)"`).FindAllSubmatch(m[1], -1) {
		roles = append(roles, string(r[1]))
	}
	sort.Strings(roles)
	return roles
}

func TestScratchInitAllowedTwoCommandsInScratchForCoderAndQA(t *testing.T) {
	s := newScratchInit(t)
	for _, role := range []string{"coder", "qa"} {
		for _, c := range siAllowed {
			r := s.r(siH+" "+c, role)
			wantExit(t, role+": "+c, r, 0)
		}
	}
}

func TestScratchInitAllowedExportedHomesForCoder(t *testing.T) {
	s := newScratchInit(t)
	wantExit(t, "export+init", s.r("export HOME=@S@/h FACTORY_HOME=@S@/f; factory init", "coder"), 0)
	wantExit(t, "export+install", s.r("export HOME=@S@/h; export FACTORY_HOME=@S@/f; bash systemd/install.sh --dry-run", "coder"), 0)
}

func TestScratchInitAllowedOtherCommandsUnchanged(t *testing.T) {
	s := newScratchInit(t)
	for _, c := range []string{"factory inbox", "factory whoami", "factory task show T-0331", "ls -l"} {
		wantExit(t, c, s.r(c, "qa"), 0)
	}
}

func TestScratchInitBlockedHomesNotBothInScratch(t *testing.T) {
	s := newScratchInit(t)
	c := "factory init"
	s.blocked([]string{
		c,
		"HOME=@S@/h " + c,
		"FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=@L@ " + c,
		"HOME=@L@ FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=f " + c,
		"HOME=@S@/h FACTORY_HOME=./f " + c,
		"HOME=@S@/h FACTORY_HOME=@S@/../outside " + c,
		"HOME=@S@/h FACTORY_HOME=@S@/esc/f " + c,
		"HOME=@S@/esc FACTORY_HOME=@S@/f " + c,
		"HOME=@S@/h FACTORY_HOME=@R@/other/scratchpad/f " + c,
		"HOME=@S@/h FACTORY_HOME=$X " + c,
		"HOME=@S@/h FACTORY_HOME=~/f " + c,
	})
}

func TestScratchInitBlockedInitLiveHomeAndUnlistedCommands(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{siH + " factory init --live-home @S@/x", siH + " factory init --live",
		"HOME=@S@/h factory up", "HOME=@S@/h factory spawn pm", "factory tick"})
}

func TestScratchInitBlockedUnsetEnvUAndSubshellForms(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{"export HOME=@S@/h FACTORY_HOME=@S@/f; unset FACTORY_HOME; factory init",
		siH + " env -u FACTORY_HOME factory init",
		siH + " env -i factory init",
		"(export HOME=@S@/h FACTORY_HOME=@S@/f); factory init",
		"export HOME=@S@/h FACTORY_HOME=@S@/f; (unset FACTORY_HOME; factory init)",
		"export HOME=@S@/h FACTORY_HOME=@S@/f; unset FACTORY_HOME; bash systemd/install.sh --dry-run"})
}

func TestScratchInitBlockedOnlyTheRealFactoryCLI(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{siH + " @S@/factory init", siH + " ./factory init", siH + " ../factory init", siH + " /usr/bin/factory init",
		siH + " python3 factory init", siH + " env factory init", siH + " command factory init"})
}

func TestScratchInitBlockedInitMatchIsStrict(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{siH + " factory --x init", siH + " factory -C @S@ init"}, "qa")
}

func TestScratchInitBlockedInstallShBashOnly(t *testing.T) {
	s := newScratchInit(t)
	var cmds []string
	for _, sh := range []string{"zsh", "sh", "dash", "ksh", "/bin/bash", "./bash"} {
		cmds = append(cmds, siH+" "+sh+" systemd/install.sh --dry-run")
	}
	s.blocked(cmds)
}

func TestScratchInitBlockedOnlyHomeAndFactoryHomeAssignments(t *testing.T) {
	s := newScratchInit(t)
	for _, c := range []string{"factory init", "bash systemd/install.sh --dry-run"} {
		s.blocked([]string{siH + " PATH=@S@/bin " + c, "PATH=@S@/bin " + siH + " " + c, siH + " BASH_ENV=@S@/e " + c,
			siH + " ENV=@S@/e " + c, siH + " GIT_CONFIG_GLOBAL=@S@/g " + c, "export PATH=@S@/bin; " + siH + " " + c,
			"export BASH_ENV=@S@/e HOME=@S@/h FACTORY_HOME=@S@/f; " + c, "FOO=1; " + siH + " " + c})
	}
}

func TestScratchInitBlockedNonASCIITextIsRefused(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{"export\u00a0HOME=@S@/h FACTORY_HOME=@S@/f; factory init",
		"export HOME=@S@/h FACTORY_HOME=@S@/f\u00a0; factory init", siH + " factory init \u00e9"})
}

func TestScratchInitBlockedStatementFormStaysAllowed(t *testing.T) {
	s := newScratchInit(t)
	for _, c := range []string{"export HOME=@S@/h; export FACTORY_HOME=@S@/f; factory init", "HOME=@S@/h; FACTORY_HOME=@S@/f; factory init"} {
		wantExit(t, c, s.r(c, "coder"), 0)
	}
}

func TestScratchInitBlockedInstallShForms(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{siH + " bash systemd/install.sh", siH + " bash systemd/install.sh --dry-run --with-serve",
		"HOME=@S@/h bash systemd/install.sh --dry-run",
		"HOME=@S@/h FACTORY_HOME=@L@ bash systemd/install.sh --dry-run",
		"HOME=@S@/h FACTORY_HOME=@S@/esc bash systemd/install.sh --dry-run"})
}

func TestScratchInitBlockedInstallShBypasses(t *testing.T) {
	s := newScratchInit(t)
	s.blocked([]string{"echo x >> systemd/install.sh; " + siH + " bash systemd/install.sh --dry-run",
		"printf x | tee -a systemd/install.sh; " + siH + " bash systemd/install.sh --dry-run",
		"true && " + siH + " bash systemd/install.sh --dry-run",
		"echo evil | " + siH + " bash -s systemd/install.sh --dry-run",
		siH + " bash -x systemd/install.sh --dry-run"})
}

func TestScratchInitBlockedEditedInstallShIsStillInspected(t *testing.T) {
	s := newScratchInit(t)
	r := s.run(siH+" bash systemd/install.sh --dry-run", "coder", "wt2", nil)
	wantExit(t, "edited install.sh", r, 2)
}

func TestScratchInitBlockedInstallShWithoutALiveCopyIsInspected(t *testing.T) {
	s := newScratchInit(t)
	r := s.run(siH+" bash systemd/install.sh --dry-run", "coder", "wt", map[string]string{"WORKER_GUARD_LIVE_HOME": s.root + "/outside"})
	wantExit(t, "no live copy", r, 2)
}

func TestScratchInitBlockedEvaluatorAndOtherRolesStayBlocked(t *testing.T) {
	s := newScratchInit(t)
	var roles []string
	for _, r := range noPushRoles(t) {
		if r != "qa" {
			roles = append(roles, r)
		}
	}
	found := false
	for _, r := range roles {
		found = found || r == "evaluator"
	}
	if !found {
		t.Fatalf("evaluator missing from %v", roles)
	}
	for _, role := range roles {
		for _, c := range siAllowed {
			wantExit(t, role+": "+c, s.r(siH+" "+c, role), 2)
		}
	}
	wantExit(t, "qa export HOME", s.r("export HOME=@S@/h; factory init", "qa"), 2)
}

func TestScratchInitBlockedFactoryProjectIsNotExempt(t *testing.T) {
	s := newScratchInit(t)
	for _, c := range siProject {
		for _, pre := range []string{siH + " ", "HOME=@S@/h "} {
			wantExit(t, pre+c, s.r(pre+c, "qa"), 2)
		}
		wantExit(t, c, s.r(siH+" "+c, "evaluator"), 2)
	}
}

func listing(t testing.TB, roots ...string) []string {
	t.Helper()
	var out []string
	for _, r := range roots {
		err := filepath.WalkDir(r, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if p != r {
				out = append(out, p)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", r, err)
		}
	}
	sort.Strings(out)
	return out
}

func TestScratchInitInstallDryRunWritesNothingListingUnchanged(t *testing.T) {
	s := newScratchInit(t)
	home, fh := filepath.Join(s.root, "scratch", "h"), filepath.Join(s.root, "scratch", "f")
	before := listing(t, home, fh)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(repoRoot(t), "systemd", "install.sh"), "--dry-run")
	cmd.Dir = s.root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FACTORY_HOME=" + fh, "USER=tester"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("install.sh --dry-run: %v", err)
	}
	after := listing(t, home, fh)
	if strings.Join(before, "\n") != strings.Join(after, "\n") {
		t.Errorf("listing changed: before %v after %v", before, after)
	}
	for _, want := range []string{"[dry-run] mkdir -p " + filepath.Join(home, ".config", "systemd", "user"),
		"[dry-run] systemctl --user enable hivemill.service"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
		t.Errorf("%s exists after a dry run (err %v)", filepath.Join(home, ".config"), err)
	}
}

func TestScratchInitRealFactoryInitCreatesTheDBUnderTheTempFactoryHome(t *testing.T) {
	ft.New(t).NeedPython()
	s := newScratchInit(t)
	home, fh := filepath.Join(s.root, "scratch", "h"), filepath.Join(s.root, "scratch", "f")
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "python3", filepath.Join(repoRoot(t), "bin", "factory"), "init")
	cmd.Dir = s.root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "FACTORY_HOME=" + fh, "USER=tester"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("factory init: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(fh, "db", "factory.db")); err != nil {
		t.Errorf("db not created: %v (%v)", err, listing(t, fh))
	}
}
