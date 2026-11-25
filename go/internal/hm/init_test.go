package hm

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tableCols(t *testing.T, s *SQLite, table string) []string {
	t.Helper()
	rows, err := s.db.Query(`SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		_ = rows.Scan(&n)
		out = append(out, n)
	}
	return out
}

func TestInitHomeIsIdempotent(t *testing.T) {
	home := t.TempDir()
	for i := 0; i < 2; i++ {
		if _, err := InitHome(home, InitOptions{NoreplyEmail: "1+zz-test@users.noreply.github.com"}); err != nil {
			t.Fatalf("init %d: %v", i, err)
		}
	}
	for _, d := range InitDirs {
		if fi, err := os.Stat(filepath.Join(home, d)); err != nil || !fi.IsDir() {
			t.Errorf("%s missing", d)
		}
	}
	s, err := OpenSQLite(filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, tb := range []string{"tasks", "goals", "projects", "config", "agents", "verdicts", "events", "owner_alert_feed", "hm_config",
		"hm_runs", "hm_sessions"} {
		if len(tableCols(t, s, tb)) == 0 {
			t.Errorf("table %s missing", tb)
		}
	}
	if got := s.FactoryCfg("noreply_email"); got != "1+zz-test@users.noreply.github.com" {
		t.Errorf("noreply_email = %q", got)
	}
	var v int
	_ = s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	if v != 0 {
		t.Errorf("user_version = %d; init must not claim the full factory schema", v)
	}
	// the alert feed dedupes through its partial unique index
	_ = s.AlertFeed("k", "r", "l")
	_ = s.AlertFeed("k", "r", "l")
	var n int
	_ = s.db.QueryRow(`SELECT count(*) FROM owner_alert_feed`).Scan(&n)
	if n != 1 {
		t.Errorf("alert rows = %d, want 1", n)
	}
}

// An existing factory DB (one the old CLI built) keeps every table and row as it was.
func TestInitHomeOnAnExistingFactoryDBChangesNothing(t *testing.T) {
	home := t.TempDir()
	dbp := filepath.Join(home, "db", "factory.db")
	_ = os.MkdirAll(filepath.Dir(dbp), 0o700)
	raw, err := sql.Open("sqlite", dbp)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(strings.ReplaceAll(FactorySchema, "IF NOT EXISTS ", "") + `PRAGMA user_version=4;
		INSERT INTO projects(id,name,created) VALUES('www','www','t');`); err != nil {
		t.Fatal(err)
	}
	raw.Close()
	before := map[string][]string{}
	s0, _ := OpenSQLite(dbp)
	for _, tb := range []string{"tasks", "goals", "projects", "agents"} {
		before[tb] = tableCols(t, s0, tb)
	}
	s0.Close()
	if _, err := InitHome(home, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	s, _ := OpenSQLite(dbp)
	defer s.Close()
	for tb, cols := range before {
		if got := tableCols(t, s, tb); strings.Join(got, ",") != strings.Join(cols, ",") {
			t.Errorf("%s columns changed: %v -> %v", tb, cols, got)
		}
	}
	var v, n int
	_ = s.db.QueryRow(`PRAGMA user_version`).Scan(&v)
	_ = s.db.QueryRow(`SELECT count(*) FROM projects`).Scan(&n)
	if v != 4 || n != 1 {
		t.Errorf("user_version %d, projects %d; want 4 and 1", v, n)
	}
}

// The schema the runner's queries need, on a fresh home: a pass with no projects idles and health passes.
func TestFreshHomeIdlesAndIsHealthy(t *testing.T) {
	home := t.TempDir()
	if _, err := InitHome(home, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	e := Env{Home: home, UserHome: t.TempDir()}
	r, err := NewRunner(e, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	var hcCalls []string
	r.Tools = Tools{
		Git: func(string, ...string) (int, string, string) { return 1, "", "no git in this test" },
		GH:  func(string, ...string) (int, string, string) { return 1, "", "no gh in this test" },
		HC: func(a ...string) (int, string, string) {
			hcCalls = append(hcCalls, strings.Join(a, " "))
			return 0, `{"terminals": []}`, ""
		},
		Sh: func(string, time.Duration, []string, ...string) (int, string) { return 0, "" },
	}
	if err := r.Pass(true); err != nil {
		t.Fatal(err)
	}
	if err := r.Heartbeat(time.Now()); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.Health(time.Now()); !ok {
		t.Error("health fails after a good pass")
	}
	if runs, _ := r.Store.Runs(); len(runs) != 0 {
		t.Errorf("a pass with no projects started %d runs", len(runs))
	}
	for _, c := range hcCalls {
		if strings.HasPrefix(c, "run") {
			t.Errorf("a pass with no projects launched a terminal: hc %s", c)
		}
	}
}

func TestDraftTasksAreNeverDispatched(t *testing.T) {
	home := t.TempDir()
	if _, err := InitHome(home, InitOptions{}); err != nil {
		t.Fatal(err)
	}
	r, err := NewRunner(Env{Home: home, UserHome: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Store.Close()
	s := r.Store.(*SQLite)
	if err := s.AddProject(Project{ID: "www", RepoPath: t.TempDir(), GHRepo: "zz-test/www"}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddProject(Project{ID: "www"}); err == nil {
		t.Error("a second project www was accepted")
	}
	g, err := s.NewGoal("www", "zz-test goal", "", "", 2)
	if err != nil || g != "G-001" {
		t.Fatalf("goal %q %v", g, err)
	}
	if gl, _ := s.Goal(g); gl.Status != "active" {
		t.Errorf("new goal is %s, want active (ReadyTasks reads active goals)", gl.Status)
	}
	if _, err := s.NewGoal("nope", "x", "", "", 2); err == nil {
		t.Error("a goal in a missing project was accepted")
	}
	ok := "1. it works - cmd:true => exit 0"
	d, _ := s.AddTask(g, "draft", "spec", ok, "draft", "", 1)
	todo, _ := s.AddTask(g, "todo", "spec", ok, "todo", "", 1)
	if d != "T-0001" || todo != "T-0002" {
		t.Fatalf("ids %s %s", d, todo)
	}
	_ = r.Store.SetCfg("projects", "www")
	cands, err := r.Candidates()
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].ID != todo {
		t.Errorf("candidates %v, want only %s", cands, todo)
	}
	if err := r.Start(d); err == nil || !strings.Contains(err.Error(), "is draft") {
		t.Errorf("Start(draft) = %v, want a refusal", err)
	}
	if err := s.UpdateTask(d, map[string]any{"status": "todo", "priority": 0}); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateTask(d, map[string]any{"role": "lead"}); err == nil {
		t.Error("role was settable")
	}
	if ts, _ := s.Tasks(g, "todo"); len(ts) != 2 {
		t.Errorf("todo tasks %d, want 2", len(ts))
	}
}

func TestConfigRefusesFable(t *testing.T) {
	_, s := testRunner(t, "")
	for _, m := range []string{"claude-fable-5", "claude-Fable-5-5", "gpt-5", ""} {
		for _, k := range []string{"model", "risk_model"} {
			if err := s.SetCfg(k, m); err == nil {
				t.Errorf("%s=%q accepted", k, m)
			}
		}
	}
	if err := s.SetCfg("model", "claude-sonnet-5-5"); err != nil {
		t.Error(err)
	}
}

// A home without roles/hivemill (a standalone install) gets the built-in prompts and settings; a home with them uses its own.
func TestRoleFilesFallBackToTheBuiltInCopy(t *testing.T) {
	e := Env{Home: t.TempDir(), UserHome: t.TempDir()}
	for _, role := range []string{"task", "task-judge"} {
		p, err := RenderSettings(e, role)
		if err != nil {
			t.Fatalf("%s: %v", role, err)
		}
		b, _ := os.ReadFile(p)
		var m map[string]any
		if json.Unmarshal(b, &m) != nil || !bytes.Contains(b, []byte(e.Home)) {
			t.Errorf("%s settings not rendered for the home", role)
		}
		pb, err := readRoleFile(filepath.Join(e.Home, "roles", "hivemill", role+".md"))
		if err != nil || len(pb) < 100 {
			t.Errorf("%s prompt: %v", role, err)
		}
	}
	own := filepath.Join(e.Home, "roles", "hivemill")
	_ = os.MkdirAll(own, 0o755)
	_ = os.WriteFile(filepath.Join(own, "task.md"), []byte("home copy"), 0o644)
	if b, _ := readRoleFile(filepath.Join(own, "task.md")); string(b) != "home copy" {
		t.Errorf("home copy not used: %q", b)
	}
	if _, err := readRoleFile(filepath.Join(e.Home, "other", "task.md")); err == nil {
		t.Error("a file outside roles/hivemill fell back to the built-in copy")
	}
}

// assets/ is a copy of the repo files; `go generate ./internal/hm` refreshes it.
func TestAssetsMatchTheRepo(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	if _, err := os.Stat(filepath.Join(root, "roles", "hivemill")); err != nil {
		t.Skip("no repo checkout around this package")
	}
	for name, rel := range AssetNames {
		want, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		got, err := Asset(name)
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("go/internal/hm/assets/%s differs from %s: run `cd go && go generate ./internal/hm`", name, rel)
		}
	}
}

func TestDaemonPlistRendersForAStandaloneHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "hive & mill")
	e := Env{Home: home, UserHome: "/Users/user"}
	p, err := RenderDaemonPlist(e, PlistInput{FactoryHome: home, Path: DefaultLaunchdPath(e.UserHome), LogDir: filepath.Join(home, "logs")})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<string>hivemill.daemon</string>", "<key>FACTORY_HOME</key>", "hive &amp; mill</string>",
		"/Users/user/.local/bin:/opt/homebrew/bin:", ":/usr/bin:/bin", "hive &amp; mill/logs/hivemill.daemon.log", "<key>KeepAlive</key>"} {
		if !strings.Contains(p, want) {
			t.Errorf("plist lacks %q", want)
		}
	}
	if strings.Contains(p, "{{") {
		t.Error("plist has a leftover template field")
	}
}
