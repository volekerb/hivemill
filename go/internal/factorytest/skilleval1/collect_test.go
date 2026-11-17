package skilleval1

import (
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/skilleval"
)

// Port of tests/test_skilleval_collect.py (32 cases): collecting a skill trial and `factory skilleval collect|list|results`.
//
// The Python unit tests mock nsbox.run_contained. Go has no such seam (skilleval.RunChecker is the only place check.py
// runs, through sandbox.RunContained), so the cases that reach the checker run the real contained checker with a
// shell script as check.py and a fake `python3` first on PATH that execs sh: no Python is needed and every
// asserted value (exit code, output, timeout) is produced by the script. They skip with the reason where user
// namespaces are unavailable. The CLI cases drive `factory skilleval ...`, which the Go CLI still hands to
// bin/factory (Env.NeedPython). The Go test of collect never uses a real claude: transcripts are fixtures, the
// evaluator "finished" in a seeded temp DB.
//
// test_skilleval_has_no_direct_exec_of_check_py greps the Python source for subprocess/os.system; its Go
// counterpart (TestSkillevalCollect_NoDirectExecOfCheckPy) scans the Go sources for the same property.

const (
	collectSHAChar  = "c"
	collectNow      = "2026-10-25T10:10:00+00:00"
	collectStart    = "2026-10-25T10:00:00+00:00"
	collectDone     = "2026-10-25T10:05:00+00:00"
	collectPassOut  = "PASS: all tests pass\n"
	collectPyShim   = "#!/bin/sh\nexec /bin/sh \"$@\"\n"
	collectChkPass  = "echo 'PASS: all tests pass'\n"
	collectChkFail  = "echo 'FAIL: tests failed'\nexit 1\n"
	collectChkBig   = "head -c 5000 /dev/zero | tr '\\0' A\nprintf TAIL\n"
	collectChkHang  = "echo partial\nsleep 300\n"
	collectChkLs    = "ls\n"
	collectProxyPid = "proxy.pid"
)

var collectSHA = strings.Repeat(collectSHAChar, 40)

// collectAPI is the Python FakeApi / ClaimingApi: a fixed clock and a force_stop whose result the test chooses.
type collectAPI struct {
	home, unshare string
	result        string // force_stop result; "stopped" also marks the agent stopped
	claim         bool   // ClaimingApi: a compare-and-set on stop_attempt_at, a stale attempt_seen is a skip
	failFirst     int
	calls         int
	stopped       []collectStop
	events        []string
	onStop        func(db *sql.DB, name string)
	cfg           map[string]string // Cfg answers
	at            time.Time         // the tick clock; zero means the fixed collectNow
	launched      []int64
}

type collectStop struct{ name, how, flag string }

func collectNewAPI(t testing.TB) *collectAPI {
	return &collectAPI{home: t.TempDir(), unshare: "unshare", result: "stopped"}
}

func (a *collectAPI) Now() string {
	if !a.at.IsZero() {
		return scout.Stamp(a.at)
	}
	return collectNow
}
func (a *collectAPI) Clock() time.Time {
	if !a.at.IsZero() {
		return a.at
	}
	ts, err := time.Parse(time.RFC3339, collectNow)
	if err != nil {
		panic(err) // a constant of this file
	}
	return ts
}
func (a *collectAPI) Cfg(_ *sql.DB, key string) (string, error) { return a.cfg[key], nil }
func (a *collectAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	a.events = append(a.events, action+":"+id+":"+detail)
	_, err := db.Exec("INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)", collectNow, entity, id, action, detail)
	if err != nil {
		return fmt.Errorf("insert event: %w", err)
	}
	return nil
}
func (a *collectAPI) ForceStop(db *sql.DB, name, how string, seen sql.NullString) (string, string, error) {
	if a.onStop != nil {
		a.onStop(db, name)
	}
	if a.claim {
		res, err := db.Exec("UPDATE agents SET stop_attempt_at=? WHERE name=? AND status='live' AND stop_requested_at IS NOT NULL "+
			"AND stop_attempt_at IS ?", collectNow, name, seen)
		if err != nil {
			return "", "", fmt.Errorf("claim stop: %w", err)
		}
		if n, _ := res.RowsAffected(); n != 1 { // a count error reads as "not claimed", the skip path
			return "skip", "", nil
		}
		a.calls++
		if a.calls <= a.failFirst {
			return "failed:pane-open", "pane still open", nil
		}
		if _, err := db.Exec("UPDATE agents SET status='stopped' WHERE name=?", name); err != nil {
			return "", "", fmt.Errorf("mark stopped: %w", err)
		}
		return "stopped", "", nil
	}
	var flag sql.NullString
	if err := db.QueryRow("SELECT stop_requested_at FROM agents WHERE name=?", name).Scan(&flag); err != nil {
		return "", "", fmt.Errorf("read stop flag: %w", err)
	}
	a.stopped = append(a.stopped, collectStop{name, how, flag.String})
	if a.result == "stopped" {
		if _, err := db.Exec("UPDATE agents SET status='stopped' WHERE name=?", name); err != nil {
			return "", "", fmt.Errorf("mark stopped: %w", err)
		}
		return "stopped", "", nil
	}
	return a.result, "pane still open", nil
}
func (a *collectAPI) Launch(db *sql.DB, t skilleval.Trial, root, home, unshare string) error {
	a.launched = append(a.launched, t.ID)
	name := sandbox.TrialName(int(t.ID))
	if _, err := db.Exec("UPDATE skill_trials SET status='running', agent=? WHERE id=? AND status='starting'", "eval-"+name, t.ID); err != nil {
		return fmt.Errorf("mark running: %w", err)
	}
	if _, err := db.Exec("INSERT INTO agents(name, role, status, started) VALUES(?,?,'live',?)", "eval-"+name, "evaluator", collectNow); err != nil {
		return fmt.Errorf("insert agent: %w", err)
	}
	return nil
}
func (a *collectAPI) Home() string    { return a.home }
func (a *collectAPI) HomeDir() string { return "/nonexistent-factory-home" }
func (a *collectAPI) Unshare() string { return a.unshare }

