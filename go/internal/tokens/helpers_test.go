package tokens

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite" // same pure-Go driver as factorydb
)

// fixedNow is the clock every test runs on (C2: no wall clock); the golden file was made with TZ=UTC and
// now = 2026-12-01T00:00:00+00:00.
var fixedNow = time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)

// setup pins the clock and the local zone to UTC for one test.
func setup(t *testing.T) {
	t.Helper()
	oldNow, oldZone := Now, zone
	Now, zone = func() time.Time { return fixedNow }, time.UTC
	t.Cleanup(func() { Now, zone = oldNow, oldZone })
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// openDB opens a fresh SQLite database (a file in t.TempDir) with testdata/schema.sql and, when seed, testdata/seed.sql.
func openDB(t *testing.T, seed bool) (*sql.DB, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "factory.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	if _, err := db.Exec(readFile(t, "testdata/schema.sql")); err != nil {
		t.Fatal(err)
	}
	if seed {
		if _, err := db.Exec(readFile(t, "testdata/seed.sql")); err != nil {
			t.Fatal(err)
		}
	}
	return db, path
}

func writeLines(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var s string
	for _, l := range lines {
		s += l + "\n"
	}
	if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
	future := fixedNow.AddDate(10, 0, 0) // never idle (see copyFixtures)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}

func openDBAt(t *testing.T, path string) (*sql.DB, error) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, nil
}

// copyFixtures copies testdata/projects and testdata/workers into a temp dir and gives every file a modification time
// ten years after fixedNow: a file is never "idle" for the dedupe-key drop, whatever the clock (git checkouts have
// arbitrary mtimes, and Python judges idleness by the real clock).
func copyFixtures(t *testing.T) (projects, workers string) {
	t.Helper()
	dst := t.TempDir()
	future := fixedNow.AddDate(10, 0, 0)
	for _, sub := range []string{"projects", "workers"} {
		err := filepath.WalkDir(filepath.Join("testdata", sub), func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel("testdata", p)
			out := filepath.Join(dst, rel)
			if d.IsDir() {
				return os.MkdirAll(out, 0o755)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if err := os.WriteFile(out, b, 0o644); err != nil {
				return err
			}
			return os.Chtimes(out, future, future)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(dst, "projects"), filepath.Join(dst, "workers")
}

// touchFuture sets a modification time that is never idle for the dedupe-key drop (see copyFixtures).
func touchFuture(t *testing.T, path string) {
	t.Helper()
	future := fixedNow.AddDate(10, 0, 0)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatal(err)
	}
}
