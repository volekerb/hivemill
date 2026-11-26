// Ported from go/internal/factorytest/guardtools: corpus_test.go, fuzz_test.go and priv_guard_test.go (T-1922).
// The old cases ran the built worker-guard binary with the hook payload on stdin; here the same payload goes to
// HookMain in-process (g2HookRaw of g2_helpers_test.go) with the clean environment the subprocess had (nothing but
// PATH, plus HOME and FACTORY_HOME at an empty temp home and LC_ALL), and the corpus cases call Check as before.
// Case map: testdata/portmap/guardtools.txt. The build-graph case is in gt_build_test.go.

package guard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"factory/go/internal/spawn"
)

// ---- hook runs (main_test.go of guardtools) ---------------------------------------------------------------------

// gtRunOpts tunes a hook run: the home (default: a fresh empty temp dir), the working directory and extra environment.
type gtRunOpts struct {
	home string
	dir  string
	env  map[string]string
}

// gtModes are the two hook modes of test_priv_guard.MODES.
var gtModes = map[string][]string{"worker": {"--role", "coder"}, "hq": {"--hq"}}

var gtModeNames = []string{"worker", "hq"}

// gtHookRun calls HookMain with payload on stdin and args as its arguments, in the environment the old binary run
// had: every variable dropped but PATH, then HOME, FACTORY_HOME and LC_ALL set, then o.env.
func gtHookRun(t testing.TB, payload string, args []string, o gtRunOpts) g2Res {
	t.Helper()
	home := o.home
	if home == "" {
		home = t.TempDir()
	}
	var drop []string
	for _, kv := range os.Environ() {
		if k, _, _ := strings.Cut(kv, "="); k != "PATH" {
			drop = append(drop, k)
		}
	}
	set := map[string]string{"HOME": home, "FACTORY_HOME": home, "LC_ALL": "C.UTF-8"}
	if os.Getenv("PATH") == "" {
		set["PATH"] = "/usr/bin:/bin"
	}
	for k, v := range o.env {
		set[k] = v
	}
	return g2HookRaw(t, payload, g2Opts{args: args, cwd: o.dir, drop: drop, set: set})
}

func gtBashPayload(cmd string) string {
	b, err := json.Marshal(map[string]any{"tool_name": "Bash", "tool_input": map[string]any{"command": cmd}})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// gtRunCmdOpts is run_hook(cmd, mode, role): a Bash payload under mode ("" = none) and role ("" = none).
func gtRunCmdOpts(t testing.TB, cmd, mode, role string, o gtRunOpts) g2Res {
	t.Helper()
	var args []string
	if mode != "" {
		args = append(args, gtModes[mode]...)
	}
	if role != "" {
		args = append(args, "--role", role)
	}
	return gtHookRun(t, gtBashPayload(cmd), args, o)
}

// gtBlocked fails the test unless the hook exits 2 for cmd; it returns the result for message checks.
func gtBlocked(t testing.TB, cmd, mode, role string) g2Res {
	t.Helper()
	r := gtRunCmdOpts(t, cmd, mode, role, gtRunOpts{})
	if r.code != 2 {
		t.Errorf("should block (mode=%q role=%q): %q: exit %d\n%s", mode, role, cmd, r.code, r.stderr)
	}
	return r
}

// gtAllowed fails the test unless the hook exits 0 for cmd.
func gtAllowed(t testing.TB, cmd, mode, role string) {
	t.Helper()
	if r := gtRunCmdOpts(t, cmd, mode, role, gtRunOpts{}); r.code != 0 {
		t.Errorf("should allow (mode=%q role=%q): %q: exit %d\n%s", mode, role, cmd, r.code, r.stderr)
	}
}

// gtEachMode runs f for both hook modes.
func gtEachMode(f func(mode string)) {
	for _, m := range gtModeNames {
		f(m)
	}
}

// ---- corpus_test.go ---------------------------------------------------------------------------------------------
// ci/guard_corpus.json, copied to testdata/gt/guard_corpus.json, against Check.

// gtPlaceholder matches the corpus stand-ins (@LIVE_HOME@, @SHARED@, @SCRATCH@): they need a prepared tree.
var gtPlaceholder = regexp.MustCompile(`@[A-Z_]+@`)

type gtCorpusEntry struct {
	Cmd    string            `json:"cmd"`
	Modes  []string          `json:"modes"`
	Role   string            `json:"role"`
	Env    map[string]string `json:"env"`
	Config map[string]string `json:"config"`
}

func (e *gtCorpusEntry) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		return json.Unmarshal(b, &e.Cmd)
	}
	type plain gtCorpusEntry
	return json.Unmarshal(b, (*plain)(e))
}

