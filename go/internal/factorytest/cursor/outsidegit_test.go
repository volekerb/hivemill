package cursor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ar "factory/go/internal/agentruntime"
	ch "factory/go/internal/cursorharness"
	ft "factory/go/internal/factorytest"
	"factory/go/internal/scout"
)

// Port of tests/test_cursor_policy_outside_git.py (14 cases; see ../ported_cursor.txt).
// adapted: Python patches find_agent_binary to /bin/agent; here a fake `agent` is first on PATH.
// The three HooksOkGit cases call the Python-only cursor_hooks_ok (no Go counterpart yet) through pyEval.

type ogFix struct{ tmp, home, state string }

func newOG(t *testing.T) ogFix {
	t.Helper()
	f := ogFix{tmp: realTemp(t)}
	f.home = mkdir(t, filepath.Join(f.tmp, "factory"))
	mkdir(t, filepath.Join(f.home, ".git"))
	f.state = filepath.Join(f.tmp, "state")
	t.Setenv("XDG_STATE_HOME", f.state)
	t.Setenv("FACTORY_HOME", "") // plan_cursor reads it; unset means no home check, as in the Python test process
	bin := t.TempDir()
	writeFile(t, filepath.Join(bin, "agent"), "#!/bin/sh\n", 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return f
}

func (f ogFix) ctx(t *testing.T, cwd, role string) ar.SpawnContext {
	return ar.SpawnContext{Role: role, Name: "zz-agent", Cwd: cwd, Model: "claude-opus-5-5", PromptText: "# p\n", Kickoff: "k",
		SettingsPath: filepath.Join(f.home, "settings.json"), Home: f.home, HooksDir: hooksDir(t), AddDirs: []string{f.home}, StateRoot: f.state}
}

func (f ogFix) check(t *testing.T, plan ar.SpawnPlan) {
	t.Helper()
	ws := plan.Workspace
	if strings.HasPrefix(ws, f.home) {
		t.Fatalf("workspace %s is under FACTORY_HOME", ws)
	}
	if ch.InsideGitWorktree(ws) {
		t.Fatalf("workspace %s is inside a git work tree", ws)
	}
	hooks := sub(t, readJSON(t, filepath.Join(ws, ".cursor", "hooks.json")), "hooks")
	for _, ev := range []string{"beforeShellExecution", "preToolUse"} {
		items := list(t, hooks, ev)
		if len(items) == 0 {
			t.Fatalf("no %s hook", ev)
		}
		for _, it := range items {
			if fc, _ := it.(map[string]any)["failClosed"].(bool); !fc {
				t.Errorf("%s entry %v must fail closed", ev, it)
			}
		}
	}
	eq(t, "--workspace", flagValue(t, plan.Argv, "--workspace"), ws)
	found := false
	for _, d := range addDirs(plan.Argv) {
		found = found || d == f.home
	}
	if !found {
		t.Fatalf("--add-dir lacks %s: %v", f.home, plan.Argv)
	}
}

func TestCursorOutsideGitHqCwdFactoryHomeWithGitDir(t *testing.T) {
	f := newOG(t)
	plan, err := ar.PlanSpawn("cursor", f.ctx(t, f.home, "lead"))
	ok(t, err)
	f.check(t, plan)
}

func TestCursorOutsideGitTaskWorktreeWithGitFile(t *testing.T) {
	f := newOG(t)
	wt := mkdir(t, filepath.Join(f.home, "worktrees", "G-001-T-1"))
	writeFile(t, filepath.Join(wt, ".git"), "gitdir: /nowhere\n", 0o644)
	plan, err := ar.PlanSpawn("cursor", f.ctx(t, wt, "coder"))
	ok(t, err)
	f.check(t, plan)
	found := false
	for _, d := range addDirs(plan.Argv) {
		found = found || d == wt
	}
	if !found {
		t.Fatalf("--add-dir lacks the worktree %s: %v", wt, plan.Argv)
	}
	if exists(filepath.Join(wt, ".cursor")) {
		t.Fatal("policy was written into the task worktree")
	}
}

func TestCursorOutsideGitXdgStateInsideFactoryHomeRaises(t *testing.T) {
	f := newOG(t)
	_, err := ch.PolicyWorkspace(f.home, "zz-agent", f.home, filepath.Join(f.home, "run"))
	if err == nil {
		t.Fatal("want an error")
	}
	contains(t, "error", err.Error(), "XDG_STATE_HOME")
}

func TestCursorOutsideGitXdgStateInsideOtherGitTreeRaises(t *testing.T) {
	f := newOG(t)
	other := filepath.Join(f.tmp, "other")
	mkdir(t, filepath.Join(other, ".git"))
	_, err := ch.PolicyWorkspace(f.home, "zz-agent", f.home, filepath.Join(other, "st"))
	if err == nil {
		t.Fatal("want an error")
	}
	contains(t, "error", err.Error(), "XDG_STATE_HOME")
}

func TestCursorOutsideGitPolicyDirMode0700(t *testing.T) {
	f := newOG(t)
	ws, err := ch.IsolatedPolicyDir(f.home, "zz-agent", f.state)
	ok(t, err)
	eq(t, "dir", ws, filepath.Join(f.state, "factory", "cursor", "zz-agent"))
	st, err := os.Stat(ws)
	ok(t, err)
	eq(t, "mode", st.Mode().Perm(), os.FileMode(0o700))
}

func TestCursorOutsideGitTargetRefusedInGitTreeAndRunCursor(t *testing.T) {
	f := newOG(t)
	for _, bad := range []string{filepath.Join(f.home, "run", "cursor", "x"), filepath.Join(f.home, "db")} {
		mkdir(t, bad)
		if err := ch.AssertPolicyTargetAllowed(f.home, bad); err == nil {
			t.Errorf("target %s must be refused", bad)
		}
	}
	repo := filepath.Join(f.tmp, "repo")
	mkdir(t, filepath.Join(repo, ".git"))
	mkdir(t, filepath.Join(repo, "sub"))
	if err := ch.AssertPolicyTargetAllowed(f.home, filepath.Join(repo, "sub")); err == nil {
		t.Error("a dir inside another git tree must be refused")
	}
}

func TestCursorOutsideGitTargetOutsideAnyGitTreeAccepted(t *testing.T) {
	f := newOG(t)
	okDir := mkdir(t, filepath.Join(f.tmp, "plain"))
	ok(t, ch.AssertPolicyTargetAllowed(f.home, okDir))
}

func TestCursorOutsideGitInsideGitWorktreeDetectsFileAndDir(t *testing.T) {
	f := newOG(t)
	a := filepath.Join(f.tmp, "a")
	deep := mkdir(t, filepath.Join(a, "x", "y"))
	if ch.InsideGitWorktree(deep) {
		t.Fatal("no .git yet")
	}
	writeFile(t, filepath.Join(a, ".git"), "gitdir: z\n", 0o644)
	if !ch.InsideGitWorktree(deep) {
		t.Fatal(".git file must count")
	}
}

// ---- HooksOkGit (Python cursor_hooks_ok) -------------------------------------------------------------------------

func writeHooks(t *testing.T, ws string) {
	t.Helper()
	// written directly: MaterializeCursorPolicy itself refuses git work trees
	ok(t, ch.WriteAtomic(filepath.Join(ws, ".cursor", "hooks.json"), scout.Dumps(ch.HooksJSON(hooksDir(t), "coder", false), 2, false), 0o644))
}

func hooksOKAt(t *testing.T, e *ft.Env, ws string) bool {
	t.Helper()
	got := pyEvalArg(t, e, "res['ok'] = mod.cursor_hooks_ok(sys.argv[3])", ws)
	v, _ := got["ok"].(bool)
	return v
}

func TestCursorOutsideGitHooksOkNestedUnderGitDirIsNotOk(t *testing.T) {
	e := ft.New(t)
	base := filepath.Join(e.UserHome, "repo")
	mkdir(t, filepath.Join(base, ".git"))
	ws := filepath.Join(base, "run", "cursor", "x")
	writeHooks(t, ws)
	if hooksOKAt(t, e, ws) {
		t.Fatal("a workspace nested under a git dir must not be ok")
	}
}

func TestCursorOutsideGitHooksOkNonGitDirIsOk(t *testing.T) {
	e := ft.New(t)
	ws := filepath.Join(e.UserHome, "plain", "ws")
	writeHooks(t, ws)
	if !hooksOKAt(t, e, ws) {
		t.Fatal("a plain dir with fail-closed hooks must be ok")
	}
}

func TestCursorOutsideGitHooksOkWorkspaceThatIsItselfTheGitRootIsOk(t *testing.T) {
	e := ft.New(t)
	ws := filepath.Join(e.UserHome, "selfrepo")
	mkdir(t, filepath.Join(ws, ".git"))
	writeHooks(t, ws)
	if !hooksOKAt(t, e, ws) {
		t.Fatal("the git root itself must be ok")
	}
}

// ---- adapter decision log ---------------------------------------------------------------------------------------

func logRun(t *testing.T, log, command string) adapterRun {
	t.Helper()
	payload := `{"command": ` + string(mustJSON(t, command)) + `, "cwd": "/tmp"}`
	env := map[string]string{"FACTORY_AGENT": "zz-agent", "FACTORY_ROLE": "coder", "FACTORY_HOME": t.TempDir()}
	if log != "" {
		env["FACTORY_CURSOR_HOOK_LOG"] = log
	}
	return runAdapter(t, payload, env, "--role", "coder", "--event", "beforeShellExecution")
}

func TestCursorOutsideGitAdapterDenyLoggedWithoutCommandText(t *testing.T) {
	log := filepath.Join(t.TempDir(), "hook-events.jsonl")
	p := logRun(t, log, "sudo id")
	eq(t, "permission", lastJSON(t, p.Stdout)["permission"], any("deny"))
	lines := strings.Split(strings.TrimSpace(readFile(t, log)), "\n")
	eq(t, "log lines", len(lines), 1)
	rec := parseJSON(t, lines[0])
	eq(t, "event", rec["event"], any("beforeShellExecution"))
	eq(t, "decision", rec["decision"], any("deny"))
	eq(t, "agent", rec["agent"], any("zz-agent"))
	eq(t, "role", rec["role"], any("coder"))
	if _, has := rec["ts"]; !has {
		t.Fatal("record has no ts")
	}
	notContains(t, "log line", lines[0], "sudo")
}

func TestCursorOutsideGitAdapterUnwritableLogKeepsDecisionAndExitCode(t *testing.T) {
	tmp := t.TempDir()
	good := logRun(t, filepath.Join(tmp, "ok.jsonl"), "sudo id")
	bad := logRun(t, filepath.Join(tmp, "no-such-dir", "hook-events.jsonl"), "sudo id")
	none := logRun(t, "", "sudo id")
	eq(t, "bad exit", bad.Code, good.Code)
	eq(t, "bad decision", lastLine(bad.Stdout), lastLine(good.Stdout))
	eq(t, "no-log exit", none.Code, good.Code)
	eq(t, "permission", lastJSON(t, bad.Stdout)["permission"], any("deny"))
}

func TestCursorOutsideGitAdapterAllowLoggedAsAllow(t *testing.T) {
	log := filepath.Join(t.TempDir(), "h.jsonl")
	logRun(t, log, "echo ok")
	eq(t, "decision", parseJSON(t, strings.TrimSpace(readFile(t, log)))["decision"], any("allow"))
}
