"""G-015 T-1110: owner_attention(con), the one narrow "needs you" selector (D7)."""
import json
import os
import sys
import unittest
from pathlib import Path
from unittest import mock

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_util_alerts import AlertCase  # noqa: E402

TMP_FULL = "10485760000,1048576000"  # 90% used
MARK = "SECRETMARKER"
KEYS = {"kind", "org", "ref", "since", "link"}


class OwnerAttention(AlertCase):
    def goal(self, gid, status="active", org="claude", updated="2026-11-01T10:00:00+01:00", title="g"):
        self.con.execute("INSERT INTO goals(id,title,status,created,updated,org) VALUES(?,?,?,?,?,?)",
                         (gid, title, status, "t", updated, org))

    def task(self, tid, goal="G-1", status="todo", waits="internal", title="t", updated="2026-11-01T11:00:00+01:00"):
        self.con.execute("INSERT INTO tasks(id,goal_id,title,status,waits,created,updated) VALUES(?,?,?,?,?,?,?)",
                         (tid, goal, title, status, waits, "t", updated))

    def ev(self, tid, action, detail, ts):
        self.con.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
                         (ts, "x", "task", tid, action, detail))

    def msg(self, subject, sender="lead-x", to="boss", kind="escalation", goal=None, task=None, body="b",
            created="2026-11-02T09:00:00+01:00"):
        return self.con.execute("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created) "
                                "VALUES(?,?,?,?,?,?,?,?)", (sender, to, kind, subject, body, goal, task, created)).lastrowid

    def sel(self, tmp=None):
        env = {"FACTORY_NOW": "2026-11-05T12:00:00"}
        if tmp:
            env["FACTORY_TMP_STATVFS"] = tmp
        with mock.patch.dict(os.environ, env):
            return self.mod.owner_attention(self.con)

    def setUp(self):
        super().setUp()
        self.goal("G-1")

    def test_task_owner_wait_listed_with_event_since(self):
        self.task("T-1", status="blocked", waits="owner")
        self.ev("T-1", "update", "status=blocked waits=owner — x", "2026-11-03T08:00:00+01:00")
        self.assertEqual(self.sel(), [{"kind": "task-owner-wait", "org": "claude", "ref": "T-1",
                                       "since": "2026-11-03T08:00:00+01:00", "link": "/task/T-1"}])

    def test_task_owner_wait_falls_back_to_updated(self):
        self.task("T-1", status="blocked", waits="owner", updated="2026-11-04T01:00:00+01:00")
        self.assertEqual(self.sel()[0]["since"], "2026-11-04T01:00:00+01:00")

    def test_judge_hold_listed_and_done_cancelled_not(self):
        self.task("T-2", status="in_review")
        self.ev("T-2", "judge-blocked", "auto_merge=0 hold", "2026-11-03T09:00:00+01:00")
        self.assertEqual(self.sel(), [{"kind": "judge-hold", "org": "claude", "ref": "T-2",
                                       "since": "2026-11-03T09:00:00+01:00", "link": "/task/T-2"}])
        for st in ("done", "cancelled"):
            self.con.execute("UPDATE tasks SET status=? WHERE id='T-2'", (st,))
            self.assertEqual(self.sel(), [], st)

    def test_judge_hold_only_latest_event_counts(self):
        self.task("T-2", status="in_review")
        self.ev("T-2", "judge-blocked", "auto_merge=0", "2026-11-03T09:00:00+01:00")
        self.ev("T-2", "judge-blocked", "ci red", "2026-11-03T10:00:00+01:00")
        self.assertEqual(self.sel(), [])

    def test_escalation_open_answered_and_tick(self):
        a = self.msg("OWNER ACTION: approve", goal="G-1")
        self.msg("owner action: from tick", sender="tick")
        row = self.sel()
        self.assertEqual(row, [{"kind": "escalation", "org": "claude", "ref": f"M{a}",
                                "since": "2026-11-02T09:00:00+01:00", "link": "/goal/G-1"}])
        self.msg("decision", sender="boss", to="owner", kind="decision", body=f"owner said yes to M{a}")
        self.assertEqual(self.sel(), [])

    def test_escalation_without_goal_links_root(self):
        self.msg("owner action: x")
        self.assertEqual(self.sel()[0]["link"], "/")

    def test_tmp_gate_org_all(self):
        rows = self.sel(tmp=TMP_FULL)
        self.assertEqual([(r["kind"], r["org"], r["ref"], r["link"]) for r in rows], [("tmp-gate", "all", "tmp", "/resources")])
        self.assertTrue(rows[0]["since"])

    def test_goal_blocked(self):
        self.goal("G-2", status="blocked", updated="2026-11-04T07:00:00+01:00")
        self.assertEqual(self.sel(), [{"kind": "goal-blocked", "org": "claude", "ref": "G-2",
                                       "since": "2026-11-04T07:00:00+01:00", "link": "/goal/G-2"}])

    def test_too_wide_is_refused(self):
        self.msg("please look", kind="request")
        self.msg("fyi escalation", kind="escalation")
        self.goal("G-3", status="proposed")
        self.task("T-3", status="blocked", waits="internal")
        self.assertEqual(self.sel(), [])

    def test_row_shape_is_exactly_five_keys(self):
        self.task("T-1", status="blocked", waits="owner")
        self.task("T-2", status="in_review")
        self.ev("T-2", "judge-blocked", "auto_merge=0", "2026-11-03T09:00:00+01:00")
        self.msg("owner action: x", goal="G-1")
        self.goal("G-2", status="blocked")
        rows = self.sel(tmp=TMP_FULL)
        self.assertEqual(len(rows), 5)
        for r in rows:
            self.assertEqual(set(r), KEYS)

    def test_no_text_leak(self):
        self.goal("G-2", status="blocked", title=MARK)
        self.task("T-1", goal="G-2", status="blocked", waits="owner", title=MARK)
        self.task("T-2", status="in_review", title=MARK)
        self.ev("T-2", "judge-blocked", "auto_merge=0", "2026-11-03T09:00:00+01:00")
        self.msg(f"owner action {MARK}", body=MARK, goal="G-1")
        out = json.dumps(self.sel())
        self.assertIn("goal-blocked", out)
        self.assertIn("escalation", out)
        self.assertEqual(out.count(MARK), 0)

    def test_org_labels(self):
        self.goal("G-C", status="blocked", org="cursor")
        self.task("T-C", goal="G-C", status="blocked", waits="owner")
        self.msg("owner action: nogoal", to="cursor-boss")
        self.con.execute("INSERT OR REPLACE INTO agents(name,role,status) VALUES('cursor-boss','boss','live')")
        self.goal("G-K", status="blocked", org="claude")
        by = {(r["kind"], r["ref"]): r["org"] for r in self.sel(tmp=TMP_FULL)}
        self.assertEqual(by[("goal-blocked", "G-C")], "cursor")
        self.assertEqual(by[("goal-blocked", "G-K")], "claude")
        self.assertEqual(by[("task-owner-wait", "T-C")], "cursor")
        self.assertEqual([v for k, v in by.items() if k[0] == "escalation"], ["cursor"])
        self.assertEqual([k for k, v in by.items() if v == "all"], [("tmp-gate", "tmp")])


if __name__ == "__main__":
    unittest.main()
