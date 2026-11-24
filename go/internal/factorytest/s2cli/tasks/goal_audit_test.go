package s2tasks

import (
	dbsql "database/sql"
	"encoding/json"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"

	_ "modernc.org/sqlite"
)

// Port of tests/test_goal_audit.py (18 of 19 cases; PARTIAL for P7a, T-1495). G-004 B2: goal audit and sign-off records.
// not ported: GoalAuditTests.test_migrate_creates_audit_tables_on_old_db - it asserts the Python db() migrate()
// re-creates the audits/signoffs tables of an old database; the Go CLI opens the database read-only and leaves
// migration to Python (go/internal/cli/cmd_res.go:1008), so there is no Go counterpart to drive.
// The two export cases drive `factory export`, which Go hands to Python: they NeedPython and skip without it.

const gaWorkerSettings = "/srv/x/factory/roles/settings/coder.json"

func gaEnv(t *testing.T) *ft.Env {
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-002", "", "zz-test goal")
	e.SeedTask(ft.Task{ID: "T-0001", Status: "done"})
	e.SeedTask(ft.Task{ID: "T-0002", Status: "cancelled"})
	seedTask(e, ft.Task{ID: "T-0003", Role: "pm", Status: "in_progress"}, true) // the audit pm's own task
	seedTask(e, ft.Task{ID: "T-0009", Goal: "G-002", Role: "pm", Status: "in_progress"}, true)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm", Role: "pm", Goal: "G-001", Task: "T-0003"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-pm2", Role: "pm", Goal: "G-002", Task: "T-0009"})
	for _, a := range [][2]string{{"lead", "lead"}, {"director", "director"}, {"boss", "boss"}, {"coder", "coder"}, {"qa", "qa"}} {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + a[0], Role: a[1], Goal: "G-001"})
	}
	return e
}

func gaAudit(e *ft.Env, agent, flag, goal, report string) ft.Result {
	return runO(e, ft.Opts{Agent: agent, Stdin: report}, "goal", "audit", goal, flag, "--report", "-")
}

// gaAudits returns "version|verdict|body|author|task_id" per audit, oldest first.
func gaAudits(e *ft.Env) []string {
	var out []string
	for _, r := range sql(e, "SELECT version, verdict, body, author, task_id FROM audits ORDER BY id") {
		out = append(out, strings.Join(r, "|"))
	}
	return out
}

func gaSignoff(e *ft.Env, n, agent string) ft.Result {
	return run(e, agent, "goal", "signoff", "G-001", "--audit", n, "--reason", "looks right")
}

func gaReadyAudit(e *ft.Env) {
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	okS(e, "zz-test-pm", "ready body", "goal", "audit", "G-001", "--ready", "--report", "-")
}

func gaSignoffs(e *ft.Env) [][]string { return sql(e, "SELECT * FROM signoffs") }

func TestGoalAuditPMRecordsAuditVersionsIncrement(t *testing.T) {
	e := gaEnv(t)
	p := okS(e, "zz-test-pm", "gap one", "goal", "audit", "G-001", "--gaps", "--report", "-")
	contains(t, "stdout", p.Stdout, "G-001 audit v1 (gaps)")
	eq(t, "director ready exit", gaAudit(e, "zz-test-director", "--ready", "G-001", "v2 body").Exit, 2) // T-0003 still open for the director
	exec(e, "UPDATE tasks SET status='done' WHERE id='T-0003'")
	p = okS(e, "zz-test-director", "v2 body", "goal", "audit", "G-001", "--ready", "--report", "-")
	contains(t, "stdout", p.Stdout, "G-001 audit v2 (ready)")
	var got []string
	for _, r := range sql(e, "SELECT version, verdict, body, author FROM audits ORDER BY id") {
		got = append(got, strings.Join(r, "|"))
	}
	eqS(t, "audits", got, []string{"1|gaps|gap one|zz-test-pm", "2|ready|v2 body|zz-test-director"})
	eqS(t, "audit events", col(e, "SELECT action FROM events WHERE entity='goal' AND entity_id='G-001' AND action LIKE 'audit v%' ORDER BY id"),
		[]string{"audit v1", "audit v2"})
	// versions are per goal
	okS(e, "zz-test-pm2", "x", "goal", "audit", "G-002", "--gaps", "--report", "-")
	eq(t, "G-002 version", val(e, "SELECT version FROM audits ORDER BY id DESC LIMIT 1"), "1")
}

