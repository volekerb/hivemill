// Package dbtest builds throw-away factory databases for tests. It is the only place that runs DDL, and only on a
// file the test just created; production code (package factorydb) never does.
package dbtest

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"factory/go/internal/factorydb"
	_ "modernc.org/sqlite"
)

// Schema is factorydb.Schema (the tables and columns Go requires), shared so the two cannot drift.
const Schema = factorydb.Schema

// New creates <home>/db/factory.db with Schema and PRAGMA user_version = version, and returns its path.
func New(t testing.TB, home string, version int) string {
	t.Helper()
	dir := filepath.Join(home, "db")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "factory.db")
	d := Raw(t, path)
	defer d.Close()
	if _, err := d.Exec("PRAGMA journal_mode=WAL"); err != nil { // a live factory.db is always WAL
		t.Fatal(err)
	}
	if _, err := d.Exec(Schema); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		t.Fatal(err)
	}
	return path
}

// Raw opens (creating if needed) a database for direct test setup and inspection.
func Raw(t testing.TB, path string) *sql.DB {
	t.Helper()
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: "_pragma=busy_timeout(30000)"}).String()
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1)
	return d
}
