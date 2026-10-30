"""T-0057: PRIV rule (sudo/su/doas/pkexec/docker blocked for every agent), HQ mode, settings, cleaner branch deletion."""
import glob
import json
import os
import subprocess
import sqlite3
import sys
import pwd
import tempfile
import shutil
from unittest import mock
import unittest

LIVE_HOME = os.path.join(pwd.getpwuid(os.getuid()).pw_dir, "factory")  # T-0212
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HOOK = os.path.join(ROOT, "roles", "hooks", "worker-guard.py")
sys.path.insert(0, os.path.join(ROOT, "tests"))
from harness import rendered_settings_dir  # noqa: E402

SETTINGS = sorted(glob.glob(os.path.join(str(rendered_settings_dir()), "*.json")))
DENY = ["Bash(sudo:*)", "Bash(su:*)", "Bash(doas:*)", "Bash(pkexec:*)", "Bash(docker:*)"]
HQ_ROLES = ("boss", "director", "lead")
WORKER_ROLES = ("pm", "coder", "qa", "reviewer", "judge", "cleaner", "staff-engineer", "scout", "evaluator")
MODES = {"worker": ["--role", "coder"], "hq": ["--hq"]}

BLOCKED = [
    "sudo -n true", "su -", "doas ls", "pkexec ls", "docker ps", "/usr/bin/sudo -n true", "./sudo x",
    '"$(command -v sudo)" -n true', "$(command -v sudo) -n true", "A=1 sudo -n true", "A=1 B=2 docker ps",
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
]
ALLOWED = [
    "grep -rn sudo README.md", "rg -n sudo roles/", 'git commit -m "block sudo"',
    'gh pr create --body "x sudo -n true y"', "echo su", "cat docs/sudo.md", "python3 -m unittest tests.test_sudo_block",
    'factory msg x "sudo" "y"', "factory task note T-1 - <<'X'\nuse sudo here\nX", "git log --format=%s",
    "ls /usr/bin/sudo", "which sudo", "command -v sudo", "echo 'docker ps'", 'echo "docker"',
    "ls tests/test_sudo_block.py", "echo sum sudoku", "git status -su", "ps -o user,su", "timeout 5 grep sudo f",
    "xargs echo sudo", "env FOO=1 grep sudo f", "find . -name sudo", "find . -name x -exec grep sudo {} \\;",
    "nice -n 5 make", "cat <<'E'\nsudo docker\nE", "gh pr create --body \"$(cat <<'EOF'\nuse sudo\nEOF\n)\"",
    "watch -n 5 ls", "echo $(date) sudo", 'python3 -c "print(1)"', "type sudo", "man sudo",
    "bash -c 'echo sudo'", "grep -n 'su ' README.md", "sudoku", "ls | grep docker", "git grep -n pkexec",
]
# worker mode blocks these (herdr / factory units / broad reads / identity); HQ mode runs only PRIV
HQ_ONLY_ALLOWED = [
    "herdr pane list", "herdr server stop", "factory spawn coder --task T-1", "factory res", "factory task list",
    "systemctl --user status factory-tick.timer", "$EDITOR file",
    "$X y", "setsid -f sleep 1", "FACTORY_AGENT=director factory agents", "unset FACTORY_ROLE",
    "python3 -W ignore bin/factory tick", "bash -c 'echo `date`'", "echo `git log -1`",
]


_HOME_PATCH = None


def setUpModule():
    """Root cause T-0636: the hook reads public_text_repos from $FACTORY_HOME's DB. A pane exports the live home, whose
    config turns the public-text rule on, so plain `git -c alias.*` / quoted-backtick commands block; CI has no DB. Run
    against an empty temp home so the result does not depend on the caller's environment."""
    global _HOME_PATCH
    tmp = tempfile.mkdtemp(prefix="zz-test-factory-guardhome-")
    _HOME_PATCH = mock.patch.dict(os.environ, {"FACTORY_HOME": tmp})
    _HOME_PATCH.start()
    unittest.addModuleCleanup(shutil.rmtree, tmp, True)
    unittest.addModuleCleanup(_HOME_PATCH.stop)


def run_hook(cmd, mode=None, role=None, env=None):
    argv = [sys.executable, HOOK] + (MODES[mode] if mode else []) + (["--role", role] if role else [])
    payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}})
    return subprocess.run(argv, input=payload, capture_output=True, text=True, timeout=30, env=env)


