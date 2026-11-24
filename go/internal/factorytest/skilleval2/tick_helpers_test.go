package skilleval2

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/sandbox"
	"factory/go/internal/scout"
	"factory/go/internal/skilleval"

	_ "modernc.org/sqlite"
)

// Shared pieces of the T-0112 tick/queue/cancel/void port (tick_test.go, tick_cli_test.go).

const (
	tickDay     = "2026-10-25" // the day of the make_trial fixtures (started 10:00)
	tickNextDay = "2026-10-26"
	tickSHA     = "cccccccccccccccccccccccccccccccccccccccc"
	tickStart   = "2026-10-25T10:00:00+00:00"
	tickDone    = "2026-10-25T10:05:00+00:00"
)

var tickKeys = [][2]string{{"skilleval_max_trials_per_day", "6"}, {"skilleval_max_concurrent", "1"}, {"skilleval_trial_minutes", "15"},
	{"skilleval_trial_max_tokens", "300000"}, {"skilleval_keep_failed_h", "48"}}

// at is the Python tickAtDay(): an injected UTC clock (TZ=UTC pins _started_today's local day).
func tickAtDay(hhmm, day string) time.Time {
	t, err := time.Parse("2006-01-02T15:04:05-07:00", day+"T"+hhmm+":00+00:00")
	if err != nil {
		panic(err)
	}
	return t
}

func tickAt(hhmm string) time.Time { return tickAtDay(hhmm, tickDay) }

// tickNeedUserns skips where user namespaces are restricted: the scheduler start path and the contained checker
// (both real in Go, mocked by the Python tests) need them.
func tickNeedUserns(t testing.TB) {
	t.Helper()
	if ok, why := sandbox.UsernsAvailable("unshare"); !ok {
		t.Skipf("user namespaces unavailable here: %s", why)
	}
}

// OSError stands for Python's OSError: its %T shows up in the trial's check_output.
type OSError string

func (e OSError) Error() string { return string(e) }

type tickStop struct{ Name, How, Flag string }

// tickAPI is the skilleval.API of the scheduler tests (Python TickApi + FakeApi): injected clock, a launch that only
// writes rows and dirs, and a force_stop that records the call.
type tickAPI struct {
	when     time.Time
	gate     string
	home     string
	db       *sql.DB
	launched []int64
	events   [][2]string
	stopped  []tickStop
	stopRes  string
	stopNote string
	launchFn func(call int) error // an error to return instead of launching
	onHome   func()               // runs once, the first time the scheduler asks for the home (after it picked a trial)
}

func (a *tickAPI) Now() string      { return scout.Stamp(a.when) }
func (a *tickAPI) Clock() time.Time { return a.when }
func (a *tickAPI) Cfg(db *sql.DB, key string) (string, error) {
	var v string
	if err := db.QueryRow("SELECT value FROM config WHERE key=?", key).Scan(&v); err != nil {
		return "", fmt.Errorf("config %s: %w", key, err)
	}
	return v, nil
}
func (a *tickAPI) Event(_ *sql.DB, _, _, action, detail string) error {
	a.events = append(a.events, [2]string{action, detail})
	return nil
}
func (a *tickAPI) ForceStop(db *sql.DB, name, how string, _ sql.NullString) (string, string, error) {
	var flag sql.NullString
	if err := db.QueryRow("SELECT stop_requested_at FROM agents WHERE name=?", name).Scan(&flag); err != nil {
		return "", "", fmt.Errorf("read stop_requested_at of %s: %w", name, err)
	}
	a.stopped = append(a.stopped, tickStop{name, how, flag.String})
	if a.stopRes == "" || a.stopRes == "stopped" {
		if _, err := db.Exec("UPDATE agents SET status='stopped' WHERE name=?", name); err != nil {
			return "", "", fmt.Errorf("stop %s: %w", name, err)
		}
		return "stopped", "", nil
	}
	return a.stopRes, a.stopNote, nil
}
func (a *tickAPI) Launch(db *sql.DB, row skilleval.Trial, root, _, _ string) error {
	a.launched = append(a.launched, row.ID)
	if a.launchFn != nil {
		if err := a.launchFn(len(a.launched)); err != nil {
			return err
		}
	}
	name := sandbox.TrialName(int(row.ID))
	d := filepath.Join(root, "trials", name)
	for _, sub := range []string{"fh", "work", "home", "cfg/projects/p"} {
		if err := os.MkdirAll(filepath.Join(d, sub), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", sub, err)
		}
	}
	agent := "eval-" + name
	if _, err := db.Exec("INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", agent, "evaluator", a.gate, a.Now()); err != nil {
		return fmt.Errorf("insert agent: %w", err)
	}
	_, err := db.Exec("UPDATE skill_trials SET status='running', agent=?, sandbox=?, started=? WHERE id=? AND status='starting'",
		agent, d, a.Now(), row.ID)
	if err != nil {
		return fmt.Errorf("mark trial running: %w", err)
	}
	return nil
}
func (a *tickAPI) Home() string {
	if a.onHome != nil {
		f := a.onHome
		a.onHome = nil
		f()
	}
	return a.home
}
func (a *tickAPI) HomeDir() string { return "/nonexistent-home" }
func (a *tickAPI) Unshare() string { return "unshare" }

