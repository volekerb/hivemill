package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
	"factory/go/internal/identity"
	_ "modernc.org/sqlite"
)

// P4c tables the factorydb test schema does not carry: a copy of scout.db.SCOUT_DDL, bin/factory_cli.py RETRO_DDL and
// REJECTION_INCIDENTS_DDL (tasks is the two columns the retro code reads).
const p4cDDL = `
CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY, pr_url TEXT);
CREATE TABLE IF NOT EXISTS rejection_incidents (id INTEGER PRIMARY KEY AUTOINCREMENT, cause TEXT NOT NULL, kind TEXT NOT NULL,
  task_id TEXT NOT NULL, verdict_id INTEGER, event_id INTEGER, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS retro_runs (id INTEGER PRIMARY KEY, day TEXT NOT NULL UNIQUE, status TEXT NOT NULL, agent TEXT,
  stop_reason TEXT, started TEXT, ended TEXT);
CREATE TABLE IF NOT EXISTS retro_reports (id INTEGER PRIMARY KEY, day TEXT NOT NULL UNIQUE, causes TEXT NOT NULL,
  evidence TEXT NOT NULL, fixes TEXT NOT NULL, author TEXT NOT NULL, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS adopted_fixes (
  id TEXT PRIMARY KEY,            -- F-001, F-002, ... (zero-padded 3, next = max+1)
  report_day TEXT NOT NULL,       -- retro_reports.day the fix came from
  fix_index INTEGER NOT NULL,     -- 1-based index into that report's fixes
  cause TEXT NOT NULL,            -- copied from the report fix (in REJECTION_CAUSES)
  type TEXT NOT NULL,             -- check | template | prompt_rule (copied)
  text TEXT NOT NULL,             -- fix text (copied)
  signature TEXT NOT NULL,        -- '|'-separated literal terms
  task_id TEXT NOT NULL,          -- the task that implements the fix
  pr_url TEXT,                    -- from tasks.pr_url at adopt / at merge sync
  status TEXT NOT NULL,           -- pending | adopted | held | reopened | noise
  adopted_at TEXT,                -- ts of the task's merged event; NULL while pending
  checked_at TEXT, check_note TEXT, recur_incidents TEXT,  -- written by T-C, NULL here
  author TEXT NOT NULL, created TEXT NOT NULL,
  UNIQUE(report_day, fix_index), UNIQUE(task_id, report_day, fix_index));
CREATE TABLE IF NOT EXISTS scout_candidates (id TEXT PRIMARY KEY, key TEXT NOT NULL UNIQUE, kind TEXT, repo TEXT, path TEXT, url TEXT,
  licence TEXT, last_commit TEXT, default_branch TEXT, head_sha TEXT, stars INTEGER, stars_per_day REAL, install_risk TEXT,
  risk_detail TEXT, sources TEXT NOT NULL DEFAULT '[]', first_seen TEXT NOT NULL, last_seen TEXT NOT NULL,
  seen_count INTEGER NOT NULL DEFAULT 1, status TEXT NOT NULL DEFAULT 'new');
CREATE TABLE IF NOT EXISTS scout_triage (id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL, purpose TEXT NOT NULL,
  target_roles TEXT NOT NULL, overlap TEXT NOT NULL, recommendation TEXT NOT NULL, rationale TEXT NOT NULL,
  eval_suggested INTEGER NOT NULL DEFAULT 0, run_id INTEGER, author TEXT, created TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS scout_stars (candidate_id TEXT NOT NULL, ts TEXT NOT NULL, stars INTEGER NOT NULL, PRIMARY KEY(candidate_id, ts));
CREATE TABLE IF NOT EXISTS scout_runs (id INTEGER PRIMARY KEY AUTOINCREMENT, slot TEXT NOT NULL UNIQUE, agent TEXT, status TEXT NOT NULL,
  started TEXT, ended TEXT, tokens INTEGER, new_candidates INTEGER, triaged INTEGER, stop_reason TEXT, reported_msg INTEGER);
CREATE TABLE IF NOT EXISTS skill_trials (id INTEGER PRIMARY KEY AUTOINCREMENT, candidate_id TEXT NOT NULL, vendor_sha TEXT NOT NULL,
  task TEXT NOT NULL, condition TEXT NOT NULL, status TEXT NOT NULL DEFAULT 'pending', agent TEXT, sandbox TEXT, queued TEXT NOT NULL,
  started TEXT, ended TEXT, passed INTEGER, wall_s INTEGER, tokens_in INTEGER, tokens_out INTEGER, cache_read INTEGER, cache_write INTEGER,
  skill_invoked INTEGER, check_output TEXT, author TEXT, void_reason TEXT, void_why TEXT,
  UNIQUE(candidate_id, vendor_sha, task, condition));
`

