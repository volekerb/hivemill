"""T-0370: tasks record who a blocked task waits on (owner | internal); task show/list, res and digest show it."""
import datetime as dt
import sqlite3
import unittest

from harness import FactoryTestCase

FROZEN = dt.datetime(2026, 10, 27, 12, 0).astimezone()


class TaskWaitsTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(lead="zz-test-lead")
        for tid in ("T-0001", "T-0002", "T-0003"):
            e.seed_task(tid, status="in_progress", pr_url=None, team="g-001-a")

    def waits(self, tid):
        return self.env.sql("SELECT waits FROM tasks WHERE id=?", (tid,))[0]["waits"]

    def test_default_is_internal_and_explicit_owner_sets(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", "--reason", "queue full")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "needs owner key", "--waits", "owner")
        self.assertEqual((self.waits("T-0001"), self.waits("T-0002")), ("internal", "owner"))
        ev = self.env.sql("SELECT detail FROM events WHERE entity_id='T-0002' AND action='update'")[0]["detail"]
        self.assertIn("waits=owner", ev)
        self.assertIn("needs owner key", ev)

    def test_show_and_list_print_waits_for_blocked_only(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.assertIn("waits: owner", self.ok("task", "show", "T-0002").stdout)
        self.assertNotIn("waits:", self.ok("task", "show", "T-0001").stdout)
        rows = {l.split()[0]: l for l in self.ok("task", "list").stdout.splitlines()[1:]}
        self.assertRegex(rows["T-0002"], r"blocked\s+owner")
        self.assertNotIn("owner", rows["T-0001"])
        self.assertIn("WAITS", self.ok("task", "list").stdout.splitlines()[0])

    def test_clearing_waits_by_leaving_blocked_or_flipping(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--waits", "internal")        # explicit flip while blocked
        self.assertEqual(self.waits("T-0002"), "internal")
        self.ok("task", "update", "T-0002", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--status", "in_progress")    # unblock resets
        self.assertEqual(self.waits("T-0002"), "internal")
        self.ok("task", "update", "T-0002", "--status", "blocked")        # re-block without --waits: internal
        self.assertEqual(self.waits("T-0002"), "internal")

    def test_reblock_without_waits_keeps_existing_value(self):
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "still waiting")
        self.assertEqual(self.waits("T-0002"), "owner")

    def test_waits_refused_when_not_blocked_or_bad_value(self):
        p = self.refused(2, "task", "update", "T-0001", "--waits", "owner")
        self.assertIn("--status blocked", p.stderr)
        self.assertEqual(self.waits("T-0001"), "internal")
        self.refused(2, "task", "update", "T-0001", "--status", "blocked", "--waits", "nobody")
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_progress")

    def test_res_counts_owner_vs_internal(self):
        self.ok("task", "update", "T-0001", "--status", "blocked")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--waits", "owner")
        self.ok("task", "update", "T-0003", "--status", "blocked")
        self.assertIn("blocked tasks: 1 owner-blocked · 2 internal-blocked", self.ok("res").stdout)

    def test_res_blocked_line_after_workers_line(self):
        lines = self.ok("res").stdout.splitlines()
        i = next(n for n, l in enumerate(lines) if l.startswith("workers live "))
        self.assertTrue(any(l.startswith("blocked tasks: ") for l in lines[i + 1:]), lines)

    def test_digest_counts_and_tags(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", "--reason", "queue")
        self.ok("task", "update", "T-0002", "--status", "blocked", "--reason", "need key", "--waits", "owner")
        out = self.ok("digest", "--date", FROZEN.date().isoformat(), "--hours", "24",
                      extra_env={"FACTORY_NOW": FROZEN.isoformat()}).stdout
        self.assertIn("Blocked tasks: 1 owner-blocked, 1 internal-blocked", out)
        self.assertIn("T-0002 zz-test task — blocked (owner): need key", out)
        self.assertIn("T-0001 zz-test task — blocked: queue", out)


class MigrateWaitsTests(FactoryTestCase):
    def test_old_db_gets_waits_column_default_internal(self):
        e = self.env
        e.seed_goal()
        e.seed_task("T-0001", status="blocked", pr_url=None)
        e.sql("ALTER TABLE tasks DROP COLUMN waits")
        self.assertNotIn("waits", [r["name"] for r in e.sql("PRAGMA table_info(tasks)")])
        self.ok("task", "show", "T-0001")                  # any command opens the DB through migrate()
        self.assertEqual(e.sql("SELECT waits FROM tasks WHERE id='T-0001'")[0]["waits"], "internal")
        self.assertIn("waits: internal", self.ok("task", "show", "T-0001").stdout)


if __name__ == "__main__":
    unittest.main()