// tickW is a TickBase: a temp factory home with the five skilleval keys, candidate C-0001 vendored under evalroot, a
// token file (so the start path is open) and the real eval tasks for the checker.
type tickW struct {
	t        *testing.T
	e        *ft.Env
	db       *sql.DB
	root     string
	home     string // the eval token lives under it
	c        skilleval.Collector
	stopRes  string
	stopNote string
	launchFn func(call int) error
}

func tickOpen(t testing.TB, path string) *sql.DB {
	t.Helper()
	d, err := factorydb.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { d.Close() })
	return d.DB
}

func tickExec(t testing.TB, db *sql.DB, q string, args ...any) sql.Result {
	t.Helper()
	r, err := db.Exec(q, args...)
	if err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return r
}

func tickVendor(t testing.TB, root string) {
	t.Helper()
	plugin := filepath.Join(root, "vendor", "C-0001@"+tickSHA)
	if err := os.MkdirAll(filepath.Join(plugin, "plugin", "skills", "demo-skill"), 0o755); err != nil {
		t.Fatal(err)
	}
	tickMust(t, os.WriteFile(filepath.Join(plugin, "plugin", "skills", "demo-skill", "SKILL.md"), []byte("---\nname: demo-skill\n---\nProse only.\n"), 0o644))
	vj, err := json.Marshal(map[string]any{"candidate": "C-0001", "sha": tickSHA, "plugin": "plugin",
		"copied": []string{"skills/demo-skill/SKILL.md"}, "skipped": []string{}, "prescreen_hits": []string{}})
	tickMust(t, err)
	tickMust(t, os.WriteFile(filepath.Join(plugin, "VENDOR.json"), vj, 0o644))
}

