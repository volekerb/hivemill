package skilleval

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
)

// TestMain makes the test binary its own stage-1 helper: Cfg.Self is this executable, so the contained checker runs
// `self --setup ...` through the real entry point (sandbox.Main).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 {
		if code, handled := sandbox.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); handled {
			os.Exit(code)
		}
	}
	os.Exit(m.Run())
}

func newDB(t testing.TB) *sql.DB {
	t.Helper()
	path := dbtest.New(t, t.TempDir(), factorydb.SchemaVersion)
	d, err := factorydb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ddl, err := os.ReadFile("testdata/scout_ddl.sql") // a copy of scout.db.SCOUT_DDL (scout/testdata/scout_ddl.sql)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

func mustExec(t testing.TB, db *sql.DB, q string, a ...any) {
	t.Helper()
	if _, err := db.Exec(q, a...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

type fakeAPI struct {
	clock     time.Time
	cfg       map[string]string
	home      string
	unshare   string
	stopRes   string
	stopCalls []string
	launched  []int64
	launchErr error
	events    []string
	onStop    func(db *sql.DB, name string)
}

func newAPI(t testing.TB) *fakeAPI {
	return &fakeAPI{clock: time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC), home: t.TempDir(), unshare: "unshare", stopRes: "stopped",
		cfg: map[string]string{"skilleval_root": t.TempDir() + "/root", "skilleval_trial_minutes": "30", "skilleval_trial_max_tokens": "1000",
			"skilleval_max_concurrent": "1", "skilleval_max_trials_per_day": "5", "skilleval_keep_failed_h": "24"}}
}
func (a *fakeAPI) Now() string                             { return scout.Stamp(a.clock) }
func (a *fakeAPI) Clock() time.Time                        { return a.clock }
func (a *fakeAPI) Cfg(_ *sql.DB, k string) (string, error) { return a.cfg[k], nil }
func (a *fakeAPI) Event(db *sql.DB, entity, id, action, detail string) error {
	a.events = append(a.events, action+":"+id+":"+detail)
	_, err := db.Exec("INSERT INTO events(ts, entity, entity_id, action, detail) VALUES(?,?,?,?,?)", a.Now(), entity, id, action, detail)
	return err
}
func (a *fakeAPI) ForceStop(db *sql.DB, name, mode string, seen sql.NullString) (string, string, error) {
	a.stopCalls = append(a.stopCalls, name+":"+mode)
	if a.onStop != nil {
		a.onStop(db, name)
	}
	return a.stopRes, "stub note", nil
}
func (a *fakeAPI) Launch(db *sql.DB, t Trial, root, home, unshare string) error {
	a.launched = append(a.launched, t.ID)
	if a.launchErr != nil {
		return a.launchErr
	}
	name := fmt.Sprintf("eval-tr%04d", t.ID)
	if _, err := db.Exec("UPDATE skill_trials SET status='running', agent=? WHERE id=? AND status='starting'", name, t.ID); err != nil {
		return err
	}
	_, err := db.Exec("INSERT INTO agents(name, role, status, started) VALUES(?,?,'live',?)", name, "evaluator", a.Now())
	return err
}
func (a *fakeAPI) Home() string    { return a.home }
func (a *fakeAPI) HomeDir() string { return "/nonexistent-factory-home" }
func (a *fakeAPI) Unshare() string { return a.unshare }

func addCand(t testing.TB, db *sql.DB, id, key, risk, status string) {
	mustExec(t, db, "INSERT INTO scout_candidates(id,key,kind,head_sha,install_risk,first_seen,last_seen,status) VALUES(?,?,'skill','abc123def456',?,'t','t',?)",
		id, key, risk, status)
}

func addTrial(t testing.TB, db *sql.DB, cid, task, cond, status string, extra ...string) int64 {
	t.Helper()
	res, err := db.Exec("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued) VALUES(?,?,?,?,?,'q')", cid, "s", task, cond, status)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func trial(t testing.TB, db *sql.DB, id int64) Trial {
	t.Helper()
	tr, ok, err := GetTrial(db, id)
	if err != nil || !ok {
		t.Fatalf("trial %d: %v %v", id, ok, err)
	}
	return tr
}

// ---- results (expected text: scout/skilleval.py result_lines on the same rows, python3 -I) ----

func TestResultLinesParity(t *testing.T) {
	db := newDB(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	addCand(t, db, "C-0002", scout.ControlKey, "prompt-only", "triaged")
	for _, r := range []struct {
		cid, task, cond, status    string
		passed, wall, tin, tout    any
		check, voidReason, voidWhy any
	}{
		{"C-0001", "fix-bug", "with", "passed", 1, 100, 10, 5, nil, nil, nil},
		{"C-0001", "fix-bug", "without", "failed", 0, 200, 20, 6, nil, nil, nil},
		{"C-0001", "add-feature", "with", "passed", 1, 101, nil, nil, nil, nil, nil},
		{"C-0001", "add-feature", "without", "voided", 0, 5, 1, 1, nil, "infra", "it's \"odd\"\né"},
		{"C-0001", "review-diff", "with", "pending", nil, nil, nil, nil, nil, nil, nil},
		{"C-0001", "review-diff", "without", "running", nil, nil, nil, nil, nil, nil, nil},
		{"C-0002", "fix-bug", "with", "passed", 1, 3, 2, 2, "ok\ncontrol_marker=1", nil, nil},
		{"C-0002", "fix-bug", "without", "passed", 1, 4, 3, 4, "control_marker=0", nil, nil},
	} {
		mustExec(t, db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,passed,wall_s,tokens_in,tokens_out,check_output,void_reason,void_why,queued) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'q')",
			r.cid, "s", r.task, r.cond, r.status, r.passed, r.wall, r.tin, r.tout, r.check, r.voidReason, r.voidWhy)
	}
	want := []string{
		`C-0001 add-feature with: trial 3 status=passed passed=1 tokens=- wall_s=101 skill_invoked=-`,
		`C-0001 add-feature without: trial 4 status=voided passed=0 tokens=2 wall_s=5 skill_invoked=- void_reason=infra void_why='it\'s "odd"` + "\\n" + `é'`,
		`C-0001 fix-bug with: trial 1 status=passed passed=1 tokens=15 wall_s=100 skill_invoked=-`,
		`C-0001 fix-bug without: trial 2 status=failed passed=0 tokens=26 wall_s=200 skill_invoked=-`,
		`C-0001 review-diff with: trial 5 status=pending passed=- tokens=- wall_s=- skill_invoked=-`,
		`C-0001 review-diff without: trial 6 status=running passed=- tokens=- wall_s=- skill_invoked=-`,
		`C-0001 summary: with: passed 2/2 tasks, median tokens 15, median wall_s 100; without: passed 0/1 tasks, median tokens 26, median wall_s 200; pending 1; running 1; voided 1`,
		`C-0002 fix-bug with: trial 7 status=passed passed=1 tokens=4 wall_s=3 skill_invoked=- control_marker=1`,
		`C-0002 fix-bug without: trial 8 status=passed passed=1 tokens=7 wall_s=4 skill_invoked=- control_marker=0`,
		`C-0002 summary: with: passed 1/1 tasks, median tokens 4, median wall_s 3; without: passed 1/1 tasks, median tokens 7, median wall_s 4`,
		`C-0002 control: positive control (local:skilleval-control), not a find; healthy = a with-trial has skill_invoked=1 or control_marker=1`,
	}
	got, err := ResultLines(db, "")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("got\n%s\nerr %v", strings.Join(got, "\n"), err)
	}
	only, _ := ResultLines(db, "C-0002")
	if !reflect.DeepEqual(only, want[7:]) {
		t.Fatalf("filtered: %v", only)
	}
	if none, _ := ResultLines(db, "C-9999"); len(none) != 0 {
		t.Fatal("unknown candidate has no lines")
	}
}

func TestWallSecondsRoundsHalfToEven(t *testing.T) { // Python: 0 2 0 None
	for _, c := range []struct {
		a, b string
		want int64
		ok   bool
	}{
		{"2026-11-14T10:00:00+01:00", "2026-11-14T10:00:00.5+01:00", 0, true},
		{"2026-11-14T10:00:00+01:00", "2026-11-14T10:00:01.5+01:00", 2, true},
		{"2026-11-14T10:00:05+01:00", "2026-11-14T10:00:00+01:00", 0, true},
		{"x", "y", 0, false},
	} {
		got, ok := wallSeconds(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("%s..%s: got (%d,%v) want (%d,%v)", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
}

func TestTailKeepsLastBytesOnACharacterBoundary(t *testing.T) { // Python: 2048 bytes of é, cut start dropped
	got := tail(strings.Repeat("é", 3000), OutputMax)
	if len(got) != 2048 || !strings.HasPrefix(got, "ééééé") {
		t.Fatalf("len %d", len(got))
	}
	if got := tail(strings.Repeat("é", 1023)+"x", 2); got != "\xc3\xa9"[:0]+"x" && got != "x" {
		// 2 bytes: the last é byte is cut mid-character and dropped, leaving "x" (Python decode(..., "ignore"))
		t.Fatalf("got %q", got)
	}
}

func TestStartLogLinesParity(t *testing.T) {
	d := t.TempDir()
	if err := os.Mkdir(filepath.Join(d, "log"), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(s string) {
		if err := os.WriteFile(filepath.Join(d, "log", "start.log"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	row := Trial{Status: "error", Sandbox: sql.NullString{String: d, Valid: true}}
	l300 := strings.Repeat("L", 300)
	write("first\n\x1b[31mred\x1b[0m sk-ant-abc_DEF-123 tail\nrc=7\n​foo​ rc=-9\n\n   \nx rc=4\n" +
		"٣rc=5\nbell\x07\x0b vtab\n" + strings.Repeat("L", 400) + "\n")
	want20 := []string{"  start.log:", "    first", "    [31mred[0m [redacted-token] tail", "    rc=7", "    foo rc=-9", "    x rc=4",
		"    ٣rc=5", "    bell vtab", "    " + l300}
	if got := StartLogLines(row, 20); !reflect.DeepEqual(got, want20) {
		t.Fatalf("n=20: %q", got)
	}
	// the last rc is searched over every line: NBSP is not a word character (matches), U+0663 is (does not)
	want3 := []string{"  start.log:", "    last rc=4", "    ٣rc=5", "    bell vtab", "    " + l300}
	if got := StartLogLines(row, 3); !reflect.DeepEqual(got, want3) {
		t.Fatalf("n=3: %q", got)
	}
	want2 := []string{"  start.log:", "    last rc=4", "    bell vtab", "    " + l300}
	if got := StartLogLines(row, 2); !reflect.DeepEqual(got, want2) {
		t.Fatalf("n=2: %q", got)
	}
	write("no rc here\n٣rc=5 only\nafter rc=12 x\n" + strings.Repeat("tail\n", 25))
	if got := StartLogLines(row, 5); !reflect.DeepEqual(got, []string{"  start.log:", "    last rc=12", "    tail", "    tail", "    tail", "    tail", "    tail"}) {
		t.Fatalf("%q", got)
	}
	write("a rc=3\nb\nc\nd\n")
	if got := StartLogLines(row, 2); !reflect.DeepEqual(got, []string{"  start.log:", "    last rc=3", "    c", "    d"}) {
		t.Fatalf("%q", got)
	}
	if StartLogLines(Trial{Status: "passed", Sandbox: row.Sandbox}, 20) != nil || StartLogLines(Trial{Status: "error"}, 20) != nil {
		t.Fatal("a trial that needs no log shows none")
	}
	// a symlinked start.log is never followed
	if err := os.Remove(filepath.Join(d, "log", "start.log")); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("topsecret rc=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(d, "log", "start.log")); err != nil {
		t.Fatal(err)
	}
	if got := StartLogLines(row, 20); got != nil {
		t.Fatalf("symlink followed: %q", got)
	}
}

// Python: re.fullmatch(r"(?:tr0*)?(\d+)", t) then int(): '3'->3, 'tr0003'->3, 'tr٣'->3, '٣'->3, 'tr'->None, '3\n'->None, NBSP+3->None
func TestUnicodeTrialTarget(t *testing.T) {
	for _, c := range []struct {
		in   string
		want int64
		ok   bool
	}{
		{"3", 3, true}, {"tr0003", 3, true}, {"tr٣", 3, true}, {"٣", 3, true}, {"tr", 0, false}, {"", 0, false},
		{"TR3", 0, false}, {"tr0", 0, true}, {"0", 0, true}, {"3\n", 0, false}, {" 3", 0, false}, {"\x0b3", 0, false}, {"C-1", 0, false},
	} {
		got, ok := ParseTrialTarget(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got (%d,%v) want (%d,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
	for in, want := range map[string]bool{"C-1": true, "C-٣": true, "C-": false, "C-1\n": false, "c-1": false, "C- 1": false} {
		if candTargetRE.MatchString(in) != want {
			t.Errorf("candTargetRE %q want %v", in, want)
		}
	}
	if n, ok := PyInt(" ٣_0\x0b"); !ok || n != 30 {
		t.Errorf("PyInt: %d %v", n, ok)
	}
}

// ---- transcripts (Python: 1 1 0 1 1 0) ----

func TestSkillWasInvokedParity(t *testing.T) {
	d := t.TempDir()
	w := func(name, body string) {
		p := filepath.Join(d, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	w("a.jsonl", `{"message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"plug:myskill"}}]}}`+"\n")
	w("b.jsonl", "not json \"tool_use\"\n"+`{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/p/lug/x/SKILL.md"}}]}}`+"\n")
	w("c.jsonl", `{"message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"/other/foo/SKILL.md"}}]}}`+"\n")
	w("e/e.jsonl", `{"message":{"content":[{"type":"tool_use","name":"Skill","input":{"name":5}}]}}`+"\n")
	set := func(n ...string) map[string]bool {
		m := map[string]bool{}
		for _, x := range n {
			m[x] = true
		}
		return m
	}
	for i, c := range []struct {
		names  map[string]bool
		plugin string
		want   int
	}{
		{set("myskill"), "", 1}, {set("zzz"), "/p/lug", 1}, {set("zzz"), "", 0}, {set("foo"), "", 1}, {set("5"), "", 1}, {set("nothing"), "/p/lugX", 0},
	} {
		got, err := SkillWasInvoked(d, c.names, c.plugin)
		if err != nil || got != c.want {
			t.Errorf("case %d: got %d %v want %d", i, got, err, c.want)
		}
	}
}

func TestControlMarker(t *testing.T) {
	w := t.TempDir()
	put := func(s string) {
		if err := os.RemoveAll(filepath.Join(w, MarkerFile)); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w, MarkerFile), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for s, want := range map[string]int{MarkerText: 1, MarkerText + "\n": 1, MarkerText + "\n\n": 0, " " + MarkerText: 0, "": 0, strings.Repeat("x", 1000): 0} {
		put(s)
		if got := ControlMarker(w); got != want {
			t.Errorf("%q: got %d want %d", s, got, want)
		}
	}
	// a symlink planted by the evaluator is never followed
	if err := os.Remove(filepath.Join(w, MarkerFile)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(target, []byte(MarkerText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(w, MarkerFile)); err != nil {
		t.Fatal(err)
	}
	if ControlMarker(w) != 0 {
		t.Fatal("symlink followed")
	}
}

func TestCleanProjectsDropsNonRegular(t *testing.T) {
	p := t.TempDir()
	if err := os.MkdirAll(filepath.Join(p, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(p, "sub", "ok.jsonl"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", filepath.Join(p, "sub", "link.jsonl")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc", filepath.Join(p, "dirlink")); err != nil {
		t.Fatal(err)
	}
	if err := CleanProjects(p); err != nil {
		t.Fatal(err)
	}
	for _, gone := range []string{"sub/link.jsonl", "dirlink"} {
		if _, err := os.Lstat(filepath.Join(p, gone)); err == nil {
			t.Errorf("%s must be removed", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(p, "sub", "ok.jsonl")); err != nil {
		t.Fatal("regular file must stay")
	}
}

func TestStageCheckerReplacesPlantedSymlinks(t *testing.T) {
	task := t.TempDir()
	if err := os.MkdirAll(filepath.Join(task, "hidden", "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	for p, s := range map[string]string{"check.py": "print(1)", "hidden/sub/t.py": "T"} {
		if err := os.WriteFile(filepath.Join(task, p), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	work := t.TempDir()
	outside := t.TempDir()
	// the evaluator planted work/sub -> outside and work/check.py -> outside/x
	if err := os.Symlink(outside, filepath.Join(work, "sub")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "x"), filepath.Join(work, "check.py")); err != nil {
		t.Fatal(err)
	}
	if err := StageChecker(task, work); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(outside); len(ents) != 0 {
		t.Fatalf("a write landed outside work/: %v", ents)
	}
	if b, _ := os.ReadFile(filepath.Join(work, "sub", "t.py")); string(b) != "T" {
		t.Fatal("hidden file not staged")
	}
	if st, _ := os.Lstat(filepath.Join(work, "check.py")); st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("check.py still a symlink")
	}
	if err := StageChecker(task, filepath.Join(work, "check.py")); err == nil {
		t.Fatal("a non-directory work must be refused")
	}
}

// ---- transitions, cancel, void, queue ----

func TestTransitionIsAtomicAndGuarded(t *testing.T) {
	db := newDB(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "triaged")
	id := addTrial(t, db, "C-0001", "fix-bug", "with", "pending")
	won, err := Claim(db, id, "2026-11-14T10:00:00+00:00")
	if err != nil || !won {
		t.Fatal(won, err)
	}
	if won, err := Claim(db, id, "later"); err != nil || won { // the loser backs off
		t.Fatalf("second claim must lose: %v %v", won, err)
	}
	if tr := trial(t, db, id); tr.Status != "starting" || tr.Started.String != "2026-11-14T10:00:00+00:00" {
		t.Fatalf("%+v", tr)
	}
	if _, err := db.Exec("BEGIN IMMEDIATE"); err != nil { // no transaction was left open
		t.Fatalf("transaction left open: %v", err)
	}
	mustExec(t, db, "ROLLBACK")
}

func TestCancelPendingAndRefusals(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "triaged")
	p := addTrial(t, db, "C-0001", "fix-bug", "with", "pending")
	s := addTrial(t, db, "C-0001", "fix-bug", "without", "starting")
	done := addTrial(t, db, "C-0001", "add-feature", "with", "passed")
	c := Collector{TasksDir: t.TempDir()}
	lines, errs, err := c.Cancel(db, "C-0001", "/nonexistent-root", api)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || !strings.HasPrefix(lines[0], fmt.Sprintf("C-0001 fix-bug with: trial %d status=cancelled passed=0", p)) {
		t.Fatalf("lines %q", lines)
	}
	if len(errs) != 1 || errs[0] != fmt.Sprintf("trial %d: is starting right now; retry the cancel in a minute", s) {
		t.Fatalf("errs %q", errs)
	}
	if trial(t, db, done).Status != "passed" {
		t.Fatal("a terminal trial is left as it is")
	}
	for _, bad := range []string{"nope", "C-9999", "99"} {
		_, _, err := c.Cancel(db, bad, "/x", api)
		var ce *CollectError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: %v", bad, err)
		}
	}
	if _, _, err := c.Cancel(db, "x", "/x", api); err == nil || err.Error() != "usage: factory skilleval cancel <trial id|trNNNN|C-xxxx>" {
		t.Fatalf("%v", err)
	}
	if _, _, err := c.Cancel(db, "99", "/x", api); err == nil || err.Error() != "nothing to cancel: no trial or candidate 99" {
		t.Fatalf("%v", err)
	}
}

func TestVoid(t *testing.T) {
	db := newDB(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "triaged")
	f := addTrial(t, db, "C-0001", "fix-bug", "with", "failed")
	p := addTrial(t, db, "C-0001", "fix-bug", "without", "pending")
	v, err := Void(db, fmt.Sprintf("tr%04d", f), "infra", "docker died")
	if err != nil || v.Prev != "failed" || v.Status != "voided" || v.VoidWhy.String != "docker died" {
		t.Fatalf("%+v %v", v, err)
	}
	for _, c := range []struct {
		target, reason, want string
	}{
		{"zz", "infra", "usage: factory skilleval void <trial id|trNNNN> --reason infra --why <text>"},
		{fmt.Sprint(f), "other", "void needs --reason infra"},
		{"99", "infra", "no trial 99"},
		{fmt.Sprint(p), "infra", fmt.Sprintf("trial %d is pending: only a finished trial (passed, failed, timeout, over-budget, error) can be voided; use `factory skilleval cancel`", p)},
		{fmt.Sprint(f), "infra", fmt.Sprintf("trial %d is voided: only a finished trial (passed, failed, timeout, over-budget, error) can be voided; nothing to do", f)},
	} {
		if _, err := Void(db, c.target, c.reason, "x"); err == nil || err.Error() != c.want {
			t.Errorf("%s/%s: %v want %q", c.target, c.reason, err, c.want)
		}
	}
}

func TestQueueTrialsIsIdempotent(t *testing.T) {
	db := newDB(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "triaged")
	added, rows, err := QueueTrials(db, "C-0001", "sha1", []string{"fix-bug", "add-feature"}, "lead", "2026-11-14T10:00:00+00:00")
	if err != nil || len(added) != 4 || len(rows) != 4 {
		t.Fatalf("%v %v %v", added, rows, err)
	}
	if rows[0].Task != "fix-bug" || rows[0].Condition != "with" || rows[1].Condition != "without" || rows[2].Task != "add-feature" {
		t.Fatalf("a task's two conditions go back to back: %+v", rows)
	}
	var st string
	if err := db.QueryRow("SELECT status FROM scout_candidates WHERE id='C-0001'").Scan(&st); err != nil || st != "queued-eval" {
		t.Fatal(st, err)
	}
	added, rows, err = QueueTrials(db, "C-0001", "sha1", []string{"fix-bug"}, "lead", "later")
	if err != nil || len(added) != 0 || len(rows) != 4 {
		t.Fatalf("a repeat adds nothing: %v %v %v", added, rows, err)
	}
}

func TestTaskNames(t *testing.T) {
	d := t.TempDir()
	for _, n := range []string{"b", "a", "nometa"} {
		if err := os.MkdirAll(filepath.Join(d, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, n := range []string{"b", "a"} {
		if err := os.WriteFile(filepath.Join(d, n, "meta.json"), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := TaskNames(d); err != nil || !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := TaskNames(filepath.Join(d, "missing")); err == nil {
		t.Fatal("a missing tasks dir must be an error")
	}
}

// ---- tick ----

func TestTickBlockedSaysItOncePerReason(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	c := Collector{TasksDir: t.TempDir()}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	addTrial(t, db, "C-0001", "fix-bug", "with", "pending")
	for i := 0; i < 3; i++ { // no eval token under api.home: a global blocker, the queue stays untouched
		if err := c.Tick(db, api); err != nil {
			t.Fatal(err)
		}
	}
	if len(api.launched) != 0 {
		t.Fatal("nothing may start without the token")
	}
	if len(api.events) != 1 || !strings.HasPrefix(api.events[0], "skilleval-blocked:-:owner decision D3 pending: no eval token (") {
		t.Fatalf("events %q", api.events)
	}
	if tr := trial(t, db, 1); tr.Status != "pending" {
		t.Fatalf("%+v", tr)
	}
}

func armToken(t *testing.T, home string) {
	t.Helper()
	d := filepath.Join(home, ".config", "factory")
	if err := os.MkdirAll(d, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "eval-oauth-token"), []byte("tok"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func needUserns(t *testing.T) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable here: %s", why)
	}
}

func TestTickStartsOneUnderCapsAndFailsCleanly(t *testing.T) {
	needUserns(t)
	db := newDB(t)
	api := newAPI(t)
	armToken(t, api.home)
	c := Collector{TasksDir: t.TempDir()}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	a := addTrial(t, db, "C-0001", "fix-bug", "with", "pending")
	b := addTrial(t, db, "C-0001", "fix-bug", "without", "pending")
	if err := c.Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(api.launched, []int64{a}) {
		t.Fatalf("one trial per tick, lowest id first: %v", api.launched)
	}
	if tr := trial(t, db, a); tr.Status != "running" || tr.Started.String != "2026-11-14T10:00:00+00:00" {
		t.Fatalf("%+v", tr)
	}
	if err := c.Tick(db, api); err != nil || len(api.launched) != 1 { // skilleval_max_concurrent=1
		t.Fatalf("concurrency cap: %v %v", api.launched, err)
	}
	// the daily cap counts starts of today
	mustExec(t, db, "UPDATE skill_trials SET status='passed', ended='x' WHERE id=?", a)
	api.cfg["skilleval_max_trials_per_day"] = "1"
	if err := c.Tick(db, api); err != nil || len(api.launched) != 1 {
		t.Fatalf("daily cap: %v %v", api.launched, err)
	}
	api.cfg["skilleval_max_trials_per_day"] = "5"
	api.launchErr = fmt.Errorf("%w: preflight said no", sandbox.ErrSandboxRefused)
	if err := c.Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if tr := trial(t, db, b); tr.Status != "error" || tr.CheckOutput.String != "preflight said no" {
		t.Fatalf("%+v", tr)
	}
	if last := api.events[len(api.events)-1]; last != fmt.Sprintf("start-refused:C-0001:trial %d: preflight said no", b) {
		t.Fatalf("events %q", api.events)
	}
}

func TestTickReapsStuckStartingAndMarksEvaluated(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	c := Collector{TasksDir: t.TempDir()}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	st := addTrial(t, db, "C-0001", "fix-bug", "with", "starting")
	mustExec(t, db, "UPDATE skill_trials SET started='2026-11-14T09:44:00+00:00' WHERE id=?", st) // 16 min > 15
	if err := c.Tick(db, api); err != nil {
		t.Fatal(err)
	}
	if tr := trial(t, db, st); tr.Status != "error" || tr.CheckOutput.String != "stuck in starting for over 15 min; its process died" {
		t.Fatalf("%+v", tr)
	}
	var cs string
	if err := db.QueryRow("SELECT status FROM scout_candidates WHERE id='C-0001'").Scan(&cs); err != nil || cs != "evaluated" {
		t.Fatalf("every trial is terminal, so the candidate is evaluated: %s %v", cs, err)
	}
}

func TestTickCleansOldSandboxes(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	c := Collector{TasksDir: t.TempDir()}
	root := api.cfg["skilleval_root"]
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "evaluated")
	old := addTrial(t, db, "C-0001", "fix-bug", "with", "error")
	fresh := addTrial(t, db, "C-0001", "fix-bug", "without", "timeout")
	mustExec(t, db, "UPDATE skill_trials SET ended='2026-11-12T09:00:00+00:00' WHERE id=?", old)   // 49 h
	mustExec(t, db, "UPDATE skill_trials SET ended='2026-11-14T09:00:00+00:00' WHERE id=?", fresh) // 1 h
	for _, name := range []string{sandbox.TrialName(int(old)), sandbox.TrialName(int(fresh)), "tr0099", "keepme"} {
		if err := os.MkdirAll(filepath.Join(root, "trials", name, "work"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Tick(db, api); err != nil {
		t.Fatal(err)
	}
	exists := func(n string) bool { _, err := os.Stat(filepath.Join(root, "trials", n)); return err == nil }
	if exists("tr0001") || exists("tr0099") || !exists("tr0002") || !exists("keepme") {
		t.Fatalf("old error and the unknown tr0099 go; the fresh one and a foreign dir stay: tr0001=%v tr0002=%v tr0099=%v keepme=%v",
			exists("tr0001"), exists("tr0002"), exists("tr0099"), exists("keepme"))
	}
}

// ---- the checker and collect ----

func mkTask(t *testing.T, check string) string {
	t.Helper()
	tasks := t.TempDir()
	d := filepath.Join(tasks, "t1")
	if err := os.MkdirAll(filepath.Join(d, "hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "check.py"), []byte(check), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "hidden", "h.txt"), []byte("hidden"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "meta.json"), []byte(`{"id":"t1","role":"coder"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return tasks
}

func mkTrialDir(t *testing.T, root string, id int64) string {
	t.Helper()
	d := filepath.Join(root, "trials", sandbox.TrialName(int(id)))
	for _, s := range []string{"work", "home", "fh", "cfg"} {
		if err := os.MkdirAll(filepath.Join(d, s), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// TestSkillevalRequiresSandbox: the evaluator and its checker run only inside the sandbox. Without user namespaces a
// try is refused with Python's text and nothing is created, started or executed; with them the evaluator goes through
// the go/internal/sandbox view.
func TestSkillevalRequiresSandbox(t *testing.T) {
	tasks := mkTask(t, "open('/tmp/should-never-exist-'+str(__import__('os').getpid()),'w')\n")
	db := newDB(t)
	api := newAPI(t)
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "triaged")
	var built, spawned int
	env := &Env{Collector: Collector{TasksDir: tasks}, Actor: "lead-1", Home: "/nonexistent-factory-home", PasswdHome: api.home,
		UnshareBin: "/nonexistent/unshare", Out: &strings.Builder{}, Api: api, Builder: &fakeBuilder{built: &built},
		Spawn: func(*sql.DB, string, string, string, [][2]string, []string) error { spawned++; return nil }}
	// 1. no eval token: refused with the sandbox text, exit 3, no trial row
	err := env.Try(db, TryOpts{Task: "t1", Candidate: "none", HasCandidate: true})
	var ee *ExitError
	if !errors.As(err, &ee) || ee.Code != 3 || !strings.HasPrefix(ee.Msg, "owner decision D3 pending: no eval token ("+api.home+"/.config/factory/eval-oauth-token: must exist, be a regular file, mode 0600, owned by uid ") {
		t.Fatalf("no token: %v", err)
	}
	// 2. a token but no user namespaces: Python's text, exit 3, nothing built or spawned, no row
	armToken(t, api.home)
	err = env.Try(db, TryOpts{Task: "t1", Candidate: "none", HasCandidate: true})
	if !errors.As(err, &ee) || ee.Code != 3 || !strings.HasPrefix(ee.Msg, "user namespaces unavailable, no unsandboxed fallback: cannot run /nonexistent/unshare: ") {
		t.Fatalf("no userns: %v", err)
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM skill_trials").Scan(&n); err != nil || n != 0 || built != 0 || spawned != 0 {
		t.Fatalf("rows=%d built=%d spawned=%d %v", n, built, spawned, err)
	}
	// 3. the checker has the same rule: no namespaces, no run, status error, check.py never executes
	root := t.TempDir()
	d := mkTrialDir(t, root, 1)
	status, passed, out := RunChecker(d, "t1", tasks, api.home, "/nonexistent/unshare")
	if status != "error" || passed.Valid || !strings.HasPrefix(out, "checker not run: user namespaces unavailable, no uncontained fallback: cannot run /nonexistent/unshare: ") {
		t.Fatalf("checker: %s %v %q", status, passed, out)
	}
	if _, err := os.Stat(filepath.Join(d, "work", "check.py")); err == nil {
		t.Fatal("check.py must not even be staged when there is no sandbox")
	}
	// 4. with a sandbox the trial starts through the builder, the preflight view is go/internal/sandbox's argv
	needUserns(t)
	env.UnshareBin = "unshare"
	fb := &fakeBuilder{built: &built, trialsRoot: t.TempDir()}
	env.Builder = fb
	env.Api.(*fakeAPI).cfg["skilleval_root"] = fb.trialsRoot
	if err := env.Try(db, TryOpts{Task: "t1", Candidate: "none", HasCandidate: true}); err != nil {
		t.Fatal(err)
	}
	if built != 1 || spawned != 1 || fb.preflightArgv[0] == "" {
		t.Fatalf("built=%d spawned=%d argv=%v", built, spawned, fb.preflightArgv)
	}
	want, err := sandbox.PreflightArgv(fb.info.Ns)
	if err != nil || !reflect.DeepEqual(fb.preflightArgv, want) {
		t.Fatalf("preflight argv %v want %v (%v)", fb.preflightArgv, want, err)
	}
	if fb.preflightArgv[len(fb.preflightArgv)-1] != "--version" || !strings.Contains(strings.Join(fb.preflightArgv, " "), "--setup") {
		t.Fatalf("not the sandbox stage-1 view: %v", fb.preflightArgv)
	}
	tr := trial(t, db, 1)
	if tr.Status != "running" || tr.Agent.String != "eval-tr0001" || tr.CandidateID != BaselineID || tr.Condition != "without" {
		t.Fatalf("%+v", tr)
	}
	if got := env.Out.(*strings.Builder).String(); !strings.HasPrefix(got, "trial 1: eval-tr0001 (t1, without, C-0000) sandbox ") {
		t.Fatalf("out %q", got)
	}
}

type fakeBuilder struct {
	built         *int
	trialsRoot    string
	info          Info
	preflightArgv []string
}

func (f *fakeBuilder) Build(db *sql.DB, t Trial, root, home, unshare string) (Info, error) {
	*f.built++
	d := filepath.Join(root, "trials", sandbox.TrialName(int(t.ID)))
	for _, s := range []string{"work", "bin", "role", "scout", "home"} {
		if err := os.MkdirAll(filepath.Join(d, s), 0o755); err != nil {
			return Info{}, err
		}
	}
	claude := filepath.Join(d, "bin", "claude")
	if err := os.WriteFile(claude, []byte("#!/bin/sh\n"), 0o755); err != nil {
		return Info{}, err
	}
	ns, err := sandbox.EvalNsCfg(d, home, claude, "", unshare, sandbox.Opts{})
	if err != nil {
		return Info{}, err
	}
	f.info = Info{Dir: d, Work: filepath.Join(d, "work"), Agent: "eval-" + sandbox.TrialName(int(t.ID)), Ns: ns}
	return f.info, nil
}

func (f *fakeBuilder) Preflight(info Info) error {
	argv, err := sandbox.PreflightArgv(info.Ns)
	f.preflightArgv = argv
	return err
}

func TestCheckerRunsContainedAndMapsResults(t *testing.T) {
	needUserns(t)
	home := t.TempDir()
	for _, c := range []struct {
		name, check, status string
		passed              int64
		outHas              string
	}{
		{"pass", "print('ok', open('h.txt').read())\n", "passed", 1, "ok hidden"},
		{"fail", "import sys\nprint('nope'); sys.exit(1)\n", "failed", 0, "nope"},
	} {
		tasks := mkTask(t, c.check)
		root := t.TempDir()
		d := mkTrialDir(t, root, 1)
		status, passed, out := RunChecker(d, "t1", tasks, home, "unshare")
		if status != c.status || !passed.Valid || passed.Int64 != c.passed || !strings.Contains(out, c.outHas) {
			t.Errorf("%s: %s %v %q", c.name, status, passed, out)
		}
	}
}

func TestCheckerTimeoutKillsHangingScript(t *testing.T) {
	needUserns(t)
	old := CheckTimeout
	CheckTimeout = 2 * time.Second
	t.Cleanup(func() { CheckTimeout = old })
	tasks := mkTask(t, "import time\nprint('start', flush=True)\ntime.sleep(300)\n")
	d := mkTrialDir(t, t.TempDir(), 1)
	begin := time.Now()
	status, passed, out := RunChecker(d, "t1", tasks, t.TempDir(), "unshare")
	if time.Since(begin) > 40*time.Second {
		t.Fatalf("the hanging checker was not killed in time: %v", time.Since(begin))
	}
	if status != "failed" || !passed.Valid || passed.Int64 != 0 || !strings.HasPrefix(out, "check.py exceeded 2 s and was killed (timeout)\n") {
		t.Fatalf("%s %v %q", status, passed, out)
	}
}

func seedFH(t *testing.T, d, agent, status, ended string) {
	t.Helper()
	path := filepath.Join(d, "fh", "db", "factory.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	raw := dbtest.Raw(t, path)
	defer raw.Close()
	if _, err := raw.Exec(dbtest.Schema); err != nil {
		t.Fatal(err)
	}
	var e any
	if ended != "" {
		e = ended
	}
	if _, err := raw.Exec("INSERT INTO agents(name, role, status, ended) VALUES(?,?,?,?)", agent, "evaluator", status, e); err != nil {
		t.Fatal(err)
	}
}

func TestCollectPassedTrialRecordsAndRemovesSandbox(t *testing.T) {
	needUserns(t)
	db := newDB(t)
	api := newAPI(t)
	api.home = t.TempDir()
	root := api.cfg["skilleval_root"]
	tasks := mkTask(t, "print('all good')\n")
	c := Collector{TasksDir: tasks}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	id := addTrial(t, db, "C-0001", "t1", "without", "running")
	d := mkTrialDir(t, root, id)
	mustExec(t, db, "UPDATE skill_trials SET agent='eval-tr0001', sandbox=?, started='2026-11-14T09:00:00+00:00' WHERE id=?", d, id)
	mustExec(t, db, "INSERT INTO agents(name, role, status, started) VALUES('eval-tr0001','evaluator','live','2026-11-14T09:00:00+00:00')")
	seedFH(t, d, "eval-tr0001", "done", "2026-11-14T09:30:00+00:00")
	api.onStop = func(db *sql.DB, name string) {
		mustExec(t, db, "UPDATE agents SET status='stopped' WHERE name=?", name)
	}
	got, err := c.CollectTrial(db, id, root, api, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != "passed" || got.Passed.Int64 != 1 || got.WallS.Int64 != 1800 || got.CheckOutput.String != "all good\n" ||
		got.TokensIn.Int64 != 0 || !got.TokensIn.Valid || got.SkillInvoked.Valid {
		t.Fatalf("%+v", got)
	}
	if !reflect.DeepEqual(api.stopCalls, []string{"eval-tr0001:now"}) {
		t.Fatalf("the live evaluator is stopped first: %v", api.stopCalls)
	}
	if _, err := os.Stat(d); err == nil {
		t.Fatal("a passed trial's sandbox is removed")
	}
	if last := api.events[len(api.events)-1]; last != "collect:C-0001:trial 1 passed" {
		t.Fatalf("%q", api.events)
	}
	// collecting a terminal trial again is a no-op
	again, err := c.CollectTrial(db, id, root, api, "")
	if err != nil || again.Status != "passed" || len(api.stopCalls) != 1 {
		t.Fatalf("%+v %v", again, err)
	}
}

func TestCollectRefusalsAndFailedStop(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	root := api.cfg["skilleval_root"]
	c := Collector{TasksDir: t.TempDir()}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	pend := addTrial(t, db, "C-0001", "t1", "with", "pending")
	if _, err := c.CollectTrial(db, pend, root, api, ""); err == nil || err.Error() != fmt.Sprintf("trial %d is pending, not running: nothing to collect", pend) {
		t.Fatalf("%v", err)
	}
	if _, err := c.CollectTrial(db, 99, root, api, ""); err == nil || err.Error() != "no trial 99" {
		t.Fatalf("%v", err)
	}
	run := addTrial(t, db, "C-0001", "t1", "without", "running")
	if _, err := c.CollectTrial(db, run, root, api, "bogus"); err == nil || err.Error() != "reason must be one of timeout, over-budget, cancelled" {
		t.Fatalf("%v", err)
	}
	// a live agent that is not done yet and no --reason: still running
	d := mkTrialDir(t, root, run)
	mustExec(t, db, "UPDATE skill_trials SET agent='eval-tr0003', sandbox=? WHERE id=?", d, run)
	mustExec(t, db, "INSERT INTO agents(name, role, status) VALUES('eval-tr0003','evaluator','live')")
	seedFH(t, d, "eval-tr0003", "live", "")
	_, err := c.CollectTrial(db, run, root, api, "")
	want := fmt.Sprintf("trial %d is still running (agent eval-tr0003 live, not done in its temp DB); pass --reason timeout|over-budget|cancelled to end it", run)
	if err == nil || err.Error() != want {
		t.Fatalf("%v", err)
	}
	// the stop fails: the checker is not run, the trial stays running and says why
	api.stopRes = "failed"
	_, err = c.CollectTrial(db, run, root, api, "timeout")
	wantStop := "force_stop of eval-tr0003 failed: stub note; checker not run, trial left running: retry collect once the agent is stopped"
	if err == nil || err.Error() != wantStop {
		t.Fatalf("%v", err)
	}
	if tr := trial(t, db, run); tr.Status != "running" || tr.CheckOutput.String != wantStop {
		t.Fatalf("%+v", tr)
	}
	if _, err := os.Stat(d); err != nil {
		t.Fatal("the sandbox must stay for the retry")
	}
	// a lost agent (no row): error, nothing to check
	lost := addTrial(t, db, "C-0001", "t2", "with", "running")
	got, err := c.CollectTrial(db, lost, root, api, "")
	if err != nil || got.Status != "error" || got.CheckOutput.String != fmt.Sprintf("sandbox of trial %d is gone; nothing to check", lost) {
		t.Fatalf("%+v %v", got, err)
	}
}

func TestCollectConcurrentIsRefused(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	root := api.cfg["skilleval_root"]
	c := Collector{TasksDir: t.TempDir()}
	addCand(t, db, "C-0001", "gh:a/b", "prompt-only", "queued-eval")
	id := addTrial(t, db, "C-0001", "t1", "without", "running")
	api.onStop = nil
	// hold the per-trial lock like a concurrent collect would
	release := holdLock(t, root, id)
	defer release()
	if _, err := c.CollectTrial(db, id, root, api, "cancelled"); err == nil || err.Error() != fmt.Sprintf("trial %d is being collected by another process; retry shortly", id) {
		t.Fatalf("%v", err)
	}
}

func holdLock(t *testing.T, root string, id int64) func() {
	t.Helper()
	dir := filepath.Join(sandbox.Realpath(root), "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, sandbox.TrialName(int(id))+".lock"), os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := f.Close(); err != nil {
			t.Error(err)
		}
	}
}

// ---- fuzz (G7): everything that parses text the evaluator or the config controls ----

func FuzzParseTrialTarget(f *testing.F) {
	for _, s := range []string{"3", "tr0003", "tr٣", "C-1", "", "tr", " 3", "9999999999999999999999"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ParseTrialTarget(s)
		candTargetRE.MatchString(s)
		PyInt(s)
	})
}

func FuzzStartLogLines(f *testing.F) {
	for _, s := range []string{"first\nrc=7\n", "\x1b[31mx\x00\xff\n", "a rc=3\n٣rc=5\n", strings.Repeat("L", 500), ""} {
		f.Add(s)
	}
	d := f.TempDir()
	if err := os.Mkdir(filepath.Join(d, "log"), 0o700); err != nil {
		f.Fatal(err)
	}
	row := Trial{Status: "error", Sandbox: sql.NullString{String: d, Valid: true}}
	f.Fuzz(func(t *testing.T, s string) {
		if err := os.WriteFile(filepath.Join(d, "log", "start.log"), []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		StartLogLines(row, StartTail)
	})
}

func FuzzInvokedLine(f *testing.F) {
	for _, s := range []string{`{"message":{"content":[{"type":"tool_use","name":"Skill","input":{"skill":"a:b"}}]}}`, `{}`, `[1]`, `{"message":{"content":[null,1,{"type":"tool_use","name":"Read","input":{"file_path":5}}]}}`, `x`} {
		f.Add(s)
	}
	names := map[string]bool{"b": true}
	f.Fuzz(func(t *testing.T, s string) { invokedLine([]byte(s), names, "/p") })
}

func FuzzSplitLines(f *testing.F) {
	for _, s := range []string{"a\nb", "a\r\nb c\x85", "", "\xff\x0b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { splitLines(s) })
}

func mkControl(t *testing.T, body string) string {
	t.Helper()
	d := filepath.Join(t.TempDir(), "marker")
	if err := os.MkdirAll(filepath.Join(d, "skills", "marker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "skills", "marker", "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestRegisterControlInsertsThenRefreshes(t *testing.T) {
	db := newDB(t)
	ctl := mkControl(t, "---\nname: marker\n---\nwrite the marker\n")
	c1, err := RegisterControl(db, "2026-11-14T10:00:00+00:00", ctl)
	if err != nil {
		t.Fatal(err)
	}
	if c1.ID != "C-0001" || c1.Key != scout.ControlKey || c1.Status != "triaged" || c1.InstallRisk.String != "prompt-only" ||
		c1.Licence.String != "repo-owned" || c1.Sources != `["control"]` || len(c1.HeadSha.String) != 40 {
		t.Fatalf("%+v", c1)
	}
	mustExec(t, db, "UPDATE scout_candidates SET status='dismissed' WHERE id='C-0001'")
	if err := os.WriteFile(filepath.Join(ctl, "skills", "marker", "SKILL.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c2, err := RegisterControl(db, "2026-11-14T11:00:00+00:00", ctl)
	if err != nil || c2.ID != "C-0001" || c2.Status != "triaged" || c2.HeadSha.String == c1.HeadSha.String || c2.LastSeen != "2026-11-14T11:00:00+00:00" {
		t.Fatalf("%+v %v", c2, err)
	}
	if ok, _ := IsControl(db, "C-0001"); !ok {
		t.Fatal("the reserved key marks the control")
	}
	var n int
	if err := db.QueryRow("SELECT count(*) FROM scout_candidates").Scan(&n); err != nil || n != 1 {
		t.Fatalf("a refresh must not add a row: %d %v", n, err)
	}
}

func TestControlQueuesBothConditions(t *testing.T) {
	db := newDB(t)
	api := newAPI(t)
	tasks := mkTask(t, "print(1)\n")
	out := &strings.Builder{}
	env := &Env{Collector: Collector{TasksDir: tasks}, ControlDir: mkControl(t, "---\nname: marker\n---\nwrite the marker\n"),
		Actor: "lead-1", Home: "/nonexistent-factory-home", Out: out, Api: api}
	if err := env.Control(db, "t1"); err != nil {
		t.Fatal(err)
	}
	rows, err := QueryTrials(db, "ORDER BY id")
	if err != nil || len(rows) != 2 || rows[0].Condition != "with" || rows[1].Condition != "without" || rows[0].Status != "pending" || rows[0].Author.String != "lead-1" {
		t.Fatalf("%+v %v", rows, err)
	}
	got := out.String()
	if !strings.Contains(got, "C-0001: queued 2 trial(s); the tick starts them under the daily cap\n") || !strings.Contains(got, " vendored: ") ||
		!strings.Contains(got, "C-0001 t1 with: trial 1 status=pending passed=- tokens=- wall_s=- skill_invoked=-\n") {
		t.Fatalf("out %q", got)
	}
	if !strings.Contains(strings.Join(api.events, "|"), "queue:C-0001:2 trial(s) for 1 task(s)") {
		t.Fatalf("events %q", api.events)
	}
	// a second control run reuses the vendored copy and queues nothing new
	out.Reset()
	if err := env.Control(db, "t1"); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "already vendored: ") || !strings.Contains(got, "C-0001: already queued; nothing added\n") {
		t.Fatalf("out %q", got)
	}
	// an unknown task and a missing one are refused with exit 2 and Python's text
	var ee *ExitError
	if err := env.QueueCandidate(db, "C-0001", "nope"); !errors.As(err, &ee) || ee.Code != 2 || ee.Msg != "unknown eval task nope; known: t1" {
		t.Fatalf("%v", err)
	}
	if err := env.QueueCandidate(db, "x", ""); !errors.As(err, &ee) || ee.Msg != "usage: factory skilleval queue C-xxxx [--tasks all|task1,task2]" {
		t.Fatalf("%v", err)
	}
	if err := env.QueueCandidate(db, "C-0042", "t1"); !errors.As(err, &ee) || ee.Msg != "no candidate C-0042" {
		t.Fatalf("%v", err)
	}
	mustExec(t, db, "UPDATE scout_candidates SET status='needs-owner' WHERE id='C-0001'")
	if err := env.QueueCandidate(db, "C-0001", "t1"); !errors.As(err, &ee) || ee.Code != 2 ||
		ee.Msg != "C-0001 is not eligible for evaluation (install_risk 'prompt-only', status 'needs-owner'); only prompt-only candidates that are not needs-owner or dismissed are queued" {
		t.Fatalf("%v", err)
	}
}
