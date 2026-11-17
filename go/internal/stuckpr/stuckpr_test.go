package stuckpr_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/stuckpr"

	_ "modernc.org/sqlite"
)

const testSchema = `
CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE tasks (
  id TEXT PRIMARY KEY, goal_id TEXT, team_id TEXT, pr_url TEXT, branch TEXT,
  status TEXT NOT NULL, updated TEXT NOT NULL);
CREATE TABLE agents (
  name TEXT PRIMARY KEY, role TEXT, status TEXT, task_id TEXT, started TEXT);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, entity TEXT, entity_id TEXT,
  action TEXT NOT NULL, detail TEXT);
CREATE TABLE messages (
  id INTEGER PRIMARY KEY AUTOINCREMENT, sender TEXT, recipient TEXT, kind TEXT,
  subject TEXT, body TEXT, goal_id TEXT, task_id TEXT, created TEXT NOT NULL);
`

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "f.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(testSchema); err != nil {
		t.Fatal(err)
	}
	return db
}

func seedTask(t *testing.T, db *sql.DB, id, updated string) stuckpr.TaskRow {
	t.Helper()
	pr := "https://github.com/volekerb/hivemill/pull/1"
	_, err := db.Exec(`INSERT INTO tasks(id,goal_id,team_id,pr_url,branch,status,updated)
VALUES(?,?,?,?,?,?,?)`, id, "G-050", "g-050-a", pr, "factory/"+id, "in_review", updated)
	if err != nil {
		t.Fatal(err)
	}
	return stuckpr.TaskRow{ID: id, GoalID: "G-050", TeamID: "g-050-a", PRURL: pr, Branch: "factory/" + id, Status: "in_review", Updated: updated}
}

func fixedNow(t0 string) func() (float64, error) {
	base, _ := time.Parse(time.RFC3339, t0)
	return func() (float64, error) { return float64(base.UnixNano()) / 1e9, nil }
}

func viewFrom(rollup []any) func(context.Context, string) (*stuckpr.PRView, error) {
	return func(context.Context, string) (*stuckpr.PRView, error) {
		m := map[string]any{
			"number": float64(1), "state": "OPEN", "headRefOid": strings.Repeat("a", 40),
			"mergeable": "MERGEABLE", "url": "https://github.com/volekerb/hivemill/pull/1",
			"statusCheckRollup": rollup,
		}
		return stuckpr.ParsePRView(m)
	}
}

func TestStillRunningNoAction(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T16:00:00Z")
	rollup := []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "IN_PROGRESS", "workflowName": "tests",
	}}
	var wakes, esc int
	d := stuckpr.Deps{
		DB: db, Now: fixedNow("2026-11-15T17:00:00Z"),
		ListTasks: func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{task}, nil },
		ViewPR:    viewFrom(rollup),
		WakeCoder: func(context.Context, *sql.DB, stuckpr.TaskRow, *stuckpr.PRView) error { wakes++; return nil },
		Escalate: func(context.Context, *sql.DB, stuckpr.TaskRow, *stuckpr.PRView, time.Duration) error {
			esc++
			return nil
		},
	}
	acts, err := stuckpr.Run(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(acts) != 0 || wakes != 0 || esc != 0 {
		t.Fatalf("acts=%v wakes=%d esc=%d", acts, wakes, esc)
	}
}

func TestFinishedWhileStatusInProgress(t *testing.T) {
	rollup := []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "IN_PROGRESS",
		"conclusion": "FAILURE", "completedAt": "2026-11-15T16:05:00Z", "workflowName": "tests",
	}}
	pr, err := stuckpr.ParsePRView(map[string]any{
		"number": float64(1), "state": "OPEN", "headRefOid": strings.Repeat("b", 40),
		"mergeable": "MERGEABLE", "statusCheckRollup": rollup,
	})
	if err != nil {
		t.Fatal(err)
	}
	if pr.StillRunning {
		t.Fatal("check with conclusion+completedAt must not be still running")
	}
	if pr.Verdict != "red" {
		t.Fatalf("verdict %q", pr.Verdict)
	}
}

