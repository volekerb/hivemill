"""FactoryEnv.run_inproc (T-0520): the in-process CLI call must look like the subprocess one and leave no state behind."""
import os
import sys

from harness import FactoryTestCase


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