func collectOpenDB(t testing.TB) *sql.DB {
	t.Helper()
	path := dbtest.New(t, t.TempDir(), factorydb.SchemaVersion)
	d, err := factorydb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ddl, err := os.ReadFile("testdata/collect/scout_ddl.sql") // a copy of scout.db.SCOUT_DDL
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

func collectExec(t testing.TB, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func collectWrite(t testing.TB, p, body string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
}

// collectCopyTree copies the regular files and directories of src into dst.
func collectCopyTree(t testing.TB, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, p)
		if rerr != nil {
			return rerr
		}
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// collectEnv is CollectBase: a DB with candidate C-0001 and its vendored plugin, an eval root and a tasks dir.
type collectEnv struct {
	t     testing.TB
	db    *sql.DB
	root  string
	tasks string
	api   *collectAPI
}

// collectSeedCandidateAndVendor inserts C-0001 and writes its vendored plugin copy under root.
func collectSeedCandidateAndVendor(t testing.TB, db *sql.DB, root string) {
	t.Helper()
	collectExec(t, db, "INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", collectSHA)
	plugin := filepath.Join(root, "vendor", "C-0001@"+collectSHA)
	collectWrite(t, filepath.Join(plugin, "plugin", "skills", "demo-skill", "SKILL.md"), "---\nname: demo-skill\n---\nProse only.\n", 0o644)
	collectWrite(t, filepath.Join(plugin, "VENDOR.json"), `{"candidate": "C-0001", "sha": "`+collectSHA+`", "plugin": "plugin", `+
		`"copied": ["skills/demo-skill/SKILL.md"], "skipped": [], "prescreen_hits": []}`, 0o644)
}

func collectSetup(t testing.TB) *collectEnv {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	e := &collectEnv{t: t, db: collectOpenDB(t), root: filepath.Join(base, "evalroot"), tasks: filepath.Join(base, "tasks"), api: collectNewAPI(t)}
	collectSeedCandidateAndVendor(t, e.db, e.root)
	collectCopyTree(t, "testdata/collect/tasks", e.tasks)
	e.check(collectChkPass)
	return e
}

// check sets the script the contained checker runs as check.py of the fix-bug task.
func (e *collectEnv) check(script string) {
	e.t.Helper()
	collectWrite(e.t, filepath.Join(e.tasks, "fix-bug", "check.py"), script, 0o644)
}

// needChecker skips unless the real contained checker can run, and puts a python3 shim (exec sh) first on PATH.
func (e *collectEnv) needChecker() { collectNeedChecker(e.t) }

func collectNeedChecker(t testing.TB) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable: %s", why)
	}
	dir := t.TempDir()
	collectWrite(t, filepath.Join(dir, "python3"), collectPyShim, 0o755)
	// testing.TB has no Setenv in the interface on older toolchains; every caller passes a *testing.T.
	tt, ok := t.(*testing.T)
	if !ok {
		t.Fatal("collectNeedChecker needs a *testing.T")
	}
	tt.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

type collectOpt struct {
	cond, task, transcript, live, cid string
	solved, done                      bool
}

func collectDefaults() collectOpt {
	return collectOpt{cond: "with", task: "fix-bug", transcript: "transcript-skill.jsonl", live: "live", cid: "C-0001", solved: true, done: true}
}

// collectMakeTrial is make_trial: a running trial with its sandbox dir (work/ with the solution applied or not), temp
// DB, transcript and a live agent row. live "" means no agent row.
func collectMakeTrial(t testing.TB, db *sql.DB, root string, o collectOpt) (int64, string, string) {
	t.Helper()
	collectExec(t, db, "DELETE FROM skill_trials WHERE candidate_id=? AND vendor_sha=? AND task=? AND condition=?",
		o.cid, collectSHA, o.task, o.cond) // subtests reuse one key; each gets a fresh row id
	res, err := db.Exec("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,started) VALUES(?,?,?,?,'running','t',?)",
		o.cid, collectSHA, o.task, o.cond, collectStart)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	d := filepath.Join(sandbox.Realpath(root), "trials", sandbox.TrialName(int(id)))
	agent := "eval-" + sandbox.TrialName(int(id))
	for _, sub := range []string{"fh", "home", "cfg/projects/p", "bin"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	collectCopyTree(t, filepath.Join("testdata/collect/tasks", o.task, "repo"), filepath.Join(d, "work"))
	if o.solved {
		collectCopyTree(t, filepath.Join("testdata/collect/tasks", o.task, "solution"), filepath.Join(d, "work"))
	}
	if o.transcript != "" {
		b, err := os.ReadFile(filepath.Join("testdata/collect/transcripts", o.transcript))
		if err != nil {
			t.Fatal(err)
		}
		collectWrite(t, filepath.Join(d, "cfg", "projects", "p", "s.jsonl"), string(b), 0o644)
	}
	collectSeedFH(t, d, agent, o.done)
	if o.live != "" {
		collectExec(t, db, "INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", agent, "evaluator", o.live, collectStart)
	}
	collectExec(t, db, "UPDATE skill_trials SET agent=?, sandbox=? WHERE id=?", agent, d, id)
	return id, d, agent
}

// collectSeedFH writes the evaluator's temp DB: the agent row says done (ended 10:05) or still live.
func collectSeedFH(t testing.TB, d, agent string, done bool) {
	t.Helper()
	p := skilleval.FhDB(filepath.Join(d, "fh"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	tdb, err := sql.Open("sqlite", "file:"+p)
	if err != nil {
		t.Fatal(err)
	}
	defer tdb.Close()
	collectExec(t, tdb, "CREATE TABLE agents(name TEXT PRIMARY KEY, status TEXT, ended TEXT)")
	if done {
		collectExec(t, tdb, "INSERT INTO agents VALUES(?,?,?)", agent, "done", collectDone)
	} else {
		collectExec(t, tdb, "INSERT INTO agents VALUES(?,?,NULL)", agent, "live")
	}
}

func (e *collectEnv) trial(o collectOpt) (int64, string, string) {
	e.t.Helper()
	return collectMakeTrial(e.t, e.db, e.root, o)
}

func (e *collectEnv) row(id int64) skilleval.Trial {
	e.t.Helper()
	tr, ok, err := skilleval.GetTrial(e.db, id)
	if err != nil || !ok {
		e.t.Fatalf("trial %d: %v %v", id, ok, err)
	}
	return tr
}

// agentRow is self.agent(name) as a column map.
func (e *collectEnv) agentRow(name string) map[string]any {
	e.t.Helper()
	rows, err := e.db.Query("SELECT * FROM agents WHERE name=?", name)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		e.t.Fatal(err)
	}
	if !rows.Next() {
		e.t.Fatalf("no agent %s", name)
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		e.t.Fatal(err)
	}
	m := map[string]any{}
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m
}

func (e *collectEnv) agentStatus(name string) string {
	e.t.Helper()
	s, _ := e.agentRow(name)["status"].(string)
	return s
}

func (e *collectEnv) collector() skilleval.Collector { return skilleval.Collector{TasksDir: e.tasks} }

func (e *collectEnv) collect(id int64, reason string) (skilleval.Trial, error) {
	e.t.Helper()
	return e.collector().CollectTrial(e.db, id, e.root, e.api, reason)
}

func (e *collectEnv) mustCollect(id int64, reason string) skilleval.Trial {
	e.t.Helper()
	row, err := e.collect(id, reason)
	if err != nil {
		e.t.Fatalf("collect %d (%q): %v", id, reason, err)
	}
	return row
}

func collectIsCollectError(err error) bool {
	var ce *skilleval.CollectError
	return errors.As(err, &ce)
}

func collectExists(p string) bool {
	_, err := os.Lstat(p)
	return err == nil
}

func collectWant(t testing.TB, what string, got, want any) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s = %#v, want %#v", what, got, want)
	}
}

func collectPassed(t testing.TB, row skilleval.Trial, status string, passed int64) {
	t.Helper()
	if row.Status != status || !row.Passed.Valid || row.Passed.Int64 != passed {
		t.Fatalf("status/passed = %s/%v, want %s/%d", row.Status, row.Passed, status, passed)
	}
}

// ---- CollectTests ----------------------------------------------------------------------------------------------

func TestSkillevalCollect_PassedRecordsMetricsAndCleansUp(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, d, agent := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	collectPassed(t, row, "passed", 1)
	collectWant(t, "tokens", [4]int64{row.TokensIn.Int64, row.TokensOut.Int64, row.CacheRead.Int64, row.CacheWrite.Int64}, [4]int64{110, 55, 1000, 250})
	collectWant(t, "wall_s", row.WallS, sql.NullInt64{Int64: 300, Valid: true})
	collectWant(t, "skill_invoked", row.SkillInvoked, sql.NullInt64{Int64: 1, Valid: true})
	collectWant(t, "check_output", row.CheckOutput.String, collectPassOut)
	if len(row.CheckOutput.String) > 2048 {
		t.Fatalf("check_output is %d bytes", len(row.CheckOutput.String))
	}
	collectWant(t, "ended", row.Ended.String, collectNow)
	if got := e.agentStatus(agent); got != "stopped" {
		t.Fatalf("agent %s: stopped first (the tick reaps it), not left live", got)
	}
	if collectExists(d) {
		t.Fatal("sandbox must be deleted on passed")
	}
	if len(e.api.stopped) != 1 {
		t.Fatalf("stops: %v", e.api.stopped)
	}
}

func TestSkillevalCollect_CheckOutputKeepsLast2kb(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check(collectChkBig)
	id, _, _ := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	if len(row.CheckOutput.String) != 2048 || !strings.HasSuffix(row.CheckOutput.String, "TAIL") {
		t.Fatalf("len %d, tail %q", len(row.CheckOutput.String), row.CheckOutput.String[max(0, len(row.CheckOutput.String)-8):])
	}
}

func TestSkillevalCollect_SkillInvokedVariants(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	for _, c := range []struct {
		transcript string
		want       int64
	}{{"transcript-skill.jsonl", 1}, {"transcript-read.jsonl", 1}, {"transcript-none.jsonl", 0}} {
		t.Run(c.transcript, func(t *testing.T) {
			o := collectDefaults()
			o.transcript = c.transcript
			id, _, _ := e.trial(o)
			row := e.mustCollect(id, "")
			collectWant(t, "skill_invoked", row.SkillInvoked, sql.NullInt64{Int64: c.want, Valid: true})
			collectWant(t, "tokens", [2]int64{row.TokensIn.Int64, row.TokensOut.Int64}, [2]int64{110, 55})
		})
	}
}

func TestSkillevalCollect_WithoutConditionHasNullSkillInvoked(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	o := collectDefaults()
	o.cond = "without"
	id, _, _ := e.trial(o)
	row := e.mustCollect(id, "")
	if row.SkillInvoked.Valid {
		t.Fatalf("skill_invoked = %v, want NULL", row.SkillInvoked)
	}
	collectWant(t, "status", row.Status, "passed")
}

func TestSkillevalCollect_FailedSolutionIsFailedAndSandboxDeleted(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check(collectChkFail)
	o := collectDefaults()
	o.solved = false
	id, d, _ := e.trial(o)
	row := e.mustCollect(id, "")
	collectPassed(t, row, "failed", 0)
	if !strings.Contains(row.CheckOutput.String, "FAIL") {
		t.Fatalf("check_output %q", row.CheckOutput.String)
	}
	if collectExists(d) {
		t.Fatal("sandbox must be deleted on failed")
	}
}

func TestSkillevalCollect_TimeoutAndOverBudgetStopFirstAndKeepSandbox(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	for _, reason := range []string{"timeout", "over-budget"} {
		t.Run(reason, func(t *testing.T) {
			o := collectDefaults()
			o.done = false
			id, d, agent := e.trial(o)
			row := e.mustCollect(id, reason)
			collectWant(t, "last stop (force_stop after the stop flag was set)", e.api.stopped[len(e.api.stopped)-1], collectStop{agent, "now", collectNow})
			collectPassed(t, row, reason, 0)
			if st, err := os.Stat(d); err != nil || !st.IsDir() {
				t.Fatal("sandbox kept for T9")
			}
			// force_stop comes before the checker, which still runs once: its staged check.py is the evidence
			if !collectExists(filepath.Join(d, "work", "check.py")) {
				t.Fatal("the checker did not run after the stop")
			}
			collectWant(t, "agent status", e.agentStatus(agent), "stopped")
		})
	}
}

func TestSkillevalCollect_CancelledStopsSkipsCheckerAndDeletes(t *testing.T) {
	e := collectSetup(t)
	o := collectDefaults()
	o.done = false
	id, d, agent := e.trial(o)
	row := e.mustCollect(id, "cancelled")
	collectWant(t, "status", row.Status, "cancelled")
	if len(e.api.stopped) == 0 || e.api.stopped[0].name != agent {
		t.Fatalf("stops: %v", e.api.stopped)
	}
	// the checker (which would put output on the row) did not run: no userns is needed for this case at all
	collectWant(t, "check_output", row.CheckOutput.String, "")
	if collectExists(d) {
		t.Fatal("sandbox must be deleted on cancelled")
	}
}

func TestSkillevalCollect_DonePathStopsTheEvaluatorBeforeStagingAndChecking(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check(collectChkLs) // the checker lists work/: proves check.py and the hidden test were staged before it ran
	id, d, agent := e.trial(collectDefaults())
	var stagedAtStop []bool
	e.api.onStop = func(db *sql.DB, name string) { // runs at force_stop: nothing is staged yet
		stagedAtStop = append(stagedAtStop, collectExists(filepath.Join(d, "work", "check.py")))
	}
	row := e.mustCollect(id, "")
	collectWant(t, "staged at stop", stagedAtStop, []bool{false})
	collectWant(t, "stops (force_stop on the done path too)", e.api.stopped, []collectStop{{agent, "now", collectNow}})
	collectWant(t, "status", row.Status, "passed")
	for _, name := range []string{"check.py", "test_taskdeps_hidden.py"} {
		if !strings.Contains(row.CheckOutput.String, name+"\n") {
			t.Fatalf("checker saw %q", row.CheckOutput.String)
		}
	}
	collectWant(t, "agent status", e.agentStatus(agent), "stopped")
}

func TestSkillevalCollect_FailedForceStopRunsNoCheckerAndLeavesTrialAndAgentLive(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	for _, reason := range []string{"", "timeout", "over-budget", "cancelled"} {
		t.Run("reason="+reason, func(t *testing.T) {
			e.api = collectNewAPI(t)
			e.api.result = "failed:pane-open"
			o := collectDefaults()
			o.done = reason == ""
			id, d, agent := e.trial(o)
			_, err := e.collect(id, reason)
			if !collectIsCollectError(err) || !strings.Contains(err.Error(), "failed:pane-open") {
				t.Fatalf("err = %v, want a CollectError naming failed:pane-open", err)
			}
			row := e.row(id)
			collectWant(t, "status (no terminal row, a retry is possible)", row.Status, "running")
			if !strings.Contains(row.CheckOutput.String, "checker not run") {
				t.Fatalf("check_output %q", row.CheckOutput.String)
			}
			collectWant(t, "agent status (row not forced to done)", e.agentStatus(agent), "live")
			if st, err := os.Stat(d); err != nil || !st.IsDir() {
				t.Fatal("sandbox must stay")
			}
			if collectExists(filepath.Join(d, "work", "check.py")) {
				t.Fatal("nothing staged")
			}
			e.api = collectNewAPI(t) // the retry succeeds once the stop works
			got := e.mustCollect(id, reason)
			switch got.Status {
			case "passed", "cancelled", "timeout", "over-budget":
			default:
				t.Fatalf("retry status %s", got.Status)
			}
		})
	}
}

func TestSkillevalCollect_CollectRetryReattemptsAStopThatFailed(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.api = collectNewAPI(t)
	e.api.claim, e.api.failFirst = true, 1
	id, _, agent := e.trial(collectDefaults())
	if _, err := e.collect(id, ""); !collectIsCollectError(err) {
		t.Fatalf("first collect: %v, want a CollectError", err)
	}
	if e.agentRow(agent)["stop_attempt_at"] == nil {
		t.Fatal("the failed stop left no attempt mark")
	}
	row := e.mustCollect(id, "")
	if e.api.calls != 2 {
		t.Fatalf("calls = %d, the retry must re-attempt the stop instead of skipping", e.api.calls)
	}
	collectWant(t, "status", row.Status, "passed")
	collectWant(t, "agent status", e.agentStatus(agent), "stopped")
}

func TestSkillevalCollect_BackgroundEvaluatorCannotForgeAPass(t *testing.T) {
	// The evaluator keeps running and would make check.py exit 0; the unconfirmed stop means it is never run.
	e := collectSetup(t)
	e.check(collectChkFail) // were it run, the verdict would be failed: a "running" row proves it was not
	e.api = collectNewAPI(t)
	e.api.result = "failed:process-alive"
	o := collectDefaults()
	o.solved = false
	id, d, _ := e.trial(o)
	collectWrite(t, filepath.Join(d, "work", "check.py"), "exit 0\n", 0o644) // what the live evaluator wrote
	if _, err := e.collect(id, ""); !collectIsCollectError(err) {
		t.Fatalf("err = %v, want a CollectError", err)
	}
	row := e.row(id)
	if row.Status != "running" || row.Passed.Valid {
		t.Fatalf("status/passed = %s/%v, want running/NULL", row.Status, row.Passed)
	}
}

func TestSkillevalCollect_ConcurrentCollectOfOneTrialIsRefused(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, d, _ := e.trial(collectDefaults())
	lockdir := filepath.Join(sandbox.Realpath(e.root), "locks")
	if err := os.MkdirAll(lockdir, 0o755); err != nil {
		t.Fatal(err)
	}
	lk, err := os.OpenFile(filepath.Join(lockdir, sandbox.TrialName(int(id))+".lock"), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lk.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	_, cerr := e.collect(id, "")
	if !collectIsCollectError(cerr) || !strings.Contains(cerr.Error(), "another process") {
		t.Fatalf("err = %v, want a CollectError mentioning another process", cerr)
	}
	if err := lk.Close(); err != nil { // closing releases the flock
		t.Fatal(err)
	}
	collectWant(t, "status", e.row(id).Status, "running")
	if !collectExists(d) {
		t.Fatal("sandbox must stay")
	}
	collectWant(t, "status after the lock is gone", e.mustCollect(id, "").Status, "passed")
}

func TestSkillevalCollect_LostRowWithoutTempDbDoneIsError(t *testing.T) {
	for _, live := range []string{"", "stopped"} {
		t.Run("live="+live, func(t *testing.T) {
			e := collectSetup(t)
			o := collectDefaults()
			o.done, o.live = false, live
			id, d, _ := e.trial(o)
			row := e.mustCollect(id, "")
			collectWant(t, "status", row.Status, "error")
			if !strings.Contains(row.CheckOutput.String, "exited without finishing") {
				t.Fatalf("check_output %q", row.CheckOutput.String)
			}
			if collectExists(filepath.Join(d, "work", "check.py")) {
				t.Fatal("the checker must not run: nothing was staged")
			}
			if !collectExists(d) {
				t.Fatal("sandbox must stay")
			}
		})
	}
}

func TestSkillevalCollect_StillRunningWithoutReasonIsRefusedAndChangesNothing(t *testing.T) {
	e := collectSetup(t)
	o := collectDefaults()
	o.done = false
	id, _, agent := e.trial(o)
	if _, err := e.collect(id, ""); !collectIsCollectError(err) {
		t.Fatalf("err = %v, want a CollectError", err)
	}
	collectWant(t, "status", e.row(id).Status, "running")
	collectWant(t, "agent status", e.agentStatus(agent), "live")
}

func TestSkillevalCollect_SecondCollectIsANoOp(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, _, agent := e.trial(collectDefaults())
	e.mustCollect(id, "")
	before := e.row(id)
	agentBefore := e.agentRow(agent)
	stops, events := len(e.api.stopped), len(e.api.events)
	second := e.mustCollect(id, "")
	collectWant(t, "second row", second, before)
	collectWant(t, "agent row", e.agentRow(agent), agentBefore)
	if len(e.api.stopped) != stops || len(e.api.events) != events {
		t.Fatalf("the second collect did work: stops %d->%d events %d->%d", stops, len(e.api.stopped), events, len(e.api.events))
	}
}

func TestSkillevalCollect_SecondCollectOfATimeoutDoesNotStopAgain(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	o := collectDefaults()
	o.done = false
	id, _, _ := e.trial(o)
	e.mustCollect(id, "timeout")
	n := len(e.api.stopped)
	e.mustCollect(id, "timeout")
	if len(e.api.stopped) != n {
		t.Fatalf("stops %d -> %d", n, len(e.api.stopped))
	}
}

func TestSkillevalCollect_HangingCheckerIsFailedWithTimeoutNote(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check(collectChkHang)
	if skilleval.CheckTimeout != 60*time.Second { // the Python assertion on run_contained's timeout=60
		t.Fatalf("CheckTimeout = %v, want 60s", skilleval.CheckTimeout)
	}
	old := skilleval.CheckTimeout
	skilleval.CheckTimeout = 2 * time.Second // a variable only so a test can shorten the wait
	t.Cleanup(func() { skilleval.CheckTimeout = old })
	id, _, _ := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	collectPassed(t, row, "failed", 0)
	if !strings.Contains(row.CheckOutput.String, "exceeded 2 s") || !strings.Contains(row.CheckOutput.String, "partial") {
		t.Fatalf("check_output %q", row.CheckOutput.String)
	}
}

func TestSkillevalCollect_UnknownTrialAndPendingTrial(t *testing.T) {
	e := collectSetup(t)
	if _, err := e.collect(999, ""); !collectIsCollectError(err) {
		t.Fatalf("unknown trial: %v, want a CollectError", err)
	}
	res, err := e.db.Exec("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) VALUES('C-0001',?,'fix-bug','without','pending','t')", collectSHA)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.collect(id, ""); !collectIsCollectError(err) {
		t.Fatalf("pending trial: %v, want a CollectError", err)
	}
}

func TestSkillevalCollect_HostileTranscriptEntriesAreDroppedNotFollowed(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, d, _ := e.trial(collectDefaults())
	proj := filepath.Join(d, "cfg", "projects")
	if err := os.Symlink("/etc/passwd", filepath.Join(proj, "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(proj, "p", "fifo.jsonl"), 0o644); err != nil { // a parser opening this would block forever
		t.Fatal(err)
	}
	row := e.mustCollect(id, "")
	collectWant(t, "status", row.Status, "passed")
	collectWant(t, "tokens_in", row.TokensIn.Int64, int64(110))
}

// ---- ContainmentTests ------------------------------------------------------------------------------------------

func TestSkillevalCollect_CheckerGoesOnlyThroughRunContained(t *testing.T) {
	// Python mocks run_contained and asserts its arguments; here the contained script reports what it sees: cwd, the
	// two-variable environment, no network interface but lo, no sibling trial dirs. The timeout is the constant.
	e := collectSetup(t)
	e.needChecker()
	e.check("echo \"cwd=$PWD\"\necho \"argv0=$0\"\necho \"ifaces=$(sed 1,2d /proc/net/dev | cut -d: -f1 | tr -d ' ' | tr '\\n' ' ')\"\n" +
		"echo \"siblings=$(ls ../.. | tr '\\n' ' ')\"\n" +
		"env | grep -v -E '^(PWD|OLDPWD|SHLVL|_)=' | sort\n" +
		"echo \"tokenvars=$(env | cut -d= -f1 | grep -i -E 'TOKEN|API_KEY|PROXY' | wc -l)\"\n")
	id, d, _ := e.trial(collectDefaults())
	if err := os.MkdirAll(filepath.Join(filepath.Dir(d), "tr9999"), 0o755); err != nil { // a sibling the checker must not see
		t.Fatal(err)
	}
	row := e.mustCollect(id, "")
	collectWant(t, "status", row.Status, "passed")
	out := row.CheckOutput.String
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/bin:/bin"
	}
	for _, want := range []string{
		"cwd=" + filepath.Join(d, "work") + "\n", // args[0] is the trial dir, cwd=work
		"argv0=check.py\n",
		"HOME=" + filepath.Join(d, "home") + "\n",
		"PATH=" + path + "\n",
		"tokenvars=0\n",
		"ifaces=lo \n", // no network for the checker
		"siblings=" + sandbox.TrialName(int(id)) + " \n",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("checker output lacks %q:\n%s", want, out)
		}
	}
	envLines := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "HOME=") || strings.HasPrefix(l, "PATH=") {
			envLines++
		}
	}
	if envLines != 2 {
		t.Fatalf("env is exactly PATH and HOME:\n%s", out)
	}
}

