package sandbox1

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
)

// Port of tests/test_testbox.py (27 cases; see ported_sandbox1.txt). `factory test` is not ported to Go yet, so the
// Go CLI hands it to bin/factory: those cases call e.NeedPython() and skip where python3 is absent. The cases that
// call testbox.build directly use sandbox.NewScratch / sandbox.BuildTest, and test_subid_range_parsing calls
// sandbox.SubidRange. Real namespaces skip with the reason where user namespaces are unavailable, and inside the
// boxed gate (FACTORY_GATEBOX=1), which cannot nest one.
// adapted: Python's os.environ passed to testbox.build is a fixed PATH/LANG map here (a reproducible environment).
// adapted: the python3 socket one-liners run through `python3 -c` found on PATH, as in Python (sys.executable).

const tbUsernsNone = "user namespaces unavailable"

// tbGit runs git in dir with a fixed identity and no owner-level config; a failure ends the test.
func tbGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=/nonexistent", "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
		"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func tbWrite(t testing.TB, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func tbRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func tbExists(path string) bool { _, err := os.Lstat(path); return err == nil }

func tbNested() bool { return os.Getenv("FACTORY_GATEBOX") == "1" }

// tbNeedUserns skips unless real user namespaces work here (and are not nested in the boxed gate).
func tbNeedUserns(t testing.TB) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("%s: %s", tbUsernsNone, why)
	}
	if tbNested() {
		t.Skip("needs a nested user namespace; skipped inside the boxed gate")
	}
}

// tbNeedSubuid skips unless the subordinate-uid mapping works here.
func tbNeedSubuid(t testing.TB) {
	t.Helper()
	tbNeedUserns(t)
	if ok, why := sandbox.SubuidAvailable("unshare", os.Args[0]); !ok {
		t.Skipf("subuid mapping unavailable: %s", why)
	}
}

// tbRun runs cmd with a 60 s limit and returns its exit code, stdout and stderr; a start failure ends the test.
func tbRun(t testing.TB, cmd *exec.Cmd) (code int, stdout, stderr string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var so, se strings.Builder
	c := exec.CommandContext(ctx, cmd.Path, cmd.Args[1:]...)
	c.Dir, c.Env, c.Stdin = cmd.Dir, cmd.Env, cmd.Stdin
	c.Stdout, c.Stderr = &so, &se
	c.WaitDelay = 5 * time.Second
	err := c.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		code = ee.ExitCode()
	default:
		t.Fatalf("run %v: %v", cmd.Args, err)
	}
	return code, so.String(), se.String()
}

// tbBox is the Python Base fixture: a factory home with a git clone at <home>/clone holding f.txt.
type tbBox struct {
	t     *testing.T
	e     *ft.Env
	clone string
	extra map[string]string
}

func tbNew(t *testing.T) *tbBox {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	b := &tbBox{t: t, e: e, clone: filepath.Join(e.Home, "clone"), extra: map[string]string{}}
	if err := os.MkdirAll(b.clone, 0o755); err != nil {
		t.Fatal(err)
	}
	tbGit(t, b.clone, "init", "-q", "-b", "main")
	tbWrite(t, b.clone+"/f.txt", "x\n", 0o644)
	tbGit(t, b.clone, "add", ".")
	tbGit(t, b.clone, "commit", "-qm", "init")
	return b
}

// cfg sets a config key as the director.
func (b *tbBox) cfg(k, v string) {
	b.t.Helper()
	if len(b.e.SQL("SELECT 1 FROM agents WHERE name='zz-test-director'")) == 0 {
		b.e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	}
	b.e.OKWith(ft.Opts{Agent: "zz-test-director"}, "config", k, v)
}

// rawCfg writes a config value past the CLI validation.
func (b *tbBox) rawCfg(k, v string) {
	b.t.Helper()
	d := b.e.RawDB()
	defer d.Close()
	if _, err := d.Exec("INSERT OR REPLACE INTO config(key,value) VALUES(?,?)", k, v); err != nil {
		b.t.Fatal(err)
	}
}

// ftestIn runs `factory test <args>` as zz-test-coder in dir.
func (b *tbBox) ftestIn(dir string, extra map[string]string, args ...string) ft.Result {
	b.t.Helper()
	cmd := exec.Command(b.e.GoBin, append([]string{"test"}, args...)...)
	cmd.Dir = dir
	cmd.Env = b.e.Environ("zz-test-coder", extra)
	code, so, se := tbRun(b.t, cmd)
	return ft.Result{Exit: code, Stdout: so, Stderr: se}
}

