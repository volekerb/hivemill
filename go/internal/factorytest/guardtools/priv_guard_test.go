package guardtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	ft "factory/go/internal/factorytest"
	"factory/go/internal/spawn"
)

// Port of tests/test_priv_guard.py (T-0057: PRIV rule, HQ mode, settings, cleaner branch deletion): 45 of 45 cases,
// one top-level Go test per Python test method. The Python subTest loops are plain loops here; a failing command is
// named in the failure text. Every command runs through the real worker-guard binary (see main_test.go).
// Python's tests/guard_impl.py hook_run runs the Python hook; here the Go guard is the only guard (T-1473 deletes the
// Python one).

var hqRoles = []string{"boss", "director", "lead"}
var workerRoles = []string{"pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "researcher", "scout", "retro", "evaluator"}

var privBlocked = []string{
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

var privAllowed = []string{
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
var hqOnlyAllowed = []string{
	"herdr pane list", "factory spawn coder --task T-1", "factory res", "factory task list",
	"systemctl --user status factory-tick.timer", "$EDITOR file",
	"$X y", "setsid -f sleep 1", "FACTORY_AGENT=director factory agents", "unset FACTORY_ROLE",
	"python3 -W ignore bin/factory tick", "bash -c 'echo `date`'", "echo `git log -1`",
}

// eachMode runs f for both hook modes.
func eachMode(f func(mode string)) {
	for _, m := range modeNames {
		f(m)
	}
}

func TestPrivGuardPrivBlockedInBothModes(t *testing.T) {
	if len(privBlocked) < 20 {
		t.Fatalf("only %d blocked commands", len(privBlocked))
	}
	eachMode(func(mode string) {
		for _, cmd := range privBlocked {
			r := blocked(t, cmd, mode, "")
			if !strings.Contains(r.stderr, "privilege escalation") || !strings.Contains(r.stderr, "owner M803") {
				t.Errorf("mode=%s %q: stderr lacks the privilege-escalation reason: %s", mode, cmd, r.stderr)
			}
		}
	})
}

func TestPrivGuardReasonNamesTheWord(t *testing.T) {
	for _, c := range [][2]string{{"sudo -n true", "sudo"}, {"/usr/bin/doas ls", "doas"}, {"env A=1 pkexec x", "pkexec"},
		{"bash -c 'su -'", "su"}, {"timeout 5 docker ps", "docker"}, {"x=1 sh -c \"sh -c 'sudo id'\"", "sudo"}} {
		eachMode(func(mode string) {
			want := "(`" + c[1] + "`)"
			if r := blocked(t, c[0], mode, ""); !strings.Contains(r.stderr, want) {
				t.Errorf("mode=%s %q: stderr lacks %s: %s", mode, c[0], want, r.stderr)
			}
		})
	}
}

func TestPrivGuardNoFalsePositivesInBothModes(t *testing.T) {
	if len(privAllowed) < 25 {
		t.Fatalf("only %d allowed commands", len(privAllowed))
	}
	eachMode(func(mode string) {
		for _, cmd := range privAllowed {
			allowed(t, cmd, mode, "")
		}
	})
}

func TestPrivGuardEveryWorkerRoleBlocked(t *testing.T) {
	for _, role := range workerRoles {
		blocked(t, "sudo -n true", "", role)
		blocked(t, "env A=1 docker ps", "", role)
	}
}

func TestPrivGuardHqModeRunsOnlyPriv(t *testing.T) {
	for _, cmd := range hqOnlyAllowed {
		allowed(t, cmd, "hq", "")
	}
}

func TestPrivGuardWorkerModeStillBlocksHerdrRules(t *testing.T) {
	for _, cmd := range []string{"herdr pane list", "factory spawn coder --task T-1", "factory res",
		"systemctl --user restart factory-tick.timer", "$X y"} {
		blocked(t, cmd, "worker", "")
	}
}

func TestPrivGuardHqUnparseableAllowsWorkerFailsClosed(t *testing.T) {
	allowed(t, "echo 'unbalanced", "hq", "")
	blocked(t, "echo 'unbalanced", "worker", "")
}

func TestPrivGuardHqDynamicCommandWordNotBlocked(t *testing.T) {
	allowed(t, "$X --version", "hq", "")
	allowed(t, "${EDITOR} f", "hq", "")
	blocked(t, "$X --version", "worker", "")
}

func TestPrivGuardHqBadJsonFailsClosed(t *testing.T) {
	if r := hookRun(t, "{not json", []string{"--hq"}, runOpts{}); r.exit != 2 {
		t.Errorf("bad json under --hq: exit %d, want 2\n%s", r.exit, r.stderr)
	}
}

func TestPrivGuardNonBashToolAllowed(t *testing.T) {
	payload := `{"tool_name": "Read", "tool_input": {"file_path": "/x"}}`
	for _, args := range [][]string{{"--hq"}, {"--role", "coder"}} {
		if r := hookRun(t, payload, args, runOpts{}); r.exit != 0 {
			t.Errorf("Read tool under %v: exit %d, want 0\n%s", args, r.exit, r.stderr)
		}
	}
}

func TestPrivGuardScriptOnDiskWithSudoBlockedInHq(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\necho hi\nsudo -n true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blocked(t, "bash "+path, "hq", "")
	blocked(t, "source "+path, "hq", "")
}

func TestPrivGuardQaReproduction(t *testing.T) {
	eachMode(func(mode string) {
		blocked(t, "sudo -n true", mode, "")
		allowed(t, "grep -rn sudo README.md", mode, "")
	})
}

// ---- InterpreterOptionTests (reviewer-t-0047 N1) ----------------------------------------------------------------

// liveHome is the fixed stand-in for the Python LIVE_HOME (~/factory): a test never reads the machine's real home.
const liveHome = "/zz/live"

func TestPrivGuardInterpreterPythonWXValues(t *testing.T) {
	live := liveHome + "/bin/factory"
	for _, cmd := range []string{"python3 -W ignore " + live + " tick", "python3 -X dev " + live + " spawn coder --task T-1",
		"python3 -W error -X dev " + live + " up",
		"python3 -W ignore -c 'import os; os.system(\"herdr pane close\")'",
		"python3 -X dev -c 'import os; os.system(\"herdr pane close\")'"} {
		blocked(t, cmd, "worker", "")
	}
}

func TestPrivGuardInterpreterPythonWXValuesPriv(t *testing.T) {
	for _, cmd := range []string{"python3 -W ignore -c 'import os; os.system(\"sudo id\")'",
		"python3 -X dev -c 'import os; os.system(\"sudo id\")'"} {
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
}

func TestPrivGuardInterpreterPythonWXNormalUseAllowed(t *testing.T) {
	for _, cmd := range []string{"python3 -W ignore -m unittest tests.test_x", "python3 -X dev bin/other.py",
		"python3 -W ignore -c 'print(1)'"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

// ---- ReviewRound2Tests (reviewer-t-0057 B1-B4 and nits) ---------------------------------------------------------

func TestPrivGuardRound2B1PythonPlainFlagsDoNotHideFactoryUnit(t *testing.T) {
	// main blocks all of these (exit 2); a flag that is not a value option must not shift the script position
	for _, flag := range []string{"-I", "-E", "-d", "-x", "-Wignore", "-B -E", "-Xdev", "-W ignore -I", "-IBs", "-OO", "-u -E"} {
		for _, sub := range []string{"tick", "up", "spawn coder", "res", "relaunch", "task list"} {
			blocked(t, "python3 "+flag+" bin/factory "+sub, "worker", "")
		}
	}
}

func TestPrivGuardRound2B1UnknownPythonOptionFailsClosed(t *testing.T) {
	for _, cmd := range []string{"python3 -Z bin/factory tick", "python3 --weird bin/factory tick", "python3 -Zq bin/factory up",
		"deno run bin/factory tick", "node --foo bin/factory tick"} {
		blocked(t, cmd, "worker", "")
	}
}

func TestPrivGuardRound2B1PrivInCodeBehindFlags(t *testing.T) {
	code := "\"import os; os.system('sudo id')\""
	for _, flag := range []string{"-I", "-E", "-d", "-x", "-Wignore", "-E -s", "-B -E", "-Xdev", "-W ignore", "-IBs", "-Ic"} {
		cmd := "python3 " + flag + " -c " + code
		if strings.HasSuffix(flag, "c") {
			cmd = "python3 " + flag + " " + code
		}
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
	for _, cmd := range []string{"perl -e \"system('sudo id')\"", "perl -I lib -e \"system('sudo id')\"",
		"ruby -r json -e \"system('sudo id')\"", "node -r x -e \"require('child_process').execSync('sudo id')\"",
		"node -p \"require('child_process').execSync('docker ps')\""} {
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B1UnknownOptionScansCodeWords(t *testing.T) {
	eachMode(func(mode string) { blocked(t, "python3 -Zq -c \"import os; os.system('sudo id')\"", mode, "") })
}

func TestPrivGuardRound2B1NormalPythonAllowed(t *testing.T) {
	for _, cmd := range []string{"python3 -I -c 'print(1)'", "python3 -E bin/other.py", "python3 -Wignore -m unittest tests.test_x",
		"python3 -x script.py", "python3 -d -c 'print(2)'", "python3 -E -s -c 'print(3)'",
		"perl -e 'print 1'", "node -e 'console.log(1)'"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B2SingleQuotedSubstitutionsAreData(t *testing.T) {
	for _, cmd := range []string{"git commit -m 'block `docker`'", "factory msg lead 'x' 'never run `sudo -n true`'",
		"echo 'a $(sudo id) b'", "echo '`sudo id`'", "gh pr create --body 'use `docker ps` and $(su -)'",
		"echo 'x' 'y $(docker ps)'", "factory msg lead s 'a `sudo` b' --kind report"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B2LiveSubstitutionsStillBlocked(t *testing.T) {
	for _, cmd := range []string{"echo \"a $(sudo id) b\"", "echo \"a `sudo id` b\"", "echo $(sudo id)", "echo `sudo id`",
		"echo 'x'; echo \"$(sudo id)\"", "echo 'x' \"$(docker ps)\"", "echo \"it's $(sudo id)\""} {
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B3WorkerGlobCommandWord(t *testing.T) {
	for _, cmd := range []string{"/usr/bin/s?do -n true", "/usr/bin/sud[o] -n true", "s*o id", "/usr/bin/d?cker ps", "/usr/bin/su* x",
		"env /usr/bin/s?do id", "nohup /usr/bin/sud? id", "(/usr/bin/s?do id)"} {
		blocked(t, cmd, "worker", "")
	}
}

func TestPrivGuardRound2B3GlobAllowedCases(t *testing.T) {
	for _, cmd := range []string{"ls /usr/bin/s?do", "grep -n 's*o' f", "[ -f x ]", "[[ -f x ]]", "ls *.py", "echo /usr/bin/s?do"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B3HqDoesNotBlockGlobs(t *testing.T) {
	allowed(t, "/usr/bin/s?do -n true", "hq", "")
}

func TestPrivGuardRound2B4GitSedAwkForms(t *testing.T) {
	for _, cmd := range []string{"git -c alias.x='!sudo id' x", "git -c core.pager='sudo less' log", "git -c core.sshCommand='docker x' fetch",
		"git config alias.x '!sudo id'", "git config core.editor 'sudo vi'", "git -C . -c alias.y='!docker ps' y",
		"sed -n '1e sudo id' f", "sed -e 'e docker ps' f", "sed 's/x/sudo id/e' f", "sed '$e su -' f",
		"awk 'BEGIN{system(\"sudo id\")}'", "awk 'BEGIN{\"docker ps\" | getline x}'",
		"awk 'BEGIN{print 1 | \"sudo tee f\"}'", "gawk 'BEGIN{system(\"pkexec ls\")}'"} {
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2B4NormalGitSedAwkAllowed(t *testing.T) {
	for _, cmd := range []string{"git -c user.name=x commit -m 'sudo'", "git config --get user.name", "git -c alias.x='status' x",
		"git config --get core.editor", "sed -n '1p' f", "sed 's/sudo/x/' f", "sed -n '/sudo/p' f",
		"awk '/sudo|su/ {print}' f", "awk '{print $1}' f", "awk 'BEGIN{print \"sudo\"}'",
		"sed -i 's/docker/podman/g' f", "git log --grep=sudo"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

func TestPrivGuardRound2NitsTimeFunctionTrap(t *testing.T) {
	for _, cmd := range []string{"time -p sudo id", "time sudo id", "function f { sudo id; }", "function f { docker ps; }; f",
		"trap 'sudo id' EXIT", "trap -- 'docker ps' INT", "f() { sudo id; }"} {
		eachMode(func(mode string) { blocked(t, cmd, mode, "") })
	}
	for _, cmd := range []string{"time -p ls", "function f { echo sudo; }", "trap 'echo done' EXIT", "trap - EXIT"} {
		eachMode(func(mode string) { allowed(t, cmd, mode, "") })
	}
}

// ---- BroadReadAdditionTests -------------------------------------------------------------------------------------

func TestPrivGuardBroadReadBlockedOnLiveHome(t *testing.T) {
	if r := blocked(t, "factory lesson list", "", "coder"); !strings.Contains(r.stderr, "broad factory reads") {
		t.Errorf("stderr lacks \"broad factory reads\": %s", r.stderr)
	}
}

func TestPrivGuardBroadReadTempHomeAllowed(t *testing.T) {
	allowed(t, "FACTORY_HOME=/tmp/qa-home factory lesson list", "", "coder")
}

func TestPrivGuardBroadReadStaffEngineerExemptAndOtherLessonFormsAllowed(t *testing.T) {
	allowed(t, "factory lesson list", "", "staff-engineer")
	for _, cmd := range []string{"factory lesson show 3", "factory lesson add x", "factory lesson edit 3"} {
		allowed(t, cmd, "", "coder")
	}
}

func TestPrivGuardBroadReadHqUnaffected(t *testing.T) {
	allowed(t, "factory lesson list", "hq", "")
}

// ---- CleanerBranchDeleteTests -----------------------------------------------------------------------------------

func TestPrivGuardCleanerAllowedForFactoryBranches(t *testing.T) {
	for _, cmd := range []string{"git branch -D factory/t-1", "git branch -d factory/t-1", "git branch --delete factory/t-1",
		"git push origin --delete factory/t-1", "git push origin -d factory/t-1",
		"git push origin :factory/t-1", "git branch -D factory/t-1 factory/t-2",
		"git -C /tmp/x push origin --delete factory/t-1", "git branch -D factory/g-005/t-1"} {
		allowed(t, cmd, "", "cleaner")
	}
}

func TestPrivGuardCleanerRefusedForOtherTargets(t *testing.T) {
	for _, cmd := range []string{"git branch -D main", "git branch -D state", "git push origin --delete main",
		"git push origin --delete state", "git push origin :main", "git push origin --delete feature/x",
		"git branch -D feature/x", "git branch -D factory/t-1 main", "git push origin --delete factory/t-1 main",
		"git push origin --delete 'factory/*'", "git branch -D 'factory/*'", "git branch -D factory/*",
		"git branch -D factory/../main", "git branch -D", "git push origin --delete",
		"git push --delete factory/t-1", "git push origin :factory/t-1 main",
		"git push origin HEAD", "git push origin factory/t-1", "git push --force origin factory/t-1",
		"git push origin +:main", "git push origin -d main factory/t-1"} {
		blocked(t, cmd, "", "cleaner")
	}
}

func TestPrivGuardCleanerWrappersAndChains(t *testing.T) {
	allowed(t, "env A=1 git branch -D factory/t-1", "", "cleaner")
	allowed(t, "git branch -D factory/t-1 && git push origin --delete factory/t-1", "", "cleaner")
	blocked(t, "env A=1 git branch -D main", "", "cleaner")
	blocked(t, "timeout 5 git push origin --delete main", "", "cleaner")
	blocked(t, "git branch -D factory/t-1; git branch -D main", "", "cleaner")
	blocked(t, "bash -c 'git push origin :main'", "", "cleaner")
}

func TestPrivGuardOtherWorkerRolesRefusedEvenForFactory(t *testing.T) {
	// role "" is Python's None: no --role at all
	for _, role := range []string{"pm", "coder", "qa", "reviewer", "judge", "staff-engineer", ""} {
		for _, cmd := range []string{"git branch -D factory/t-1", "git push origin --delete factory/t-1",
			"git push origin :factory/t-1"} {
			blocked(t, cmd, "", role)
		}
	}
}

// guardExport writes <home>/run/guard-export.json (what the tick writes; the Go guard reads it, not the DB, T-0767).
// generated_at must be fresh: the guard rejects an export older than 15 minutes against the wall clock.
func guardExport(t testing.TB, home string, taskWorktrees map[string]string) {
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

func TestPrivGuardCleanerNormalGitUnaffected(t *testing.T) {
	for _, role := range []string{"cleaner", "coder"} {
		for _, cmd := range []string{"git branch", "git branch -a", "git branch --list 'factory/*'", "git branch --show-current",
			"git branch -vv", "git status", "git log --oneline", "git branch --sort=-committerdate"} {
			allowed(t, cmd, "", role)
		}
	}
	for _, cmd := range []string{"git push -u origin HEAD", "git push origin factory/t-1", "git push"} {
		allowed(t, cmd, "", "judge")
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
	guardExport(t, home, map[string]string{"T-1": cwd})
	r := runCmdOpts(t, "git push origin HEAD:factory/t-1", "", "coder",
		runOpts{home: home, dir: cwd, env: map[string]string{"FACTORY_AGENT": "coder-t-1"}})
	if r.exit != 0 {
		t.Errorf("coder push pin to the own branch: exit %d, want 0\n%s", r.exit, r.stderr)
	}
}

func TestPrivGuardCleanerHqUnaffected(t *testing.T) {
	allowed(t, "git branch -D main", "hq", "")
}

// ---- SettingsTests ----------------------------------------------------------------------------------------------
// The Python tests render the settings with bin/factory render_all_settings; here the Go renderer (internal/spawn,
// the one the Go spawn path uses) renders the same roles/settings/*.json.in with the fixed liveHome.

type settingsFile struct {
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

func (s settingsFile) bashHookCommands() []string {
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
func renderedSettings(t testing.TB) map[string]settingsFile {
	t.Helper()
	repo, err := ft.RepoRoot()
	if err != nil {
		t.Fatal(err)
	}
	rolesDir := filepath.Join(repo, "roles")
	files, err := filepath.Glob(filepath.Join(rolesDir, "settings", "*.json.in"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]settingsFile{}
	for _, f := range files {
		role := strings.TrimSuffix(filepath.Base(f), ".json.in")
		text, ref := spawn.SettingsFromDir(rolesDir, role, spawn.SettingsTokens(liveHome, "/zz", "/zz/.config"))
		if ref != nil {
			t.Fatalf("render %s: %v", role, ref)
		}
		var s settingsFile
		if err := json.Unmarshal([]byte(text), &s); err != nil {
			t.Fatalf("rendered %s settings are not JSON: %v", role, err)
		}
		out[role] = s
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func TestPrivGuardSettingsAllRoleFiles(t *testing.T) {
	want := append(append([]string{}, hqRoles...), workerRoles...)
	sort.Strings(want)
	if got := sortedKeys(renderedSettings(t)); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("settings files = %v, want %v", got, want)
	}
}

func TestPrivGuardSettingsAllRolesDenyPrivCommands(t *testing.T) {
	deny := []string{"Bash(sudo:*)", "Bash(su:*)", "Bash(doas:*)", "Bash(pkexec:*)", "Bash(docker:*)"}
	for role, s := range renderedSettings(t) {
		for _, e := range deny {
			if !contains(s.Permissions.Deny, e) {
				t.Errorf("%s: deny list lacks %s", role, e)
			}
		}
	}
}

func TestPrivGuardSettingsHqRolesRunHqMode(t *testing.T) {
	all := renderedSettings(t)
	for _, role := range hqRoles {
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

func TestPrivGuardSettingsWorkerRolesKeepWorkerMode(t *testing.T) {
	all := renderedSettings(t)
	for _, role := range workerRoles {
		cmds := all[role].bashHookCommands()
		want := "sh " + liveHome + "/roles/hooks/worker-guard --role " + role // T-0356: the shim
		if len(cmds) != 1 || cmds[0] != want || strings.Contains(cmds[0], "--hq") {
			t.Errorf("%s: hook commands %v, want [%s]", role, cmds, want)
		}
	}
}

func TestPrivGuardSettingsCleanerBranchDeleteScope(t *testing.T) {
	p := renderedSettings(t)["cleaner"].Permissions
	for _, e := range []string{"Bash(git branch -D factory/*)", "Bash(git push origin --delete factory/*)"} {
		if !contains(p.Allow, e) {
			t.Errorf("cleaner allow lacks %s", e)
		}
	}
	for _, e := range []string{"Bash(git branch -D:*)", "Bash(git push origin --delete:*)", "Bash(git push:*)"} {
		if contains(p.Deny, e) {
			t.Errorf("cleaner deny has %s", e)
		}
	}
	for _, e := range []string{"Bash(git push origin main:*)", "Bash(git push --force:*)", "Bash(git push -f:*)",
		"Bash(git push origin HEAD:main:*)", "Bash(git commit:*)"} {
		if !contains(p.Deny, e) {
			t.Errorf("cleaner deny lacks %s", e)
		}
	}
}

func TestPrivGuardSettingsOtherRolesUnchangedOnBranchDelete(t *testing.T) {
	all := renderedSettings(t)
	for _, role := range append(append([]string{}, hqRoles...), workerRoles...) {
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
		if role != "coder" && !contains(p.Deny, "Bash(git push:*)") {
			t.Errorf("%s: deny lacks Bash(git push:*)", role)
		}
	}
}

func TestPrivGuardSettingsHookPathsExist(t *testing.T) {
	for role, s := range renderedSettings(t) {
		want := "sh " + liveHome + "/roles/hooks/worker-guard "
		if contains(hqRoles, role) {
			want = "python3 " + liveHome + "/roles/hooks/worker-guard.py"
		}
		for _, cmd := range s.bashHookCommands() {
			if !strings.HasPrefix(cmd, want) {
				t.Errorf("%s: hook command %q does not start with %q", role, cmd, want)
			}
		}
	}
}
