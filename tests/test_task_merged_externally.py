import json
import unittest

from harness import FactoryTestCase, PR_URL

SHA = "a" * 40


class MergedExternallyTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_task("T-0001", status="in_progress")
        e.seed_task("T-0002", status="todo", pr_url=None)
        e.sql("UPDATE tasks SET depends_on=? WHERE id='T-0002'", (json.dumps(["T-0001"]),))
        e.seed_agent("zz-test-director", "director")
        e.seed_agent("zz-test-coder", "coder", goal="G-001", task="T-0001")

    def merged(self, state="MERGED", sha=SHA, status="identical"):
        e = self.env
        e.seed_pr(state=state)
        st = json.loads(e.gh_state.read_text())
        st["prs"][PR_URL]["mergeCommit"] = {"oid": sha} if sha else None
        st["api"] = [{"match": ["compare"], "out": {"status": status}}]
        e.gh_state.write_text(json.dumps(st))

    def close(self, agent="zz-test-director"):
        return self.env.run("task", "update", "T-0001", "--status", "done", "--merged-externally",
                            "--reason", "owner merged by hand", agent=agent)

    def status(self):
        return self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"]

    def test_director_closes_merged_pr_and_dependants_become_ready(self):
        self.merged()
        self.assertNotIn("T-0002", self.ok("task", "ready").stdout)
        p = self.close()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertEqual(self.status(), "done")
        self.assertIn("T-0002", self.ok("task", "ready").stdout)
        hist = self.ok("task", "show", "T-0001").stdout
        for want in ("zz-test-director", "merged-externally", PR_URL, SHA, "owner merged by hand"):
            self.assertIn(want, hist)

    def test_owner_may_close(self):
        self.merged()
        self.assertEqual(self.env.run("task", "update", "T-0001", "--status", "done", "--merged-externally",
                                      "--reason", "x").returncode, 0)
        self.assertEqual(self.status(), "done")

    def test_not_merged_refused(self):
        for state, sha in (("OPEN", None), ("CLOSED", None)):
            self.merged(state=state, sha=sha)
            p = self.close()
            self.assertEqual(p.returncode, 3, p.stderr)
            self.assertIn("not MERGED", p.stderr)
            self.assertEqual(self.status(), "in_progress")

    def test_merge_commit_not_on_base_refused(self):
        self.merged(status="diverged")
        p = self.close()
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("not on main", p.stderr)
        self.assertEqual(self.status(), "in_progress")

    def test_gh_failure_refused(self):
        self.merged()
        st = json.loads(self.env.gh_state.read_text())
        st["api"] = [{"match": ["compare"], "exit": 1, "err": "boom"}]
        self.env.gh_state.write_text(json.dumps(st))
        self.assertEqual(self.close().returncode, 3)
        self.assertEqual(self.status(), "in_progress")

    def test_wrong_role_refused(self):
        self.merged()
        p = self.close(agent="zz-test-coder")
        self.assertEqual(p.returncode, 3, p.stderr)
        self.assertIn("only the director or the owner", p.stderr)
        self.assertEqual(self.status(), "in_progress")

    def test_other_flags_refused(self):
        self.merged()
        p = self.env.run("task", "update", "T-0001", "--status", "done", "--merged-externally", "--reason", "x",
                         "--priority", "1", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(self.status(), "in_progress")

    def test_already_done_and_no_pr_refused(self):
        self.merged()
        self.env.sql("UPDATE tasks SET status='done' WHERE id='T-0001'")
        self.assertEqual(self.close().returncode, 2)
        p = self.env.run("task", "update", "T-0002", "--status", "done", "--merged-externally", "--reason", "x",
                         agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertIn("not a coder task with a PR", p.stderr)

    def test_needs_reason(self):
        self.merged()
        p = self.env.run("task", "update", "T-0001", "--status", "done", "--merged-externally", agent="zz-test-director")
        self.assertEqual(p.returncode, 2, p.stderr)
        self.assertEqual(self.status(), "in_progress")


if __name__ == "__main__":
    unittest.main()