class Base(unittest.TestCase):
    def blocked(self, cmd, word=None, **kw):
        p = run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 2, f"should block {kw}: {cmd!r}\n{p.stderr}")
        return p

    def allowed(self, cmd, **kw):
        p = run_hook(cmd, **kw)
        self.assertEqual(p.returncode, 0, f"should allow {kw}: {cmd!r}\n{p.stderr}")


class PrivTests(Base):
    def test_priv_blocked_in_both_modes(self):
        self.assertGreaterEqual(len(BLOCKED), 20)
        for mode in MODES:
            for cmd in BLOCKED:
                with self.subTest(mode=mode, cmd=cmd):
                    p = self.blocked(cmd, mode=mode)
                    self.assertIn("privilege escalation", p.stderr)
                    self.assertIn("owner M803", p.stderr)

    def test_reason_names_the_word(self):
        for cmd, word in (("sudo -n true", "sudo"), ("/usr/bin/doas ls", "doas"), ("env A=1 pkexec x", "pkexec"),
                          ("bash -c 'su -'", "su"), ("timeout 5 docker ps", "docker"),
                          ("x=1 sh -c \"sh -c 'sudo id'\"", "sudo")):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.assertIn(f"(`{word}`)", self.blocked(cmd, mode=mode).stderr)

    def test_no_false_positives_in_both_modes(self):
        self.assertGreaterEqual(len(ALLOWED), 25)
        for mode in MODES:
            for cmd in ALLOWED:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)

    def test_every_worker_role_blocked(self):
        for role in WORKER_ROLES:
            with self.subTest(role=role):
                self.blocked("sudo -n true", role=role)
                self.blocked("env A=1 docker ps", role=role)

    def test_hq_mode_runs_only_priv(self):
        for cmd in HQ_ONLY_ALLOWED:
            with self.subTest(cmd=cmd):
                self.allowed(cmd, mode="hq")

    def test_worker_mode_still_blocks_herdr_rules(self):
        for cmd in ("herdr pane list", "factory spawn coder --task T-1", "factory res",
                    "systemctl --user restart factory-tick.timer", "$X y"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, mode="worker")

    def test_hq_unparseable_allows_worker_fails_closed(self):
        self.allowed("echo 'unbalanced", mode="hq")
        self.blocked("echo 'unbalanced", mode="worker")

    def test_hq_dynamic_command_word_not_blocked(self):
        self.allowed("$X --version", mode="hq")
        self.allowed("${EDITOR} f", mode="hq")
        self.blocked("$X --version", mode="worker")

    def test_hq_bad_json_fails_closed(self):
        p = subprocess.run([sys.executable, HOOK, "--hq"], input="{not json", capture_output=True, text=True)
        self.assertEqual(p.returncode, 2)

    def test_non_bash_tool_allowed(self):
        payload = json.dumps({"tool_name": "Read", "tool_input": {"file_path": "/x"}})
        for argv in (["--hq"], ["--role", "coder"]):
            p = subprocess.run([sys.executable, HOOK] + argv, input=payload, capture_output=True, text=True)
            self.assertEqual(p.returncode, 0)

    def test_script_on_disk_with_sudo_blocked_in_hq(self):
        with tempfile.TemporaryDirectory() as td:
            path = os.path.join(td, "x.sh")
            with open(path, "w") as f:
                f.write("#!/bin/sh\necho hi\nsudo -n true\n")
            self.blocked(f"bash {path}", mode="hq")
            self.blocked(f"source {path}", mode="hq")

    def test_qa_reproduction(self):
        for mode in MODES:
            self.blocked("sudo -n true", mode=mode)
            self.allowed("grep -rn sudo README.md", mode=mode)


class InterpreterOptionTests(Base):
    """reviewer-t-0047 N1: an interpreter option that takes a value hid the real script/-c position."""

    def test_python_w_x_values(self):
        live = LIVE_HOME + "/bin/factory"
        for cmd in (f"python3 -W ignore {live} tick", f"python3 -X dev {live} spawn coder --task T-1",
                    f"python3 -W error -X dev {live} up",
                    "python3 -W ignore -c 'import os; os.system(\"herdr pane close\")'",
                    "python3 -X dev -c 'import os; os.system(\"herdr pane close\")'"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, mode="worker")

    def test_python_w_x_values_priv(self):
        for cmd in ("python3 -W ignore -c 'import os; os.system(\"sudo id\")'",
                    "python3 -X dev -c 'import os; os.system(\"sudo id\")'"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)

    def test_python_w_x_normal_use_allowed(self):
        for cmd in ("python3 -W ignore -m unittest tests.test_x", "python3 -X dev bin/other.py",
                    "python3 -W ignore -c 'print(1)'"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)


