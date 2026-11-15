package stuckpr_test

import (
	"context"
	"database/sql"
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
	for _, name := range []string{"stuckpr.go", "checks.go"} {
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
		_, err := stuckpr.ParsePRView(map[string]any{
			"state": "OPEN", "headRefOid": "a", "statusCheckRollup": []any{m},
		})
		if err != nil {
			t.Skip()
		}
	})
}
