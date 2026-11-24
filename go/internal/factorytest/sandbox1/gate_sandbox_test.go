package sandbox1

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	sb "factory/go/internal/sandbox"
)

// Port of tests/test_gate_sandbox.py (15 cases; see ported_sandbox1.txt): config gate_sandbox runs `factory gate run`'s
// gate_cmd and gate_count_cmd under the gate profile. The Go CLI still hands `gate run` to bin/factory, so every case
// except Wiring calls e.NeedPython() and skips where python3 is absent (counted in the PR body). The real namespaces run
// only where user namespaces are available (skipped with the reason otherwise). Credentials and the marker are fake.
//
// Spelling differences, not assertion differences:
//   - the network probe is bash /dev/tcp instead of python's create_connection;
//   - the stub `unshare` is written into the harness stub dir, which the Python CLI finds first on PATH;
//   - Wiring (gate_argv passes the runtime dir explicitly) has no Go twin of gate_argv; its Go counterpart is
//     sandbox.BuildGate taking the runtime dir from GateArgs.Runtime and not from the environment.
// not ported: none.

const gbDirector = "zz-test-director"

// gbGate is GateBase: a project with a gate_cmd, a goal and an open PR whose head is a real commit on a local bare remote.
type gbGate struct {
	t       *testing.T
	e       *ft.Env
	head    string
	out     string
	wtRoot  string
	marker  string
	extra   map[string]string
	sandbox bool
}