func (b *tbBox) ftest(extra map[string]string, args ...string) ft.Result {
	b.t.Helper()
	return b.ftestIn(b.clone, extra, args...)
}

func (b *tbBox) scratches() []string {
	m, _ := filepath.Glob(b.clone + "/.factory-test-*")
	return m
}

func tbContains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func tbEq[T comparable](t testing.TB, what string, got, want T) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %v, want %v", what, got, want)
	}
}

func tbNoScratch(t testing.TB, b *tbBox) {
	t.Helper()
	if s := b.scratches(); len(s) != 0 {
		t.Fatalf("scratch dirs left behind: %v", s)
	}
}

// ---- DirectAndFlag ---------------------------------------------------------------------------------------------

func TestTestboxDefaultIsZeroAndRunsDirectly(t *testing.T) {
	b := tbNew(t)
	r := b.ftest(nil, "--", "sh", "-c", `echo "agent=$FACTORY_AGENT"; exit 7`)
	tbEq(t, "exit", r.Exit, 7)
	tbContains(t, "stdout", r.Stdout, "agent=zz-test-coder") // the env is untouched: not sandboxed
}

func TestTestboxInvalidFlagIsAnErrorAndNotRun(t *testing.T) {
	b := tbNew(t)
	b.rawCfg("test_sandbox", "yes")
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "factory config test_sandbox 1")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

func TestTestboxNoCommandIsUsageError(t *testing.T) {
	b := tbNew(t)
	tbEq(t, "exit", b.ftest(nil).Exit, 2)
}

// ---- FailClosed ------------------------------------------------------------------------------------------------

func TestTestboxUnbuildableSandboxDoesNotRunTheCommand(t *testing.T) {
	b := tbNew(t)
	b.cfg("test_sandbox", "1")
	fake := filepath.Join(b.e.Home, "bin")
	tbWrite(t, fake+"/unshare", "#!/bin/sh\nexit 1\n", 0o755)
	r := b.ftest(map[string]string{"PATH": fake + string(os.PathListSeparator) + os.Getenv("PATH")}, "touch", "ran")
	if r.Exit == 0 {
		t.Fatalf("exit 0, want non-zero: %s", r.Stderr)
	}
	tbContains(t, "stderr", r.Stderr, "factory config test_sandbox 0")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
	tbNoScratch(t, b)
}

// ---- Sandboxed -------------------------------------------------------------------------------------------------

func tbSandboxed(t *testing.T) *tbBox {
	t.Helper()
	tbNeedUserns(t)
	b := tbNew(t)
	b.cfg("test_sandbox", "1")
	b.extra = map[string]string{"GIT_DIR": "/nonexistent", "GH_TOKEN": "tok", "GITHUB_TOKEN": "tok", "CLAUDE_CODE_OAUTH_TOKEN": "tok", "SSH_AUTH_SOCK": "/x/agent"}
	return b
}

func (b *tbBox) box(script string) ft.Result {
	b.t.Helper()
	return b.ftest(b.extra, "--", "sh", "-c", script)
}

func TestTestboxEnvAllowList(t *testing.T) {
	b := tbSandboxed(t)
	r := b.box(`env | grep -E "^(GIT_|GH_TOKEN|GITHUB_TOKEN|CLAUDE_CODE_OAUTH_TOKEN|SSH_AUTH_SOCK|FACTORY_AGENT)" || echo clean`)
	tbEq(t, "stdout", strings.TrimSpace(r.Stdout), "clean")
}

func TestTestboxFactoryHomeIsTheScratchNotTheRealOne(t *testing.T) {
	b := tbSandboxed(t)
	r := b.box(`echo "$FACTORY_HOME"`)
	if strings.TrimSpace(r.Stdout) == b.e.Home {
		t.Fatalf("FACTORY_HOME is the real home %s", b.e.Home)
	}
	tbContains(t, "stdout", r.Stdout, ".factory-test-")
}

func TestTestboxSignalDeathAndNonzeroExitCleanUp(t *testing.T) {
	b := tbSandboxed(t)
	tbEq(t, "exit", b.box("sh -c 'kill -9 $$'").Exit, 137)
	tbNoScratch(t, b)
}

