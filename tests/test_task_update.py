import json
import unittest

from harness import FactoryTestCase


class TaskUpdateAfterTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_goal("G-002")
        for tid in ("T-0001", "T-0002", "T-0003"):
            e.seed_task(tid, status="todo", pr_url=None)
        e.seed_task("T-0009", goal="G-002", status="todo", pr_url=None)

    def deps(self, tid="T-0003"):
        return json.loads(self.env.sql("SELECT depends_on FROM tasks WHERE id=?", (tid,))[0]["depends_on"])

    def test_task_update_after_sets_deps(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001,T-0002")
        self.assertEqual(self.deps(), ["T-0001", "T-0002"])
        self.ok("task", "update", "T-0003", "--after", "T-0002")  # replaces, not appends
        self.assertEqual(self.deps(), ["T-0002"])
        p = self.ok("task", "show", "T-0003")
        self.assertIn("depends_on: " + json.dumps(["T-0002"]), p.stdout)
        ev = self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0003' AND action='update'")
        self.assertTrue(any('depends_on=["T-0002"]' in r["detail"] for r in ev))

    def test_task_update_after_unknown_id_rejected(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001")
        for bad in ("T-ZZZ", "T-0001,T-ZZZ", "T-0003", "T-0009"):  # unknown, partly unknown, self, other goal
            p = self.refused(2, "task", "update", "T-0003", "--after", bad, "--title", "changed")
            self.assertIn("factory:", p.stderr)
            self.assertEqual(self.deps(), ["T-0001"])
        self.assertEqual(self.env.sql("SELECT title FROM tasks WHERE id='T-0003'")[0]["title"], "zz-test task")

    def test_task_update_after_empty_clears(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001,T-0002")
        self.ok("task", "update", "T-0003", "--after", "")
        self.assertEqual(self.deps(), [])

    def test_task_update_without_after_keeps_deps(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001")
        self.ok("task", "update", "T-0003", "--title", "new")
        self.assertEqual(self.deps(), ["T-0001"])

    def test_task_update_after_rejects_direct_cycle(self):
        self.ok("task", "update", "T-0001", "--after", "T-0002")
        p = self.refused(2, "task", "update", "T-0002", "--after", "T-0001", "--title", "changed")
        self.assertIn("dependency cycle: T-0002 -> T-0001 -> T-0002", p.stderr)
        self.assertEqual(self.deps("T-0002"), [])
        self.assertEqual(self.env.sql("SELECT title FROM tasks WHERE id='T-0002'")[0]["title"], "zz-test task")

    def test_task_update_after_rejects_indirect_cycle(self):
        self.ok("task", "update", "T-0001", "--after", "T-0002")
        self.ok("task", "update", "T-0002", "--after", "T-0003")
        p = self.refused(2, "task", "update", "T-0003", "--after", "T-0001")
        self.assertIn("dependency cycle: T-0003 -> T-0001 -> T-0002 -> T-0003", p.stderr)
        self.assertEqual(self.deps("T-0003"), [])

    def test_task_update_after_allows_diamond(self):
        e = self.env
        e.seed_task("T-0004", status="todo", pr_url=None)
        # A=T-0001 -> B=T-0002, C=T-0003 -> D=T-0004
        self.ok("task", "update", "T-0001", "--after", "T-0002,T-0003")
        self.ok("task", "update", "T-0002", "--after", "T-0004")
        self.ok("task", "update", "T-0003", "--after", "T-0004")
        # re-set D's deps and A's deps: D is reached via both B and C, no false positive
        self.ok("task", "update", "T-0004", "--after", "")
        self.ok("task", "update", "T-0001", "--after", "T-0002,T-0003,T-0004")
        self.assertEqual(self.deps("T-0001"), ["T-0002", "T-0003", "T-0004"])
        self.assertEqual(self.deps("T-0003"), ["T-0004"])

    def test_task_update_note_only_keeps_deps_and_clean_detail(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001")
        self.ok("task", "update", "T-0003", "--note", "hi")
        self.assertEqual(self.deps(), ["T-0001"])
        ev = self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0003' AND action='update'")
        self.assertEqual(ev[-1]["detail"], "hi")

    def count(self):
        return self.env.sql("SELECT COUNT(*) AS n FROM tasks")[0]["n"]

    def test_task_add_after_unknown_id_rejected(self):
        n = self.count()
        for bad in ("T-ZZZ", "T-0001,T-ZZZ", "T-0009"):  # unknown, partly unknown, other goal
            p = self.refused(2, "task", "add", "--goal", "G-001", "--title", "x", "--after", bad)
            self.assertIn("factory:", p.stderr)
        self.assertEqual(self.count(), n)

    def test_task_add_forward_ref_cycle_impossible(self):
        # reviewer repro: add --after T-0005 -> T-0004; add --after T-0004 -> T-0005
        n = self.count()
        p = self.refused(2, "task", "add", "--goal", "G-001", "--title", "first", "--after", "T-0005")
        self.assertIn("no task T-0005", p.stderr)
        self.assertEqual(self.count(), n)
        self.ok("task", "add", "--goal", "G-001", "--title", "ok", "--after", "T-0001,T-0002")
        new = self.env.sql("SELECT id, depends_on FROM tasks ORDER BY id DESC LIMIT 1")[0]
        self.assertEqual(json.loads(new["depends_on"]), ["T-0001", "T-0002"])

    def test_task_update_after_dedupes_ids_and_clean_event_detail(self):
        self.ok("task", "update", "T-0003", "--after", "T-0001, T-0002,T-0001")
        self.assertEqual(self.deps(), ["T-0001", "T-0002"])
        ev = self.env.sql("SELECT detail FROM events WHERE entity='task' AND entity_id='T-0003' AND action='update'")
        self.assertEqual(ev[-1]["detail"], 'depends_on=["T-0001", "T-0002"]')


class CoderDoneGuardTests(FactoryTestCase):
    URL = "https://github.com/o/r/pull/7"

    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_task("T-0001", status="in_progress", pr_url=None)
        e.seed_task("T-0002", status="in_review", pr_url=self.URL)
        for name, role in (("lead-g-001-a", "lead"), ("coder-t-0001", "coder"), ("judge-1", "judge")):
            e.seed_agent(name, role, goal="G-001")

    def row(self, tid):
        return self.env.sql("SELECT status, pr_url FROM tasks WHERE id=?", (tid,))[0]

    def test_lead_closes_coder_task_without_pr(self):
        self.ok("task", "update", "T-0001", "--status", "done", agent="lead-g-001-a")
        self.assertEqual(self.row("T-0001")["status"], "done")

    def test_lead_cannot_close_coder_task_with_pr(self):
        p = self.refused(3, "task", "update", "T-0002", "--status", "done", agent="lead-g-001-a")
        self.assertIn("only done when the judge merges", p.stderr)
        self.assertEqual(self.row("T-0002")["status"], "in_review")

    def test_lead_cannot_set_done_and_pr_in_one_call(self):
        self.refused(3, "task", "update", "T-0001", "--status", "done", "--pr", self.URL, agent="lead-g-001-a")
        self.assertEqual(self.row("T-0001")["status"], "in_progress")
        self.assertIsNone(self.row("T-0001")["pr_url"])

    def test_director_closes_coder_task_without_pr(self):
        self.env.seed_agent("director-1", "director")
        self.ok("task", "update", "T-0001", "--status", "done", agent="director-1")
        self.assertEqual(self.row("T-0001")["status"], "done")

    def test_coder_cannot_close_own_task_without_pr(self):
        p = self.refused(3, "task", "update", "T-0001", "--status", "done", agent="coder-t-0001")
        self.assertIn("no PR", p.stderr)
        self.assertIn("factory judge", p.stderr)
        self.assertEqual(self.row("T-0001")["status"], "in_progress")

    def test_clearing_pr_only_owner_or_judge(self):
        self.refused(3, "task", "update", "T-0002", "--pr", "", agent="lead-g-001-a")
        self.refused(3, "task", "update", "T-0002", "--status", "done", "--pr", "", agent="lead-g-001-a")
        self.assertEqual(self.row("T-0002")["pr_url"], self.URL)
        self.ok("task", "update", "T-0002", "--pr", "", agent="judge-1")
        self.assertFalse(self.row("T-0002")["pr_url"])
        self.env.sql("UPDATE tasks SET pr_url=? WHERE id='T-0002'", (self.URL,))
        self.ok("task", "update", "T-0002", "--pr", "")  # owner
        self.assertFalse(self.row("T-0002")["pr_url"])

    def test_owner_sets_done_with_pr(self):
        # judge never reaches this guard: it is refused any task status earlier (merges go through `factory judge`)
        self.ok("task", "update", "T-0002", "--status", "done")
        self.assertEqual(self.row("T-0002")["status"], "done")
        p = self.refused(3, "task", "update", "T-0001", "--status", "done", agent="judge-1")
        self.assertIn("factory judge", p.stderr)


if __name__ == "__main__":
    unittest.main()
