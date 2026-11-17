package sandbox1

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	sb "factory/go/internal/sandbox"
)

// Port of tests/test_gatebox.py (20 cases; see ported_sandbox1.txt). The Python module drives scout/gatebox.py; the Go
// counterpart is sandbox.BuildGate / RunGate. Real-namespace cases run the test binary itself as stage 1 (TestMain) and
// skip with the reason where user namespaces are unavailable.
//
// Differences from the Python file, all in how a probe is spelled, not in what is asserted:
//   - the Python probes (socket.connect, socket.create_connection, py_compile) become perl / bash /dev/tcp one-liners,
//     so only the py_compile case needs python3 (it checks PYTHONPYCACHEPREFIX, a Python interpreter feature);
//   - the exception text "FileNotFoundError" / "Error" of a Python socket probe becomes perl's "No such file or directory" /
//     any failing connect (the exit code is the assertion; the text only shows which refusal it was).
// not ported: none.

// gbFixture is Fixture of the Python module: a fake home with fake credentials, a runtime dir with a listening bus
// socket, a loopback TCP listener, a repo whose git common dir sits OUTSIDE main, and a gate tree (a worktree) below main.
type gbFixture struct {
	t                                   *testing.T
	root, home, runtime, main, wt, repo string
	tree, sha                           string
	env                                 map[string]string
	bus                                 *net.UnixListener
	tcp                                 net.Listener
	port                                int
}

func gbMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func gbPut(t testing.TB, path, body string) {
	t.Helper()
	gbMust(t, os.MkdirAll(filepath.Dir(path), 0o755))
	gbMust(t, os.WriteFile(path, []byte(body), 0o644))
}

