"""T-0880: `uv run [opts] [--] CMD` and `uvx [opts] CMD` are wrappers: CMD is checked as if run directly (git push,
gh writes, T-0789 inline interpreter code, the coder push pin). Defense in depth only; the sandbox + worker PAT are
the boundary."""
import json
import os
import sqlite3
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)
from harness import git  # noqa: E402
from guard_impl import hook_run  # noqa: E402

GUARD_IMPL_PYTHON_ONLY = "Go mirror follow-up in G-016"

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
NO_PUSH = ("qa", "reviewer", "pm", "staff-engineer", "scout", "evaluator", "researcher")
HQ = ("boss", "director")
TASK = "t-0880"
PY_PUSH = 'import subprocess; subprocess.run(["git","push"])'

DENIED = [
    "uv run git push origin main",
    "uv run -- git push",
    "uv run --with requests python3 -c '%s'" % PY_PUSH,
    "uvx --from gh gh pr merge 1",
    "uv run --python 3.12 --directory /x git push",
    "uvx git push",
    "uv tool run gh pr create --fill",
    "uv --directory /x run git push",
    "uv run --env-file .env -p 3.12 gh pr merge 1",
    "uv run --with=x git push",
    "uv run --index-url http://x git push",
    "uv run --color never git push",
    "uv --color never run git push",
    "uvx --index-url http://x gh pr merge 1",
    "uv run --python-preference only-system git push",
    "uv run --index-url http://x python3 -c '%s'" % PY_PUSH,
    "uv run --foo bar git push",
    "uv run --foo bar /usr/bin/git push",
    "uv run --foo bar git -C /tmp push",
    "uv run --foo bar gh pr merge 1",
    "cd /x && uv run git push origin main",
    "cd /x && uvx --from gh gh pr merge 1",
    "bash -c 'uv run git push origin main'",
    "bash -c 'cd /x && uvx --from gh gh pr merge 1'",
    "bash -c \"uv run --with requests python3 -c '%s'\"" % PY_PUSH,
]
ALLOWED = [
    "uv run pytest tests/test_harness.py -q",
    "uv run python3 -m unittest x",
    "uvx ruff check .",
    "uv pip list",
    "uv run --with requests python3 script.py",
    "uv run git status",
    "uv run gh pr view 1",
    "uv sync",
    "cd /x && uv run python3 -c 'import json'",
    # T-0880 B1: real file arguments (a shebang script, a Python file with no shebang)
    "uv run python3 ci/affected.py --base origin/main --quick --run",
    "uv run pytest tests/test_worker_guard_uv_wrapper.py -q",
    "uv run python3 -m unittest tests/test_worker_guard_uv_wrapper.py",
    "uvx ruff check roles/hooks/worker-guard.py",
    "uv run mypy bin/factory",
    "uv run --with requests pytest tests/test_worker_guard_uv_wrapper.py -q",
    # T-0880 item 11: the data word git is not a push
    "uv run pytest -k git -q",
    "uv run pytest -k git tests/test_harness.py",
    "uv run --with git pytest tests/test_harness.py -q",
    "uv run pytest tests/git tests/test_harness.py",
    "uv run --color never git commit -m x",  # accepted, same as main (unknown option hides the command word)
    "python3 -m uv run git push",  # accepted, same as main (item 11): uv is not the command word
]
# exit 2 for qa/reviewer/coder: the command word gets the bare command's rules
FIRST_WORD_DENIED = ["uv run git config user.name x", "uv run herdr list", "uv run kill 1", "uv run su" + "do ls"]
# pinned as accepted false positives (item 10)
PINNED_DENIED = ["uv run python3 x.py git push", "uv run pytest -k git push"]
# coder + no-push: later-word git push after unknown options
BOTH_DENIED = ["uv run --foo bar git push", "uv run --foo bar /usr/bin/git push", "uv run --foo bar git -C /tmp push",
               "uv run --index-url http://x git push", "uv run --color never git push", "uv --color never run git push"]
DATA_WORDS = ["uv run python3 x.py herdr", "uv run python3 script.py kill 1", "uvx --from foo python3 x.py herdr"]


class UvWrapperTests(unittest.TestCase):
    def run_hook(self, cmd, role):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "WORKER_GUARD_SCRATCH")}
        payload = {"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": ROOT}
        return hook_run(json.dumps(payload), ["--role", role], env=env)

    def test_denied_for_every_no_push_role(self):
        for role in NO_PUSH:
            for cmd in DENIED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 2, p.stderr)

    def test_first_word_rules_and_pinned(self):
        for role in ("qa", "reviewer"):
            for cmd in FIRST_WORD_DENIED + ["uv run git commit -m x"] + PINNED_DENIED:
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 2)

    def test_denied_reason_names_the_rule(self):
        self.assertIn("push", self.run_hook("uv run git push origin main", "qa").stderr)
        self.assertIn("push", self.run_hook("uv run -- git push", "reviewer").stderr)
        self.assertIn("gh write", self.run_hook("uv run --with requests python3 -c '%s'" % 'import os; os.system("gh pr merge 1")', "qa").stderr)

    def test_allowed_for_no_push_roles(self):
        for role in NO_PUSH:
            for cmd in ALLOWED:
                with self.subTest(role=role, cmd=cmd):
                    p = self.run_hook(cmd, role)
                    self.assertEqual(p.returncode, 0, p.stderr)

    def test_real_files_exist(self):
        for rel in ("ci/affected.py", "tests/test_worker_guard_uv_wrapper.py", "roles/hooks/worker-guard.py", "bin/factory"):
            self.assertTrue(os.path.exists(os.path.join(ROOT, rel)), rel)

    def test_data_words_match_bare_command(self):
        """T-0880 B2: a later uv suffix never meets the kill/herdr rules; same exit as the bare command."""
        for role in NO_PUSH:
            for cmd in DATA_WORDS:
                bare = cmd.replace("uvx --from foo ", "").replace("uv run ", "")
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, self.run_hook(bare, role).returncode)

    def test_cleaner_judge_unchanged(self):
        for role in ("cleaner", "judge"):
            for cmd in ("uv run pytest -q", "uvx ruff check .", "uv pip list", "uv run git status") + tuple(DATA_WORDS):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)
        # the cleaner's uv wrapper is not tightened: same exit as on origin/main (0)
        self.assertEqual(self.run_hook("uv run git push origin main", "cleaner").returncode, 0)

    def test_hq_unchanged(self):
        for role in HQ:
            for cmd in ("uv run git push origin main", "uvx --from gh gh pr merge 1", "uv run -- git push",
                        "uv run pytest -q", "uv pip list", "uv run git push origin --delete factory/t-1") + tuple(DATA_WORDS):
                with self.subTest(role=role, cmd=cmd):
                    self.assertEqual(self.run_hook(cmd, role).returncode, 0)