class ReviewRound2Tests(Base):
    """reviewer-t-0057 B1-B4 and nits."""

    def test_b1_python_plain_flags_do_not_hide_factory_unit(self):
        # main blocks all of these (exit 2); a flag that is not a value option must not shift the script position
        for flag in ("-I", "-E", "-d", "-x", "-Wignore", "-B -E", "-Xdev", "-W ignore -I", "-IBs", "-OO", "-u -E"):
            for sub in ("tick", "up", "spawn coder", "res", "relaunch", "task list"):
                cmd = f"python3 {flag} bin/factory {sub}"
                with self.subTest(cmd=cmd):
                    self.blocked(cmd, mode="worker")

    def test_b1_unknown_python_option_fails_closed(self):
        for cmd in ("python3 -Z bin/factory tick", "python3 --weird bin/factory tick", "python3 -Zq bin/factory up",
                    "deno run bin/factory tick", "node --foo bin/factory tick"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, mode="worker")

    def test_b1_priv_in_code_behind_flags(self):
        code = "\"import os; os.system('sudo id')\""
        for flag in ("-I", "-E", "-d", "-x", "-Wignore", "-E -s", "-B -E", "-Xdev", "-W ignore", "-IBs", "-Ic"):
            cmd = f"python3 {flag} -c {code}" if not flag.endswith("c") else f"python3 {flag} {code}"
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)
        for cmd in ("perl -e \"system('sudo id')\"", "perl -I lib -e \"system('sudo id')\"",
                    "ruby -r json -e \"system('sudo id')\"", "node -r x -e \"require('child_process').execSync('sudo id')\"",
                    "node -p \"require('child_process').execSync('docker ps')\""):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)

    def test_b1_unknown_option_scans_code_words(self):
        for mode in MODES:
            with self.subTest(mode=mode):
                self.blocked("python3 -Zq -c \"import os; os.system('sudo id')\"", mode=mode)

    def test_b1_normal_python_allowed(self):
        for cmd in ("python3 -I -c 'print(1)'", "python3 -E bin/other.py", "python3 -Wignore -m unittest tests.test_x",
                    "python3 -x script.py", "python3 -d -c 'print(2)'", "python3 -E -s -c 'print(3)'",
                    "perl -e 'print 1'", "node -e 'console.log(1)'"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)

    def test_b2_single_quoted_substitutions_are_data(self):
        for cmd in ("git commit -m 'block `docker`'", "factory msg lead 'x' 'never run `sudo -n true`'",
                    "echo 'a $(sudo id) b'", "echo '`sudo id`'", "gh pr create --body 'use `docker ps` and $(su -)'",
                    "echo 'x' 'y $(docker ps)'", "factory msg lead s 'a `sudo` b' --kind report"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)

    def test_b2_live_substitutions_still_blocked(self):
        for cmd in ('echo "a $(sudo id) b"', 'echo "a `sudo id` b"', "echo $(sudo id)", "echo `sudo id`",
                    "echo 'x'; echo \"$(sudo id)\"", "echo 'x' \"$(docker ps)\"", "echo \"it's $(sudo id)\""):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)

    def test_b3_worker_glob_command_word(self):
        for cmd in ("/usr/bin/s?do -n true", "/usr/bin/sud[o] -n true", "s*o id", "/usr/bin/d?cker ps", "/usr/bin/su* x",
                    "env /usr/bin/s?do id", "nohup /usr/bin/sud? id", "(/usr/bin/s?do id)"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, mode="worker")

    def test_b3_glob_allowed_cases(self):
        for cmd in ("ls /usr/bin/s?do", "grep -n 's*o' f", "[ -f x ]", "[[ -f x ]]", "ls *.py", "echo /usr/bin/s?do"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)

    def test_b3_hq_does_not_block_globs(self):
        self.allowed("/usr/bin/s?do -n true", mode="hq")

    def test_b4_git_sed_awk_forms(self):
        for cmd in ("git -c alias.x='!sudo id' x", "git -c core.pager='sudo less' log", "git -c core.sshCommand='docker x' fetch",
                    "git config alias.x '!sudo id'", "git config core.editor 'sudo vi'", "git -C . -c alias.y='!docker ps' y",
                    "sed -n '1e sudo id' f", "sed -e 'e docker ps' f", "sed 's/x/sudo id/e' f", "sed '$e su -' f",
                    "awk 'BEGIN{system(\"sudo id\")}'", "awk 'BEGIN{\"docker ps\" | getline x}'",
                    "awk 'BEGIN{print 1 | \"sudo tee f\"}'", "gawk 'BEGIN{system(\"pkexec ls\")}'"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)

    def test_b4_normal_git_sed_awk_allowed(self):
        for cmd in ("git -c user.name=x commit -m 'sudo'", "git config --get user.name","git -c alias.x='status' x",
                    "git config --get core.editor","sed -n '1p' f", "sed 's/sudo/x/' f", "sed -n '/sudo/p' f",
                    "awk '/sudo|su/ {print}' f", "awk '{print $1}' f", "awk 'BEGIN{print \"sudo\"}'",
                    "sed -i 's/docker/podman/g' f", "git log --grep=sudo"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)

    def test_nits_time_function_trap(self):
        for cmd in ("time -p sudo id", "time sudo id", "function f { sudo id; }", "function f { docker ps; }; f",
                    "trap 'sudo id' EXIT", "trap -- 'docker ps' INT", "f() { sudo id; }"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.blocked(cmd, mode=mode)
        for cmd in ("time -p ls", "function f { echo sudo; }", "trap 'echo done' EXIT", "trap - EXIT"):
            for mode in MODES:
                with self.subTest(mode=mode, cmd=cmd):
                    self.allowed(cmd, mode=mode)


class BroadReadAdditionTests(Base):
    CMDS = ("factory lesson list",)

    def test_blocked_on_live_home(self):
        for cmd in self.CMDS:
            with self.subTest(cmd=cmd):
                self.assertIn("broad factory reads", self.blocked(cmd, role="coder").stderr)

    def test_temp_home_allowed(self):
        for cmd in self.CMDS:
            with self.subTest(cmd=cmd):
                self.allowed(f"FACTORY_HOME=/tmp/qa-home {cmd}", role="coder")

    def test_staff_engineer_exempt_and_other_lesson_forms_allowed(self):
        self.allowed("factory lesson list", role="staff-engineer")
        for cmd in ("factory lesson show 3", "factory lesson add x", "factory lesson edit 3"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, role="coder")

    def test_hq_unaffected(self):
        for cmd in self.CMDS:
            self.allowed(cmd, mode="hq")


class CleanerBranchDeleteTests(Base):
    def test_cleaner_allowed_for_factory_branches(self):
        for cmd in ("git branch -D factory/t-1", "git branch -d factory/t-1", "git branch --delete factory/t-1",
                    "git push origin --delete factory/t-1", "git push origin -d factory/t-1",
                    "git push origin :factory/t-1", "git branch -D factory/t-1 factory/t-2",
                    "git -C /tmp/x push origin --delete factory/t-1", "git branch -D factory/g-005/t-1"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, role="cleaner")

    def test_cleaner_refused_for_other_targets(self):
        for cmd in ("git branch -D main", "git branch -D state", "git push origin --delete main",
                    "git push origin --delete state", "git push origin :main", "git push origin --delete feature/x",
                    "git branch -D feature/x", "git branch -D factory/t-1 main", "git push origin --delete factory/t-1 main",
                    "git push origin --delete 'factory/*'", "git branch -D 'factory/*'", "git branch -D factory/*",
                    "git branch -D factory/../main", "git branch -D", "git push origin --delete",
                    "git push --delete factory/t-1", "git push origin :factory/t-1 main",
                    "git push origin HEAD", "git push origin factory/t-1", "git push --force origin factory/t-1",
                    "git push origin +:main", "git push origin -d main factory/t-1"):
            with self.subTest(cmd=cmd):
                self.blocked(cmd, role="cleaner")

    def test_cleaner_wrappers_and_chains(self):
        self.allowed("env A=1 git branch -D factory/t-1", role="cleaner")
        self.allowed("git branch -D factory/t-1 && git push origin --delete factory/t-1", role="cleaner")
        self.blocked("env A=1 git branch -D main", role="cleaner")
        self.blocked("timeout 5 git push origin --delete main", role="cleaner")
        self.blocked("git branch -D factory/t-1; git branch -D main", role="cleaner")
        self.blocked("bash -c 'git push origin :main'", role="cleaner")

    def test_other_worker_roles_refused_even_for_factory(self):
        for role in ("pm", "coder", "qa", "reviewer", "judge", "staff-engineer", None):
            for cmd in ("git branch -D factory/t-1", "git push origin --delete factory/t-1",
                        "git push origin :factory/t-1"):
                with self.subTest(role=role, cmd=cmd):
                    self.blocked(cmd, role=role)

    def test_normal_git_unaffected(self):
        for role in ("cleaner", "coder"):
            for cmd in ("git branch", "git branch -a", "git branch --list 'factory/*'", "git branch --show-current",
                        "git branch -vv", "git status", "git log --oneline", "git branch --sort=-committerdate"):
                with self.subTest(role=role, cmd=cmd):
                    self.allowed(cmd, role=role)
        for cmd in ("git push -u origin HEAD", "git push origin factory/t-1", "git push"):
            with self.subTest(cmd=cmd):
                self.allowed(cmd, role="judge")
        # T-0743: the coder push pin resolves the own worktree (here the hook cwd) from tasks.worktree in the DB
        with tempfile.TemporaryDirectory() as home:
            os.makedirs(os.path.join(home, "db"))
            con = sqlite3.connect(os.path.join(home, "db", "factory.db"))
            con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
            con.execute("INSERT INTO tasks VALUES ('T-1', ?)", (os.getcwd(),))
            con.commit()
            con.close()
            self.allowed("git push origin HEAD:factory/t-1", role="coder",
                         env={**os.environ, "FACTORY_AGENT": "coder-t-1", "FACTORY_HOME": home})

    def test_hq_unaffected(self):
        self.allowed("git branch -D main", mode="hq")


class SettingsTests(unittest.TestCase):
    def load(self, role):
        with open(os.path.join(str(rendered_settings_dir()), role + ".json")) as f:
            return json.load(f)

    def hook_commands(self, d):
        return [h["command"] for e in d.get("hooks", {}).get("PreToolUse", []) if e.get("matcher") == "Bash"
                for h in e["hooks"]]

    def test_all_role_files(self):
        self.assertEqual(sorted(os.path.basename(p)[:-5] for p in SETTINGS), sorted(HQ_ROLES + WORKER_ROLES))

    def test_all_roles_deny_priv_commands(self):
        for path in SETTINGS:
            with open(path) as f:
                deny = json.load(f)["permissions"]["deny"]
            for entry in DENY:
                with self.subTest(file=os.path.basename(path), entry=entry):
                    self.assertIn(entry, deny)

    def test_hq_roles_run_hq_mode(self):
        for role in HQ_ROLES:
            with self.subTest(role=role):
                cmds = self.hook_commands(self.load(role))
                self.assertEqual(len(cmds), 1)
                self.assertTrue(cmds[0].endswith("roles/hooks/worker-guard.py --hq"), cmds[0])
                self.assertNotIn("--role", cmds[0])

    def test_worker_roles_keep_worker_mode(self):
        for role in WORKER_ROLES:
            with self.subTest(role=role):
                cmds = self.hook_commands(self.load(role))
                self.assertEqual(len(cmds), 1)
                self.assertTrue(cmds[0].endswith(f"worker-guard.py --role {role}"), cmds[0])
                self.assertNotIn("--hq", cmds[0])

    def test_cleaner_branch_delete_scope(self):
        d = self.load("cleaner")["permissions"]
        for entry in ("Bash(git branch -D factory/*)", "Bash(git push origin --delete factory/*)"):
            self.assertIn(entry, d["allow"])
        for entry in ("Bash(git branch -D:*)", "Bash(git push origin --delete:*)", "Bash(git push:*)"):
            self.assertNotIn(entry, d["deny"])
        for entry in ("Bash(git push origin main:*)", "Bash(git push --force:*)", "Bash(git push -f:*)",
                      "Bash(git push origin HEAD:main:*)", "Bash(git commit:*)"):
            self.assertIn(entry, d["deny"])

    def test_other_roles_unchanged_on_branch_delete(self):
        for role in HQ_ROLES + tuple(r for r in WORKER_ROLES if r != "cleaner"):
            d = self.load(role)["permissions"]
            self.assertFalse([a for a in d["allow"] if "git branch" in a or "--delete" in a], role)
            if role not in ("coder",):
                self.assertIn("Bash(git push:*)", d["deny"], role)

    def test_hook_paths_exist(self):
        for path in SETTINGS:
            with open(path) as f:
                d = json.load(f)
            for cmd in self.hook_commands(d):
                self.assertTrue(cmd.startswith("python3 " + LIVE_HOME + "/roles/hooks/worker-guard.py"), cmd)


if __name__ == "__main__":
    unittest.main()