func TestSkillevalCollect_HiddenTestsAndCheckAreStagedBeforeTheRun(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check(collectChkLs)
	id, _, _ := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	names := strings.Split(strings.TrimSpace(row.CheckOutput.String), "\n")
	for _, want := range []string{"check.py", "test_taskdeps_hidden.py"} {
		found := false
		for _, n := range names {
			found = found || n == want
		}
		if !found {
			t.Fatalf("%s not staged before the run: %v", want, names)
		}
	}
}

func TestSkillevalCollect_PlantedSymlinksInWorkDoNotRedirectTheStaging(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, d, _ := e.trial(collectDefaults())
	outside := filepath.Join(filepath.Dir(e.root), "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"check.py", "test_taskdeps_hidden.py"} {
		if err := os.Symlink(filepath.Join(outside, "victim.py"), filepath.Join(d, "work", n)); err != nil {
			t.Fatal(err)
		}
	}
	row := e.mustCollect(id, "")
	collectWant(t, "status", row.Status, "passed")
	ents, err := os.ReadDir(outside)
	if err != nil || len(ents) != 0 {
		t.Fatalf("nothing may be written through a planted symlink: %v %v", ents, err)
	}
}

func TestSkillevalCollect_NoUsernsMeansErrorAndCheckPyNeverRan(t *testing.T) {
	e := collectSetup(t)
	// an unshare that fails the way a restricted host does: no namespaces, no checker, no fallback
	fake := filepath.Join(t.TempDir(), "unshare")
	collectWrite(t, fake, "#!/bin/sh\necho 'restricted by test' >&2\nexit 1\n", 0o755)
	e.api.unshare = fake
	id, d, _ := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	collectWant(t, "status", row.Status, "error")
	for _, want := range []string{"user namespaces unavailable", "restricted by test"} {
		if !strings.Contains(row.CheckOutput.String, want) {
			t.Fatalf("check_output %q lacks %q", row.CheckOutput.String, want)
		}
	}
	if collectExists(filepath.Join(d, "work", "check.py")) {
		t.Fatal("check.py was not even staged")
	}
	if !collectExists(d) {
		t.Fatal("sandbox must stay")
	}
}