func gtLoadCorpus(t *testing.T) (block, allow []gtCorpusEntry) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "gt", "guard_corpus.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		MustBlock []gtCorpusEntry `json:"must_block"`
		MustAllow []gtCorpusEntry `json:"must_allow"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("corpus is not valid JSON: %v", err)
	}
	return c.MustBlock, c.MustAllow
}

// gtCorpusCheck runs every entry that needs no env, config or placeholder through Check; wantBlock is the expected
// verdict. Entries with a role run in worker mode under it; strings and mode lists follow the corpus doc.
func gtCorpusCheck(t *testing.T, entries []gtCorpusEntry, wantBlock bool, defaultModes []string) int {
	t.Helper()
	checked := 0
	for _, e := range entries {
		if len(e.Env) > 0 || len(e.Config) > 0 || gtPlaceholder.MatchString(e.Cmd) {
			continue
		}
		modes := e.Modes
		if len(modes) == 0 {
			modes = defaultModes
		}
		for _, m := range modes {
			role, hq := e.Role, m == "hq"
			if role == "" && !hq {
				role = "coder"
			}
			if e.Role != "" {
				hq = false
			}
			err := Check(e.Cmd, t.TempDir(), role, hq)
			if wantBlock && err == nil {
				t.Errorf("must_block %q (mode %s, role %q) was allowed", e.Cmd, m, role)
			}
			if !wantBlock && err != nil {
				t.Errorf("must_allow %q (mode %s, role %q) was blocked: %v", e.Cmd, m, role, err)
			}
			checked++
		}
	}
	return checked
}

func TestGTGuardDiffCorpusMustBlock(t *testing.T) {
	t.Setenv("FACTORY_HOME", t.TempDir())
	block, _ := gtLoadCorpus(t)
	if n := gtCorpusCheck(t, block, true, []string{"worker"}); n < 1000 {
		t.Fatalf("only %d must_block entries checked; the corpus or the filter is broken", n)
	}
}

func TestGTGuardDiffCorpusMustAllow(t *testing.T) {
	t.Setenv("FACTORY_HOME", t.TempDir())
	_, allow := gtLoadCorpus(t)
	if n := gtCorpusCheck(t, allow, false, []string{"worker", "hq"}); n < 500 {
		t.Fatalf("only %d must_allow entries checked; the corpus or the filter is broken", n)
	}
}

// ---- fuzz_test.go -----------------------------------------------------------------------------------------------

// G8 Unicode parity: NBSP, vertical tab and U+0663 are not shell whitespace and not ASCII word characters, so a
// "sudo" glued to them is a different word (what the Python hook answered for the same input).
func TestGTPrivGuardUnicodeParity(t *testing.T) {
	for _, cmd := range []string{"sudo id", "echo hi; sudo id", "echo hi;\vsudo id", "sudo\vid", "٣sudo id",
		"echo ٣ sudo", "echo sudo", "su -", "docker ps", "ls | xargs sudo"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

// G7: the command parser must never panic and must never let a plain leading "sudo " through, whatever follows.
func FuzzGTPrivGuardCommand(f *testing.F) {
	for _, s := range []string{"id", "-n true", "'x", " ", "$(", "`", "\\\n", "a | b", "٣"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, tail string) {
		if err := Check("sudo "+tail, "/tmp", "coder", false); err == nil {
			t.Errorf("sudo %q was allowed", tail)
		}
		_ = Check(tail, "/tmp", "coder", true) // must not panic in HQ mode either
	})
}

// ---- priv_guard_test.go -----------------------------------------------------------------------------------------
// Port of tests/test_priv_guard.py (T-0057: PRIV rule, HQ mode, settings, cleaner branch deletion).

var gtHqRoles = []string{"boss", "director", "lead"}
var gtWorkerRoles = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}

