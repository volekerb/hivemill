package factorytest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStubLogFormat: the gh, herdr and hc stubs write call-log lines byte-equal to the Python stubs. testdata/
// calls_python.jsonl holds the gh lines captured from a run of tests/stubs/gh and the herdr and hc lines produced by
// the json.dumps expression those stubs use, for the same calls (non-ASCII argv, a quote, a backslash, an astral
// character, a set and an unset env variable, stdin on gh pr create).
func TestStubLogFormat(t *testing.T) {
	e := New(t)
	title := "caf é ✓ 😀"
	e.RunStub("gh", "body \"q\" \\ é\n", map[string]string{"GH_REPO": "o/r"}, "pr", "create", "--title", title, "--body", "-")
	e.RunStub("gh", "", nil, "pr", "ready", "7")
	for _, tool := range []string{"herdr", "hc"} {
		e.RunStub(tool, "", map[string]string{"HERDR_SOCKET_PATH": "/tmp/s.sock"}, "pane", "list", "--json", title)
		e.RunStub(tool, "", nil, "tab", "create")
	}
	e.RunStub("gh", "", nil, "api", "a\x7fb") // DEL is escaped by ensure_ascii
	want, err := os.ReadFile(filepath.Join("testdata", "calls_python.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	// the fixture lists gh, herdr, hc; the Go run logged gh, herdr, herdr, hc, hc in call order
	got := e.CallLines()
	wantLines := strings.Split(strings.TrimRight(string(want), "\n"), "\n")
	if len(got) != len(wantLines) {
		t.Fatalf("logged %d lines, fixture has %d:\n%s", len(got), len(wantLines), strings.Join(got, "\n"))
	}
	for i := range got {
		if got[i] != wantLines[i] {
			t.Errorf("line %d differs from the Python log\n go: %s\n py: %s", i+1, got[i], wantLines[i])
		}
	}
	calls := e.Calls("gh")
	if len(calls) != 3 || calls[0].Stdin == nil || *calls[0].Stdin != "body \"q\" \\ é\n" || calls[1].Stdin != nil {
		t.Fatalf("parsed gh calls = %+v", calls)
	}
	if h := e.Calls("herdr"); len(h) != 2 || h[0].Sock == nil || *h[0].Sock != "/tmp/s.sock" || h[1].Sock != nil {
		t.Fatalf("parsed herdr calls = %+v", h)
	}
}

// TestStubScript: a scripted answer wins, the default gh pr create answer is the fixture URL, and an unscripted gh
// call fails with a message that says what to do.
func TestStubScript(t *testing.T) {
	e := New(t)
	if r := e.RunStub("gh", "", nil, "pr", "create"); r.Exit != 0 || strings.TrimSpace(r.Stdout) != "https://github.com/zz-test/repo/pull/7" {
		t.Fatalf("default pr create = %+v", r)
	}
	if r := e.RunStub("gh", "", nil, "pr", "view", "x"); r.Exit != 1 || !strings.Contains(r.Stderr, "Env.Script") {
		t.Fatalf("unscripted pr view = %+v", r)
	}
	e.Script("gh", StubRule{Match: []string{"pr", "view", "x$"}, Out: "{\"state\": \"OPEN\"}\n"}, StubRule{Match: []string{"pr merge"}, Err: "boom\n", Exit: 4})
	if r := e.RunStub("gh", "", nil, "pr", "view", "x"); r.Exit != 0 || r.Stdout != "{\"state\": \"OPEN\"}\n" {
		t.Fatalf("scripted pr view = %+v", r)
	}
	if r := e.RunStub("gh", "", nil, "pr", "view", "xy"); r.Exit != 1 {
		t.Fatalf("$ anchor ignored: %+v", r)
	}
	if r := e.RunStub("gh", "", nil, "pr", "merge", "1"); r.Exit != 4 || r.Stderr != "boom\n" {
		t.Fatalf("scripted merge = %+v", r)
	}
	if r := e.RunStub("herdr", "", nil, "workspace", "list"); r.Exit != 0 || r.Stdout != "{\"id\": \"fake\", \"result\": {}}\n" {
		t.Fatalf("default herdr answer = %+v", r)
	}
	if n := len(e.Calls("")); n != 6 {
		t.Fatalf("logged %d calls, want 6", n)
	}
}

// TestEnvLayout: the home has the layout FactoryEnv builds, the CLI runs hermetically inside it and the stubs come first on PATH.
func TestEnvLayout(t *testing.T) {
	e := New(t)
	for _, p := range []string{e.DBPath, filepath.Join(e.Home, ".git"), filepath.Join(e.Home, "run"), e.StubBin, e.UserHome, e.ProcDir, e.CallsFile} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("missing %s: %v", p, err)
		}
	}
	if !strings.HasPrefix(e.Home, e.Root) || strings.HasPrefix(e.Home, os.Getenv("HOME")+"/factory") {
		t.Errorf("home %s is not inside the temp root %s", e.Home, e.Root)
	}
	if n := e.Count("SELECT count(*) FROM config WHERE key='git_push' AND value='0'"); n != 1 {
		t.Errorf("config git_push seed missing")
	}
	for _, kv := range [][2]string{{"scout_enabled", "0"}, {"retro_enabled", "0"}, {"digest_hour", "24"}, {"min_mem_available_mb", "1"}} {
		if v := e.SQL("SELECT value FROM config WHERE key=?", kv[0]); len(v) != 1 || v[0][0] != kv[1] {
			t.Errorf("config %s = %v, want %s", kv[0], v, kv[1])
		}
	}
	// the runner's own agent identity must not leak into the CLI
	t.Setenv("FACTORY_AGENT", "zz-leak")
	t.Setenv("HERDR_PANE_ID", "w0:p0")
	r := e.OK("whoami")
	if !strings.Contains(r.Stdout, "owner") {
		t.Errorf("whoami = %q, want owner (identity leaked from the runner?)", r.Stdout)
	}
	env := strings.Join(e.Environ("zz-test-coder", map[string]string{"FOO": "1", "CLAUDECODE": ""}), "\n")
	for _, want := range []string{"FACTORY_AGENT=zz-test-coder", "FACTORY_TEST_NOW=" + DefaultNow, "FACTORY_HOME=" + e.Home, "FOO=1", "PATH=" + e.StubBin + ":"} {
		if !strings.Contains(env, want) {
			t.Errorf("environment lacks %s", want)
		}
	}
	if strings.Contains(env, "CLAUDECODE") || strings.Contains(env, "zz-leak") {
		t.Errorf("environment kept a variable it must drop:\n%s", env)
	}
}

