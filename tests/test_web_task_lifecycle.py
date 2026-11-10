"""T-1238: web.queries.task_lifecycle() stages for one task, on fixture rows in a temp DB."""
import json
import sqlite3
import sys
import tempfile
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import NOW, make_db, ts  # noqa: E402
from web import queries as q  # noqa: E402

E = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
V = "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)"
A = "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,started,ended) VALUES(?,?,?,?,?,?,?,?,?)"
T = "INSERT INTO tasks(id,goal_id,title,role,status,created,updated) VALUES(?,?,?,?,?,?,?)"


class LifecycleCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.con = make_db(str(Path(self.tmp.name) / "f.db"), seed=False)
        self.con.row_factory = sqlite3.Row
        self.con.execute("INSERT INTO projects(id,name,created) VALUES('p1','P','x')")
        self.con.execute("INSERT INTO goals VALUES('G-1','p1','g',NULL,NULL,'active',1,'owner',NULL,?,?)", (ts(7, 0), ts(7, 0)))

    def task(self, tid="T-1", created=(8, 0), status="done", actor="director"):
        self.con.execute(T, (tid, "G-1", "t", "coder", status, ts(*created), ts(*created)))
        self.con.execute(E, (ts(*created), actor, "task", tid, "add", "x"))

    def basic(self, tid="T-1"):
        """created 08:00, spawn 08:10, PR 08:40, review 08:40, pass 09:00, merged 09:10."""
        x = self.con.execute
        x(A, ("coder-1", "coder", "G-1", None, tid, "done", "m", ts(8, 10), ts(8, 45)))
        x(E, (ts(8, 40), "coder-1", "task", tid, "update", "status=in_review pr=u"))
        x(V, (tid, "u", "aaaaaaaa1111", "reviewer", "pass", "ok", "r", ts(8, 55)))
        x(V, (tid, "u", "aaaaaaaa1111", "qa", "pass", "ok", "qa", ts(9, 0)))
        x(E, (ts(9, 10), "judge", "task", tid, "merged", "@abc"))

    def stage(self, r, name):
        return [s for s in r["stages"] if s["stage"] == name]

    def assert_partition(self, r):
        self.assertIsNotNone(r["gaps_s"])
        self.assertEqual(sum(s["seconds"] for s in r["stages"]) + r["gaps_s"], r["total"]["seconds"])


