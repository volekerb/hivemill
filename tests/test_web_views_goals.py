"""Tests for web/views/goals.py and web/views/tasks.py (goals, owner-pending, criteria, task list, PR-cycle timeline)."""
import datetime as dt
import json
import re
import sys
import tempfile
import time
import unittest
from pathlib import Path
from types import SimpleNamespace

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import CANARY, NOW, make_db, ts  # noqa: E402
from web import ci, db as wdb  # noqa: E402
from web import html as wh  # noqa: E402
from web.views import ROUTES, goals, tasks  # noqa: E402

XSS = "<script>alert(1)</script>"


class ViewCase(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.path = Path(self.tmp.name) / "factory.db"
        self.w = make_db(self.path)
        self.addCleanup(self.w.close)
        # a goal whose criterion 3 is met by a done+merged [SC3] task, and one criterion nobody tags
        x = self.w.execute
        x("INSERT INTO goals VALUES('G-9','p1','Goal nine',?,?,'active',1,'owner',NULL,?,?)",
          ("nine", "1. one\n2. two\n3. three\n4. four", ts(8, 0), ts(9, 0)))
        T = "INSERT INTO tasks(id,goal_id,title,spec,acceptance,role,status,attempts,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?)"
        x(T, ("T-91", "G-9", "[SC3] built", "s", "a", "coder", "done", 1, ts(8, 0), ts(9, 0)))
        x(T, ("T-92", "G-9", "[SC1] built, not merged", "s", "a", "coder", "done", 1, ts(8, 0), ts(9, 0)))
        x(T, ("T-93", "G-9", "[SC2] wip", "s", "a", "coder", "in_progress", 1, ts(8, 0), ts(9, 0)))
        x("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?, 'judge','task','T-91','merged','@12345678aa')", (ts(9, 0),))

    def ctx(self):
        con = wdb.connect(self.path)
        self.addCleanup(con.close)
        return SimpleNamespace(db=con, now=NOW, usage_fn=None, mem_fn=None, tokens=None, defaults={}, factory_home=None)

    def page(self, view, **params):
        c = self.ctx()
        d = view.data(c, **params)
        json.dumps(d)   # the /api/<page>.json payload must be serialisable
        return d, str(view.render(c, d))


class TestGoalsList(ViewCase):
    def test_lists_every_goal_with_progress_sorted_by_status(self):
        d, html = self.page(goals)
        self.assertEqual({g["id"] for g in d["goals"]}, {"G-1", "G-2", "G-3", "G-4", "G-9"})
        order = [g["id"] for g in d["goals"]]
        self.assertEqual(order, ["G-1", "G-9", "G-3", "G-4", "G-2"])   # active (P1), proposed, blocked, done
        g1 = next(g for g in d["goals"] if g["id"] == "G-1")
        self.assertEqual((g1["tasks_done"], g1["tasks_total"]), (1, 5))
        self.assertIn("1/5", html)
        self.assertIn("bar bar--20", html)
        self.assertRegex(html, r'href="/goal/G-1"')
        self.assertIn("<details>", html)   # done goals sit under a collapsed card
        self.assertLess(html.index("Goal one"), html.index("<details>"))
        self.assertGreater(html.index("Goal two"), html.index("<details>"))

    def test_priority_zero_sorts_first_within_a_status(self):
        self.w.execute("INSERT INTO goals VALUES('G-8','p1','Goal eight',NULL,NULL,'active',0,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        d, _html = self.page(goals)
        self.assertEqual([g["id"] for g in d["goals"]][:3], ["G-8", "G-1", "G-9"])

    def test_internal_blocked_neutral_owner_blocked_red(self):
        self.w.execute("UPDATE tasks SET waits='owner' WHERE id='T-4'")
        self.w.execute("INSERT INTO tasks(id,goal_id,title,status,created,updated) VALUES('T-8','G-1','queued job','blocked','a','b')")
        d, html = self.page(goals)
        owner = html[html.index("Waiting on owner"):html.index("Blocked (internal)")]
        self.assertIn("T-4", owner)
        self.assertNotIn("T-8", owner)                       # internal-blocked is not an owner item
        self.assertIn('<span class="badge badge--bad">blocked: owner</span>', html)
        internal = html[html.index("Blocked (internal)"):html.index("Open goals")]
        self.assertIn("T-8", internal)
        self.assertIn('<span class="badge badge--muted">queued/deferred</span>', internal)
        self.assertNotIn("badge--bad", internal)
        self.assertIn('<span class="badge badge--bad">1 owner</span>', html)    # goal table, G-1
        self.assertIn('<span class="badge badge--muted">1 queued</span>', html)
        _, tasks_html = self.page(tasks)
        self.assertIn('<span class="badge badge--bad">blocked: owner</span>', tasks_html)
        self.assertIn('<span class="badge badge--muted">blocked (internal)</span>', tasks_html)

    def test_no_internal_card_without_internal_blocked(self):
        self.w.execute("UPDATE tasks SET waits='owner' WHERE id='T-4'")
        _, html = self.page(goals)
        self.assertNotIn("Blocked (internal)", html)

    def test_waiting_on_owner_card_first_subjects_only(self):
        self.w.execute("UPDATE tasks SET waits='owner' WHERE id='T-4'")
        d, html = self.page(goals)
        self.assertLess(html.index("Waiting on owner"), html.index("Open goals"))
        for needle in ("G-3", "G-4", "T-4", "T-3", "boss one", "auto-merge off"):
            self.assertIn(needle, html)
        self.assertNotIn(CANARY, html)

    def test_empty_db_renders_empty_states(self):
        self.w.executescript("DELETE FROM goals; DELETE FROM tasks; DELETE FROM messages; DELETE FROM events;")
        d, html = self.page(goals)
        self.assertIn("No open goals.", html)
        self.assertIn("Nothing is waiting on the owner.", html)

    def test_only_known_classes(self):
        for view, params in ((goals, {}), (goals.detail, {"id": "G-1"}), (tasks, {}), (tasks.detail, {"id": "T-1"})):
            _, html = self.page(view, **params)
            used = {c for m in re.findall(r'class="([^"]*)"', html) for c in m.split()}
            self.assertEqual(used - wh.CLASSES, set(), view)
            self.assertNotIn("style=", html)


class TestGoalDetail(ViewCase):
    def test_header_criteria_teams_tasks_plan(self):
        d, html = self.page(goals.detail, id="G-1")
        self.assertEqual([c["state"] for c in d["criteria"]], ["met", "in_progress", "in_progress", "untracked"])
        for needle in ("Goal one", "met", "in progress", "untracked", "scope A", "lead-a", "T-1", "T-6", "Version 1"):
            self.assertIn(needle, html)
        self.assertRegex(html, r'href="/task/T-3"')
        self.assertRegex(html, r'href="https://github.com/o/r/pull/3"')
        self.assertLess(len(d["intent"]), 700)
        self.assertEqual(d["plan"]["version"], 1)

    def test_tagged_done_merged_task_makes_criterion_met_and_untagged_is_untracked(self):
        d, html = self.page(goals.detail, id="G-9")
        st = {c["n"]: c["state"] for c in d["criteria"]}
        self.assertEqual(st, {1: "in_progress", 2: "in_progress", 3: "met", 4: "untracked"})   # T-92 done but never merged
        self.assertIn("untracked", html)

    def test_plan_body_never_shown(self):
        d, html = self.page(goals.detail, id="G-1")
        self.assertNotIn(CANARY, html + json.dumps(d))

    def test_unknown_goal(self):
        d, html = self.page(goals.detail, id="G-404")
        self.assertIn("No goal G-404.", html)


class TestHostileText(ViewCase):
    def test_titles_escaped_everywhere(self):
        self.w.execute("UPDATE goals SET title=? WHERE id='G-1'", (XSS,))
        self.w.execute("UPDATE tasks SET title=? WHERE id='T-1'", (XSS,))
        self.w.execute("UPDATE goals SET intent=? WHERE id='G-1'", (XSS,))
        self.w.execute("UPDATE messages SET subject=? WHERE kind='escalation'", (XSS,))
        for view, params in ((goals, {}), (goals.detail, {"id": "G-1"}), (tasks, {}), (tasks.detail, {"id": "T-1"})):
            _, html = self.page(view, **params)
            self.assertNotIn("<script>alert", html)
        self.assertIn("&lt;script&gt;", self.page(goals.detail, id="G-1")[1])

    def test_canaries_never_rendered(self):
        for view, params in ((goals, {}), (goals.detail, {"id": "G-1"}), (tasks, {}), (tasks.detail, {"id": "T-1"}),
                             (tasks.detail, {"id": "T-3"})):
            d, html = self.page(view, **params)
            self.assertNotIn(CANARY, html + json.dumps(d), view)


class TestTasksList(ViewCase):
    def test_list_columns_and_ci(self):
        d, html = self.page(tasks)
        by = {t["id"]: t for t in d["tasks"]}
        self.assertEqual(by["T-1"]["ci"], "green at merge")
        self.assertEqual(by["T-1"]["rejections"], 1)       # one reviewer fail verdict
        self.assertEqual(by["T-3"]["rejections"], 1)       # judge reject verdict + judge-reject event = one rejection
        self.assertEqual(by["T-2"]["rejections"], 1)       # judge-reject event without a verdict row
        self.assertIsNone(by["T-4"]["ci"])
        self.assertEqual(by["T-1"]["ci"], "green at merge")   # the JSON keeps the raw word; the page says "CI passed" (B3 copy map)
        self.assertIn("CI passed", html)
        self.assertIn("1 rejection · 2 tries", html)

    def test_open_pr_ci_comes_from_the_cache_and_misses_show_unknown(self):
        pr = "https://github.com/o/r/pull/3"
        old = ci.CACHE
        fake = ci.CiCache(gh="/nonexistent", autostart=False)
        fake._entries[pr] = {"state": "open", "checks": [{"name": "unittest", "conclusion": "failure"}], "fetched": time.time()}
        ci.CACHE = fake
        self.addCleanup(setattr, ci, "CACHE", old)
        d, html = self.page(tasks)
        self.assertEqual({t["id"]: t["ci"] for t in d["tasks"]}["T-3"], "failing")
        self.assertIn("failing", html)

        ci.CACHE = ci.CiCache(gh="/nonexistent", autostart=False)
        d, _ = self.page(tasks)
        self.assertEqual({t["id"]: t["ci"] for t in d["tasks"]}["T-3"], "unknown")

    def test_page_does_not_wait_on_a_slow_gh(self):
        slow = Path(self.tmp.name) / "gh"
        slow.write_text("#!/bin/sh\nsleep 5\necho '{}'\n")
        slow.chmod(0o755)
        old = ci.CACHE
        ci.CACHE = ci.CiCache(gh=str(slow))
        self.addCleanup(setattr, ci, "CACHE", old)
        t = time.time()
        d, _ = self.page(tasks)
        self.assertLess(time.time() - t, 1.0)
        self.assertEqual({x["id"]: x["ci"] for x in d["tasks"]}["T-3"], "unknown")

    def test_filters_validated(self):
        d, _ = self.page(tasks, goal="G-2")
        self.assertEqual([t["id"] for t in d["tasks"]], ["T-5"])
        d, _ = self.page(tasks, status="blocked")
        self.assertEqual([t["id"] for t in d["tasks"]], ["T-4"])
        d, _ = self.page(tasks, goal="G-1", status="done")
        self.assertEqual([t["id"] for t in d["tasks"]], ["T-1"])
        d, _ = self.page(tasks, goal="G-1'; DROP TABLE tasks;--", status="nonsense")
        self.assertEqual(d["filter"], {"goal": None, "status": None})
        self.assertEqual(len(d["tasks"]), 9)


class TestTaskDetail(ViewCase):
    def test_timeline_in_time_order_with_verdicts_rejection_and_merge(self):
        d, html = self.page(tasks.detail, id="T-1")
        texts = [s["text"] for s in d["steps"]]
        self.assertEqual(d["steps"][0]["text"], "spawn coder")
        self.assertIn("reviewer: fail (fail)", texts)
        self.assertLess(texts.index("reviewer: fail (fail)"), texts.index("reviewer: pass (pass)"))
        self.assertLess(texts.index("reviewer: pass (pass)"), texts.index("judge: pass (merge)"))
        self.assertLess(texts.index("judge: pass (merge)"), texts.index("merged"))
        merged = next(s for s in d["steps"] if s["text"] == "merged")
        self.assertEqual(merged["sha"], "deadbeef")
        fail = next(s for s in d["steps"] if s["text"].startswith("reviewer: fail"))
        self.assertEqual(fail["sha"], "abcdef12")
        self.assertIn("judge blocked: checks not green: unittest", texts)
        self.assertIn("status -> in_review", texts)
        times = [s["ts"] for s in d["steps"]]
        self.assertEqual(times, sorted(times))
        self.assertNotIn("note", texts)
        self.assertNotIn("needs work", html)   # verdict summaries are free text

    def test_pr_opened_item_at_first_pr_or_in_review_update(self):
        d, html = self.page(tasks.detail, id="T-1")
        texts = [s["text"] for s in d["steps"]]
        self.assertEqual(texts.count("PR opened"), 1)
        opened = next(s for s in d["steps"] if s["text"] == "PR opened")
        self.assertEqual(opened["ts"], ts(10, 30))
        self.assertEqual(texts.count("status -> in_review"), 1)   # the later re-review stays a plain status step
        self.assertLess(texts.index("PR opened"), texts.index("reviewer: fail (fail)"))
        self.assertIn("PR opened", html)

    def test_kpis_and_counts_match_the_verdicts_table(self):
        for tid in ("T-1", "T-3", "T-2"):
            d, html = self.page(tasks.detail, id=tid)
            n = self.w.execute("SELECT count(*) FROM verdicts WHERE task_id=? AND verdict IN ('fail','reject')", (tid,)).fetchone()[0]
            ev = self.w.execute("SELECT count(*) FROM events WHERE entity_id=? AND action='judge-reject'", (tid,)).fetchone()[0]
            self.assertEqual(d["rejections"], n + max(0, ev - self.w.execute(
                "SELECT count(*) FROM verdicts WHERE task_id=? AND role='judge' AND verdict IN ('fail','reject')", (tid,)).fetchone()[0]))
        d, html = self.page(tasks.detail, id="T-1")
        self.assertEqual((d["rejections"], d["retries"], d["ci"], d["merged"]), (1, 1, "green at merge", True))
        self.assertEqual(d["total_s"], 3 * 3600 + 20 * 60)   # created 08:00 -> merged 11:20
        for needle in ("Total time", "Rejections", "Retries", "CI", "green at merge", "PR cycle"):
            self.assertIn(needle, html)

    def test_follow_ups_linked(self):
        d, html = self.page(tasks.detail, id="T-1")
        self.assertEqual([f["id"] for f in d["follow_ups"]], ["T-3"])
        self.assertRegex(html, r'href="/task/T-3"')
        d, html = self.page(tasks.detail, id="T-3")
        self.assertEqual(d["follow_up_of"], "T-1")

    def test_follow_up_event_links_task_ids_only(self):
        self.w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?, 'lead','task','T-2','follow-up',?)",
                       (ts(11, 50), "created T-93 because " + XSS))
        d, html = self.page(tasks.detail, id="T-2")
        self.assertRegex(html, r'href="/task/T-93"')
        self.assertNotIn("because", html)
        self.assertNotIn("<script>alert", html)

    def test_negative_total_is_unknown(self):
        # created after its merged event (clock skew): total is unknown, shown as "-", never negative
        self.w.execute("UPDATE tasks SET created=? WHERE id='T-91'", (ts(12, 0),))
        d, html = self.page(tasks.detail, id="T-91")
        self.assertIsNone(d["total_s"])
        self.assertRegex(html, r"Total time.{0,200}?-")
        self.assertNotRegex(html, r"-\d+[smhd]")
        # open task created after ctx.now
        self.w.execute("UPDATE tasks SET created=? WHERE id='T-93'", ("2999-01-01T00:00:00+00:00",))
        d, html = self.page(tasks.detail, id="T-93")
        self.assertIsNone(d["total_s"])

    def test_unknown_task(self):
        d, html = self.page(tasks.detail, id="T-404")
        self.assertIn("No task T-404.", html)