func TestTestboxExitCodePropagates(t *testing.T) {
	b := tbSandboxed(t)
	tbEq(t, "exit 5", b.box("exit 5").Exit, 5)
	tbEq(t, "exit true", b.box("true").Exit, 0)
}

func TestTestboxNoNetworkButLo(t *testing.T) {
	b := tbSandboxed(t)
	ip := strings.Join([]string{"203", "0", "113", "5"}, ".") // no literal IP: public-text guard
	code := "import socket; socket.create_connection(('" + ip + "', 80), 2)"
	if r := b.box(`python3 -c "` + code + `"`); r.Exit == 0 {
		t.Fatalf("a connection left the box: %s", r.Stdout)
	}
	fields := strings.Fields(strings.Split(b.box("tail -n +3 /proc/net/dev").Stdout, ":")[0])
	if len(fields) != 1 || fields[0] != "lo" {
		t.Fatalf("interfaces = %v, want [lo]", fields)
	}
}

func TestTestboxOwnPidNamespace(t *testing.T) {
	b := tbSandboxed(t)
	var pid int
	if _, err := fmt.Sscan(b.box("echo $$").Stdout, &pid); err != nil || pid >= 20 {
		t.Fatalf("pid = %d (%v), want a low pid of a fresh namespace", pid, err)
	}
}

func TestTestboxTreeRwGitDirRoAndScratchRemoved(t *testing.T) {
	b := tbSandboxed(t)
	r := b.box("echo hi > out.txt; echo x > .git/planted")
	if r.Exit == 0 {
		t.Fatal("writing into .git succeeded")
	}
	tbEq(t, "out.txt", tbRead(t, b.clone+"/out.txt"), "hi\n")
	if tbExists(b.clone + "/.git/planted") {
		t.Fatal(".git/planted exists")
	}
	tbNoScratch(t, b)
}

func TestTestboxWorktreeGitPointerFileIsReadOnly(t *testing.T) {
	b := tbSandboxed(t)
	wt := filepath.Join(b.e.Home, "wt")
	tbGit(t, b.clone, "worktree", "add", "-q", "-b", "wt-branch", wt)
	if fi, err := os.Lstat(wt + "/.git"); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("%s/.git is not a pointer file: %v", wt, err)
	}
	before := tbRead(t, wt+"/.git")
	r := b.ftestIn(wt, b.extra, "--", "sh", "-c", "! echo 'gitdir: /elsewhere' > .git && ! rm -f .git && ! mv .git .git.x && echo ok > out.txt")
	tbEq(t, "exit", r.Exit, 0) // all three writes to the pointer were refused, the tree itself is rw
	tbEq(t, "out.txt", tbRead(t, wt+"/out.txt"), "ok\n")
	tbEq(t, ".git", tbRead(t, wt+"/.git"), before) // still the original pointer, not redirected
	if tbExists(wt + "/.git.x") {
		t.Fatal(".git.x exists")
	}
}