func TestRedWakeNoSecondCoder(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T16:00:00Z")
	rollup := []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "COMPLETED",
		"conclusion": "FAILURE", "completedAt": "2026-11-15T16:05:00Z", "workflowName": "tests",
	}}
	var wakes int
	wake := func(context.Context, *sql.DB, stuckpr.TaskRow, *stuckpr.PRView) error { wakes++; return nil }
	d := stuckpr.Deps{
		DB: db, Now: fixedNow("2026-11-15T17:00:00Z"),
		ListTasks: func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{task}, nil },
		ViewPR:    viewFrom(rollup),
		WakeCoder: wake,
		Escalate:  stuckpr.DefaultEscalate,
		LastSignal: func(context.Context, *sql.DB, stuckpr.TaskRow) (float64, error) {
			return fixedNow("2026-11-15T16:55:00Z")()
		},
	}
	acts, err := stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 1 || acts[0].Kind != "wake" || wakes != 1 {
		t.Fatalf("first run acts=%v wakes=%d err=%v", acts, wakes, err)
	}
	_, err = db.Exec(`INSERT INTO agents(name,role,status,task_id) VALUES('cursor-coder-t-1552','coder','live','T-1552')`)
	if err != nil {
		t.Fatal(err)
	}
	acts, err = stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 0 || wakes != 1 {
		t.Fatalf("second run acts=%v wakes=%d err=%v", acts, wakes, err)
	}
}

func TestStaleEscalateOnce(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T15:00:00Z")
	rollup := []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "COMPLETED",
		"conclusion": "SUCCESS", "completedAt": "2026-11-15T15:05:00Z", "workflowName": "tests",
	}}
	d := stuckpr.Deps{
		DB: db, Now: fixedNow("2026-11-15T16:00:00Z"),
		ListTasks: func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{task}, nil },
		ViewPR:    viewFrom(rollup),
		Escalate: func(ctx context.Context, db *sql.DB, task stuckpr.TaskRow, pr *stuckpr.PRView, age time.Duration) error {
			return stuckpr.DefaultEscalate(ctx, db, task, pr, age)
		},
		LastSignal: func(context.Context, *sql.DB, stuckpr.TaskRow) (float64, error) {
			return fixedNow("2026-11-15T15:00:00Z")()
		},
	}
	acts, err := stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 1 || acts[0].Kind != "escalate" {
		t.Fatalf("first acts=%v err=%v", acts, err)
	}
	var subj, body string
	if err := db.QueryRow(`SELECT subject, body FROM messages ORDER BY id DESC LIMIT 1`).Scan(&subj, &body); err != nil {
		t.Fatal(err)
	}
	combined := subj + "\n" + body
	for _, want := range []string{"T-1552", "hivemill", "conflict no", "green"} {
		if !strings.Contains(combined, want) {
			t.Errorf("missing %q in %q / %q", want, subj, body)
		}
	}
	if !strings.Contains(body, "age ") || !strings.Contains(body, "conflict") {
		t.Fatalf("body %q", body)
	}
	acts, err = stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 0 {
		t.Fatalf("second acts=%v err=%v", acts, err)
	}
}

func TestNoForbiddenGitHubActions(t *testing.T) {
	for _, name := range []string{"stuckpr.go", "checks.go", "../tick/stuckpr.go"} {
		b, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, banned := range []string{"checks --watch", "pr merge", "pr close", "push --force", "gh pr edit"} {
			if strings.Contains(src, banned) {
				t.Errorf("%s: forbidden substring %q", name, banned)
			}
		}
	}
}

