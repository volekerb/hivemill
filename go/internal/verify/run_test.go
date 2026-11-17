package verify

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
)

const reTrace = `^verify: (step [0-9]+ [a-z0-9_-]+ (ok|fail) [0-9]+ms( .*)?|result (PASS|FAIL) mode=(cli|mod|web) steps=[0-9]+ ms=[0-9]+( reason=.+)?)$`

type harness struct {
	t       *testing.T
	home    string
	tmpdir  string
	environ []string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	bin, err := filepath.Abs("testdata/bin")
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{t: t, home: t.TempDir(), tmpdir: t.TempDir()}
	h.environ = []string{"PATH=" + bin + ":/usr/bin:/bin", "HOME=" + h.home, "TMPDIR=" + h.tmpdir, "FACTORY_OTHER=leak", "FACTORY_HOME=" + filepath.Join(h.home, "inherited")}
	return h
}

func (h *harness) tree() string {
	p, err := filepath.Abs("testdata/tree")
	if err != nil {
		h.t.Fatal(err)
	}
	return p
}

func (h *harness) run(args ...string) (int, string, []string) {
	h.t.Helper()
	var out bytes.Buffer
	code := Run(args, h.environ, &out)
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	return code, out.String(), lines
}

func (h *harness) noLeftover() {
	h.t.Helper()
	m, _ := filepath.Glob(filepath.Join(h.tmpdir, "verify-home-*"))
	if len(m) != 0 {
		h.t.Fatalf("temp home left behind: %v", m)
	}
}

func lastLine(lines []string) string { return lines[len(lines)-1] }

func TestCLIPass(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "out")
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--out", out, "--setup", "setup one", "--expect-stdout", "^ok goal list", "--expect-stderr", "^$", "--", "goal", "list")
	if code != 0 || !strings.HasPrefix(lastLine(lines), "verify: result PASS mode=cli steps=") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	re := regexp.MustCompile(reTrace)
	for _, l := range lines {
		if !re.MatchString(l) {
			t.Errorf("line breaks the trace format: %q", l)
		}
	}
	saved, err := os.ReadFile(filepath.Join(out, "trace.txt"))
	if err != nil || string(saved) != stdout {
		t.Errorf("trace.txt differs from stdout (err=%v)", err)
	}
	for _, want := range []string{"impl=python", "setup-1 ok", "check-stdout-1 ok", "check-stderr-1 ok"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("trace lacks %q:\n%s", want, stdout)
		}
	}
	h.noLeftover()
}

func TestCLIChildEnvIsolated(t *testing.T) {
	h := newHarness(t)
	out := filepath.Join(t.TempDir(), "out")
	code, stdout, _ := h.run("cli", "--tree", h.tree(), "--out", out, "--", "env")
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	b, _ := os.ReadFile(filepath.Join(out, "stdout.txt"))
	got := string(b)
	if !strings.Contains(got, "other=unset") || !strings.Contains(got, "impl=python") || strings.Contains(got, "inherited") {
		t.Errorf("child env not isolated: %s", got)
	}
	if !strings.Contains(got, "home="+h.tmpdir+"/verify-home-") || !strings.Contains(got, "h="+h.tmpdir+"/verify-home-") {
		t.Errorf("child did not get the temp home: %s", got)
	}
}

func TestCLIWrongExitFails(t *testing.T) {
	h := newHarness(t)
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--expect-exit", "0", "--", "exit3")
	if code != 1 || !strings.Contains(lastLine(lines), "result FAIL mode=cli") || !strings.Contains(lastLine(lines), "reason=exit code 3, expected 0") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	h.noLeftover()
}