func TestSkillevalCollect_NoDirectExecOfCheckPy(t *testing.T) {
	// The Go counterpart of test_skilleval_has_no_direct_exec_of_check_py: the collect/queue/results code starts no
	// process of its own; check.py reaches a process only through sandbox.RunContained. (builder.go runs git, the
	// factory init and the preflight, none of which is third-party code.)
	root, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"collect.go", "queue.go", "results.go", "trial.go", "ops.go", "stopproxy.go"} {
		b, err := os.ReadFile(filepath.Join(root, "internal", "skilleval", f))
		if err != nil {
			t.Fatal(err)
		}
		for _, bad := range []string{"exec.Command", "syscall.Exec", "syscall.ForkExec", "os.StartProcess", "plugin.Open", "\"os/exec\""} {
			if strings.Contains(string(b), bad) && !(f == "collect.go" && bad == "\"os/exec\"") { // collect.go: exec.LookPath only
				t.Errorf("internal/skilleval/%s must not use %s", f, bad)
			}
		}
	}
	b, err := os.ReadFile(filepath.Join(root, "internal", "skilleval", "collect.go"))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(b), "sandbox.RunContained("); n != 1 {
		t.Errorf("collect.go calls sandbox.RunContained %d times, want exactly 1 (the checker)", n)
	}
}