func FuzzParsePRView(f *testing.F) {
	f.Add("IN_PROGRESS", "FAILURE", "2026-11-15T16:00:00Z")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, status, concl, completed string) {
		m := map[string]any{
			"__typename": "CheckRun", "name": "ci", "workflowName": "tests",
			"status": status, "conclusion": concl, "completedAt": completed,
		}
		pr, err := stuckpr.ParsePRView(map[string]any{
			"state": "OPEN", "headRefOid": "a", "statusCheckRollup": []any{m},
		})
		if err != nil {
			t.Skip()
		}
		if concl != "" && completed != "" && pr.StillRunning {
			t.Fatalf("conclusion %q + completedAt %q must be finished", concl, completed)
		}
	})
}

func greenRollup() []any {
	return []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "COMPLETED",
		"conclusion": "SUCCESS", "completedAt": "2026-11-15T15:05:00Z", "workflowName": "tests",
	}}
}

func viewWith(mergeable, commit string) func(context.Context, string) (*stuckpr.PRView, error) {
	return func(context.Context, string) (*stuckpr.PRView, error) {
		m := map[string]any{
			"number": float64(1), "state": "OPEN", "headRefOid": strings.Repeat("a", 40),
			"mergeable": mergeable, "url": "https://github.com/volekerb/hivemill/pull/1",
			"statusCheckRollup": greenRollup(),
			"commits":           []any{map[string]any{"committedDate": commit}},
		}
		return stuckpr.ParsePRView(m)
	}
}

func staleDeps(db *sql.DB, task stuckpr.TaskRow, view func(context.Context, string) (*stuckpr.PRView, error), now string) stuckpr.Deps {
	return stuckpr.Deps{
		DB: db, Now: fixedNow(now), ViewPR: view, Escalate: stuckpr.DefaultEscalate,
		ListTasks: func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{task}, nil },
		// a fresh task event must not hide an old commit
		LastSignal: func(context.Context, *sql.DB, stuckpr.TaskRow) (float64, error) { return fixedNow(now)() },
	}
}

func TestStaleAgeIsTimeSinceLastCommit(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T16:00:00Z")
	// old commit, fresh event: escalates
	acts, err := stuckpr.Run(context.Background(), staleDeps(db, task, viewWith("MERGEABLE", "2026-11-15T15:00:00Z"), "2026-11-15T16:00:00Z"))
	if err != nil || len(acts) != 1 || acts[0].Kind != "escalate" {
		t.Fatalf("old commit acts=%v err=%v", acts, err)
	}
	// fresh commit: nothing, whatever the event age
	db2 := testDB(t)
	task2 := seedTask(t, db2, "T-1552", "2026-11-15T10:00:00Z")
	d := staleDeps(db2, task2, viewWith("MERGEABLE", "2026-11-15T15:50:00Z"), "2026-11-15T16:00:00Z")
	d.LastSignal = func(context.Context, *sql.DB, stuckpr.TaskRow) (float64, error) {
		return fixedNow("2026-11-15T10:00:00Z")()
	}
	acts, err = stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 0 {
		t.Fatalf("fresh commit acts=%v err=%v", acts, err)
	}
}

