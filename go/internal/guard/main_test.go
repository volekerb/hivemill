package guard

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestMain points FACTORY_HOME at an empty temp directory for the whole package: the rules that read the factory DB
// (own goal, test_sandbox, public-text repos, the coder's task worktree) then take Python's "no DB file" branch
// without delegating, and no test touches the live home (C6). The tests of the delegation create a DB file in
// their own temp FACTORY_HOME.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "guard-test-home-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("FACTORY_HOME", dir)
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// dbHome is a temp FACTORY_HOME holding an (empty) DB file: the rules that read the DB are delegated to the Python
// hook, which Go cannot do itself. It also exports a terms-file path (as spawn does), so that the terms roles are not
// delegated for the DB's scrub_terms_file key: tests of that unset the variable themselves.
func dbHome(t *testing.T) string {
	t.Helper()
	exportTerms(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "db", "factory.db"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return home
}

// exportTerms points the terms-file variable at a real file, as spawn does for the workers.
func exportTerms(t *testing.T) {
	t.Helper()
	tf := filepath.Join(t.TempDir(), "terms.txt")
	if err := os.WriteFile(tf, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(termsVar, tf)
}
