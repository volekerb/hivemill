package main

import (
	"bytes"
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