func gbNewGate(t *testing.T) *gbGate {
	t.Helper()
	e := ft.New(t)
	e.NeedPython() // `factory gate run` and `factory project update` are not ported to Go yet
	e.SeedAgent(ft.AgentRow{Name: gbDirector, Role: "director"})
	d := e.RawDB()
	defer d.Close()
	for _, q := range []string{
		"INSERT INTO projects(id,name,auto_merge,created) VALUES('zz-test-proj','zz-test-proj',1,'t')",
		"UPDATE projects SET gh_repo='zz-test/repo' WHERE id='zz-test-proj'"} {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	e.SeedGoal("G-001", "zz-test-proj", "zz-test goal")
	e.SeedTask(ft.Task{})
	g := &gbGate{t: t, e: e}
	g.head = e.UseGitHead()
	// UseGitHead scripts `repo view` and `api .../commits/main`; the PR the stub answers `pr view` with (open, at that
	// head) is added to the same script, since Script replaces the rules of a tool.
	mainSHA := gbGit(t, e.RepoURL, "rev-parse", "refs/heads/main")
	pr := `{"number": 1, "state": "OPEN", "headRefOid": "` + g.head + `", "headRefName": "factory/t-0001", "baseRefName": "main", "mergeable": "MERGEABLE", "isDraft": false, "url": "` + ft.PRURL + `", "statusCheckRollup": []}` + "\n"
	e.Script("gh",
		ft.StubRule{Match: []string{"repo view"}, Out: `{"url": "` + e.RepoURL + `"}` + "\n"},
		ft.StubRule{Match: []string{"api", "/commits/main"}, Out: mainSHA + "\n"},
		ft.StubRule{Match: []string{"pr view"}, Out: pr})
	g.out = filepath.Join(e.Home, "out.txt")
	g.wtRoot = filepath.Join(e.UserHome, ".cache", "factory-gate")
	g.marker = filepath.Join(e.Home, "marker.txt")
	g.extra = map[string]string{"FACTORY_GATE_SLOT_WAIT_S": "0", "FACTORY_GATE_POLL_S": "0", "GATE_OUT": g.out,
		"GATE_OUT2": filepath.Join(e.Home, "out2.txt"), "GATE_MARKER": g.marker}
	return g
}

// sandboxed is SandboxBase.setUp: config gate_sandbox 1.
func (g *gbGate) sandboxed() *gbGate {
	g.t.Helper()
	g.cfg("gate_sandbox", "1")
	g.sandbox = true
	return g
}

func (g *gbGate) cfg(k, v string) {
	g.t.Helper()
	g.e.OKWith(ft.Opts{Agent: gbDirector}, "config", k, v)
}

func (g *gbGate) setGate(cmd string, opts ...string) {
	g.t.Helper()
	args := append([]string{"project", "update", "zz-test-proj", "--gate-cmd", cmd}, opts...)
	g.e.OKWith(ft.Opts{Agent: gbDirector}, args...)
}

func (g *gbGate) gate(args ...string) ft.Result {
	g.t.Helper()
	return g.e.RunWith(ft.Opts{Agent: gbDirector, Env: g.extra}, append([]string{"gate"}, args...)...)
}

func (g *gbGate) gateEnv(extra map[string]string, args ...string) ft.Result {
	g.t.Helper()
	env := map[string]string{}
	for k, v := range g.extra {
		env[k] = v
	}
	for k, v := range extra {
		env[k] = v
	}
	return g.e.RunWith(ft.Opts{Agent: gbDirector, Env: env}, append([]string{"gate"}, args...)...)
}

type gbRunRow struct {
	status, note, logPath, count string
	exit                         string
}

func (g *gbGate) row(id int) gbRunRow {
	g.t.Helper()
	rows := g.e.SQL("SELECT status, coalesce(note,''), coalesce(log_path,''), coalesce(test_count,'NULL'), coalesce(exit_code,'NULL') FROM gate_runs WHERE id=?", id)
	if len(rows) != 1 {
		g.t.Fatalf("gate_runs #%d: %d rows, want 1", id, len(rows))
	}
	r := rows[0]
	return gbRunRow{status: r[0], note: r[1], logPath: r[2], count: r[3], exit: r[4]}
}

func (g *gbGate) log(r gbRunRow) string { g.t.Helper(); return gbRead(g.t, r.logPath) }

func (g *gbGate) lastID() int { return g.e.Count("SELECT max(id) FROM gate_runs") }

func (g *gbGate) needUserns() {
	g.t.Helper()
	gbUserns(g.t)
}

func (g *gbGate) okRun(p ft.Result, want int) {
	g.t.Helper()
	if p.Exit != want {
		g.t.Fatalf("gate run exit = %d, want %d\nstdout: %s\nstderr: %s", p.Exit, want, p.Stdout, p.Stderr)
	}
}

// assertClean: no leftover worktree, neither on disk nor registered in the repo.
func (g *gbGate) assertClean() {
	g.t.Helper()
	if m, _ := filepath.Glob(filepath.Join(g.wtRoot, "*", "*")); len(m) != 0 {
		g.t.Errorf("leftover gate worktrees on disk: %v", m)
	}
	if n := len(strings.Split(gbGit(g.t, g.e.Home, "worktree", "list"), "\n")); n != 1 {
		g.t.Errorf("worktree list has %d entries, want 1", n)
	}
}

// fakeUnshare puts an unshare that always fails first on the CLI's PATH.
func (g *gbGate) fakeUnshare(body string) {
	g.t.Helper()
	gbMust(g.t, os.WriteFile(filepath.Join(g.e.StubBin, "unshare"), []byte(body), 0o755))
}

const gbUnshareDenied = "#!/bin/sh\necho 'unshare: unshare failed: Operation not permitted' >&2\nexit 1\n"

// ---- SandboxedRun ----------------------------------------------------------------------------------------------

func TestGateSandboxFakeCredentialsUnreadableAndRecordedAsSandboxed(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	cred := g.e.UserHome + "/.config/gh/hosts.yml"
	gbPut(t, cred, "FAKE-GH-TOKEN")
	g.setGate("cat " + cred)
	g.okRun(g.gate("run", "T-0001"), 1)
	r := g.row(1)
	if r.status != "fail" {
		t.Errorf("status = %q, want fail", r.status)
	}
	gbHas(t, "log", g.log(r), "No such file")
	gbHasNot(t, "log", g.log(r), "FAKE-GH-TOKEN")
	gbHas(t, "note", r.note, "sandboxed")
}

func TestGateSandboxPassingCommandPassesAndReportsInTheRwDirs(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	g.setGate(`git rev-parse HEAD > "$GATE_REPORT_DIR/head.txt"; echo hi > "$TMPDIR/t"; test "$HOME" != ""`)
	p := g.gate("run", "T-0001")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	r := g.row(1)
	if r.status != "pass" {
		t.Errorf("status = %q, want pass\n%s", r.status, g.log(r))
	}
	gbHas(t, "note", r.note, "sandboxed")
}

func TestGateSandboxNetworkFails(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	g.setGate("exec 3<>/dev/tcp/127.0.0.1/9") // python's create_connection(('127.0.0.1', 9), 2) in the original
	g.okRun(g.gate("run", "T-0001"), 1)
	if !regexp.MustCompile(`refused|unreachable|Errno`).MatchString(g.log(g.row(1))) {
		t.Errorf("log = %q, want refused|unreachable|Errno", g.log(g.row(1)))
	}
}

func TestGateSandboxWritesOutsideTheRwDirsFail(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	for _, where := range []string{"$FACTORY_MAIN/stray", "$(git rev-parse --git-common-dir)/hooks/stray", "$(git rev-parse --git-common-dir)/config"} {
		g.setGate("echo x > " + where)
		g.okRun(g.gate("run", "T-0001"), 1)
		gbHas(t, where+" log", gbRead(t, g.e.SQL("SELECT log_path FROM gate_runs ORDER BY id DESC LIMIT 1")[0][0]), "Read-only file system")
	}
	for _, p := range []string{g.e.Home + "/stray", g.e.UserHome + "/stray"} {
		if gbExists(p) {
			t.Errorf("%s exists: a write outside the rw dirs reached the host", p)
		}
	}
}

func TestGateSandboxGateTreeIsWritableButTheGitCommonDirIsNot(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	g.setGate(`echo x > "$PWD/inside" && mkdir -p "$PWD/newdir" && echo y > "$PWD/newdir/f" && test -f "$PWD/inside"`)
	p := g.gate("run", "T-0001")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s%s", p.Exit, p.Stdout, p.Stderr, g.log(g.row(1)))
	}
	if r := g.row(1); r.status != "pass" {
		t.Errorf("status = %q, want pass", r.status)
	}
}

