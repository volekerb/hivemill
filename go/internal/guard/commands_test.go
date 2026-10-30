package guard

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The expected exits in testdata/commands_cases.json are those of roles/hooks/worker-guard.py on origin/main:
// every Bash call the Python test modules below send to the hook was replayed against the Python hook and the
// exit recorded (the command text is verbatim from the tests). A Go result differs from Python only by a stub of
// the Python hook (a Delegate error: the decision reads the factory DB), where Python decides.

type pyCase struct {
	Module string            `json:"module"`
	Cmd    string            `json:"cmd"`
	Role   string            `json:"role"`
	HQ     bool              `json:"hq"`
	Exit   int               `json:"exit"`
	Env    map[string]string `json:"env"`
}

// casesPerModule is the number of distinct cases ported from each Python test module (a silent loss of cases
// would otherwise pass).
var casesPerModule = map[string]int{
	"test_worker_guard":         327,
	"test_priv_guard":           572,
	"test_worker_guard_heredoc": 101,
	"test_worker_guard_units":   111,
	"test_worker_guard_reads":   135,
}

func loadPyCases(t *testing.T) []pyCase {
	t.Helper()
	raw, err := os.ReadFile("testdata/commands_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []pyCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// clearTermsEnv keeps the host's exported owner-terms path out of the table tests.
func clearTermsEnv(t *testing.T) { unsetEnv(t, termsVar) }

// unsetEnv removes k for the test (t.Setenv(k, "") would leave it set but empty) and restores it afterwards.
func unsetEnv(t *testing.T, k string) {
	t.Helper()
	t.Setenv(k, "") // registers the restore of the original value
	if err := os.Unsetenv(k); err != nil {
		t.Fatal(err)
	}
}

func exitOf(err error) int {
	if err != nil {
		return 2
	}
	return 0
}

func TestCommandsPythonCases(t *testing.T) {
	clearTermsEnv(t)
	cases := loadPyCases(t)
	perModule := map[string]int{}
	stubs := map[string]int{}
	// The exits were recorded against a factory DB: the home has a DB file, so a decision that reads it is
	// delegated (Python decides); one the DB does not touch must match the recorded exit.
	home := dbHome(t)
	missing := filepath.Join(t.TempDir(), "missing")
	for _, c := range cases {
		perModule[c.Module]++
		t.Setenv("FACTORY_HOME", home)
		for k, v := range c.Env {
			if k == "FACTORY_HOME" {
				if strings.HasSuffix(v, "/missing") {
					v = missing
				} else {
					v = home
				}
			}
			t.Setenv(k, v)
		}
		err := Check(c.Cmd, "", c.Role, c.HQ)
		got := exitOf(err)
		switch {
		case IsDelegate(err):
			stubs[c.Module]++
		case c.Exit == 2 && got != 2:
			t.Errorf("%s: LOOSENED %q (role %q hq %v): python blocks, go allows", c.Module, c.Cmd, c.Role, c.HQ)
		case c.Exit == 0 && got == 2:
			t.Errorf("%s: TIGHTENED %q (role %q hq %v): python allows, go blocks: %v", c.Module, c.Cmd, c.Role, c.HQ, err)
		}
		for k := range c.Env {
			unsetEnv(t, k)
		}
	}
	if !reflect.DeepEqual(perModule, casesPerModule) {
		t.Errorf("cases per module = %v, want %v", perModule, casesPerModule)
	}
	t.Logf("cases per module %v; cases delegated to the Python hook: %v", perModule, stubs)
}

// TestCommandsAgreeOnTheCore pins how much of the corpus the port decides on its own: the number of
// python-allowed cases that go also allows must not fall (a stub added by mistake would shrink it).
func TestCommandsAgreeOnTheCore(t *testing.T) {
	clearTermsEnv(t)
	t.Setenv("FACTORY_HOME", dbHome(t))
	allowed, agree := 0, 0
	for _, c := range loadPyCases(t) {
		if c.Exit != 0 || len(c.Env) > 0 {
			continue
		}
		allowed++
		if Check(c.Cmd, "", c.Role, c.HQ) == nil {
			agree++
		}
	}
	if agree < 230 {
		t.Errorf("go allows %d of %d python-allowed cases, want at least 230", agree, allowed)
	}
}

type helperCase struct {
	Fn  string          `json:"fn"`
	In  string          `json:"in"`
	Out json.RawMessage `json:"out"`
}

// TestCommandsHelpers checks the hand-written matchers against the Python regexes they replace (expected values
// come from running those regexes on the same text, non-ASCII included).
func TestCommandsHelpers(t *testing.T) {
	raw, err := os.ReadFile("testdata/helpers_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []helperCase
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 150 {
		t.Fatalf("only %d helper cases", len(cases))
	}
	for _, c := range cases {
		var got any
		var want any
		switch c.Fn {
		case "sedE":
			m, g := sedE(c.In)
			got = []any{m, g}
		case "awkRun":
			got = awkRun(c.In)
		case "r2Set":
			got = matchR2Set(c.In)
		case "r2Unset":
			got = matchR2Unset(c.In)
		case "redirSubst":
			ms := redirSubstMatches(c.In)
			arr := make([]any, len(ms))
			for i, m := range ms {
				arr[i] = m
			}
			got = arr
		case "claudeCLI":
			got = claudeCLI(c.In)
		case "sedHerdr":
			got = sedHerdr(c.In)
		case "shShebang":
			got = shShebang(c.In)
		case "pyLines":
			ls := pyLines(c.In)
			arr := make([]any, len(ls))
			for i, l := range ls {
				arr[i] = l
			}
			got = arr
		case "isSigWord":
			got = isSigWord(c.In)
		case "isJobTarget":
			got = isJobTarget(c.In)
		default:
			t.Fatalf("unknown helper %q", c.Fn)
		}
		if err := json.Unmarshal(c.Out, &want); err != nil {
			t.Fatal(err)
		}
		if want == nil {
			want = []any{}
		}
		if gs, ok := got.([]any); ok && len(gs) == 0 {
			got = []any{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s(%q) = %v, python says %v", c.Fn, c.In, got, want)
		}
	}
}

func checkRole(cmd, role string) error { return Check(cmd, "", role, false) }

func TestPrivRuleBlocksPrivilegeWords(t *testing.T) {
	clearTermsEnv(t)
	blocked := []string{
		"sudo ls", "su -", "doas x", "pkexec x", "docker ps", "/usr/bin/sudo ls", "env FOO=1 sudo ls", "nohup sudo x &",
		"timeout 5 sudo ls", "bash -c 'sudo ls'", "echo $(sudo ls)", "`sudo ls`", "find . -exec sudo rm {} ;",
		"xargs sudo", "python3 -c 'import os; os.system(\"sudo ls\")'", "/usr/bin/s?do ls", "sed 's/a/b/e;1e sudo x' f",
	}
	for _, role := range []string{"coder", "qa", "reviewer", "judge", "pm", "cleaner", ""} {
		for _, cmd := range blocked {
			err := checkRole(cmd, role)
			if err == nil {
				t.Errorf("role %q: %q allowed", role, cmd)
			} else if !IsPrivBlock(err) {
				t.Errorf("role %q: %q blocked by %v, want the PRIV rule", role, cmd, err)
			}
		}
	}
	for _, cmd := range []string{"echo sudo", "grep -r sudo .", "ls -la", "printf 'su -' > /dev/null", "pseudo ls", "git log --grep=sudo"} {
		if err := checkRole(cmd, "coder"); err != nil && !IsDelegate(err) {
			t.Errorf("%q blocked: %v", cmd, err)
		}
	}
	err := checkRole("sudo ls", "coder")
	var p *PrivBlock
	if !errors.As(err, &p) || p.Word != "sudo" {
		t.Fatalf("sudo ls: err = %v, want PrivBlock{sudo}", err)
	}
	if want := "worker-guard: blocked: privilege escalation (`sudo`) is not allowed for factory agents (owner M803)\n"; Explain(err) != want {
		t.Errorf("Explain = %q, want %q", Explain(err), want)
	}
}

func TestPrivInHQMode(t *testing.T) {
	clearTermsEnv(t)
	if err := Check("sudo ls", "", "", true); !IsPrivBlock(err) {
		t.Errorf("hq sudo: %v", err)
	}
	// parse trouble allows in HQ mode, fails closed for workers
	if err := Check("echo 'a", "", "", true); err != nil {
		t.Errorf("hq unbalanced quote: %v", err)
	}
	if err := Check("echo 'a", "", "coder", false); err == nil {
		t.Errorf("worker unbalanced quote allowed")
	}
	// HQ mode blocks mutating systemctl and systemd-run as UnitBlock, workers only the herdr/factory units
	err := Check("systemctl --user restart foo.service", "", "", true)
	var u *UnitBlock
	if !errors.As(err, &u) || u.Msg != "systemctl restart" {
		t.Errorf("hq systemctl restart: %v", err)
	}
	if err := checkRole("systemctl --user restart foo.service", "coder"); err != nil {
		t.Errorf("worker systemctl restart foo: %v", err)
	}
	if err := checkRole("systemd-run --user ls", "coder"); !IsPrivBlock(err) {
		t.Errorf("worker systemd-run: %v", err)
	}
	// a dynamic command word is only an error for workers
	if err := Check("$X ls", "", "", true); err != nil {
		t.Errorf("hq dynamic word: %v", err)
	}
	var d *DynamicBlock
	if err := checkRole("$X ls", "coder"); !errors.As(err, &d) {
		t.Errorf("worker dynamic word: %v", err)
	}
}

func TestShellPayloadsAreAnalysed(t *testing.T) {
	clearTermsEnv(t)
	cases := []struct {
		cmd     string
		blocked bool
	}{
		{"bash -c 'echo hi'", false},
		{"bash -c 'herdr x'", true},
		{"sh -c \"bash -c 'herdr x'\"", true},
		{"bash -lc 'claude'", true},
		{"bash -c", true},
		{"echo x | bash", true},
		{"echo x | bash -s", true},
		{"eval \"$X\"", true},
		{"eval 'echo hi'", false},
		{"eval 'herdr x'", true},
		{"cat <<EOF | bash\nherdr x\nEOF", true},
		{"bash <<EOF\necho hi\nEOF", false},
		{"bash <<EOF\nherdr x\nEOF", true},
		{"python3 <<EOF\nimport os\nEOF", false},
		{"python3 - <<EOF\nos.system('herdr x')\nEOF", true},
		{"sh -c 'sh -c \"sh -c \\\"sh -c x\\\"\"'", false},
		{"time herdr x", true},
		{"FOO=1 BAR=2 herdr x", true},
		{"{ herdr x; }", true},
		{"(herdr x)", true},
		{"echo $(herdr x)", true},
		{"cat tests/stubs/herdr", false},
		{"./tests/stubs/herdr x", false},
		{"grep herdr README.md", false},
	}
	for _, c := range cases {
		err := checkRole(c.cmd, "coder")
		if c.blocked && err == nil {
			t.Errorf("%q allowed", c.cmd)
		}
		if !c.blocked && err != nil && !IsDelegate(err) {
			t.Errorf("%q blocked: %v", c.cmd, err)
		}
	}
}

func TestShellNestingDepthIsBounded(t *testing.T) {
	clearTermsEnv(t)
	cmd := "ls"
	for i := 0; i <= MaxDepth+1; i++ {
		cmd = "bash -c " + shellQuote(cmd)
	}
	err := checkRole(cmd, "coder")
	var b *Block
	if !errors.As(err, &b) || b.Msg != "nesting too deep" {
		t.Errorf("deeply nested bash -c: %v, want nesting too deep", err)
	}
	cmd = "ls"
	for i := 0; i < 3; i++ {
		cmd = "bash -c " + shellQuote(cmd)
	}
	if err := checkRole(cmd, "coder"); err != nil {
		t.Errorf("three nested bash -c: %v", err)
	}
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestShellScriptFilesAreRead(t *testing.T) {
	clearTermsEnv(t)
	dir := t.TempDir()
	writeFile(t, dir, "ok.sh", "#!/bin/bash\necho hi\n")
	writeFile(t, dir, "bad.sh", "#!/bin/bash\nsudo ls\n")
	writeFile(t, dir, "crlf.sh", "echo hi\r\nsudo ls\r\n")
	writeFile(t, dir, "herdr.sh", "herdr server stop\n")
	writeFile(t, dir, "py.sh", "#!/usr/bin/python3\nprint(1)\n")
	writeFile(t, dir, "big.sh", strings.Repeat("echo hi\n", 1<<18))
	cases := []struct {
		cmd     string
		blocked bool
		why     string
	}{
		{"bash ok.sh", false, ""},
		{"bash bad.sh", true, "sudo"},
		{"bash crlf.sh", true, "sudo"},
		{"sh herdr.sh", true, "direct herdr invocation"},
		{"source bad.sh", true, "sudo"},
		{". ./bad.sh", true, "sudo"},
		{"./bad.sh", true, "sudo"},
		{"./ok.sh", false, ""},
		{"./py.sh", false, ""},
		{"./missing.sh", false, ""},
		{"bash missing.sh", true, "cannot inspect script missing.sh"},
		{"bash big.sh", true, "cannot inspect script big.sh"},
		{"bash $F", true, "dynamic script path"},
		{"bash " + dir, true, "cannot inspect script"},
	}
	for _, c := range cases {
		err := Check(c.cmd, dir, "coder", false)
		if c.blocked != (err != nil) {
			t.Errorf("%q: err = %v, blocked want %v", c.cmd, err, c.blocked)
			continue
		}
		if c.why != "" && !strings.Contains(err.Error(), c.why) {
			t.Errorf("%q: err = %q, want it to name %q", c.cmd, err, c.why)
		}
	}
	// HQ mode runs only the PRIV rule, through the same files
	if err := Check("bash bad.sh", dir, "", true); !IsPrivBlock(err) {
		t.Errorf("hq bash bad.sh: %v", err)
	}
	if err := Check("bash herdr.sh", dir, "", true); err != nil {
		t.Errorf("hq bash herdr.sh: %v", err)
	}
}

func TestCommandsKillRule(t *testing.T) {
	clearTermsEnv(t)
	allowed := []string{"kill %1", "kill -9 %1", "kill -TERM %2 %3", "kill $!", "kill -s KILL %1", "kill -l", "kill -- %1", "kill -9 ${!}"}
	blocked := []string{"kill 123", "kill -9 123", "kill 0", "kill -1", "kill -9 -1", "kill $PID", "kill $(pgrep x)", "kill", "xargs kill",
		"kill %1 123", "pkill foo", "killall foo", "kill -9 -123", "kill -s 9 -9"}
	for _, c := range allowed {
		if err := checkRole(c, "coder"); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
	for _, c := range blocked {
		if err := checkRole(c, "coder"); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
}

func TestCommandsClaudeRule(t *testing.T) {
	clearTermsEnv(t)
	for _, c := range []string{"claude", "/usr/bin/claude -p x", "npx @anthropic-ai/claude-code", "pnpm dlx @anthropic-ai/claude-code",
		"node node_modules/@anthropic-ai/claude-code/cli.js", "bunx @anthropic-ai/claude-code", "env claude", "alias c=claude", "alias c='claude -p'",
		"X=claude; $X"} {
		if err := checkRole(c, "coder"); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
	err := checkRole("claude", "coder")
	if !IsClaudeBlock(err) {
		t.Fatalf("claude: %v", err)
	}
	want := "worker-guard: blocked (direct claude invocation). only factory spawn starts Claude (lesson M347); ask your lead.\n"
	if Explain(err) != want {
		t.Errorf("Explain = %q", Explain(err))
	}
	for _, c := range []string{"echo claude", "grep claude README.md", "ls claude-code"} {
		if err := checkRole(c, "coder"); err != nil && !IsDelegate(err) {
			t.Errorf("%q: %v", c, err)
		}
	}
	// sudo claude is both: the Claude block wins in worker mode
	if err := checkRole("sudo claude", "coder"); !IsClaudeBlock(err) {
		t.Errorf("sudo claude: %v, want the Claude block", err)
	}
	if err := Check("sudo claude", "", "", true); !IsPrivBlock(err) {
		t.Errorf("hq sudo claude: %v, want the PRIV block", err)
	}
}

func TestCommandsRawBackstop(t *testing.T) {
	clearTermsEnv(t)
	for _, c := range []string{
		"export HERDR_SOCKET_PATH=/tmp/x", "HERDR_SESSION=x true", "cat ~/.config/herdr/config", "ls herdr.sock",
		"unset HERDR_SOCKET_PATH; herdr list", "env -i herdr pane list", "cat < /usr/bin/herdr", "dd if=/usr/bin/herdr of=/tmp/h",
		"xargs herdr", "find . -exec herdr server stop \\;", "env herdr agent start", "bash -c 'x' herdr pane close",
	} {
		if err := checkRole(c, "coder"); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
	for _, c := range []string{"echo 'cat ~/.config/herdr'", "git commit -m 'herdr pane close'", "grep 'herdr.sock' notes.md"} {
		if err := checkRole(c, "coder"); err != nil && !IsDelegate(err) {
			t.Errorf("%q: %v", c, err)
		}
	}
}

// A decision that reads the factory DB is delegated to the Python hook when the DB file exists; without one the hook
// takes its "no DB" branch and so does Go.
func TestCommandsDelegation(t *testing.T) {
	clearTermsEnv(t)
	t.Setenv("FACTORY_AGENT", "coder-t-9")
	for _, c := range []string{"git commit -m x", "env git commit -m x", "gh pr create --title x", "git tag -a v1 -m x",
		"factory goal show G-005", "git push origin HEAD:factory/t-9", "uvx pytest", "python3 -m pytest"} {
		t.Setenv("FACTORY_HOME", t.TempDir()) // no DB file
		if err := checkRole(c, "coder"); IsDelegate(err) {
			t.Errorf("%q without a DB file: delegated (%v)", c, err)
		}
		t.Setenv("FACTORY_HOME", dbHome(t))
		err := checkRole(c, "coder")
		if !IsDelegate(err) {
			t.Errorf("%q with a DB file: err = %v, want a delegation", c, err)
		}
	}
	// HQ mode runs git and gh through the public-text rule, which reads the factory DB
	t.Setenv("FACTORY_HOME", dbHome(t))
	if err := Check("git push", "", "", true); !IsDelegate(err) {
		t.Errorf("hq git push: %v", err)
	}
	if err := Check("bash -c 'git commit -m x'", "", "", true); !IsDelegate(err) {
		t.Errorf("hq bash -c git commit: %v", err)
	}
	// commands that cannot reach a DB read are decided by Go whatever the DB file says
	for _, c := range []string{"ls -la", "factory task show T-1", "factory inbox", "cat README.md", "git status", "git tag v1",
		"gh pr list", "gh api repos/o/r", "factory goal show", "git push --version"} {
		if err := checkRole(c, "coder"); IsDelegate(err) {
			t.Errorf("%q: delegated (%v)", c, err)
		}
	}
	// the terms roles: with no exported terms file the scrub_terms_file key of the DB may name it, so Python decides
	unsetEnv(t, termsVar)
	for _, role := range []string{"coder", "qa", "reviewer", "judge", "pm"} {
		if err := checkRole("ls -la", role); !IsDelegate(err) {
			t.Errorf("%s: ls without an exported terms file and with a DB: %v, want a delegation", role, err)
		}
	}
	if err := checkRole("ls -la", "cleaner"); err != nil {
		t.Errorf("cleaner ls: %v", err)
	}
	t.Setenv(termsVar, filepath.Join(t.TempDir(), "t.txt"))
	if err := checkRole("ls -la", "coder"); err != nil {
		t.Errorf("coder ls with an exported terms file: %v", err)
	}
	t.Setenv("FACTORY_HOME", t.TempDir())
	unsetEnv(t, termsVar)
	if err := checkRole("ls -la", "coder"); err != nil {
		t.Errorf("coder ls with no terms file and no DB: %v", err)
	}
	// FACTORY_AGENT unset: own_goal is None without a DB read
	unsetEnv(t, "FACTORY_AGENT")
	if err := checkRole("factory goal show G-005", "coder"); IsDelegate(err) || err == nil {
		t.Errorf("goal show without FACTORY_AGENT: %v, want the broad-read block", err)
	}
}

func TestCommandsOwnerTermsFile(t *testing.T) {
	dir := t.TempDir()
	terms := filepath.Join(dir, "terms.txt")
	writeFile(t, dir, "terms.txt", "secret\n")
	t.Setenv(termsVar, terms)
	for _, c := range []string{"cat " + terms, "cat " + dir + "/te*", "echo $" + termsVar, "grep -r x " + dir, "bash -c 'cat " + terms + "'",
		"cp " + terms + " /tmp/x", "cd " + dir + " && cat terms.txt", "cat " + dir + "/../" + filepath.Base(dir) + "/terms.txt"} {
		err := Check(c, dir, "coder", false)
		var pb *PublicBlock
		if !errors.As(err, &pb) {
			t.Errorf("%q: err = %v, want PublicBlock", c, err)
		}
	}
	for _, c := range []string{"ls -la", "python3 ci/scrub_scan.py " + terms, "echo hi"} {
		if err := Check(c, dir, "coder", false); err != nil && !IsDelegate(err) {
			t.Errorf("%q: %v", c, err)
		}
	}
	// roles outside TERMS_ROLES and HQ mode do not run the rule
	if err := Check("cat "+terms, dir, "cleaner", false); err != nil && !IsDelegate(err) {
		t.Errorf("cleaner: %v", err)
	}
	if err := Check("cat "+terms, dir, "", true); err != nil {
		t.Errorf("hq: %v", err)
	}
}

func TestCommandsSystemctlAndXargs(t *testing.T) {
	clearTermsEnv(t)
	for _, c := range []string{"systemctl --user stop herdr.service", "systemctl --user daemon-reload", "systemctl --user enable factory-up.service",
		"systemctl --user kill 'factory-*'", "systemctl isolate x", "systemctl restart", "systemctl stop $UNIT", "xargs -I{} {} x", "xargs -I X X"} {
		if err := checkRole(c, "coder"); err == nil {
			t.Errorf("%q allowed", c)
		}
	}
	for _, c := range []string{"systemctl --user status herdr.service", "systemctl --user restart foo.service", "xargs -I{} echo {}", "xargs ls"} {
		if err := checkRole(c, "coder"); err != nil {
			t.Errorf("%q: %v", c, err)
		}
	}
}

func TestCommandsUnicodeCommandWords(t *testing.T) {
	clearTermsEnv(t)
	// Python's \w, \s and \d are Unicode-aware: a word glued to a PRIV word by a non-ASCII letter is not that word
	for _, c := range []string{"echo ésudo", "python3 -c 'print(\"ésudo\")'", "echo ٣ sudo"} {
		if err := checkRole(c, "coder"); err != nil && !IsDelegate(err) {
			t.Errorf("%q blocked: %v", c, err)
		}
	}
	if err := checkRole("python3 -c 'import os; os.system(\"sudo ls\")'", "coder"); err == nil {
		t.Errorf("sudo followed by a no-break space allowed")
	}
}

// chdirGone moves the process into a directory and removes it, so os.Getwd fails.
func chdirGone(t *testing.T) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Getwd(); err == nil {
		t.Skip("os.Getwd still works in a removed directory on this platform")
	}
}

// Python's `cwd or os.getcwd()` raises when the cwd is gone (exit 2); Go must not carry on with cwd "".
func TestCommandsGetwdErrorFailsClosed(t *testing.T) {
	clearTermsEnv(t)
	chdirGone(t)
	for _, hq := range []bool{false, true} {
		if err := Check("ls -la", "", "coder", hq); err == nil {
			t.Errorf("Check with a removed cwd (hq %v) = nil, want a block", hq)
		}
	}
	if _, err := NewAnalyzer("", "coder", false); err == nil {
		t.Error("NewAnalyzer with a removed cwd = nil error, want a block")
	}
	// termsBash returns early when no terms file is configured; point it at a real file
	f := filepath.Join(t.TempDir(), "terms")
	if err := os.WriteFile(f, []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(termsVar, f)
	if err := TermsBash("ls", ""); err == nil {
		t.Error("TermsBash with a removed cwd = nil, want a block")
	}
	// an explicit cwd never needs os.Getwd
	if err := Check("ls -la", t.TempDir(), "coder", false); err != nil && !IsDelegate(err) {
		t.Errorf("Check with an explicit cwd = %v, want allow", err)
	}
}

// A read error on a regular file run as a command is exit 2 in Python (read(200) raises); /proc/self/mem is a
// regular file whose read fails with EIO.
func TestCommandsExecPathReadErrorFailsClosed(t *testing.T) {
	clearTermsEnv(t)
	p := "/proc/self/mem"
	f, err := os.Open(p)
	if err != nil {
		t.Skip("no /proc/self/mem")
	}
	defer f.Close()
	if _, rerr := f.Read(make([]byte, 1)); rerr == nil || rerr == io.EOF {
		t.Skip("/proc/self/mem is readable here")
	}
	an := &Analyzer{Cwd: "/", Role: "coder"}
	if err := an.execPath(p, 0); err == nil {
		t.Error("execPath on an unreadable regular file = nil, want a block")
	}
	if err := Check(p, "/", "coder", false); err == nil {
		t.Error("Check on an unreadable regular file = nil, want a block")
	}
}

// Python reads scripts with errors="replace": one U+FFFD per undecodable byte.
func TestReplaceInvalidUTF8PerByte(t *testing.T) {
	got := replaceInvalidUTF8([]byte("a\xff\xfe\xc3b\xe2\x82"))
	if want := "a\ufffd\ufffd\ufffdb\ufffd\ufffd"; got != want {
		t.Errorf("replaceInvalidUTF8 = %q, want %q", got, want)
	}
	if got := replaceInvalidUTF8([]byte("ok \u00e9\ufffd")); got != "ok \u00e9\ufffd" {
		t.Errorf("valid text changed: %q", got)
	}
}

// TestUvxRunnerRouting ports the cases of tests/test_worker_guard_uv_runner.py (command text verbatim, expected exit
// from the Python hook with test_sandbox=1): the routed runners reach the test_sandbox stub (exit 2), the others pass.
func TestUvxRunnerRouting(t *testing.T) {
	clearTermsEnv(t)
	t.Setenv("FACTORY_HOME", dbHome(t))
	const url = "https://pypi.example.org/simple"
	routed := []string{
		"uvx --index-url " + url + " pytest",
		"uvx --default-index " + url + " pytest",
		"uvx --extra-index-url " + url + " pytest",
		"uvx 'pytest[testing]'",
		"uvx pytest==8.0",
		"uvx pytest" + "@" + "8.0",
		"uv -q tool run pytest",
		"uv tool -q run pytest",
		"uv tool uvx pytest",
		"uvx 'pytest >=8'",
		"uvx 'pytest (>=8)'",
		"uvx 'pytest ; python_version>\"3\"'",
		"uvx 'pytest >=8'", // non-ASCII whitespace: Python's \s splits on it
		"uvx 'pytest 8'",
		"uvx --from=x unittest",
		"uvx path/to/py.test",
	}
	allowed := []string{
		"uvx ruff", "uv tool run black", "uvx --index-url " + url + " ruff", "uv -q tool run black",
		"uv tool -q run black", "uv tool uvx ruff", "uvx 'ruff >=0.5'",
		"uvx pytestx", "uvx pytest-cov",
	}
	for _, role := range []string{"coder", "qa"} {
		for _, c := range routed {
			if err := checkRole(c, role); !IsDelegate(err) {
				t.Errorf("%s: %q: err = %v, want a delegation (test_sandbox is read by the Python hook)", role, c, err)
			}
		}
		for _, c := range allowed {
			if err := checkRole(c, role); err != nil {
				t.Errorf("%s: %q: %v, want exit 0", role, c, err)
			}
		}
	}
}
