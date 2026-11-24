package s2cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
)

const (
	riLead = "zz-lead-x"
	riSess = "sess-zz-lead-x"
	riPane = "px9"
)

// riEnv is RelaunchIdleCase.setUp (tests/test_relaunch_idle.py).
type riEnv struct {
	*ft.Env
}

var riSlots = make(chan struct{}, 3)

func newRelaunchIdleEnv(t *testing.T) *riEnv {
	t.Helper()
	t.Parallel()
	riSlots <- struct{}{}
	t.Cleanup(func() { <-riSlots })
	e := ft.New(t)
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", riLead)
	for _, row := range []struct{ name, role, sess, pane string }{
		{"zz-director", "director", "sess-dir", "p-director"},
		{"zz-boss", "boss", "sess-boss", "p-boss"},
	} {
		e.SeedAgent(ft.AgentRow{Name: row.name, Role: row.role, PaneID: row.pane})
		sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", row.sess, row.name)
	}
	e.SeedAgent(ft.AgentRow{Name: riLead, Role: "lead", Goal: "G-001", Team: "g-001-a", PaneID: riPane})
	sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", riSess, riLead)
	r := &riEnv{e}
	r.herdrAgents(t, [][4]string{{"idle", riLead, riSess, riPane}})
	e.SetHerdr(map[string]any{
		"workspaces": []map[string]string{{"workspace_id": "w1", "label": "x"}},
		"tabs":       []map[string]any{{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 1}},
		"panes":      []map[string]string{{"pane_id": riPane, "tab_id": "t1", "workspace_id": "w1"}},
		"seq":        10,
	})
	copyRolesTree(t, e)
	return r
}

// copyRolesTree replaces the roles symlink with a copy so role edits never touch the repo (Python shutil.copytree).
func copyRolesTree(t testing.TB, e *ft.Env) {
	t.Helper()
	link := filepath.Join(e.Home, "roles")
	src, err := filepath.EvalSymlinks(link)
	if err != nil {
		t.Fatalf("roles symlink: %v", err)
	}
	dst := filepath.Join(e.Root, "roles-copy")
	if err := os.RemoveAll(dst); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cp", "-a", src, dst)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cp -a roles: %v\n%s", err, out)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dst, link); err != nil {
		t.Fatal(err)
	}
}

func (r *riEnv) herdrAgents(t *testing.T, rows [][4]string) {
	t.Helper()
	agents := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		st, name, sess, pane := row[0], row[1], row[2], row[3]
		agents = append(agents, map[string]any{
			"name": name, "agent": "claude", "pane_id": pane, "agent_status": st,
			"agent_session": map[string]string{"value": sess},
		})
	}
	r.SetHerdr(map[string]any{"agents": agents})
}

func (r *riEnv) setStatus(t *testing.T, st string) {
	t.Helper()
	r.herdrAgents(t, [][4]string{{st, riLead, riSess, riPane}})
}

func (r *riEnv) starts() [][]string { return r.HerdrStarts() }

func (r *riEnv) closes() [][]string {
	var out [][]string
	for _, c := range r.Calls("herdr") {
		if len(c.Argv) >= 2 && c.Argv[0] == "pane" && c.Argv[1] == "close" {
			out = append(out, c.Argv)
		}
	}
	return out
}

func (r *riEnv) closeThenStartOrder(t *testing.T) {
	t.Helper()
	var pairs [][2]string
	for _, c := range r.Calls("herdr") {
		if len(c.Argv) < 2 {
			continue
		}
		p := c.Argv[0] + " " + c.Argv[1]
		if p == "pane close" || p == "agent start" {
			pairs = append(pairs, [2]string{c.Argv[0], c.Argv[1]})
		}
	}
	want := [][2]string{{"pane", "close"}, {"agent", "start"}}
	if !reflect.DeepEqual(pairs, want) {
		t.Fatalf("herdr close/start order = %v, want %v", pairs, want)
	}
}

func (r *riEnv) relaunchFlag(t *testing.T, name string) (at, by string) {
	t.Helper()
	if name == "" {
		name = riLead
	}
	rows := r.SQL("SELECT relaunch_requested_at, relaunch_requested_by FROM agents WHERE name=?", name)
	if len(rows) != 1 {
		t.Fatalf("agent %s: %v", name, rows)
	}
	return rows[0][0], rows[0][1]
}

