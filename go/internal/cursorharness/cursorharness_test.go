package cursorharness

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The expected values below are the output of scout/cursor_harness.py (python3 -I) for the same inputs.
func TestTranslatePermissionTokenParity(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"Bash(git *)", "Shell(git *)", true},
		{"Bash(x) ", "Shell(x)", true},    // NBSP: Python \s matches it
		{"Bash(x)\x0b", "Shell(x)", true}, // \v
		{"Bash(x)\n", "Shell(x)", true},
		{"Bash(a\nb)", "", false},
		{"Edit", "Write(**)", true},
		{"Edit(src/**)  ", "Write(src/**)", true},
		{"Read(٣)", "Read(٣)", true}, // U+0663 is carried through
		{"Workflow(x)", "", false},
		{"Advisor", "", false},
		{"Bash(x)y", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := TranslatePermissionToken(c.in)
		if got != c.want || ok != c.ok {
			t.Errorf("%q: got (%q,%v) want (%q,%v)", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestUnicodeStrip(t *testing.T) {
	got, err := ParsePilotRoles(" pm\x0b, coder ,pm")
	if err != nil || !reflect.DeepEqual(got, []string{"pm", "coder"}) {
		t.Fatalf("got %v %v", got, err)
	}
	if MapModel(" x\x0b") != "x" || MapModel(" ") != "auto" || MapModel("") != "auto" {
		t.Fatal("MapModel must strip like Python")
	}
}

func TestParsePilotRolesError(t *testing.T) {
	_, err := ParsePilotRoles("pm,zzz,evaluator")
	want := "cursor_pilot_roles: 'zzz', 'evaluator' not in cursor allow-list (boss, cleaner, coder, director, judge, lead, pm, qa, reviewer, scout, staff-engineer)"
	if err == nil || err.Error() != want {
		t.Fatalf("got %v", err)
	}
	if RoleUsesCursor("pm,zzz", "pm") || !RoleUsesCursor("pm", "pm") {
		t.Fatal("RoleUsesCursor")
	}
}

func TestAdapterCommand(t *testing.T) {
	got := AdapterCommand("/h/o ks", "pm", "preToolUse", true)
	want := "python3 '/h/o ks/cursor-guard-adapter.py' --hq --role pm --event preToolUse"
	if got != want {
		t.Fatalf("got %s", got)
	}
}

func TestBuildCursorArgv(t *testing.T) {
	dir := t.TempDir()
	argv := BuildCursorArgv(ArgvOpts{Cwd: dir, Model: "m", Prompt: "p", Resume: "r", PluginDirs: []string{"a"},
		AddDirs: []string{"b"}, PrintMode: true, Force: true, Sandbox: true})
	want := []string{"--workspace", resolve(dir), "--model", "m", "--trust", "--print", "--output-format", "text", "--force",
		"--sandbox", "enabled", "--resume", "r", "--plugin-dir", "a", "--add-dir", "b", "p"}
	if !reflect.DeepEqual(argv[1:], want) {
		t.Fatalf("got %v", argv)
	}
	if !reflect.DeepEqual(AddDirsFromArgv(argv), []string{"b"}) {
		t.Fatal("AddDirsFromArgv")
	}
}

func TestMaterializeAndGuards(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	state := filepath.Join(root, "state")
	hooks := filepath.Join(root, "hooks")
	for _, d := range []string{home, state, hooks} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	// a task worktree under home: policy goes to STATE/factory/cursor/<name>
	wt := filepath.Join(home, "worktrees", "G-1-T-1")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	ws, err := PolicyWorkspace(home, "coder-1", wt, state)
	if err != nil || ws != resolve(filepath.Join(state, "factory", "cursor", "coder-1")) {
		t.Fatalf("ws=%s err=%v", ws, err)
	}
	if err := AssertPolicyTargetAllowed(home, wt); err == nil || !strings.Contains(err.Error(), "factory worktree") {
		t.Fatalf("worktree must be refused: %v", err)
	}
	if err := AssertPolicyTargetAllowed(home, filepath.Join(home, "x")); err == nil || !strings.Contains(err.Error(), "under FACTORY_HOME") {
		t.Fatalf("home must be refused: %v", err)
	}
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := AssertPolicyTargetAllowed(home, other); err == nil || !strings.Contains(err.Error(), "git work tree") {
		t.Fatalf("git tree must be refused: %v", err)
	}
	if err := AssertPolicyTargetAllowed(home, state); err != nil {
		t.Fatalf("a plain dir must be accepted: %v", err)
	}
	p, err := MaterializeCursorPolicy(ws, "pm", MaterializeOpts{HooksDir: hooks, Allow: []string{"Bash(ls)", "Edit"},
		Deny: []string{"Advisor"}, SystemPrompt: "role text\n\n", FactoryHome: home})
	if err != nil {
		t.Fatal(err)
	}
	if p.Notes[0] != "all Claude deny rules dropped in translation (check Advisor/Workflow-only templates)" || len(p.Notes) != 4 {
		t.Fatalf("notes %v", p.Notes)
	}
	cli, _ := os.ReadFile(p.CLI)
	wantCLI := "{\n  \"permissions\": {\n    \"allow\": [\n      \"Shell(ls)\",\n      \"Write(**)\"\n    ],\n    \"deny\": []\n  },\n" +
		"  \"attribution\": {\n    \"attributeCommitsToAgent\": false,\n    \"attributePRsToAgent\": false\n  }\n}\n"
	if string(cli) != wantCLI {
		t.Fatalf("cli.json:\n%s", cli)
	}
	rule, _ := os.ReadFile(p.Rule)
	if !strings.HasSuffix(string(rule), "\n\nrole text\n") {
		t.Fatalf("rule %q", rule)
	}
	// an add-dir root with policy files is refused
	if err := AssertAddDirRootsClean([]string{ws}); err == nil {
		t.Fatal("leak must be refused")
	}
	if err := AssertAddDirRootsClean([]string{t.TempDir()}); err != nil {
		t.Fatal(err)
	}
}

func TestIsolatedPolicyDirRefusesGitTree(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := IsolatedPolicyDir(filepath.Join(t.TempDir(), "home"), "a", root); err == nil {
		t.Fatal("state root inside a git tree must be refused")
	}
}

func FuzzTranslatePermissionToken(f *testing.F) {
	for _, s := range []string{"Bash(git *)", "Bash(x) ", "Edit", "Read(٣)", "Workflow(x)", "Bash(a\nb)", ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { TranslatePermissionToken(s) })
}

func FuzzParsePilotRoles(f *testing.F) {
	for _, s := range []string{"pm", " pm , coder,pm", "pm,zzz", "", ",,"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { ParsePilotRoles(s) })
}

// TestResolveRealpathParity pins resolve to Python's os.path.realpath (non-strict) on a symlinked parent of a
// missing path, a dangling symlink and a loop. Expected values are the Python 3.13 output (D = temp dir).
func TestResolveRealpathParity(t *testing.T) {
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.Mkdir(filepath.Join(d, "real"), 0o755))
	must(os.Symlink(filepath.Join(d, "real"), filepath.Join(d, "lnk")))
	must(os.Symlink(filepath.Join(d, "missing"), filepath.Join(d, "dang")))
	must(os.Symlink("l", filepath.Join(d, "l")))
	cases := map[string]string{
		"lnk/missing/x": "real/missing/x", // python: D/real/missing/x
		"dang":          "missing",        // python: D/missing
		"dang/x":        "missing/x",      // python: D/missing/x
		"l":             "l",              // python: D/l
		"l/x":           "l/x",            // python: D/l/x
		"lnk/../x":      "x",              // python: D/x (the ".." is applied after the link is followed)
	}
	for in, want := range cases {
		if got := resolve(filepath.Join(d, in)); got != filepath.Join(d, want) {
			t.Errorf("resolve(%s)=%s want %s", in, got, filepath.Join(d, want))
		}
	}
	// The three policy guards see through a symlinked parent of a missing path.
	home := filepath.Join(d, "home")
	must(os.MkdirAll(filepath.Join(home, "worktrees"), 0o755))
	must(os.Symlink(filepath.Join(home, "worktrees"), filepath.Join(d, "wt")))
	if !IsFactoryWorktreePath(home, filepath.Join(d, "wt", "missing", "x")) {
		t.Error("IsFactoryWorktreePath missed a symlinked parent of a missing path")
	}
	if err := AssertPolicyTargetAllowed(home, filepath.Join(d, "wt", "missing")); err == nil {
		t.Error("AssertPolicyTargetAllowed accepted a symlinked parent into worktrees")
	}
	must(os.Symlink(home, filepath.Join(d, "hl")))
	if _, err := IsolatedPolicyDir(home, "a", filepath.Join(d, "hl", "state")); err == nil {
		t.Error("IsolatedPolicyDir accepted a state root under FACTORY_HOME through a symlink")
	}
	if ws, err := PolicyWorkspace(home, "a", filepath.Join(d, "wt", "missing"), filepath.Join(d, "st")); err != nil ||
		ws != filepath.Join(d, "st", "factory", "cursor", "a") {
		t.Errorf("PolicyWorkspace=%q,%v want isolated dir", ws, err)
	}
}
