package scout

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	osexec "os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/spawn"
)

// Port of tests/test_scout_roles.py (19 cases; see ported_scout.txt): the scout and evaluator roles (table, prompts,
// rendered settings, spawn rules, command refusals, guard rules, evaluator home runtime check). Repo files are read
// for real (roles/, bin/factory_cli.py). The settings are rendered with spawn.SettingsFromDir, the Go form of
// settings_text(). Guard and `spawn`/`done` cases drive the Python worker-guard.py and bin/factory, so they call
// NeedPython and skip where python3 is absent.
// not ported: none.

var rpNewRoles = []string{"scout", "evaluator"}

func rpHomeDir(t testing.TB) string {
	t.Helper()
	u, err := user.Current() // the passwd entry, like pwd.getpwuid(os.getuid()).pw_dir
	if err != nil {
		t.Fatal(err)
	}
	return u.HomeDir
}

func rpLiveHome(t testing.TB) string { return filepath.Join(rpHomeDir(t), "factory") }

func rpRepo(t testing.TB) string {
	t.Helper()
	r, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func rpReadRepo(t testing.TB, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(rpRepo(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// rpSettings is settings(role): the role's template rendered for the live home, parsed.
func rpSettings(t testing.TB, role string) map[string]any {
	t.Helper()
	real, err := filepath.EvalSymlinks(rpHomeDir(t))
	if err != nil {
		t.Fatal(err)
	}
	live, err := filepath.EvalSymlinks(rpLiveHome(t))
	if err != nil {
		live = rpLiveHome(t)
	}
	text, ref := spawn.SettingsFromDir(filepath.Join(rpRepo(t), "roles"), role,
		spawn.SettingsTokens(live, real, filepath.Join(real, ".config")))
	if ref != nil {
		t.Fatalf("render %s: %v", role, ref)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(text), &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func rpStrs(v any) []string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func rpHas(t testing.TB, what string, list []string, want string) {
	t.Helper()
	for _, x := range list {
		if x == want {
			return
		}
	}
	t.Fatalf("%s: %q not in the list", what, want)
}

func rpHasNot(t testing.TB, what string, list []string, bad string) {
	t.Helper()
	for _, x := range list {
		if x == bad {
			t.Fatalf("%s: %q must not be in the list", what, bad)
		}
	}
}

func rpBaseline(t testing.TB) []string {
	live := rpLiveHome(t)
	return []string{
		"Bash(gh pr merge:*)", "Bash(sqlite3:*)", "Bash(herdr server stop:*)", "Bash(herdr agent start:*)",
		"Bash(git push origin main:*)", "Bash(git push --force:*)", "Bash(git push -f:*)",
		"Bash(rm -rf ~/factory:*)", "Edit(/" + live + "/factory.db*)",
		"Write(/" + live + "/exports/**)", "Edit(/" + live + "/state/**)",
		"Write(/" + live + "/bin/**)", "Edit(/" + live + "/roles/**)",
	}
}

func TestScoutRolesRolesAndModels(t *testing.T) {
	src := rpReadRepo(t, "bin/factory_cli.py")
	rolesLit := regexp.MustCompile(`(?s)\nROLES = \[(.*?)\]`).FindStringSubmatch(src)
	modelsLit := regexp.MustCompile(`(?s)\nMODELS = \{(.*?)\n\}`).FindStringSubmatch(src)
	if rolesLit == nil || modelsLit == nil {
		t.Fatal("ROLES / MODELS literal not found in bin/factory_cli.py")
	}
	models := map[string]string{}
	for _, m := range regexp.MustCompile(`"([a-z-]+)": "([a-z0-9-]+)"`).FindAllStringSubmatch(modelsLit[1], -1) {
		models[m[1]] = m[2]
	}
	for _, r := range rpNewRoles {
		contains(t, "ROLES", rolesLit[1], `"`+r+`"`)
		if models[r] != "claude-sonnet-5-5" {
			t.Fatalf("MODELS[%s] = %q", r, models[r])
		}
	}
}

func TestScoutRolesNotPersistent(t *testing.T) {
	var line string
	for _, l := range strings.Split(rpReadRepo(t, "bin/factory_cli.py"), "\n") {
		if strings.HasPrefix(l, "PERSISTENT_ROLES") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatal("PERSISTENT_ROLES not found")
	}
	for _, r := range rpNewRoles {
		notContains(t, "PERSISTENT_ROLES", line, `"`+r+`"`)
	}
}

func TestScoutRolesPromptsExistAndFitBudget(t *testing.T) {
	root := rpRepo(t)
	common, err := os.Stat(filepath.Join(root, "roles", "_common.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range rpNewRoles {
		st, err := os.Stat(filepath.Join(root, "roles", r+".md"))
		if err != nil || !st.Mode().IsRegular() {
			t.Fatalf("roles/%s.md: %v", r, err)
		}
		if common.Size()+st.Size() > 12000 {
			t.Fatalf("%s: prompt %d bytes over the 12000 budget", r, common.Size()+st.Size())
		}
	}
	text := rpReadRepo(t, "roles/_common.md")
	contains(t, "_common.md", text, "| scout |")
	contains(t, "_common.md", text, "| evaluator |")
}

func TestScoutRolesSettingsBaselineHookAndSocket(t *testing.T) {
	for _, r := range rpNewRoles {
		d := rpSettings(t, r)
		deny := rpStrs(d["permissions"].(map[string]any)["deny"])
		for _, rule := range rpBaseline(t) {
			rpHas(t, r+" deny", deny, rule)
		}
		env := d["env"].(map[string]any)
		if env["FACTORY_ROLE"] != r {
			t.Fatalf("%s: FACTORY_ROLE = %v", r, env["FACTORY_ROLE"])
		}
		if !strings.HasPrefix(env["HERDR_SOCKET_PATH"].(string), "/proc/nonexistent/") {
			t.Fatalf("%s: HERDR_SOCKET_PATH = %v", r, env["HERDR_SOCKET_PATH"])
		}
		pre := d["hooks"].(map[string]any)["PreToolUse"].([]any)[0].(map[string]any)["hooks"].([]any)[0].(map[string]any)["command"].(string)
		if !regexp.MustCompile(`^sh \S+/roles/hooks/worker-guard --role ` + r + `$`).MatchString(pre) { // T-0356: the shim
			t.Fatalf("%s: hook command %q", r, pre)
		}
	}
}

func TestScoutRolesScoutRules(t *testing.T) {
	p := rpSettings(t, "scout")["permissions"].(map[string]any)
	deny, allow := rpStrs(p["deny"]), rpStrs(p["allow"])
	for _, rule := range []string{"Edit", "Write", "NotebookEdit", "WebFetch", "WebSearch", "Bash(gh pr:*)", "Bash(gh issue:*)",
		"Bash(curl:*)", "Bash(wget:*)", "Bash(git commit:*)", "Bash(git push:*)", "Bash(git merge:*)"} {
		rpHas(t, "scout deny", deny, rule)
	}
	rpHas(t, "scout allow", allow, "Bash(gh api:*)")
	rpHas(t, "scout allow", allow, "Bash(factory:*)")
}

func TestScoutRolesEvaluatorRules(t *testing.T) {
	deny := rpStrs(rpSettings(t, "evaluator")["permissions"].(map[string]any)["deny"])
	live, home := rpLiveHome(t), rpHomeDir(t)
	for _, rule := range []string{"WebFetch", "WebSearch", "Bash(curl:*)", "Bash(wget:*)", "Bash(gh:*)", "Bash(pip:*)",
		"Bash(npm:*)", "Bash(npx:*)", "Bash(ssh:*)", "Bash(scp:*)", "Bash(git push:*)",
		"Bash(herdr:*)", "Edit(/" + live + "/**)", "Write(/" + live + "/**)",
		"Edit(/" + home + "/.claude/**)", "Write(/" + home + "/.claude/**)"} {
		rpHas(t, "evaluator deny", deny, rule)
	}
	for _, rule := range []string{"Edit", "Write"} { // the sandbox itself stays writable
		rpHasNot(t, "evaluator deny", deny, rule)
	}
}

func rpRolesEnv(t *testing.T) *ft.Env {
	rpPar(t)
	t.Helper()
	e := ft.New(t)
	e.NeedPython()
	e.HerdrStateful()
	rpPinTUI(t, e)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	return e
}

func rpSeedHQ(e *ft.Env) {
	for _, a := range [][2]string{{"zz-test-director", "director"}, {"zz-test-lead", "lead"}, {"zz-test-boss", "boss"}} {
		e.SeedAgent(ft.AgentRow{Name: a[0], Role: a[1], Goal: "G-001", Team: "g-001-a"})
	}
}

func TestScoutRolesScoutSpawnDirectorAndOwner(t *testing.T) {
	e := rpRolesEnv(t)
	rpSeedHQ(e)
	for _, c := range [][2]string{{"zz-test-director", "zz-test-s1"}, {"", "zz-test-s2"}} {
		out := rpOK(t, e, c[0], nil, "spawn", "scout", "--name", c[1]).Stdout
		contains(t, "spawn output", out, "SPAWNED "+c[1])
	}
	starts := map[string][]string{}
	for _, c := range e.Calls("herdr") {
		if len(c.Argv) > 2 && c.Argv[0] == "agent" && c.Argv[1] == "start" {
			starts[c.Argv[2]] = c.Argv
		}
	}
	argv := starts["zz-test-s1"]
	if argv == nil {
		t.Fatalf("no agent start for zz-test-s1: %v", starts)
	}
	idx := func(flag string) int {
		for i, a := range argv {
			if a == flag {
				return i
			}
		}
		return -1
	}
	if idx("--plugin-dir") >= 0 {
		t.Fatalf("scout got --plugin-dir: %v", argv)
	}
	if i := idx("--model"); i < 0 || argv[i+1] != "claude-sonnet-5-5" {
		t.Fatalf("model in %v", argv)
	}
	if i := idx("--settings"); i < 0 || !strings.HasSuffix(argv[i+1], "run/settings/scout.json") {
		t.Fatalf("settings in %v", argv)
	}
}

func TestScoutRolesScoutSpawnRefusedForLeadAndBoss(t *testing.T) {
	e := rpRolesEnv(t)
	rpSeedHQ(e)
	for _, who := range []string{"zz-test-lead", "zz-test-boss"} {
		rpRefused(t, e, 3, who, nil, "spawn", "scout", "--name", "zz-test-sx")
	}
	if n := e.Count("SELECT count(*) FROM agents WHERE name='zz-test-sx'"); n != 0 {
		t.Fatalf("agents rows = %d, want 0", n)
	}
}

func TestScoutRolesEvaluatorSpawnRefusedForEveryone(t *testing.T) {
	e := rpRolesEnv(t)
	rpSeedHQ(e)
	for _, who := range []string{"zz-test-director", "zz-test-lead", "zz-test-boss", ""} {
		r := rpRefused(t, e, 3, who, nil, "spawn", "evaluator", "--name", "zz-test-ev")
		contains(t, "stderr", r.Stderr, "factory skilleval try")
	}
	if n := e.Count("SELECT count(*) FROM agents WHERE name='zz-test-ev'"); n != 0 {
		t.Fatalf("agents rows = %d, want 0", n)
	}
}

func rpRefusalEnv(t *testing.T) *ft.Env {
	e := rpRolesEnv(t)
	e.SeedTask(ft.Task{ID: "T-0001", Role: "coder", Status: "in_review", Team: "g-001-a"})
	for _, r := range rpNewRoles {
		e.SeedAgent(ft.AgentRow{Name: "zz-test-" + r, Role: r})
	}
	return e
}

func TestScoutRolesNewRolesRefused(t *testing.T) {
	e := rpRefusalEnv(t)
	commands := [][]string{
		{"spawn", "coder", "--name", "zz-test-x"},
		{"tick"},
		{"judge", "T-0001", "--merge", "--reason", "x"},
		{"verdict", "T-0001", "--pass", "--summary", "x"},
		{"plan", "G-001", "x"},
		{"task", "add", "G-001", "--title", "x", "--role", "coder"},
		{"lesson", "add", "--title", "x", "--rule", "y"},
		{"goal", "new", "--title", "x"},
	}
	for _, r := range rpNewRoles {
		for _, cmd := range commands {
			rpRefused(t, e, 3, "zz-test-"+r, nil, cmd...)
		}
	}
	if n := e.Count("SELECT count(*) FROM goals"); n != 1 {
		t.Fatalf("goals = %d, want 1", n)
	}
	if n := e.Count("SELECT count(*) FROM tasks"); n != 1 {
		t.Fatalf("tasks = %d, want 1", n)
	}
}

func TestScoutRolesScoutDoneWithoutTaskOrGoal(t *testing.T) {
	e := rpRefusalEnv(t)
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director"})
	r := rpOK(t, e, "zz-test-scout", nil, "done", "scouted")
	contains(t, "stdout", r.Stdout, "done; summary sent to director")
	rows := e.SQL("SELECT status FROM agents WHERE name='zz-test-scout'")
	if len(rows) != 1 || rows[0][0] != "done" {
		t.Fatalf("agent row = %v", rows)
	}
}

// rpClean is the environment of a guard run: the test process environment without the factory identity variables.
func rpClean(extra ...string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "FACTORY_") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, extra...)
}

// rpHook runs roles/hooks/worker-guard.py --role role with a Bash payload of command and returns exit and stderr.
func rpHook(t testing.TB, command, role string, env []string) (int, string) {
	t.Helper()
	if _, err := osexec.LookPath("python3"); err != nil {
		t.Skip("python3 is not on PATH: this test drives the Python worker-guard.py")
	}
	payload, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}})
	if err != nil {
		t.Fatal(err)
	}
	cmd := osexec.Command("python3", filepath.Join(rpRepo(t), "roles", "hooks", "worker-guard.py"), "--role", role)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = env
	var se bytes.Buffer
	cmd.Stderr = &se
	if err := cmd.Start(); err != nil {
		t.Fatalf("hook %q: %v", command, err)
	}
	timer := time.AfterFunc(30*time.Second, func() { _ = cmd.Process.Kill() }) // the Python run had timeout=30
	defer timer.Stop()
	err = cmd.Wait()
	if ee, ok := err.(*osexec.ExitError); ok {
		return ee.ExitCode(), se.String()
	} else if err != nil {
		t.Fatalf("hook %q: %v", command, err)
	}
	return 0, se.String()
}

func TestScoutRolesHerdrAndClaudeBlockedPerRole(t *testing.T) {
	for _, r := range rpNewRoles {
		for _, cmd := range []string{"herdr pane list", "env HOME=x herdr server stop", "claude -p hi", "/usr/bin/claude"} {
			if rc, se := rpHook(t, cmd, r, rpClean()); rc != 2 {
				t.Fatalf("%s %q: exit %d, want 2\n%s", r, cmd, rc, se)
			}
		}
	}
}

func TestScoutRolesScoutRunsFactoryScout(t *testing.T) {
	for _, cmd := range []string{"factory scout fetch", "factory scout pending", "factory done x", "factory whoami"} {
		if rc, se := rpHook(t, cmd, "scout", rpClean()); rc != 0 {
			t.Fatalf("%q: exit %d, want 0\n%s", cmd, rc, se)
		}
	}
	if rc, _ := rpHook(t, "factory spawn coder", "scout", rpClean()); rc != 2 {
		t.Fatalf("factory spawn coder: exit %d, want 2", rc)
	}
}

func rpEvaluator(t testing.TB, cmd, home string) (int, string) {
	t.Helper()
	var extra []string
	if home != "" {
		extra = []string{"FACTORY_HOME=" + home}
	}
	return rpHook(t, cmd, "evaluator", rpClean(extra...))
}

func TestScoutRolesEvaluatorFactoryOnlyAgainstTempHome(t *testing.T) {
	live := rpLiveHome(t)
	tmp := t.TempDir()
	for _, cmd := range []string{"factory done 'did it'", "factory task show T-0001", "factory whoami", "factory inbox", "factory msg lead hi there"} {
		if rc, se := rpEvaluator(t, cmd, tmp); rc != 0 {
			t.Fatalf("%q: exit %d, want 0\n%s", cmd, rc, se)
		}
	}
	if rc, _ := rpEvaluator(t, "FACTORY_HOME="+tmp+" factory done x", live); rc != 0 {
		t.Fatalf("FACTORY_HOME=tmp prefix against the live home: exit %d, want 0", rc)
	}
	for _, cmd := range []string{"factory scout pending", "factory goal show G-001", "factory task update T-0001 --status done"} {
		if rc, _ := rpEvaluator(t, cmd, tmp); rc != 2 {
			t.Fatalf("%q: exit %d, want 2", cmd, rc)
		}
	}
	for _, home := range []string{live, ""} {
		if rc, _ := rpEvaluator(t, "factory done x", home); rc != 2 {
			t.Fatalf("factory done x with home %q: exit %d, want 2", home, rc)
		}
	}
	if rc, _ := rpEvaluator(t, "FACTORY_HOME="+live+" factory done x", "/tmp"); rc != 2 {
		t.Fatalf("FACTORY_HOME=live prefix against /tmp: exit %d, want 2", rc)
	}
}

func TestScoutRolesEvaluatorFactoryHomeTokenClosedByDefault(t *testing.T) {
	live := rpLiveHome(t)
	tmp := t.TempDir()
	f := func(format string, a ...any) string { return fmt.Sprintf(format, a...) }
	for _, cmd := range []string{
		f("export FACTORY_HOME=%s; factory done x", live),
		f("declare -x FACTORY_HOME=%s; factory msg director hi", live),
		f("FACTORY_HOME=%s; export FACTORY_HOME; factory inbox", live),
		f("FACTORY_HOME=%s; factory inbox", live),
		"unset FACTORY_HOME; factory msg director hi",
		"env -u FACTORY_HOME factory inbox",
		f("env FACTORY_HOME=%s factory inbox", live),
		f("readonly FACTORY_HOME=%s; factory inbox", live),
		f("FACTORY_HOME=%s factory inbox", live),
		f("FACTORY_HOME=%s/worktrees/x factory inbox", live),
		f("eval 'export FACTORY_HOME=%s'; factory inbox", live),
		f("bash -c 'export FACTORY_HOME=%s; factory inbox'", live),
		f("read FACTORY_HOME <<< %s; factory inbox", live),
		f("printf -v FACTORY_HOME %%s %s; factory inbox", live),
		f("for FACTORY_HOME in %s; do factory done x; done", live),
		f("mapfile -t FACTORY_HOME <<< %s; factory inbox", live),
		"getopts a FACTORY_HOME -a; factory inbox",
		f("export {FACTORY_HOME,Y}=%s; factory inbox", live),
		f("FACTORY_HOME=%s factory inbox; export FACTORY_HOME=%s", tmp, live),
		f("FACTORY_HOME=%s factory inbox && FACTORY_HOME=%s factory inbox", tmp, live),
		"echo FACTORY_HOME; factory inbox",
		"FACTORY_HOME=$FACTORY_HOME factory inbox",
	} {
		if rc, se := rpEvaluator(t, cmd, tmp); rc != 2 {
			t.Fatalf("%q: exit %d, want 2\n%s", cmd, rc, se)
		}
	}
	for _, cmd := range []string{"FACTORY_HOME=" + tmp + " factory inbox", "FACTORY_HOME=" + tmp + " factory done 'did it'", "factory done x"} {
		if rc, se := rpEvaluator(t, cmd, tmp); rc != 0 {
			t.Fatalf("%q: exit %d, want 0\n%s", cmd, rc, se)
		}
	}
}

func TestScoutRolesOtherRolesMayMentionFactoryHome(t *testing.T) {
	tmp := t.TempDir()
	if rc, se := rpHook(t, "export FACTORY_HOME="+tmp+"; echo hi", "scout", rpClean()); rc != 0 {
		t.Fatalf("exit %d, want 0\n%s", rc, se)
	}
}

// rpWhoami runs `python3 bin/factory whoami` as role in the env of e with FACTORY_HOME = home (nil = unset).
func rpWhoami(t *testing.T, e *ft.Env, agent, role string, home *string) (int, string, string) {
	t.Helper()
	var env []string
	for _, kv := range e.Environ(agent, map[string]string{"FACTORY_ROLE": role}) {
		if !strings.HasPrefix(kv, "FACTORY_HOME=") {
			env = append(env, kv)
		}
	}
	if home != nil {
		env = append(env, "FACTORY_HOME="+*home)
	}
	cmd := osexec.Command("python3", filepath.Join(rpRepo(t), "bin", "factory"), "whoami")
	cmd.Env, cmd.Dir = env, e.Home
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	timer := time.AfterFunc(60*time.Second, func() { _ = cmd.Process.Kill() }) // the Python run had timeout=60
	defer timer.Stop()
	err := cmd.Wait()
	if ee, ok := err.(*osexec.ExitError); ok {
		return ee.ExitCode(), so.String(), se.String()
	} else if err != nil {
		t.Fatal(err)
	}
	return 0, so.String(), se.String()
}

func TestScoutRolesEvaluatorRefusedOnLiveUnsetOrEmptyHome(t *testing.T) {
	e := rpRolesEnv(t)
	live := rpLiveHome(t)
	link := filepath.Join(e.Home, "live-link")
	if err := os.Symlink(live, link); err != nil {
		t.Fatal(err)
	}
	str := func(s string) *string { return &s }
	for _, home := range []*string{nil, str(""), str(live), str(live + "/"), str(link), str(link + "/"), str(live + "/../factory")} {
		rc, so, se := rpWhoami(t, e, "zz-test-ev", "evaluator", home)
		if rc != 3 {
			t.Fatalf("home %v: exit %d, want 3\n%s%s", home, rc, so, se)
		}
		contains(t, "stderr", se, "evaluator")
	}
}

func TestScoutRolesEvaluatorWorksWithTempHome(t *testing.T) {
	e := rpRolesEnv(t)
	rc, _, se := rpWhoami(t, e, "zz-test-ev", "evaluator", &e.Home)
	if rc != 0 {
		t.Fatalf("exit %d, want 0\n%s", rc, se)
	}
}

func TestScoutRolesOtherRolesUnaffected(t *testing.T) {
	e := rpRolesEnv(t)
	home := rpLiveHome(t) + "-nonexistent-zz"
	rc, _, se := rpWhoami(t, e, "zz-test-c", "coder", &home)
	if rc == 3 {
		t.Fatalf("a coder on a non-live home was refused (exit 3): %s", se)
	}
}
