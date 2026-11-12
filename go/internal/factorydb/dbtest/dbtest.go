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

	_ "modernc.org/sqlite"
)

// Schema is the subset of bin/factory_cli.py SCHEMA the read commands touch (same column names and order).
const Schema = `
CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE agents (
  name TEXT PRIMARY KEY, role TEXT NOT NULL, goal_id TEXT, team_id TEXT, task_id TEXT,
  status TEXT NOT NULL,
  pane_id TEXT, workspace_id TEXT, tab_id TEXT, model TEXT, session_id TEXT, cwd TEXT,
  spawn_args TEXT, summary TEXT, last_nudge REAL, blocked_since REAL, blocked_alerted INTEGER DEFAULT 0,
  queued_at TEXT, started TEXT, ended TEXT,
  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT,
  stop_requested_at TEXT, stop_attempt_at TEXT, stop_failed_kind TEXT,
  relaunch_requested_at TEXT, relaunch_requested_by TEXT, relaunch_fresh INTEGER NOT NULL DEFAULT 0,
  start_pending REAL, start_pending_info TEXT, org TEXT);
CREATE TABLE events (
  id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, actor TEXT, entity TEXT,
  entity_id TEXT, action TEXT NOT NULL, detail TEXT);
`

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