var gtPrivBlocked = []string{
	"sudo -n true", "su -", "doas ls", "pkexec ls", "docker ps", "/usr/bin/sudo -n true", "./sudo x",
	"\"$(command -v sudo)\" -n true", "$(command -v sudo) -n true", "A=1 sudo -n true", "A=1 B=2 docker ps",
	"env A=1 sudo -n true", "env -u X sudo id", "nohup sudo id", "timeout 5 sudo id", "timeout -s KILL 5 sudo id",
	"ls | xargs sudo", "xargs -I{} sudo {}", "command sudo id", "exec sudo id", "nice -n 5 sudo id",
	"setsid sudo id", "stdbuf -o0 sudo id", "watch -n 5 sudo id", "watch 'sudo id'", "busybox su",
	"find . -exec sudo rm {} \\;", "find . -exec docker ps {} +", "bash -c 'sudo id'", "sh -c \"bash -c 'sudo id'\"",
	"zsh -c \"sh -c 'bash -c \\\"sudo id\\\"'\"", "echo $(sudo -n true)", "`sudo -n true`", "echo hi; sudo id",
	"true && sudo id", "false || sudo id", "echo a | sudo tee f", "(sudo id)", "{ sudo id; }", "sudo \\\n -n true",
	"su''do id", "s\\udo id", "'sudo' id", "$(sudo id)", "python3 <<'E'\nimport os; os.system('sudo id')\nE",
	"python3 -c \"import os; os.system('sudo id')\"", "python3 -W ignore -c \"import os; os.system('sudo id')\"",
	"python3 -X dev -c \"import os; os.system('docker ps')\"", "env -S 'sudo id'", "if true; then sudo id; fi",
	"ssh host sudo id", "bash <<'E'\nsudo id\nE", "eval 'sudo id'", "! sudo id", "time sudo id",
}

var gtPrivAllowed = []string{
	"grep -rn sudo README.md", "rg -n sudo roles/", "git commit -m \"block sudo\"",
	"gh pr create --body \"x sudo -n true y\"", "echo su", "cat docs/sudo.md", "python3 -m unittest tests.test_sudo_block",
	"factory msg x \"sudo\" \"y\"", "factory task note T-1 - <<'X'\nuse sudo here\nX", "git log --format=%s",
	"ls /usr/bin/sudo", "which sudo", "command -v sudo", "echo 'docker ps'", "echo \"docker\"",
	"ls tests/test_sudo_block.py", "echo sum sudoku", "git status -su", "ps -o user,su", "timeout 5 grep sudo f",
	"xargs echo sudo", "env FOO=1 grep sudo f", "find . -name sudo", "find . -name x -exec grep sudo {} \\;",
	"nice -n 5 make", "cat <<'E'\nsudo docker\nE", "gh pr create --body \"$(cat <<'EOF'\nuse sudo\nEOF\n)\"",
	"watch -n 5 ls", "echo $(date) sudo", "python3 -c \"print(1)\"", "type sudo", "man sudo",
	"bash -c 'echo sudo'", "grep -n 'su ' README.md", "sudoku", "ls | grep docker", "git grep -n pkexec",
}

// worker mode blocks these (herdr / factory units / broad reads / identity); HQ mode runs only PRIV
var gtHqOnlyAllowed = []string{
	"herdr pane list", "factory spawn coder --task T-1", "factory res", "factory task list",
	"systemctl --user status factory-tick.timer", "$EDITOR file",
	"$X y", "setsid -f sleep 1", "FACTORY_AGENT=director factory agents", "unset FACTORY_ROLE",
	"python3 -W ignore bin/factory tick", "bash -c 'echo `date`'", "echo `git log -1`",
}


func TestGTPrivGuardPrivBlockedInBothModes(t *testing.T) {
	if len(gtPrivBlocked) < 20 {
		t.Fatalf("only %d blocked commands", len(gtPrivBlocked))
	}
	gtEachMode(func(mode string) {
		for _, cmd := range gtPrivBlocked {
			r := gtBlocked(t, cmd, mode, "")
			if !strings.Contains(r.stderr, "privilege escalation") || !strings.Contains(r.stderr, "owner M803") {
				t.Errorf("mode=%s %q: stderr lacks the privilege-escalation reason: %s", mode, cmd, r.stderr)
			}
		}
	})
}

func TestGTPrivGuardReasonNamesTheWord(t *testing.T) {
	for _, c := range [][2]string{{"sudo -n true", "sudo"}, {"/usr/bin/doas ls", "doas"}, {"env A=1 pkexec x", "pkexec"},
		{"bash -c 'su -'", "su"}, {"timeout 5 docker ps", "docker"}, {"x=1 sh -c \"sh -c 'sudo id'\"", "sudo"}} {
		gtEachMode(func(mode string) {
			want := "(`" + c[1] + "`)"
			if r := gtBlocked(t, c[0], mode, ""); !strings.Contains(r.stderr, want) {
				t.Errorf("mode=%s %q: stderr lacks %s: %s", mode, c[0], want, r.stderr)
			}
		})
	}
}

func TestGTPrivGuardNoFalsePositivesInBothModes(t *testing.T) {
	if len(gtPrivAllowed) < 25 {
		t.Fatalf("only %d allowed commands", len(gtPrivAllowed))
	}
	gtEachMode(func(mode string) {
		for _, cmd := range gtPrivAllowed {
			gtAllowed(t, cmd, mode, "")
		}
	})
}

