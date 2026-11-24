package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	ft "factory/go/internal/factorytest"
)

// Port of tests/test_spawn_strict_mcp.py (6 cases; see ../ported_cursor.txt).
// The Python module inherits setUp from test_plugins.PluginDirSpawnTests (the sandbox ingredients of
// test_sandbox_spawn.prep) and test_resume.ResumeTestCase; both setups are written out here. ROLES is read from the
// literal in the Python CLI source, as tests/harness.py does. Spawn, relaunch and init still run in Python: the tests
// skip without python3.

// Owner list via director M24599 (cursor-boss relay); no other ids in role settings.
var accountPluginIDs = []string{"caveman@caveman", "engineering@synced", "design@synced", "product-management@synced",
	"productivity@synced", "cowork-plugin-management@synced"}

var cavemanOnRoles = map[string]bool{"director": true, "lead": true, "cleaner": true}

const tokenMarker = "zz-test-token-marker-5c1e9d"

var rolesLiteral = regexp.MustCompile(`(?s)\nROLES = \[(.*?)\]`)

func allRoles(t *testing.T) []string {
	t.Helper()
	m := rolesLiteral.FindStringSubmatch(readFile(t, filepath.Join(repoRoot(t), "bin", "factory_cli.py")))
	if m == nil {
		t.Fatal("no ROLES literal in bin/factory_cli.py")
	}
	var roles []string
	for _, q := range regexp.MustCompile(`"([a-z-]+)"`).FindAllStringSubmatch(m[1], -1) {
		roles = append(roles, q[1])
	}
	if len(roles) == 0 {
		t.Fatal("empty ROLES literal")
	}
	return roles
}

func cliSpawnable(t *testing.T) []string {
	var out []string
	for _, r := range allRoles(t) {
		if r != "evaluator" {
			out = append(out, r)
		}
	}
	return out
}

func spawnAgentName(role string) string {
	if role == "retro" {
		return "cursor-zz-test-retro"
	}
	return "zz-test-" + role
}

func spawnCmd(role string) []string {
	base := []string{"spawn", role, "--name", spawnAgentName(role)}
	if role == "coder" || role == "qa" || role == "reviewer" || role == "researcher" {
		base = append(base, "--goal", "G-001", "--task", "T-0001")
		if role == "qa" { // T-0956: a no-PR qa task needs an explicit cwd
			base = append(base, "--cwd", os.TempDir())
		}
	}
	return base
}

// pluginEnv is PluginDirSpawnTests.setUp: prep(env) plus a todo coder task and lifted caps.
func pluginEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	writeFile(t, filepath.Join(e.StubBin, "agent"), "#!/bin/sh\necho CURSOR_OK\n", 0o755) // cursor retro/scout spawns need it
	// prep(): a fake claude and unshare, the three token files, the repo the helper is scoped to, goal, team, worktrees
	writeFile(t, filepath.Join(e.StubBin, "claude"), "#!/bin/sh\nexit 0\n", 0o755)
	writeFile(t, filepath.Join(e.StubBin, "unshare"), "#!/bin/sh\nexit 0\n", 0o755)
	for _, n := range []string{"worker-oauth-token", "gh-worker-ro-token", "gh-worker-rw-token"} {
		writeFile(t, filepath.Join(e.UserHome, ".config", "factory", n), tokenMarker+"\n", 0o600)
	}
	sqlExec(t, e, "INSERT INTO config(key,value) VALUES('worker_gh_repos','volekerb/hivemill') ON CONFLICT(key) DO UPDATE SET value=excluded.value")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-lead")
	for _, role := range []string{"reviewer", "qa", "coder"} {
		wt := mkdir(t, filepath.Join(e.Home, "worktrees", "w-"+role))
		if role != "reviewer" {
			mkdir(t, filepath.Join(wt, ".git"))
		}
	}
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "todo", Team: "g-001-a"})
	sqlExec(t, e, "UPDATE tasks SET pr_url=NULL WHERE id='T-0001'")
	e.OK("config", "max_live_agents", "99")
	e.OK("config", "max_total_claude", "99") // one pane per role now exceeds the default 10
	return e
}

func startsByName(e *ft.Env) map[string][]string {
	out := map[string][]string{}
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) >= 3 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			out[c.Argv[2]] = c.Argv
		}
	}
	return out
}

func assertStrict(t *testing.T, argv []string, role string) {
	t.Helper()
	if indexOf(argv, "--strict-mcp-config") < 0 {
		t.Errorf("%s: no --strict-mcp-config in %v", role, argv)
		return
	}
	var cfg map[string]map[string]any
	if err := json.Unmarshal([]byte(flagValue(t, argv, "--mcp-config")), &cfg); err != nil {
		t.Errorf("%s: --mcp-config is not JSON: %v", role, err)
		return
	}
	if len(cfg) != 1 || cfg["mcpServers"] == nil || len(cfg["mcpServers"]) != 0 {
		t.Errorf("%s: --mcp-config = %v, want {\"mcpServers\": {}}", role, cfg)
	}
}

func TestSpawnStrictMcpFlagsForEveryRoleButBoss(t *testing.T) {
	e := pluginEnv(t)
	roles := cliSpawnable(t)
	for _, role := range roles {
		if r := e.Run(spawnCmd(role), ""); r.Exit != 0 {
			t.Errorf("spawn %s: exit %d\n%s%s", role, r.Exit, r.Stdout, r.Stderr)
		}
	}
	starts := startsByName(e)
	for _, role := range roles {
		argv := starts[spawnAgentName(role)]
		if argv == nil {
			t.Errorf("%s: no agent start was logged", role)
			continue
		}
		if role == "boss" || role == "retro" { // retro is cursor runtime; strict MCP flags are Claude-only
			if indexOf(argv, "--strict-mcp-config") >= 0 || indexOf(argv, "--mcp-config") >= 0 {
				t.Errorf("%s must not carry MCP flags: %v", role, argv)
			}
		} else {
			assertStrict(t, argv, role)
		}
	}
}

