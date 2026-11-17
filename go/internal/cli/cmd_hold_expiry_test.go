package cli

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/identity"
)

// holdEnv is a factorytest home with an in_review PR task T-0001, reviewer+QA PASS and a hold set 121 min ago (the
// rule reads the real clock, so the event is dated relative to it).
func holdEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTask(ft.Task{ID: "T-0001"})
	e.SeedAgent(ft.AgentRow{Name: "zz-coder", Role: "coder"})
	d := e.RawDB()
	defer d.Close()
	ts := time.Now().Add(-121 * time.Minute).Format("2006-01-02T15:04:05-07:00")
	stmts := []string{
		"UPDATE tasks SET judge_hold_reason='wait for PR 703' WHERE id='T-0001'",
		"INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('" + ts + "','zz-lead','task','T-0001','judge-hold-set','wait for PR 703')",
	}
	for _, role := range []string{"reviewer", "qa"} {
		stmts = append(stmts, "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES('T-0001','"+ft.PRURL+"','a','"+role+"','PASS','zz','zz-"+role+"','t')")
	}
	for _, q := range stmts {
		if _, err := d.Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	return e
}

func holdOf(e *ft.Env) string {
	return e.SQL("SELECT COALESCE(judge_hold_reason,'') FROM tasks WHERE id='T-0001'")[0][0]
}

// runHoldExpiry runs `factory-go hold-expiry <args>` in process as who against e's home.
func runHoldExpiry(e *ft.Env, who string, args ...string) (code int, out, errs string, execs int) {
	m := map[string]string{"FACTORY_HOME": e.Home, "HOME": e.UserHome, "FACTORY_PROC_DIR": e.ProcDir, "FACTORY_AGENT": who, "TZ": "UTC"}
	var o, eb bytes.Buffer
	code = Run(Deps{
		Args: append([]string{"hold-expiry"}, args...), Argv0: "bin/factory", Environ: []string{"FACTORY_CLI_IMPL=go"},
		Env: identity.Env{
			Getenv:    func(k string) string { return m[k] },
			LookupEnv: func(k string) (string, bool) { v, ok := m[k]; return v, ok },
			Uid:       os.Getuid(),
			PwDir:     func(int) (string, error) { return e.UserHome, nil },
			Now:       time.Now,
		},
		Stdout: &o, Stderr: &eb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		LookPath: func(f string) (string, error) { return "/usr/bin/" + f, nil },
		Exec:     func(string, []string, []string) error { execs++; return nil },
	})
	return code, o.String(), eb.String(), execs
}

func TestHoldExpiryCmdOwnerClearsAnExpiredHold(t *testing.T) {
	e := holdEnv(t)
	code, out, errs, execs := runHoldExpiry(e, "owner")
	if code != 0 || execs != 0 {
		t.Fatalf("exit %d, execs %d, out %q err %q", code, execs, out, errs)
	}
	if !strings.Contains(out, "judge-hold-expired T-0001:") {
		t.Fatalf("stdout %q has no judge-hold-expired line", out)
	}
	if h := holdOf(e); h != "" {
		t.Fatalf("hold %q kept, want cleared", h)
	}
}

func TestHoldExpiryCmdWorkerIsRefused(t *testing.T) {
	e := holdEnv(t)
	code, out, errs, _ := runHoldExpiry(e, "zz-coder")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (out %q err %q)", code, out, errs)
	}
	if h := holdOf(e); h != "wait for PR 703" {
		t.Fatalf("hold %q changed by a worker", h)
	}
}

func TestHoldExpiryCmdArgumentIsAUsageError(t *testing.T) {
	e := holdEnv(t)
	code, out, errs, _ := runHoldExpiry(e, "owner", "x")
	if code != 2 {
		t.Fatalf("exit %d, want 2 (out %q err %q)", code, out, errs)
	}
	if h := holdOf(e); h != "wait for PR 703" {
		t.Fatalf("hold %q changed by a refused call", h)
	}
}
