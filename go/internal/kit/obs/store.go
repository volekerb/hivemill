// Package obs builds and reads state/obs.db, the factory's observability store (docs/obs.md). Ingest reads Claude Code
// transcripts incrementally with the hm-spend parser and price table, joins each session to its agent, task, goal,
// project and PR from factory.db (opened read-only), and stores pr-flow snapshots. The API side answers the Spend
// and Flow pages of the web UI as JSON.
//
// Privacy: obs.db holds only numbers, ids, timestamps, model names, tool and skill names, tool result sizes and Bash
// command heads from a fixed allowlist, plus each transcript file's own location as the ingest cursor. Message text,
// prompts, tool input and output, file contents and the paths a tool touched are never stored; a session's kickoff
// message is matched for its role and agent name, then dropped.
package obs

import (
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, same as package factorydb
)

// SchemaVersion is stored in meta.schema; a different value rebuilds the store from scratch (it is derived data).
const SchemaVersion = "1"

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS files (
  path TEXT PRIMARY KEY, ino INTEGER NOT NULL, size INTEGER NOT NULL, mtime_ms INTEGER NOT NULL,
  off INTEGER NOT NULL, session TEXT NOT NULL, parent TEXT, role TEXT NOT NULL DEFAULT '',
  kickoff_agent TEXT NOT NULL DEFAULT '', agent_name TEXT NOT NULL DEFAULT '', wt_goal TEXT NOT NULL DEFAULT '',
  wt_task TEXT NOT NULL DEFAULT '', users_seen INTEGER NOT NULL DEFAULT 0, first_done INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS requests (
  key TEXT PRIMARY KEY, session TEXT NOT NULL, ts_ms INTEGER NOT NULL, model TEXT NOT NULL, speed TEXT NOT NULL,
  input INTEGER NOT NULL, output INTEGER NOT NULL, cache_read INTEGER NOT NULL, cache_write_5m INTEGER NOT NULL,
  cache_write_1h INTEGER NOT NULL, priced INTEGER NOT NULL, cost REAL NOT NULL, cache_write_cost REAL NOT NULL,
  spawn REAL NOT NULL, polling REAL NOT NULL, work REAL NOT NULL);
CREATE INDEX IF NOT EXISTS requests_ts ON requests(ts_ms);
CREATE INDEX IF NOT EXISTS requests_session ON requests(session);
CREATE TABLE IF NOT EXISTS tool_calls (
  id TEXT PRIMARY KEY, session TEXT NOT NULL, ts_ms INTEGER NOT NULL, tool TEXT NOT NULL,
  skill TEXT NOT NULL DEFAULT '', head TEXT NOT NULL DEFAULT '', result_bytes INTEGER);
CREATE INDEX IF NOT EXISTS tool_calls_ts ON tool_calls(ts_ms);
CREATE TABLE IF NOT EXISTS sessions (
  session TEXT PRIMARY KEY, parent TEXT, role TEXT NOT NULL, agent TEXT NOT NULL DEFAULT '',
  task TEXT NOT NULL DEFAULT '', goal TEXT NOT NULL DEFAULT '', project TEXT NOT NULL DEFAULT '',
  pr TEXT NOT NULL DEFAULT '', via TEXT NOT NULL, first_ms INTEGER, last_ms INTEGER);
CREATE TABLE IF NOT EXISTS flow (
  repo TEXT NOT NULL, project TEXT NOT NULL, hours INTEGER NOT NULL, at_ms INTEGER NOT NULL, report TEXT NOT NULL,
  PRIMARY KEY (repo, hours));
`

// Paths are the files one factory home uses.
type Paths struct {
	Home, FactoryDB, Events, Store, Lock string
}

// PathsFor returns the paths under factory home dir home.
func PathsFor(home string) Paths {
	return Paths{
		Home:      home,
		FactoryDB: filepath.Join(home, "db", "factory.db"),
		Events:    filepath.Join(home, "state", "events.jsonl"),
		Store:     filepath.Join(home, "state", "obs.db"),
		Lock:      filepath.Join(home, "state", "obs.lock"),
	}
}

func dsn(path string, ro bool) string {
	// A relative path would be read as the URI authority: make it absolute.
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	if ro {
		q.Set("mode", "ro")
		q.Add("_pragma", "query_only(1)")
	} else {
		q.Add("_pragma", "journal_mode(WAL)")
		q.Add("_pragma", "synchronous(NORMAL)")
	}
	return (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
}

// openStore opens (creating if needed) obs.db read-write with the current schema.
func openStore(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("obs: create %s: %w", filepath.Dir(path), err)
	}
	db, err := sql.Open("sqlite", dsn(path, false))
	if err != nil {
		return nil, fmt.Errorf("obs: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	var v string
	err = db.QueryRow(`SELECT value FROM meta WHERE key='schema'`).Scan(&v)
	if err == nil && v != SchemaVersion {
		for _, t := range []string{"meta", "files", "requests", "tool_calls", "sessions", "flow"} {
			if _, err := db.Exec("DROP TABLE IF EXISTS " + t); err != nil {
				db.Close()
				return nil, fmt.Errorf("obs: reset %s: %w", path, err)
			}
		}
	}
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("obs: create schema in %s: %w", path, err)
	}
	if _, err := db.Exec(`INSERT OR REPLACE INTO meta(key, value) VALUES('schema', ?)`, SchemaVersion); err != nil {
		db.Close()
		return nil, fmt.Errorf("obs: write schema version: %w", err)
	}
	return db, nil
}

// openRO opens an existing SQLite file read-only; a missing file is an error that says what to run.
func openRO(path, hint string) (*sql.DB, error) {
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("obs: %s not found (%s)", path, hint)
	}
	db, err := sql.Open("sqlite", dsn(path, true))
	if err != nil {
		return nil, fmt.Errorf("obs: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("obs: open %s: %w", path, err)
	}
	return db, nil
}

func metaGet(db *sql.DB, key string) string {
	var v string
	if db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v) != nil {
		return ""
	}
	return v
}