func TestGTPrivGuardEveryWorkerRoleBlocked(t *testing.T) {
	for _, role := range gtWorkerRoles {
		gtBlocked(t, "sudo -n true", "", role)
		gtBlocked(t, "env A=1 docker ps", "", role)
	}
}

func TestGTPrivGuardHqModeRunsOnlyPriv(t *testing.T) {
	for _, cmd := range gtHqOnlyAllowed {
		gtAllowed(t, cmd, "hq", "")
	}
}

func TestGTPrivGuardWorkerModeStillBlocksHerdrRules(t *testing.T) {
	for _, cmd := range []string{"herdr pane list", "factory spawn coder --task T-1", "factory res",
		"systemctl --user restart factory-tick.timer", "$X y"} {
		gtBlocked(t, cmd, "worker", "")
	}
}

func TestGTPrivGuardHqUnparseableAllowsWorkerFailsClosed(t *testing.T) {
	gtAllowed(t, "echo 'unbalanced", "hq", "")
	gtBlocked(t, "echo 'unbalanced", "worker", "")
}

func TestGTPrivGuardHqDynamicCommandWordNotBlocked(t *testing.T) {
	gtAllowed(t, "$X --version", "hq", "")
	gtAllowed(t, "${EDITOR} f", "hq", "")
	gtBlocked(t, "$X --version", "worker", "")
}

func TestGTPrivGuardHqBadJsonFailsClosed(t *testing.T) {
	if r := gtHookRun(t, "{not json", []string{"--hq"}, gtRunOpts{}); r.code != 2 {
		t.Errorf("bad json under --hq: exit %d, want 2\n%s", r.code, r.stderr)
	}
}

func TestGTPrivGuardNonBashToolAllowed(t *testing.T) {
	payload := `{"tool_name": "Read", "tool_input": {"file_path": "/x"}}`
	for _, args := range [][]string{{"--hq"}, {"--role", "coder"}} {
		if r := gtHookRun(t, payload, args, gtRunOpts{}); r.code != 0 {
			t.Errorf("Read tool under %v: exit %d, want 0\n%s", args, r.code, r.stderr)
		}
	}
}

func TestGTPrivGuardScriptOnDiskWithSudoBlockedInHq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\nsudo -n true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gtBlocked(t, "bash "+path, "hq", "")
	gtBlocked(t, "source "+path, "hq", "")
}

func TestGTPrivGuardQaReproduction(t *testing.T) {
	gtEachMode(func(mode string) {
		gtBlocked(t, "sudo -n true", mode, "")
		gtAllowed(t, "grep -rn sudo README.md", mode, "")
	})
}

// ---- InterpreterOptionTests (reviewer-t-0047 N1) ----------------------------------------------------------------

// gtLiveHome is the fixed stand-in for the Python LIVE_HOME (~/factory): a test never reads the machine's real home.
const gtLiveHome = "/zz/live"

func TestGTPrivGuardInterpreterPythonWXValues(t *testing.T) {
	live := gtLiveHome + "/bin/factory"
	for _, cmd := range []string{"python3 -W ignore " + live + " tick", "python3 -X dev " + live + " spawn coder --task T-1",
		"python3 -W error -X dev " + live + " up",
		"python3 -W ignore -c 'import os; os.system(\"herdr pane close\")'",
		"python3 -X dev -c 'import os; os.system(\"herdr pane close\")'"} {
		gtBlocked(t, cmd, "worker", "")
	}
}