// TestDumpDB: every table, rows in primary-key order, timestamps normalised; two homes with the same seeds dump equal.
func TestDumpDB(t *testing.T) {
	a, b := New(t), New(t)
	for _, e := range []*Env{a, b} {
		e.SeedGoal("G-002", "", "second")
		e.SeedGoal("G-001", "", "first")
		e.SeedAgent(AgentRow{Name: "zz-x", Role: "coder"})
		e.OKWith(Opts{Agent: "owner"}, "msg", "zz-x", "subject", "body")
	}
	da, db := a.DumpDB(), b.DumpDB()
	if len(da["goals"]) != 2 || !strings.HasPrefix(da["goals"][0], "G-001|") {
		t.Fatalf("goals not in primary-key order: %v", da["goals"])
	}
	if len(da["messages"]) != 1 || strings.Contains(da["messages"][0], "2026-06-01") {
		t.Fatalf("messages row not normalised: %v", da["messages"])
	}
	if fmt.Sprint(da) != fmt.Sprint(db) {
		t.Fatalf("same commands, different dumps:\n%v\n%v", da, db)
	}
}

// TestBuildOnce: factory-go is built at most once per test binary however many Envs are made, and the build log of
// this source hash has one line per binary built, never one per Env.
func TestBuildOnce(t *testing.T) {
	before := buildCount
	tools, err := buildTools()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Stat(tools.Factory)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		e := New(t)
		got, err := os.Stat(e.GoBin) // every Env runs the one cached binary (hard link), not a rebuild
		if err != nil || !os.SameFile(src, got) {
			t.Fatalf("Env %d runs a different factory-go than the cached build (%v)", i, err)
		}
	}
	if buildCount != before {
		t.Fatalf("Env construction built binaries again (%d -> %d)", before, buildCount)
	}
	if buildCount > 2 { // factory-go and the stub
		t.Fatalf("this test binary ran %d builds, want at most 2", buildCount)
	}
	log, err := BuildLog()
	if err != nil {
		t.Fatal(err)
	}
	mine := 0
	for _, l := range log {
		if strings.Contains(l, "./cmd/factory") && strings.HasSuffix(l, fmt.Sprintf("by pid %d", os.Getpid())) {
			mine++
		}
	}
	if mine > 1 {
		t.Fatalf("build log has %d factory-go builds by this process: %v", mine, log)
	}
}

