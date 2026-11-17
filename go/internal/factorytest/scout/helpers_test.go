package scout

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"

	_ "modernc.org/sqlite"
)

// mem is the Python mem(): an in-memory database holding the scout tables (SCOUT_DDL). The DDL is the real file
// internal/scout/testdata/scout_ddl.sql (a copy of scout.db.SCOUT_DDL); the gate tree has it, so no fallback.
func mem(t testing.TB) *sql.DB {
	t.Helper()
	root, err := ft.ModuleRoot()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "internal", "scout", "testdata", "scout_ddl.sql")
	ddl, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	d, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	d.SetMaxOpenConns(1) // an in-memory database lives in one connection
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatalf("scout DDL: %v", err)
	}
	return d
}

func exec(t testing.TB, d *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func notContains(t testing.TB, what, got, bad string) {
	t.Helper()
	if strings.Contains(got, bad) {
		t.Fatalf("%s = %q, must not contain %q", what, got, bad)
	}
}

func eqStrings(t testing.TB, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") || len(got) != len(want) {
		t.Fatalf("%s = %q, want %q", what, got, want)
	}
}