func TestGoalAuditStoresCallersTaskID(t *testing.T) {
	e := gaEnv(t)
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--ready", "--report", "-")
	okS(e, "zz-test-director", "r", "goal", "audit", "G-001", "--gaps", "--report", "-")
	eqS(t, "task_id", col(e, "SELECT COALESCE(task_id,'NULL') FROM audits ORDER BY id"), []string{"T-0003", "NULL"})
}

func TestGoalAuditRequiresReadyOrGaps(t *testing.T) {
	e := gaEnv(t)
	for _, flags := range [][]string{nil, {"--ready", "--gaps"}} {
		args := append(append([]string{"goal", "audit", "G-001"}, flags...), "--report", "-")
		p := refusedO(e, 2, ft.Opts{Agent: "zz-test-pm", Stdin: "r"}, args...)
		contains(t, "stderr", p.Stderr, "exactly one of --ready or --gaps")
	}
	p := refusedO(e, 2, ft.Opts{Agent: "zz-test-pm", Stdin: " "}, "goal", "audit", "G-001", "--gaps", "--report", "-")
	contains(t, "stderr", p.Stderr, "--report is required")
	eqS(t, "audits", gaAudits(e), nil)
}

func TestGoalAuditReadyRefusedWithOpenTasksExceptCallersOwn(t *testing.T) {
	e := gaEnv(t)
	e.SeedTask(ft.Task{ID: "T-0004", Status: "in_review"})
	seedTask(e, ft.Task{ID: "T-0005", Status: "todo"}, true)
	p := refusedO(e, 2, ft.Opts{Agent: "zz-test-pm", Stdin: "r"}, "goal", "audit", "G-001", "--ready", "--report", "-")
	contains(t, "stderr", p.Stderr, "audit --ready refused: open tasks T-0004(in_review), T-0005(todo)")
	notContains(t, "stderr", p.Stderr, "T-0003")
	eqS(t, "audits", gaAudits(e), nil)
	exec(e, "UPDATE tasks SET status='done' WHERE id IN ('T-0004','T-0005')")
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--ready", "--report", "-")
	// --gaps is never gated by open tasks
	exec(e, "UPDATE tasks SET status='todo' WHERE id='T-0004'")
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--gaps", "--report", "-")
}

func TestGoalAuditRefusedForWrongRole(t *testing.T) {
	e := gaEnv(t)
	for _, who := range []string{"coder", "qa", "lead"} {
		p := refusedO(e, 3, ft.Opts{Agent: "zz-test-" + who, Stdin: "r"}, "goal", "audit", "G-001", "--gaps", "--report", "-")
		contains(t, "stderr", p.Stderr, "role '"+who+"' may not record a goal audit; allowed: pm (own goal), director")
	}
	eqS(t, "audits", gaAudits(e), nil)
}

func TestGoalAuditRefusedForPMOfOtherGoal(t *testing.T) {
	e := gaEnv(t)
	p := refusedO(e, 3, ft.Opts{Agent: "zz-test-pm2", Stdin: "r"}, "goal", "audit", "G-001", "--gaps", "--report", "-")
	contains(t, "stderr", p.Stderr, "zz-test-pm2 works on G-002; the audit is in G-001")
	eqS(t, "audits", gaAudits(e), nil)
}

func TestGoalAuditDirectorSignsOffLatestReadyAudit(t *testing.T) {
	e := gaEnv(t)
	gaReadyAudit(e)
	p := ok(e, "zz-test-director", "goal", "signoff", "G-001", "--audit", "1", "--reason", "all verified")
	contains(t, "stdout", p.Stdout, "G-001 audit v1 signed off")
	so := sql(e, "SELECT goal_id, audit_id, author, reason, snapshot FROM signoffs")
	eq(t, "signoff rows", len(so), 1)
	eqS(t, "signoff", so[0][:4], []string{"G-001", val(e, "SELECT id FROM audits ORDER BY id LIMIT 1"), "zz-test-director", "all verified"})
	var snap map[string]string
	if err := json.Unmarshal([]byte(so[0][4]), &snap); err != nil {
		t.Fatalf("snapshot %q: %v", so[0][4], err)
	}
	if len(snap) != 3 || snap["T-0001"] != "done" || snap["T-0002"] != "cancelled" || snap["T-0003"] != "done" {
		t.Fatalf("snapshot = %v, want T-0001 done, T-0002 cancelled, T-0003 done", snap)
	}
	eq(t, "signoff events", val(e, "SELECT count(*) FROM events WHERE action='signoff v1'"), "1")
}

