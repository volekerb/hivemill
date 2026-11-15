package data

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/factorydb"
	"factory/go/internal/factorydb/dbtest"
)

func stamp(t *testing.T, p string) (int64, time.Time, bool) {
	t.Helper()
	st, err := os.Stat(p)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, time.Time{}, false
		}
		t.Fatal(err)
	}
	return st.Size(), st.ModTime(), true
}

// TestNoWrites runs every query function on the fixture and shows the database was not written: PRAGMA
// data_version (as seen by a second connection) and the mtime and size of the db and WAL files are unchanged, and
// a write statement on the web connection is refused.
func TestNoWrites(t *testing.T) {
	_, p := newFixture(t)
	mon := dbtest.Raw(t, p)
	defer mon.Close()
	dv := func() int64 {
		var v int64
		if err := mon.QueryRow("PRAGMA data_version").Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	type st struct {
		size int64
		mod  time.Time
		ok   bool
	}
	snap := func() map[string]st {
		m := map[string]st{}
		for _, f := range []string{p, p + "-wal"} {
			s, mt, ok := stamp(t, f)
			m[f] = st{s, mt, ok}
		}
		return m
	}
	beforeV, beforeF := dv(), snap()
	time.Sleep(20 * time.Millisecond) // a write would move the mtime past this point

	c, err := Connect(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, gc := range goldenCases() {
		if _, err := gc.run(c); err != nil {
			t.Fatalf("%s: %v", gc.name, err)
		}
	}
	for _, stmt := range []string{
		"INSERT INTO config(key,value) VALUES ('x','y')",
		"UPDATE tasks SET status='done'",
		"DELETE FROM events",
		"CREATE TABLE zz (a)",
		"PRAGMA user_version = 9",
	} {
		if _, err := c.Exec(stmt); err == nil {
			t.Errorf("write statement %q succeeded on the read-only connection", stmt)
		}
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if afterV := dv(); afterV != beforeV {
		t.Errorf("PRAGMA data_version moved from %d to %d: something committed", beforeV, afterV)
	}
	afterF := snap()
	for f, b := range beforeF {
		if a := afterF[f]; a != b {
			t.Errorf("%s changed: before %+v, after %+v", f, b, a)
		}
	}
	var n int
	if err := mon.QueryRow("SELECT COUNT(*) FROM config").Scan(&n); err != nil || n != 4 {
		t.Errorf("config rows = %d (err %v), want the 4 seeded rows", n, err)
	}
}

func TestConnectMissingFileCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db", "factory.db")
	_, err := Connect(p)
	if !errors.Is(err, ErrNoDB) {
		t.Fatalf("Connect(missing) error = %v, want ErrNoDB", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "db")); !os.IsNotExist(err) {
		t.Errorf("Connect created %s/db", dir)
	}
	if err := Reader(p, func(*Conn) error { t.Error("callback ran"); return nil }); !errors.Is(err, ErrNoDB) {
		t.Errorf("Reader(missing) = %v", err)
	}
	if _, err := ConnectRO(p); err == nil || !strings.Contains(err.Error(), "FACTORY_HOME") {
		t.Errorf("ConnectRO(missing) = %v, want a message naming FACTORY_HOME", err)
	}
}

func TestReaderOpensAndCloses(t *testing.T) {
	_, p := newFixture(t)
	var held *Conn
	if err := Reader(p, func(c *Conn) error {
		held = c
		has, err := c.HasColumn("tasks", "waits")
		if err != nil || !has {
			t.Errorf("HasColumn(tasks.waits) = %v, %v", has, err)
		}
		has, err = c.HasColumn("tasks", "nope")
		if err != nil || has {
			t.Errorf("HasColumn(tasks.nope) = %v, %v", has, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := held.Ping(); err == nil {
		t.Error("connection still open after Reader returned")
	}
}

// TestDBPathMatchesFactorydb: the web UI and the CLI resolve the database the same way, for the live home (no
// FACTORY_HOME: ~/factory), a temp home and a FACTORY_HOME override, with the legacy fallback.
func TestDBPathMatchesFactorydb(t *testing.T) {
	t.Run("live home", func(t *testing.T) {
		h := t.TempDir() // stands in for the user's home: the live ~/factory is never touched
		t.Setenv("HOME", h)
		t.Setenv("FACTORY_HOME", "")
		want := factorydb.ResolvePath(filepath.Join(h, "factory"))
		if got := DBPath(); got != want {
			t.Errorf("DBPath() = %s, factorydb = %s", got, want)
		}
		if want != filepath.Join(h, "factory", "db", "factory.db") {
			t.Errorf("default path = %s", want)
		}
	})
	t.Run("temp home", func(t *testing.T) {
		home, p := newFixture(t)
		if got := ResolveDBPath(home); got != p || got != factorydb.ResolvePath(home) {
			t.Errorf("ResolveDBPath = %s, want %s", got, p)
		}
	})
	t.Run("FACTORY_HOME override", func(t *testing.T) {
		home := t.TempDir()
		t.Setenv("FACTORY_HOME", home)
		t.Setenv("HOME", t.TempDir())
		if got, want := DBPath(), factorydb.ResolvePath(home); got != want {
			t.Errorf("DBPath() = %s, factorydb = %s", got, want)
		}
		if got := FactoryHome(); got != home {
			t.Errorf("FactoryHome() = %s, want %s", got, home)
		}
	})
	t.Run("legacy fallback", func(t *testing.T) {
		home := t.TempDir()
		legacy := filepath.Join(home, "factory.db")
		if err := os.WriteFile(legacy, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got := ResolveDBPath(home); got != legacy || got != factorydb.ResolvePath(home) {
			t.Errorf("legacy only: got %s, want %s", got, legacy)
		}
		if err := os.MkdirAll(filepath.Join(home, "db"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, "db", "factory.db"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if got, want := ResolveDBPath(home), filepath.Join(home, "db", "factory.db"); got != want {
			t.Errorf("both exist: got %s, want %s", got, want)
		}
	})
	t.Run("neither exists", func(t *testing.T) {
		home := t.TempDir()
		if got, want := ResolveDBPath(home), filepath.Join(home, "db", "factory.db"); got != want || got != factorydb.ResolvePath(home) {
			t.Errorf("neither: got %s, want %s", got, want)
		}
	})
}