// ---- ProxyTests ------------------------------------------------------------------------------------------------

// collectStartProxy starts a process that looks like the trial's egress proxy (its cmdline names egress.py) and
// records its pid in <trial>/proxy.pid. The returned channel closes when it has exited.
func collectStartProxy(t testing.TB, d string) <-chan struct{} {
	t.Helper()
	cmd := exec.Command("sh", "-c", "sleep 60; true", "egress.py")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return // already gone, which is the normal case after collect
		}
	})
	done := make(chan struct{})
	go func() {
		if err := cmd.Wait(); err != nil {
			_ = err // killed by the code under test: the exit status is of no interest
		}
		close(done)
	}()
	collectWrite(t, filepath.Join(d, collectProxyPid), strconv.Itoa(cmd.Process.Pid)+"\n", 0o644)
	return done
}

func TestSkillevalCollect_CollectStopsTheTrialProxy(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	id, d, _ := e.trial(collectDefaults())
	gone := collectStartProxy(t, d)
	row := e.mustCollect(id, "")
	collectWant(t, "status", row.Status, "passed")
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the trial proxy process must be gone after collect")
	}
}

func TestSkillevalCollect_StopProxyIsCalledBeforeTheChecker(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	e.check("if [ -e ../" + collectProxyPid + " ]; then echo proxy_pid=present; else echo proxy_pid=absent; fi\n")
	o := collectDefaults()
	o.done = false
	id, d, _ := e.trial(o)
	gone := collectStartProxy(t, d)
	if !collectExists(filepath.Join(d, collectProxyPid)) {
		t.Fatal("setup: no pid file")
	}
	row := e.mustCollect(id, "timeout")
	collectWant(t, "status", row.Status, "timeout")
	if !strings.Contains(row.CheckOutput.String, "proxy_pid=absent") { // the checker ran after stop_proxy removed the file
		t.Fatalf("check_output %q", row.CheckOutput.String)
	}
	select {
	case <-gone:
	case <-time.After(5 * time.Second):
		t.Fatal("the trial proxy process must be gone after collect")
	}
}

