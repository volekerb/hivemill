package s2restier

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/gate"
)

func as(e *ft.Env, agent string, args ...string) ft.Result {
	e.T.Helper()
	return e.RunWith(ft.Opts{Agent: agent}, args...)
}

func contains(t testing.TB, what, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("%s = %q, want it to contain %q", what, got, want)
	}
}

func sqlExec(t testing.TB, e *ft.Env, q string, args ...any) {
	t.Helper()
	d := e.RawDB()
	defer d.Close()
	if _, err := d.Exec(q, args...); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

// capBaseEnv is CapBase.setUp from tests/test_lead_caps.py (shared by test_res_org and test_res_hq_cap).
func capBaseEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedGoal("G-003", "", "zz-test goal")
	sqlExec(t, e, "UPDATE goals SET priority=1 WHERE id='G-001'")
	sqlExec(t, e, "UPDATE goals SET priority=3 WHERE id='G-003'")
	e.SeedTask(ft.Task{Status: "todo", PRURL: ""})
	e.OK("config", "lead_parking", "1")
	e.OK("config", "max_live_agents", "18")
	return e
}

func cfgGet(e *ft.Env, key string) (string, error) {
	rows := e.SQL("SELECT value FROM config WHERE key=?", key)
	if len(rows) == 0 {
		return "", fmt.Errorf("config %s: missing", key)
	}
	return rows[0][0], nil
}

// gateHQCap is gate(role="lead")[2]["hq_cap"] from the Python harness: gate.LoadConfig on the env DB and gate.Decide.
func gateHQCap(t *testing.T, e *ft.Env) int {
	t.Helper()
	cfg, err := gate.LoadConfig(func(k string) (string, error) { return cfgGet(e, k) })
	if err != nil {
		t.Fatalf("gate.LoadConfig: %v", err)
	}
	now, ok := gate.ParseISO(ft.DefaultNow)
	if !ok {
		t.Fatalf("FACTORY_TEST_NOW %q", ft.DefaultNow)
	}
	snap := gate.Snapshot{
		Now: now, HerdrReachable: true, MemMB: 3000,
		Usage:  gate.Usage{FiveHour: ptrF64(10), SevenDay: ptrF64(20), Fresh: true},
		Procs:  gate.ProcScan{Known: true, Argv: map[int][]string{}},
		Load1:  0.5,
		NumCPU: 8,
	}
	d := gate.Decide(snap, cfg, gate.Options{Role: "lead"})
	return d.Stats.HQCap
}

func ptrF64(v float64) *float64 { return &v }

// tierBaseEnv is TaskTierTests.setUp from tests/test_task_tier.py.
func tierBaseEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	for _, pair := range []struct{ name, role string }{
		{"zz-lead", "lead"}, {"zz-coder", "coder"}, {"zz-rev", "reviewer"},
	} {
		e.SeedAgent(ft.AgentRow{Name: pair.name, Role: pair.role, Goal: "G-001", Team: "g-001-a"})
	}
	e.SeedTask(ft.Task{ID: "T-0001", Status: "in_review"})
	if err := os.MkdirAll(filepath.Join(e.Home, "ci"), 0o755); err != nil {
		t.Fatal(err)
	}
	copyCIFromRepo(t, e)
	return e
}

func copyCIFromRepo(t *testing.T, e *ft.Env) {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"tier_rules.json", "affected.py"} {
		src := filepath.Join(repo, "ci", f)
		b, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		dst := filepath.Join(e.Home, "ci", f)
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			t.Fatalf("write %s: %v", dst, err)
		}
	}
}

func tierOf(e *ft.Env) string {
	rows := e.SQL("SELECT tier FROM tasks WHERE id='T-0001'")
	if len(rows) == 0 || rows[0][0] == "NULL" {
		return ""
	}
	return rows[0][0]
}

// homeRepo is TaskTierTests._home_repo: base commit on main, branch pr edits tier rules and guard_x.py.
func homeRepo(t *testing.T, e *ft.Env) {
	t.Helper()
	ci := filepath.Join(e.Home, "ci")
	if err := os.WriteFile(filepath.Join(ci, "guard_x.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Home, "web.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitHome(t, e.Home, "add", "ci", "web.py")
	gitHome(t, e.Home, "commit", "-q", "-m", "base")
	gitHome(t, e.Home, "checkout", "-q", "-b", "pr")
	rulesPath := filepath.Join(ci, "tier_rules.json")
	raw, err := os.ReadFile(rulesPath)
	if err != nil {
		t.Fatal(err)
	}
	var rules map[string]any
	if err := json.Unmarshal(raw, &rules); err != nil {
		t.Fatal(err)
	}
	rules["sensitive_paths"] = []any{}
	rules["large_lines"] = 1_000_000
	out, err := json.Marshal(rules)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rulesPath, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ci, "guard_x.py"), []byte("x = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitHome(t, e.Home, "commit", "-q", "-m", "pr edits rules and a guard", "ci")
	gitHome(t, e.Home, "checkout", "-q", "main")
}

func gitHome(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=zz-test", "GIT_AUTHOR_EMAIL=zz-test",
		"GIT_COMMITTER_NAME=zz-test", "GIT_COMMITTER_EMAIL=zz-test")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

func riskTiersOn(t *testing.T, e *ft.Env) {
	t.Helper()
	sqlExec(t, e, "INSERT INTO config(key,value) VALUES('risk_tiers','on') ON CONFLICT(key) DO UPDATE SET value='on'")
}
