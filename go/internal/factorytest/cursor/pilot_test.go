package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_cursor_pilot.py (18 cases; see ../ported_cursor.txt).
// adapted: the pm template is read from roles/settings/pm.json.in with the same three placeholder replacements the
// Python test makes; the JSON is parsed generically.

func TestCursorPilotTranslateBashAndEditMap(t *testing.T) {
	for in, want := range map[string]string{"Bash(sudo:*)": "Shell(sudo:*)", "Edit(/x/db/**)": "Write(/x/db/**)", "Write": "Write(**)"} {
		got, found := ch.TranslatePermissionToken(in)
		if !found || got != want {
			t.Fatalf("translate %q = %q,%v want %q", in, got, found, want)
		}
	}
	for _, in := range []string{"Advisor", "Workflow(coder-skills:multi-angle-review)"} {
		if got, found := ch.TranslatePermissionToken(in); found {
			t.Fatalf("translate %q = %q, want dropped", in, got)
		}
	}
}

func TestCursorPilotPmTemplateKeepsWriteDeny(t *testing.T) {
	text := readFile(t, filepath.Join(repoRoot(t), "roles", "settings", "pm.json.in"))
	text = strings.NewReplacer("@FACTORY_LIVE_HOME@", "/live", "@FACTORY_HOME_DIR@", "/zz-test-home",
		"@FACTORY_USER_CONFIG@", "/zz-test-home/.config").Replace(text)
	perms := sub(t, parseJSON(t, text), "permissions")
	got := ch.TranslatePermissions(strs(list(t, perms, "allow")), strs(list(t, perms, "deny")))
	found := false
	sudo := false
	for _, d := range got.Deny {
		found = found || d == "Write(**)"
		sudo = sudo || strings.HasPrefix(d, "Shell(sudo")
	}
	if !found || !sudo {
		t.Fatalf("deny = %v, want Write(**) and a Shell(sudo...) rule", got.Deny)
	}
}

func shellRun(t *testing.T, command string) (map[string]any, int) {
	t.Helper()
	r := adapterEvent(t, "beforeShellExecution", map[string]any{"command": command, "cwd": "/tmp"}, "pm", false, nil)
	return lastJSON(t, r.Stdout), r.Code
}

func TestCursorPilotAdapterAllowsHarmlessShell(t *testing.T) {
	body, code := shellRun(t, "echo ok")
	eq(t, "permission", body["permission"], any("allow"))
	eq(t, "exit", code, 0)
}

func TestCursorPilotAdapterBlocksHerdrViaBeforeShell(t *testing.T) {
	body, code := shellRun(t, "herdr server stop")
	eq(t, "permission", body["permission"], any("deny"))
	eq(t, "exit", code, 0) // deny is in the JSON for beforeShellExecution
	msg, _ := body["agent_message"].(string)
	contains(t, "agent_message", strings.ToLower(msg), "herdr")
}