// ---- CliTests (bin/factory: NeedPython) --------------------------------------------------------------------------

// collectCLI is CliTests.setUp: a harness home whose skilleval_root is <root>/evalroot, with candidate C-0001
// seeded in the harness DB and a lead agent.
type collectCLI struct {
	e    *ft.Env
	root string
	db   *sql.DB
}

func collectNewCLI(t *testing.T, needPython bool) *collectCLI {
	t.Helper()
	e := ft.New(t)
	if needPython {
		e.NeedPython()
	}
	c := &collectCLI{e: e, root: filepath.Join(e.Root, "evalroot")}
	c.db = e.RawDB()
	t.Cleanup(func() { c.db.Close() })
	collectSeedCandidateAndVendor(t, c.db, c.root)
	// `factory config` itself is handed to Python; the row it writes is all the cases need, so write it directly.
	collectExec(t, c.db, "INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", "skilleval_root", c.root)
	e.SeedAgent(ft.AgentRow{Name: "zz-lead", Role: "lead"})
	return c
}

func (c *collectCLI) as(agent string) ft.Opts { return ft.Opts{Agent: agent} }

func TestSkillevalCollect_WorkersAreRefusedAllowedRolesGetPastTheGuard(t *testing.T) {
	c := collectNewCLI(t, true)
	for _, w := range [][2]string{{"zz-coder", "coder"}, {"zz-scout", "scout"}, {"zz-eval", "evaluator"}, {"zz-qa", "qa"}} {
		c.e.SeedAgent(ft.AgentRow{Name: w[0], Role: w[1]})
		t.Run(w[1], func(t *testing.T) {
			p := c.e.FailWith(c.as(w[0]), 3, "skilleval", "collect", "1")
			if !strings.Contains(p.Stderr+p.Stdout, "may not") {
				t.Fatalf("output %q", p.Stderr+p.Stdout)
			}
		})
	}
	for _, who := range []string{"owner", "zz-lead"} {
		t.Run(who, func(t *testing.T) {
			p := c.e.FailWith(c.as(who), 2, "skilleval", "collect", "999") // past the role guard: unknown trial
			if !strings.Contains(p.Stderr+p.Stdout, "no trial 999") {
				t.Fatalf("output %q", p.Stderr+p.Stdout)
			}
		})
	}
	c.e.SeedAgent(ft.AgentRow{Name: "zz-director", Role: "director"})
	c.e.FailWith(c.as("zz-director"), 2, "skilleval", "collect", "999")
}

