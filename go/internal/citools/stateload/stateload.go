// Package stateload builds a temp factory DB from a state/ directory (port of ci/state_load.py).
//
//	FACTORY_HOME=<tmp> ci-state-load <state-dir>
//
// <state-dir> holds factory.sql (schema and rows, no events) and events.jsonl (one event per line). The DB is
// written to $FACTORY_HOME/db/factory.db. Refuses: FACTORY_HOME unset, FACTORY_HOME is a live factory home, a DB
// already exists there. Prints "loaded <path>" and the row count per table. Exit 0 ok, 1 refusal or failure, 2 usage.
package stateload

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"factory/go/internal/citools/freshexport"
	_ "modernc.org/sqlite"
)

// realpath resolves symlinks of the longest existing prefix of p, like Python's os.path.realpath.
func realpath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	cur := abs
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// LiveHomes returns the live factory homes: <base>/factory plus the one absolute line of <base>/.config/factory/live_home.
func LiveHomes(base string) map[string]bool {
	homes := map[string]bool{realpath(filepath.Join(base, "factory")): true}
	data, err := os.ReadFile(filepath.Join(base, ".config", "factory", "live_home"))
	if err != nil {
		return homes
	}
	if live, ok := freshexport.LiveHomeLine(data); ok {
		homes[realpath(live)] = true
	}
	return homes
}

// CheckHome returns the resolved home, or an error text when envHome may not be loaded into.
func CheckHome(envHome string, homes map[string]bool) (string, string) {
	if envHome == "" {
		return "", "FACTORY_HOME is unset or empty: set it to a fresh temp dir"
	}
	home := realpath(envHome)
	if homes[home] {
		return "", fmt.Sprintf("FACTORY_HOME %s is a live factory home: use a fresh temp dir", home)
	}
	for _, name := range []string{"db/factory.db", "factory.db"} {
		p := filepath.Join(home, name)
		if _, err := os.Lstat(p); err == nil {
			return "", fmt.Sprintf("%s already exists: refusing to overwrite; use a fresh FACTORY_HOME", p)
		}
	}
	return home, ""
}

// Count is one table and its row count, in table-name order.
type Count struct {
	Table string
	Rows  int
}

// Load creates home/db/factory.db from stateDir and returns the row count per table.
func Load(home, stateDir string) ([]Count, error) {
	sqlPath, jsonlPath := filepath.Join(stateDir, "factory.sql"), filepath.Join(stateDir, "events.jsonl")
	for _, p := range []string{sqlPath, jsonlPath} {
		if st, err := os.Stat(p); err != nil || !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%s not found: pass the state/ directory holding factory.sql and events.jsonl", p)
		}
	}
	jsonl, err := freshexport.ReadText(jsonlPath)
	if err != nil {
		return nil, err
	}
	var events []map[string]any
	for n, line := range freshexport.SplitLines(jsonl) {
		if strings.TrimFunc(line, func(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }) == "" {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(line))
		dec.UseNumber()
		var e map[string]any
		if err := dec.Decode(&e); err != nil || e == nil {
			return nil, fmt.Errorf("events.jsonl line %d: not a JSON object (%v)", n+1, err)
		}
		if _, err := dec.Token(); !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("events.jsonl line %d: trailing data after the JSON object", n+1)
		}
		events = append(events, e)
	}
	schema, err := freshexport.ReadText(sqlPath)
	if err != nil {
		return nil, err
	}
	dbDir := filepath.Join(home, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		return nil, err
	}
	final, tmp := filepath.Join(dbDir, "factory.db"), filepath.Join(dbDir, "factory.db.tmp")
	if err := os.Remove(tmp); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	counts, err := build(tmp, schema, events)
	if err != nil {
		os.Remove(tmp)
		return nil, fmt.Errorf("load failed (%v); nothing was written", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, err
	}
	return counts, nil
}

func build(tmp, schema string, events []map[string]any) ([]Count, error) {
	db, err := sql.Open("sqlite", "file:"+tmp)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		return nil, err
	}
	valid := map[string]bool{}
	rows, err := db.Query("PRAGMA table_info(events)")
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var cid, notnull, pk int
		var name, typ string
		var dflt any
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			rows.Close()
			return nil, err
		}
		valid[name] = true
	}
	rows.Close()
	var allowed []string
	for c := range valid {
		allowed = append(allowed, c)
	}
	sort.Strings(allowed)
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	for n, e := range events {
		if len(e) == 0 {
			tx.Rollback()
			return nil, fmt.Errorf(`events.jsonl line %d: empty event {}; every event needs at least one column`, n+1)
		}
		cols := make([]string, 0, len(e))
		for c := range e {
			cols = append(cols, c)
		}
		sort.Strings(cols)
		args := make([]any, 0, len(cols))
		for _, c := range cols {
			if !valid[c] {
				tx.Rollback()
				return nil, fmt.Errorf("events.jsonl line %d: unknown column %q; allowed: %s", n+1, c, strings.Join(allowed, ", "))
			}
			v, err := scalar(e[c])
			if err != nil {
				tx.Rollback()
				return nil, fmt.Errorf("events.jsonl event %d column %q: %v", n+1, c, err)
			}
			args = append(args, v)
		}
		q := fmt.Sprintf("INSERT INTO events (%s) VALUES (%s)", strings.Join(cols, ","), strings.TrimSuffix(strings.Repeat("?,", len(cols)), ","))
		if _, err := tx.Exec(q, args...); err != nil {
			tx.Rollback()
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	trows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		return nil, err
	}
	var tables []string
	for trows.Next() {
		var t string
		if err := trows.Scan(&t); err != nil {
			trows.Close()
			return nil, err
		}
		tables = append(tables, t)
	}
	trows.Close()
	var out []Count
	for _, t := range tables {
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM "` + strings.ReplaceAll(t, `"`, `""`) + `"`).Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, Count{t, n})
	}
	return out, nil
}

// scalar converts a decoded JSON value to a SQLite value; arrays and objects cannot be stored.
func scalar(v any) (any, error) {
	switch x := v.(type) {
	case nil, string:
		return x, nil
	case bool:
		if x {
			return int64(1), nil
		}
		return int64(0), nil
	case json.Number:
		if i, err := x.Int64(); err == nil {
			return i, nil
		}
		f, err := x.Float64()
		return f, err
	}
	return nil, errors.New("arrays and objects cannot be stored in an events column")
}

// Main runs the tool; it returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if len(args) != 1 {
		fmt.Fprintln(stderr, "usage: FACTORY_HOME=<tmp> ci-state-load <state-dir>")
		return 2
	}
	home, msg := CheckHome(os.Getenv("FACTORY_HOME"), LiveHomes(freshexport.HomeBase()))
	if msg != "" {
		fmt.Fprintf(stderr, "state_load: %s\n", msg)
		return 1
	}
	counts, err := Load(home, args[0])
	if err != nil {
		fmt.Fprintf(stderr, "state_load: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "loaded %s\n", filepath.Join(home, "db", "factory.db"))
	for _, c := range counts {
		fmt.Fprintf(stdout, "%s\t%d\n", c.Table, c.Rows)
	}
	return 0
}
