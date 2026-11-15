package scout

import (
	"database/sql"
	"os"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
)

func init() { localLoc = time.UTC } // goldens were generated under TZ=UTC; naive stamps are local time

// newDB builds a throw-away factory DB (tests are the only place that runs DDL; Python owns SCOUT_DDL in production)
// with the scout tables from testdata/scout_ddl.sql, a copy of scout.db.SCOUT_DDL.
func newDB(t testing.TB) *sql.DB {
	t.Helper()
	home := t.TempDir()
	path := dbtest.New(t, home, factorydb.SchemaVersion)
	d, err := factorydb.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	ddl, err := os.ReadFile("testdata/scout_ddl.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.Exec(string(ddl)); err != nil {
		t.Fatal(err)
	}
	return d.DB
}

func mustExec(t testing.TB, db *sql.DB, q string, args ...any) {
	t.Helper()
	if _, err := db.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}
