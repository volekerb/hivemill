package data

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // pure-Go SQLite driver, same as package factorydb
)

// ErrNoDB means the factory database file does not exist; Connect never creates it.
var ErrNoDB = errors.New("factory database not found")

// Conn is a read-only connection to factory.db (web/db.py connect): SQLite mode=ro and PRAGMA query_only=ON, so a
// write statement fails even if one were ever added by mistake.
type Conn struct {
	*sql.DB
	Path string
}

// Connect opens dbPath read-only. A missing file is ErrNoDB and nothing is created (web/db.py connect).
// It does not use factorydb.Open, which opens read-write and may switch the journal mode.
func Connect(dbPath string) (*Conn, error) {
	path, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("data: cannot resolve %s: %w", dbPath, err)
	}
	st, err := os.Stat(path)
	if err != nil || !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: %s", ErrNoDB, path)
	}
	q := url.Values{}
	q.Set("mode", "ro")
	q.Add("_pragma", "busy_timeout(2000)")
	q.Add("_pragma", "query_only(1)")
	dsn := (&url.URL{Scheme: "file", Path: path, RawQuery: q.Encode()}).String()
	sdb, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("data: cannot open %s: %w", path, err)
	}
	sdb.SetMaxOpenConns(1)
	if err := sdb.Ping(); err != nil {
		sdb.Close()
		return nil, fmt.Errorf("data: cannot open %s: %w", path, err)
	}
	return &Conn{DB: sdb, Path: path}, nil
}

// Reader is web/db.py reader: open, run fn, always close.
func Reader(dbPath string, fn func(*Conn) error) error {
	c, err := Connect(dbPath)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

// HasColumn is web/db.py has_column: true if table has column col (tolerates older databases).
func (c *Conn) HasColumn(table, col string) (bool, error) {
	rows, err := c.Query(fmt.Sprintf("PRAGMA table_info(%q)", table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
}

// DataVersion is PRAGMA data_version.
func (c *Conn) DataVersion() (int64, error) {
	var v int64
	err := c.QueryRow("PRAGMA data_version").Scan(&v)
	return v, err
}
