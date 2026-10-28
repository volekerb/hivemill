import unittest

from harness import FactoryTestCase, gates_summary


def run(name, concl, started=None, status="COMPLETED", wf="tests", **kw):
    c = {"__typename": "CheckRun", "name": name, "workflowName": wf, "status": status, "conclusion": concl}
    if started:
        c["startedAt"] = started
    c.update(kw)
    return c


T1, T2, T3 = "2026-10-24T10:00:00Z", "2026-10-24T10:05:00Z", "2026-10-24T10:10:00Z"


class LatestChecksPureTests(FactoryTestCase):
    def bad(self, rollup):
        with self.env.load_factory() as mod:
            return mod.checks_ok({"statusCheckRollup": rollup})

    def test_cancelled_then_success_is_green(self):
        self.assertEqual(self.bad([run("unittest", "CANCELLED", T1), run("unittest", "SUCCESS", T2)]), [])

    def test_order_comes_from_timestamp_not_list_position(self):
        self.assertEqual(self.bad([run("unittest", "SUCCESS", T2), run("unittest", "CANCELLED", T1)]), [])

    def test_success_then_cancelled_is_bad(self):
        self.assertEqual(self.bad([run("unittest", "SUCCESS", T1), run("unittest", "CANCELLED", T2)]),
                         ["unittest=CANCELLED"])

    def test_failure_latest_is_bad(self):
        self.assertEqual(self.bad([run("unittest", "FAILURE", T1)]), ["unittest=FAILURE"])

    def test_failure_then_success_is_green(self):
        self.assertEqual(self.bad([run("unittest", "FAILURE", T1), run("unittest", "SUCCESS", T2)]), [])

    def test_each_name_judged_on_its_own_latest(self):
        self.assertEqual(self.bad([run("unittest", "CANCELLED", T1), run("unittest", "SUCCESS", T2),
                                   run("lint", "FAILURE", T1), run("lint", "SUCCESS", T3)]), [])

    def test_in_progress_latest_is_bad(self):
        got = self.bad([run("unittest", "SUCCESS", T1), run("lint", "SUCCESS", T1),
                        run("lint", None, T2, status="IN_PROGRESS")])
        self.assertEqual(got, ["lint=IN_PROGRESS"])

    def test_no_timestamps_list_order_decides(self):
        self.assertEqual(self.bad([run("unittest", "CANCELLED"), run("unittest", "SUCCESS")]), [])
        self.assertEqual(self.bad([run("unittest", "SUCCESS"), run("unittest", "CANCELLED")]), ["unittest=CANCELLED"])

    def test_missing_timestamp_sorts_oldest(self):
        self.assertEqual(self.bad([run("unittest", "SUCCESS", T1), run("unittest", "CANCELLED")]), [])

    def test_fallback_to_completed_then_created(self):
        a = run("unittest", "CANCELLED", completedAt=T1)
        b = run("unittest", "SUCCESS", completedAt=T2)
        self.assertEqual(self.bad([b, a]), [])
        s = {"__typename": "StatusContext", "context": "ci/x", "state": "FAILURE", "createdAt": T1}
        s2 = {"__typename": "StatusContext", "context": "ci/x", "state": "SUCCESS", "createdAt": T2}
        self.assertEqual(self.bad([s2, s]), [])

    def test_same_name_in_different_workflows_not_merged(self):
        self.assertEqual(self.bad([run("build", "FAILURE", T1, wf="a"), run("build", "SUCCESS", T2, wf="b")]),
                         ["build=FAILURE"])

    def test_status_context_latest_only(self):
        self.assertEqual(self.bad([{"context": "ci/x", "state": "FAILURE"}, {"context": "ci/x", "state": "SUCCESS"}]), [])
        self.assertEqual(self.bad([{"context": "ci/x", "state": "SUCCESS"}, {"context": "ci/x", "state": "FAILURE"}]),
                         ["ci/x=FAILURE"])

    def test_latest_checks_empty(self):
        with self.env.load_factory() as mod:
            self.assertEqual(mod.latest_checks(None), [])
            self.assertEqual(mod.latest_checks([]), [])


class LatestChecksJudgeTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj")
        e.seed_task()
        e.seed_agent("zz-test-reviewer", "reviewer", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-qa", "qa", goal="G-001", task="T-0001")
        e.seed_agent("zz-test-judge", "judge", goal="G-001", task="T-0001")
        e.use_git_head()
        e.seed_pr(checks=[])
        self.ok("config", "judge_ci_wait_s", "0")  # these tests assert the immediate refusal; test_judge_wait.py covers the wait
        self.ok("verdict", "T-0001", "--pass", "--summary", "ok", agent="zz-test-reviewer")
        self.ok("verdict", "T-0001", "--pass", "--summary", gates_summary(), agent="zz-test-qa")

    def judge(self):
        return self.env.run("judge", "T-0001", "--merge", "--reason", "zz-test", agent="zz-test-judge")

    def merges(self):
        return [c for c in self.env.calls("gh") if c["argv"][:2] == ["pr", "merge"]]

    def test_superseded_cancelled_run_does_not_block_merge(self):
        self.env.seed_pr(checks=[run("unittest", "CANCELLED", T1), run("unittest", "SUCCESS", T2)])
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertEqual(len(self.merges()), 1)

    def test_newer_cancelled_run_refused(self):
        self.env.seed_pr(checks=[run("unittest", "SUCCESS", T1), run("unittest", "CANCELLED", T2)])
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("unittest=CANCELLED", p.stderr)
        self.assertEqual(self.merges(), [])

    def test_latest_failure_refused(self):
        self.env.seed_pr(checks=[run("unittest", "SUCCESS", T1), run("unittest", "FAILURE", T2)])
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("unittest=FAILURE", p.stderr)

    def test_failure_then_success_merges(self):
        self.env.seed_pr(checks=[run("unittest", "FAILURE", T1), run("unittest", "SUCCESS", T2)])
        p = self.judge()
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)

    def test_in_progress_latest_refused(self):
        self.env.seed_pr(checks=[run("unittest", "SUCCESS", T1), run("lint", None, T2, status="IN_PROGRESS")])
        p = self.judge()
        self.assertEqual(p.returncode, 4, p.stdout + p.stderr)
        self.assertIn("lint=IN_PROGRESS", p.stderr)


if __name__ == "__main__":
    unittest.main()