func gbRead(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func gbExists(path string) bool { _, err := os.Lstat(path); return err == nil }

// gbGit runs git in dir with a fixed identity and no global config; it returns trimmed stdout.
func gbGit(t testing.TB, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// gbUserns skips the test with the probe's reason when this box cannot run the namespaces.
func gbUserns(t testing.TB) {
	t.Helper()
	if ok, why := sb.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
}

func gbNeedTool(t testing.TB, name string) string {
	t.Helper()
	p, err := exec.LookPath(name)
	if err != nil {
		t.Skipf("%s is not on PATH: this case drives it inside the box", name)
	}
	return p
}

// gbNew builds the fixture. hideTmp is the HideTmp the profile runs with ("/tmp", "/dev/shm" for the fixture, whose root
// is under /var/tmp; gbRealTmp = the real list).
func gbNew(t *testing.T, hideTmp []string) *gbFixture {
	t.Helper()
	if fi, err := os.Stat("/var/tmp"); err != nil || !fi.IsDir() {
		t.Skip("/var/tmp is not usable: the fixture must not sit below /tmp or /dev/shm")
	}
	root, err := os.MkdirTemp("/var/tmp", "zz-test-gatebox-")
	if err != nil {
		t.Skipf("cannot create a fixture under /var/tmp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	root = sb.Realpath(root)
	f := &gbFixture{t: t, root: root}
	f.home, f.runtime, f.main, f.wt, f.repo = root+"/home", root+"/run", root+"/main", root+"/wt", root+"/repo"
	for p, body := range map[string]string{".claude/.credentials.json": "FAKE-CLAUDE", ".config/gh/hosts.yml": "FAKE-GH", ".ssh/id_ed25519": "FAKE-SSH"} {
		gbPut(t, f.home+"/"+p, body)
	}
	gbMust(t, os.MkdirAll(f.runtime, 0o755))
	f.bus, err = net.ListenUnix("unix", &net.UnixAddr{Name: f.runtime + "/bus", Net: "unix"})
	gbMust(t, err)
	t.Cleanup(func() { f.bus.Close() })
	f.tcp, err = net.Listen("tcp", "127.0.0.1:0")
	gbMust(t, err)
	t.Cleanup(func() { f.tcp.Close() })
	f.port = f.tcp.Addr().(*net.TCPAddr).Port
	// the git common dir lives OUTSIDE main (like a repo whose worktrees are checked out into FACTORY_MAIN)
	gbMust(t, os.MkdirAll(f.repo, 0o755))
	gbGit(t, f.repo, "init", "-q", "-b", "main")
	gbPut(t, f.repo+"/f.txt", "x\n")
	gbGit(t, f.repo, "add", ".")
	gbGit(t, f.repo, "commit", "-qm", "init")
	f.sha = gbGit(t, f.repo, "rev-parse", "HEAD")
	gbPut(t, f.main+"/tests/gate_shards.py", "SHARDS = 1\n")
	for _, d := range sb.Masked {
		gbMust(t, os.MkdirAll(f.main+"/"+d, 0o755))
	}
	gbPut(t, f.main+"/db/factory.db", "LIVE-DB")
	f.tree = f.main + "/worktrees/gate-tree"
	gbGit(t, f.repo, "worktree", "add", "-q", "--detach", f.tree, f.sha)
	f.env = map[string]string{"PATH": os.Getenv("PATH"), "LANG": "C.UTF-8", "TERM": "dumb", "GH_TOKEN": "tok", "SSH_AUTH_SOCK": "/x",
		"FACTORY_AGENT": "coder-x", "DBUS_SESSION_BUS_ADDRESS": "unix:path=/x", "LC_ALL": "C.UTF-8", "GATE_SHARDS": "3"}
	old := sb.HideTmp
	sb.HideTmp = hideTmp
	t.Cleanup(func() { sb.HideTmp = old }) // a package var: these tests never run in parallel
	return f
}

// gbFixtureTmp is Fixture.fixture_hide_tmp: the fixture root is under /var/tmp, which the profile hides too.
func gbFixtureTmp() []string { return []string{"/tmp", "/dev/shm"} }

func (f *gbFixture) args(cmd string) sb.GateArgs {
	return sb.GateArgs{Cmd: cmd, Tree: f.tree, FactoryMain: f.main, WtRoot: f.wt, Env: f.env, Home: f.home, Runtime: f.runtime}
}

// build is Fixture.build with the userns probe patched to "available" (the pure argv/env cases must pass on runners
// without user namespaces).
func (f *gbFixture) build(cmd string) ([]string, map[string]string, error) {
	a := f.args(cmd)
	a.Probe = func(bool) (bool, string) { return true, "" }
	return sb.BuildGate(a)
}

func (f *gbFixture) run(cmd string) sb.Contained {
	return f.runT(cmd, 120*time.Second)
}

func (f *gbFixture) runT(cmd string, timeout time.Duration) sb.Contained {
	f.t.Helper()
	exe, err := os.Executable()
	gbMust(f.t, err)
	a := f.args(cmd)
	a.Seams.Self = exe
	r, err := sb.RunGate(a, timeout)
	if err != nil {
		f.t.Fatalf("RunGate(%q): %v", cmd, err)
	}
	return r
}

// gbSetupCfg is json.loads(argv[argv.index("--setup") + 1]).
func gbSetupCfg(t testing.TB, argv []string) *sb.Cfg {
	t.Helper()
	for i, a := range argv {
		if a == "--setup" && i+1 < len(argv) {
			var c sb.Cfg
			gbMust(t, json.Unmarshal([]byte(argv[i+1]), &c))
			return &c
		}
	}
	t.Fatalf("no --setup cfg in %v", argv)
	return nil
}

func gbSorted(s []string) []string { c := append([]string{}, s...); sort.Strings(c); return c }

func gbEq(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(gbSorted(got), "\x00") != strings.Join(gbSorted(want), "\x00") {
		t.Fatalf("%s = %q, want %q", what, gbSorted(got), gbSorted(want))
	}
}

func gbHas(t testing.TB, what, hay, needle string) {
	t.Helper()
	if !strings.Contains(hay, needle) {
		t.Fatalf("%s = %q, want it to contain %q", what, hay, needle)
	}
}

func gbHasNot(t testing.TB, what, hay, needle string) {
	t.Helper()
	if strings.Contains(hay, needle) {
		t.Fatalf("%s = %q, must not contain %q", what, hay, needle)
	}
}

// ---- ProfileTests ----------------------------------------------------------------------------------------------

func TestGateboxProfileEnvIsAnAllowList(t *testing.T) {
	f := gbNew(t, gbFixtureTmp())
	_, env, err := f.build("true")
	gbMust(t, err)
	for _, k := range []string{"GH_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT", "DBUS_SESSION_BUS_ADDRESS"} {
		if _, has := env[k]; has {
			t.Errorf("%s reached the gate env", k)
		}
	}
	for k, want := range map[string]string{"HOME": f.home, "TMPDIR": f.wt + "/tmp", "PYTHONDONTWRITEBYTECODE": "1",
		"PYTHONPYCACHEPREFIX": f.wt + "/tmp/pycache", "GATE_SHARDS": "3", "LC_ALL": "C.UTF-8", "FACTORY_HOME": f.wt + "/factory-home",
		"GATE_REPORT_DIR": f.wt + "/reports"} {
		if env[k] != want {
			t.Errorf("env[%s] = %q, want %q", k, env[k], want)
		}
	}
}

func TestGateboxProfileArgvIsTheAllowProfile(t *testing.T) {
	f := gbNew(t, gbFixtureTmp())
	argv, _, err := f.build("true")
	gbMust(t, err)
	cfg := gbSetupCfg(t, argv)
	if cfg.Mode != "allow" || cfg.Net || cfg.NoPid {
		t.Errorf("mode=%q net=%v nopid=%v, want allow, no net, own pid namespace", cfg.Mode, cfg.Net, cfg.NoPid)
	}
	gbEq(t, "rw", cfg.Rw, []string{f.tree, f.wt + "/reports", f.wt + "/factory-home", f.wt + "/tmp"})
	ro := []string{f.main, f.repo + "/.git", f.tree + "/.git"} // the tree is a worktree: its .git pointer file is ro
	for _, d := range sb.RunKeepRO("/etc/resolv.conf") {       // the DNS dir, ro (T-0673)
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			ro = append(ro, d)
		}
	}
	gbEq(t, "ro", cfg.Ro, ro)
	var masks []string
	for _, d := range sb.Masked {
		masks = append(masks, f.main+"/"+d)
	}
	gbEq(t, "masks", cfg.Masks, masks)
	gbEq(t, "hide_dirs", cfg.HideDirs, []string{f.home, f.runtime, "/tmp", "/dev/shm", "/run"}) // /var/tmp is patched out here
	if n := len(argv); n < 3 || strings.Join(argv[n-3:], " ") != "bash -c true" {
		t.Errorf("argv tail = %q, want bash -c true", argv)
	}
}

func TestGateboxProfileUnavailableUsernsRaisesAndNeverRuns(t *testing.T) {
	f := gbNew(t, gbFixtureTmp())
	marker := f.root + "/ran"
	a := f.args("touch " + marker)
	a.Probe = func(bool) (bool, string) { return false, "denied" }
	argv, _, err := sb.BuildGate(a)
	var ge *sb.GateboxError
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "unsandboxed") || argv != nil {
		t.Fatalf("BuildGate = %v, %v; want a GateboxError naming 'unsandboxed' and no argv", argv, err)
	}
	if _, err := sb.RunGate(a, 30*time.Second); !errors.As(err, &ge) {
		t.Fatalf("RunGate err = %v, want a GateboxError", err)
	}
	if gbExists(marker) {
		t.Fatal("the command ran although the sandbox was unavailable")
	}
}

func TestGateboxProfileBadPathsRaise(t *testing.T) {
	f := gbNew(t, gbFixtureTmp())
	a := f.args("true")
	a.Tree = f.root + "/nope"
	a.Probe = func(bool) (bool, string) { return true, "" }
	_, _, err := sb.BuildGate(a)
	var ge *sb.GateboxError
	if !errors.As(err, &ge) || !regexp.MustCompile(`gate tree .* is not a directory`).MatchString(err.Error()) {
		t.Fatalf("err = %v, want a GateboxError 'gate tree ... is not a directory'", err)
	}
}

// ---- VarTmp ----------------------------------------------------------------------------------------------------

func TestGateboxVarTmpProfileHiddenWithTheOtherTmpDirs(t *testing.T) {
	f := gbNew(t, gbRealTmp()) // the real HIDE_TMP
	argv, _, err := f.build("true")
	gbMust(t, err)
	cfg := gbSetupCfg(t, argv)
	found := false
	for _, d := range cfg.HideDirs {
		found = found || d == "/var/tmp"
	}
	if !found {
		t.Errorf("hide_dirs = %v, want /var/tmp in it", cfg.HideDirs)
	}
	if cfg.TmpfsSizes["/var/tmp"] != "16m" {
		t.Errorf("tmpfs_sizes[/var/tmp] = %q, want 16m", cfg.TmpfsSizes["/var/tmp"])
	}
}

// gbRealTmp is the real HideTmp list of the profile.
func gbRealTmp() []string { return []string{"/tmp", "/dev/shm", "/var/tmp"} }

func TestGateboxVarTmpWritesDoNotPersist(t *testing.T) {
	gbUserns(t)
	f := gbNew(t, gbRealTmp()) // the fixture root (under /var/tmp) is itself hidden, rw dirs are bound back
	n := filepath.Base(f.root)
	r := f.run("echo x > /var/tmp/" + n + "-leak && cat /var/tmp/" + n + "-leak && echo ok > " + f.wt + "/reports/x")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	if gbExists("/var/tmp/" + n + "-leak") {
		t.Error("a /var/tmp write persisted on the host")
	}
	if got := gbRead(t, f.wt+"/reports/x"); got != "ok\n" { // the rw dirs under /var/tmp are bound back
		t.Errorf("reports/x = %q, want ok", got)
	}
}

func TestGateboxVarTmpNotReadableFromTheGate(t *testing.T) {
	gbUserns(t)
	f := gbNew(t, gbRealTmp())
	gbPut(t, f.root+"/secret.txt", "SECRET")
	r := f.run("cat " + f.root + "/secret.txt")
	if r.ReturnCode == 0 {
		t.Errorf("rc=0, want a failure: %s", r.Output)
	}
	gbHasNot(t, "output", r.Output, "SECRET")
}

// ---- HomeLayoutTests -------------------------------------------------------------------------------------------

// gbHomeLayout is HomeLayoutTests.setUp: FACTORY_MAIN, the gate trees and wt_root all live UNDER the hidden $HOME
// (~/factory, ~/.cache/factory-gate/<id>). Fake HOME, never the live one.
func gbHomeLayout(t *testing.T) *gbFixture {
	gbUserns(t)
	f := gbNew(t, gbFixtureTmp())
	gbMust(t, os.Rename(f.root+"/main", f.home+"/factory"))
	f.main = f.home + "/factory"
	f.wt = f.home + "/.cache/factory-gate/7"
	f.tree = f.wt + "/head"
	gbMust(t, os.MkdirAll(filepath.Dir(f.tree), 0o755))
	gbGit(t, f.repo, "worktree", "prune") // the moved tree's old registration is stale
	gbGit(t, f.repo, "worktree", "add", "-q", "--detach", f.tree, f.sha)
	return f
}

func TestGateboxHomeLayoutGateRunsWithEverythingUnderHome(t *testing.T) {
	f := gbHomeLayout(t)
	r := f.run("cat " + f.main + "/tests/gate_shards.py; git rev-parse HEAD; echo hi > " + f.wt + "/reports/x; " +
		"touch " + f.tree + "/y; echo z > " + f.main + "/z; echo cred=[$(cat " + f.home + "/.config/gh/hosts.yml 2>&1)]")
	gbHas(t, "output", r.Output, "SHARDS = 1")
	gbHas(t, "output", r.Output, f.sha)
	gbHas(t, "output", r.Output, "Read-only file system")
	gbHas(t, "output", r.Output, "No such file")
	gbHasNot(t, "output", r.Output, "FAKE-GH")
	if got := gbRead(t, f.wt+"/reports/x"); got != "hi\n" {
		t.Errorf("reports/x = %q, want hi", got)
	}
	if got := gbRead(t, f.tree+"/y"); got != "" { // the gate tree is rw
		t.Errorf("tree/y = %q, want empty", got)
	}
	if gbExists(f.main + "/z") {
		t.Error("a write to FACTORY_MAIN reached the host")
	}
}

func TestGateboxHomeLayoutHomeTmpfsWritesStayInTheBox(t *testing.T) {
	f := gbHomeLayout(t)
	r := f.run("echo x > " + f.home + "/stray; mkdir -p " + f.home + "/.cache/pip && echo y > " + f.home + "/.cache/pip/z")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	if gbExists(f.home + "/stray") {
		t.Error("a write under the hidden HOME persisted")
	}
	if gbExists(f.home + "/.cache/pip") {
		t.Error("a directory made under the hidden HOME persisted")
	}
}

// ---- RealNamespaceTests ----------------------------------------------------------------------------------------

func gbReal(t *testing.T) *gbFixture {
	gbUserns(t)
	return gbNew(t, gbFixtureTmp())
}

func TestGateboxRealFakeCredentialsUnreadable(t *testing.T) {
	f := gbReal(t)
	for _, rel := range []string{".claude/.credentials.json", ".config/gh/hosts.yml", ".ssh/id_ed25519"} {
		r := f.run("cat " + f.home + "/" + rel)
		if r.ReturnCode == 0 {
			t.Errorf("%s: rc=0, want a failure: %s", rel, r.Output)
		}
		gbHasNot(t, rel+" output", r.Output, "FAKE-")
	}
}

// gbDial is a unix-socket connect probe: perl, as a Python-free stand-in for socket.connect.
func gbDial(path string) string {
	return "perl -MSocket -e 'socket(S, PF_UNIX, SOCK_STREAM, 0) or die \"socket: $!\"; connect(S, sockaddr_un($ARGV[0])) or die \"connect: $!\"' " + path
}

func TestGateboxRealUserBusSocketUnreachable(t *testing.T) {
	f := gbReal(t)
	gbNeedTool(t, "perl")
	r := f.run(gbDial(f.runtime + "/bus"))
	if r.ReturnCode == 0 {
		t.Fatalf("rc=0, want a failure: %s", r.Output)
	}
	gbHas(t, "output", r.Output, "connect:")
	f.bus.SetDeadline(time.Now().Add(200 * time.Millisecond))
	if c, err := f.bus.Accept(); err == nil { // nothing ever reached the real listener
		c.Close()
		t.Fatal("the real bus listener accepted a connection from the box")
	} else if ne, ok := err.(net.Error); !ok || !ne.Timeout() {
		t.Fatalf("accept: %v, want a timeout", err)
	}
}

func TestGateboxRealHostRunSocketsUnreachable(t *testing.T) {
	f := gbReal(t)
	gbNeedTool(t, "perl")
	// T-0673: the container-engine and ssh-unix-local sockets under the host /run are masked in the gate profile.
	var socks []string
	for _, p := range []string{"/run/docker.sock", "/var/run/docker.sock", "/run/ssh-unix-local/socket"} {
		if c, err := net.Dial("unix", p); err == nil { // control: connectable outside, or not asserted
			c.Close()
			socks = append(socks, p)
		}
	}
	if len(socks) == 0 {
		t.Skip("no connectable host /run socket on this box")
	}
	for _, p := range socks {
		r := f.run(gbDial(p))
		if r.ReturnCode == 0 {
			t.Errorf("%s: rc=0, want a failure: %s", p, r.Output)
		}
		gbHas(t, p+" output", r.Output, "No such file")
	}
}

func TestGateboxRealOutsideLoopbackListenerUnreachable(t *testing.T) {
	f := gbReal(t)
	// bash /dev/tcp instead of python's create_connection; the net namespace has lo only and nothing listens there
	r := f.run("exec 3<>/dev/tcp/127.0.0.1/" + strconv.Itoa(f.port))
	if r.ReturnCode == 0 {
		t.Fatalf("rc=0, want a failure: %s", r.Output)
	}
	if !regexp.MustCompile(`refused|unreachable|Errno`).MatchString(r.Output) {
		t.Errorf("output = %q, want refused|unreachable|Errno", r.Output)
	}
}

func TestGateboxRealMainAndGitCommonDirReadOnlyTreeWritable(t *testing.T) {
	f := gbReal(t)
	for _, p := range []string{f.main + "/x", f.repo + "/.git/hooks/x", f.repo + "/.git/config"} {
		var before []byte
		existed := gbExists(p)
		if existed {
			before, _ = os.ReadFile(p)
		}
		r := f.run("echo x >> " + p)
		if r.ReturnCode == 0 {
			t.Errorf("%s: rc=0, want a refusal", p)
		}
		gbHas(t, p+" output", r.Output, "Read-only file system")
		if gbExists(p) != existed {
			t.Errorf("%s: existence changed", p)
		}
		if existed {
			if after, _ := os.ReadFile(p); string(after) != string(before) {
				t.Errorf("%s was modified", p)
			}
		}
	}
	r := f.run("echo ok > " + f.tree + "/x")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	if got := gbRead(t, f.tree+"/x"); got != "ok\n" {
		t.Errorf("tree/x = %q, want ok", got)
	}
}

func TestGateboxRealHiddenTreesDoNotLeakWritesAndRwDirsPersist(t *testing.T) {
	f := gbReal(t)
	n := filepath.Base(f.root) // unique, so a leftover file of another run cannot confuse the outside check
	r := f.run("echo hi > " + f.home + "/x && echo ok > " + f.wt + "/reports/x && echo t > " + f.wt + "/tmp/x " +
		"&& echo f > " + f.wt + "/factory-home/x && echo s > /tmp/" + n + " && echo d > /dev/shm/" + n)
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	if gbExists(f.home + "/x") {
		t.Error("a write under the hidden HOME persisted")
	}
	for rel, want := range map[string]string{"/reports/x": "ok\n", "/tmp/x": "t\n", "/factory-home/x": "f\n"} {
		if got := gbRead(t, f.wt+rel); got != want {
			t.Errorf("%s = %q, want %q", rel, got, want)
		}
	}
	if gbExists("/tmp/"+n) || gbExists("/dev/shm/"+n) {
		t.Error("a /tmp or /dev/shm write persisted on the host")
	}
}

func TestGateboxRealMainReadableStateMaskedAndGitWorks(t *testing.T) {
	f := gbReal(t)
	r := f.run("cat " + f.main + "/tests/gate_shards.py; echo db=[$(ls -A " + f.main + "/db)]; " +
		"git -C " + f.tree + " rev-parse HEAD; echo home=[$(ls -A " + f.home + ")]")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	gbHas(t, "output", r.Output, "SHARDS = 1")
	gbHas(t, "output", r.Output, "db=[]")
	gbHasNot(t, "output", r.Output, "LIVE-DB")
	gbHas(t, "output", r.Output, f.sha)
	gbHas(t, "output", r.Output, "home=[]")
	if got := gbRead(t, f.main+"/db/factory.db"); got != "LIVE-DB" {
		t.Errorf("the live db changed: %q", got)
	}
}

func TestGateboxRealPyCompileWorksInTheReadOnlyTree(t *testing.T) {
	f := gbReal(t)
	gbNeedTool(t, "python3")
	gbPut(t, f.tree+"/m.py", "X = 1\n") // a tracked-style file: the tree is checked out, then bound ro
	r := f.run("python3 -m py_compile m.py && ls " + f.wt + "/tmp/pycache >/dev/null && echo compiled")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	gbHas(t, "output", r.Output, "compiled")
	if gbExists(f.tree + "/__pycache__") {
		t.Error("__pycache__ was written into the tree")
	}
}

func TestGateboxRealCallerSecretsUnsetInside(t *testing.T) {
	f := gbReal(t)
	r := f.run("env")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	for _, k := range []string{"GH_TOKEN", "SSH_AUTH_SOCK", "FACTORY_AGENT", "DBUS_SESSION_BUS_ADDRESS"} {
		gbHasNot(t, "env", r.Output, k+"=")
	}
	gbHas(t, "env", r.Output, "HOME="+f.home)
	gbHas(t, "env", r.Output, "TMPDIR="+f.wt+"/tmp")
	gbHas(t, "env", r.Output, "FACTORY_MAIN="+f.main)
}

func TestGateboxRealCapsDroppedAndOwnPidNamespace(t *testing.T) {
	f := gbReal(t)
	r := f.run("grep -E '^(CapEff|CapBnd|NoNewPrivs)' /proc/self/status; echo pid=$$; ls /proc | grep -c '^[0-9]'")
	if r.ReturnCode != 0 {
		t.Fatalf("rc=%d %s", r.ReturnCode, r.Output)
	}
	gbHas(t, "output", r.Output, "CapEff:\t0000000000000000")
	gbHas(t, "output", r.Output, "CapBnd:\t0000000000000000")
	gbHas(t, "output", r.Output, "NoNewPrivs:\t1")
	if !regexp.MustCompile(`pid=\d\b`).MatchString(r.Output) { // a fresh pid namespace: low pids only
		t.Errorf("output = %q, want pid=<one digit>", r.Output)
	}
	fields := strings.Fields(r.Output)
	n, err := strconv.Atoi(fields[len(fields)-1])
	if err != nil || n >= 10 { // only this namespace's processes are visible
		t.Errorf("visible processes = %q, want fewer than 10", fields[len(fields)-1])
	}
}

func TestGateboxRealTimeoutKillsTheTree(t *testing.T) {
	f := gbReal(t)
	// a unique sleep length per run, so the outlived-the-timeout check cannot see another test's sleeper
	secs := strconv.Itoa(300 + os.Getpid()%500)
	r := f.runT("sleep "+secs+" & echo $! > "+f.wt+"/tmp/child; wait", 2*time.Second)
	if !r.TimedOut {
		t.Fatalf("TimedOut=false, rc=%d %s", r.ReturnCode, r.Output)
	}
	inner, err := strconv.Atoi(strings.TrimSpace(gbRead(t, f.wt+"/tmp/child"))) // pid inside the namespace: gone with its init
	gbMust(t, err)
	// Python asserts pid 2 (unshare, then bash). The Go stage chain (self --setup, setpriv, bash) puts the child at a
	// higher pid; what matters is that it is a fresh namespace's low pid, so the host pid cannot be the one printed.
	if inner < 2 || inner >= 20 {
		t.Errorf("inner pid = %d, want a low pid of a fresh pid namespace", inner)
	}
	var got []string
	for i := 0; i < 20; i++ { // the kill is asynchronous: poll up to 2 s for the sleeper to be gone
		out, _ := exec.Command("pgrep", "-f", "^sleep "+secs+"$", "-u", strconv.Itoa(os.Getuid())).Output() // exit 1 = no match
		if got = strings.Fields(string(out)); len(got) == 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(got) != 0 {
		t.Errorf("a gate child outlived the timeout: pids %v", got)
	}
}
