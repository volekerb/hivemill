package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_cursor_workspace.py (11 cases; see ../ported_cursor.txt).
// adapted: Python patches scout.agent_runtime.ch.policy_workspace so the spawn targets a worktree; Go cannot patch
// the Python spawn, so the refusal is driven twice for real: the Go guard AssertPolicyTargetAllowed on a worktree
// target, and a spawn whose XDG_STATE_HOME points under FACTORY_HOME (the policy dir is refused, exit 3, no start).

func spawnLead(t *testing.T, e *ft.Env, role, name string, extra ...string) ft.Result {
	t.Helper()
	args := append([]string{"spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a"}, extra...)
	return asDirector(e, args...)
}

func TestCursorWorkspaceLeadSpawnArgvWorkspaceAndAddDir(t *testing.T) {
	e := cursorEnv(t, "*=cursor")
	before := len(e.Calls("herdr"))
	name := "zz-cursor-lead-ws"
	if r := spawnLead(t, e, "lead", name); r.Exit != 0 {
		t.Fatalf("spawn: %d\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	starts := startsSince(e, before)
	eq(t, "agent starts", len(starts), 1)
	aa := agentArgv(t, starts[0])
	ws := flagValue(t, aa, "--workspace")
	if !strings.HasSuffix(ws, "/state/factory/cursor/"+name) {
		t.Fatalf("workspace = %s", ws)
	}
	if indexOf(aa, "--add-dir") < 0 {
		t.Fatalf("no --add-dir in %v", aa)
	}
	if exists(filepath.Join(e.Home, ".cursor")) || exists(filepath.Join(e.Home, "AGENTS.md")) {
		t.Fatal("policy files leaked into FACTORY_HOME")
	}
	hooks := filepath.Join(ws, ".cursor", "hooks.json")
	if !isFile(hooks) || !failClosedOK(t, hooks) {
		t.Fatalf("hooks.json missing or not fail-closed: %s", hooks)
	}
	cmd := hookCommand(t, hooks)
	contains(t, "hook command", cmd, "--hq")
	notContains(t, "hook command", cmd, "--role pm")
}

func TestCursorWorkspaceSecondMaterializeRefreshesHooksMtime(t *testing.T) {
	e := ft.New(t)
	state := filepath.Join(e.UserHome, ".local", "state")
	ws, err := ch.IsolatedPolicyDir(e.Home, "zz-cursor-remat", state)
	ok(t, err)
	hooks := filepath.Join(ws, ".cursor", "hooks.json")
	opts := ch.MaterializeOpts{HQ: true, FactoryHome: e.Home}
	materialize(t, ws, "lead", opts)
	st1, err := os.Stat(hooks)
	ok(t, err)
	time.Sleep(50 * time.Millisecond)
	materialize(t, ws, "lead", opts)
	st2, err := os.Stat(hooks)
	ok(t, err)
	if st2.ModTime().Before(st1.ModTime()) {
		t.Fatalf("mtime went backwards: %v -> %v", st1.ModTime(), st2.ModTime())
	}
	m, err := filepath.Glob(filepath.Join(state, "factory", "cursor", "zz-cursor-remat"))
	ok(t, err)
	eq(t, "policy dirs", len(m), 1)
}

func TestCursorWorkspaceClaudePmUnchangedWhenRuntimesEmpty(t *testing.T) {
	e := cursorEnv(t, "")
	before := len(e.Calls("herdr"))
	if r := spawnLead(t, e, "pm", "zz-claude-pm"); r.Exit != 0 {
		t.Fatalf("spawn: %d\n%s%s", r.Exit, r.Stdout, r.Stderr)
	}
	starts := startsSince(e, before)
	eq(t, "agent starts", len(starts), 1)
	eq(t, "--kind", flagValue(t, starts[0], "--kind"), "claude")
}

func retireDirector(t *testing.T, e *ft.Env) {
	t.Helper()
	// T-1265: one live director per org; the fixture director stops counting so a spawned one can be checked
	sqlExec(t, e, "UPDATE agents SET status='exited' WHERE name='zz-test-director'")
}

func TestCursorWorkspaceHqRolesAddDirRootsClean(t *testing.T) {
	e := cursorEnv(t, "*=cursor")
	retireDirector(t, e)
	before := len(e.Calls("herdr"))
	for _, c := range [][2]string{{"director", "zz-c-dir"}, {"boss", "zz-c-boss"}, {"lead", "zz-c-lead"}} {
		if r := spawnLead(t, e, c[0], c[1]); r.Exit != 0 {
			t.Fatalf("spawn %s: %d\n%s%s", c[0], r.Exit, r.Stdout, r.Stderr)
		}
	}
	starts := startsSince(e, before)
	eq(t, "agent starts", len(starts), 3)
	for _, s := range starts {
		aa := agentArgv(t, s)
		ws := flagValue(t, aa, "--workspace")
		if !strings.HasPrefix(ws, stateCursorDir(e)) {
			t.Errorf("workspace %s is not under %s", ws, stateCursorDir(e))
		}
		for _, d := range addDirs(aa) {
			p, err := filepath.EvalSymlinks(d)
			ok(t, err)
			if exists(filepath.Join(p, ".cursor")) || exists(filepath.Join(p, "AGENTS.md")) {
				t.Errorf("--add-dir root %s carries policy files", d)
			}
		}
	}
}

func TestCursorWorkspaceSpawnRefusedWhenAddDirHasCursorDir(t *testing.T) {
	e := cursorEnv(t, "*=cursor")
	mkdir(t, filepath.Join(e.Home, ".cursor"))
	before := len(e.Calls("herdr"))
	r := e.FailWith(ft.Opts{Agent: director}, 3, "spawn", "lead", "--name", "zz-leak-lead", "--goal", "G-001", "--team", "g-001-a")
	contains(t, "stderr", strings.ToLower(r.Stderr), "add-dir")
	eq(t, "agent starts", len(startsSince(e, before)), 0)
}

func TestCursorWorkspaceSpawnRefusedWhenAddDirHasAgentsMd(t *testing.T) {
	e := cursorEnv(t, "*=cursor")
	writeFile(t, filepath.Join(e.Home, "AGENTS.md"), "# leak\n", 0o644)
	before := len(e.Calls("herdr"))
	retireDirector(t, e)
	r := e.FailWith(ft.Opts{Agent: director}, 3, "spawn", "director", "--name", "zz-leak-dir", "--goal", "G-001", "--team", "g-001-a")
	contains(t, "stderr", strings.ToLower(r.Stderr), "add-dir")
	eq(t, "agent starts", len(startsSince(e, before)), 0)
}

func TestCursorWorkspacePolicyTargetFactoryHomeRefused(t *testing.T) {
	home := mkdir(t, filepath.Join(realTemp(t), "factory"))
	_, err := ch.MaterializeCursorPolicy(home, "boss", ch.MaterializeOpts{HooksDir: hooksDir(t), SystemPrompt: "# b\n", FactoryHome: home})
	if err == nil {
		t.Fatal("materialising into FACTORY_HOME must be refused")
	}
	contains(t, "error", err.Error(), "FACTORY_HOME")
}

func TestCursorWorkspacePolicyTargetUnderHomeOutsideRunCursorRefused(t *testing.T) {
	home := mkdir(t, filepath.Join(realTemp(t), "factory"))
	bad := mkdir(t, filepath.Join(home, "db"))
	if _, err := ch.MaterializeCursorPolicy(bad, "coder", ch.MaterializeOpts{HooksDir: hooksDir(t), SystemPrompt: "# c\n", FactoryHome: home}); err == nil {
		t.Fatal("materialising under FACTORY_HOME must be refused")
	}
}

func TestCursorWorkspacePolicyTargetWorktreeStillRefused(t *testing.T) {
	home := mkdir(t, filepath.Join(realTemp(t), "factory"))
	wt := mkdir(t, filepath.Join(home, "worktrees", "G-001-T-x"))
	if _, err := ch.MaterializeCursorPolicy(wt, "coder", ch.MaterializeOpts{HooksDir: hooksDir(t), SystemPrompt: "# c\n", FactoryHome: home}); err == nil {
		t.Fatal("materialising into a worktree must be refused")
	}
}

func TestCursorWorkspaceCrossRoleHooksMatchRole(t *testing.T) {
	tmp := t.TempDir()
	mk := func(name, role string, hq bool) string {
		d := mkdir(t, filepath.Join(tmp, name))
		return hookCommand(t, materialize(t, d, role, ch.MaterializeOpts{HQ: hq}).Hooks)
	}
	pm, coder, lead := mk("pm-ws", "pm", false), mk("coder-ws", "coder", false), mk("lead-ws", "lead", true)
	contains(t, "pm", pm, "--role pm")
	notContains(t, "pm", pm, "--role coder")
	contains(t, "coder", coder, "--role coder")
	notContains(t, "coder", coder, "--role pm")
	contains(t, "lead", lead, "--hq")
	contains(t, "lead", lead, "--role lead")
	notContains(t, "lead", lead, "--role pm")
}

func TestCursorWorkspaceSpawnExits3WhenPolicyTargetIsWorktree(t *testing.T) {
	e := cursorEnv(t, "coder=cursor")
	wt := mkdir(t, filepath.Join(e.Home, "worktrees", "G-001-T-0822"))
	// the guard itself: a worktree is never a policy target
	err := ch.AssertPolicyTargetAllowed(e.Home, wt)
	if err == nil {
		t.Fatal("a worktree must be refused as a policy target")
	}
	contains(t, "error", strings.ToLower(err.Error()), "worktree")
	// and the spawn: a state root under FACTORY_HOME leaves no valid policy dir, so the spawn is refused
	before := len(e.Calls("herdr"))
	r := e.FailWith(ft.Opts{Agent: director, Env: map[string]string{"XDG_STATE_HOME": filepath.Join(e.Home, "worktrees", "state")}}, 3,
		"spawn", "coder", "--name", "zz-bad-coder", "--goal", "G-001", "--team", "g-001-a", "--cwd", wt)
	contains(t, "stderr", strings.ToLower(r.Stderr), "work tree")
	eq(t, "agent starts", len(startsSince(e, before)), 0)
}
