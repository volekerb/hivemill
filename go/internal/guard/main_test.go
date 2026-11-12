package guard

import (
	"encoding/json"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
	"time"
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
	// expandUser reads $HOME while the host (live homes) is built from the passwd entry; the tests assume they are
	// equal. The worker sandbox sets a fake HOME, so align it here. Outside the sandbox this is a no-op.
	u, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Setenv("HOME", u.HomeDir)
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
	writeExport(t, home, exportDoc{})
	return home
}

// exportDoc is the content of a test guard export; the zero value is a fresh export of an empty DB.
type exportDoc struct {
	Config    map[string]string
	Goals     map[string]string
	Worktrees map[string]string
	Age       time.Duration // how long ago generated_at is
}

// writeExport writes <home>/run/guard-export.json as the tick would (version 1, RFC 3339 generated_at).
func writeExport(t *testing.T, home string, d exportDoc) {
	t.Helper()
	nz := func(m map[string]string) map[string]string {
		if m == nil {
			return map[string]string{}
		}
		return m
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "generated_at": time.Now().Add(-d.Age).UTC().Format(time.RFC3339),
		"config": nz(d.Config), "agent_goals": nz(d.Goals), "task_worktrees": nz(d.Worktrees)})
	if err != nil {
		t.Fatal(err)
	}
	writeExportRaw(t, home, raw)
}

// writeExportRaw writes the export file as it is (a broken file included) and drops the cached read.
func writeExportRaw(t *testing.T, home string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "guard-export.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cachedExport.done = false
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