func TestSkillevalCollect_CollectUsageErrors(t *testing.T) {
	c := collectNewCLI(t, true)
	c.e.FailWith(c.as("owner"), 2, "skilleval", "collect")
	c.e.FailWith(c.as("owner"), 2, "skilleval", "collect", "x/../y")
}

func TestSkillevalCollect_ResultsLinesAndSummary(t *testing.T) {
	c := collectNewCLI(t, false) // results and list are answered by the Go CLI
	type row struct {
		cid, task, cond, st string
		p, ti, to, w, inv   any
	}
	for _, r := range []row{
		{"C-0001", "fix-bug", "with", "passed", 1, 110, 55, 300, 1},
		{"C-0001", "fix-bug", "without", "failed", 0, 200, 100, 500, nil},
		{"C-0001", "add-feature", "with", "timeout", 0, 400, 200, 600, 0},
		{"C-0001", "add-feature", "without", "passed", 1, 20, 10, 100, nil},
	} {
		collectExec(t, c.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,passed,tokens_in,"+
			"tokens_out,wall_s,skill_invoked) VALUES(?,?,?,?,?,'t',?,?,?,?,?)", r.cid, collectSHA, r.task, r.cond, r.st, r.p, r.ti, r.to, r.w, r.inv)
	}
	collectExec(t, c.db, "INSERT INTO scout_candidates(id,key,kind,status,first_seen,last_seen) VALUES('C-0002','k','unknown','new','t','t')")
	collectExec(t, c.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) VALUES('C-0002','d','fix-bug','with','running','t')")
	out := strings.Split(strings.TrimRight(c.e.OKWith(c.as("owner"), "skilleval", "results", "--candidate", "C-0001").Stdout, "\n"), "\n")
	want := []string{
		"C-0001 add-feature with: trial 3 status=timeout passed=0 tokens=600 wall_s=600 skill_invoked=0",
		"C-0001 add-feature without: trial 4 status=passed passed=1 tokens=30 wall_s=100 skill_invoked=-",
		"C-0001 fix-bug with: trial 1 status=passed passed=1 tokens=165 wall_s=300 skill_invoked=1",
		"C-0001 fix-bug without: trial 2 status=failed passed=0 tokens=300 wall_s=500 skill_invoked=-",
		"C-0001 summary: with: passed 1/2 tasks, median tokens 382, median wall_s 450; " +
			"without: passed 1/2 tasks, median tokens 165, median wall_s 300",
	}
	collectWant(t, "results lines", out, want)
	every := c.e.OKWith(c.as("zz-lead"), "skilleval", "results").Stdout
	for _, w := range []string{"C-0002 summary", "C-0002 fix-bug with: trial 5 status=running passed=- tokens=- wall_s=- skill_invoked=-"} {
		if !strings.Contains(every, w) {
			t.Fatalf("results lacks %q:\n%s", w, every)
		}
	}
	listed := strings.Split(strings.TrimRight(c.e.OKWith(c.as("owner"), "skilleval", "list", "--candidate", "C-0002").Stdout, "\n"), "\n")
	if len(listed) != 2 { // T-0112: list shows the candidate summary too
		t.Fatalf("list: %q", listed)
	}
	if !strings.Contains(listed[0], "status=running") || !strings.HasPrefix(listed[1], "C-0002 summary:") || !strings.HasSuffix(listed[1], "; running 1") {
		t.Fatalf("list: %q", listed)
	}
	none := c.e.OKWith(c.as("owner"), "skilleval", "results", "--candidate", "C-9999").Stdout
	collectWant(t, "no trials", strings.TrimSpace(none), "no trials")
}

