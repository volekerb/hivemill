"""Lifecycle drift (G-013 F2): the hourly tick alert and the digest Drift line. See docs/lifecycle.md."""
import datetime as dt
import json
import os
import stat
import time
import unittest

from harness import FactoryTestCase

URL_88 = "https://github.com/zz-test/repo/pull/88"

# Own gh stub (tests/stubs/gh stays untouched): `gh pr view <url> --json state` answers from drift-gh.json
# ({"states": {url: state}, "fail": bool}) and logs every call to drift-gh-calls.log.
GH_STUB = """#!/usr/bin/env python3
import json, os, sys, time
d = os.path.dirname(os.path.abspath(__file__))
open(os.path.join(d, "drift-gh-calls.log"), "a").write(" ".join(sys.argv[1:]) + "\\n")
st = json.load(open(os.path.join(d, "drift-gh.json")))
time.sleep(st.get("sleep", 0))
if st.get("fail"):
    print("gh: network down", file=sys.stderr)
    sys.exit(1)
if sys.argv[1:3] == ["pr", "view"] and sys.argv[3] in st["states"]:
    print(json.dumps({"state": st["states"][sys.argv[3]]}))
    sys.exit(0)
print("gh: no pull request found", file=sys.stderr)
sys.exit(1)
"""


def ago(**kw):
    return (dt.datetime.now().astimezone() - dt.timedelta(**kw)).isoformat(timespec="seconds")


class DriftBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.gh_dir = e.home / "driftgh"
        self.gh_dir.mkdir()
        gh = self.gh_dir / "gh"
        gh.write_text(GH_STUB)
        gh.chmod(gh.stat().st_mode | stat.S_IXUSR)
        self.set_gh_states({})
        e.seed_goal("G-001")
        e.seed_team("g-001-a", "G-001", lead="zz-test-lead")
        e.seed_agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        e.seed_agent("zz-test-director", "director")
        self.panes = []
        self.register("zz-test-lead", "zz-test-director")

    def register(self, *names):
        """Make the pane server report these agents, so the tick's reap (which runs before the drift check) keeps them live."""
        self.panes += [{"name": n, "pane_id": f"p-{n}", "agent_status": "idle", "agent_session": {"value": f"s-{n}"}} for n in names]
        self.env.set_herdr(agents=self.panes)
        for n in names:
            self.env.sql("UPDATE agents SET pane_id=? WHERE name=?", (f"p-{n}", n))

    def set_gh_states(self, states, fail=False, sleep=0):
        (self.gh_dir / "drift-gh.json").write_text(json.dumps({"states": states, "fail": fail, "sleep": sleep}))

    def gh_calls(self):
        f = self.gh_dir / "drift-gh-calls.log"
        return f.read_text().splitlines() if f.exists() else []

    def task(self, tid, status, updated, goal="G-001", pr_url=None, deps="[]", assignee=None):
        self.env.seed_task(tid, goal=goal, status=status, pr_url=pr_url, team="g-001-a")
        self.env.sql("UPDATE tasks SET updated=?, depends_on=?, assignee=? WHERE id=?", (updated, deps, assignee, tid))

    def tick(self, extra_env=None):
        path = f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}"  # the drift gh stub shadows tests/stubs/gh
        return self.ok("tick", extra_env={"PATH": path, **(extra_env or {})})

    def drift_msgs(self):
        return [dict(r) for r in self.env.sql("SELECT recipient, subject, body, goal_id FROM messages WHERE subject LIKE 'drift %' ORDER BY id")]

    def reopen_throttle(self):
        self.env.sql("DELETE FROM throttle WHERE key='drift'")

    def digest(self, *args):
        path = f"{self.gh_dir}{os.pathsep}{self.env.env()['PATH']}"
        return self.ok("digest", *args, extra_env={"PATH": path}).stdout


