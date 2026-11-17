package cursor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
)

// Port of tests/test_cursor_worktree_policy.py (7 cases; see ../ported_cursor.txt).
// adapted: SpawnWorktreePolicyRefusal patches scout.cursor_harness.policy_workspace in Python; see the note in
// workspace_test.go for how the refusal is driven here.

func wtEnv(t *testing.T, runtimes, wtName string) (*ft.Env, string) {
	t.Helper()
	e := cursorEnv(t, runtimes)
	return e, mkdir(t, filepath.Join(e.Home, "worktrees", wtName))
}

func spawnWT(t *testing.T, e *ft.Env, role, name, cwd string) {
	t.Helper()
	args := []string{"spawn", role, "--name", name, "--goal", "G-001", "--team", "g-001-a"}
	if cwd != "" {
		args = append(args, "--cwd", cwd)
	}
	e.OKWith(ft.Opts{Agent: director}, args...)
}

func hooksOf1(e *ft.Env, name string) string {
	return filepath.Join(stateCursorDir(e), name, ".cursor", "hooks.json")
}

func TestCursorWorktreePolicyCoderPolicyNotInWorktree(t *testing.T) {
	e, wt := wtEnv(t, "*=cursor", "G-001-T-0833")
	mkdir(t, filepath.Join(wt, "src"))
	spawnWT(t, e, "coder", "zz-wt-coder", wt)
	if exists(filepath.Join(wt, ".cursor")) || exists(filepath.Join(wt, "AGENTS.md")) {
		t.Fatal("policy was written into the task worktree")
	}
	if !isFile(hooksOf1(e, "zz-wt-coder")) {
		t.Fatal("coder hooks.json missing in the isolated policy dir")
	}
}

func TestCursorWorktreePolicyReviewerSpawnDoesNotClobberCoderPolicy(t *testing.T) {
	e, wt := wtEnv(t, "*=cursor", "G-001-T-0833")
	mkdir(t, filepath.Join(wt, "src"))
	spawnWT(t, e, "coder", "zz-wt-coder", wt)
	coderHooks := hooksOf1(e, "zz-wt-coder")
	before, err := os.ReadFile(coderHooks)
	ok(t, err)
	contains(t, "coder hook", hookCommand(t, coderHooks), "--role coder")

	spawnWT(t, e, "reviewer", "zz-wt-reviewer", wt)
	if exists(filepath.Join(wt, ".cursor")) || exists(filepath.Join(wt, "AGENTS.md")) {
		t.Fatal("the reviewer spawn wrote policy into the worktree")
	}
	after, err := os.ReadFile(coderHooks)
	ok(t, err)
	if !bytes.Equal(before, after) {
		t.Fatal("the reviewer spawn changed the coder's hooks.json")
	}
	contains(t, "coder hook", hookCommand(t, coderHooks), "--role coder")
	rev := hooksOf1(e, "zz-wt-reviewer")
	if !isFile(rev) || rev == coderHooks {
		t.Fatalf("reviewer hooks.json = %s", rev)
	}
	contains(t, "reviewer hook", hookCommand(t, rev), "--role reviewer")
}

func TestCursorWorktreePolicyMaterializeRefusalWorktreeTargetRaises(t *testing.T) {
	home := realTemp(t)
	wt := mkdir(t, filepath.Join(home, "worktrees", "G-001-T-x"))
	_, err := ch.MaterializeCursorPolicy(wt, "coder", ch.MaterializeOpts{HooksDir: hooksDir(t), SystemPrompt: "# c\n", FactoryHome: home})
	if err == nil {
		t.Fatal("materialising into a worktree must be refused")
	}
	contains(t, "error", strings.ToLower(err.Error()), "worktree")
}

func TestCursorWorktreePolicySpawnExits3WhenPolicyTargetIsWorktree(t *testing.T) {
	e, wt := wtEnv(t, "coder=cursor", "G-001-T-refuse")
	before := len(e.Calls("herdr"))
	r := e.FailWith(ft.Opts{Agent: director, Env: map[string]string{"XDG_STATE_HOME": filepath.Join(e.Home, "worktrees", "state")}}, 3,
		"spawn", "coder", "--name", "zz-bad-coder", "--goal", "G-001", "--team", "g-001-a", "--cwd", wt)
	contains(t, "stderr", strings.ToLower(r.Stderr), "work tree")
	eq(t, "agent starts", len(startsSince(e, before)), 0)
}

func TestCursorWorktreePolicyHqPmAtFactoryHomeUsesIsolatedDir(t *testing.T) {
	e := cursorEnv(t, "pm=cursor")
	spawnWT(t, e, "pm", "zz-hq-pm", "")
	if exists(filepath.Join(e.Home, ".cursor", "hooks.json")) {
		t.Fatal("policy landed in FACTORY_HOME")
	}
	if !isFile(hooksOf1(e, "zz-hq-pm")) {
		t.Fatal("hooks.json missing in the isolated policy dir")
	}
}

func TestCursorWorktreePolicyWorkspaceUnitWorktreeCwdUsesIsolatedDir(t *testing.T) {
	tmp := realTemp(t)
	home := mkdir(t, filepath.Join(tmp, "factory"))
	wt := mkdir(t, filepath.Join(home, "worktrees", "G-001-T-1"))
	state := filepath.Join(tmp, "state")
	ws, err := ch.PolicyWorkspace(home, "zz-agent", wt, state)
	ok(t, err)
	eq(t, "workspace", ws, filepath.Join(state, "factory", "cursor", "zz-agent"))
	st, err := os.Stat(ws)
	ok(t, err)
	if !st.IsDir() {
		t.Fatal("not a dir")
	}
	eq(t, "mode", st.Mode().Perm(), os.FileMode(0o700))
}

func TestCursorWorktreePolicyWorkspaceUnitTempCwdUnchanged(t *testing.T) {
	tmp := realTemp(t)
	home := mkdir(t, filepath.Join(tmp, "factory"))
	ext := mkdir(t, filepath.Join(tmp, "cursor-pilot-ws"))
	ws, err := ch.PolicyWorkspace(home, "cursor-pilot", ext, filepath.Join(tmp, "state"))
	ok(t, err)
	eq(t, "workspace", ws, ext)
}
