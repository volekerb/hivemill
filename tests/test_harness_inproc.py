"""FactoryEnv.run_inproc (T-0520): the in-process CLI call must look like the subprocess one and leave no state behind."""
import os
import sys

from harness import FACTORY, FactoryTestCase


class RunInprocTests(FactoryTestCase):
    def both(self, *args, **kw):
        return self.env.run(*args, inproc=False, **kw), self.env.run(*args, inproc=True, **kw)

    def test_output_and_exit_code_match_subprocess(self):
        self.env.seed_goal()
        for args in (("goal", "show", "G-001"), ("goal", "show", "G-999"), ("config", "digest_hour")):
            sub, inp = self.both(*args)
            self.assertEqual((inp.returncode, inp.stdout, inp.stderr), (sub.returncode, sub.stdout, sub.stderr), args)

    def test_stdin_agent_and_writes_reach_the_db(self):
        self.env.seed_goal()
        p = self.env.run("plan", "G-001", "-", stdin="plan body\n", agent="owner", inproc=True)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.env.sql("SELECT body FROM plans WHERE goal_id='G-001'")[0]["body"].strip(), "plan body")

    def test_environment_cwd_argv_and_stdin_are_restored(self):
        env_before, cwd, argv, stdin = dict(os.environ), os.getcwd(), list(sys.argv), sys.stdin
        self.env.run("whoami", agent="zz-x", extra_env={"TZ": "Etc/GMT+2", "FACTORY_ZZ": "1"}, inproc=True)
        self.assertEqual((dict(os.environ), os.getcwd(), sys.argv, sys.stdin), (env_before, cwd, argv, stdin))

    def test_inproc_flag_follows_the_class_default_and_can_be_overridden(self):
        self.assertFalse(self.env.inproc)  # FactoryTestCase.INPROC is False
        self.env.inproc = True
        p = self.env.run("whoami", agent="zz-x", inproc=False)  # a per-call False wins over the env default
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_refusal_is_a_nonzero_exit_with_a_reason(self):
        p = self.env.run("goal", "show", inproc=True)  # missing id: the CLI refuses on its own terms
        self.assertNotEqual(p.returncode, 0)
        self.assertTrue(p.stderr.strip(), "a refusal must say why")


class InprocClassDefaultTests(FactoryTestCase):
    INPROC = True

    def test_class_attribute_routes_run_in_process(self):
        self.assertTrue(self.env.inproc)


class FactoryImplTests(FactoryTestCase):
    """FACTORY_IMPL=python|go|diff (G-041 S1-d): the parity runner must compare something, and must say what differed."""

    def stub_go(self, tail="", head=""):
        """A fake Go binary: runs the real Python CLI, with `head` run before and `tail` after it (shell)."""
        path = self.env.home / "stub-go"
        path.write_text(f'#!/bin/sh\n{head}\n{sys.executable} "{FACTORY}" "$@"\nrc=$?\n{tail}\nexit $rc\n')
        path.chmod(0o755)
        return str(path)

    def with_impl(self, mode, go_bin=None):
        from unittest import mock
        import harness
        env = {"FACTORY_IMPL": mode}
        if go_bin:
            env["FACTORY_GO_BIN"] = go_bin
        p = mock.patch.dict(os.environ, env)
        p.start()
        self.addCleanup(p.stop)
        self.addCleanup(harness._cli_go.clear)  # each test stages its own stub
        harness._cli_go.clear()

    def test_diff_passes_when_both_sides_agree_and_returns_python_result(self):
        self.with_impl("diff", self.stub_go())
        p = self.env.run("whoami", agent="zz-x")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("zz-x", p.stdout)

    def test_diff_catches_an_extra_space_and_names_the_argv(self):
        self.with_impl("diff", self.stub_go(tail="printf ' '"))
        with self.assertRaises(AssertionError) as cm:
            self.env.run("whoami", agent="zz-x")
        self.assertIn("factory whoami", str(cm.exception))
        self.assertIn("stdout", str(cm.exception))

    def test_diff_catches_an_extra_db_row_and_names_the_table(self):
        sql = f"INSERT INTO config(key,value) VALUES('zz_extra','1')"
        tail = f'{sys.executable} -c "import sqlite3,os;c=sqlite3.connect(os.environ[\'FACTORY_HOME\']+\'/db/factory.db\');c.execute(\\"{sql}\\");c.commit()"'
        self.with_impl("diff", self.stub_go(tail=tail))
        with self.assertRaises(AssertionError) as cm:
            self.env.run("whoami", agent="zz-x")
        self.assertIn("DB table config", str(cm.exception))

    def test_diff_catches_a_different_exit_code(self):
        self.with_impl("diff", self.stub_go(tail="rc=3"))
        with self.assertRaises(AssertionError) as cm:
            self.env.run("whoami", agent="zz-x")
        self.assertIn("exit code", str(cm.exception))

    def test_go_mode_returns_the_go_result_and_forces_inproc_off(self):
        self.with_impl("go", self.stub_go(tail="echo GO-SIDE"))
        p = self.env.run("whoami", agent="zz-x", inproc=True)
        self.assertIn("GO-SIDE", p.stdout)

    def test_unknown_impl_raises_naming_the_variable(self):
        self.with_impl("bogus")
        with self.assertRaises(ValueError) as cm:
            self.env.run("whoami")
        self.assertIn("FACTORY_IMPL", str(cm.exception))

    def test_unset_impl_is_python(self):
        from unittest import mock
        with mock.patch.dict(os.environ):
            os.environ.pop("FACTORY_IMPL", None)
            p = self.env.run("whoami", agent="zz-x")
        self.assertEqual(p.returncode, 0, p.stderr)

    def test_oracles_file_lists_ten_existing_modules(self):
        from harness import REPO
        lines = (REPO / "go" / "cmd" / "factory" / "testdata" / "oracles.txt").read_text().splitlines()
        mods = [l.strip() for l in lines if l.strip() and not l.lstrip().startswith("#")]
        self.assertEqual(len(mods), 10)
        for m in mods:
            self.assertTrue(m.startswith("tests.test_"), m)
            self.assertTrue((REPO / (m.replace(".", "/") + ".py")).is_file(), m)
