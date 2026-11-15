package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/identity"
	_ "modernc.org/sqlite"
)

func schedRun(t *testing.T, agent string, argv ...string) (int, string, string, string) {
	t.Helper()
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "s1"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
	var out, errb bytes.Buffer
	deps := Deps{Args: argv, Stdout: &out, Stderr: &errb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}}
	code := Run(deps)
	return code, out.String(), errb.String(), home
}

func scheduleEventCount(t *testing.T, home, action string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "db", "factory.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM events WHERE action=?", action).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduleJobJSONRoundTrip(t *testing.T) {
	job := `quotes "' and $(x) and ` + "`" + `backticks`
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "digestall"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_PROC_DIR": t.TempDir()}
	idEnv := identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
		PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}
	pid := func() int { return 1 }
	var errb bytes.Buffer
	if code := Run(Deps{Args: []string{"schedule", "add", "--hour", "10", "--goal", "G-1", "--token-cap", "100000", job},
		Stdout: new(bytes.Buffer), Stderr: &errb, Getpid: pid, Getppid: pid, Env: idEnv}); code != 0 {
		t.Fatalf("add exit %d stderr=%s", code, errb.String())
	}
	subproc := false
	var out bytes.Buffer
	var errb2 bytes.Buffer
	code := Run(Deps{Args: []string{"schedule", "list", "--json"}, Stdout: &out, Stderr: &errb2, Getpid: pid, Getppid: pid,
		RunCmd: func(string, []string, string, []string, time.Duration) (string, string, int, error) {
			subproc = true
			return "", "", 0, nil
		}, Env: idEnv})
	if code != 0 || subproc {
		t.Fatalf("list exit %d subproc=%v", code, subproc)
	}
	raw := out.String()
	if i := strings.Index(raw, "\nfixed schedules:"); i >= 0 {
		raw = raw[:i]
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("json: %v stdout=%q", err, out.String())
	}
	if len(rows) != 1 || rows[0]["job"] != job {
		t.Fatalf("job bytes changed: %v", rows)
	}
}

func TestScheduleAddRefusedIdentity(t *testing.T) {
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "digestall"); err != nil {
		t.Fatal(err)
	}
	run := func(agent string, argv ...string) (int, string, string) {
		t.Helper()
		env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
		var out, errb bytes.Buffer
		deps := Deps{Args: argv, Stdout: &out, Stderr: &errb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
			Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
				PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}}
		return Run(deps), out.String(), errb.String()
	}
	before := scheduleEventCount(t, home, "schedule-refused")
	code, _, stderr := run("claude-coder-t-9999", "schedule", "add", "--hour", "9", "--goal", "G-1", "--token-cap", "100", "j")
	if code != 3 {
		t.Fatalf("add exit %d stderr=%q", code, stderr)
	}
	if got := scheduleEventCount(t, home, "schedule-refused"); got != before+1 {
		t.Fatalf("schedule-refused events after add: got %d want %d", got, before+1)
	}
	code, _, _ = run("claude-coder-t-9999", "schedule", "remove", "S-001")
	if code != 3 {
		t.Fatalf("remove exit %d", code)
	}
	if got := scheduleEventCount(t, home, "schedule-refused"); got != before+2 {
		t.Fatalf("schedule-refused events after remove: got %d want %d", got, before+2)
	}
	code, _, _ = run("claude-coder-t-9999", "schedule", "list")
	if code != 0 {
		t.Fatalf("list exit %d", code)
	}
}

func TestScheduleRemoveListAll(t *testing.T) {
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "digestall"); err != nil {
		t.Fatal(err)
	}
	run := func(agent string, argv ...string) (int, string, string) {
		t.Helper()
		env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": agent, "FACTORY_PROC_DIR": t.TempDir()}
		var out, errb bytes.Buffer
		deps := Deps{Args: argv, Stdout: &out, Stderr: &errb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
			Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
				PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}}
		return Run(deps), out.String(), errb.String()
	}
	code, stdout, _ := run("owner", "schedule", "add", "--hour", "10", "--goal", "G-1", "--token-cap", "100000", "weekly check")
	if code != 0 {
		t.Fatalf("add exit %d", code)
	}
	id := strings.TrimSpace(stdout)
	code, stdout, _ = run("owner", "schedule", "remove", id)
	if code != 0 {
		t.Fatalf("remove exit %d", code)
	}
	code, stdout, _ = run("owner", "schedule", "list")
	if code != 0 {
		t.Fatalf("list exit %d", code)
	}
	if strings.Contains(stdout, id) {
		t.Fatalf("active list still shows %s: %q", id, stdout)
	}
	code, stdout, _ = run("owner", "schedule", "list", "--all")
	if code != 0 || !strings.Contains(stdout, id) {
		t.Fatalf("list --all missing %s: code=%d stdout=%q", id, code, stdout)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(home, "db", "factory.db")+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var removed sql.NullString
	if err := db.QueryRow("SELECT removed_at FROM schedules WHERE id=?", id).Scan(&removed); err != nil || !removed.Valid || removed.String == "" {
		t.Fatalf("removed_at: err=%v valid=%v", err, removed.Valid)
	}
	code, _, stderr := run("owner", "schedule", "remove", id)
	if code != 2 || !strings.Contains(stderr, "already removed") {
		t.Fatalf("second remove: code=%d stderr=%q", code, stderr)
	}
}

func TestScheduleDigestHourWarning(t *testing.T) {
	home := t.TempDir()
	if _, err := buildGoldenDB(t, home, "digestall"); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"FACTORY_HOME": home, "HOME": home, "FACTORY_AGENT": "owner", "FACTORY_PROC_DIR": t.TempDir()}
	var out, errb bytes.Buffer
	code := Run(Deps{Args: []string{"schedule", "add", "--hour", "8", "--goal", "G-1", "--token-cap", "100", "overlap"},
		Stdout: &out, Stderr: &errb, Getpid: func() int { return 1 }, Getppid: func() int { return 1 },
		Env: identity.Env{Getenv: func(k string) string { return env[k] }, LookupEnv: func(k string) (string, bool) { v, ok := env[k]; return v, ok },
			PwDir: func(int) (string, error) { return home, nil }, Now: func() time.Time { return time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC) }}})
	stderr := errb.String()
	if code != 0 {
		t.Fatalf("add exit %d stderr=%q", code, stderr)
	}
	if !strings.Contains(stderr, "digest_hour") {
		t.Fatalf("stderr missing digest_hour overlap: %q", stderr)
	}
}

func FuzzParseScheduleAdd(f *testing.F) {
	f.Add("--hour 9 --goal G-1 --token-cap 100 job")
	f.Fuzz(func(t *testing.T, s string) {
		_, _ = parseScheduleAdd(strings.Fields(s))
	})
}