func TestCursorPilotAdapterBlocksShellToolPreToolUse(t *testing.T) {
	r := adapterEvent(t, "preToolUse", map[string]any{"tool_name": "Shell", "tool_input": map[string]any{"command": "sudo id"}, "cwd": "/tmp"}, "pm", false, nil)
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

func TestCursorPilotAdapterBlocksWriteToBashrcWithPathKey(t *testing.T) {
	r := adapterEvent(t, "preToolUse", map[string]any{"tool_name": "Write", "tool_input": map[string]any{"path": passwdHome(t) + "/.bashrc"}, "cwd": "/tmp"}, "pm", false, nil)
	eq(t, "permission", lastJSON(t, r.Stdout)["permission"], any("deny"))
	eq(t, "exit", r.Code, 2)
}

func TestCursorPilotMaterializeWritesHooksCLIAndRule(t *testing.T) {
	tmp := t.TempDir()
	info := materialize(t, tmp, "pm", ch.MaterializeOpts{
		Deny: []string{"Bash(sudo:*)", "Edit", "Advisor"}, Allow: []string{"Bash(git status:*)"}})
	hooks := sub(t, readJSON(t, info.Hooks), "hooks")
	first := list(t, hooks, "beforeShellExecution")[0].(map[string]any)
	if fc, _ := first["failClosed"].(bool); !fc {
		t.Fatal("beforeShellExecution must fail closed")
	}
	cmd, _ := first["command"].(string)
	contains(t, "command", cmd, "cursor-guard-adapter.py")
	cli := readFile(t, info.CLI)
	deny := strs(list(t, sub(t, parseJSON(t, cli), "permissions"), "deny"))
	for _, want := range []string{"Shell(sudo:*)", "Write(**)"} {
		found := false
		for _, d := range deny {
			found = found || d == want
		}
		if !found {
			t.Errorf("deny %v lacks %s", deny, want)
		}
	}
	notContains(t, "cli.json", cli, "Advisor")
	contains(t, "rule", readFile(t, info.Rule), "alwaysApply: true")
	if !isFile(filepath.Join(tmp, "AGENTS.md")) {
		t.Fatal("AGENTS.md not written")
	}
}

func pilotEnv(t *testing.T) *ft.Env {
	t.Helper()
	e := ft.New(t)
	e.SeedGoal("G-001", "", "zz-test goal")
	e.SeedTeam("g-001-a", "G-001", "zz-test-lead")
	e.SeedAgent(ft.AgentRow{Name: "zz-test-director", Role: "director", Goal: "G-001", Team: "g-001-a"})
	e.SeedAgent(ft.AgentRow{Name: "zz-test-lead", Role: "lead", Goal: "G-001", Team: "g-001-a"})
	return e
}

func TestCursorPilotConfigRejectsEvaluator(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	r := e.FailWith(ft.Opts{Agent: director}, 2, "config", "cursor_pilot_roles", "evaluator")
	contains(t, "stderr", r.Stderr, "allow-list")
}

func configValue(e *ft.Env, key string) string {
	e.T.Helper()
	return strings.TrimSpace(e.OKWith(ft.Opts{Agent: director}, "config", key).Stdout)
}

func TestCursorPilotConfigAcceptsPm(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	e.OKWith(ft.Opts{Agent: director}, "config", "cursor_pilot_roles", "pm")
	eq(t, "value", configValue(e, "cursor_pilot_roles"), "pm")
}

func TestCursorPilotConfigAcceptsCoder(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	e.OKWith(ft.Opts{Agent: director}, "config", "cursor_pilot_roles", "coder")
	eq(t, "value", configValue(e, "cursor_pilot_roles"), "coder")
}

func TestCursorPilotStatusListsMatrix(t *testing.T) {
	e := pilotEnv(t)
	r := e.OKWith(ft.Opts{Agent: director}, "cursor-pilot", "status")
	contains(t, "stdout", r.Stdout, "WORKS")
	contains(t, "stdout", r.Stdout, "NEEDS_WORK")
}

func TestCursorPilotProbePasses(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // probe runs the Python adapter
	r := e.OKWith(ft.Opts{Agent: director}, "cursor-pilot", "probe", "--role", "pm")
	contains(t, "stdout", r.Stdout, "PROBE ok")
}

func TestCursorPilotPrepareMaterializes(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython()
	tmp := t.TempDir()
	r := e.OKWith(ft.Opts{Agent: director}, "cursor-pilot", "prepare", "--role", "pm", "--cwd", tmp)
	contains(t, "stdout", r.Stdout, "PREPARED")
	for _, f := range []string{"hooks.json", "cli.json"} {
		if !isFile(filepath.Join(tmp, ".cursor", f)) {
			t.Errorf(".cursor/%s not written", f)
		}
	}
}

func TestCursorPilotAgentRuntimesConfig(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "pm=cursor")
	eq(t, "value", configValue(e, "agent_runtimes"), "pm=cursor")
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "coder=cursor")
	e.FailWith(ft.Opts{Agent: director}, 2, "config", "agent_runtimes", "evaluator=cursor")
}

func TestCursorPilotAgentRuntimesStarCursor(t *testing.T) {
	e := pilotEnv(t)
	e.NeedPython() // `config <key> <value>` still runs in Python
	e.OKWith(ft.Opts{Agent: director}, "config", "agent_runtimes", "*=cursor")
	val := configValue(e, "agent_runtimes")
	for _, want := range []string{"boss=cursor", "pm=cursor", "coder=cursor"} {
		contains(t, "agent_runtimes", val, want)
	}
	notContains(t, "agent_runtimes", val, "evaluator=cursor")
}

