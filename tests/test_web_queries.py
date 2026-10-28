"""Tests for web/db.py and web/queries.py against a WAL-mode fixture DB."""
import datetime as dt
import json
import os
import sqlite3
import sys
import tempfile
import threading
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from web import db as wdb  # noqa: E402
from web import queries as q  # noqa: E402

SCHEMA = (ROOT / "tests" / "fixtures" / "web" / "schema.sql").read_text()
CANARY = "SECRET-CANARY-A"


def ts(h, m, s=0, day=24):
    """Local-time ISO string on 2026-10-<day>."""
    return dt.datetime(2026, 10, day, h, m, s).astimezone().isoformat(timespec="seconds")


NOW = dt.datetime(2026, 10, 24, 12, 0).astimezone()


def make_db(path, schema=SCHEMA, seed=True):
    con = sqlite3.connect(path, isolation_level=None)
    con.execute("PRAGMA journal_mode=WAL")
    con.executescript(schema)
    if seed:
        seed_data(con)
    return con


def seed_data(c):
    x = c.execute
    x("INSERT INTO config VALUES('max_agents','6')")
    x("INSERT INTO config VALUES('judge_mode','auto')")
    x("INSERT INTO projects(id,name,created) VALUES('p1','P','x')")
    long_intent = "I" * 1000
    crit = "1. first\n   continued line\n2) second\n3. third\n4. fourth untagged"
    x("INSERT INTO goals VALUES('G-1','p1','Goal one',?,?,'active',1,'owner',NULL,?,?)", (long_intent, crit, ts(8, 0), ts(9, 0)))
    x("INSERT INTO goals VALUES('G-2','p1','Goal two',NULL,NULL,'done',2,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
    x("INSERT INTO goals VALUES('G-3','p1','Goal three',NULL,'only line one\nline two','proposed',2,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
    x("INSERT INTO goals VALUES('G-4','p1','Goal four',NULL,NULL,'blocked',3,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
    x("INSERT INTO teams VALUES('t-a','G-1','scope A','lead-a',NULL,'active',?)", (ts(8, 0),))
    x("INSERT INTO teams VALUES('t-b','G-1','scope B','lead-b',NULL,'closed',?)", (ts(8, 0),))
    T = "INSERT INTO tasks(id,goal_id,team_id,title,spec,acceptance,role,status,assignee,depends_on,pr_url,attempts,created,updated,follow_up_of) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)"
    sp = f"spec {CANARY}"
    rows = [
        ("T-1", "G-1", "t-a", "[SC1] one", "coder", "done", "coder-1", '[]', "https://github.com/o/r/pull/1", 2, None),
        ("T-2", "G-1", "t-a", "[SC2] two", "coder", "in_progress", "coder-2", '["T-1"]', None, 1, None),
        ("T-3", "G-1", "t-a", "[SC2,SC3] three", "coder", "in_review", "coder-3", '["T-1", "T-2"]', "https://github.com/o/r/pull/3", 3, "T-1"),
        ("T-4", "G-1", "t-a", "untagged", "pm", "blocked", None, 'not json', None, 0, None),
        ("T-5", "G-2", None, "[SC1] ship it", "coder", "done", "coder-5", "", "https://github.com/o/r/pull/5", 1, None),
        ("T-6", "G-1", "t-b", "[SC4] cancelled", "coder", "cancelled", None, '[]', None, 0, None),
    ]
    for i, g, tm, title, role, st, asg, dep, pr, att, fu in rows:
        x(T, (i, g, tm, title, sp, f"acc {CANARY}", role, st, asg, dep, pr, att, ts(8, 0), ts(9, 30), fu))
    A = "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,pane_id,model,session_id,spawn_args,queued_at,started,ended) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)"
    sa = f"kickoff {CANARY}"
    for a in [
        ("coder-t-1", "coder", "G-1", "t-a", "T-1", "done", None, "m", "s1", sa, None, ts(10, 0), ts(10, 35)),
        ("reviewer-t-1", "reviewer", "G-1", "t-a", "T-1", "done", None, "m", None, sa, None, ts(10, 40), ts(10, 46)),
        ("lead-a", "lead", "G-1", "t-a", None, "live", "p1", "opus", "s2", sa, None, ts(8, 0), None),
        ("coder-2", "coder", "G-1", "t-a", "T-2", "live", "p2", "sonnet", None, sa, None, ts(11, 0), None),
        ("queued-x", "qa", "G-1", "t-a", "T-3", "queued", None, None, None, sa, ts(11, 30), None, None),
    ]:
        x(A, a)
    E = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
    for e in [
        (ts(10, 30), "coder-t-1", "task", "T-1", "update", "status=in_review pr=https://github.com/o/r/pull/1"),
        (ts(10, 45), "reviewer-t-1", "task", "T-1", "reviewer-fail", "@abcdef12"),
        (ts(10, 50), "lead-a", "task", "T-1", "update", "status=in_progress"),
        (ts(11, 0), "coder-t-1", "task", "T-1", "update", "status=in_review"),
        (ts(11, 5), "judge", "task", "T-1", "judge-blocked", "checks not green: unittest"),
        (ts(11, 20), "judge", "task", "T-1", "merged", "@deadbeef"),
        (ts(11, 25), "judge", "task", "T-1", "note", None),
        (ts(11, 30), "judge", "task", "T-5", "merged", "@cafebabe"),
        (ts(11, 31), "judge", "task", "T-3", "judge-blocked", "checks not green: unittest"),
        (ts(11, 32), "judge", "task", "T-3", "judge-blocked", "project has auto_merge=0 — boss must get the owner's OK"),
        (ts(11, 33), "judge", "task", "T-5", "judge-blocked", "project has auto_merge=0 — boss must get the owner's OK"),
        (ts(11, 34), "judge", "task", "T-3", "judge-reject", "bad"),
        (ts(11, 35), "judge", "task", "T-2", "judge-reject", "old style, no verdict row"),
        (ts(11, 36), "lead", "task", "T-3", "reopen", "again"),
        (ts(11, 30), "factory", "agent", "queued-x", "queued", "gate closed: 6/6 panes"),
        (ts(11, 40), "factory", "agent", "queued-x", "queued", "gate closed: low RAM"),
        (ts(9, 0), "director", "config", "max_agents", "set", "6"),
    ]:
        x(E, e)
    V = "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)"
    for v in [
        ("T-1", "u", "abcdef1234", "reviewer", "fail", "needs work " + "w" * 400, "reviewer-t-1", ts(10, 45)),
        ("T-1", "u", "bbbbbbbb99", "reviewer", "pass", "ok", "reviewer-t-1", ts(11, 10)),
        ("T-1", "u", "bbbbbbbb99", "qa", "pass", "ok", "qa-t-1", ts(11, 15)),
        ("T-1", "u", "bbbbbbbb99", "judge", "merge", "merged", "judge", ts(11, 20)),
        ("T-3", "u", "cccccccc11", "judge", "reject", "bad", "judge", ts(11, 34)),
    ]:
        x(V, v)
    x("INSERT INTO evaluations(team_id,score,notes,author,created) VALUES('t-a',4,'n','d',?)", (ts(9, 0),))
    x("INSERT INTO evaluations(team_id,score,notes,author,created) VALUES('t-a',5,'n','d',?)", (ts(10, 0),))
    M = "INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created,read_at) VALUES(?,?,?,?,?,?,?,?,?)"
    body = f"body {CANARY}"
    x(M, ("lead-a", "owner", "escalation", "E" * 200, body, "G-1", None, ts(9, 0), None))
    x(M, ("lead-a", "owner", "escalation", "already read", body, "G-1", None, ts(9, 1), ts(9, 2)))
    x(M, ("lead-a", "boss", "report", "just a report", body, "G-1", None, ts(9, 3), None))
    x(M, ("lead-a", "lead-b", "escalation", "to a lead", body, "G-1", None, ts(9, 4), None))
    x(M, ("lead-a", "boss", "escalation", "boss one", body, None, None, ts(9, 5), None))
    x("INSERT INTO plans(goal_id,version,body,author,created) VALUES('G-1',1,?,'pm',?)", (f"plan {CANARY}", ts(8, 0)))


class QueryCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = os.path.join(self.tmp.name, "factory.db")
        self.writer = make_db(self.path)  # stays open so the -wal file persists
        self.addCleanup(self.writer.close)
        self.con = wdb.connect(self.path)
        self.addCleanup(self.con.close)


class ReadOnlyTests(QueryCase):
    def test_writes_raise(self):
        for sql in ("INSERT INTO config VALUES('k','v')", "UPDATE config SET value='1'", "DELETE FROM config",
                    "CREATE TABLE zz(a)", "PRAGMA journal_mode=DELETE"):
            with self.subTest(sql=sql):
                with self.assertRaises(sqlite3.OperationalError):
                    self.con.execute(sql)

    def test_missing_path_raises_and_creates_nothing(self):
        missing = os.path.join(self.tmp.name, "nope.db")
        before = sorted(os.listdir(self.tmp.name))
        with self.assertRaises(FileNotFoundError):
            wdb.connect(missing)
        with self.assertRaises(FileNotFoundError):
            with wdb.reader(missing):
                pass
        self.assertEqual(sorted(os.listdir(self.tmp.name)), before)

    def test_path_with_spaces_and_hash(self):
        d = os.path.join(self.tmp.name, "a b#c")
        os.mkdir(d)
        p = os.path.join(d, "f.db")
        make_db(p).close()
        with wdb.reader(p) as con:
            self.assertEqual(con.execute("SELECT COUNT(*) FROM goals").fetchone()[0], 4)

    def test_reader_closes(self):
        with wdb.reader(self.path) as con:
            con.execute("SELECT 1")
        with self.assertRaises(sqlite3.ProgrammingError):
            con.execute("SELECT 1")

    def test_has_column(self):
        self.assertTrue(wdb.has_column(self.con, "tasks", "follow_up_of"))
        self.assertFalse(wdb.has_column(self.con, "tasks", "nope"))

    def test_all_queries_leave_db_files_untouched(self):
        wal = self.path + "-wal"
        self.assertTrue(os.path.exists(wal))  # the writer connection keeps it alive
        snap = lambda: [(os.stat(p).st_mtime_ns, os.stat(p).st_size) for p in (self.path, wal)]
        before = snap()
        run_all(self.con)
        # -shm may be touched by SQLite for read locks; only the db and -wal must be unchanged
        self.assertEqual(snap(), before)

    def test_writer_not_blocked_by_open_reader(self):
        cur = self.con.execute("SELECT * FROM events")
        cur.fetchone()  # read transaction is open
        took = []

        def write():
            w = sqlite3.connect(self.path, isolation_level=None, timeout=2)
            t0 = time.monotonic()
            w.execute("BEGIN IMMEDIATE")
            w.execute("INSERT INTO config VALUES('k','v')")
            w.execute("COMMIT")
            took.append(time.monotonic() - t0)
            w.close()
        with wdb.reader(self.path) as held:
            held.execute("SELECT * FROM events").fetchone()
            th = threading.Thread(target=write)
            th.start()
            th.join(5)
        self.assertEqual(len(took), 1)
        self.assertLess(took[0], 0.2)


def run_all(con):
    return {
        "goals": q.goals(con), "goal_detail": q.goal_detail(con, "G-1"), "owner_pending": q.owner_pending(con),
        "teams": q.teams(con), "agents": q.agents(con, include_ended=True, now=NOW), "tasks": q.tasks(con),
        "pr_cycle": q.pr_cycle(con, "T-1"), "events": q.events(con), "messages_meta": q.messages_meta(con),
        "throughput": q.throughput(con, now=NOW), "quality": q.quality(con), "changelog": q.changelog(con),
        "config": q.config(con), "resources_db": q.resources_db(con, {"max_agents": 8, "min_free_mb": 900}),
    }


class PrivacyTests(QueryCase):
    def test_canary_never_in_results(self):
        res = run_all(self.con)
        res["tasks_goal"] = q.tasks(self.con, goal="G-1", status="done")
        res["pr_cycle_all"] = [q.pr_cycle(self.con, t["id"]) for t in q.tasks(self.con)]
        res["agents_default"] = q.agents(self.con)
        res["events_filtered"] = q.events(self.con, actions=["note"], entity="task")
        blob = json.dumps(res)
        self.assertNotIn(CANARY, blob)
        self.assertIn("G-1", blob)  # sanity: the dump is real data


class GoalTests(QueryCase):
    def test_goals(self):
        g = {r["id"]: r for r in q.goals(self.con)}
        self.assertEqual([r["id"] for r in q.goals(self.con)], ["G-1", "G-2", "G-3", "G-4"])
        g1 = g["G-1"]
        self.assertEqual((g1["tasks_total"], g1["tasks_done"], g1["tasks_blocked"], g1["open_prs"]), (5, 1, 1, 1))
        self.assertEqual(g1["teams"], ["t-a", "t-b"])
        self.assertEqual((g["G-3"]["tasks_total"], g["G-3"]["teams"]), (0, []))
        self.assertEqual(g["G-2"]["tasks_done"], 1)
        self.assertNotIn("intent", g1)

    def test_goal_detail_criteria_states(self):
        d = q.goal_detail(self.con, "G-1")
        self.assertEqual(len(d["intent"]), 600)
        by = {c["n"]: c for c in d["criteria"]}
        self.assertEqual(by[1]["state"], "met")
        self.assertEqual(by[1]["tasks"], ["T-1"])
        self.assertIn("continued line", by[1]["text"])
        self.assertEqual(by[2]["state"], "in_progress")
        self.assertEqual(by[2]["tasks"], ["T-2", "T-3"])
        self.assertEqual(by[3]["state"], "in_progress")
        self.assertEqual(by[4]["state"], "untracked")  # only a cancelled task carries SC4
        self.assertEqual(by[4]["tasks"], ["T-6"])

    def test_done_goal_all_met_and_fallback_lines(self):
        self.writer.execute("UPDATE goals SET status='done' WHERE id='G-3'")
        d = q.goal_detail(self.con, "G-3")
        self.assertEqual([(c["n"], c["text"], c["state"]) for c in d["criteria"]],
                         [(1, "only line one", "met"), (2, "line two", "met")])

    def test_untracked_fallback_and_null_criteria(self):
        self.assertEqual(q.goal_detail(self.con, "G-4")["criteria"], [])
        self.assertEqual(q.goal_detail(self.con, "G-2")["criteria"], [])
        self.writer.execute("UPDATE goals SET status='active' WHERE id='G-3'")
        self.assertEqual({c["state"] for c in q.goal_detail(self.con, "G-3")["criteria"]}, {"untracked"})
        self.assertIsNone(q.goal_detail(self.con, "G-99"))

    def test_done_task_without_merged_event_is_not_met(self):
        self.writer.execute("DELETE FROM events WHERE action='merged' AND entity_id='T-1'")
        by = {c["n"]: c for c in q.goal_detail(self.con, "G-1")["criteria"]}
        self.assertEqual(by[1]["state"], "in_progress")

    def test_parse_criteria_helpers(self):
        self.assertEqual(q.parse_criteria(None), [])
        self.assertEqual(q.parse_criteria("5) a\n6. b"), [(5, "a"), (6, "b")])
        self.assertEqual(q._tags("[SC1,SC3] x [SC7]"), {1, 3, 7})
        self.assertEqual(q._strip_tags("[SC1,SC2] hello  [SC3]"), "hello")

    def test_owner_pending_excludes_internal_blocked(self):
        w = self.writer
        self.assertEqual(q.owner_pending(self.con)["tasks_blocked"], [])  # T-4 is blocked but waits=internal (default)
        self.assertEqual([t["id"] for t in q.internal_blocked(self.con)], ["T-4"])
        w.execute("UPDATE tasks SET waits='owner' WHERE id='T-4'")
        w.execute("INSERT INTO tasks(id,goal_id,title,status,created,updated) VALUES('T-8','G-1','queued','blocked','a','b')")
        self.assertEqual([t["id"] for t in q.owner_pending(self.con)["tasks_blocked"]], ["T-4"])
        self.assertEqual([t["id"] for t in q.internal_blocked(self.con)], ["T-8"])
        g1 = {g["id"]: g for g in q.goals(self.con)}["G-1"]
        self.assertEqual((g1["tasks_blocked"], g1["tasks_blocked_owner"]), (2, 1))

    def test_owner_pending_requests_count_other_kinds_do_not(self):
        w = self.writer
        M = "INSERT INTO messages(sender,recipient,kind,subject,created) VALUES('director',?,?,?,?)"
        w.execute(M, ("boss", "request", "REQ-UNREAD", ts(9, 0)))
        w.execute(M, ("boss", "report", "REPORT-UNREAD", ts(9, 0)))
        subj = [m["subject"] for m in q.owner_pending(self.con)["escalations"]]
        self.assertIn("REQ-UNREAD", subj)
        self.assertNotIn("REPORT-UNREAD", subj)

    def test_owner_pending(self):
        o = q.owner_pending(self.con)
        self.assertEqual([g["id"] for g in o["goals"]], ["G-3", "G-4"])  # by priority, then id
        self.assertEqual([t["id"] for t in o["tasks_blocked"]], [])
        self.assertEqual([t["task_id"] for t in o["judge_blocked_auto_merge"]], ["T-3"])  # T-5 is done
        esc = o["escalations"]
        self.assertEqual([m["subject"][:5] for m in esc], ["EEEEE", "boss "])  # unread owner/boss escalations only
        self.assertEqual(len(esc[0]["subject"]), 120)
        self.assertNotIn("body", esc[0])


class TeamAgentTaskTests(QueryCase):
    def test_teams(self):
        t = q.teams(self.con)
        self.assertEqual([(r["id"], r["lead_agent"], r["status"]) for r in t], [("t-a", "lead-a", "active"), ("t-b", "lead-b", "closed")])

    def test_agents(self):
        live = {a["name"]: a for a in q.agents(self.con, now=NOW)}
        self.assertEqual(sorted(live), ["coder-2", "lead-a", "queued-x"])
        self.assertEqual(live["lead-a"]["age_s"], 4 * 3600)
        self.assertEqual(live["queued-x"]["age_s"], 1800)  # since queued_at
        self.assertNotIn("spawn_args", live["lead-a"])
        allr = {a["name"]: a for a in q.agents(self.con, include_ended=True, now=NOW)}
        self.assertEqual(allr["coder-t-1"]["age_s"], 35 * 60)  # started -> ended
        self.assertEqual(len(allr), 5)

    def test_tasks_filters_and_depends(self):
        allt = {t["id"]: t for t in q.tasks(self.con)}
        self.assertEqual(allt["T-3"]["depends_on"], ["T-1", "T-2"])
        self.assertEqual(allt["T-4"]["depends_on"], [])  # invalid JSON
        self.assertEqual(allt["T-5"]["depends_on"], [])  # empty string
        self.assertEqual(allt["T-3"]["follow_up_of"], "T-1")
        self.assertNotIn("spec", allt["T-1"])
        self.assertNotIn("acceptance", allt["T-1"])
        self.assertEqual([t["id"] for t in q.tasks(self.con, goal="G-2")], ["T-5"])
        self.assertEqual([t["id"] for t in q.tasks(self.con, goal="G-1", status="blocked")], ["T-4"])


class PrCycleTests(QueryCase):
    def test_timeline(self):
        c = q.pr_cycle(self.con, "T-1")
        labels = [(i["kind"], i["label"]) for i in c["items"]]
        self.assertEqual(labels[0], ("agent", "spawn coder"))
        self.assertIn(("verdict", "reviewer fail"), labels)
        self.assertIn(("event", "merged"), labels)
        self.assertNotIn("reviewer-fail", [l for _, l in labels])  # only the listed event actions
        stamps = [q._epoch(i["ts"]) for i in c["items"]]
        self.assertEqual(stamps, sorted(stamps))
        fail = next(i for i in c["items"] if i["label"] == "reviewer fail")
        self.assertEqual(fail["head"], "abcdef12")
        self.assertEqual(len(fail["detail"]), 300)
        self.assertEqual((c["rejections"], c["attempts"], c["retries"], c["reopens"]), (1, 2, 1, 0))
        self.assertIsNone(next(i for i in c["items"] if i["label"] == "note")["detail"])

    def test_rejection_counting_does_not_double_count(self):
        c3 = q.pr_cycle(self.con, "T-3")  # judge verdict row + judge-reject event = one rejection
        self.assertEqual((c3["rejections"], c3["reopens"], c3["retries"]), (1, 1, 2))
        self.assertEqual(q.pr_cycle(self.con, "T-2")["rejections"], 1)  # event only (older data)

    def test_unknown_task(self):
        c = q.pr_cycle(self.con, "T-404")
        self.assertEqual((c["items"], c["rejections"], c["retries"]), ([], 0, 0))


class EventTests(QueryCase):
    def test_events_filters(self):
        self.assertEqual(len(q.events(self.con, limit=3)), 3)
        self.assertEqual(q.events(self.con)[0]["action"], "set")  # newest id first
        self.assertEqual({e["action"] for e in q.events(self.con, actions=["merged"])}, {"merged"})
        self.assertEqual({e["entity"] for e in q.events(self.con, entity="config")}, {"config"})
        since = [e for e in q.events(self.con, since=ts(11, 33))]
        self.assertTrue(since and all(q._epoch(e["ts"]) >= q._epoch(ts(11, 33)) for e in since))

    def test_messages_meta(self):
        m = q.messages_meta(self.con)
        self.assertEqual(len(m), 5)
        self.assertTrue(all("body" not in r for r in m))
        self.assertEqual(max(len(r["subject"]) for r in m), 120)
        self.assertEqual([r["kind"] for r in q.messages_meta(self.con, kinds=("report",))], ["report"])
        self.assertEqual(len(q.messages_meta(self.con, limit=2)), 2)
        self.assertIs(next(r for r in m if r["subject"] == "already read")["read"], True)


class ThroughputTests(QueryCase):
    def test_stage_split_and_none_piece(self):
        t = q.throughput(self.con, days=14, now=NOW)
        by = {r["task_id"]: r for r in t["tasks"]}
        self.assertEqual(by["T-1"]["total_s"], 80 * 60)
        self.assertEqual(by["T-1"]["coding_s"], 30 * 60)
        self.assertEqual(by["T-1"]["review_s"], 15 * 60)  # last in_review (11:00) -> last pass (11:15)
        self.assertEqual(by["T-1"]["judge_s"], 5 * 60)
        self.assertEqual(by["T-1"]["rework_s"], 15 * 60)  # fail 10:45 -> in_review 11:00
        t5 = by["T-5"]  # no agents, no updates, no pass verdicts
        self.assertEqual((t5["total_s"], t5["coding_s"], t5["review_s"], t5["judge_s"]), (None, None, None, None))
        self.assertEqual(t5["rework_s"], 0)

    def test_per_day_and_goal(self):
        t = q.throughput(self.con, days=14, now=NOW)
        self.assertEqual(len(t["merges_per_day"]), 14)
        self.assertEqual(t["merges_per_day"][-1], {"day": "2026-10-24", "merges": 2})
        self.assertEqual(sum(d["merges"] for d in t["merges_per_day"]), 2)
        self.assertEqual({(g["goal_id"], g["done"]) for g in t["tasks_per_goal"]}, {("G-1", 1), ("G-2", 1)})
        old = q.throughput(self.con, days=1, now=NOW + dt.timedelta(days=3))
        self.assertEqual(old["tasks"], [])

    def test_overlapping_rework_spans_are_merged(self):
        self.writer.execute("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                            "VALUES('T-1','u','abcdef1234','qa','fail','x','qa',?)", (ts(10, 46),))
        by = {r["task_id"]: r for r in q.throughput(self.con, now=NOW)["tasks"]}
        self.assertEqual(by["T-1"]["rework_s"], 15 * 60)

    def test_fail_verdict_after_merge_gives_unknown_rework_not_negative(self):
        # T-1 merged at 11:20; a fail verdict recorded afterwards is out-of-order data
        self.writer.execute("INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) "
                            "VALUES('T-1','u','abcdef1234','judge','fail','late','judge',?)", (ts(11, 50),))
        by = {r["task_id"]: r for r in q.throughput(self.con, now=NOW)["tasks"]}
        self.assertIsNone(by["T-1"]["rework_s"])
        for t in q.throughput(self.con, now=NOW)["tasks"]:
            for k in ("total_s", "coding_s", "review_s", "judge_s", "rework_s"):
                self.assertTrue(t[k] is None or t[k] >= 0, (t["task_id"], k, t[k]))


class QualityTests(QueryCase):
    def test_quality(self):
        r = q.quality(self.con)
        ta = next(t for t in r["teams"] if t["team_id"] == "t-a")
        self.assertEqual([t["team_id"] for t in r["teams"]], ["t-a", "t-b"])
        self.assertEqual((ta["tasks"], ta["merged"], ta["rejections"], ta["retries"]), (4, 1, 3, 3))
        self.assertNotIn("rejection_rate", ta)
        self.assertNotIn("rejection_rate", r["overall"])
        self.assertEqual(ta["rejections_per_merged"], 3.0)
        self.assertEqual(ta["score"], 5)  # latest evaluation
        self.assertEqual((ta["ci_blocked"], ta["ci_pass_rate"]), (2, round(1 / 3, 3)))
        o = r["overall"]
        self.assertEqual((o["tasks"], o["merged"], o["rejections"], o["ci_pass_rate"]), (6, 2, 3, 0.5))
        tb = next(t for t in r["teams"] if t["team_id"] == "t-b")
        self.assertEqual((tb["tasks"], tb["rejections_per_merged"], tb["ci_pass_rate"], tb["score"]), (1, None, None, None))


class ChangelogConfigResourcesTests(QueryCase):
    def test_changelog(self):
        c = q.changelog(self.con)
        self.assertEqual([g["goal_id"] for g in c], ["G-1", "G-2"])
        self.assertEqual(c[0]["goal_title"], "Goal one")
        self.assertEqual(c[0]["items"], [{"task_id": "T-1", "title": "one", "pr_url": "https://github.com/o/r/pull/1",
                                          "merged": ts(11, 20), "sha": "deadbeef"}])
        self.assertEqual(c[1]["items"][0]["title"], "ship it")

    def test_config(self):
        self.assertEqual(q.config(self.con), [{"key": "judge_mode", "value": "auto"}, {"key": "max_agents", "value": "6"}])

    def test_resources_db(self):
        r = q.resources_db(self.con, {"max_agents": 8, "min_free_mb": 900})
        self.assertEqual((r["live"], r["queued_count"]), (2, 1))
        self.assertEqual(r["queued"][0]["name"], "queued-x")
        self.assertEqual(r["queued"][0]["reason"], "gate closed: low RAM")  # latest queued event
        self.assertEqual(r["caps"]["max_agents"], {"value": "6", "source": "config"})
        self.assertEqual(r["caps"]["min_free_mb"], {"value": 900, "source": "default"})
        self.assertEqual(len(r["queued_events"]), 2)
        self.assertEqual([e["detail"] for e in r["config_events"]], ["6"])
        self.assertEqual(q.resources_db(self.con)["caps"], {})


class EdgeCaseTests(unittest.TestCase):
    def _open(self, schema=SCHEMA, seed=False):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        p = os.path.join(tmp.name, "f.db")
        w = make_db(p, schema, seed)
        self.addCleanup(w.close)
        con = wdb.connect(p)
        self.addCleanup(con.close)
        return con, w

    def test_empty_schema_only_db(self):
        con, _ = self._open()
        res = run_all(con)
        self.assertEqual(res["goals"], [])
        self.assertIsNone(res["goal_detail"])
        self.assertEqual(res["owner_pending"], {"goals": [], "tasks_blocked": [], "judge_blocked_auto_merge": [], "escalations": []})
        self.assertNotIn("rejection_rate", res["quality"]["overall"])
        self.assertIsNone(res["quality"]["overall"]["rejections_per_merged"])
        self.assertEqual(res["throughput"]["tasks"], [])
        self.assertEqual(res["resources_db"]["live"], 0)
        self.assertEqual(res["changelog"], [])
        json.dumps(res)

    def test_old_schema_without_newer_columns(self):
        old = SCHEMA.replace(", follow_up_of TEXT, waits TEXT NOT NULL DEFAULT 'internal'", "").replace(
            "  flagless INTEGER NOT NULL DEFAULT 0, flagless_since TEXT,\n  stop_requested_at TEXT, stop_attempt_at TEXT, stop_failed_kind TEXT);", "  x INTEGER);")
        self.assertNotIn("follow_up_of", old)
        self.assertNotIn("waits", old)
        self.assertNotIn("stop_requested_at", old)
        con, w = self._open(old)
        w.execute("INSERT INTO goals VALUES('G-1',NULL,'g',NULL,NULL,'active',1,NULL,NULL,'a','b')")
        w.execute("INSERT INTO tasks(id,goal_id,title,created,updated) VALUES('T-1','G-1','t','a','b')")
        w.execute("INSERT INTO agents(name,role,status,started) VALUES('a','coder','live',?)", (ts(10, 0),))
        self.assertIsNone(q.tasks(con)[0]["follow_up_of"])
        self.assertEqual(q.tasks(con)[0]["waits"], "internal")
        w.execute("UPDATE tasks SET status='blocked'")
        self.assertEqual(q.owner_pending(con)["tasks_blocked"], [])  # pre-waits DB: every blocked task is internal
        self.assertEqual([t["id"] for t in q.internal_blocked(con)], ["T-1"])
        self.assertEqual(q.goals(con)[0]["tasks_blocked_owner"], 0)
        self.assertIsNone(q.agents(con, now=NOW)[0]["stop_requested_at"])
        run_all(con)

    def test_garbage_values(self):
        con, w = self._open()
        w.execute("INSERT INTO goals VALUES('G-1',NULL,'g',NULL,NULL,'weird',1,NULL,NULL,'a','b')")
        w.execute("INSERT INTO tasks(id,goal_id,title,status,depends_on,created,updated) VALUES('T-1','G-1','t','mystery','{\"a\":1}','garbage','b')")
        w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES('not a date',NULL,'task','T-1','merged',NULL)")
        w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,NULL,'task','T-1','update',NULL)", (ts(10, 0),))
        w.execute("INSERT INTO agents(name,role,status,started) VALUES('a','coder','live','???')")
        res = run_all(con)
        self.assertEqual(res["tasks"][0]["depends_on"], [])
        self.assertIsNone(res["agents"][0]["age_s"])
        self.assertEqual(res["goals"][0]["tasks_total"], 1)
        self.assertEqual(res["throughput"]["tasks"], [])  # unparseable merge ts is skipped, not guessed
        self.assertEqual(res["changelog"][0]["items"][0]["sha"], None)

    def test_parse_ts(self):
        self.assertIsNone(q.parse_ts(None))
        self.assertIsNone(q.parse_ts("garbage"))
        self.assertIsNone(q.parse_ts(12))
        self.assertEqual(q.parse_ts("2026-10-24T10:00:00+02:00").utcoffset(), dt.timedelta(hours=2))
        self.assertIsNotNone(q.parse_ts("2026-10-24T10:00:00").tzinfo)


if __name__ == "__main__":
    unittest.main()
