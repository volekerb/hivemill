package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"factory/go/internal/hm"
)

// runMainEnv makes the test binary stand in for the hivemill binary: the hooks the tests install (the guard hook of
// the rendered settings, the pre-push hook of a clone) call it as a subprocess, exactly as they call hivemill.
const runMainEnv = "HIVEMILL_TEST_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		os.Exit(Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

// selfIsTheTestBinary points the hooks at this test binary.
func selfIsTheTestBinary(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(os.Args[0])
	if err != nil {
		t.Fatal(err)
	}
	old := hm.SelfBinary
	hm.SelfBinary = func() (string, error) { return bin, nil }
	t.Cleanup(func() { hm.SelfBinary = old })
	return bin
}

func hookEnv(home string, extra ...string) []string {
	return append([]string{runMainEnv + "=1", "PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "FACTORY_HOME=" + home}, extra...)
}

// guardCommands renders the task settings of a home and returns the PreToolUse hook commands.
func guardCommands(t *testing.T, home string) []string {
	t.Helper()
	e := hm.LoadEnv()
	e.Home = home
	p, err := hm.RenderSettings(e, "task")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	var doc struct {
		Hooks struct {
			PreToolUse []struct {
				Hooks []struct{ Command string } `json:"hooks"`
			} `json:"PreToolUse"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range doc.Hooks.PreToolUse {
		for _, h := range m.Hooks {
			out = append(out, h.Command)
		}
	}
	return out
}

// hook runs a hook command the way Claude Code does (a shell command, the payload on stdin) and returns its exit code.
func hook(t *testing.T, command string, env []string, payload map[string]any) (int, string) {
	t.Helper()
	b, _ := json.Marshal(payload)
	cmd := exec.Command("sh", "-c", command)
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(b)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	err := cmd.Run()
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0, errb.String()
	case errors.As(err, &ee):
		return ee.ExitCode(), errb.String()
	}
	t.Fatal(err)
	return -1, ""
}

func bash(command, cwd string) map[string]any {
	return map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": command}, "cwd": cwd, "session_id": "zz-test"}
}

// A standalone home has no roles/hooks/worker-guard: its settings must resolve to a guard that exists, and that guard
// must allow ordinary work and deny what the guard denies (its own rules, the live home's protected files, the
// docker privilege rule), and block when it cannot run at all.
func TestStandaloneHomeSettingsResolveToAWorkingGuard(t *testing.T) {
	bin := selfIsTheTestBinary(t)
	_, home := standalone(t)
	if _, err := os.Stat(hm.GuardShim(home)); err == nil {
		t.Fatal("the standalone home must not have the repo shim")
	}
	cmds := guardCommands(t, home)
	if len(cmds) != 3 {
		t.Fatalf("hooks: %q", cmds)
	}
	for _, c := range cmds {
		if strings.Contains(c, hm.GuardShim(home)) || !strings.Contains(c, bin) || !strings.HasSuffix(c, " --role coder") {
			t.Fatalf("hook %q does not call this binary's guard", c)
		}
	}
	if _, err := os.Stat(bin); err != nil {
		t.Fatalf("the guard binary the hook names must exist: %v", err)
	}
	// the export the guard decides from: a runner pass writes it
	r, err := hm.NewRunner(hm.Env{Home: home, UserHome: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WriteGuardExport(time.Now()); err != nil {
		t.Fatal(err)
	}
	r.Store.Close()
	env := hookEnv(home, "FACTORY_AGENT=claude-coder-t-0001-a7", "FACTORY_ROLE=task")
	work := t.TempDir()
	for _, c := range []struct {
		name    string
		payload map[string]any
		code    int
		stderr  string
	}{
		{"allow: git status", bash("git status", work), 0, ""},
		{"allow: ls", bash("ls -la", work), 0, ""},
		{"deny: push to main", bash("git push origin HEAD:main", work), 2, "coders push exactly"},
		{"deny: herdr", bash("herdr workspace list", work), 2, "herdr"},
		{"deny: docker privileged", bash("docker run --privileged alpine", work), 2, "docker"},
		{"deny: forge the guard export", bash("echo {} > "+filepath.Join(home, "run", "guard-export.json"), work), 2, "blocked"},
		{"deny: write the home's guard tree", bash("echo x > "+filepath.Join(home, "roles", "hooks", "worker-guard"), work), 2, "protected tree"},
		{"deny: Write tool on the export", map[string]any{"tool_name": "Write", "cwd": work,
			"tool_input": map[string]any{"file_path": filepath.Join(home, "run", "guard-export.json"), "content": "{}"}}, 2, "guard-export"},
	} {
		code, stderr := hook(t, cmds[0], env, c.payload)
		if code != c.code || !strings.Contains(stderr, c.stderr) {
			t.Errorf("%s: exit %d, want %d; stderr %q", c.name, code, c.code, stderr)
		}
	}
	// the guard cannot run (binary gone): blocked, never allowed
	broken := strings.Replace(cmds[0], bin, filepath.Join(t.TempDir(), "no-such-hivemill"), 1)
	if code, stderr := hook(t, broken, env, bash("ls", work)); code != 2 || !strings.Contains(stderr, "blocking") {
		t.Errorf("missing binary: exit %d %q", code, stderr)
	}
	// a stale export blocks (as the Python tick's would)
	exp := filepath.Join(home, "run", "guard-export.json")
	old := time.Now().Add(-time.Hour)
	b, _ := os.ReadFile(exp)
	var doc map[string]any
	_ = json.Unmarshal(b, &doc)
	doc["generated_at"] = old.UTC().Format("2006-01-02T15:04:05+00:00")
	b, _ = json.Marshal(doc)
	_ = os.WriteFile(exp, b, 0o600)
	if code, stderr := hook(t, cmds[0], env, bash("git status", work)); code != 2 || !strings.Contains(stderr, "stale") {
		t.Errorf("stale export: exit %d %q", code, stderr)
	}
	// a bad call of the guard itself blocks
	if code := Main([]string{"guard", "--role", "coder"}, strings.NewReader("{}"), io.Discard, io.Discard); code != 2 {
		t.Errorf("guard without --home: exit %d", code)
	}
}

func gitT(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_AUTHOR_NAME=zz", "GIT_AUTHOR_EMAIL=1+zz@users.noreply.github.com",
		"GIT_COMMITTER_NAME=zz", "GIT_COMMITTER_EMAIL=1+zz@users.noreply.github.com"), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// The pre-push hook of a task clone (`hivemill check public-text`), through a real git push; the cases are those of
// tests/test_pre_push_public_text.py.
func TestPrePushHookOfAStandaloneHome(t *testing.T) {
	selfIsTheTestBinary(t)
	c, home := standalone(t)
	term := "zebra" + "corp"
	list := filepath.Join(t.TempDir(), "owner-list.txt")
	_ = os.WriteFile(list, []byte(term+"\n"), 0o600)
	c.ok("config", "scrub_terms_file", list)
	c.ok("config", "public_text_repos", "acme/pub")
	root := t.TempDir()
	bare := filepath.Join(root, "acme", "pub.git")
	_ = os.MkdirAll(bare, 0o755)
	gitT(t, bare, nil, "init", "-q", "--bare", "-b", "main")
	wt := filepath.Join(root, "wt")
	gitT(t, root, nil, "init", "-q", "-b", "main", wt)
	gitT(t, wt, nil, "commit", "-q", "--allow-empty", "-m", "base")
	gitT(t, wt, nil, "remote", "add", "origin", bare)
	gitT(t, wt, nil, "push", "-q", "-u", "origin", "main")
	hookText, err := hm.PrePushHook(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hookText, " check public-text --home ") || strings.Contains(hookText, "python") {
		t.Fatalf("hook %q", hookText)
	}
	_ = os.WriteFile(filepath.Join(wt, ".git", "hooks", "pre-push"), []byte(hookText), 0o755)
	env := []string{runMainEnv + "=1", "SCRUB_TERMS_FILE=", "FACTORY_AGENT="}
	push := func(ref string) (int, string) {
		cmd := exec.Command("git", "push", "origin", "HEAD:"+ref)
		cmd.Dir = wt
		cmd.Env = append(os.Environ(), env...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return 1, string(out)
		}
		return 0, string(out)
	}
	pushed := func() string { return gitT(t, bare, nil, "branch", "--list", "factory/*") }
	commit := func(msg string) { gitT(t, wt, nil, "commit", "-q", "--allow-empty", "-m", msg) }

	commit("T-0001: add a thing") // clean range is pushed
	if code, out := push("factory/t-0001"); code != 0 || !strings.Contains(pushed(), "factory/t-0001") {
		t.Fatalf("clean push: %d %s", code, out)
	}
	commit("add " + term + " notes") // a hit in an older commit of the range is refused, the term never printed
	commit("fix the notes")
	if code, out := push("factory/t-0002"); code == 0 || !strings.Contains(out, "pre-push: refused (public text:") ||
		!strings.Contains(out, "owner-term") || strings.Contains(out, term) || strings.Contains(pushed(), "t-0002") {
		t.Errorf("term in the range: %d %s", code, out)
	}
	gitT(t, wt, nil, "reset", "-q", "--hard", "HEAD~2")
	if code, out := push("factory/" + term + "-fix"); code == 0 || strings.Contains(out, term+"-fix\n") { // a hit in the branch name
		t.Errorf("term in the branch: %d", code)
	}
	commit("add " + term + " notes") // a remote that is not public is unaffected
	c.ok("config", "public_text_repos", "someone/else")
	if code, out := push("factory/t-0003"); code != 0 {
		t.Errorf("non-public remote: %d %s", code, out)
	}
	gitT(t, wt, nil, "reset", "-q", "--hard", "HEAD~1")
	c.ok("config", "public_text_repos", "acme/pub") // a missing owner list fails closed
	c.ok("config", "scrub_terms_file", list)
	_ = os.Remove(list)
	commit("clean")
	if code, out := push("factory/t-0004"); code == 0 || !strings.Contains(out, "pre-push: refused (cannot read terms file") {
		t.Errorf("missing owner list: %d %s", code, out)
	}
	// usage, as pre_push_public_text.py
	var errb bytes.Buffer
	if code := Main([]string{"check", "public-text", "origin"}, strings.NewReader(""), io.Discard, &errb); code != 1 ||
		!strings.Contains(errb.String(), "usage") {
		t.Errorf("bad usage: %d %q", code, errb.String())
	}
}

// `hivemill affected` is ci/affected.py in Go: the cases of tests/test_affected.py that need no Python test run.
func TestAffectedSubcommand(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(repo, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(text), 0o644)
	}
	gitT(t, repo, nil, "init", "-q", "-b", "main")
	smoke := []string{"test_db_layout.py", "test_inbox.py", "test_judge.py"}
	for _, n := range append(smoke, "test_alpha.py", "test_gamma.py") {
		write("tests/"+n, "# "+n+"\n")
	}
	for i := 0; i < 30; i++ {
		write("tests/test_filler"+string(rune('a'+i))+".py", "# filler\n")
	}
	write("docs/a.md", "a\n")
	gitT(t, repo, nil, "add", "-A")
	gitT(t, repo, nil, "commit", "-qm", "base")
	gitT(t, repo, nil, "branch", "base")
	write("tests/test_gamma.py", "# edited\n")
	gitT(t, repo, nil, "commit", "-qam", "change")
	t.Setenv("FACTORY_HOME", t.TempDir()) // no DB: the smoke default
	var out, errb bytes.Buffer
	if code := Main([]string{"affected", "--root", repo, "--base", "base", "--list"}, strings.NewReader(""), &out, &errb); code != 0 {
		t.Fatalf("exit %d: %s", code, errb.String())
	}
	if got := strings.Join(strings.Fields(out.String()), " "); got != "test_db_layout.py test_gamma.py test_inbox.py test_judge.py" {
		t.Errorf("test file only change selects %q", got)
	}
	errb.Reset()
	if code := Main([]string{"affected", "--root", repo, "--base", "nope"}, strings.NewReader(""), io.Discard, &errb); code != 2 ||
		!strings.Contains(errb.String(), "git fetch") {
		t.Errorf("bad base: exit %d %q", code, errb.String())
	}
	if code := Main([]string{"affected"}, strings.NewReader(""), io.Discard, io.Discard); code != 2 {
		t.Errorf("no --base: exit %d", code)
	}
}

func TestConfigSetsTheFactoryKeys(t *testing.T) {
	c, _ := standalone(t)
	if code, _, errs := c.run("", "config", "noreply_email", "me@example.com"); code == 0 || !strings.Contains(errs, "noreply") {
		t.Errorf("a non-noreply address: %d %q", code, errs)
	}
	c.ok("config", "noreply_email", "1+zz@users.noreply.github.com")
	if out := c.ok("config", "noreply_email"); out != "1+zz@users.noreply.github.com\n" {
		t.Errorf("get: %q", out)
	}
	if code, _, _ := c.run("", "config", "scrub_terms_file", "/nonexistent/terms.txt"); code == 0 {
		t.Error("a terms file that does not exist")
	}
	out := c.ok("config")
	for _, want := range []string{"alert_cmd=\n", "noreply_email=1+zz@users.noreply.github.com\n", "scrub_terms_file=\n", "max_concurrent=2\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("config listing lacks %q", want)
		}
	}
	c.ok("config", "alert_cmd", "true")
}

func TestInitGivesTheHomeItsFilesAndTheNoreplyIdentity(t *testing.T) {
	old := hm.GitGlobalEmail
	hm.GitGlobalEmail = func() string { return "1+zz@users.noreply.github.com" }
	t.Cleanup(func() { hm.GitGlobalEmail = old })
	c, home := standalone(t)
	if _, err := os.Stat(filepath.Join(home, "ci", "scrub_allowlist.txt")); err != nil {
		t.Error(err)
	}
	if out := c.ok("config", "noreply_email"); out != "1+zz@users.noreply.github.com\n" {
		t.Errorf("noreply from git: %q", out)
	}
	out := c.ok("launchd", "health", "--home", home)
	if !strings.Contains(out, "<integer>300</integer>") || !strings.Contains(out, "health --alert") {
		t.Errorf("health plist: %s", out)
	}
}

func TestProjectProfileSetsSensitivePaths(t *testing.T) {
	c, _ := standalone(t)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", t.TempDir(), "--gate", "pnpm test")
	out := c.ok("project", "profile", "www")
	if !strings.Contains(out, "built-in ci/tier_rules.json") {
		t.Errorf("default: %q", out)
	}
	out = c.ok("project", "profile", "www", "--sensitive", "infra/**", "--sensitive", "package.json")
	if !strings.Contains(out, `sensitive_paths "infra/**,package.json"`) || !strings.Contains(out, "with the project's sensitive_paths") {
		t.Errorf("set: %q", out)
	}
	bad := filepath.Join(t.TempDir(), "rules.json")
	_ = os.WriteFile(bad, []byte("{"), 0o644)
	if code, _, _ := c.run("", "project", "profile", "www", "--tier-rules", bad); code == 0 {
		t.Error("a rules file that does not load must be refused")
	}
	if code, _, _ := c.run("", "project", "profile", "nope"); code != 3 {
		t.Errorf("unknown project: exit %d", code)
	}
}

// The allow side from where a session really works: its task clone under <home>/worktrees, which is inside the live
// home. Every step of the task role (edit, commit, the pinned push, hivemill pr create, the CI wait) and the judge's
// reads must pass the guard, or a standalone session cannot finish a task.
func TestStandaloneSessionCanDoItsWorkInItsClone(t *testing.T) {
	bin := selfIsTheTestBinary(t)
	c, home := standalone(t)
	wt := filepath.Join(home, "worktrees", "G-001-T-0001")
	gitT(t, home, nil, "init", "-q", "-b", "factory/t-0001", wt)
	c.ok("project", "add", "www", "--repo", "zz-test/www", "--path", wt)
	c.ok("goal", "new", "--project", "www", "--title", "zz-test goal")
	acc := filepath.Join(t.TempDir(), "acc.txt")
	_ = os.WriteFile(acc, []byte("1. it works - cmd:true => exit 0\n"), 0o600)
	c.ok("task", "add", "--goal", "G-001", "--title", "zz-test task", "--spec", "s", "--acceptance-file", acc)
	db, err := sql.Open("sqlite", filepath.Join(home, "db", "factory.db"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE tasks SET worktree=?, branch='factory/t-0001' WHERE id='T-0001'`, wt); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO agents(name,role,goal_id,task_id,status,started) VALUES('claude-coder-t-0001-a7','task','G-001','T-0001','live','t')`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	r, err := hm.NewRunner(hm.Env{Home: home, UserHome: t.TempDir()}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.WriteGuardExport(time.Now()); err != nil {
		t.Fatal(err)
	}
	r.Store.Close()
	env := hookEnv(home, "FACTORY_AGENT=claude-coder-t-0001-a7", "FACTORY_ROLE=task")
	task := guardCommands(t, home)[0]
	tool := func(name string, in map[string]any) map[string]any {
		return map[string]any{"tool_name": name, "tool_input": in, "cwd": wt, "session_id": "zz-test"}
	}
	f := filepath.Join(wt, "page.html")
	for _, p := range []map[string]any{
		tool("Write", map[string]any{"file_path": f, "content": "x"}),
		tool("Edit", map[string]any{"file_path": f, "old_string": "x", "new_string": "y"}),
		tool("Read", map[string]any{"file_path": f}),
		bash("git add -A", wt), bash("git commit -m 'T-0001: page'", wt),
		bash("git fetch origin && git merge origin/main", wt),
		bash("git push origin HEAD:factory/t-0001", wt),
		bash("hivemill pr create --title 'T-0001: add the page' --body - <<'EOF'\n## Summary\nx\nEOF", wt),
		bash("gh pr checks 1 --watch --fail-fast --interval 60 > .hivemill/ci.log 2>&1; echo exit=$?", wt),
	} {
		if code, stderr := hook(t, task, env, p); code != 0 {
			b, _ := json.Marshal(p["tool_input"])
			t.Errorf("task session %s %s: exit %d %q", p["tool_name"], b, code, stderr)
		}
	}
	if code, _ := hook(t, task, env, bash("git push origin HEAD:factory/t-0002", wt)); code != 2 {
		t.Error("the push stays pinned to the session's own branch")
	}
	// the judge's settings get the same guard, with its own role
	e := hm.LoadEnv()
	e.Home = home
	p, err := hm.RenderSettings(e, "task-judge")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "hivemill-guard") || !strings.Contains(string(b), "--role reviewer") || strings.Contains(string(b), hm.GuardShim(home)) {
		t.Fatalf("judge settings do not call the guard: %s", bin)
	}
	judge := strings.Replace(task, "--role coder", "--role reviewer", 1)
	for _, cmd := range []string{"gh pr diff 1", "gh pr view 1 --json body", "git log --oneline -3"} {
		if code, stderr := hook(t, judge, hookEnv(home, "FACTORY_AGENT=claude-judge-t-0001"), bash(cmd, wt)); code != 0 {
			t.Errorf("judge %q: exit %d %q", cmd, code, stderr)
		}
	}
}