class DriftRulesTests(DriftBase):
    def test_d1_planning_goal_with_done_task_flagged(self):
        self.env.sql("UPDATE goals SET status='planning'")
        self.env.sql("DELETE FROM teams")
        self.task("T-0001", "done", ago(hours=1))
        self.tick()
        (m,) = self.drift_msgs()
        self.assertIn("D1 G-001 is planning but has done tasks", m["body"])

    def test_d1_proposed_goal_with_live_team_flagged(self):
        self.env.sql("UPDATE goals SET status='proposed'")
        self.tick()
        (m,) = self.drift_msgs()
        self.assertIn("D1 G-001 is proposed but has live team g-001-a", m["body"])

    def test_d1_not_flagged_for_empty_planning_goal_or_active_goal_with_done_task(self):
        self.env.sql("UPDATE goals SET status='planning'")
        self.env.sql("DELETE FROM teams")
        self.env.seed_goal("G-002")
        self.task("T-0002", "done", ago(hours=1), goal="G-002")
        self.tick()
        self.assertEqual(self.drift_msgs(), [])

    def test_d2_in_review_task_with_merged_or_closed_pr_flagged(self):
        self.task("T-0001", "in_review", ago(minutes=5), pr_url=URL_88)
        self.task("T-0002", "in_review", ago(minutes=5), pr_url="https://github.com/zz-test/repo/pull/89")
        self.set_gh_states({URL_88: "MERGED", "https://github.com/zz-test/repo/pull/89": "CLOSED"})
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual(m["body"].splitlines(), ["D2 T-0001 in_review but PR #88 MERGED", "D2 T-0002 in_review but PR #89 CLOSED"])
        self.assertEqual(m["subject"], "drift G-001: 2 items")

    def test_d2_open_pr_not_flagged(self):
        self.task("T-0001", "in_review", ago(minutes=5), pr_url=URL_88)
        self.set_gh_states({URL_88: "OPEN"})
        self.tick()
        self.assertEqual(self.drift_msgs(), [])
        self.assertEqual(len(self.gh_calls()), 1)  # it did ask gh

    def test_d3_task_without_agent_and_activity_flagged(self):
        self.task("T-0001", "in_progress", ago(hours=3))
        self.task("T-0002", "todo", ago(hours=3))
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual([l.split(" ")[:3] for l in m["body"].splitlines()], [["D3", "T-0001", "in_progress"], ["D3", "T-0002", "todo"]])

    def test_d3_todo_task_whose_dependency_is_cancelled_flagged(self):
        self.task("T-0009", "cancelled", ago(hours=5))
        self.task("T-0001", "todo", ago(hours=3), deps='["T-0009"]')
        self.task("T-0002", "todo", ago(hours=3), deps='["T-0009", "T-0008"]')
        self.task("T-0008", "in_progress", ago(minutes=5))
        self.task("T-0003", "todo", ago(minutes=30), deps='["T-0009"]')  # D3 stays an idle rule
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual(m["body"].splitlines(), ["D3 T-0001 todo but its dependency T-0009 is cancelled",
                                                  "D3 T-0002 todo but its dependency T-0009 is cancelled"])

    def test_d3_not_flagged_with_agent_recent_activity_or_unmet_dependency(self):
        self.task("T-0001", "in_progress", ago(hours=3))
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")  # live agent
        self.register("zz-test-coder")
        self.task("T-0002", "in_progress", ago(hours=1))                                           # recent update
        self.task("T-0003", "todo", ago(hours=3), deps='["T-0009"]')                               # waits for an open task
        self.task("T-0009", "in_progress", ago(minutes=5))
        self.task("T-0004", "in_progress", ago(hours=3))                                           # a recent task event counts
        self.env.sql("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                     (ago(minutes=20), "zz-test-coder", "task", "T-0004", "note", "working"))
        self.tick()
        self.assertEqual(self.drift_msgs(), [])

    def test_d4_task_blocked_over_24h_flagged(self):
        self.task("T-0001", "blocked", ago(hours=25))
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual(m["body"], "D4 T-0001 blocked for over 24 h")

    def test_d4_task_blocked_under_24h_not_flagged(self):
        self.task("T-0001", "blocked", ago(hours=23))
        self.tick()
        self.assertEqual(self.drift_msgs(), [])


class DriftDeliveryTests(DriftBase):
    def test_one_message_per_goal_to_the_live_lead(self):
        self.task("T-0001", "blocked", ago(hours=30))
        self.task("T-0002", "in_progress", ago(hours=4))
        self.env.seed_goal("G-002")
        self.env.seed_team("g-002-a", "G-002", lead="zz-test-lead2")
        self.env.seed_agent("zz-test-lead2", "lead", goal="G-002", team="g-002-a")
        self.register("zz-test-lead2")
        self.env.seed_task("T-0003", goal="G-002", status="blocked", pr_url=None, team="g-002-a")
        self.env.sql("UPDATE tasks SET updated=? WHERE id='T-0003'", (ago(hours=40),))
        self.tick()
        msgs = {m["goal_id"]: m for m in self.drift_msgs()}
        self.assertEqual(sorted(msgs), ["G-001", "G-002"])
        self.assertEqual(msgs["G-001"]["recipient"], "zz-test-lead")
        self.assertEqual(len(msgs["G-001"]["body"].splitlines()), 2)
        self.assertEqual(msgs["G-002"]["recipient"], "zz-test-lead2")
        ev = self.env.sql("SELECT entity_id FROM events WHERE action='drift' ORDER BY entity_id")
        self.assertEqual([r[0] for r in ev], ["G-001", "G-002"])

    def test_no_live_lead_goes_to_the_director(self):
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-lead'")
        self.task("T-0001", "blocked", ago(hours=30))
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual(m["recipient"], "zz-test-director")

    def test_no_drift_sends_nothing_and_tick_exits_0(self):
        self.task("T-0001", "in_progress", ago(minutes=10))
        p = self.tick()
        self.assertEqual(p.returncode, 0)
        self.assertEqual(self.drift_msgs(), [])
        self.assertEqual(self.env.sql("SELECT count(*) FROM events WHERE action='drift'")[0][0], 0)

    def test_throttle_second_tick_within_an_hour_sends_nothing(self):
        self.task("T-0001", "blocked", ago(hours=30))
        self.tick()
        self.assertEqual(len(self.drift_msgs()), 1)
        self.env.sql("DELETE FROM messages")
        self.env.sql("DELETE FROM events WHERE action='drift'")  # only the throttle can hold the next send back
        self.tick()
        self.assertEqual(self.drift_msgs(), [])
        self.env.sql("UPDATE throttle SET ts=? WHERE key='drift'", (str(time.time() - 3601),))
        self.tick()
        self.assertEqual(len(self.drift_msgs()), 1)  # an hour later it runs again

    def test_dedupe_same_item_set_not_resent_within_24h(self):
        self.task("T-0001", "blocked", ago(hours=30))
        self.tick()
        self.reopen_throttle()
        self.tick()
        self.assertEqual(len(self.drift_msgs()), 1)
        self.task("T-0002", "blocked", ago(hours=30))  # a different item set is new information
        self.reopen_throttle()
        self.tick()
        self.assertEqual(len(self.drift_msgs()), 2)
        self.env.sql("UPDATE events SET ts=? WHERE action='drift'", (ago(hours=25),))  # the window passed
        self.reopen_throttle()
        self.tick()
        self.assertEqual(len(self.drift_msgs()), 3)

    def test_gh_error_marks_d2_unchecked_and_tick_exits_0(self):
        self.task("T-0001", "in_review", ago(minutes=5), pr_url=URL_88)
        self.task("T-0002", "blocked", ago(hours=30))
        self.set_gh_states({}, fail=True)
        p = self.tick()
        self.assertEqual(p.returncode, 0)
        (m,) = self.drift_msgs()
        self.assertIn("D4 T-0002 blocked for over 24 h", m["body"])
        self.assertIn("D2 unchecked T-0001", m["body"])
        self.assertEqual(m["subject"], "drift G-001: 1 items")  # unchecked is a note, not an item

    def test_gh_time_budget_bounds_the_run_and_reports_the_rest_unchecked(self):
        urls = [f"https://github.com/zz-test/repo/pull/{n}" for n in (11, 12, 13, 14)]
        for i, url in enumerate(urls, start=1):
            self.task(f"T-000{i}", "in_review", ago(minutes=5), pr_url=url)
        self.task("T-0009", "blocked", ago(hours=30))  # a real item, so the message (with its unchecked lines) is sent
        self.set_gh_states({u: "OPEN" for u in urls}, sleep=0.2)  # every gh call takes 0.2 s
        p = self.tick({"FACTORY_DRIFT_GH_BUDGET_S": "0.3"})
        self.assertEqual(p.returncode, 0)
        self.assertLessEqual(len(self.gh_calls()), 2)  # the 1st answers, the 2nd is cut at what is left, the rest are not asked
        (m,) = self.drift_msgs()
        lines = m["body"].splitlines()
        self.assertEqual(lines[0], "D4 T-0009 blocked for over 24 h")
        self.assertEqual([l.split(" ")[:3] for l in lines[1:]], [["D2", "unchecked", f"T-000{i}"] for i in (2, 3, 4)])
        self.assertIn("gh time budget of 0.3s used up", lines[-1])

    def test_invalid_gh_budget_falls_back_to_the_default_and_drift_still_runs(self):
        url = "https://github.com/zz-test/repo/pull/11"
        self.task("T-0001", "in_review", ago(minutes=5), pr_url=url)
        self.task("T-0009", "blocked", ago(hours=30))
        self.set_gh_states({url: "OPEN"})
        p = self.tick({"FACTORY_DRIFT_GH_BUDGET_S": "soon"})
        self.assertEqual(p.returncode, 0)
        self.assertEqual(len(self.gh_calls()), 1)  # the default budget applies: the PR is still asked
        (m,) = self.drift_msgs()
        self.assertEqual(m["body"], "D4 T-0009 blocked for over 24 h")

    def test_drift_runs_after_the_reap_so_a_just_reaped_agent_gives_no_d3(self):
        self.task("T-0001", "in_progress", ago(hours=3))
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001", status="done", started=ago(hours=3))
        self.tick()
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='zz-test-coder'")[0]["status"], "exited")
        self.assertEqual(self.drift_msgs(), [])  # the reap stamps the agent's end: the task has just seen activity

    def test_drift_still_runs_when_the_pane_server_is_unreachable(self):
        self.task("T-0001", "blocked", ago(hours=30))
        self.env.set_herdr(fail=True)
        self.tick()
        (m,) = self.drift_msgs()
        self.assertEqual(m["body"], "D4 T-0001 blocked for over 24 h")


class DriftDigestTests(DriftBase):
    def test_digest_drift_none(self):
        self.task("T-0001", "in_progress", ago(minutes=10))
        self.assertIn("\nDrift: none\n", self.digest())

    def test_digest_lists_drift_items_without_calling_gh(self):
        self.task("T-0001", "blocked", ago(hours=30))
        self.task("T-0002", "in_review", ago(minutes=5), pr_url=URL_88)
        self.set_gh_states({URL_88: "MERGED"})
        self.tick()  # records the PR states
        self.set_gh_states({}, fail=True)
        before = len(self.gh_calls())
        out = self.digest()
        self.assertIn("Drift: D2 T-0002 in_review but PR #88 MERGED; D4 T-0001 blocked for over 24 h", out)
        self.assertEqual(len(self.gh_calls()), before)  # the digest reused the tick's result

    def test_digest_before_any_tick_does_not_guess_d2(self):
        self.task("T-0002", "in_review", ago(minutes=5), pr_url=URL_88)
        self.assertIn("\nDrift: none\n", self.digest())


if __name__ == "__main__":
    unittest.main()
