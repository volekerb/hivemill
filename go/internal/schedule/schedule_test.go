package schedule

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	_ "modernc.org/sqlite"
)

func openSchedTestDB(t *testing.T) *sql.DB {
	t.Helper()
	home := t.TempDir()
	dir := filepath.Join(home, "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "factory.db")
	base, err := os.ReadFile(filepath.Join("..", "cli", "testdata", "golden", "base.sql"))
	if err != nil {
		t.Fatal(err)
	}
	d, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout(30000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	d.SetMaxOpenConns(1)
	for _, q := range []string{"PRAGMA journal_mode=WAL", string(base), fmt.Sprintf("PRAGMA user_version = %d", factorydb.SchemaVersion)} {
		if _, err := d.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Active claude goal for happy-path inserts.
	if _, err := d.Exec(`INSERT INTO goals(id,title,status,org,priority,created,updated) VALUES
		('G-001','g1','active','claude',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('G-002','g2','proposed','claude',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z'),
		('G-003','g3','active','cursor',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	return d
}

func schedCount(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM schedules").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestScheduleAddRefuses(t *testing.T) {
	now := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
	ok := func(t *testing.T, db *sql.DB, hour int, days, goal, job string, cap int) {
		t.Helper()
		before := schedCount(t, db)
		_, err := Add(db, now, "claude-boss", hour, days, goal, job, cap)
		if err == nil {
			t.Fatal("expected error")
		}
		if schedCount(t, db) != before {
			t.Fatalf("row count changed: was %d", before)
		}
	}
	t.Run("unknown goal", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-999", "job", 100)
	})
	t.Run("non-active goal", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-002", "job", 100)
	})
	t.Run("cursor-org goal", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-003", "job", 100)
	})
	t.Run("goal already scheduled", func(t *testing.T) {
		db := openSchedTestDB(t)
		if _, err := Add(db, now, "claude-boss", 9, "", "G-001", "first", 100); err != nil {
			t.Fatal(err)
		}
		ok(t, db, 10, "", "G-001", "second", 100)
	})
	t.Run("6th active job", func(t *testing.T) {
		db := openSchedTestDB(t)
		for i := range 5 {
			gid := fmt.Sprintf("G-1%02d", i)
			if _, err := db.Exec(`INSERT INTO goals(id,title,status,org,priority,created,updated) VALUES
				(?,?, 'active','claude',1,'2026-01-01T00:00:00Z','2026-01-01T00:00:00Z')`, gid, gid); err != nil {
				t.Fatal(err)
			}
			if _, err := Add(db, now, "claude-boss", 9, "", gid, "j", 100); err != nil {
				t.Fatal(err)
			}
		}
		ok(t, db, 9, "", "G-001", "one more", 100)
	})
	t.Run("hour 24", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 24, "", "G-001", "job", 100)
	})
	t.Run("days funday", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "funday", "G-001", "job", 100)
	})
	t.Run("empty job", func(t *testing.T) {
		db := openSchedTestDB(t)
		_, err := Add(db, now, "claude-boss", 9, "", "G-001", "", 100)
		if err == nil || schedCount(t, db) != 0 {
			t.Fatalf("empty job: %v", err)
		}
	})
	t.Run("501-char job", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-001", strings.Repeat("x", 501), 100)
	})
	t.Run("job with newline", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-001", "a\nb", 100)
	})
	t.Run("job with control", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-001", "a\x07b", 100)
	})
	t.Run("job with delimiters", func(t *testing.T) {
		db := openSchedTestDB(t)
		ok(t, db, 9, "", "G-001", "has <<< here", 100)
	})
}