func TestCLIExpectedNonZeroExitPasses(t *testing.T) {
	h := newHarness(t)
	code, stdout, _ := h.run("cli", "--tree", h.tree(), "--expect-exit", "3", "--expect-stdout", "out line", "--expect-stderr", "err", "--", "exit3")
	if code != 0 {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
}

func TestCLIRegexMissFails(t *testing.T) {
	h := newHarness(t)
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--expect-stdout", "never-printed", "--", "goal", "list")
	if code != 1 || !strings.Contains(lastLine(lines), "reason=stdout does not match /never-printed/") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
}

func TestCLISetupFailureFails(t *testing.T) {
	h := newHarness(t)
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--setup", "fail", "--", "goal", "list")
	if code != 1 || !strings.Contains(lastLine(lines), `setup "fail" exit 1: boom`) {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	if strings.Contains(stdout, " run ") {
		t.Errorf("run step must not start after a failed setup:\n%s", stdout)
	}
}

func TestCLITimeoutKillsChildren(t *testing.T) {
	h := newHarness(t)
	pidfile := filepath.Join(t.TempDir(), "pid")
	h.environ = append(h.environ, "VERIFY_TEST_PIDFILE="+pidfile)
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--timeout", "400ms", "--", "sleep")
	if code != 1 || !strings.Contains(lastLine(lines), "result FAIL") || !strings.Contains(lastLine(lines), "timeout") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	b, err := os.ReadFile(pidfile)
	if err != nil {
		t.Fatalf("fake never started: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(pid, 0); err != syscall.ESRCH {
		t.Errorf("fake factory pid %d is still alive (kill -0 gave %v)", pid, err)
	}
	if !strings.Contains(stdout, "cleanup ok") {
		t.Errorf("cleanup step must pass after a timeout:\n%s", stdout)
	}
	h.noLeftover()
}

func TestModPass(t *testing.T) {
	h := newHarness(t)
	log := filepath.Join(t.TempDir(), "node.log")
	h.environ = append(h.environ, "VERIFY_TEST_LOG="+log)
	code, stdout, lines := h.run("mod", "--tree", h.tree())
	if code != 0 || !strings.HasPrefix(lastLine(lines), "verify: result PASS mode=mod") || !strings.Contains(stdout, "dashboard.test.mjs") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	b, _ := os.ReadFile(log)
	if got := string(b); strings.Count(got, "--check") != 2 || !strings.Contains(got, "--test plugins/hivemill/herdr-app/tests/dashboard.test.mjs") {
		t.Errorf("node calls: %q", got)
	}
	h.noLeftover()
}

func TestModNodeExitOneFails(t *testing.T) {
	h := newHarness(t)
	h.environ = append(h.environ, "FAKE_NODE_EXIT=1")
	code, stdout, lines := h.run("mod", "--tree", h.tree())
	if code != 1 || !strings.Contains(lastLine(lines), "result FAIL mode=mod") || !strings.Contains(lastLine(lines), "reason=") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
}

func TestModMissingNodeIsCouldNotRun(t *testing.T) {
	h := newHarness(t)
	h.environ = append(h.environ, "PATH=")
	code, stdout, lines := h.run("mod", "--tree", h.tree())
	if code != 1 || !strings.Contains(lastLine(lines), "reason=could not run: node not found on PATH") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	h.noLeftover()
}

func TestModBadJSONFails(t *testing.T) {
	h := newHarness(t)
	tree := t.TempDir()
	if err := os.CopyFS(tree, os.DirFS(h.tree())); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tree, modDir, "mod.json"), []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, stdout, lines := h.run("mod", "--tree", tree)
	if code != 1 || !strings.Contains(lastLine(lines), "mod.json is not valid JSON") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
}

// TestLiveHomeRefused injects the live home: the temp dir base is inside $HOME/factory.
func TestLiveHomeRefused(t *testing.T) {
	h := newHarness(t)
	live := filepath.Join(h.home, "factory", "tmp")
	if err := os.MkdirAll(live, 0o755); err != nil {
		t.Fatal(err)
	}
	h.environ = append(h.environ, "TMPDIR="+live)
	code, stdout, lines := h.run("cli", "--tree", h.tree(), "--", "goal", "list")
	if code != 1 || !strings.Contains(lastLine(lines), "reason=could not run: live home") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
	if m, _ := filepath.Glob(filepath.Join(live, "*")); len(m) != 0 {
		t.Errorf("harness wrote into the live home: %v", m)
	}
}

func TestLiveHomeRefusedViaInheritedFactoryHome(t *testing.T) {
	h := newHarness(t)
	h.environ = append(h.environ, "FACTORY_HOME="+h.tmpdir)
	code, stdout, lines := h.run("mod", "--tree", h.tree())
	if code != 1 || !strings.Contains(lastLine(lines), "reason=could not run: live home") {
		t.Fatalf("code=%d\n%s", code, stdout)
	}
}

func TestBadInputIsFail(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{}, {"bogus"}, {"cli", "--nope"}, {"cli", "--tree", h.tree()}, {"cli", "--impl", "rust", "--", "x"},
		{"cli", "--expect-stdout", "(", "--", "x"}, {"mod", "--timeout", "0s"}, {"web"},
	} {
		code, stdout, lines := h.run(args...)
		if code != 1 || !strings.Contains(lastLine(lines), "result FAIL") || !strings.Contains(lastLine(lines), "reason=") {
			t.Errorf("args %q: code=%d\n%s", args, code, stdout)
		}
	}
	code, _, lines := h.run("cli", "--tree", filepath.Join(h.tmpdir, "missing"), "--", "goal", "list")
	if code != 1 || !strings.Contains(lastLine(lines), "reason=could not run: tree") {
		t.Errorf("missing tree: %v", lines)
	}
	h.noLeftover()
}

func FuzzParseArgs(f *testing.F) {
	for _, s := range []string{"cli --tree . -- goal list", "mod --timeout 5s", "cli --expect-stdout ( -- x", "", "web"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		// Any input must parse or return an error; it must never panic.
		_, _ = ParseArgs(strings.Fields(s))
	})
}
