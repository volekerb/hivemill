"""T-0400 (G-014): the tick mails a parked lead when its team's in_review PR is CLEAN + green (or red), then wakes it."""
import json
import re
import unittest

from harness import FactoryTestCase, FACTORY, FACTORY_SRC, PR_URL
from harness import git

LEAD = "lead-g-001-a"
TEAM = "g-001-a"
SHA1, SHA2 = "a" * 40, "b" * 40
OK = {"name": "unittest", "status": "COMPLETED", "conclusion": "SUCCESS"}
OK2 = {"name": "lint", "status": "COMPLETED", "conclusion": "SKIPPED"}
BAD = {"name": "build", "status": "COMPLETED", "conclusion": "FAILURE"}
CANCELLED = {"name": "e2e", "status": "COMPLETED", "conclusion": "CANCELLED"}
RUNNING = {"name": "slow", "status": "IN_PROGRESS", "conclusion": ""}


class CiWakeTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal()
        e.seed_team(TEAM, lead=LEAD)
        e.seed_agent(LEAD, "lead", goal="G-001", team=TEAM, status="exited")
        e.seed_agent("zz-director", "director")
        e.seed_task("T-0001", status="in_review", team=TEAM)
        e.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.ok("config", "lead_parking", "1")

    def pr(self, checks, sha=SHA1, merge="CLEAN"):
        self.env.seed_pr(sha=sha, checks=checks)
        self.env.set_gh(prs={**json.loads(self.env.gh_state.read_text())["prs"],
                             PR_URL: {**json.loads(self.env.gh_state.read_text())["prs"][PR_URL], "mergeStateStatus": merge}})

    def mails(self, prefix):
        return self.env.sql("SELECT sender, kind, task_id, subject, body FROM messages WHERE recipient=? AND subject LIKE ?",
                            (LEAD, prefix + "%"))

    def gh_views(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "view"] and "number,state,headRefOid,mergeStateStatus,statusCheckRollup" in c["argv"]]

    def lead(self):
        return self.env.sql("SELECT status FROM agents WHERE name=?", (LEAD,))[0]["status"]

    def test_green_one_message_and_same_tick_wake(self):
        self.pr([OK, OK2])
        self.ok("tick")
        m = self.mails("CI green: T-0001")
        self.assertEqual(len(m), 1)
        self.assertEqual((m[0]["sender"], m[0]["kind"], m[0]["task_id"]), ("tick", "report", "T-0001"))
        self.assertIn("@" + SHA1[:8], m[0]["subject"])
        self.assertIn(SHA1, m[0]["body"])
        self.assertIn("unittest", m[0]["body"])
        self.assertIn("M6364", m[0]["body"])
        self.assertEqual(self.lead(), "live")

    def test_same_sha_not_repeated_new_sha_new_message(self):
        self.pr([OK])
        self.ok("tick")
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.ok("tick")
        self.assertEqual(len(self.mails("CI green")), 1)
        self.pr([OK], sha=SHA2)
        self.env.sql("DELETE FROM throttle WHERE key LIKE 'ci-head:%'")  # the recheck window passed
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.ok("tick")
        subjects = [m["subject"] for m in self.mails("CI green")]
        self.assertEqual(len(subjects), 2)
        self.assertIn("@" + SHA2[:8], subjects[1])

    def test_red_once_naming_failed_checks(self):
        self.pr([OK, BAD, CANCELLED], merge="BLOCKED")
        self.ok("tick")
        m = self.mails("CI red: T-0001")
        self.assertEqual(len(m), 1)
        self.assertIn("build", m[0]["subject"])
        self.assertIn("e2e", m[0]["subject"])
        self.assertNotIn("unittest", m[0]["subject"])
        self.assertEqual(self.mails("CI green"), [])
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.ok("tick")
        self.assertEqual(len(self.mails("CI red")), 1)

    def test_pending_or_not_clean_no_message(self):
        self.pr([OK, RUNNING])
        self.ok("tick")
        self.pr([OK], merge="BEHIND")
        self.ok("tick")
        self.assertEqual(self.mails("CI"), [])
        self.assertEqual(self.lead(), "exited")

    def test_live_lead_no_message_no_gh_call(self):
        self.pr([OK])
        self.env.sql("UPDATE teams SET parked_at=NULL WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='live' WHERE name=?", (LEAD,))
        self.ok("tick")
        self.assertEqual(self.mails("CI"), [])
        self.assertEqual(self.gh_views(), [])

    def test_flag_off_no_message_no_gh_call(self):
        self.pr([OK])
        self.ok("config", "lead_parking", "0")
        self.ok("tick")
        self.assertEqual(self.mails("CI"), [])
        self.assertEqual(self.gh_views(), [])

    def test_closed_pr_no_message(self):
        self.env.seed_pr(state="CLOSED", checks=[BAD])
        self.ok("tick")
        self.assertEqual(self.mails("CI"), [])

    def test_gh_error_logged_tick_continues(self):
        self.env.seed_pr(url="https://github.com/x/y/pull/99")  # PR_URL unknown to the stub: gh exits 1
        p = self.env.run("tick")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.mails("CI"), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='ci-wake-error' AND entity_id='T-0001'")
        self.assertEqual(len(ev), 1)

    def test_slow_gh_times_out_logged_tick_continues(self):
        self.pr([OK])
        self.env.set_gh(pr_view_sleep=5)
        p = self.env.run("tick", extra_env={"FACTORY_CI_WAKE_GH_TIMEOUT_S": "1"})
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(self.mails("CI"), [])
        ev = self.env.sql("SELECT detail FROM events WHERE action='ci-wake-error' AND entity_id='T-0001'")
        self.assertEqual(len(ev), 1)
        self.assertIn("timed out", ev[0]["detail"])

    def test_default_gh_timeout_is_short(self):
        m = re.search(r"^CI_WAKE_GH_TIMEOUT_S = (\d+)", FACTORY_SRC.read_text(), re.M)
        self.assertIsNotNone(m)
        self.assertLessEqual(int(m.group(1)), 20)

    def test_notified_head_makes_no_gh_call_until_recheck_window_passes(self):
        self.pr([OK])
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        for _ in range(2):  # lead woke and parked again; head unchanged
            self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
            self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
            self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        self.env.sql("UPDATE throttle SET ts='0' WHERE key LIKE 'ci-head:%'")
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 2)
        self.assertEqual(len(self.mails("CI green")), 1)

    def test_pending_pr_is_still_polled_every_tick(self):
        self.pr([OK, RUNNING])
        self.ok("tick")
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 2)

    # T-0456: the gh call is skipped by head sha (origin branch head via ls-remote), not by time.
    def setup_origin(self):
        e = self.env
        self.bare, self.wt = e.home / "ci-origin.git", e.home / "ci-wt"
        git("init", "-q", "--bare", "-b", "main", str(self.bare), cwd=e.home)
        git("clone", "-q", str(self.bare), str(self.wt), cwd=e.home)
        git("config", "user.email", "zz-test@example.invalid", cwd=self.wt)
        git("config", "user.name", "zz-test", cwd=self.wt)
        git("remote", "add", "origin", str(self.bare), cwd=e.home_dir)
        e.sql("UPDATE tasks SET branch='factory/t-0001' WHERE id='T-0001'")
        self.push("one")
        return git("rev-parse", "HEAD", cwd=self.wt)

    def push(self, name):
        (self.wt / name).write_text(name)
        git("add", name, cwd=self.wt)
        git("commit", "-q", "-m", name, cwd=self.wt)
        git("push", "-q", "-f", "origin", "HEAD:refs/heads/factory/t-0001", cwd=self.wt)
        return git("rev-parse", "HEAD", cwd=self.wt)

    def park(self):
        self.env.sql("UPDATE teams SET parked_at='2026-01-01T00:00:00+00:00' WHERE id=?", (TEAM,))
        self.env.sql("UPDATE agents SET status='exited' WHERE name=?", (LEAD,))

    def test_changed_head_gets_gh_call_on_next_tick(self):
        sha1 = self.setup_origin()
        self.pr([OK], sha=sha1)
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        sha2 = self.push("two")
        self.pr([OK], sha=sha2)
        self.park()
        self.ok("tick")  # same minute: no time window may hold it back
        self.assertEqual(len(self.gh_views()), 2)
        self.assertEqual(len(self.mails("CI green")), 2)

    def test_handled_final_head_makes_no_gh_call_on_three_ticks(self):
        self.pr([BAD], sha=self.setup_origin())
        self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        for _ in range(3):
            self.park()
            self.ok("tick")
        self.assertEqual(len(self.gh_views()), 1)
        self.assertEqual(len(self.mails("CI red")), 1)

    def test_red_then_rerun_green_on_same_head_wakes_after_window(self):
        sha1 = self.setup_origin()
        self.pr([BAD], sha=sha1)
        self.ok("tick")
        self.pr([OK], sha=sha1)  # failed job re-run, now green
        self.park()
        self.ok("tick")
        self.assertEqual(len(self.mails("CI green")), 0)  # inside the window: no gh call
        self.env.sql("UPDATE throttle SET ts='0' WHERE key LIKE 'ci-head:%'")
        self.park()
        self.ok("tick")
        self.assertEqual(len(self.mails("CI green")), 1)

    def test_pending_on_same_head_polled_then_final_wakes_once(self):
        sha1 = self.setup_origin()
        self.pr([OK, RUNNING], sha=sha1)
        for _ in range(2):
            self.ok("tick")
            self.park()
        self.assertEqual(len(self.gh_views()), 2)
        self.assertEqual(len(self.mails("CI green")), 0)
        self.pr([OK, OK2], sha=sha1)  # the rerun finished on the same head
        self.ok("tick")
        self.assertEqual(len(self.mails("CI green")), 1)
        for _ in range(2):
            self.park()
            self.ok("tick")
        self.assertEqual(len(self.mails("CI green")), 1)
        self.assertEqual(len(self.gh_views()), 3)


if __name__ == "__main__":
    unittest.main()
