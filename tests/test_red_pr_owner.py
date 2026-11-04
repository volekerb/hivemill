"""T-0982 (G-022 SC3): config red_pr_owner_alert=1 -> the tick mails the lead (director if no team) once per red/conflicted head."""
import json
import unittest

from harness import FactoryTestCase, PR_URL
from harness import git

LEAD = "lead-g-001-a"
TEAM = "g-001-a"
SHA1, SHA2 = "a" * 40, "b" * 40
OK = {"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}
BAD = {"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"}


class RedPrOwnerTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="live")
        e.seed_agent("zz-director", "director")
        e.seed_task("T-0001", status="in_progress", team=TEAM)
        self.ok("config", "red_pr_owner_alert", "1")

    def pr(self, checks, sha=SHA1, merge="BLOCKED"):
        self.env.seed_pr(sha=sha, checks=checks)
        st = json.loads(self.env.gh_state.read_text())
        st["prs"][PR_URL]["mergeStateStatus"] = merge
        self.env.set_gh(prs=st["prs"])

    def mails(self, to=LEAD):
        return self.env.sql("SELECT sender, kind, task_id, subject, body FROM messages WHERE recipient=? AND subject LIKE 'red PR%'", (to,))

    def gh_views(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "view"]]

    def spawns(self):
        return self.env.sql("SELECT name FROM agents WHERE role='coder'")

    def test_in_progress_red_no_coder_mails_lead(self):
        self.pr([OK, BAD])
        self.ok("tick")
        m = self.mails()
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["sender"], m[0]["kind"], m[0]["task_id"]), ("tick", "report", "T-0001"))
        self.assertIn("@" + SHA1[:8], m[0]["subject"])
        self.assertIn("build", m[0]["body"])
        self.assertEqual(self.spawns(), [])

    def test_in_review_red_mails_lead(self):
        self.env.sql("UPDATE tasks SET status='in_review' WHERE id='T-0001'")
        self.pr([BAD])
        self.ok("tick")
        self.assertEqual(len(self.mails()), 1)

    def test_teamless_task_mails_director(self):
        self.env.sql("UPDATE tasks SET team_id=NULL WHERE id='T-0001'")
        self.pr([BAD])
        self.ok("tick")
        self.assertEqual(len(self.mails("director")), 1)
        self.assertEqual(self.mails(), [])

    def test_two_ticks_one_sha_one_mail_new_sha_second_mail(self):
        self.pr([BAD])
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.mails()), 1)
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'red-pr-probe:%'")  # no origin in this test: the recheck window passed
        self.pr([BAD], sha=SHA2)
        self.ok("tick")
        m = self.mails()
        self.assertEqual(len(m), 2)
        self.assertIn("@" + SHA2[:8], m[1]["subject"])

    def test_blocked_task_mails_with_reason_and_waits_once(self):
        self.ok("task", "update", "T-0001", "--status", "blocked", "--waits", "owner", "--note", "waiting on owner decision X")
        self.pr([BAD])
        self.ok("tick")
        self.ok("tick")
        m = self.mails()
        self.assertEqual(len(m), 1)
        self.assertIn("blocked (waits owner)", m[0]["body"])
        self.assertIn("waiting on owner decision X", m[0]["body"])

    def test_blocked_task_with_live_coder_no_mail(self):
        self.env.sql("UPDATE tasks SET status='blocked' WHERE id='T-0001'")
        self.env.seed_agent("zz-coder-b", "coder", goal="G-001", task="T-0001", status="live", pane_id="p-b")
        self.env.set_herdr(agents=[{"name": "zz-coder-b", "agent": "claude", "pane_id": "p-b", "agent_status": "working"}])
        self.pr([BAD])
        self.ok("tick")
        self.assertEqual(self.mails(), [])

    def test_live_or_queued_coder_no_mail(self):
        self.pr([BAD])
        for st in ("live", "starting", "queued"):
            self.env.seed_agent("zz-coder-" + st, "coder", goal="G-001", task="T-0001", status=st, pane_id="p-" + st)
            self.env.set_herdr(agents=[{"name": "zz-coder-" + st, "agent": "claude", "pane_id": "p-" + st, "agent_status": "working"}])
            # a queued row carries a fresh claim so the tick's dequeue leaves it alone (no spawn anywhere in this module)
            self.env.sql("UPDATE agents SET spawn_args='{\"claimed_at\": 99999999999}' WHERE name=?", ("zz-coder-" + st,))
            self.ok("tick")
            self.assertEqual(self.mails(), [], st)
            self.env.sql("UPDATE agents SET status='exited' WHERE name=?", ("zz-coder-" + st,))

    def test_flag_off_no_mail_no_gh_call(self):
        self.ok("config", "red_pr_owner_alert", "0")
        self.pr([BAD])
        self.ok("tick")
        self.assertEqual(self.mails(), [])
        self.assertEqual(self.gh_views(), [])

    def test_green_or_pending_no_mail(self):
        self.pr([OK], merge="CLEAN")
        self.ok("tick")
        self.assertEqual(self.mails(), [])

    def test_conflicted_pr_one_mail_no_spawn(self):
        self.pr([OK], merge="DIRTY")
        self.ok("tick")
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'red-pr-probe:%'")
        self.ok("tick")
        m = self.mails()
        self.assertEqual(len(m), 1)
        self.assertIn("conflict", m[0]["body"])
        self.assertEqual(self.spawns(), [])

    def test_parked_team_ci_wake_red_not_double_mailed(self):
        self.ok("config", "lead_parking", "1")
        self.env.sql("UPDATE tasks SET status='in_review' WHERE id='T-0001'")
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.pr([BAD])
        self.ok("tick")
        n = self.env.sql("SELECT COUNT(*) AS n FROM messages WHERE recipient=? AND (subject LIKE 'CI red%' OR subject LIKE 'red PR%')", (LEAD,))
        self.assertEqual(n[0]["n"], 1)

    def test_closed_team_task_mails_director_not_dead_lead(self):
        self.env.sql("UPDATE teams SET status='closed' WHERE id=?", (TEAM,))
        self.pr([BAD])
        self.ok("tick")
        self.assertEqual(len(self.mails("director")), 1)
        self.assertEqual(self.mails(), [])

    def test_green_head_probed_once_per_window(self):
        self.pr([OK], merge="CLEAN")
        self.ok("tick")
        n = len(self.gh_views())
        self.assertEqual(n, 1)
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), n)
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'red-pr-probe:%'")
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), n + 1)

    def test_green_head_with_readable_origin_probed_once_per_window(self):
        sha = self.push_origin_head()
        self.pr([OK], sha=sha, merge="CLEAN")
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)

    def test_mailed_head_makes_no_gh_call(self):
        sha = self.push_origin_head()
        self.pr([BAD], sha=sha)
        self.ok("tick")
        self.assertEqual(len(self.mails()), 1)
        n = len(self.gh_views())
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), n)
        self.assertEqual(len(self.mails()), 1)

    def push_origin_head(self):
        e = self.env
        bare, wt = e.home / "rp-origin.git", e.home / "rp-wt"
        git("init", "-q", "--bare", "-b", "main", str(bare), cwd=e.home)
        git("clone", "-q", str(bare), str(wt), cwd=e.home)
        git("config", "user.email", "zz-test@example.invalid", cwd=wt)
        git("config", "user.name", "zz-test", cwd=wt)
        git("remote", "add", "origin", str(bare), cwd=e.home_dir)
        (wt / "f").write_text("x")
        git("add", "f", cwd=wt)
        git("commit", "-q", "-m", "f", cwd=wt)
        git("push", "-q", "-f", "origin", "HEAD:refs/heads/factory/t-0001", cwd=wt)
        sha = git("rev-parse", "HEAD", cwd=wt)
        e.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        return sha


if __name__ == "__main__":
    unittest.main()