func TestGoalAuditSignoffExemptsAuditTaskAndOwnerMaySign(t *testing.T) {
	e := gaEnv(t)
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--ready", "--report", "-") // T-0003 open
	ok(e, "", "goal", "signoff", "G-001", "--audit", "1", "--reason", "r")          // owner (no FACTORY_AGENT)
	var snap map[string]string
	if err := json.Unmarshal([]byte(val(e, "SELECT snapshot FROM signoffs")), &snap); err != nil {
		t.Fatal(err)
	}
	eq(t, "T-0003 in snapshot", snap["T-0003"], "in_progress")
}

func TestGoalAuditSignoffRefusedForOpenTaskOtherThanAuditTask(t *testing.T) {
	e := gaEnv(t)
	gaReadyAudit(e)
	seedTask(e, ft.Task{ID: "T-0004", Status: "todo"}, true)
	p := refused(e, 2, "zz-test-director", "goal", "signoff", "G-001", "--audit", "1", "--reason", "r")
	contains(t, "stderr", p.Stderr, "open tasks T-0004(todo)")
	eq(t, "signoffs", len(gaSignoffs(e)), 0)
}

func TestGoalAuditSignoffRefusedForPMLeadBoss(t *testing.T) {
	e := gaEnv(t)
	e.NeedPython() // the worker refusal (refuse_worker) of goal signoff is Python
	gaReadyAudit(e)
	for _, who := range []string{"lead", "boss"} {
		p := gaSignoff(e, "1", "zz-test-"+who)
		eq(t, who+" exit", p.Exit, 3)
		contains(t, "stderr", p.Stderr, "only the director signs off a goal close")
	}
	for _, who := range []string{"pm", "coder"} { // worker roles are stopped by refuse_worker
		p := gaSignoff(e, "1", "zz-test-"+who)
		eq(t, who+" exit", p.Exit, 3)
		contains(t, "stderr", p.Stderr, "workers may not run `factory goal signoff`")
	}
	eq(t, "signoffs", len(gaSignoffs(e)), 0)
}

func TestGoalAuditSignoffRefusedForWorkerProcessClaimingDirector(t *testing.T) {
	e := gaEnv(t)
	e.NeedPython() // the worker refusal (refuse_worker) of goal signoff is Python
	gaReadyAudit(e)
	e.FakeProc(os.Getpid(), "x", 1, "claude", "--settings", gaWorkerSettings, "--resume", "s")
	p := gaSignoff(e, "1", "zz-test-director")
	eq(t, "exit", p.Exit, 3)
	contains(t, "stderr", p.Stderr, "workers may not run `factory goal signoff`")
	notContains(t, "stderr", p.Stderr, "only the director") // refuse_worker ran before require_role
	eq(t, "signoffs", len(gaSignoffs(e)), 0)
}

func TestGoalAuditSignoffRefusedForGapsAudit(t *testing.T) {
	e := gaEnv(t)
	okS(e, "zz-test-pm", "r", "goal", "audit", "G-001", "--gaps", "--report", "-")
	p := gaSignoff(e, "1", "")
	eq(t, "exit", p.Exit, 2)
	contains(t, "stderr", p.Stderr, "audit v1 reports gaps; nothing to sign off")
}

func TestGoalAuditSignoffRefusedForNonLatestAudit(t *testing.T) {
	e := gaEnv(t)
	gaReadyAudit(e)
	okS(e, "zz-test-pm", "r2", "goal", "audit", "G-001", "--ready", "--report", "-")
	p := gaSignoff(e, "1", "")
	eq(t, "exit", p.Exit, 2)
	contains(t, "stderr", p.Stderr, "audit v1 is not the latest audit of G-001 (latest: v2)")
	eq(t, "signoffs", len(gaSignoffs(e)), 0)
}