// p4cRig is one command run against a temp factory home with the P4c tables.
type p4cRig struct {
	t     *testing.T
	home  string
	repo  string
	env   map[string]string
	dbp   string
	execs int
}

func newP4cRig(t *testing.T, agent, role string) *p4cRig {
	t.Helper()
	root := t.TempDir()
	r := &p4cRig{t: t, home: filepath.Join(root, "fhome"), repo: filepath.Join(root, "checkout"),
		env: map[string]string{"FACTORY_AGENT": agent, "FACTORY_PROC_DIR": filepath.Join(root, "proc")}}
	for _, d := range []string{filepath.Join(r.repo, "bin"), filepath.Join(r.repo, "skilleval", "tasks"), r.env["FACTORY_PROC_DIR"]} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	r.env["FACTORY_HOME"], r.env["HOME"] = r.home, filepath.Join(root, "pw")
	r.dbp = dbtest.New(t, r.home, factorydb.SchemaVersion)
	r.sql(p4cDDL)
	if role != "" {
		r.sql("INSERT INTO agents(name, role, status) VALUES(?,?,'live')", agent, role)
	}
	return r
}

func (r *p4cRig) sql(q string, a ...any) {
	r.t.Helper()
	d := dbtest.Raw(r.t, r.dbp)
	defer d.Close()
	if _, err := d.Exec(q, a...); err != nil {
		r.t.Fatal(err)
	}
}

