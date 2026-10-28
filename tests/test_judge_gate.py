"""T-0391: the tick starts a judge only when its PR's CI is green and fewer than max_live_judges judges are live or
starting; a held judge stays queued and the rows behind it start."""
import datetime as dt
import json
import time
import unittest

from harness import PR_URL, FactoryTestCase
from test_dequeue import DequeueBase, at

GREEN = [{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "SUCCESS"},
         {"__typename": "CheckRun", "name": "lint", "status": "COMPLETED", "conclusion": "SKIPPED"}]
PENDING = [{"__typename": "CheckRun", "name": "unit", "status": "IN_PROGRESS", "conclusion": ""}]


def now():
    return dt.datetime.now().astimezone().isoformat(timespec="seconds")


class JudgeGate(DequeueBase):
    def setUp(self):
        super().setUp()
        self.env.seed_task("T-0002", pr_url=PR_URL, team="g-001-a")
        self.env.seed_pr(checks=GREEN)

    def queue_judge(self, name="zz-jd", n=1):
        self.env.set_herdr(fail=True)
        self.assertIn("QUEUED", self.ok("spawn", "judge", "--name", name, "--goal", "G-003", "--task", "T-0002").stdout)
        self.env.sql("UPDATE agents SET queued_at=? WHERE name=?", (at(n), name))
        self.env.set_herdr(fail=False)

    def set_checks(self, checks, merge_state=None):
        self.env.seed_pr(checks=checks)
        if merge_state:
            st = json.loads(self.env.gh_state.read_text())
            st["prs"][PR_URL]["mergeStateStatus"] = merge_state
            self.env.gh_state.write_text(json.dumps(st))
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")  # drop the 2-min cache

    def ci_views(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "view"] and "mergeStateStatus,statusCheckRollup" in c["argv"]]

    def seed_live_judges(self, n):
        for i in range(n):
            # started just now: the tick keeps a fresh live row that the pane list does not show (180 s grace)
            self.env.seed_agent(f"zz-livejd{i}", "judge", goal="G-003", status="live", started=now(), pane_id=f"p-zz{i}")

    def run_tick(self, agents=1):
        self.ok("config", "max_live_agents", str(agents))
        self.ok("tick")

    def test_pending_pr_judge_stays_queued_and_coder_starts(self):
        self.set_checks(PENDING)
        self.queue_judge()
        self.queue("zz-c", "coder", "G-003", at(2))
        self.run_tick()
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("queued", "live"))

    def test_green_pr_judge_starts_first(self):
        self.queue_judge()
        self.queue("zz-c", "coder", "G-001", at(1))
        self.run_tick()
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("live", "queued"))

    def test_pending_then_green_starts_judge_after_cache_expires(self):
        self.set_checks(PENDING)
        self.queue_judge()
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "queued")
        self.env.seed_pr(checks=GREEN)  # CI finished, but the not-ready answer is cached
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "queued")
        self.assertEqual(len(self.ci_views()), 1)
        self.env.sql("UPDATE config SET value=json_set(value,'$.at',0) WHERE key LIKE 'judge_ci:%'")
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "live")

    def test_cache_limits_gh_calls(self):
        self.set_checks(PENDING)
        self.queue_judge()
        self.run_tick()
        self.run_tick()
        self.assertEqual(len(self.ci_views()), 1)

    def test_behind_and_dirty_prs_are_not_ready(self):
        for state in ("BEHIND", "DIRTY"):
            with self.subTest(state):
                self.set_checks(GREEN, merge_state=state)
                self.env.sql("DELETE FROM agents WHERE name='zz-jd'")
                self.queue_judge()
                self.run_tick()
                self.assertEqual(self.status("zz-jd"), "queued")

    def test_failed_check_is_not_ready(self):
        self.set_checks([{"__typename": "CheckRun", "name": "unit", "status": "COMPLETED", "conclusion": "FAILURE"}])
        self.queue_judge()
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "queued")

    def test_project_required_checks_decide_readiness(self):
        self.ok("config", "required_checks", "unittest")
        self.set_checks(GREEN, merge_state="CLEAN")
        self.queue_judge()
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "queued")  # global gate: unittest missing
        self.env.seed_project("zz-dib")  # no goal links to it: found through the PR's repo
        self.env.sql("UPDATE projects SET required_checks='unit,lint', gh_repo='zz-test/repo' WHERE id='zz-dib'")
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "live")

    def test_clean_merge_state_is_ready(self):
        self.set_checks(GREEN, merge_state="CLEAN")
        self.queue_judge()
        self.run_tick()
        self.assertEqual(self.status("zz-jd"), "live")

    def test_gh_failure_is_not_ready_logs_once_and_does_not_crash(self):
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")
        st = json.loads(self.env.gh_state.read_text())
        st["prs"] = {}  # gh pr view exits 1
        self.env.gh_state.write_text(json.dumps(st))
        self.queue_judge()
        self.queue("zz-c", "coder", "G-003", at(2))
        self.run_tick()
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("queued", "live"))
        self.env.sql("DELETE FROM config WHERE key LIKE 'judge_ci:%'")
        self.run_tick()
        ev = self.env.sql("SELECT count(*) n FROM events WHERE action='judge-ci-check-failed'")[0]["n"]
        self.assertEqual(ev, 1)

    def test_cap_three_with_three_live_queues_fourth_and_coder_starts(self):
        self.seed_live_judges(3)
        self.queue_judge()
        self.queue("zz-c", "coder", "G-003", at(2))
        self.run_tick(agents=10)
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("queued", "live"))

    def test_starting_judges_count_toward_the_cap(self):
        self.seed_live_judges(2)
        self.env.seed_agent("zz-startjd", "judge", goal="G-003", status="starting")
        self.env.sql("UPDATE agents SET spawn_args=json_object('claimed_at', ?) WHERE name='zz-startjd'", (time.time(),))
        self.queue_judge()
        self.run_tick(agents=10)
        self.assertEqual(self.status("zz-jd"), "queued")

    def test_under_cap_judge_starts(self):
        self.seed_live_judges(2)
        self.queue_judge()
        self.run_tick(agents=10)
        self.assertEqual(self.status("zz-jd"), "live")

    def test_cap_zero_is_old_ordering(self):
        self.ok("config", "max_live_judges", "0")
        self.seed_live_judges(5)
        self.queue_judge()
        self.queue("zz-c", "coder", "G-001", at(1))
        self.run_tick(agents=6)  # five judges live, one slot left
        self.assertEqual((self.status("zz-jd"), self.status("zz-c")), ("live", "queued"))

    def test_invalid_cap_falls_back_to_three(self):
        for bad in ("abc", "-2", ""):
            with self.subTest(bad):
                self.ok("config", "max_live_judges", bad)
                self.env.sql("DELETE FROM agents WHERE role='judge'")
                self.seed_live_judges(3)
                self.queue_judge()
                self.run_tick(agents=10)
                self.assertEqual(self.status("zz-jd"), "queued")

    def test_direct_spawn_of_judge_with_pending_pr_queues_even_with_open_gate(self):
        self.set_checks(PENDING)
        out = self.ok("spawn", "judge", "--name", "zz-jd", "--goal", "G-003", "--task", "T-0002").stdout
        self.assertIn("QUEUED", out)
        self.assertEqual(self.status("zz-jd"), "queued")
        self.assertEqual(self.starts(), [])

    def test_direct_spawn_of_judge_with_green_pr_starts(self):
        self.ok("spawn", "judge", "--name", "zz-jd", "--goal", "G-003", "--task", "T-0002")
        self.assertEqual(self.status("zz-jd"), "live")


if __name__ == "__main__":
    unittest.main()