class TestLifecycle(LifecycleCase):
    def test_merged_first_try_no_spec(self):
        self.task()
        self.basic()
        r = q.task_lifecycle(self.con, "T-1", NOW)
        self.assertEqual([s["stage"] for s in r["stages"]], ["queued", "coding", "review", "judge"])
        self.assertEqual([s["seconds"] for s in r["stages"]], [600, 1800, 1200, 600])
        self.assertEqual(r["stages"][2]["head"], "aaaaaaaa")
        self.assertEqual(r["total"]["seconds"], 4200)
        self.assertFalse(r["total"]["open"])
        self.assertEqual(r["gaps_s"], 0)
        self.assert_partition(r)

    def test_qa_fail_then_pass_gives_two_rounds_and_a_gap(self):
        self.task()
        x = self.con.execute
        x(A, ("coder-1", "coder", "G-1", None, "T-1", "done", "m", ts(8, 10), ts(8, 45)))
        x(E, (ts(8, 40), "coder-1", "task", "T-1", "update", "status=in_review pr=u"))
        x(V, ("T-1", "u", "aaaaaaaa1111", "qa", "fail", "no", "qa", ts(8, 50)))
        x(E, (ts(9, 10), "coder-1", "task", "T-1", "update", "status=in_review"))
        x(V, ("T-1", "u", "bbbbbbbb2222", "qa", "pass", "ok", "qa", ts(9, 20)))
        x(E, (ts(9, 30), "judge", "task", "T-1", "merged", "@abc"))
        r = q.task_lifecycle(self.con, "T-1", NOW)
        rounds = self.stage(r, "review")
        self.assertEqual([(s["seconds"], s["head"]) for s in rounds], [(600, "aaaaaaaa"), (600, "bbbbbbbb")])
        self.assertEqual(self.stage(r, "judge")[0]["seconds"], 600)
        self.assertEqual(r["gaps_s"], 1200)  # 08:50 -> 09:10 rework
        self.assert_partition(r)

    def test_owner_blocked_span(self):
        self.task()
        self.basic()
        x = self.con.execute
        x(E, (ts(8, 15), "lead", "task", "T-1", "update", "status=blocked waits=owner"))
        x(E, (ts(8, 25), "lead", "task", "T-1", "update", "status=in_progress"))
        x(E, (ts(8, 30), "lead", "task", "T-1", "update", "status=blocked"))
        x(E, (ts(8, 32), "lead", "task", "T-1", "update", "status=in_progress"))
        r = q.task_lifecycle(self.con, "T-1", NOW)
        b = [w for w in r["waits"] if w["kind"] == "blocked"]
        self.assertEqual([(w["waits"], w["seconds"]) for w in b], [("owner", 600), ("internal", 120)])
        self.assertEqual(r["waits_total"]["owner_blocked_s"], 600)
        self.assertEqual(r["waits_total"]["internal_blocked_s"], 120)
        self.assertEqual(r["total"]["seconds"], 4200)  # waits stay inside the total
        self.assert_partition(r)

    def test_ci_wait_span_ends_at_next_autospawn_and_hourly_repeats_collapse(self):
        self.task()
        self.basic()
        x = self.con.execute
        x(E, (ts(8, 41), "tick", "task", "T-1", "review-autospawn-wait", json.dumps({"head": "aaaaaaaa", "kind": "ci"})))
        x(E, (ts(8, 46), "tick", "task", "T-1", "review-autospawn-wait", json.dumps({"head": "aaaaaaaa", "kind": "ci"})))
        x(E, (ts(8, 50), "tick", "task", "T-1", "review-autospawn", json.dumps({"agent": "r"})))
        r = q.task_lifecycle(self.con, "T-1", NOW)
        ci = [w for w in r["waits"] if w["kind"] == "ci"]
        self.assertEqual([w["seconds"] for w in ci], [540])
        self.assertEqual(r["waits_total"]["ci_s"], 540)
        self.assertEqual(r["total"]["seconds"], 4200)

    def test_ci_wait_without_following_autospawn_is_none(self):
        self.task()
        self.basic()
        self.con.execute(E, (ts(8, 41), "tick", "task", "T-1", "review-autospawn-wait", "{}"))
        ci = [w for w in q.task_lifecycle(self.con, "T-1", NOW)["waits"] if w["kind"] == "ci"]
        self.assertIsNone(ci[0]["seconds"])

    def test_pm_spec_task_adds_spec_stage_and_moves_total_start(self):
        x = self.con.execute
        x(T, ("T-0", "G-1", "spec", "pm", "done", ts(7, 30), ts(7, 50)))
        x(A, ("pm-0", "pm", "G-1", None, "T-0", "done", "m", ts(7, 31), ts(7, 50)))
        self.task(actor="pm-0")
        self.basic()
        r = q.task_lifecycle(self.con, "T-1", NOW)
        self.assertEqual([s["stage"] for s in r["stages"]][:2], ["spec", "queued"])
        self.assertEqual(self.stage(r, "spec")[0]["seconds"], 1800)
        self.assertEqual(r["total"]["start"], ts(7, 30))
        self.assertEqual(r["total"]["seconds"], 6000)
        self.assert_partition(r)

    def test_non_pm_add_actor_has_no_spec_stage(self):
        self.con.execute(T, ("T-0", "G-1", "x", "coder", "done", ts(7, 30), ts(7, 50)))
        self.con.execute(A, ("coder-0", "coder", "G-1", None, "T-0", "done", "m", ts(7, 31), ts(7, 50)))
        self.task(actor="coder-0")
        self.basic()
        r = q.task_lifecycle(self.con, "T-1", NOW)
        self.assertEqual(self.stage(r, "spec"), [])
        self.assertEqual(r["total"]["start"], ts(8, 0))

    def test_pm_actor_without_task_id_has_no_spec_stage(self):
        self.con.execute(A, ("pm-x", "pm", "G-1", None, None, "done", "m", ts(7, 31), ts(7, 50)))
        self.task(actor="pm-x")
        self.basic()
        self.assertEqual(self.stage(q.task_lifecycle(self.con, "T-1", NOW), "spec"), [])

    def test_out_of_order_timestamps_give_none(self):
        self.task()
        x = self.con.execute
        x(A, ("coder-1", "coder", "G-1", None, "T-1", "done", "m", ts(8, 10), None))
        x(E, (ts(8, 5), "coder-1", "task", "T-1", "update", "status=in_review pr=u"))  # PR before the spawn
        x(V, ("T-1", "u", "aaaaaaaa1111", "qa", "pass", "ok", "qa", ts(8, 30)))
        x(E, (ts(8, 20), "judge", "task", "T-1", "merged", "@abc"))  # merged before the pass
        r = q.task_lifecycle(self.con, "T-1", NOW)
        self.assertIsNone(self.stage(r, "coding")[0]["seconds"])
        self.assertIsNone(self.stage(r, "judge")[0]["seconds"])
        self.assertIsNone(r["gaps_s"])

    def test_open_task_totals_to_now_and_missing_inputs_are_none(self):
        self.task(status="in_progress")
        r = q.task_lifecycle(self.con, "T-1", NOW)
        self.assertTrue(r["total"]["open"])
        self.assertEqual(r["total"]["seconds"], 4 * 3600)
        self.assertEqual([s["seconds"] for s in r["stages"]], [None, None, None])
        self.assertIsNone(r["gaps_s"])

    def test_unknown_task_is_none(self):
        self.assertIsNone(q.task_lifecycle(self.con, "T-404", NOW))


if __name__ == "__main__":
    unittest.main()