func TestCursorPilotSpawnCursorPmHerdrKind(t *testing.T) {
	e := cursorEnv(t, "pm=cursor")
	before := len(e.Calls("herdr"))
	r := e.OKWith(ft.Opts{Agent: director}, "spawn", "pm", "--name", "zz-cursor-pm", "--goal", "G-001", "--team", "g-001-a")
	for _, want := range []string{"runtime=cursor", "kind=cursor", "SPAWNED zz-cursor-pm"} {
		contains(t, "stdout", r.Stdout, want)
	}
	starts := startsSince(e, before)
	eq(t, "agent starts", len(starts), 1)
	argv := starts[0]
	eq(t, "--kind", flagValue(t, argv, "--kind"), "cursor")
	head := argv
	if i := indexOf(argv, "--"); i >= 0 {
		head = argv[:i]
	}
	if indexOf(head, "claude") >= 0 {
		t.Fatalf("claude in the herdr args before --: %v", head)
	}
	row := e.SQL("SELECT spawn_args, pane_id, status FROM agents WHERE name='zz-cursor-pm'")[0]
	sa := parseJSON(t, row[0])
	eq(t, "runtime", sa["runtime"], any("cursor"))
	eq(t, "launch", sa["launch"], any("herdr-kind"))
	eq(t, "herdr_kind", sa["herdr_kind"], any("cursor"))
	if row[1] == "NULL" {
		t.Fatal("pane_id is NULL")
	}
	eq(t, "status", row[2], "live")
	prompts := 0
	for _, c := range e.Calls("herdr")[before:] {
		if len(c.Argv) >= 2 && c.Argv[0] == "agent" && c.Argv[1] == "prompt" {
			prompts++
		}
	}
	if prompts == 0 {
		t.Fatal("kickoff must go via herdr agent prompt")
	}
}

func TestCursorPilotSpawnStarCursorHqAndWorker(t *testing.T) {
	e := cursorEnv(t, "*=cursor")
	before := len(e.Calls("herdr"))
	e.OKWith(ft.Opts{Agent: director}, "spawn", "lead", "--name", "zz-cursor-lead", "--goal", "G-001", "--team", "g-001-a")
	e.OKWith(ft.Opts{Agent: director}, "spawn", "coder", "--name", "zz-cursor-coder", "--goal", "G-001", "--team", "g-001-a")
	var kinds []string
	for _, a := range startsSince(e, before) {
		kinds = append(kinds, flagValue(t, a, "--kind"))
	}
	eq(t, "kinds", strings.Join(kinds, ","), "cursor,cursor")
	lead := e.SQL("SELECT spawn_args, cwd FROM agents WHERE name='zz-cursor-lead'")[0]
	sa := parseJSON(t, lead[0])
	eq(t, "herdr_kind", sa["herdr_kind"], any("cursor"))
	ws, _ := sa["cursor_workspace"].(string)
	if !strings.HasSuffix(ws, "/factory/cursor/zz-cursor-lead") {
		t.Fatalf("cursor_workspace = %q", ws)
	}
	if !isFile(filepath.Join(ws, ".cursor", "hooks.json")) {
		t.Fatal("hooks.json missing in the isolated policy workspace")
	}
	// HQ cwd is FACTORY_HOME; policy must not land in the shared checkout root's .cursor/
	if isFile(filepath.Join(lead[1], ".cursor", "hooks.json")) {
		t.Fatal("policy landed in the shared checkout")
	}
}

func TestCursorPilotBuildArgvPrintForce(t *testing.T) {
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "agent"), "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	argv := ch.BuildCursorArgv(ch.ArgvOpts{Cwd: "/tmp/ws", Model: "claude-opus-5-5", Prompt: "hi", PrintMode: true, Force: true})
	eq(t, "argv[0]", argv[0], filepath.Join(bin, "agent"))
	for _, want := range []string{"--print", "--force", "--trust", "hi"} {
		if indexOf(argv, want) < 0 {
			t.Errorf("argv %v lacks %s", argv, want)
		}
	}
}