// TestPortedCounts: every line of ported*.txt names a Go port with at least as many tests as the Python module has cases.
func TestPortedCounts(t *testing.T) {
	entries, problems, err := ReadPorted(".")
	if err != nil {
		t.Fatal(err)
	}
	problems = append(problems, CheckPorted(entries, GoTestList)...)
	for _, p := range problems {
		t.Error(p)
	}
}

// TestPortedCountsBites (C4): the check fails when a line claims more Python cases than the Go port has, when the
// prefix matches nothing, when the line is malformed and when there are no lines at all.
func TestPortedCountsBites(t *testing.T) {
	entries, problems := ParsePorted("scratch.txt", "tests/test_identity.py 23 -> internal/factorytest:TestIdentity 23\n")
	if len(problems) != 0 || len(entries) != 1 {
		t.Fatalf("parse: %v %v", entries, problems)
	}
	if got := CheckPorted(entries, GoTestList); len(got) != 1 || !strings.Contains(got[0], "has 23 Python cases") {
		t.Fatalf("overstated Python count not caught: %v", got)
	}
	entries, _ = ParsePorted("scratch.txt", "tests/test_x.py 3 -> internal/factorytest:TestNoSuchPrefixAnywhere 3\n")
	if got := CheckPorted(entries, GoTestList); len(got) != 1 || !strings.Contains(got[0], "no test named") {
		t.Fatalf("missing port not caught: %v", got)
	}
	entries, _ = ParsePorted("scratch.txt", "tests/test_identity.py 22 -> internal/factorytest:TestIdentity 99\n")
	if got := CheckPorted(entries, GoTestList); len(got) != 1 || !strings.Contains(got[0], "claims 99") {
		t.Fatalf("overstated Go claim not caught: %v", got)
	}
	if _, p := ParsePorted("scratch.txt", "tests/test_x.py many -> pkg:TestX 1\ntests/test_y.py 0 -> pkg:TestY 1\n# fine\n"); len(p) != 2 {
		t.Fatalf("malformed lines not reported: %v", p)
	}
	if got := CheckPorted(nil, GoTestList); len(got) != 1 || !strings.Contains(got[0], "vacuously") {
		t.Fatalf("empty list not caught: %v", got)
	}
	// and the accepted shape passes
	entries, _ = ParsePorted("scratch.txt", "tests/test_identity.py 22 -> internal/factorytest:TestIdentity 22\n")
	if got := CheckPorted(entries, GoTestList); len(got) != 0 {
		t.Fatalf("the real line is refused: %v", got)
	}
}

// TestCacheDirChecks: a cache directory that is not a private directory of this user is refused.
func TestCacheDirChecks(t *testing.T) {
	d := t.TempDir()
	if err := os.Chmod(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := checkOwned(d); err != nil {
		t.Fatalf("a private temp dir is refused: %v", err)
	}
	if err := os.Chmod(d, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := checkOwned(d); err == nil {
		t.Fatal("a world-writable dir is accepted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(d, link); err != nil {
		t.Fatal(err)
	}
	if err := checkOwned(link); err == nil {
		t.Fatal("a symlink is accepted")
	}
}