func (r *riEnv) eventDetails(t *testing.T, action string) []string {
	t.Helper()
	var out []string
	for _, row := range r.SQL("SELECT detail FROM events WHERE action=? AND entity_id=?", action, riLead) {
		out = append(out, row[0])
	}
	return out
}

func (r *riEnv) queue(t *testing.T, who string) ft.Result {
	t.Helper()
	if who == "" {
		who = "zz-director"
	}
	r.NeedPython()
	return r.OKWith(ft.Opts{Agent: who}, "relaunch", riLead, "--when-idle")
}

func (r *riEnv) editLeadRole(t *testing.T) {
	t.Helper()
	f := filepath.Join(r.Home, "roles", "lead.md")
	b, err := os.ReadFile(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, append(b, []byte("\nZZ-NEW-RULE\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (r *riEnv) writeUsage(t *testing.T, fiveHour, sevenDay int, ageS int) {
	t.Helper()
	f := filepath.Join(r.UserHome, ".claude", "usage-limits.json")
	if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(map[string]any{
		"five_hour_pct": fiveHour, "seven_day_pct": sevenDay,
		"updated_at": time.Now().Unix() - int64(ageS),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f, body, 0o644); err != nil {
		t.Fatal(err)
	}
}

func argvIndex(argv []string, flag string) int {
	for i, a := range argv {
		if a == flag {
			return i
		}
	}
	return -1
}

func (r *riEnv) assertResumeArgv(t *testing.T, argv []string) {
	t.Helper()
	i := argvIndex(argv, "--system-prompt-snapshot")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != "off" {
		t.Fatalf("--system-prompt-snapshot off missing in %v", argv)
	}
	j := argvIndex(argv, "--resume")
	if j < 0 || j+1 >= len(argv) || argv[j+1] != riSess {
		t.Fatalf("--resume %s missing in %v", riSess, argv)
	}
	if argvIndex(argv, "--settings") < 0 {
		t.Fatalf("--settings missing in %v", argv)
	}
	if argvIndex(argv, "--append-system-prompt-file") < 0 {
		t.Fatalf("--append-system-prompt-file missing in %v", argv)
	}
}

func (r *riEnv) promptPath() string {
	return filepath.Join(r.Home, "run", riLead+".prompt.md")
}

func (r *riEnv) agentCols(t *testing.T) map[string]struct{} {
	t.Helper()
	out := map[string]struct{}{}
	for _, row := range r.SQL("PRAGMA table_info(agents)") {
		if len(row) > 1 {
			out[row[1]] = struct{}{}
		}
	}
	return out
}

func factorySrcPath(t *testing.T) string {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(repo, "bin", "factory_cli.py")
}

const riSpawnPy = `import contextlib, importlib.util, io, os, sys
from pathlib import Path
repo = Path(os.environ["FACTORY_REPO_ROOT"])
loader = importlib.util.spec_from_file_location("factory_under_test", repo / "bin" / "factory_cli.py")
mod = importlib.util.module_from_spec(loader)
with contextlib.redirect_stdout(io.StringIO()):
    loader.loader.exec_module(mod)
con = mod.db()
name = os.environ["SPAWN_NAME"]
resume = os.environ.get("SPAWN_RESUME") or None
from_queue = os.environ.get("SPAWN_FROM_QUEUE") == "1"
kw = {}
if from_queue:
    kw["from_queue"] = True
try:
    with contextlib.redirect_stdout(io.StringIO()):
        mod.spawn(con, "lead", "G-001", "g-001-a", name=name, resume=resume, **kw)
except SystemExit as e:
    sys.exit(e.code if e.code is not None else 0)
`

// pythonSpawn is harness load_factory().spawn(...) for lead resume paths the CLI does not expose.
func (r *riEnv) pythonSpawn(t *testing.T, name, resume string, fromQueue bool) int {
	t.Helper()
	r.NeedPython()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(r.Root, "spawn_inproc.py")
	if err := os.WriteFile(script, []byte(riSpawnPy), 0o644); err != nil {
		t.Fatal(err)
	}
	fq := "0"
	if fromQueue {
		fq = "1"
	}
	cmd := exec.Command("python3", script)
	cmd.Dir = r.Root
	cmd.Env = r.Environ("", map[string]string{
		"FACTORY_REPO_ROOT": repo,
		"SPAWN_NAME":        name,
		"SPAWN_RESUME":      resume,
		"SPAWN_FROM_QUEUE":  fq,
	})
	var se bytes.Buffer
	cmd.Stderr = &se
	err = cmd.Run()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("python spawn: %v\n%s", err, se.String())
		}
	}
	return code
}

func wantLen[T any](t *testing.T, what string, got []T, n int) {
	t.Helper()
	if len(got) != n {
		t.Fatalf("%s len = %d, want %d (%v)", what, len(got), n, got)
	}
}

func wantEmpty[T any](t *testing.T, what string, got []T) {
	t.Helper()
	if len(got) != 0 {
		t.Fatalf("%s = %v, want empty", what, got)
	}
}

func startsNamed(starts [][]string, name string) ([]string, bool) {
	for _, a := range starts {
		if len(a) > 2 && a[2] == name {
			return a, true
		}
	}
	return nil, false
}

func relaunchFlagClear(at string) bool {
	return at == "" || at == "NULL"
}

func expectFlagTimestamp(t *testing.T, at string) {
	t.Helper()
	if relaunchFlagClear(at) {
		t.Fatalf("relaunch_requested_at = %q, want set", at)
	}
}

func expectFlagClearPair(t *testing.T, at, by string) {
	t.Helper()
	if !relaunchFlagClear(at) || !(by == "" || by == "NULL") {
		t.Fatalf("relaunch flag = (%q, %q), want (NULL, NULL)", at, by)
	}
}

func rowField(rows [][]string, i int) string {
	if len(rows) == 0 || len(rows[0]) <= i {
		return ""
	}
	return rows[0][i]
}

func msgRelaunchFailed(r *riEnv) [][]string {
	return r.SQL("SELECT recipient, sender, kind FROM messages WHERE subject LIKE 'relaunch of %failed'")
}

func agentsLine(r *riEnv, stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, riLead) {
			return line
		}
	}
	return ""
}

