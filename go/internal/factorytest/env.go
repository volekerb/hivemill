package factorytest

import (
	"bytes"
	"database/sql"
	_ "embed"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed testdata/init.sql
var initSQL string

// DefaultNow is the FACTORY_TEST_NOW every Env starts with (the CLI honours it off a live home only).
const DefaultNow = "2026-06-01T12:00:00+00:00"

// PRURL is the PR url the Python harness seeds (tests/harness.py PR_URL).
const PRURL = "https://github.com/zz-test/repo/pull/1"

// runTimeout ends a hung CLI run.
const runTimeout = 60 * time.Second

// Result is one finished CLI run.
type Result struct {
	Exit           int
	Stdout, Stderr string
}

// Opts tunes one run: the acting agent (FACTORY_AGENT; "" leaves it unset), extra environment (wins over the
// harness values; a "" value unsets the variable) and stdin.
type Opts struct {
	Agent string
	Env   map[string]string
	Stdin string
}

// Env is one throwaway factory home. Fields are paths a test may read or seed.
type Env struct {
	T          testing.TB
	Root       string // the temp dir holding everything below
	UserHome   string // HOME of the CLI
	StubBin    string // gh, herdr, hc
	Home       string // FACTORY_HOME
	DBPath     string
	ProcDir    string // FACTORY_PROC_DIR, a fake /proc
	CallsFile  string // calls.jsonl: one record per stub call
	ScriptFile string // stub-script.json, written by Script
	GoBin      string // the factory-go this Env runs
	Now        string // FACTORY_TEST_NOW
	Agent      string // default FACTORY_AGENT of Run and OK
}

// New builds a fresh Env. It builds factory-go and the stub on first use in this test binary.
func New(t testing.TB) *Env {
	t.Helper()
	tools, err := buildTools()
	if err != nil {
		t.Fatalf("factorytest: %v", err)
	}
	repo, err := RepoRoot()
	if err != nil {
		t.Fatalf("factorytest: %v", err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &Env{T: t, Root: root, Now: DefaultNow}
	e.UserHome = filepath.Join(root, "userhome")
	e.StubBin = filepath.Join(root, "stubbin")
	e.Home = filepath.Join(root, "factory")
	e.ProcDir = filepath.Join(root, "fakeproc-host")
	e.DBPath = filepath.Join(e.Home, "db", "factory.db")
	e.CallsFile = filepath.Join(root, "calls.jsonl")
	e.ScriptFile = filepath.Join(root, "stub-script.json")
	for _, d := range []string{e.UserHome, e.StubBin, e.Home, e.ProcDir, filepath.Join(e.Home, "run"), filepath.Join(root, "gobin")} {
		must(t, os.MkdirAll(d, 0o755))
	}
	must(t, os.WriteFile(e.CallsFile, nil, 0o644))
	for _, n := range []string{"gh", "herdr", "hc"} { // copies, like the Python harness: the worker gate exempts only a stub path
		must(t, linkOrCopy(tools.Stub, filepath.Join(e.StubBin, n)))
	}
	// factory-go next to a `factory` symlink to bin/factory: the Go CLI hands unported commands to <its dir>/factory
	e.GoBin = filepath.Join(root, "gobin", "factory-go")
	must(t, linkOrCopy(tools.Factory, e.GoBin))
	if py := filepath.Join(repo, "bin", "factory"); exists(py) {
		must(t, os.Symlink(py, filepath.Join(root, "gobin", "factory")))
	}
	must(t, os.MkdirAll(filepath.Join(e.UserHome, ".local", "bin"), 0o755))
	must(t, os.Symlink(filepath.Join(e.Home, "bin", "factory"), filepath.Join(e.UserHome, ".local", "bin", "factory")))
	if roles := filepath.Join(repo, "roles"); exists(roles) {
		must(t, os.Symlink(roles, filepath.Join(e.Home, "roles")))
	}
	e.git("init", "-q", "-b", "main")
	e.initDB()
	return e
}

func must(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("factorytest: %v", err)
	}
}

func (e *Env) git(args ...string) {
	e.T.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = e.Home
	cmd.Env = append(e.baseEnv(), "GIT_CONFIG_GLOBAL=/dev/null")
	if out, err := cmd.CombinedOutput(); err != nil {
		e.T.Fatalf("factorytest: git %v: %v\n%s", args, err, out)
	}
}

func (e *Env) initDB() {
	e.T.Helper()
	must(e.T, os.MkdirAll(filepath.Dir(e.DBPath), 0o700))
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil {
		e.T.Fatal(err)
	}
	if _, err := d.Exec(initSQL); err != nil {
		e.T.Fatalf("factorytest: testdata/init.sql: %v", err)
	}
}

// RawDB opens the home's database for seeding and assertions (one connection, 30 s busy timeout).
func (e *Env) RawDB() *sql.DB {
	e.T.Helper()
	dsn := (&url.URL{Scheme: "file", Path: e.DBPath, RawQuery: "_pragma=busy_timeout(30000)"}).String()
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		e.T.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	return d
}

// baseEnv is the part of the environment shared by git and the CLI: nothing is inherited except PATH, TZ and the
// harness identity, so the runner's own settings (agent identity, XDG, GIT_*) cannot leak in.
func (e *Env) baseEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	env := []string{
		"PATH=" + e.StubBin + string(os.PathListSeparator) + path,
		"HOME=" + e.UserHome,
		"XDG_STATE_HOME=" + filepath.Join(e.UserHome, ".local", "state"),
		"SHELL=/bin/sh",
		"GIT_AUTHOR_NAME=zz-test", "GIT_AUTHOR_EMAIL=zz-test@example.invalid",
		"GIT_COMMITTER_NAME=zz-test", "GIT_COMMITTER_EMAIL=zz-test@example.invalid",
		"GIT_CONFIG_NOSYSTEM=1",
		"LC_ALL=C.UTF-8",
	}
	if tz, ok := os.LookupEnv("TZ"); ok {
		env = append(env, "TZ="+tz)
	}
	return env
}

// Environ is the environment of a CLI run for agent (FACTORY_AGENT unset when "") with extra applied on top.
func (e *Env) Environ(agent string, extra map[string]string) []string {
	m := map[string]string{}
	for _, kv := range e.baseEnv() {
		k, v, _ := strings.Cut(kv, "=")
		m[k] = v
	}
	for k, v := range map[string]string{
		"FACTORY_HOME":            e.Home,
		"FAKE_HERDR_STATE":        filepath.Join(e.Root, "herdr.json"),
		"FAKE_GH_STATE":           filepath.Join(e.Root, "gh.json"),
		"FAKE_CALLS":              e.CallsFile,
		"FAKE_STUB_SCRIPT":        e.ScriptFile,
		"FACTORY_PROC_DIR":        e.ProcDir,
		"FACTORY_TMP_STATVFS":     "1000000000,500000000",
		"FACTORY_TMP_DIR":         filepath.Join(e.Root, "faketmp"),
		"FACTORY_LOADAVG":         "0",
		"FACTORY_KICKOFF_DELAY_S": "0",
		"FACTORY_SHELL_SETTLE_S":  "0.01",
		"FACTORY_RELAUNCH_POLL_S": "0.02",
		"FACTORY_STOP_CONFIRM_S":  "0.2",
		"FACTORY_TEST_NOW":        e.Now,
		"PYTHONDONTWRITEBYTECODE": "1",
	} {
		m[k] = v
	}
	if agent != "" {
		m["FACTORY_AGENT"] = agent
	}
	for k, v := range extra {
		m[k] = v
	}
	var out []string
	for k, v := range m {
		if ev, set := extra[k]; set && ev == "" {
			continue // an empty extra value unsets the variable
		}
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}

// Run runs the CLI with stdin as the default agent; see RunWith.
func (e *Env) Run(argv []string, stdin string) Result {
	e.T.Helper()
	return e.RunWith(Opts{Agent: e.Agent, Stdin: stdin}, argv...)
}

// RunWith runs factory-go in the home. After 60 s the process is killed and the result has exit -1.
func (e *Env) RunWith(o Opts, args ...string) Result {
	e.T.Helper()
	cmd := exec.Command(e.GoBin, args...)
	cmd.Dir = e.Root
	cmd.Env = e.Environ(o.Agent, o.Env)
	cmd.Stdin = strings.NewReader(o.Stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Start(); err != nil {
		e.T.Fatalf("factorytest: start %s: %v", e.GoBin, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(runTimeout):
		_ = cmd.Process.Kill()
		<-done
		return Result{Exit: -1, Stdout: so.String(), Stderr: se.String() + "factorytest: timed out after 60s\n"}
	}
	return Result{Exit: exitCode(e.T, args, err), Stdout: so.String(), Stderr: se.String()}
}

func exitCode(t testing.TB, args []string, err error) int {
	t.Helper()
	if err == nil {
		return 0
	}
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	t.Fatalf("factorytest: run %v: %v", args, err)
	return -1
}

// OK runs args as the default agent and fails the test unless the exit code is 0.
func (e *Env) OK(args ...string) Result { e.T.Helper(); return e.OKWith(Opts{Agent: e.Agent}, args...) }

// OKWith is OK with options.
func (e *Env) OKWith(o Opts, args ...string) Result {
	e.T.Helper()
	r := e.RunWith(o, args...)
	if r.Exit != 0 {
		e.T.Fatalf("%v: exit %d, want 0\nstdout: %s\nstderr: %s", args, r.Exit, r.Stdout, r.Stderr)
	}
	return r
}

// Fail runs args as the default agent and fails the test unless the exit code is code.
func (e *Env) Fail(code int, args ...string) Result {
	e.T.Helper()
	return e.FailWith(Opts{Agent: e.Agent}, code, args...)
}

// FailWith is Fail with options.
func (e *Env) FailWith(o Opts, code int, args ...string) Result {
	e.T.Helper()
	r := e.RunWith(o, args...)
	if r.Exit != code {
		e.T.Fatalf("%v: exit %d, want %d\nstdout: %s\nstderr: %s", args, r.Exit, code, r.Stdout, r.Stderr)
	}
	return r
}

// NeedPython skips the test when python3 is not on PATH: the command under test is not ported to Go yet, so the Go
// CLI hands it to bin/factory.
func (e *Env) NeedPython() {
	e.T.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		e.T.Skip("python3 is not on PATH: this test drives a command the Go CLI still hands to Python; it runs where python3 exists")
	}
}

// ---- state ----------------------------------------------------------------------------------------------------

// SQL runs a query and returns every row as strings (NULL is "NULL"); a failure ends the test.
func (e *Env) SQL(query string, args ...any) [][]string {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	rows, err := d.Query(query, args...)
	if err != nil {
		e.T.Fatalf("factorytest: %s: %v", query, err)
	}
	defer rows.Close()
	out, err := scanAll(rows)
	if err != nil {
		e.T.Fatalf("factorytest: %s: %v", query, err)
	}
	return out
}

// Count runs a query whose first column of the first row is a number, such as
// "SELECT count(*) FROM events WHERE action=?", and returns it.
func (e *Env) Count(query string, args ...any) int {
	e.T.Helper()
	rows := e.SQL(query, args...)
	if len(rows) == 0 {
		e.T.Fatalf("factorytest: %s returned no row", query)
	}
	n, err := strconv.Atoi(rows[0][0])
	if err != nil {
		e.T.Fatalf("factorytest: %s did not return a number: %v", query, rows)
	}
	return n
}

func scanAll(rows *sql.Rows) ([][]string, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out [][]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		row := make([]string, len(cols))
		for i, v := range vals {
			row[i] = fmtVal(v)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func fmtVal(v any) string {
	switch x := v.(type) {
	case nil:
		return "NULL"
	case []byte:
		return string(x)
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'g', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}

func (e *Env) exec(query string, args ...any) {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(query, args...); err != nil {
		e.T.Fatalf("factorytest: %s: %v", query, err)
	}
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// SeedGoal inserts an active goal (harness seed_goal); project "" is NULL.
func (e *Env) SeedGoal(id, project, title string) {
	e.T.Helper()
	e.exec("INSERT INTO goals(id,project_id,title,status,created,updated) VALUES(?,?,?,'active','t','t')", id, nullable(project), title)
}

// SeedTeam inserts a team (harness seed_team); lead "" is NULL.
func (e *Env) SeedTeam(id, goal, lead string) {
	e.T.Helper()
	e.exec("INSERT INTO teams(id,goal_id,lead_agent,created) VALUES(?,?,?,'t')", id, goal, nullable(lead))
}

// Task is the seed_task row; zero fields take the harness defaults (T-0001, G-001, coder, in_review, PRURL).
type Task struct{ ID, Goal, Role, Status, PRURL, Team, Spec, Acceptance string }

// SeedTask inserts a task (harness seed_task).
func (e *Env) SeedTask(t Task) {
	e.T.Helper()
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&t.ID, "T-0001")
	def(&t.Goal, "G-001")
	def(&t.Role, "coder")
	def(&t.Status, "in_review")
	def(&t.PRURL, PRURL)
	def(&t.Spec, "zz-test spec")
	def(&t.Acceptance, "1. zz-test")
	e.exec("INSERT INTO tasks(id,goal_id,team_id,title,role,status,pr_url,spec,acceptance,created,updated) "+
		"VALUES(?,?,?,?,?,?,?,?,?,'t','t')", t.ID, t.Goal, nullable(t.Team), "zz-test task", t.Role, t.Status, t.PRURL, t.Spec, t.Acceptance)
}

// AgentRow is the seed_agent row; Status defaults to live and Started to 2026-01-01T00:00:00+00:00.
type AgentRow struct{ Name, Role, Goal, Team, Task, Status, Started, PaneID string }

// SeedAgent inserts an agents row (harness seed_agent).
func (e *Env) SeedAgent(a AgentRow) {
	e.T.Helper()
	if a.Status == "" {
		a.Status = "live"
	}
	if a.Started == "" {
		a.Started = "2026-01-01T00:00:00+00:00"
	}
	e.exec("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,started,pane_id) VALUES(?,?,?,?,?,?,?,?)",
		a.Name, a.Role, nullable(a.Goal), nullable(a.Team), nullable(a.Task), a.Status, a.Started, nullable(a.PaneID))
}

// FakeClaude writes a fake /proc/<pid> for a claude process whose parent is ppid (harness fake_claude); argv
// defaults to just "claude".
func (e *Env) FakeClaude(pid, ppid int, argv ...string) {
	e.T.Helper()
	if len(argv) == 0 {
		argv = []string{"claude"}
	}
	e.FakeProc(pid, "claude", ppid, argv...)
}

// FakeProc writes a fake /proc entry: comm and parent pid in stat, argv in cmdline. An existing entry is replaced.
func (e *Env) FakeProc(pid int, comm string, ppid int, argv ...string) {
	e.T.Helper()
	d := filepath.Join(e.ProcDir, strconv.Itoa(pid))
	must(e.T, os.MkdirAll(d, 0o755))
	must(e.T, os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(argv, "\x00")+"\x00"), 0o644))
	stat := fmt.Sprintf("%d (%s) S %d %d %d 0 -1 0\n", pid, comm, ppid, pid, pid)
	must(e.T, os.WriteFile(filepath.Join(d, "stat"), []byte(stat), 0o644))
}

// ---- stub calls and scripts -------------------------------------------------------------------------------------

// Call is one logged stub call. Env and Stdin are set for gh, Sock for herdr and hc; nil pointers are JSON null.
type Call struct {
	Tool  string             `json:"tool"`
	Argv  []string           `json:"argv"`
	Env   map[string]*string `json:"env"`
	Stdin *string            `json:"stdin"`
	Sock  *string            `json:"sock"`
}

// Calls returns the logged stub calls, only those of tool when it is not "" (harness calls()).
func (e *Env) Calls(tool string) []Call {
	e.T.Helper()
	var out []Call
	for _, line := range e.CallLines() {
		var c Call
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			e.T.Fatalf("factorytest: call log line %q is not JSON: %v", line, err)
		}
		if tool == "" || c.Tool == tool {
			out = append(out, c)
		}
	}
	return out
}

// CallLines returns the raw call-log lines, byte for byte as the stubs wrote them.
func (e *Env) CallLines() []string {
	e.T.Helper()
	b, err := os.ReadFile(e.CallsFile)
	if err != nil {
		e.T.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// StubRule scripts one stub answer: the first rule of a tool whose Match substrings all occur in the space-joined
// argv wins (a substring ending in $ must end it).
type StubRule struct {
	Match []string `json:"match"`
	Out   string   `json:"out"`
	Err   string   `json:"err"`
	Exit  int      `json:"exit"`
}

// Script replaces the answers of tool ("gh", "herdr" or "hc"); other tools keep theirs.
func (e *Env) Script(tool string, rules ...StubRule) {
	e.T.Helper()
	all := map[string][]StubRule{}
	if b, err := os.ReadFile(e.ScriptFile); err == nil {
		must(e.T, json.Unmarshal(b, &all))
	}
	all[tool] = rules
	b, err := json.Marshal(all)
	must(e.T, err)
	must(e.T, os.WriteFile(e.ScriptFile, b, 0o644))
}

// RunStub runs the stub as tool ("gh", "herdr", "hc") the way the CLI would, for tests of the harness itself.
func (e *Env) RunStub(tool, stdin string, extraEnv map[string]string, args ...string) Result {
	e.T.Helper()
	cmd := exec.Command(filepath.Join(e.StubBin, tool), args...)
	cmd.Dir = e.Root
	cmd.Env = e.Environ("", extraEnv)
	cmd.Stdin = strings.NewReader(stdin)
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	return Result{Exit: exitCode(e.T, args, err), Stdout: so.String(), Stderr: se.String()}
}

// ---- DB dump -----------------------------------------------------------------------------------------------------

var (
	reTS    = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)`)
	reEpoch = regexp.MustCompile(`\b1[5-9]\d{8}(?:\.\d+)?\b`)
	reUUID  = regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)
	reHost  = regexp.MustCompile(`(?m)^ +\d+ MB .*$|(MemAvailable |mem available |after )\d+|\d+(\.\d+)?G free|\d+ MB`)
)

// Normalize masks what two runs of the same command never share: ISO timestamps, epoch seconds, uuids and host
// readings (the masks tests/harness.py applies under FACTORY_IMPL=diff).
func Normalize(s string) string {
	s = reTS.ReplaceAllString(s, "<ts>")
	s = reEpoch.ReplaceAllString(s, "<ts>")
	s = reUUID.ReplaceAllString(s, "<uuid>")
	return reHost.ReplaceAllString(s, "<host>")
}

// DumpDB returns every table of the home's database as rows ordered by primary key (rowid when there is none),
// each row "v1|v2|...", normalised with Normalize: the Go form of harness dump_db.
func (e *Env) DumpDB() map[string][]string {
	e.T.Helper()
	d := e.RawDB()
	defer d.Close()
	names, err := d.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	must(e.T, err)
	var tables []string
	for names.Next() {
		var n string
		must(e.T, names.Scan(&n))
		tables = append(tables, n)
	}
	must(e.T, names.Err())
	names.Close()
	out := map[string][]string{}
	for _, tb := range tables {
		order := e.orderBy(d, tb)
		rows, err := d.Query(fmt.Sprintf(`SELECT * FROM "%s" ORDER BY %s`, tb, order))
		if err != nil {
			e.T.Fatalf("factorytest: dump %s: %v", tb, err)
		}
		all, err := scanAll(rows)
		rows.Close()
		must(e.T, err)
		lines := []string{}
		for _, r := range all {
			lines = append(lines, Normalize(strings.Join(r, "|")))
		}
		out[tb] = lines
	}
	return out
}

// orderBy is the ORDER BY list of table tb: its primary key columns in key order, else rowid.
func (e *Env) orderBy(d *sql.DB, tb string) string {
	info, err := d.Query(fmt.Sprintf(`PRAGMA table_info("%s")`, tb))
	must(e.T, err)
	defer info.Close()
	type pk struct {
		pos  int
		name string
	}
	var pks []pk
	for info.Next() {
		var cid, notnull, p int
		var name, typ string
		var dflt sql.NullString
		must(e.T, info.Scan(&cid, &name, &typ, &notnull, &dflt, &p))
		if p > 0 {
			pks = append(pks, pk{p, name})
		}
	}
	must(e.T, info.Err())
	sort.Slice(pks, func(i, j int) bool { return pks[i].pos < pks[j].pos })
	var cols []string
	for _, p := range pks {
		cols = append(cols, `"`+p.name+`"`)
	}
	if len(cols) == 0 {
		return "rowid"
	}
	return strings.Join(cols, ", ")
}