class TestPerformance(ViewCase):
    def test_pages_fast_on_bigger_data(self):
        x = self.w.execute
        for i in range(300):
            x("INSERT INTO tasks(id,goal_id,title,spec,acceptance,role,status,attempts,created,updated,pr_url) VALUES(?,?,?,?,?,?,?,?,?,?,?)",
              (f"T-{1000 + i}", "G-1", f"[SC1] bulk {i}", "s", "a", "coder", "done", 1, ts(8, 0), ts(9, 0), None))
            x("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?, 'judge','task',?,'merged','@abcdef12')", (ts(9, 0), f"T-{1000 + i}"))
        for view, params in ((goals, {}), (goals.detail, {"id": "G-1"}), (tasks, {}), (tasks.detail, {"id": "T-1"})):
            t = time.time()
            self.page(view, **params)
            self.assertLess(time.time() - t, 1.0, view)


class TestRoutes(unittest.TestCase):
    def test_registry_serves_these_modules(self):
        self.assertIs(ROUTES["/goals"], goals)
        self.assertIs(ROUTES["/goal/<id>"], goals.detail)
        self.assertIs(ROUTES["/tasks"], tasks)
        self.assertIs(ROUTES["/task/<id>"], tasks.detail)


if __name__ == "__main__":
    unittest.main()