// riInstallSSH is the harness line shutil.copy2(STUBS / "ssh", stub_bin / "ssh"): the placement cases need the fake ssh.
func (r *riEnv) riInstallSSH(t *testing.T) {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(repo, "tests", "stubs", "ssh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(r.StubBin, "ssh"), b, 0o755); err != nil {
		t.Fatal(err)
	}
}

var riLowHQ = map[string]string{
	"FACTORY_MEM_MB":    "1800",
	"FACTORY_LOADAVG":   "0.1",
	"FACTORY_NPROC":     "8",
	"FACTORY_FACTS_NOW": "10000",
	"FACTORY_SSH_FACTS": `{"workerhost": {"mem_mb": 11000, "load1": 0.1, "nproc": 16, "exit": 0}}`,
}

var riRoomy = map[string]string{"FACTORY_MEM_MB": "8000", "FACTORY_LOADAVG": "0.1", "FACTORY_NPROC": "8"}

// riArm is PlacementRelaunchTests._arm.
func (r *riEnv) riArm(t *testing.T) {
	t.Helper()
	r.riInstallSSH(t)
	r.OKWith(ft.Opts{Agent: "zz-director"}, "config", "worker_machines", "workerhost")
	r.OKWith(ft.Opts{Agent: "zz-director"}, "config", "machine_hq", "hqhost")
}

// riFeatureOffAgent is FeatureOffRelaunchTests._coder: a queued idle relaunch for a second live agent.
func (r *riEnv) riFeatureOffAgent(t *testing.T, name, role, org string) {
	t.Helper()
	pane := "p-" + name
	r.SeedAgent(ft.AgentRow{Name: name, Role: role, Goal: "G-001", PaneID: pane})
	sqlExec(t, r.Env, "UPDATE agents SET session_id=? WHERE name=?", "sess-"+name, "")
	sqlExec(t, r.Env, "UPDATE agents SET session_id=? WHERE name=?", "sess-"+name, name)
	if org != "" {
		sqlExec(t, r.Env, "UPDATE agents SET org=? WHERE name=?", org, name)
	}
	r.herdrAgents(t, [][4]string{{"idle", riLead, riSess, riPane}, {"idle", name, "sess-" + name, pane}})
	r.NeedPython()
	r.OKWith(ft.Opts{Agent: "zz-director"}, "relaunch", name, "--when-idle")
}

func (r *riEnv) riDeferred(name string) [][]string {
	return r.SQL("SELECT detail FROM events WHERE action='relaunch-deferred' AND entity_id=?", name)
}

func (r *riEnv) riRan(name string) [][]string {
	return r.SQL("SELECT 1 FROM events WHERE action='relaunch-run' AND entity_id=?", name)
}