func TestGateSandboxVarTmpWriteFailsToPersist(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	leak := "/var/tmp/zz-test-gate-sandbox-" + strconv.Itoa(os.Getpid())
	t.Cleanup(func() { os.Remove(leak) })
	g.setGate("echo x > " + leak + "; cat " + leak)
	p := g.gate("run", "T-0001")
	if p.Exit != 0 { // the write lands in the box's private tmpfs
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	if gbExists(leak) {
		t.Errorf("%s persisted on the host", leak)
	}
}

func TestGateSandboxCountCmdRunsInTheBoxToo(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	g.setGate("true", "--gate-count-cmd", "touch $FACTORY_MAIN/x 2>/dev/null || echo 7") // FACTORY_MAIN is read-only inside the box
	p := g.gate("run", "T-0001")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	r := g.row(1)
	if r.count != "7" {
		t.Errorf("test_count = %q, want 7", r.count)
	}
	gbHas(t, "log", g.log(r), "gate_count_cmd in")
}

func TestGateSandboxCountCmdCannotReadCredentials(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	cred := g.e.UserHome + "/secret.txt"
	gbPut(t, cred, "123")
	g.setGate("true", "--gate-count-cmd", "cat "+cred+" || echo 5")
	p := g.gate("run", "T-0001")
	if p.Exit != 0 {
		t.Fatalf("exit %d\n%s%s", p.Exit, p.Stdout, p.Stderr)
	}
	if r := g.row(1); r.count != "5" { // the file was hidden, so cat failed and the fallback printed 5
		t.Errorf("test_count = %q, want 5", r.count)
	}
}

// ---- FlagRollback ----------------------------------------------------------------------------------------------

// TestGateSandboxFlagRollbackOnThenRollbackThenOnAgain is T-0705 (GS3): gate_sandbox 1 -> 0 (the documented
// rollback) -> 1 on a real factory unittest module, all in the temp home. test_nsbox_allow.GoldenDefaultsTests is pure
// (no namespaces, network or $HOME writes) and runs in well under a second. The module is the Python one: the oracle
// stays until P7a, so this case needs python3 and reads the real tests dir (the merge gate's archive has it).
func TestGateSandboxFlagRollbackOnThenRollbackThenOnAgain(t *testing.T) {
	g := gbNewGate(t)
	g.needUserns()
	g.sandboxed()
	repo, err := ft.RepoRoot()
	gbMust(t, err)
	tests := filepath.Join(repo, "tests")
	if _, err := os.Stat(filepath.Join(tests, "test_nsbox_allow.py")); err != nil {
		t.Fatalf("read %s: %v", tests, err)
	}
	py, err := exec.LookPath("python3")
	gbMust(t, err)
	runGate := func() string {
		p := g.gate("run", "T-0001", "--no-cache") // a pass from the previous flag value must not be reused
		r := g.row(g.lastID())
		if p.Exit != 0 {
			t.Fatalf("exit %d\n%s%s%s", p.Exit, p.Stdout, p.Stderr, g.log(r))
		}
		if r.status != "pass" {
			t.Fatalf("status = %q, want pass", r.status)
		}
		gbHas(t, "log", g.log(r), "Ran 18 tests") // unittest really ran, not a vacuous exit 0
		return r.note
	}
	g.setGate("cd " + tests + " && " + py + " -B -m unittest test_nsbox_allow.GoldenDefaultsTests")
	gbHas(t, "note on", runGate(), "sandboxed")
	g.cfg("gate_sandbox", "0")
	gbHasNot(t, "note off", runGate(), "sandboxed")
	g.cfg("gate_sandbox", "1")
	gbHas(t, "note on again", runGate(), "sandboxed")
}

// ---- FailClosed ------------------------------------------------------------------------------------------------

func TestGateSandboxFailClosedUsernsUnavailableIsErrorAndTheCommandNeverRuns(t *testing.T) {
	g := gbNewGate(t)
	g.sandboxed()
	g.fakeUnshare(gbUnshareDenied)
	g.setGate(`touch "$GATE_MARKER"`)
	g.okRun(g.gate("run", "T-0001"), 5)
	r := g.row(1)
	if r.status != "error" {
		t.Errorf("status = %q, want error", r.status)
	}
	gbHas(t, "note", r.note, "not run unsandboxed")
	if gbExists(g.marker) {
		t.Error("the gate command ran without the sandbox")
	}
	g.assertClean()
}

func TestGateSandboxFailClosedCountCmdNeverRunsUnsandboxedEither(t *testing.T) {
	g := gbNewGate(t)
	g.sandboxed()
	g.fakeUnshare(gbUnshareDenied)
	g.setGate("true", "--gate-count-cmd", `touch "$GATE_MARKER"; echo 1`)
	g.okRun(g.gate("run", "T-0001"), 5)
	if gbExists(g.marker) {
		t.Error("the count command ran without the sandbox")
	}
}

func TestGateSandboxFailClosedBadConfigValueIsErrorAndTheCommandNeverRuns(t *testing.T) {
	g := gbNewGate(t)
	g.cfg("gate_sandbox", "yes")
	g.setGate(`touch "$GATE_MARKER"`)
	g.okRun(g.gate("run", "T-0001"), 5)
	gbHas(t, "note", g.row(1).note, "gate_sandbox")
	if gbExists(g.marker) {
		t.Error("the gate command ran with an invalid gate_sandbox value")
	}
}

// ---- FlagOff ---------------------------------------------------------------------------------------------------

func TestGateSandboxFlagOffDefaultIsOffAndRunsUnboxed(t *testing.T) {
	g := gbNewGate(t)
	g.setGate(`touch "$GATE_OUT"`)
	g.okRun(g.gate("run", "T-0001"), 0)
	if !gbExists(g.out) {
		t.Error("the gate command did not run")
	}
	gbHasNot(t, "note", g.row(1).note, "sandboxed")
}

func TestGateSandboxFlagOffIgnoresAMissingUserns(t *testing.T) {
	g := gbNewGate(t)
	g.fakeUnshare("#!/bin/sh\nexit 1\n")
	g.setGate("true")
	g.okRun(g.gate("run", "T-0001"), 0)
}

// ---- Wiring ----------------------------------------------------------------------------------------------------

// TestGateSandboxWiringRuntimeIsPassedExplicitly: the Python case checks that gate_argv hands gatebox.build the
// XDG_RUNTIME_DIR it read. In Go the runtime dir is a GateArgs field; the profile must hide that dir and must not
// pick up the process environment when the field is set.
func TestGateSandboxWiringRuntimeIsPassedExplicitly(t *testing.T) {
	f := gbNew(t, gbFixtureTmp())
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/from-env")
	a := f.args("true")
	a.Runtime = "/run/user/xyz"
	a.Probe = func(bool) (bool, string) { return true, "" }
	argv, _, err := sb.BuildGate(a)
	var ge *sb.GateboxError
	if errors.As(err, &ge) {
		t.Fatalf("BuildGate: %v", err)
	}
	gbMust(t, err)
	cfg := gbSetupCfg(t, argv)
	var hidden bool
	for _, d := range cfg.HideDirs {
		hidden = hidden || d == "/run/user/xyz"
		if d == "/run/user/from-env" {
			t.Errorf("hide_dirs = %v: the environment's runtime dir won over the explicit one", cfg.HideDirs)
		}
	}
	if !hidden {
		t.Errorf("hide_dirs = %v, want the explicit runtime dir /run/user/xyz", cfg.HideDirs)
	}
}
