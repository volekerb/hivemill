"""T-0941: `factory task update T --role R` applies the role with checks and a history event; `task add` keeps its
coder default now that the parser default is gone."""
import unittest

from harness import FactoryTestCase

T = "T-0001"
LEAD = "zz-lead"


class Base(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team("g-001-a", lead=LEAD)
        self.env.seed_agent(LEAD, "lead", goal="G-001", team="g-001-a", status="exited")
        self.env.seed_task(T, role="coder", status="todo", pr_url=None, team="g-001-a")

    def row(self):
        return dict(self.env.sql("SELECT * FROM tasks WHERE id=?", (T,))[0])

    def upd(self, *args, agent=LEAD):
        return self.env.run("task", "update", T, *args, agent=agent)

    def assert_unchanged(self, before):
        self.assertEqual(self.row(), before)


class TaskUpdateRoleTests(Base):
    def test_lead_changes_role_and_history_records_it(self):
        p = self.upd("--role", "researcher")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.row()["role"], "researcher")
        self.assertIn("role=coder->researcher", self.ok("task", "show", T).stdout)

    def test_same_role_alone_is_allowed(self):
        p = self.upd("--role", "coder")
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertIn("role=coder->coder", self.ok("task", "show", T).stdout)

    def test_invalid_role_refused_row_unchanged(self):
        before = self.row()
        p = self.upd("--role", "bogus", "--title", "new title")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("role must be one of", p.stderr)
        self.assert_unchanged(before)

    def test_live_and_queued_agent_refuse_stopping_agent_does_not(self):
        for status in ("live", "queued"):
            self.env.seed_agent(f"zz-coder-{status}", "coder", goal="G-001", task=T, status=status)
            before = self.row()
            p = self.upd("--role", "qa", "--status", "blocked")
            self.assertNotEqual(p.returncode, 0, status)
            self.assertIn(f"zz-coder-{status}", p.stderr)
            self.assertIn("factory stop", p.stderr)
            self.assert_unchanged(before)
            self.env.sql("UPDATE agents SET stop_requested_at='2026-01-01T00:00:00+00:00' WHERE name=?",
                         (f"zz-coder-{status}",))
            p = self.upd("--role", "qa")
            self.assertEqual(p.returncode, 0, p.stderr)
            self.env.sql("UPDATE tasks SET role='coder' WHERE id=?", (T,))

    def test_worker_actor_refused_row_unchanged(self):
        for role in ("coder", "pm"):
            self.env.seed_agent(f"zz-{role}-x", role, goal="G-001", status="live")
            before = self.row()
            p = self.upd("--role", "qa", agent=f"zz-{role}-x")
            self.assertNotEqual(p.returncode, 0, role)
            self.assert_unchanged(before)

    def test_sandbox_required_task_refuses_unsandboxable_role(self):
        self.env.sql("UPDATE tasks SET sandbox_required=1 WHERE id=?", (T,))
        before = self.row()
        p = self.upd("--role", "pm")
        self.assertNotEqual(p.returncode, 0)
        self.assertIn("cannot be sandboxed", p.stderr)
        self.assert_unchanged(before)

    def test_update_without_role_keeps_non_coder_role(self):
        self.env.sql("UPDATE tasks SET role='qa' WHERE id=?", (T,))
        p = self.upd("--title", "x")
        self.assertEqual(p.returncode, 0, p.stderr)
        row = self.row()
        self.assertEqual((row["role"], row["title"]), ("qa", "x"))
        self.assertNotIn("role=", self.ok("task", "show", T).stdout.split("## history")[-1])


class TaskAddRoleTests(Base):
    def add(self, *extra):
        return self.env.run("task", "add", "--goal", "G-001", "--title", "t", "--spec", "s", *extra, agent=LEAD)

    def test_add_default_explicit_and_bogus_role(self):
        p = self.add()
        self.assertEqual(p.returncode, 0, p.stderr)
        q = self.add("--role", "qa")
        self.assertEqual(q.returncode, 0, q.stderr)
        roles = [r["role"] for r in self.env.sql("SELECT role FROM tasks WHERE id IN (?,?)",
                                                 (p.stdout.strip(), q.stdout.strip()))]
        self.assertEqual(sorted(roles), ["coder", "qa"])
        n = len(self.env.sql("SELECT id FROM tasks"))
        bad = self.add("--role", "bogus")
        self.assertNotEqual(bad.returncode, 0)
        self.assertIn("role must be one of", bad.stderr)
        self.assertEqual(len(self.env.sql("SELECT id FROM tasks")), n)


if __name__ == "__main__":
    unittest.main()