func TestGTPrivGuardInterpreterPythonWXValuesPriv(t *testing.T) {
	for _, cmd := range []string{"python3 -W ignore -c 'import os; os.system(\"sudo id\")'",
		"python3 -X dev -c 'import os; os.system(\"sudo id\")'"} {
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardInterpreterPythonWXNormalUseAllowed(t *testing.T) {
	for _, cmd := range []string{"python3 -W ignore -m unittest tests.test_x", "python3 -X dev bin/other.py",
		"python3 -W ignore -c 'print(1)'"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

// ---- ReviewRound2Tests (reviewer-t-0057 B1-B4 and nits) ---------------------------------------------------------

func TestGTPrivGuardRound2B1PythonPlainFlagsDoNotHideFactoryUnit(t *testing.T) {
	// main blocks all of these (exit 2); a flag that is not a value option must not shift the script position
	for _, flag := range []string{"-I", "-E", "-d", "-x", "-Wignore", "-B -E", "-Xdev", "-W ignore -I", "-IBs", "-OO", "-u -E"} {
		for _, sub := range []string{"tick", "up", "spawn coder", "res", "relaunch", "task list"} {
			gtBlocked(t, "python3 "+flag+" bin/factory "+sub, "worker", "")
		}
	}
}

func TestGTPrivGuardRound2B1UnknownPythonOptionFailsClosed(t *testing.T) {
	for _, cmd := range []string{"python3 -Z bin/factory tick", "python3 --weird bin/factory tick", "python3 -Zq bin/factory up",
		"deno run bin/factory tick", "node --foo bin/factory tick"} {
		gtBlocked(t, cmd, "worker", "")
	}
}

func TestGTPrivGuardRound2B1PrivInCodeBehindFlags(t *testing.T) {
	code := "\"import os; os.system('sudo id')\""
	for _, flag := range []string{"-I", "-E", "-d", "-x", "-Wignore", "-E -s", "-B -E", "-Xdev", "-W ignore", "-IBs", "-Ic"} {
		cmd := "python3 " + flag + " -c " + code
		if strings.HasSuffix(flag, "c") {
			cmd = "python3 " + flag + " " + code
		}
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
	for _, cmd := range []string{"perl -e \"system('sudo id')\"", "perl -I lib -e \"system('sudo id')\"",
		"ruby -r json -e \"system('sudo id')\"", "node -r x -e \"require('child_process').execSync('sudo id')\"",
		"node -p \"require('child_process').execSync('docker ps')\""} {
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B1UnknownOptionScansCodeWords(t *testing.T) {
	gtEachMode(func(mode string) { gtBlocked(t, "python3 -Zq -c \"import os; os.system('sudo id')\"", mode, "") })
}

func TestGTPrivGuardRound2B1NormalPythonAllowed(t *testing.T) {
	for _, cmd := range []string{"python3 -I -c 'print(1)'", "python3 -E bin/other.py", "python3 -Wignore -m unittest tests.test_x",
		"python3 -x script.py", "python3 -d -c 'print(2)'", "python3 -E -s -c 'print(3)'",
		"perl -e 'print 1'", "node -e 'console.log(1)'"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B2SingleQuotedSubstitutionsAreData(t *testing.T) {
	for _, cmd := range []string{"git commit -m 'block `docker`'", "factory msg lead 'x' 'never run `sudo -n true`'",
		"echo 'a $(sudo id) b'", "echo '`sudo id`'", "gh pr create --body 'use `docker ps` and $(su -)'",
		"echo 'x' 'y $(docker ps)'", "factory msg lead s 'a `sudo` b' --kind report"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B2LiveSubstitutionsStillBlocked(t *testing.T) {
	for _, cmd := range []string{"echo \"a $(sudo id) b\"", "echo \"a `sudo id` b\"", "echo $(sudo id)", "echo `sudo id`",
		"echo 'x'; echo \"$(sudo id)\"", "echo 'x' \"$(docker ps)\"", "echo \"it's $(sudo id)\""} {
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B3WorkerGlobCommandWord(t *testing.T) {
	for _, cmd := range []string{"/usr/bin/s?do -n true", "/usr/bin/sud[o] -n true", "s*o id", "/usr/bin/d?cker ps", "/usr/bin/su* x",
		"env /usr/bin/s?do id", "nohup /usr/bin/sud? id", "(/usr/bin/s?do id)"} {
		gtBlocked(t, cmd, "worker", "")
	}
}

func TestGTPrivGuardRound2B3GlobAllowedCases(t *testing.T) {
	for _, cmd := range []string{"ls /usr/bin/s?do", "grep -n 's*o' f", "[ -f x ]", "[[ -f x ]]", "ls *.py", "echo /usr/bin/s?do"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B3HqDoesNotBlockGlobs(t *testing.T) {
	gtAllowed(t, "/usr/bin/s?do -n true", "hq", "")
}

func TestGTPrivGuardRound2B4GitSedAwkForms(t *testing.T) {
	for _, cmd := range []string{"git -c alias.x='!sudo id' x", "git -c core.pager='sudo less' log", "git -c core.sshCommand='docker x' fetch",
		"git config alias.x '!sudo id'", "git config core.editor 'sudo vi'", "git -C . -c alias.y='!docker ps' y",
		"sed -n '1e sudo id' f", "sed -e 'e docker ps' f", "sed 's/x/sudo id/e' f", "sed '$e su -' f",
		"awk 'BEGIN{system(\"sudo id\")}'", "awk 'BEGIN{\"docker ps\" | getline x}'",
		"awk 'BEGIN{print 1 | \"sudo tee f\"}'", "gawk 'BEGIN{system(\"pkexec ls\")}'"} {
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2B4NormalGitSedAwkAllowed(t *testing.T) {
	for _, cmd := range []string{"git -c user.name=x commit -m 'sudo'", "git config --get user.name", "git -c alias.x='status' x",
		"git config --get core.editor", "sed -n '1p' f", "sed 's/sudo/x/' f", "sed -n '/sudo/p' f",
		"awk '/sudo|su/ {print}' f", "awk '{print $1}' f", "awk 'BEGIN{print \"sudo\"}'",
		"sed -i 's/docker/podman/g' f", "git log --grep=sudo"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

func TestGTPrivGuardRound2NitsTimeFunctionTrap(t *testing.T) {
	for _, cmd := range []string{"time -p sudo id", "time sudo id", "function f { sudo id; }", "function f { docker ps; }; f",
		"trap 'sudo id' EXIT", "trap -- 'docker ps' INT", "f() { sudo id; }"} {
		gtEachMode(func(mode string) { gtBlocked(t, cmd, mode, "") })
	}
	for _, cmd := range []string{"time -p ls", "function f { echo sudo; }", "trap 'echo done' EXIT", "trap - EXIT"} {
		gtEachMode(func(mode string) { gtAllowed(t, cmd, mode, "") })
	}
}

// ---- BroadReadAdditionTests -------------------------------------------------------------------------------------

func TestGTPrivGuardBroadReadBlockedOnLiveHome(t *testing.T) {
	if r := gtBlocked(t, "factory lesson list", "", "coder"); !strings.Contains(r.stderr, "broad factory reads") {
		t.Errorf("stderr lacks \"broad factory reads\": %s", r.stderr)
	}
}

func TestGTPrivGuardBroadReadTempHomeAllowed(t *testing.T) {
	gtAllowed(t, "FACTORY_HOME=/tmp/qa-home factory lesson list", "", "coder")
}

func TestGTPrivGuardBroadReadStaffEngineerExemptAndOtherLessonFormsAllowed(t *testing.T) {
	gtAllowed(t, "factory lesson list", "", "staff-engineer")
	for _, cmd := range []string{"factory lesson show 3", "factory lesson add x", "factory lesson edit 3"} {
		gtAllowed(t, cmd, "", "coder")
	}
}

func TestGTPrivGuardBroadReadHqUnaffected(t *testing.T) {
	gtAllowed(t, "factory lesson list", "hq", "")
}

// ---- CleanerBranchDeleteTests -----------------------------------------------------------------------------------

func TestGTPrivGuardCleanerAllowedForFactoryBranches(t *testing.T) {
	for _, cmd := range []string{"git branch -D factory/t-1", "git branch -d factory/t-1", "git branch --delete factory/t-1",
		"git push origin --delete factory/t-1", "git push origin -d factory/t-1",
		"git push origin :factory/t-1", "git branch -D factory/t-1 factory/t-2",
		"git -C /tmp/x push origin --delete factory/t-1", "git branch -D factory/g-005/t-1"} {
		gtAllowed(t, cmd, "", "cleaner")
	}
}

func TestGTPrivGuardCleanerRefusedForOtherTargets(t *testing.T) {
	for _, cmd := range []string{"git branch -D main", "git branch -D state", "git push origin --delete main",
		"git push origin --delete state", "git push origin :main", "git push origin --delete feature/x",
		"git branch -D feature/x", "git branch -D factory/t-1 main", "git push origin --delete factory/t-1 main",
		"git push origin --delete 'factory/*'", "git branch -D 'factory/*'", "git branch -D factory/*",
		"git branch -D factory/../main", "git branch -D", "git push origin --delete",
		"git push --delete factory/t-1", "git push origin :factory/t-1 main",
		"git push origin HEAD", "git push origin factory/t-1", "git push --force origin factory/t-1",
		"git push origin +:main", "git push origin -d main factory/t-1"} {
		gtBlocked(t, cmd, "", "cleaner")
	}
}

func TestGTPrivGuardCleanerWrappersAndChains(t *testing.T) {
	gtAllowed(t, "env A=1 git branch -D factory/t-1", "", "cleaner")
	gtAllowed(t, "git branch -D factory/t-1 && git push origin --delete factory/t-1", "", "cleaner")
	gtBlocked(t, "env A=1 git branch -D main", "", "cleaner")
	gtBlocked(t, "timeout 5 git push origin --delete main", "", "cleaner")
	gtBlocked(t, "git branch -D factory/t-1; git branch -D main", "", "cleaner")
	gtBlocked(t, "bash -c 'git push origin :main'", "", "cleaner")
}

func TestGTPrivGuardOtherWorkerRolesRefusedEvenForFactory(t *testing.T) {
	// role "" is Python's None: no --role at all
	for _, role := range []string{"pm", "coder", "qa", "reviewer", "judge", "staff-engineer", ""} {
		for _, cmd := range []string{"git branch -D factory/t-1", "git push origin --delete factory/t-1",
			"git push origin :factory/t-1"} {
			gtBlocked(t, cmd, "", role)
		}
	}
}

// guardExport writes <home>/run/guard-export.json (what the tick writes; the Go guard reads it, not the DB, T-0767).
// generated_at must be fresh: the guard rejects an export older than 15 minutes against the wall clock.
func gtGuardExport(t testing.TB, home string, taskWorktrees map[string]string) {
	t.Helper()
	doc := map[string]any{"version": 1, "generated_at": time.Now().UTC().Format(time.RFC3339), "config": map[string]string{},
		"agent_goals": map[string]string{}, "task_worktrees": taskWorktrees}
	b, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "run"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "run", "guard-export.json"), b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGTPrivGuardCleanerNormalGitUnaffected(t *testing.T) {
	for _, role := range []string{"cleaner", "coder"} {
		for _, cmd := range []string{"git branch", "git branch -a", "git branch --list 'factory/*'", "git branch --show-current",
			"git branch -vv", "git status", "git log --oneline", "git branch --sort=-committerdate"} {
			gtAllowed(t, cmd, "", role)
		}
	}
	for _, cmd := range []string{"git push -u origin HEAD", "git push origin factory/t-1", "git push"} {
		gtAllowed(t, cmd, "", "judge")
	}
	// T-0743: the coder push pin resolves the own worktree (here the hook cwd) from the home's task worktrees
	home := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "db"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "db", "factory.db"), nil, 0o644); err != nil { // an empty file is an empty SQLite DB
		t.Fatal(err)
	}
	gtGuardExport(t, home, map[string]string{"T-1": cwd})
	r := gtRunCmdOpts(t, "git push origin HEAD:factory/t-1", "", "coder",
		gtRunOpts{home: home, dir: cwd, env: map[string]string{"FACTORY_AGENT": "coder-t-1"}})
	if r.code != 0 {
		t.Errorf("coder push pin to the own branch: exit %d, want 0\n%s", r.code, r.stderr)
	}
}

func TestGTPrivGuardCleanerHqUnaffected(t *testing.T) {
	gtAllowed(t, "git branch -D main", "hq", "")
}

// ---- SettingsTests ----------------------------------------------------------------------------------------------
// The Python tests render the settings with bin/factory render_all_settings; here the Go renderer (internal/spawn,
// the one the Go spawn path uses) renders the same roles/settings/*.json.in with the fixed gtLiveHome.

type gtSettingsFile struct {
	Permissions struct {
		Allow []string `json:"allow"`
		Deny  []string `json:"deny"`
	} `json:"permissions"`
	Hooks struct {
		PreToolUse []struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"PreToolUse"`
	} `json:"hooks"`
}

func (s gtSettingsFile) bashHookCommands() []string {
	var out []string
	for _, e := range s.Hooks.PreToolUse {
		if e.Matcher == "Bash" {
			for _, h := range e.Hooks {
				out = append(out, h.Command)
			}
		}
	}
	return out
}

// renderedSettings renders every roles/settings/<role>.json.in: role -> parsed file.
func gtRenderedSettings(t testing.TB) map[string]gtSettingsFile {
	t.Helper()
	rolesDir := filepath.Join(g2RepoRoot(t), "roles")
	files, err := filepath.Glob(filepath.Join(rolesDir, "settings", "*.json.in"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]gtSettingsFile{}
	for _, f := range files {
		role := strings.TrimSuffix(filepath.Base(f), ".json.in")
		text, ref := spawn.SettingsFromDir(rolesDir, role, spawn.SettingsTokens(gtLiveHome, "/zz", "/zz/.config"))
		if ref != nil {
			t.Fatalf("render %s: %v", role, ref)
		}
		var s gtSettingsFile
		if err := json.Unmarshal([]byte(text), &s); err != nil {
			t.Fatalf("rendered %s settings are not JSON: %v", role, err)
		}
		out[role] = s
	}
	return out
}

func gtSortedKeys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func gtContains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestGTPrivGuardSettingsAllRoleFiles(t *testing.T) {
	want := append(append([]string{}, gtHqRoles...), gtWorkerRoles...)
	sort.Strings(want)
	if got := gtSortedKeys(gtRenderedSettings(t)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("settings files = %v, want %v", got, want)
	}
}

func TestGTPrivGuardSettingsAllRolesDenyPrivCommands(t *testing.T) {
	deny := []string{"Bash(sudo:*)", "Bash(su:*)", "Bash(doas:*)", "Bash(pkexec:*)", "Bash(docker:*)"}
	for role, s := range gtRenderedSettings(t) {
		for _, e := range deny {
			if !gtContains(s.Permissions.Deny, e) {
				t.Errorf("%s: deny list lacks %s", role, e)
			}
		}
	}
}

func TestGTPrivGuardSettingsHqRolesRunHqMode(t *testing.T) {
	all := gtRenderedSettings(t)
	for _, role := range gtHqRoles {
		cmds := all[role].bashHookCommands()
		if len(cmds) != 1 {
			t.Errorf("%s: %d Bash hooks, want 1: %v", role, len(cmds), cmds)
			continue
		}
		// T-1803: the boss hook also passes --role boss (hq_deny_hit needs the role); --hq stays
		want := "roles/hooks/worker-guard.py --hq"
		if role == "boss" {
			want += " --role boss"
		}
		if !strings.HasSuffix(cmds[0], want) || (role != "boss" && strings.Contains(cmds[0], "--role")) {
			t.Errorf("%s: hook command %q is not the --hq hook", role, cmds[0])
		}
	}
}

func TestGTPrivGuardSettingsWorkerRolesKeepWorkerMode(t *testing.T) {
	all := gtRenderedSettings(t)
	for _, role := range gtWorkerRoles {
		cmds := all[role].bashHookCommands()
		want := "sh " + gtLiveHome + "/roles/hooks/worker-guard --role " + role // T-0356: the shim
		if len(cmds) != 1 || cmds[0] != want || strings.Contains(cmds[0], "--hq") {
			t.Errorf("%s: hook commands %v, want [%s]", role, cmds, want)
		}
	}
}

func TestGTPrivGuardSettingsCleanerBranchDeleteScope(t *testing.T) {
	p := gtRenderedSettings(t)["cleaner"].Permissions
	for _, e := range []string{"Bash(git branch -D factory/*)", "Bash(git push origin --delete factory/*)"} {
		if !gtContains(p.Allow, e) {
			t.Errorf("cleaner allow lacks %s", e)
		}
	}
	for _, e := range []string{"Bash(git branch -D:*)", "Bash(git push origin --delete:*)", "Bash(git push:*)"} {
		if gtContains(p.Deny, e) {
			t.Errorf("cleaner deny has %s", e)
		}
	}
	for _, e := range []string{"Bash(git push origin main:*)", "Bash(git push --force:*)", "Bash(git push -f:*)",
		"Bash(git push origin HEAD:main:*)", "Bash(git commit:*)"} {
		if !gtContains(p.Deny, e) {
			t.Errorf("cleaner deny lacks %s", e)
		}
	}
}

func TestGTPrivGuardSettingsOtherRolesUnchangedOnBranchDelete(t *testing.T) {
	all := gtRenderedSettings(t)
	for _, role := range append(append([]string{}, gtHqRoles...), gtWorkerRoles...) {
		if role == "cleaner" {
			continue
		}
		p := all[role].Permissions
		// M40734: the boss alone may run exactly these gh pr merge forms (both option orders); nothing else
		bossOK := map[string]bool{}
		if role == "boss" {
			for _, repo := range []string{"hivemill", "hivemill-www"} {
				bossOK["Bash(gh pr merge * --repo volekerb/"+repo+" --squash --delete-branch)"] = true
				bossOK["Bash(gh pr merge * --squash --delete-branch --repo volekerb/"+repo+")"] = true
			}
		}
		for _, a := range p.Allow {
			if (strings.Contains(a, "git branch") || strings.Contains(a, "--delete")) && !bossOK[a] {
				t.Errorf("%s: allow has %q", role, a)
			}
			if strings.Contains(a, "--admin") {
				t.Errorf("%s: allow has %q", role, a)
			}
		}
		if role != "coder" && !gtContains(p.Deny, "Bash(git push:*)") {
			t.Errorf("%s: deny lacks Bash(git push:*)", role)
		}
	}
}

func TestGTPrivGuardSettingsHookPathsExist(t *testing.T) {
	for role, s := range gtRenderedSettings(t) {
		want := "sh " + gtLiveHome + "/roles/hooks/worker-guard "
		if gtContains(gtHqRoles, role) {
			want = "python3 " + gtLiveHome + "/roles/hooks/worker-guard.py"
		}
		for _, cmd := range s.bashHookCommands() {
			if !strings.HasPrefix(cmd, want) {
				t.Errorf("%s: hook command %q does not start with %q", role, cmd, want)
			}
		}
	}
}
