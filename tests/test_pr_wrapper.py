import json
import subprocess
import sys
import unittest

from harness import FACTORY, FactoryTestCase, git, rendered_settings

REPO_SLUG = "zz-test/repo"


class PrWrapperTests(FactoryTestCase):
    """`factory pr create|ready` (T-0687): repo from the task's project, branch must be the own task branch."""

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.sql("UPDATE projects SET gh_repo=? WHERE id='zz-test-proj'", (REPO_SLUG,))
        e.seed_goal(project="zz-test-proj")
        e.seed_task(status="in_progress", pr_url=None)
        e.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        self.wt = e.home / "wt"
        git("init", "-q", "-b", "factory/t-0001", str(self.wt), cwd=e.home)

    def pr(self, *args, agent="zz-test-coder", stdin=None, extra=None, cwd=None):
        return subprocess.run([sys.executable, str(FACTORY), "pr", *args], input=stdin, capture_output=True, text=True,
                              env=self.env.env(agent, extra), cwd=cwd or self.wt, timeout=60)

    def gh_calls(self):
        return self.env.calls("gh")

    def test_create_uses_fixed_repo_branch_and_stdin_body(self):
        p = self.pr("create", "--title", "T-0001 thing", "--body", "-", stdin="the body\n")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("/pull/7", p.stdout)
        (c,) = self.gh_calls()
        self.assertEqual(c["argv"], ["pr", "create", "--repo", REPO_SLUG, "--base", "main", "--head", "factory/t-0001",
                                     "--title", "T-0001 thing", "--body-file", "-"])
        self.assertEqual(c["stdin"], "the body")

    def test_ready_targets_own_branch_only(self):
        p = self.pr("ready")
        self.assertEqual(p.returncode, 0, p.stderr)
        (c,) = self.gh_calls()
        self.assertEqual(c["argv"], ["pr", "ready", "factory/t-0001", "--repo", REPO_SLUG])

    def test_no_passthrough_args(self):
        for args in (["ready", "https://github.com/evil/repo/pull/1"], ["ready", "--repo", "evil/repo"],
                     ["create", "--title", "t", "--body", "-", "--repo", "evil/repo"], ["ready", "--title", "x"],
                     ["create", "--title", "t", "--body", "inline text"]):
            p = self.pr(*args, stdin="b")
            self.assertNotEqual(p.returncode, 0, args)
        self.assertEqual(self.gh_calls(), [])

    def test_env_overrides_dropped_and_repo_not_taken_from_env(self):
        p = self.pr("ready", extra={"GH_REPO": "evil/repo", "GH_HOST": "evil.example", "FACTORY_TASK": "T-9999"})
        self.assertEqual(p.returncode, 0, p.stderr)
        (c,) = self.gh_calls()
        self.assertEqual(c["env"], {"GH_REPO": None, "GH_HOST": None})
        self.assertIn(REPO_SLUG, c["argv"])
        self.assertNotIn("evil/repo", c["argv"])

    def test_non_coder_refused(self):
        for agent in ("zz-test-qa", "zz-test-nobody"):
            p = self.pr("ready", agent=agent)
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("coder", p.stderr)
        self.assertEqual(self.gh_calls(), [])

    def test_wrong_branch_refused(self):
        git("checkout", "-q", "-b", "factory/t-0002", cwd=self.wt)
        p = self.pr("ready")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("factory/t-0001", p.stderr)
        git("checkout", "-q", "-b", "main2", cwd=self.wt)
        self.assertEqual(self.pr("create", "--title", "t", "--body", "-", stdin="b").returncode, 3)
        self.assertEqual(self.gh_calls(), [])

    def test_project_without_gh_repo_refused(self):
        self.env.sql("UPDATE projects SET gh_repo=NULL")
        p = self.pr("ready")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("gh_repo", p.stderr)
        self.assertEqual(self.gh_calls(), [])


class CoderSettingsTests(unittest.TestCase):
    def test_coder_settings_allow_wrapper_and_deny_raw_gh(self):
        perm = json.loads(rendered_settings("coder").read_text())["permissions"]
        for rule in ("Bash(factory pr create *)", "Bash(factory pr ready)"):
            self.assertIn(rule, perm["allow"])
        for rule in ("Bash(gh pr create*)", "Bash(gh pr ready*)"):
            self.assertIn(rule, perm["deny"])
            self.assertNotIn(rule, perm["allow"])


if __name__ == "__main__":
    unittest.main()