func TestGoalAuditSignoffRequiresReason(t *testing.T) {
	e := gaEnv(t)
	gaAudit(e, "zz-test-pm", "--ready", "G-001", "all good")
	for _, extra := range [][]string{nil, {"--reason", "  "}} {
		args := append([]string{"goal", "signoff", "G-001", "--audit", "1"}, extra...)
		p := refused(e, 2, "zz-test-director", args...)
		contains(t, "stderr", p.Stderr, "--reason is required")
	}
	eq(t, "signoffs", len(gaSignoffs(e)), 0)
}

func TestGoalAuditSignoffRefusedWithoutAudit(t *testing.T) {
	e := gaEnv(t)
	p := gaSignoff(e, "1", "")
	eq(t, "exit", p.Exit, 2)
	contains(t, "stderr", p.Stderr, "no audit for G-001: run factory goal audit")
}

func TestGoalAuditGoalShowPrintsLatestAuditAndSignoff(t *testing.T) {
	e := gaEnv(t)
	notContains(t, "goal show", ok(e, "", "goal", "show", "G-001").Stdout, "## audit")
	okS(e, "zz-test-pm", "old gap report", "goal", "audit", "G-001", "--gaps", "--report", "-")
	gaReadyAudit(e)
	out := ok(e, "", "goal", "show", "G-001").Stdout
	contains(t, "goal show", out, "## audit v2 ready (zz-test-pm,")
	contains(t, "goal show", out, "ready body")
	notContains(t, "goal show", out, "old gap report")
	notContains(t, "goal show", out, "sign-off")
	ok(e, "zz-test-director", "goal", "signoff", "G-001", "--audit", "2", "--reason", "verified by director")
	out = ok(e, "", "goal", "show", "G-001").Stdout
	contains(t, "goal show", out, "## sign-off of audit v2 (zz-test-director,")
	contains(t, "goal show", out, "verified by director")
}

func gaExport(t *testing.T, e *ft.Env) {
	t.Helper()
	e.NeedPython() // `factory export` is not ported: the Go CLI hands it to bin/factory
	cmd := osexec.Command("git", "init", "-q")
	cmd.Dir = e.Home
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	ok(e, "", "export", "--no-push")
}

func TestGoalAuditExportGoalPageIncludesAuditAndSignoff(t *testing.T) {
	e := gaEnv(t)
	gaReadyAudit(e)
	ok(e, "zz-test-director", "goal", "signoff", "G-001", "--audit", "1", "--reason", "verified by director")
	gaExport(t, e)
	b, err := os.ReadFile(filepath.Join(e.Home, "exports", "goals", "G-001.md"))
	if err != nil {
		t.Fatal(err)
	}
	page := string(b)
	contains(t, "goal page", page, "## Audit (v1, ready, zz-test-pm,")
	contains(t, "goal page", page, "ready body")
	contains(t, "goal page", page, "### Sign-off (zz-test-director,")
	contains(t, "goal page", page, "verified by director")
}

func TestGoalAuditStateDumpContainsAuditsAndSignoffs(t *testing.T) {
	e := gaEnv(t)
	gaReadyAudit(e)
	ok(e, "zz-test-director", "goal", "signoff", "G-001", "--audit", "1", "--reason", "r")
	gaExport(t, e)
	b, err := os.ReadFile(filepath.Join(e.Home, "state", "factory.sql"))
	if err != nil {
		t.Fatal(err)
	}
	contains(t, "state dump", string(b), "audits")
	d, err := openMemDB()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Exec(string(b)); err != nil {
		t.Fatalf("state dump does not load: %v", err)
	}
	var ver int
	var verdict, reason string
	if err := d.QueryRow("SELECT version, verdict FROM audits").Scan(&ver, &verdict); err != nil {
		t.Fatal(err)
	}
	if ver != 1 || verdict != "ready" {
		t.Fatalf("audits row = %d %s, want 1 ready", ver, verdict)
	}
	if err := d.QueryRow("SELECT reason FROM signoffs").Scan(&reason); err != nil || reason != "r" {
		t.Fatalf("signoffs reason = %q (%v), want r", reason, err)
	}
}

// openMemDB is an in-memory database the exported state dump is loaded into (one connection: :memory: is per connection).
func openMemDB() (*dbsql.DB, error) {
	d, err := dbsql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, err
	}
	d.SetMaxOpenConns(1)
	return d, nil
}