func (r *p4cRig) count(q string, a ...any) int {
	r.t.Helper()
	d := dbtest.Raw(r.t, r.dbp)
	defer d.Close()
	var n int
	if err := d.QueryRow(q, a...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

func (r *p4cRig) run(stdin string, argv ...string) (code int, out, errs string) {
	r.t.Helper()
	var ob, eb bytes.Buffer
	r.execs = 0
	pid := func() int { return 1 }
	var environ []string
	for k, v := range r.env {
		environ = append(environ, k+"="+v)
	}
	code = Run(Deps{Args: argv, Argv0: filepath.Join(r.repo, "bin", "factory"), Environ: environ, Stdout: &ob, Stderr: &eb,
		Stdin: strings.NewReader(stdin), Getpid: pid, Getppid: pid,
		Exec:     func(string, []string, []string) error { r.execs++; return nil },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Env: identity.Env{Getenv: func(k string) string { return r.env[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := r.env[k]; return v, ok },
			Uid:       os.Getuid(), PwDir: func(int) (string, error) { return r.env["HOME"], nil },
			Now: func() time.Time { return time.Date(2026, 11, 14, 12, 0, 5, 0, time.UTC) }}})
	return code, ob.String(), eb.String()
}

// TestRegistryP4cWords: the four P4c words are registered (unreachable until P4-L adds them to cli_go_cmds).
func TestRegistryP4cWords(t *testing.T) {
	want := map[string][]string{
		"scout":        {"list", "show", "runs"},
		"skilleval":    {"vendor", "queue", "control", "void", "list", "results"},
		"retro":        {"packet", "file", "show", "adopt", "fixes"},
		"cursor-pilot": {"status"},
	}
	for word, actions := range want {
		for _, a := range actions {
			if _, ok := registry[key{word, a}]; !ok {
				t.Errorf("%s %s is not registered", word, a)
			}
		}
		t.Logf("registered: %s %s", word, strings.Join(actions, " "))
	}
	// the argv shapes Python must keep answering are not claimed: they parse to "not ours"
	for _, argv := range [][]string{{"skilleval", "try", "fix-bug"}, {"skilleval", "collect", "3"}, {"skilleval", "cancel", "3"},
		{"scout", "fetch"}, {"scout", "pending"}, {"scout", "triage", "C-1"}, {"scout", "report"},
		{"cursor-pilot", "prepare"}, {"cursor-pilot", "probe"}, {"cursor-pilot", "run"}, {"retro", "file", "x"},
		{"skilleval", "list", "--rescreen"}, {"skilleval", "void", "1", "--reason", "bogus"}} {
		if _, _, ok := lookup(argv); ok {
			t.Errorf("%v must stay with Python", argv)
		}
	}
}

func TestSkillevalCLI(t *testing.T) {
	r := newP4cRig(t, "director-1", "director")
	r.sql("INSERT INTO scout_candidates(id,key,kind,head_sha,install_risk,first_seen,last_seen,status) VALUES('C-0001','gh:a/b','skill','abc','prompt-only','t','t','triaged')")
	r.sql("INSERT INTO skill_trials(candidate_id,vendor_sha,task,condition,status,passed,wall_s,tokens_in,tokens_out,queued) VALUES('C-0001','abc','fix-bug','with','failed',0,9,1,2,'q')")
	code, out, errs := r.run("", "skilleval", "list")
	want := "C-0001 fix-bug with: trial 1 status=failed passed=0 tokens=3 wall_s=9 skill_invoked=-\n" +
		"C-0001 summary: with: passed 0/1 tasks, median tokens 3, median wall_s 9; without: passed 0/0 tasks, median tokens -, median wall_s -\n"
	if code != 0 || out != want || r.execs != 0 {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
	if code, out, _ := r.run("", "skilleval", "results", "--candidate", "C-9999"); code != 0 || out != "no trials\n" {
		t.Fatalf("results: %d %q", code, out)
	}
	code, out, errs = r.run("", "skilleval", "void", "tr0001", "--reason", "infra", "--why", "docker died")
	if code != 0 || !strings.HasSuffix(out, "void_reason=infra void_why='docker died'\n") || !strings.Contains(out, "status=voided") {
		t.Fatalf("void: %d %q %q", code, out, errs)
	}
	if n := r.count("SELECT COUNT(*) FROM events WHERE action='void' AND actor='director-1' AND detail LIKE 'trial 1 fix-bug with was failed (passed=0 tokens_in=1 tokens_out=2 wall_s=9): infra: docker died'"); n != 1 {
		t.Fatalf("void event rows: %d", n)
	}
	// usage and role refusals use Python's text and exit codes
	if code, _, errs := r.run("", "skilleval", "void", "1", "--reason", "infra"); code != 2 || !strings.Contains(errs, `usage: factory skilleval void <trial id|trNNNN> --reason infra --why "<what failed>"`) {
		t.Fatalf("void usage: %d %q", code, errs)
	}
	if code, _, errs := r.run("", "skilleval", "queue", "C-0001"); code != 2 || !strings.Contains(errs, "unknown eval task (none given); known: ") {
		t.Fatalf("queue with no eval tasks: %d %q", code, errs)
	}
	if code, _, errs := r.run("", "skilleval", "vendor"); code != 2 || !strings.Contains(errs, "usage: factory skilleval vendor C-xxxx [--rescreen]") {
		t.Fatalf("vendor usage: %d %q", code, errs)
	}
	if code, _, errs := r.run("", "skilleval", "vendor", "C-9", "--rescreen"); code != 2 || !strings.Contains(errs, "no candidate C-9") {
		t.Fatalf("vendor rescreen unknown: %d %q", code, errs)
	}
	boss := newP4cRig(t, "boss-1", "boss")
	if code, _, errs := boss.run("", "skilleval", "queue", "C-0001"); code != 2 || !strings.Contains(errs, "unknown eval task") {
		t.Fatalf("boss may queue: %d %q", code, errs)
	}
	if code, _, errs := boss.run("", "skilleval", "list"); code != 3 || !strings.Contains(errs, "role 'boss' (boss-1) may not do this; allowed: director, lead") {
		t.Fatalf("boss may queue but not list: %d %q", code, errs)
	}
	pm := newP4cRig(t, "pm-1", "pm")
	if code, _, _ := pm.run("", "skilleval", "list"); pm.execs != 1 {
		// a worker is Python's to refuse (its text, its stub exemption): the Go CLI hands the command over
		t.Fatalf("pm: code %d execs %d", code, pm.execs)
	}
}

func TestScoutCLI(t *testing.T) {
	r := newP4cRig(t, "director-1", "director")
	if code, out, _ := r.run("", "scout", "list"); code != 0 || out != "(none)\n" {
		t.Fatalf("empty list: %d %q", code, out)
	}
	r.sql("INSERT INTO scout_candidates(id,key,kind,repo,head_sha,stars,stars_per_day,install_risk,first_seen,last_seen,status) VALUES('C-0001','gh:a/b','skill','a/b','abc',12,3.25,'prompt-only','2026-11-01T00:00:00+00:00','2026-11-02T00:00:00+00:00','triaged')")
	r.sql("INSERT INTO scout_candidates(id,key,first_seen,last_seen) VALUES('C-0002','gh:c/d','2026-11-03T00:00:00+00:00','t')")
	r.sql("INSERT INTO scout_triage(candidate_id,purpose,target_roles,overlap,recommendation,rationale,created,author) VALUES('C-0001','p','coder','o','adopt','w','2026-11-04T00:00:00+00:00','scout-1')")
	r.sql("INSERT INTO scout_stars(candidate_id,ts,stars) VALUES('C-0001','2026-11-02T00:00:00+00:00',10)")
	r.sql("INSERT INTO scout_runs(slot,status,agent,started,tokens,new_candidates,triaged) VALUES('2026-11-04T09','done','claude-scout','2026-11-04T09:00:00+00:00',500,2,2)")
	r.sql("INSERT INTO scout_runs(slot,status,stop_reason) VALUES('2026-11-04T18','stopped','wall')")
	wantList := "C-0001 triaged   adopt 12* +3.2/d risk=prompt-only gh:a/b\nC-0002 new       -     -* - risk=- gh:c/d\n"
	if code, out, errs := r.run("", "scout", "list"); code != 0 || out != wantList {
		t.Fatalf("list: %d %q %q", code, out, errs)
	}
	if code, out, _ := r.run("", "scout", "list", "--new"); code != 0 || out != "C-0002 new       -     -* - risk=- gh:c/d\n" {
		t.Fatalf("list --new: %d %q", code, out)
	}
	if code, out, _ := r.run("", "scout", "list", "--rec", "adopt", "--risk", "prompt-only"); code != 0 || !strings.HasPrefix(out, "C-0001") || strings.Contains(out, "C-0002") {
		t.Fatalf("list filters: %d %q", code, out)
	}
	if code, _, errs := r.run("", "scout", "list", "--since", "nonsense"); code != 2 || !strings.Contains(errs, "bad --since 'nonsense': expected an ISO 8601 date or timestamp") {
		t.Fatalf("bad since: %d %q", code, errs)
	}
	code, out, _ := r.run("", "scout", "show", "C-0001")
	for _, want := range []string{"id: C-0001\n", "stars_per_day: 3.25\n", "default_branch: None\n", "seen_count: 1\n", "## star snapshots\n- 2026-11-02T00:00:00+00:00 10\n",
		"## triage\n- 2026-11-04T00:00:00+00:00 scout-1 rec=adopt roles=coder eval_suggested=0\n  purpose: p\n  overlap: o\n  why: w\n"} {
		if code != 0 || !strings.Contains(out, want) {
			t.Fatalf("show lacks %q:\n%s", want, out)
		}
	}
	if code, _, errs := r.run("", "scout", "show", "C-9"); code != 2 || !strings.Contains(errs, "no candidate C-9") {
		t.Fatalf("show missing: %d %q", code, errs)
	}
	wantRuns := "2026-11-04T18 stopped  agent=- new=None triaged=None tokens=- started=- ended=- wall\n" +
		"2026-11-04T09 done     agent=claude-scout new=2 triaged=2 tokens=500 started=2026-11-04T09:00:00+00:00 ended=-\n"
	if code, out, _ := r.run("", "scout", "runs"); code != 0 || out != wantRuns {
		t.Fatalf("runs: %d %q", code, out)
	}
}

func TestCursorPilotStatus(t *testing.T) {
	r := newP4cRig(t, "director-1", "director")
	r.sql("INSERT INTO config(key,value) VALUES('agent_runtimes','scout=cursor,pm=cursor,scout=claude')")
	t.Setenv("PATH", t.TempDir()) // no agent binary
	code, out, errs := r.run("", "cursor-pilot", "status")
	if code != 0 {
		t.Fatalf("%d %q", code, errs)
	}
	for _, want := range []string{"agent_runtimes = 'scout=cursor,pm=cursor,scout=claude'\n", "cursor_pilot_roles = '' (legacy alias)\n",
		"resolved = {'scout': 'claude', 'pm': 'cursor'}\n", "agent_binary = (not on PATH)\n",
		"WORKS       Multi-runtime registry (claude default, cursor extension): scout.agent_runtime.resolve_runtime / plan_spawn; *=cursor for whole factory\n",
		"NEEDS_WORK  Headless agent CLI installed: install: curl https://cursor.com/install | bash\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	r.sql("UPDATE config SET value='pm=bogus' WHERE key='agent_runtimes'")
	if _, out, _ := r.run("", "cursor-pilot", "status"); !strings.Contains(out, "resolved = {'<error>': \"unknown runtime 'bogus'; known: claude, cursor\"}\n") {
		t.Errorf("error form:\n%s", out)
	}
	pm := newP4cRig(t, "pm-1", "pm")
	if code, _, _ := pm.run("", "cursor-pilot", "status"); pm.execs != 1 {
		t.Errorf("a pm is refused by Python, not served: code %d execs %d", code, pm.execs)
	}
	lead := newP4cRig(t, "lead-1", "lead")
	if code, _, errs := lead.run("", "cursor-pilot", "status"); code != 3 || !strings.Contains(errs, "role 'lead' (lead-1) may not do this; allowed: director, boss") {
		t.Errorf("lead: %d %q", code, errs)
	}
}