func TestTestboxTreeUnderMaskedWorktreesDirStaysRwWithRoPointer(t *testing.T) {
	tbNeedUserns(t)
	b := tbNew(t)
	main := filepath.Join(b.e.Home, "main")
	for _, d := range []string{main + "/worktrees", main + "/db"} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wt := main + "/worktrees/G-1-T-1"
	tbGit(t, b.clone, "worktree", "add", "-q", "-b", "wt2", wt)
	scratch, err := sandbox.NewScratch(wt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(scratch); err != nil {
			t.Errorf("remove %s: %v", scratch, err)
		}
	})
	argv, benv, err := sandbox.BuildTest([]string{"sh", "-c", "echo hi > out.txt; ls ../../db | wc -l; ! echo x > .git"}, wt, main, scratch,
		map[string]string{"PATH": os.Getenv("PATH"), "LANG": "C.UTF-8"}, sandbox.GateArgs{Seams: sandbox.Opts{Self: os.Args[0]}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = wt
	for k, v := range benv {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	code, so, _ := tbRun(t, cmd)
	tbEq(t, "exit", code, 0)
	tbEq(t, "stdout", strings.TrimSpace(so), "0")        // FACTORY_MAIN/db is masked
	tbEq(t, "out.txt", tbRead(t, wt+"/out.txt"), "hi\n") // the tree under the masked worktrees dir is still rw
	if !strings.HasPrefix(tbRead(t, wt+"/.git"), "gitdir: ") {
		t.Fatal(".git pointer changed")
	}
}

func TestTestboxTmpdirIsDiskScratchInTheClone(t *testing.T) {
	b := tbSandboxed(t)
	real, err := filepath.EvalSymlinks(b.clone)
	if err != nil {
		t.Fatal(err)
	}
	r := b.box(fmt.Sprintf(`case "$TMPDIR" in %s/.factory-test-*) echo ok;; *) echo "$TMPDIR";; esac; touch "$TMPDIR/a"`, real))
	tbEq(t, "stdout", strings.TrimSpace(r.Stdout), "ok")
}

func TestTestboxGitPushFails(t *testing.T) {
	b := tbSandboxed(t)
	bare := filepath.Join(b.e.Home, "bare.git")
	tbGit(t, b.clone, "init", "-q", "--bare", bare)
	tbGit(t, b.clone, "remote", "add", "origin", bare)
	if r := b.box("git push origin main"); r.Exit == 0 {
		t.Fatalf("git push succeeded: %s", r.Stdout)
	}
	cmd := exec.Command("git", "-C", bare, "rev-parse", "--verify", "-q", "main")
	if code, _, _ := tbRun(t, cmd); code != 1 {
		t.Fatalf("main exists in the bare repo (rev-parse exit %d)", code)
	}
}

func TestTestboxHomeIsHidden(t *testing.T) {
	b := tbSandboxed(t)
	cred := b.e.UserHome + "/.config/gh/hosts.yml"
	tbWrite(t, cred, "FAKE-GH", 0o644)
	r := b.box("cat " + cred)
	if r.Exit == 0 || strings.Contains(r.Stdout, "FAKE-GH") {
		t.Fatalf("credential readable: exit %d out %q", r.Exit, r.Stdout)
	}
}

// ---- SubuidFailClosed ------------------------------------------------------------------------------------------

func tbSubuidFlags(t *testing.T) *tbBox {
	t.Helper()
	b := tbNew(t)
	b.cfg("test_sandbox", "1")
	b.cfg("test_sandbox_subuid", "1")
	return b
}

func TestTestboxMissingNewuidmapRefusesAndDoesNotRun(t *testing.T) {
	tbNeedUserns(t) // without user namespaces the userns refusal comes first
	b := tbSubuidFlags(t)
	bare := filepath.Join(b.e.Home, "bin")
	if err := os.MkdirAll(bare, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tool := range []string{"unshare", "setpriv", "sh", "touch", "git", "true", "python3", "env"} { // everything but newuidmap/newgidmap
		p, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("%s: %v", tool, err)
		}
		if err := os.Symlink(p, bare+"/"+tool); err != nil {
			t.Fatal(err)
		}
	}
	r := b.ftest(map[string]string{"PATH": bare}, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "newuidmap")
	tbContains(t, "stderr", r.Stderr, "test_sandbox_subuid 0")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
	tbNoScratch(t, b)
}

func TestTestboxInvalidSubuidFlagIsAnErrorAndNotRun(t *testing.T) {
	b := tbSubuidFlags(t)
	b.rawCfg("test_sandbox_subuid", "yes")
	r := b.ftest(nil, "touch", "ran")
	tbEq(t, "exit", r.Exit, 3)
	tbContains(t, "stderr", r.Stderr, "factory config test_sandbox_subuid 1")
	if tbExists(b.clone + "/ran") {
		t.Fatal("the command ran")
	}
}

func TestTestboxSubidRangeParsing(t *testing.T) {
	check := func(path, user string, uid, wantStart, wantCount int) {
		t.Helper()
		s, c := sandbox.SubidRange(path, user, uid)
		if s != wantStart || c != wantCount {
			t.Fatalf("SubidRange(%s, %q, %d) = (%d, %d), want (%d, %d)", path, user, uid, s, c, wantStart, wantCount)
		}
	}
	dir := t.TempDir()
	check("/nonexistent/subuid", "x", 1, 0, 0)
	f := dir + "/subuid"
	tbWrite(t, f, "bob:100000:65536\n1000:200000:10\nbob:5:2\n", 0o644)
	check(f, "bob", 7, 100000, 65536)
	check(f, "carol", 1000, 200000, 10)
	check(f, "carol", 1001, 0, 0)
}

// ---- SubuidBox -------------------------------------------------------------------------------------------------

type tbSubBox struct {
	*tbBox
	outside, secret string
}

func tbSubuidBox(t *testing.T) *tbSubBox {
	t.Helper()
	tbNeedSubuid(t)
	b := tbSubuidFlags(t)
	if err := os.Chmod(b.clone, 0o755); err != nil { // the box is another host uid: it reads the tree through other-read, never owns it
		t.Fatal(err)
	}
	if err := os.Chmod(b.e.Home, 0o711); err != nil { // traverse only: the box must reach the paths below it by other-x
		t.Fatal(err)
	}
	// the box reaches the paths below the home through other-x of every parent the test created (t.TempDir dirs are 0700)
	for p := b.e.Root; strings.HasPrefix(p, os.TempDir()+"/") && p != os.TempDir(); p = filepath.Dir(p) {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, fi.Mode().Perm()|0o711); err != nil {
			t.Fatal(err)
		}
	}
	for p := b.e.Home; p != "/"; p = filepath.Dir(p) { // an ancestor outside os.TempDir (GOTMPDIR under a 0700 home) cannot be opened up here
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm()&0o001 == 0 {
			t.Skipf("the subordinate uid cannot traverse %s (mode %v): run with TMPDIR under /tmp", p, fi.Mode().Perm())
		}
	}
	s := &tbSubBox{tbBox: b, outside: b.e.Home + "/outside"}
	if err := os.Mkdir(s.outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.outside, 0o755); err != nil {
		t.Fatal(err)
	}
	s.secret = s.outside + "/secret.txt"
	tbWrite(t, s.secret, "FAKE-OWNER-SECRET", 0o600)
	return s
}

func (s *tbSubBox) box(script string) ft.Result {
	s.t.Helper()
	return s.ftest(nil, "--", "sh", "-c", script)
}

func TestTestboxRunsAsASubordinateUidInAPrivateCopyOfTheTree(t *testing.T) {
	s := tbSubuidBox(t)
	r := s.box("id -u; id -g; echo hi > out.txt; echo changed > f.txt; cat f.txt out.txt; ls -n f.txt | cut -d' ' -f3,4")
	tbEq(t, "exit", r.Exit, 0)
	tbEq(t, "stdout", strings.Join(strings.Fields(r.Stdout), " "), "1 1 changed hi 1 1") // writable copy owned by the box uid
	if tbExists(s.clone + "/out.txt") {
		t.Fatal("a write reached the host tree")
	}
	tbEq(t, "f.txt", tbRead(t, s.clone+"/f.txt"), "x\n")
}

func TestTestboxExitCodeAndStdoutReachFactoryTest(t *testing.T) {
	s := tbSubuidBox(t)
	r := s.box("echo report-line; exit 5")
	tbEq(t, "exit", r.Exit, 5)
	tbEq(t, "stdout", strings.TrimSpace(r.Stdout), "report-line")
}

func TestTestboxNoHostPathIsWritableFromTheBox(t *testing.T) {
	s := tbSubuidBox(t)
	r := s.box(fmt.Sprintf("! echo x > %s/new && ! echo x > /etc/planted && echo ok", s.outside))
	tbEq(t, "stdout", strings.TrimSpace(r.Stdout), "ok")
	if m, _ := filepath.Glob(s.outside + "/new"); len(m) != 0 {
		t.Fatalf("host file created: %v", m)
	}
}

func TestTestboxCopyFailureFailsClosed(t *testing.T) {
	s := tbSubuidBox(t)
	main := s.e.Home + "/main"
	if err := os.Mkdir(main, 0o755); err != nil {
		t.Fatal(err)
	}
	argv, env, err := sandbox.BuildTest([]string{"true"}, s.clone, main, s.e.Home, map[string]string{"PATH": os.Getenv("PATH")},
		sandbox.GateArgs{Subuid: true, Seams: sandbox.Opts{Self: os.Args[0]}})
	if err != nil {
		t.Fatal(err)
	}
	i := -1
	for j, a := range argv {
		if a == "--launch" {
			i = j + 1
		}
	}
	if i < 0 {
		t.Fatalf("no --launch in %v", argv)
	}
	var cfg map[string]any
	if err := json.Unmarshal([]byte(argv[i]), &cfg); err != nil {
		t.Fatalf("launch cfg: %v", err)
	}
	cfg["copy"].(map[string]any)["size"] = "4k" // far too small for the tree: the copy fails and the command must not run
	js, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	marker := s.e.Home + "/ran"
	full := append(append([]string{}, argv[:i]...), string(js), "--", "sh", "-c", "touch "+marker)
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Dir = s.clone
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if code, _, _ := tbRun(t, cmd); code == 0 {
		t.Fatal("exit 0, want non-zero")
	}
	if tbExists(marker) {
		t.Fatal("the command ran after a failed copy")
	}
}

func TestTestboxNoCapabilitiesAndNoSupplementaryGroups(t *testing.T) {
	s := tbSubuidBox(t)
	r := s.box("grep -E '^(CapEff|CapBnd):' /proc/self/status; id -G")
	f := strings.Fields(r.Stdout)
	if len(f) < 5 || f[1] != "0000000000000000" || f[3] != "0000000000000000" {
		t.Fatalf("capabilities = %q", r.Stdout)
	}
	tbEq(t, "groups", f[len(f)-1], "1") // only the subordinate gid: no owner group, no engine group
}

// contained is nsbox directly with the /run mask OFF and no pid namespace: only the uid mapping stands between box and owner.
func (s *tbSubBox) contained(script string, ro ...string) (int, string, string) {
	s.t.Helper()
	o := sandbox.NewOpts(s.clone)
	o.Home = s.e.UserHome
	o.Mode, o.Net, o.NoPid, o.NoRunMask, o.Subuid = "allow", false, true, true, true
	o.HideDirs, o.HideFiles = nil, nil
	o.Ro = append([]string{s.clone}, ro...)
	o.Cwd = s.clone
	o.Self = os.Args[0]
	cfg, err := sandbox.MakeCfg(o)
	if err != nil {
		s.t.Fatal(err)
	}
	argv, err := sandbox.ComposeStage1(cfg, []string{"sh", "-c", script})
	if err != nil {
		s.t.Fatal(err)
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = s.clone
	return tbRun(s.t, cmd)
}

func TestTestboxOwner0600FileIsUnreadableEvenThoughVisible(t *testing.T) {
	s := tbSubuidBox(t)
	code, so, se := s.contained(fmt.Sprintf("ls %s && cat %s", s.outside, s.secret), s.outside)
	tbContains(t, "stdout", so, "secret.txt") // the path is bound in: only the uid mapping stops the read
	if strings.Contains(so, "FAKE-OWNER-SECRET") || code == 0 {
		t.Fatalf("secret read: exit %d out %q", code, so)
	}
	tbContains(t, "stderr", strings.ToLower(se), "denied")
}

func TestTestboxSocketConnectFailsWithoutTheRunMask(t *testing.T) {
	s := tbSubuidBox(t)
	dir, err := os.MkdirTemp("/tmp", "tbs") // short: a unix socket path is capped near 108 bytes, t.TempDir paths can exceed it
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("remove %s: %v", dir, err)
		}
	})
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	sock := dir + "/o.sock"
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := ln.Close(); err != nil {
			t.Errorf("close listener: %v", err)
		}
	})
	if err := os.Chmod(sock, 0o660); err != nil { // engine-socket bits: group-only access; the box is neither the owner nor in the group
		t.Fatal(err)
	}
	code, _, se := s.contained(fmt.Sprintf(`python3 -c "import socket; c = socket.socket(socket.AF_UNIX); c.connect('%s')"`, sock), dir)
	if code == 0 {
		t.Fatal("connected to the owner socket")
	}
	tbContains(t, "stderr", se, "PermissionError")
	if _, err := os.Stat("/run/docker.sock"); err == nil { // the real one, when this box has it
		code, _, se = s.contained(`python3 -c "import socket; c = socket.socket(socket.AF_UNIX); c.connect('/run/docker.sock')"`)
		if code == 0 {
			t.Fatal("connected to the engine socket")
		}
		tbContains(t, "stderr", se, "PermissionError")
	}
}

func TestTestboxKillOfAnOwnerPidFailsEvenInTheSamePidNamespace(t *testing.T) {
	s := tbSubuidBox(t)
	victim := exec.Command("sleep", "60")
	if err := victim.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := victim.Process.Kill(); err != nil {
			t.Errorf("kill victim: %v", err)
		}
		_ = victim.Wait() // the exit status of a killed sleep is not interesting
	})
	code, _, se := s.contained(fmt.Sprintf("kill -9 %d", victim.Process.Pid))
	if code == 0 {
		t.Fatal("kill succeeded")
	}
	tbContains(t, "stderr", strings.ToLower(se), "not permitted")
	if err := victim.Process.Signal(syscall.Signal(0)); err != nil { // still alive
		t.Fatalf("victim died: %v", err)
	}
}
