package agentruntime

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

const allowList = "['boss', 'cleaner', 'coder', 'director', 'judge', 'lead', 'pm', 'qa', 'reviewer', 'scout', 'staff-engineer']"

// Expected values are the output of scout/agent_runtime.py parse_runtimes (python3 -I).
func TestParseRuntimesParity(t *testing.T) {
	cases := []struct {
		rt, pilot string
		want      map[string]string
		err       string
	}{
		{"pm=cursor", "", map[string]string{"pm": "cursor"}, ""},
		{"all=claude", "", map[string]string{}, ""},
		{"", "pm,coder", map[string]string{"pm": "cursor", "coder": "cursor"}, ""},
		{"pm=claude", "pm", map[string]string{"pm": "claude"}, ""},
		{"pm", "", nil, "agent_runtimes entry 'pm' must be role=runtime (known runtimes: claude, cursor; use *=cursor or all=cursor for every Cursor-capable role)"},
		{"pm=bogus", "", nil, "unknown runtime 'bogus'; known: claude, cursor"},
		{"evaluator=cursor", "", nil, "runtime 'cursor' allow-list is " + allowList + "; not 'evaluator'"},
		{"x=cursor", "", nil, "runtime 'cursor' allow-list is " + allowList + "; not 'x'"},
		{"", "zz", nil, "cursor_pilot_roles: 'zz' not in cursor allow-list (boss, cleaner, coder, director, judge, lead, pm, qa, reviewer, scout, staff-engineer)"},
	}
	for _, c := range cases {
		got, err := ParseRuntimes(c.rt, c.pilot)
		if c.err != "" {
			if err == nil || err.Error() != c.err {
				t.Errorf("%q/%q: err %v want %s", c.rt, c.pilot, err, c.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q/%q: got %v %v want %v", c.rt, c.pilot, got, err, c.want)
		}
	}
	all, err := ParseRuntimes("*=cursor", "")
	if err != nil || len(all) != 11 || all["pm"] != "cursor" || all["evaluator"] != "" {
		t.Fatalf("*=cursor: %v %v", all, err)
	}
	// Unicode whitespace around tokens is stripped like Python str.strip()
	got, err := ParseRuntimes(" pm\x0b= cursor ,", "")
	if err != nil || got["pm"] != "cursor" {
		t.Fatalf("unicode strip: %v %v", got, err)
	}
}

func TestResolveRuntime(t *testing.T) {
	if rt, err := ResolveRuntime("pm", "pm=cursor", ""); err != nil || rt != "cursor" {
		t.Fatal(rt, err)
	}
	if rt, err := ResolveRuntime("coder", "pm=cursor", ""); err != nil || rt != "claude" {
		t.Fatal(rt, err)
	}
	if _, err := ResolveRuntime("pm", "bad", ""); err == nil {
		t.Fatal("bad config must error")
	}
}

func TestPlanClaude(t *testing.T) {
	ctx := SpawnContext{Role: "coder", Name: "c1", Cwd: "/w", Model: "m", SettingsPath: "/s.json", AppendPromptFile: "/p",
		Effort: []string{"--effort", "high"}, AddDirs: []string{"/a"}, PluginDirs: []string{"/pl"}, Fresh: true,
		NewUUID: func() string { return "U" }}
	p, err := PlanSpawn("claude", ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--model", "m", "--permission-mode", "auto", "--settings", "/s.json", "--append-system-prompt-file", "/p",
		"--effort", "high", "--add-dir", "/a", "-n", "c1", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
		"--plugin-dir", "/pl", "--session-id", "U", "--system-prompt-snapshot", "off"}
	if !reflect.DeepEqual(p.Argv, want) || p.SessionID != "U" || p.HerdrKind != "claude" || p.Launch != "herdr-kind" {
		t.Fatalf("%+v", p)
	}
	ctx.Role, ctx.Evaluator, ctx.Resume = "boss", true, "R"
	p, _ = PlanSpawn("claude", ctx)
	want = []string{"--model", "m", "--permission-mode", "auto", "--settings", "/s.json", "--append-system-prompt-file", "/p",
		"--effort", "high", "-n", "c1", "--plugin-dir", "/pl", "--resume", "R", "--system-prompt-snapshot", "off"}
	if !reflect.DeepEqual(p.Argv, want) || p.SessionID != "R" {
		t.Fatalf("%+v", p)
	}
	if _, err := PlanSpawn("codex", ctx); err == nil || err.Error() != "no planner for runtime 'codex'" {
		t.Fatalf("%v", err)
	}
}

func TestPlanCursorRefusesEvaluatorAndMaterializes(t *testing.T) {
	if _, err := PlanSpawn("cursor", SpawnContext{Role: "coder", Evaluator: true}); err == nil ||
		err.Error() != "runtime cursor: evaluator/skilleval stays on Claude" {
		t.Fatalf("%v", err)
	}
	if _, err := PlanSpawn("cursor", SpawnContext{Role: "evaluator"}); err == nil {
		t.Fatal("evaluator role must be refused")
	}
	root := t.TempDir()
	home, state, hooks, bin := filepath.Join(root, "home"), filepath.Join(root, "state"), filepath.Join(root, "hooks"), filepath.Join(root, "bin")
	wt := filepath.Join(home, "worktrees", "G-1-T-1")
	for _, d := range []string{wt, state, hooks, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(bin, "agent"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("FACTORY_HOME", home)
	ctx := SpawnContext{Role: "lead", Name: "lead-1", Cwd: wt, Model: "m", PromptText: "hi", Home: home, HooksDir: hooks,
		Allow: []string{"Bash(ls)"}, StateRoot: state, NewUUID: func() string { return "U" }, Sandbox: true,
		PluginDirs: []string{"/pl"}, AddDirs: []string{"/x"}}
	p, err := PlanSpawn("cursor", ctx)
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := filepath.EvalSymlinks(filepath.Join(state, "factory", "cursor", "lead-1"))
	wtR, _ := filepath.EvalSymlinks(wt)
	homeR, _ := filepath.EvalSymlinks(home)
	want := []string{"--workspace", ws, "--model", "m", "--trust", "--force", "--sandbox", "enabled", "--plugin-dir", "/pl",
		"--add-dir", wtR, "--add-dir", "/x", "--add-dir", homeR}
	if !reflect.DeepEqual(p.Argv, want) || p.SessionID != "U" || p.HerdrKind != "cursor" || p.Binary != filepath.Join(bin, "agent") {
		t.Fatalf("%+v", p)
	}
	if !p.Prepared.HQ || !strings.Contains(strings.Join(p.Notes, "|"), "HQ role: adapter --hq") {
		t.Fatalf("lead must be HQ: %+v", p)
	}
	if _, err := os.Stat(filepath.Join(ws, ".cursor", "hooks.json")); err != nil {
		t.Fatal(err)
	}
	// no agent binary on PATH: refused with Python's text
	t.Setenv("PATH", filepath.Join(root, "empty"))
	if _, err := PlanSpawn("cursor", ctx); err == nil || !strings.HasPrefix(err.Error(), "cursor runtime: agent/cursor-agent not on PATH") {
		t.Fatalf("%v", err)
	}
}

func TestUUID4Shape(t *testing.T) {
	u, err := UUID4()
	if err != nil || !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(u) {
		t.Fatalf("%q %v", u, err)
	}
}

func TestLaunchSubprocessLogs(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "logs", "a.log")
	cmd, err := LaunchSubprocess(SpawnPlan{Argv: []string{"/bin/sh", "-c", "echo out; echo err 1>&2"}}, dir, os.Environ(), log)
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	if !strings.Contains(string(b), "out\n") || !strings.Contains(string(b), "err\n") {
		t.Fatalf("log %q", b)
	}
	if _, err := LaunchSubprocess(SpawnPlan{Argv: []string{filepath.Join(dir, "nope")}}, dir, nil, log); err == nil {
		t.Fatal("missing binary must error")
	}
}

func TestWorksMatrixFirstRow(t *testing.T) {
	m := WorksMatrix()
	if len(m) != 13 || m[0].Item != "Multi-runtime registry (claude default, cursor extension)" {
		t.Fatalf("%d %v", len(m), m[0])
	}
}

func FuzzParseRuntimes(f *testing.F) {
	for _, s := range []string{"pm=cursor", "*=cursor", "all=claude", "pm", "", "a=b,c", " pm=cursor"} {
		f.Add(s, "pm,coder")
	}
	f.Fuzz(func(t *testing.T, a, b string) { ParseRuntimes(a, b) })
}
