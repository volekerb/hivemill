package factorydb_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
)

func TestResolvePath(t *testing.T) {
	home := t.TempDir()
	if got := factorydb.ResolvePath(home); got != filepath.Join(home, "db", "factory.db") {
		t.Fatalf("nothing exists yet: %s", got)
	}
	os.WriteFile(filepath.Join(home, "factory.db"), nil, 0o600)
	if got := factorydb.ResolvePath(home); got != filepath.Join(home, "factory.db") {
		t.Fatalf("legacy only: %s", got)
	}
	os.MkdirAll(filepath.Join(home, "db"), 0o700)
	os.WriteFile(filepath.Join(home, "db", "factory.db"), nil, 0o600)
	if got := factorydb.ResolvePath(home); got != filepath.Join(home, "db", "factory.db") {
		t.Fatalf("both: %s", got)
	}
}

func TestOpenNeverCreates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "db", "factory.db")
	if _, err := factorydb.Open(p); err == nil {
		t.Fatal("a missing database must be an error")
	}
	if _, err := os.Stat(p); err == nil {
		t.Fatal("Open created the file")
	}
}

func TestOpenPragmasAndVersion(t *testing.T) {
	p := dbtest.New(t, t.TempDir(), factorydb.SchemaVersion)
	d, err := factorydb.Open(p)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	var jm string
	var bt, fk int
	d.QueryRow("PRAGMA journal_mode").Scan(&jm)
	d.QueryRow("PRAGMA busy_timeout").Scan(&bt)
	d.QueryRow("PRAGMA foreign_keys").Scan(&fk)
	if jm != "wal" || bt != 30000 || fk != 1 {
		t.Fatalf("pragmas journal_mode=%s busy_timeout=%d foreign_keys=%d", jm, bt, fk)
	}
	p2 := dbtest.New(t, t.TempDir(), factorydb.SchemaVersion+1)
	if _, err := factorydb.Open(p2); !errors.Is(err, factorydb.ErrSchemaMismatch) {
		t.Fatalf("want ErrSchemaMismatch, got %v", err)
	}
}
