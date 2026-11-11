import os
import unittest
from unittest import mock

from harness import FactoryTestCase

PANE = "w9:p9"
IN_PANE = {"CLAUDECODE": "1", "HERDR_PANE_ID": PANE}


class IdentityTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="in_review", team="g-001-a")
        self.env.seed_pr()

    def events(self, action):
        return self.env.sql("SELECT * FROM events WHERE action=?", (action,))

    def test_identity_unset_in_pane_recovers_from_pane_id(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001", pane_id=PANE)
        p = self.ok("whoami", extra_env=IN_PANE)
        self.assertIn("zz-test-coder", p.stdout)
        self.assertIn("role=coder", p.stdout)
        self.assertIn("FACTORY_AGENT", p.stderr)
        # recovered as coder: role gates apply
        self.refused(3, "goal", "new", "--title", "zz-test", extra_env=IN_PANE)

    def test_identity_unset_in_pane_no_match_refuses_write(self):
        for extra in ({"CLAUDECODE": "1"}, IN_PANE):
            p = self.env.run("whoami", extra_env=extra)
            self.assertEqual(p.returncode, 0)
            self.assertNotIn("not a factory agent", p.stdout)
            self.assertTrue(p.stdout.startswith("unknown"), p.stdout)
            p = self.refused(3, "goal", "new", "--title", "zz-test", extra_env=extra)
            self.assertIn("FACTORY_AGENT=<your agent name>, or FACTORY_AGENT=owner if you are the owner", p.stderr)
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    def test_identity_unknown_serve_not_refused(self):
        # serve is read-only: with an unknown identity it fails on its own terms (bad bind), never exit 3
        for extra in ({"CLAUDECODE": "1"}, IN_PANE):
            p = self.env.run("serve", "--bind", "0.0.0.0", extra_env=extra)
            self.assertNotEqual(p.returncode, 3, p.stderr)
            self.assertNotIn("identity unknown", p.stderr)

    def test_identity_owner_shell_unchanged(self):
        self.assertIn("owner", self.ok("whoami").stdout)
        self.ok("goal", "new", "--title", "zz-test owner goal")

    def test_identity_unset_in_pane_cannot_merge(self):
        self.env.seed_agent("zz-test-judge", "judge", goal="G-001", team="g-001-a", pane_id="w1:p1")
        for extra in ({"CLAUDECODE": "1"}, IN_PANE):
            self.refused(3, "judge", "T-0001", "--merge", "--reason", "zz-test", extra_env=extra)
        self.assertEqual(self.env.sql("SELECT status FROM tasks WHERE id='T-0001'")[0]["status"], "in_review")
        self.assertEqual(self.events("merged"), [])

    def test_identity_explicit_owner_in_agent_context(self):
        p = self.ok("whoami", agent="owner", extra_env=IN_PANE)
        self.assertIn("owner", p.stdout)
        self.ok("goal", "new", "--title", "zz-test owner goal", agent="owner", extra_env={"CLAUDECODE": "1"})

    def test_identity_unknown_inbox_no_side_effects(self):
        self.env.seed_agent("zz-test-coder", "coder", pane_id="w1:p1")
        self.ok("msg", "zz-test-coder", "hello", "body", agent="owner")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 1)
        for extra in ({"CLAUDECODE": "1"}, IN_PANE):
            p = self.ok("inbox", extra_env=extra)
            self.assertEqual(p.stdout, "")
            p = self.ok("inbox", "--agent", "zz-test-coder", extra_env=extra)
            self.assertEqual(p.stdout, "")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM messages WHERE read_at IS NULL")[0]["c"], 1)

    def test_identity_agent_pane_claiming_owner_refused(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", pane_id=PANE)
        p = self.refused(3, "goal", "new", "--title", "zz-test", agent="owner", extra_env=IN_PANE)
        self.assertIn("zz-test-coder", p.stderr)
        ev = self.events("identity-spoof")
        self.assertEqual(len(ev), 1)
        self.assertIn(PANE, ev[0]["detail"])
        self.assertEqual(ev[0]["entity_id"], "zz-test-coder")
        self.assertEqual(self.env.sql("SELECT count(*) c FROM goals")[0]["c"], 1)

    def test_identity_stale_pane_may_claim_owner(self):
        # statuses that no longer own a pane: exited (reaped), lost, queued with a stale pane_id
        for i, st in enumerate(("exited", "lost", "queued")):
            self.env.seed_agent(f"zz-test-old{i}", "coder", status=st, pane_id=PANE)
            self.ok("goal", "new", "--title", f"zz-test {st}", agent="owner", extra_env=IN_PANE)
            self.ok("whoami", agent="owner", extra_env=IN_PANE)
        self.assertEqual(self.events("identity-spoof"), [])

    def test_identity_done_pane_claiming_owner_refused(self):
        self.env.seed_agent("zz-test-done", "coder", status="done", pane_id=PANE)
        self.refused(3, "whoami", agent="owner", extra_env=IN_PANE)
        ev = self.events("identity-spoof")
        self.assertEqual(len(ev), 1)
        self.assertEqual(ev[0]["actor"], f"pane:{PANE}")

    def test_identity_stale_pane_not_recovered(self):
        self.env.seed_agent("zz-test-old", "coder", status="exited", pane_id=PANE)
        self.assertTrue(self.ok("whoami", extra_env=IN_PANE).stdout.startswith("unknown"))

    def test_identity_explicit_agent_unchanged_in_pane(self):
        self.env.seed_agent("zz-test-director", "director", pane_id="w1:p1")
        p = self.ok("whoami", agent="zz-test-director", extra_env=IN_PANE)
        self.assertIn("role=director", p.stdout)
        self.assertEqual(p.stderr, "")


class StalePaneRowTests(FactoryTestCase):
    """T-1292: a stale live worker row on a reused pane id must not make an HQ agent resolve as a worker."""

    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.env.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", team="g-001-a", pane_id=PANE)
        self.env.sql("UPDATE agents SET session_id='sess-rev' WHERE name='zz-test-reviewer'")

    def worker_of(self, agent="zz-test-lead"):
        env = {"FACTORY_AGENT": agent, "HERDR_PANE_ID": PANE, "FACTORY_PROC_DIR": str(self.env.proc_dir)}
        with self.env.load_factory() as f:
            with mock.patch.dict(os.environ, env):
                os.environ.pop("FACTORY_ROLE", None)
                return f.caller_is_worker()

    def events(self, action):
        return self.env.sql("SELECT * FROM events WHERE action=?", (action,))

    def test_lead_on_reused_pane_with_stale_reviewer_row_is_not_worker(self):
        # the T-1292 incident: the reviewer's claude is gone, its row stayed live on the pane id the lead now holds
        self.assertIsNone(self.worker_of())
        ev = self.events("identity-conflict")
        self.assertEqual(len(ev), 1)
        self.assertIn("zz-test-reviewer", ev[0]["detail"])
        self.assertIn("zz-test-lead", ev[0]["detail"])
        self.assertEqual(self.events("identity-spoof"), [])
        self.assertIsNone(self.worker_of())  # asking again records no second event
        self.assertEqual(len(self.events("identity-conflict")), 1)

    def test_lead_on_reused_pane_can_run_park_via_the_cli(self):
        p = self.env.run("park", "zz-test note", agent="zz-test-lead",
                         extra_env={"HERDR_PANE_ID": PANE, "CLAUDECODE": "1"})
        self.assertNotIn("workers may not run", p.stderr)

    def test_worker_forging_hq_env_in_its_own_pane_still_refused(self):
        # a live worker: a claude process runs its session, so the row is not stale whatever env it forges
        self.env.fake_claude(4101, argv=("claude", "--resume", "sess-rev"))
        self.assertEqual(self.worker_of(), "reviewer")
        self.assertEqual(len(self.events("identity-spoof")), 1)
        self.assertEqual(self.events("identity-conflict"), [])
        self.refused(3, "park", "zz-test note", agent="zz-test-lead",
                     extra_env={"HERDR_PANE_ID": PANE, "CLAUDECODE": "1"})

    def test_worker_row_without_session_id_fails_closed(self):
        self.env.sql("UPDATE agents SET session_id=NULL WHERE name='zz-test-reviewer'")
        self.assertEqual(self.worker_of(), "reviewer")

    def test_stopped_row_no_longer_matches_its_pane_id(self):
        self.env.fake_claude(4101, argv=("claude", "--resume", "sess-rev"))
        self.assertEqual(self.worker_of(), "reviewer")
        self.env.sql("UPDATE agents SET status='stopped' WHERE name='zz-test-reviewer'")
        self.assertIsNone(self.worker_of())
        self.assertEqual(self.events("identity-conflict"), [])


if __name__ == "__main__":
    unittest.main()