func TestSpawnStrictMcpExistingFlagsSurvive(t *testing.T) {
	e := pluginEnv(t)
	e.OK(spawnCmd("coder")...)
	argv := startsByName(e)["zz-test-coder"]
	n := 0
	for _, a := range argv {
		if a == "--plugin-dir" {
			n++
		}
	}
	eq(t, "--plugin-dir count", n, 1) // no goal plugin dir exists for G-001 in the temp home
	for _, flag := range []string{"--model", "--settings", "--append-system-prompt-file", "--session-id"} {
		if indexOf(argv, flag) < 0 {
			t.Errorf("%s missing in %v", flag, argv)
		}
	}
}

// resumeEnv is ResumeTestCase.setUp: three lost agents with session ids.
func resumeEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	sqlExec(t, e, "UPDATE config SET value='herdr' WHERE key='terminal_backend'")
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	for _, a := range []ft.AgentRow{
		{Name: "zz-test-director", Role: "director", Status: "lost"},
		{Name: "zz-test-boss", Role: "boss", Status: "lost"},
		{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a", Status: "lost"}} {
		e.SeedAgent(a)
	}
	for n, s := range map[string]string{"zz-test-director": "sess-dir", "zz-test-boss": "sess-boss", "zz-test-lead": "sess-lead"} {
		sqlExec(t, e, "UPDATE agents SET session_id=? WHERE name=?", s, n)
	}
	return e
}

func relaunch(t *testing.T, e *ft.Env, name string) {
	t.Helper()
	proc := mkdir(t, filepath.Join(e.Home, "fakeproc"))
	r := e.RunWith(ft.Opts{Agent: "zz-test-director", Env: map[string]string{"FACTORY_PROC_DIR": proc}}, "relaunch", name)
	if r.Exit != 0 {
		t.Fatalf("relaunch %s: exit %d\n%s%s", name, r.Exit, r.Stdout, r.Stderr)
	}
}

func TestSpawnStrictMcpResumeRelaunchResumeCarriesStrictFlagsAndResumeFlags(t *testing.T) {
	e := resumeEnv(t)
	relaunch(t, e, "zz-test-lead")
	argv := startsByName(e)["zz-test-lead"]
	assertStrict(t, argv, "lead")
	eq(t, "--system-prompt-snapshot", flagValue(t, argv, "--system-prompt-snapshot"), "off")
}

func TestSpawnStrictMcpResumeOfBossHasNoStrictFlags(t *testing.T) {
	e := resumeEnv(t)
	relaunch(t, e, "zz-test-boss")
	argv := startsByName(e)["zz-test-boss"]
	if indexOf(argv, "--strict-mcp-config") >= 0 || indexOf(argv, "--mcp-config") >= 0 {
		t.Fatalf("boss must not carry MCP flags: %v", argv)
	}
	eq(t, "--system-prompt-snapshot", flagValue(t, argv, "--system-prompt-snapshot"), "off")
}

// renderedSettings runs `factory init`, which renders every role's settings from roles/settings/<role>.json.in into
// <home>/run/settings/ (the harness renders them with render_all_settings).
func renderedSettings(t *testing.T) (*ft.Env, func(role string) (map[string]any, string)) {
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.OK("init")
	return e, func(role string) (map[string]any, string) {
		raw := readFile(t, filepath.Join(e.Home, "run", "settings", role+".json"))
		return parseJSON(t, raw), raw
	}
}

func TestSpawnStrictMcpSettingsNonBossDisableConnectorsAndAccountPlugins(t *testing.T) {
	_, load := renderedSettings(t)
	for _, role := range allRoles(t) {
		if role == "boss" {
			continue
		}
		t.Run(role, func(t *testing.T) {
			d, raw := load(role)
			eq(t, "ENABLE_CLAUDEAI_MCP_SERVERS", sub(t, d, "env")["ENABLE_CLAUDEAI_MCP_SERVERS"], any("false"))
			ep := sub(t, d, "enabledPlugins")
			var got []string
			for k := range ep {
				got = append(got, k)
			}
			want := append([]string(nil), accountPluginIDs...)
			sort.Strings(got)
			sort.Strings(want)
			eq(t, "plugin ids", strings.Join(got, ","), strings.Join(want, ","))
			// the key order is part of the contract: the file lists them in the owner's order
			last := -1
			for _, id := range accountPluginIDs {
				i := strings.Index(raw, `"`+id+`"`)
				if i < last {
					t.Errorf("%s: plugin %s is out of order", role, id)
				}
				last = i
			}
			for _, id := range accountPluginIDs {
				on, _ := ep[id].(bool)
				if id == "caveman@caveman" {
					eq(t, role+" caveman", on, cavemanOnRoles[role])
				} else if on {
					t.Errorf("%s: %s must be disabled", role, id)
				}
			}
		})
	}
}

func TestSpawnStrictMcpSettingsBossDoesNotDisableConnectors(t *testing.T) {
	_, load := renderedSettings(t)
	d, _ := load("boss")
	if _, has := d["enabledPlugins"]; has {
		t.Error("boss settings must not carry enabledPlugins")
	}
	if _, has := sub(t, d, "env")["ENABLE_CLAUDEAI_MCP_SERVERS"]; has {
		t.Error("boss settings must not disable the connectors")
	}
}
