"""T-0956: `factory spawn qa` on a task with no PR and no worktree is refused (no cwd = the shared checkout), and
`factory done` by a qa worker on a no-PR qa task that ended BLOCKED with no verdict reopens the task instead of
closing it. Real CLI as a subprocess, temp home."""
import unittest

from harness import FactoryTestCase

FIX = "factory spawn qa --task T-0001 --cwd <worktree>"


class SpawnQaNoPr(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team("g-001-a", lead="zz-lead")
        e.seed_agent("zz-director", "director")

    def spawn(self, *extra):
        return self.env.run("spawn", "qa", "--task", "T-0001", *extra, agent="zz-director")

    def agents(self):
        return [r["name"] for r in self.env.sql("SELECT name FROM agents WHERE role='qa'")]

    def test_no_pr_no_worktree_no_cwd_refused(self):
        self.env.seed_task(role="qa", status="todo", team="g-001-a", pr_url=None)
        p = self.spawn()
        out = p.stdout + p.stderr
        self.assertEqual(p.returncode, 2, out)
        self.assertIn("no PR and no worktree", out)
        self.assertIn("--cwd <worktree>", out)
        self.assertEqual(self.agents(), [])

    def test_no_pr_with_explicit_cwd_spawns(self):
        self.env.seed_task(role="qa", status="todo", team="g-001-a", pr_url=None)
        p = self.spawn("--cwd", str(self.env.home))
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.agents()), 1)

    def test_no_pr_with_existing_worktree_spawns(self):
        self.env.seed_task(role="qa", status="todo", team="g-001-a", pr_url=None)
        self.env.sql("UPDATE tasks SET worktree=? WHERE id='T-0001'", (str(self.env.home),))
        p = self.spawn()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.agents()), 1)

    def test_task_with_pr_not_refused_by_this_rule(self):
        self.env.seed_task(role="qa", status="todo", team="g-001-a")
        p = self.spawn()
        self.assertNotIn("no PR and no worktree", p.stdout + p.stderr)


class DoneQaBlocked(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-lead")
        e.seed_agent("zz-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_task(role="qa", status="in_progress", team="g-001-a", pr_url=None)
        e.seed_agent("zz-qa", "qa", goal="G-001", team="g-001-a", task="T-0001")

    def status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    def report(self):
        return self.env.sql("SELECT body FROM messages WHERE recipient='zz-lead' AND kind='report'")[0]["body"]

    def test_blocked_no_verdict_reopens_and_tells_lead(self):
        self.ok("done", "BLOCKED: guard denied git checkout, no runs", agent="zz-qa")
        self.assertEqual(self.status(), "todo")
        self.assertIn("NOT closed", self.report())
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-qa'")[0]["status"], "done")

    def test_normal_summary_closes(self):
        self.ok("done", "ran the checks, all 5 pass", agent="zz-qa")
        self.assertEqual(self.status(), "done")
        self.assertNotIn("NOT closed", self.report())

    def test_blocked_with_a_verdict_closes(self):
        self.env.sql("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                     "VALUES('T-0001','x','s','qa','pass','ok','zz-qa','t')")
        self.ok("done", "BLOCKED on one item, rest passed", agent="zz-qa")
        self.assertEqual(self.status(), "done")

    def test_blocked_with_pr_closes_as_before(self):
        self.env.sql("UPDATE tasks SET pr_url='https://github.com/o/r/pull/1' WHERE id='T-0001'")
        self.ok("done", "BLOCKED: cannot run", agent="zz-qa")
        self.assertEqual(self.status(), "done")


if __name__ == "__main__":
    unittest.main()