class CoderUvPushTests(unittest.TestCase):
    def setUp(self):
        td = tempfile.TemporaryDirectory()
        self.addCleanup(td.cleanup)
        self.tmp = os.path.realpath(td.name)
        self.home = os.path.join(self.tmp, "factory")
        self.wt = os.path.join(self.home, "worktrees", "G-010-T-0880")
        os.makedirs(os.path.join(self.home, ".git"))
        os.makedirs(os.path.join(self.home, "roles"))
        os.makedirs(os.path.join(self.home, "db"))
        con = sqlite3.connect(os.path.join(self.home, "db", "factory.db"))
        con.execute("CREATE TABLE tasks (id TEXT PRIMARY KEY, worktree TEXT)")
        con.execute("INSERT INTO tasks VALUES (?, ?)", (TASK.upper(), self.wt))
        con.commit()
        con.close()
        origin = os.path.join(self.tmp, "origin.git")
        ident = ("-c", "user.email=t@t", "-c", "user.name=t")
        git(*ident, "init", "-q", "--bare", origin, cwd=self.tmp)
        git(*ident, "init", "-q", "-b", "main", self.wt, cwd=self.tmp)
        git(*ident, "commit", "-q", "--allow-empty", "-m", "i", cwd=self.wt)
        git(*ident, "remote", "add", "origin", origin, cwd=self.wt)

    def run_hook(self, cmd):
        env = {k: v for k, v in os.environ.items() if k not in ("WORKER_GUARD_LIVE_HOME", "FACTORY_AGENT")}
        env.update(HOME=self.tmp, WORKER_GUARD_LIVE_HOME=self.home, FACTORY_HOME=self.home,
                   FACTORY_AGENT=f"coder-{TASK}")
        payload = json.dumps({"tool_name": "Bash", "tool_input": {"command": cmd}, "cwd": self.wt})
        return hook_run(payload, ["--role", "coder"], env=env)

    def test_denied(self):
        for cmd in ("uv run git push origin main", "uv run -- git push", "uv run git push origin HEAD:factory/t-0001",
                    "uvx git push origin main", "uv run git push --force origin HEAD:factory/%s" % TASK):
            with self.subTest(cmd=cmd):
                p = self.run_hook(cmd)
                self.assertEqual(p.returncode, 2, p.stderr)

    def test_item11_forms(self):
        for cmd in BOTH_DENIED + FIRST_WORD_DENIED + PINNED_DENIED:
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 2)
        for cmd in ("uv run pytest -k git -q", "uv run pytest -k git tests/test_harness.py", "uv run --with git pytest tests/test_harness.py -q",
                    "uv run pytest tests/git tests/test_harness.py", "uv run --color never git commit -m x",
                    "python3 -m uv run git push"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 0)

    def test_own_push_allowed(self):
        p = self.run_hook("uv run git push origin HEAD:factory/%s" % TASK)
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_own_push_allowed_with_separator(self):
        self.assertEqual(self.run_hook("uv run -- git push origin HEAD:factory/%s" % TASK).returncode, 0)

    def test_data_words_match_bare_command(self):
        for cmd in DATA_WORDS:
            bare = cmd.replace("uvx --from foo ", "").replace("uv run ", "")
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, self.run_hook(bare).returncode)

    def test_real_file_arguments_allowed(self):
        for cmd in ("uv run python3 %s/ci/affected.py --help" % ROOT, "uv run pytest %s/tests/test_worker_guard_uv_wrapper.py -q" % ROOT,
                    "uvx ruff check %s/roles/hooks/worker-guard.py" % ROOT, "uv run mypy %s/bin/factory" % ROOT):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 0)

    def test_bare_uv_wrapped_pytest_refused(self):
        # T-0435 x T-0880: the uv wrapper is unwrapped, so a bare pytest (full suite) stays refused (exit 2)
        for cmd in ("uv run pytest -q", "uv run --with git pytest -q", "uvx pytest", "uv tool run pytest"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 2)

    def test_other_uv_commands_allowed(self):
        for cmd in ("uv run pytest tests/test_harness.py -q", "uvx ruff check .", "uv pip list", "uv run python3 -m unittest x"):
            with self.subTest(cmd=cmd):
                self.assertEqual(self.run_hook(cmd).returncode, 0)


if __name__ == "__main__":
    unittest.main()
