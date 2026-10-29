"""factory task tier: stored risk tier, escalate-only, rules from the main checkout (G-022 #1)."""
import json
import shutil
import unittest

from harness import REPO, FactoryTestCase, git


class TaskTierTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-lead")
        for n, r in (("zz-lead", "lead"), ("zz-coder", "coder"), ("zz-rev", "reviewer")):
            e.seed_agent(n, r, goal="G-001", team="g-001-a")
        e.seed_task("T-0001", status="in_review")
        self.home = e.home_dir
        (self.home / "ci").mkdir(exist_ok=True)  # the main checkout carries the classifier and its rules
        for f in ("risk_tier.py", "tier_rules.json", "affected.py"):
            shutil.copy(REPO / "ci" / f, self.home / "ci" / f)

    def tier(self):
        return self.env.sql("SELECT tier FROM tasks WHERE id='T-0001'")[0][0]

    def test_show_prints_tier(self):
        self.ok("task", "tier", "T-0001", "up", "T2", agent="zz-lead")
        self.assertIn("tier: T2", self.ok("task", "show", "T-0001", agent="zz-coder").stdout)

    def test_unset_tier_reads_as_dash(self):
        self.assertEqual(self.ok("task", "tier", "T-0001", agent="zz-coder").stdout.strip(), "-")

    def test_up_raises_and_never_lowers(self):
        self.ok("task", "tier", "T-0001", "T1", agent="zz-rev")
        self.ok("task", "tier", "T-0001", "up", "T3", agent="zz-lead")
        self.assertEqual(self.tier(), "T3")
        p = self.refused(2, "task", "tier", "T-0001", "T1", agent="zz-lead")
        self.assertIn("never lowered", p.stderr)
        self.assertEqual(self.tier(), "T3")

    def test_bad_tier_refused(self):
        self.refused(2, "task", "tier", "T-0001", "up", "T9", agent="zz-lead")
        self.assertIsNone(self.tier())

    def test_coder_cannot_raise(self):
        self.refused(3, "task", "tier", "T-0001", "up", "T3", agent="zz-coder")
        self.assertIsNone(self.tier())

    def test_classify_refused_while_flag_off(self):
        self.refused(2, "task", "tier", "T-0001", "--classify", "--head", "main", agent="zz-lead")

    def _home_repo(self, rules=None):
        """The main checkout (HOME): ci/ copies, a commit on main, and a PR branch that edits the tier rules."""
        (self.home / "ci" / "guard_x.py").write_text("x = 1\n")
        (self.home / "web.py").write_text("x = 1\n")
        git("add", "ci", "web.py", cwd=self.home)
        git("commit", "-q", "-m", "base", cwd=self.home)
        git("checkout", "-q", "-b", "pr", cwd=self.home)
        r = json.loads((self.home / "ci" / "tier_rules.json").read_text())
        r["sensitive_paths"] = []
        r["large_lines"] = 10 ** 6
        (self.home / "ci" / "tier_rules.json").write_text(json.dumps(r))
        (self.home / "ci" / "guard_x.py").write_text("x = 2\n")
        git("commit", "-q", "-m", "pr edits rules and a guard", "ci", cwd=self.home)
        git("checkout", "-q", "main", cwd=self.home)

    def test_rules_come_from_main_checkout_not_the_pr(self):
        self._home_repo()
        self.env.sql("INSERT INTO config(key,value) VALUES('risk_tiers','on') ON CONFLICT(key) DO UPDATE SET value='on'")
        out = self.ok("task", "tier", "T-0001", "--classify", "--base", "main", "--head", "pr", agent="zz-lead").stdout
        self.assertIn("tier T3", out)  # the PR emptied sensitive_paths in its own copy; HOME's list still applies
        self.assertEqual(self.tier(), "T3")
        self.assertIn("sensitive path ci/guard_x.py", self.env.sql("SELECT detail FROM events WHERE action='tier'")[0][0])

    def test_new_commit_sets_max_of_old_and_new(self):
        self._home_repo()
        self.env.sql("INSERT INTO config(key,value) VALUES('risk_tiers','on') ON CONFLICT(key) DO UPDATE SET value='on'")
        git("checkout", "-q", "-b", "small", "main", cwd=self.home)
        (self.home / "web.py").write_text("x = 2\n")
        git("commit", "-q", "-m", "small change", "web.py", cwd=self.home)
        git("checkout", "-q", "main", cwd=self.home)
        self.ok("task", "tier", "T-0001", "--classify", "--base", "main", "--head", "small", agent="zz-lead")
        self.assertEqual(self.tier(), "T1")
        self.ok("task", "tier", "T-0001", "--classify", "--base", "main", "--head", "pr", agent="zz-lead")
        self.assertEqual(self.tier(), "T3")
        out = self.ok("task", "tier", "T-0001", "--classify", "--base", "main", "--head", "small", agent="zz-lead").stdout
        self.assertEqual(self.tier(), "T3")  # a later, smaller commit does not lower it
        self.assertIn("kept the higher", out)

    def test_task_tier_does_not_change_merge_or_spawn_state(self):
        before = self.env.sql("SELECT status, assignee, pr_url FROM tasks WHERE id='T-0001'")
        self.ok("task", "tier", "T-0001", "up", "T3", agent="zz-lead")
        self.assertEqual(self.env.sql("SELECT status, assignee, pr_url FROM tasks WHERE id='T-0001'"), before)


if __name__ == "__main__":
    unittest.main()