func TestUnknownMergeableDefersThenReportsUnknown(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T15:00:00Z")
	d := staleDeps(db, task, viewWith("UNKNOWN", "2026-11-15T15:00:00Z"), "2026-11-15T15:40:00Z")
	acts, err := stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 0 {
		t.Fatalf("unknown at 40m should defer, acts=%v err=%v", acts, err)
	}
	d = staleDeps(db, task, viewWith("UNKNOWN", "2026-11-15T15:00:00Z"), "2026-11-15T16:05:00Z")
	acts, err = stuckpr.Run(context.Background(), d)
	if err != nil || len(acts) != 1 {
		t.Fatalf("unknown at 65m acts=%v err=%v", acts, err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM messages ORDER BY id DESC LIMIT 1`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "conflict unknown") {
		t.Fatalf("body %q", body)
	}
}

func TestConflictingReportedYes(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T15:00:00Z")
	if _, err := stuckpr.Run(context.Background(), staleDeps(db, task, viewWith("CONFLICTING", "2026-11-15T15:00:00Z"), "2026-11-15T16:00:00Z")); err != nil {
		t.Fatal(err)
	}
	var body string
	if err := db.QueryRow(`SELECT body FROM messages ORDER BY id DESC LIMIT 1`).Scan(&body); err != nil || !strings.Contains(body, "conflict yes") {
		t.Fatalf("body %q err %v", body, err)
	}
}

func TestOneBadPRDoesNotAbortScan(t *testing.T) {
	db := testDB(t)
	good := seedTask(t, db, "T-2", "2026-11-15T15:00:00Z")
	bad := good
	bad.ID, bad.PRURL = "T-1", "--help"
	d := staleDeps(db, good, viewWith("MERGEABLE", "2026-11-15T15:00:00Z"), "2026-11-15T16:00:00Z")
	d.ListTasks = func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{bad, good}, nil }
	var viewed []string
	inner := d.ViewPR
	d.ViewPR = func(ctx context.Context, u string) (*stuckpr.PRView, error) {
		viewed = append(viewed, u)
		return inner(ctx, u)
	}
	acts, err := stuckpr.Run(context.Background(), d)
	if err == nil || !strings.Contains(err.Error(), "T-1") {
		t.Fatalf("want error naming T-1, got %v", err)
	}
	if len(acts) != 1 || acts[0].TaskID != "T-2" || len(viewed) != 1 {
		t.Fatalf("acts=%v viewed=%v", acts, viewed)
	}
}

func TestValidPRURL(t *testing.T) {
	for u, want := range map[string]bool{
		"https://github.com/volekerb/hivemill/pull/714": true,
		"--help": false,
		"https://127.0.0.1/volekerb/hivemill/pull/1":              false,
		"https://github.com.invalid/volekerb/hivemill/pull/1":     false,
		"https://github.com/volekerb/hivemill/pull/1?x=https://a": false,
		"http://github.com/volekerb/hivemill/pull/1":              false,
	} {
		if got := stuckpr.ValidPRURL(u); got != want {
			t.Errorf("ValidPRURL(%q)=%v want %v", u, got, want)
		}
	}
}

func TestConfigErrorsAreNotSwallowed(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T15:00:00Z")
	d := staleDeps(db, task, viewWith("MERGEABLE", "2026-11-15T15:00:00Z"), "2026-11-15T16:00:00Z")
	d.ConfigGet = func(context.Context, *sql.DB, string) (string, bool, error) {
		return "", false, errors.New("db locked")
	}
	acts, err := stuckpr.Run(context.Background(), d)
	if err == nil || len(acts) != 0 {
		t.Fatalf("failed dedupe read must not escalate: acts=%v err=%v", acts, err)
	}
	var n int
	_ = db.QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d messages sent", n)
	}
}

func TestRedWakeOncePerHead(t *testing.T) {
	db := testDB(t)
	task := seedTask(t, db, "T-1552", "2026-11-15T16:00:00Z")
	rollup := []any{map[string]any{
		"__typename": "CheckRun", "name": "ci", "status": "COMPLETED",
		"conclusion": "FAILURE", "completedAt": "2026-11-15T16:05:00Z", "workflowName": "tests",
	}}
	var wakes int
	d := stuckpr.Deps{
		DB: db, Now: fixedNow("2026-11-15T16:10:00Z"), ViewPR: viewFrom(rollup), Escalate: stuckpr.DefaultEscalate,
		ListTasks: func(context.Context, *sql.DB) ([]stuckpr.TaskRow, error) { return []stuckpr.TaskRow{task}, nil },
		WakeCoder: func(context.Context, *sql.DB, stuckpr.TaskRow, *stuckpr.PRView) error { wakes++; return nil },
		LastSignal: func(context.Context, *sql.DB, stuckpr.TaskRow) (float64, error) {
			return fixedNow("2026-11-15T16:09:00Z")()
		},
	}
	for i := 0; i < 3; i++ {
		if _, err := stuckpr.Run(context.Background(), d); err != nil {
			t.Fatal(err)
		}
	}
	if wakes != 1 {
		t.Fatalf("wakes=%d, want 1 for the same head", wakes)
	}
}