func TestSkillevalCollect_CollectThroughTheCliWithALiveUnstoppableAgentRunsNoChecker(t *testing.T) {
	// Real bin/factory force_stop (no session id for the row, so the stop cannot be confirmed): exit 2.
	c := collectNewCLI(t, true)
	id, d, agent := collectMakeTrial(t, c.db, c.root, collectDefaults())
	p := c.e.RunWith(c.as("owner"), "skilleval", "collect", strconv.FormatInt(id, 10))
	if p.Exit != 2 || !strings.Contains(p.Stderr, "checker not run") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", p.Exit, p.Stdout, p.Stderr)
	}
	if collectExists(filepath.Join(d, "work", "check.py")) {
		t.Fatal("nothing may be staged")
	}
	var status, agentStatus string
	if err := c.db.QueryRow("SELECT status FROM skill_trials WHERE id=?", id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := c.db.QueryRow("SELECT status FROM agents WHERE name=?", agent).Scan(&agentStatus); err != nil {
		t.Fatal(err)
	}
	collectWant(t, "trial/agent status", [2]string{status, agentStatus}, [2]string{"running", "live"})
}

func TestSkillevalCollect_CollectThroughTheCliWithUsernsUnavailableIsError(t *testing.T) {
	c := collectNewCLI(t, true)
	o := collectDefaults()
	o.live = ""
	id, d, _ := collectMakeTrial(t, c.db, c.root, o)
	p := c.e.RunWith(ft.Opts{Agent: "owner", Env: map[string]string{"NSBOX_TEST_UNSHARE": "/bin/false"}}, "skilleval", "collect", strconv.FormatInt(id, 10))
	if p.Exit != 0 || !strings.Contains(p.Stdout, "status=error") {
		t.Fatalf("exit %d\nstdout: %s\nstderr: %s", p.Exit, p.Stdout, p.Stderr)
	}
	if collectExists(filepath.Join(d, "work", "check.py")) {
		t.Fatal("nothing may be staged")
	}
}

// ---- ContainedCheckerIntegration -------------------------------------------------------------------------------

// collectIntegrationScript is CHECK_PY: the checker has no network and sees the real ~/.claude and ~/factory empty.
func collectIntegrationScript(home string) string {
	return "REAL='" + home + "'\n" +
		"if timeout 3 bash -c 'exec 3<>/dev/tcp/1.1.1." + "1/443' 2>/dev/null; then fetch=ok; else fetch=failed; fi\n" +
		"claude=$(ls -A \"$REAL/.claude\" 2>/dev/null | tr '\\n' ' ')\n" +
		"factory=$(ls -A \"$REAL/factory\" 2>/dev/null | tr '\\n' ' ')\n" +
		"envv=$(env | cut -d= -f1 | grep -i -E 'TOKEN|API_KEY|PROXY' | tr '\\n' ' ')\n" +
		"echo \"ifaces=$(sed 1,2d /proc/net/dev | cut -d: -f1 | tr -d ' ' | tr '\\n' ' ')\"\n" +
		"echo \"fetch=$fetch claude=[$claude] factory=[$factory] env=[$envv]\"\n" +
		"[ \"$fetch\" = failed ] && [ -z \"$claude\" ] && [ -z \"$factory\" ] && [ -z \"$envv\" ]\n"
}

func TestSkillevalCollect_CheckerSeesNoNetworkAndEmptySecretDirs(t *testing.T) {
	e := collectSetup(t)
	e.needChecker()
	home := sandbox.RealHome()
	e.api.home = home
	e.check(collectIntegrationScript(home))
	t.Logf("contained checker integration: running (userns ok, home %s)", home)
	id, _, _ := e.trial(collectDefaults())
	row := e.mustCollect(id, "")
	collectWant(t, "status ("+row.CheckOutput.String+")", row.Status, "passed")
	if !strings.Contains(row.CheckOutput.String, "fetch=failed claude=[] factory=[] env=[]") {
		t.Fatalf("check_output %q", row.CheckOutput.String)
	}
	if !strings.Contains(row.CheckOutput.String, "ifaces=lo \n") {
		t.Fatalf("the checker must see only the loopback interface: %q", row.CheckOutput.String)
	}
}