func tickMust(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func newTickW(t *testing.T) *tickW {
	t.Helper()
	e := ft.New(t)
	w := &tickW{t: t, e: e, db: tickOpen(t, e.DBPath), root: filepath.Join(e.Home, "evalroot"), home: t.TempDir()}
	w.c = skilleval.Collector{TasksDir: tasksDir(t)}
	for _, kv := range tickKeys {
		tickExec(t, w.db, "INSERT INTO config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", kv[0], kv[1])
	}
	tickExec(t, w.db, "INSERT INTO config(key,value) VALUES('skilleval_root',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", w.root)
	tickExec(t, w.db, "INSERT INTO scout_candidates(id,key,repo,head_sha,install_risk,status,sources,first_seen,last_seen) "+
		"VALUES('C-0001','gh:o/r','o/r',?,'prompt-only','new','[]','t','t')", tickSHA)
	tickVendor(t, w.root)
	w.armToken()
	return w
}

func (w *tickW) tokenPath() string {
	return filepath.Join(w.home, ".config", "factory", "eval-oauth-token")
}

func (w *tickW) armToken() {
	tickMust(w.t, os.MkdirAll(filepath.Dir(w.tokenPath()), 0o700))
	tickMust(w.t, os.WriteFile(w.tokenPath(), []byte("dummy-token"), 0o600))
}

// noToken is the check_token refusal the blocked-start tests mock.
func (w *tickW) noToken() { tickMust(w.t, os.Remove(w.tokenPath())) }

func (w *tickW) api(when time.Time, gate string) *tickAPI {
	if gate == "" {
		gate = "live"
	}
	return &tickAPI{when: when, gate: gate, home: w.home, db: w.db, stopRes: w.stopRes, stopNote: w.stopNote, launchFn: w.launchFn}
}

func (w *tickW) tick(when time.Time) *tickAPI { return w.tickGate(when, "live") }

func (w *tickW) tickGate(when time.Time, gate string) *tickAPI {
	w.t.Helper()
	a := w.api(when, gate)
	if err := w.c.Tick(w.db, a); err != nil {
		w.t.Fatalf("tick %s: %v", when, err)
	}
	return a
}

func (w *tickW) queue(tasks ...string) []int64 {
	w.t.Helper()
	if len(tasks) == 0 {
		tasks = []string{"fix-bug", "add-feature", "review-diff"}
	}
	ids, _, err := skilleval.QueueTrials(w.db, "C-0001", tickSHA, tasks, "zz", "t")
	tickMust(w.t, err)
	return ids
}

func (w *tickW) status() []string {
	w.t.Helper()
	rows, err := w.db.Query("SELECT status FROM skill_trials ORDER BY id")
	tickMust(w.t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		tickMust(w.t, rows.Scan(&s))
		out = append(out, s)
	}
	tickMust(w.t, rows.Err())
	return out
}

func (w *tickW) candStatus() string {
	var s string
	tickMust(w.t, w.db.QueryRow("SELECT status FROM scout_candidates WHERE id='C-0001'").Scan(&s))
	return s
}

func (w *tickW) firstID() int64 {
	var id int64
	tickMust(w.t, w.db.QueryRow("SELECT min(id) FROM skill_trials").Scan(&id))
	return id
}

func (w *tickW) trial(id int64) skilleval.Trial {
	w.t.Helper()
	r, ok, err := skilleval.GetTrial(w.db, id)
	if err != nil || !ok {
		w.t.Fatalf("trial %d: %v %v", id, ok, err)
	}
	return r
}

func (w *tickW) agentStatus(name string) string {
	var s string
	tickMust(w.t, w.db.QueryRow("SELECT status FROM agents WHERE name=?", name).Scan(&s))
	return s
}

func tickCount(ss []string, want string) int {
	n := 0
	for _, s := range ss {
		if s == want {
			n++
		}
	}
	return n
}

func (w *tickW) bigLog(d string, in, out, cacheWrite, cacheRead int) {
	line := map[string]any{"type": "assistant", "timestamp": "2026-10-26T10:00:00Z", "requestId": "rb", "uuid": "rb",
		"message": map[string]any{"id": "mb", "model": "claude-sonnet-5-5", "usage": map[string]any{
			"input_tokens": in, "output_tokens": out, "cache_creation_input_tokens": cacheWrite, "cache_read_input_tokens": cacheRead}}}
	b, err := json.Marshal(line)
	tickMust(w.t, err)
	tickMust(w.t, os.WriteFile(filepath.Join(d, "cfg", "projects", "p", "big.jsonl"), append(b, '\n'), 0o644))
}

type tickMk struct {
	cond, task string
	unsolved   bool
	transcript string // "" = transcript-skill.jsonl, "-" = none
	notDone    bool
	live       string // "" = live, "-" = no agent row
}

// makeTrial is CollectBase.make_trial: a running trial with a sandbox dir (work/ with the solution applied or not,
// a temp DB, a transcript) and an agent row.
func (w *tickW) makeTrial(o tickMk) (int64, string, string) {
	w.t.Helper()
	if o.cond == "" {
		o.cond = "with"
	}
	if o.task == "" {
		o.task = "fix-bug"
	}
	if o.live == "" {
		o.live = "live"
	}
	if o.transcript == "" {
		o.transcript = "transcript-skill.jsonl"
	}
	res := tickExec(w.t, w.db, "INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,queued,started) VALUES('C-0001',?,?,?,'running','t',?)",
		tickSHA, o.task, o.cond, tickStart)
	tid, err := res.LastInsertId()
	tickMust(w.t, err)
	name := sandbox.TrialName(int(tid))
	d := filepath.Join(w.root, "trials", name)
	agent := "eval-" + name
	for _, sub := range []string{"fh", "home", "cfg/projects/p", "bin"} {
		tickMust(w.t, os.MkdirAll(filepath.Join(d, sub), 0o755))
	}
	tasksOverlay(w.t, filepath.Join(tasksDir(w.t), o.task, "repo"), filepath.Join(d, "work"))
	if !o.unsolved {
		ents, err := os.ReadDir(filepath.Join(tasksDir(w.t), o.task, "solution"))
		tickMust(w.t, err)
		for _, f := range ents {
			b, err := os.ReadFile(filepath.Join(tasksDir(w.t), o.task, "solution", f.Name()))
			tickMust(w.t, err)
			tickMust(w.t, os.WriteFile(filepath.Join(d, "work", f.Name()), b, 0o644))
		}
	}
	if o.transcript != "-" {
		root, err := ft.RepoRoot()
		tickMust(w.t, err)
		b, err := os.ReadFile(filepath.Join(root, "tests", "fixtures", "skilleval", o.transcript))
		tickMust(w.t, err)
		tickMust(w.t, os.WriteFile(filepath.Join(d, "cfg", "projects", "p", "s.jsonl"), b, 0o644))
	}
	fhdb := skilleval.FhDB(filepath.Join(d, "fh"))
	tickMust(w.t, os.MkdirAll(filepath.Dir(fhdb), 0o755))
	tmp, err := sql.Open("sqlite", fhdb)
	tickMust(w.t, err)
	tickExec(w.t, tmp, "CREATE TABLE agents(name TEXT PRIMARY KEY, status TEXT, ended TEXT)")
	if o.notDone {
		tickExec(w.t, tmp, "INSERT INTO agents VALUES(?,?,?)", agent, "live", nil)
	} else {
		tickExec(w.t, tmp, "INSERT INTO agents VALUES(?,?,?)", agent, "done", tickDone)
	}
	tickMust(w.t, tmp.Close())
	if o.live != "-" {
		tickExec(w.t, w.db, "INSERT INTO agents(name,role,status,started) VALUES(?,?,?,?)", agent, "evaluator", o.live, tickStart)
	}
	tickExec(w.t, w.db, "UPDATE skill_trials SET agent=?, sandbox=? WHERE id=?", agent, d, tid)
	return tid, d, agent
}

func tickExists(p string) bool { _, err := os.Stat(p); return err == nil }

// other opens a second connection to the same factory DB (Python: self.env.db()).
func (w *tickW) other() *sql.DB { return tickOpen(w.t, w.e.DBPath) }

// ---- CLI side ----

func tickRows(t testing.TB, db *sql.DB, q string, args ...any) []map[string]string {
	t.Helper()
	rows, err := db.Query(q, args...)
	tickMust(t, err)
	defer rows.Close()
	cols, err := rows.Columns()
	tickMust(t, err)
	var out []map[string]string
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		tickMust(t, rows.Scan(ptrs...))
		m := map[string]string{}
		for i, c := range cols {
			if vals[i] == nil {
				m[c] = "<nil>"
			} else {
				m[c] = fmt.Sprint(vals[i])
			}
		}
		out = append(out, m)
	}
	tickMust(t, rows.Err())
	return out
}

func tickLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func tickContains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func tickEqual(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}
