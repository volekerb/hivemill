"""Tests for ci/affected.py: affected-test selection from a git diff, in temp git repos."""
import os
import re
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
import _gitenv  # noqa: F401  (hermetic git: tests/_gitenv.py)

ROOT = Path(__file__).resolve().parent.parent
AFFECTED = ROOT / "ci" / "affected.py"

BIN_BASE = '''SCHEMA = "a"
DEFAULT_CONFIG = {"k": "v"}

def db():
    return 1

def helper(x):
    return x

def cmd_judge(a):
    return helper(a)

def cmd_inbox(a):
    return 2

def hub(x):
    return x

def use_hub():
    return [%s]

def main():
    return 0
''' % ", ".join("hub(%d)" % i for i in range(16))

SMOKE = ["test_db_layout.py", "test_inbox.py", "test_judge.py"]


class AffectedTest(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.repo = Path(self.tmp.name) / "repo"
        self.home = Path(self.tmp.name) / "home"  # no DB here: the smoke default applies
        self.home.mkdir()
        (self.repo / "tests" / "stubs").mkdir(parents=True)
        (self.repo / "ci").mkdir()
        (self.repo / "roles").mkdir()
        (self.repo / "docs").mkdir()
        (self.repo / "bin").mkdir()
        self.git("init", "-q", "-b", "main")
        self.git("config", "user.email", "t@example.com")
        self.git("config", "user.name", "t")
        for name in SMOKE + ["test_alpha.py", "test_beta.py", "test_gamma.py", "test_shard.py", "test_roles.py"]:
            self.write("tests/" + name, "# %s\n" % name)
        for i in range(30):
            self.write("tests/test_filler%02d.py" % i, "# filler\n")
        self.write("tests/test_alpha.py", "# uses helper and ci/tool and bin\n# helper\n# ci/tool\n")
        self.write("tests/test_beta.py", "# factory inbox call\n")
        self.write("tests/test_gamma.py", "# factory judge call\n")
        self.write("tests/test_roles.py", "# roles/coder.md\n")
        self.write("tests/harness.py", "# h\n")
        self.write("tests/stubs/herdr", "# s\n")
        self.write("ci/tool.py", "x = 1\n")
        self.write("ci/shard.py", "y = 1\n")
        self.write("roles/coder.md", "a\n")
        self.write("docs/a.md", "a\n")
        self.write("bin/factory", BIN_BASE)
        self.git("add", "-A")
        self.git("commit", "-qm", "base")
        self.git("branch", "base")
        self.git("checkout", "-qb", "work")

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.repo)] + list(args), check=True, capture_output=True)

    def write(self, rel, text):
        p = self.repo / rel
        p.parent.mkdir(parents=True, exist_ok=True)
        p.write_text(text)

    def commit(self):
        self.git("add", "-A")
        self.git("commit", "-qm", "change")

    def affected(self, *extra):
        env = dict(os.environ, FACTORY_HOME=str(self.home))
        r = subprocess.run([sys.executable, str(AFFECTED), "--root", str(self.repo), "--base", "base"] + list(extra),
                           capture_output=True, text=True, env=env)
        return r

    def selected(self):
        r = self.affected("--list")
        self.assertEqual(r.returncode, 0, r.stderr)
        return r.stdout.split()

    def test_test_file_only_change(self):
        self.write("tests/test_gamma.py", "# edited\n")
        self.commit()
        self.assertEqual(self.selected(), sorted(SMOKE + ["test_gamma.py"]))

    def test_ci_change_selects_mentioning_modules_and_own_test(self):
        self.write("ci/shard.py", "y = 2\n")
        self.commit()
        got = self.selected()
        self.assertIn("test_shard.py", got)
        self.assertNotIn("test_alpha.py", got)
        self.write("ci/tool.py", "x = 2\n")
        self.commit()
        got = self.selected()
        self.assertIn("test_alpha.py", got)  # mentions ci/tool

    def test_bin_factory_change_inside_one_cmd_selects_its_tests_only(self):
        self.write("bin/factory", BIN_BASE.replace("def cmd_inbox(a):\n    return 2", "def cmd_inbox(a):\n    return 3"))
        self.commit()
        got = self.selected()
        self.assertIn("test_beta.py", got)  # "inbox" subcommand
        self.assertNotIn("test_gamma.py", got)
        self.assertNotIn("test_alpha.py", got)

    def test_bin_factory_helper_change_selects_by_def_name(self):
        self.write("bin/factory", BIN_BASE.replace("def helper(x):\n    return x", "def helper(x):\n    return x + 1"))
        self.commit()
        got = self.selected()
        self.assertIn("test_alpha.py", got)
        self.assertNotIn("test_beta.py", got)

    def test_schema_change_is_full(self):
        self.write("bin/factory", BIN_BASE.replace('SCHEMA = "a"', 'SCHEMA = "b"'))
        self.commit()
        r = self.affected()
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertTrue(r.stdout.startswith("FULL"), r.stdout)
        self.assertIn("SCHEMA", r.stdout)

    def test_default_config_main_and_db_are_full(self):
        for old, new in [('{"k": "v"}', '{"k": "w"}'), ("def main():\n    return 0", "def main():\n    return 1"),
                         ("def db():\n    return 1", "def db():\n    return 2")]:
            self.git("checkout", "-q", "work")
            self.git("reset", "-q", "--hard", "base")
            self.write("bin/factory", BIN_BASE.replace(old, new))
            self.commit()
            self.assertTrue(self.affected().stdout.startswith("FULL"), old)

    def test_hub_def_change_is_full(self):
        self.write("bin/factory", BIN_BASE.replace("def hub(x):\n    return x", "def hub(x):\n    return -x"))
        self.commit()
        r = self.affected()
        self.assertTrue(r.stdout.startswith("FULL"), r.stdout)
        self.assertIn("16 call sites", r.stdout)

    def test_harness_and_stub_changes_are_full(self):
        self.write("tests/harness.py", "# changed\n")
        self.commit()
        self.assertTrue(self.affected().stdout.startswith("FULL"))
        self.git("reset", "-q", "--hard", "base")
        self.write("tests/stubs/herdr", "# changed\n")
        self.commit()
        self.assertTrue(self.affected().stdout.startswith("FULL"))

    def test_docs_only_is_the_smoke_set(self):
        self.write("docs/a.md", "changed\n")
        self.write("README.md", "new\n")
        self.commit()
        self.assertEqual(self.selected(), sorted(SMOKE))

    def test_roles_change_selects_mentioning_modules(self):
        self.write("roles/coder.md", "b\n")
        self.commit()
        self.assertIn("test_roles.py", self.selected())

    def test_smoke_set_comes_from_config(self):
        (self.home / "db").mkdir()
        import sqlite3
        con = sqlite3.connect(str(self.home / "db" / "factory.db"))
        con.execute("CREATE TABLE config (key TEXT PRIMARY KEY, value TEXT)")
        con.execute("INSERT INTO config VALUES ('affected_smoke', 'test_beta.py')")
        con.commit()
        con.close()
        self.write("docs/a.md", "changed\n")
        self.commit()
        self.assertEqual(self.selected(), ["test_beta.py"])

    def test_over_40_percent_is_full(self):
        for i in range(30):
            self.write("tests/test_filler%02d.py" % i, "# edited\n")
        self.commit()
        r = self.affected()
        self.assertTrue(r.stdout.startswith("FULL"), r.stdout)
        self.assertIn("rule 5", r.stdout)

    def test_deleted_test_module_not_selected(self):
        self.git("rm", "-q", "tests/test_gamma.py")
        self.commit()
        got = self.selected()
        self.assertNotIn("test_gamma.py", got)
        self.assertEqual(got, sorted(SMOKE))

    def test_renamed_test_module_selects_new_name(self):
        self.git("mv", "tests/test_gamma.py", "tests/test_delta.py")
        self.commit()
        got = self.selected()
        self.assertIn("test_delta.py", got)
        self.assertNotIn("test_gamma.py", got)

    def test_unknown_path_nobody_mentions_is_full(self):
        self.write("web/app.py", "x\n")
        self.commit()
        r = self.affected()
        self.assertTrue(r.stdout.startswith("FULL"), r.stdout)
        self.assertIn("web/app.py", r.stdout)

    def test_unknown_path_selects_modules_that_mention_it(self):
        self.write("tests/test_beta.py", "# imports web.app and checks web/app.py\n")
        self.git("add", "-A")
        self.git("commit", "-qm", "mention")
        self.git("branch", "-f", "base")
        self.write("web/app.py", "x\n")
        self.commit()
        self.assertEqual(self.selected(), sorted(SMOKE + ["test_beta.py"]))

    def test_view_change_selects_modules_that_import_it(self):
        # T-0665: `from web.views import home` mentions neither the path web/views/home.py nor the dotted web.views.home
        for name, body in (("test_util.py", "from web.views import home  # noqa\n"),
                           ("test_multi.py", "from web.views import (\n    agents,\n    home as h,\n)\n"),
                           ("test_dotted.py", "import web.views.home\n"),
                           ("test_other.py", "from web.views import homepage, agents\n")):
            self.write("tests/" + name, body)
        self.git("add", "-A")
        self.git("commit", "-qm", "importers")
        self.git("branch", "-f", "base")
        self.write("web/views/home.py", "x = 1\n")
        self.commit()
        got = self.selected()
        self.assertEqual(got, sorted(SMOKE + ["test_util.py", "test_multi.py", "test_dotted.py"]))

    def test_view_change_selects_parenthesised_import_on_one_line(self):
        self.write("tests/test_util.py", "from web.views import (agents, home)\n")
        self.git("add", "-A")
        self.git("commit", "-qm", "importer")
        self.git("branch", "-f", "base")
        self.write("web/views/home.py", "x = 1\n")
        self.commit()
        self.assertIn("test_util.py", self.selected())

    def test_run_leaves_uncommitted_and_untracked_edits_in_the_caller_worktree(self):
        # T-0665: a dirty worktree must survive an affected --run byte for byte
        self.write("tests/test_beta.py",
                   "import unittest\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertTrue(True)\n")
        self.commit()
        self.write("ci/tool.py", "x = 2  # uncommitted\n")
        self.write("scratch.txt", "untracked\n")
        self.write("docs/a.md", "staged\n")
        self.git("add", "docs/a.md")

        def snap():
            st = subprocess.run(["git", "-C", str(self.repo), "status", "--porcelain=v1", "-uall"], capture_output=True, text=True).stdout
            st = "".join(l for l in st.splitlines(True) if "__pycache__" not in l)  # the run's own bytecode is not an edit
            return st, {f: (self.repo / f).read_text() for f in ("ci/tool.py", "scratch.txt", "docs/a.md")}
        before = snap()
        r = self.affected("--run")
        self.assertEqual(r.returncode, 0, r.stdout + r.stderr)
        self.assertIn("Ran 1 tests", r.stdout)
        self.assertEqual(snap(), before)
        self.assertEqual(before[1]["ci/tool.py"], "x = 2  # uncommitted\n")

    def test_bad_base_fails_with_hint(self):
        env = dict(os.environ, FACTORY_HOME=str(self.home))
        r = subprocess.run([sys.executable, str(AFFECTED), "--root", str(self.repo), "--base", "nope"],
                           capture_output=True, text=True, env=env)
        self.assertEqual(r.returncode, 2)
        self.assertIn("git fetch", r.stderr)

    def test_run_executes_selection_and_reports_count(self):
        self.write("tests/test_gamma.py",
                   "import unittest\nclass T(unittest.TestCase):\n    def test_ok(self):\n        self.assertTrue(True)\n")
        self.commit()
        r = self.affected("--run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("affected: Ran 1 tests in 4 modules: OK", r.stdout)

    def test_run_fails_on_failing_test(self):
        self.write("tests/test_gamma.py",
                   "import unittest\nclass T(unittest.TestCase):\n    def test_bad(self):\n        self.fail('x')\n")
        self.commit()
        r = self.affected("--run")
        self.assertEqual(r.returncode, 1)
        self.assertIn("FAILED", r.stdout)

    def test_quick_runs_affected_in_one_process_and_prints_elapsed(self):
        self.write("tests/test_gamma.py",
                   "import os\nimport unittest\nclass T(unittest.TestCase):\n    def test_pid(self):\n"
                   "        print('PID', os.getpid())\n")
        self.write("tests/test_beta.py",
                   "import os\nimport unittest\nclass T(unittest.TestCase):\n    def test_pid(self):\n"
                   "        print('PID', os.getpid())\n")
        self.commit()
        r = self.affected("--quick", "--run")
        self.assertEqual(r.returncode, 0, r.stderr)
        self.assertIn("affected: Ran 2 tests in 5 modules: OK", r.stdout)
        self.assertRegex(r.stdout, r"affected: elapsed [\d.]+ s of 120 s budget")
        pids = set(re.findall(r"PID (\d+)", r.stdout + r.stderr))
        self.assertEqual(len(pids), 1, "quick mode must stay in one process")

    def test_quick_refuses_full_and_runs_nothing(self):
        self.write("tests/harness.py", "raise SystemExit('must not be imported')\n")
        self.commit()
        for extra in (("--quick",), ("--quick", "--run"), ("--budget", "30", "--run")):
            with self.subTest(extra=extra):
                r = self.affected(*extra)
                self.assertEqual(r.returncode, 0, r.stderr)
                self.assertIn("FULL needed: rely on CI", r.stdout)
                self.assertNotIn("Ran ", r.stdout)

    def test_without_quick_full_still_runs_everything(self):
        self.write("tests/harness.py", "# edited\n")
        self.commit()
        r = self.affected("--run")
        self.assertIn("FULL (", r.stdout)

    def test_budget_stops_starting_modules_and_says_rely_on_ci(self):
        self.write("tests/test_gamma.py",
                   "import time\nimport unittest\nclass T(unittest.TestCase):\n    def test_slow(self):\n        time.sleep(1.3)\n")
        self.commit()
        r = self.affected("--budget", "1", "--run")
        self.assertEqual(r.returncode, 4, r.stdout + r.stderr)
        self.assertIn("Ran 1 tests in 2 modules: OK", r.stdout)
        self.assertIn("elapsed", r.stdout)
        self.assertIn("rely on CI", r.stderr)
        self.assertIn("test_inbox.py", r.stderr)

    def test_budget_must_be_positive(self):
        r = self.affected("--budget", "0")
        self.assertEqual(r.returncode, 2)


if __name__ == "__main__":
    unittest.main()
