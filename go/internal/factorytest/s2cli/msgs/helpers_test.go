package s2msgs

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/spawn"
)

const body10Lines = 10

var (
	digestFrozen = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	frozenEnv    = map[string]string{"FACTORY_NOW": digestFrozen.Format(time.RFC3339)}
	msgIDRe      = regexp.MustCompile(`^M\d+ → `)
)

func body10() string {
	var b strings.Builder
	for i := 1; i <= body10Lines; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func sqlNullOK(t *testing.T, e *ft.Env, q string, args ...any) {
	t.Helper()
	v := sql1(t, e, q, args...)
	if v != "NULL" {
		t.Fatalf("%s = %q, want NULL", q, v)
	}
}

func digestISO(delta time.Duration, base time.Time) string {
	return base.Add(delta).Format("2006-01-02T15:04:05-07:00")
}

func isoDelta(delta time.Duration) string {
	return digestISO(delta, digestFrozen)
}

func section(out, name string) string {
	return strings.Split(strings.Split(out, "### "+name)[1], "###")[0]
}

func seedPRFile(t *testing.T, e *ft.Env, url, sha string, checks any) {
	t.Helper()
	p := filepath.Join(e.Root, "gh.json")
	b, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	st := map[string]any{}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &st); err != nil {
			t.Fatal(err)
		}
	}
	prs, _ := st["prs"].(map[string]any)
	if prs == nil {
		prs = map[string]any{}
	}
	entry := map[string]any{
		"number": 1, "state": "OPEN", "headRefOid": sha, "headRefName": "factory/t-0001",
		"baseRefName": "main", "mergeable": "MERGEABLE", "isDraft": false, "url": url,
	}
	if checks == nil {
		entry["statusCheckRollup"] = []any{}
	} else {
		entry["statusCheckRollup"] = checks
	}
	prs[url] = entry
	st["prs"] = prs
	out, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// seedPRStub writes gh.json and scripts gh pr view (the Go stub ignores gh.json alone).
func seedPRStub(t *testing.T, e *ft.Env, url, sha string, checks any) {
	t.Helper()
	seedPRFile(t, e, url, sha, checks)
	var ck []map[string]string
	switch c := checks.(type) {
	case nil:
	case []map[string]string:
		ck = c
	default:
		b, _ := json.Marshal(checks)
		_ = json.Unmarshal(b, &ck)
	}
	appendGhRules(t, e, prViewRule(url, sha, "OPEN", nil, ck))
}

func repoFile(t *testing.T, rel string) string {
	t.Helper()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, rel)
}

func roleSettings(t *testing.T, e *ft.Env, role string) map[string]any {
	t.Helper()
	roles := repoFile(t, "roles")
	tok := spawn.SettingsTokens(e.Home, e.UserHome, filepath.Join(e.UserHome, ".config"))
	text, r := spawn.SettingsFromDir(roles, role, tok)
	if r != nil {
		t.Fatalf("settings %s: %v", role, r.Msg)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func pyReviewAlert(t *testing.T, e *ft.Env, goalOrg, team, sha string) {
	t.Helper()
	e.NeedPython()
	root, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`
import importlib.util, importlib.machinery, os, sys
home, sha, goal_org, team = sys.argv[1:5]
os.environ.update({"FACTORY_HOME": home, "PATH": os.environ.get("PATH",""), "HOME": os.environ.get("HOME",""), "PYTHONDONTWRITEBYTECODE": "1"})
src = %q
loader = importlib.machinery.SourceFileLoader("f", src)
mod = importlib.util.module_from_spec(importlib.util.spec_from_loader(loader.name, loader))
loader.exec_module(mod)
con = mod.db()
con.execute("UPDATE goals SET org=? WHERE id='G-001'", (goal_org,))
name = mod._rerun_name(con, "T-0001", "qa", sha)
con.execute("INSERT OR REPLACE INTO agents(name,role,goal_id,team_id,task_id,status) VALUES(?,?,?,?,?,?)",
    (name, "qa", "G-001", team, "T-0001", "exited"))
t = con.execute("SELECT * FROM tasks WHERE id='T-0001'").fetchone()
mod._review_alert_no_verdict(con, t, sha, 1.0)
con.commit()
`, filepath.Join(root, "bin", "factory_cli.py"))
	cmd := exec.Command("python3", "-c", script, e.Home, sha, goalOrg, team)
	cmd.Env = e.Environ("", nil)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("review_alert python: %v\n%s", err, out)
	}
}

func nullTime(t *testing.T, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	var v sql.NullString
	if err := d.QueryRow(q, args...).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v.Valid {
		t.Fatalf("%s: read_at = %q, want NULL", q, v.String)
	}
}
