package data

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"factory/go/internal/factorydb/dbtest"
)

// The goldens were produced with TZ=UTC; naive timestamps and local days therefore mean UTC here.
func TestMain(m *testing.M) {
	Loc = time.UTC
	os.Exit(m.Run())
}

// testNow is "now" of every time-dependent golden: 2026-11-14T12:00:00+01:00.
var testNow = time.Date(2026, 11, 14, 12, 0, 0, 0, time.FixedZone("", 3600))

// newFixture builds <tmp>/db/factory.db (WAL, like a live one) from testdata/fixture.sql and returns the home and DB path.
func newFixture(t testing.TB) (home, dbPath string) {
	t.Helper()
	home = t.TempDir()
	dbPath = dbtest.New(t, home, 2)
	sqlText, err := os.ReadFile(filepath.Join("testdata", "fixture.sql"))
	if err != nil {
		t.Fatal(err)
	}
	d := dbtest.Raw(t, dbPath)
	defer d.Close()
	if _, err := d.Exec(string(sqlText)); err != nil {
		t.Fatalf("fixture.sql: %v", err)
	}
	return home, dbPath
}

func openFixture(t testing.TB) *Conn {
	t.Helper()
	_, p := newFixture(t)
	c, err := Connect(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func projectsRoot() string { return filepath.Join("testdata", "projects") }
