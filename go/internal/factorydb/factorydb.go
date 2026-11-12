// Package factorydb opens the existing factory SQLite database for the Go CLI.
//
// It never creates a file and never runs DDL: only `factory init` (Python) creates the database and only Python
// migrates it. A database whose PRAGMA user_version is not SchemaVersion belongs to a different schema than this
// binary was written for, so Open returns ErrSchemaMismatch and the caller hands the command to Python.
package factorydb

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (G-041 D1, owner M32349); no cgo
)

// SchemaVersion is the PRAGMA user_version this binary understands. It must equal SCHEMA_VERSION in
// bin/factory_cli.py (added by S1-c); until Python stamps it every database reports 0 and Go defers to Python.
const SchemaVersion = 1

// ErrSchemaMismatch means the database user_version differs from SchemaVersion.
var ErrSchemaMismatch = errors.New("factorydb: user_version does not match SchemaVersion")

// ResolvePath is web/dbpath.py resolve(): <home>/db/factory.db, else the pre-migration <home>/factory.db when only
// that one exists. Only the home decides.
func ResolvePath(home string) string {
	newp := filepath.Join(home, "db", "factory.db")
	legacy := filepath.Join(home, "factory.db")
	if exists(newp) || !exists(legacy) {
		return newp
	}
	return legacy
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// DB is an open connection pool to the factory database.
type DB struct {
	*sql.DB
	Path string
}

// Open opens the existing database at path read-write (mode=rw: a missing file is an error, never created) with
// journal_mode=WAL, busy_timeout=30000 and foreign_keys=ON, then checks user_version.
func Open(path string) (*DB, error) {
	if _, err := os.Stat(path); err != nil {
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}
	q := url.Values{}
	q.Set("mode", "rw")
	q.Add("_pragma", "busy_timeout(30000)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "foreign_keys(1)")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("cannot open database %s: %w", path, err)
	}
	// One connection: the CLI is a short single-threaded process and a pool would only add WAL readers.
	sdb.SetMaxOpenConns(1)
	var v int
	if err := sdb.QueryRow("PRAGMA user_version").Scan(&v); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("cannot read user_version of %s: %w", path, err)
	}
	if v != SchemaVersion {
		sdb.Close()
		return nil, fmt.Errorf("%w: database has %d, binary expects %d", ErrSchemaMismatch, v, SchemaVersion)
	}
	return &DB{DB: sdb, Path: path}, nil
}

// DataVersion returns PRAGMA data_version (changes when another connection commits).
func (d *DB) DataVersion() (int64, error) {
	var v int64
	err := d.QueryRow("PRAGMA data_version").Scan(&v)
	return v, err
}
