// Command ci-pre-push-public-text is the Go port of ci/pre_push_public_text.py (the git pre-push hook body); the logic
// lives in factory/go/internal/guard. This main owns the one thing the guard package must not import: sqlite.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver

	"factory/go/internal/guard"
)

// readConfig is pre_push_public_text.read_config: the two config keys, read-only, busy timeout 5 s.
func readConfig(home string) (map[string]string, error) {
	db := filepath.Join(home, "db", "factory.db")
	if _, err := os.Stat(db); err != nil {
		return nil, fmt.Errorf("factory db: %w", err)
	}
	dsn := "file:" + (&url.URL{Path: db}).EscapedPath() + "?mode=ro&_pragma=busy_timeout(5000)"
	con, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open factory db: %w", err)
	}
	out, err := queryConfig(con)
	if cerr := con.Close(); err == nil && cerr != nil {
		err = fmt.Errorf("close factory db: %w", cerr)
	}
	return out, err
}

func queryConfig(con *sql.DB) (map[string]string, error) {
	rows, err := con.Query("SELECT key, value FROM config WHERE key IN ('public_text_repos','scrub_terms_file')")
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	out := map[string]string{}
	for rows.Next() {
		var k string
		var v sql.NullString
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan config: %w", errors.Join(err, rows.Close()))
		}
		out[k] = v.String
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read config: %w", errors.Join(err, rows.Close()))
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close config rows: %w", err)
	}
	return out, nil
}

func main() {
	os.Exit(guard.CIPrePushPublicTextMain(os.Args[1:], os.Stdin, os.Stderr, readConfig))
}
