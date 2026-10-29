"""G-019 B5: /goals, /goal/<id> and /task/<id> through the real server (web/views/goals.py, web/views/tasks.py)."""
import json
import re
import statistics
import sys
import time
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import ts  # noqa: E402
from test_web_server import ServerCase  # noqa: E402
from web import html as wh  # noqa: E402

T_INS = "INSERT INTO tasks(id,goal_id,team_id,title,spec,acceptance,role,status,assignee,pr_url,attempts,created,updated) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)"
E_INS = "INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)"
V_INS = "INSERT INTO verdicts(task_id,pr_url,head_sha,role,verdict,summary,author,created) VALUES(?,?,?,?,?,?,?,?)"
A_INS = ("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,started,ended) VALUES(?,?,?,?,?,?,?,?,?)")


class DetailCase(ServerCase):
    def page(self, path):
        status, _h, body = self.get(path)
        self.assertEqual(status, 200, path)
        return body.decode()

    def h1(self, path):
        return re.search(r"<h1>(.*?)</h1>", self.page(path), re.S).group(1)

    def seed_three_attempts(self):
        x = self.con.execute
        x("INSERT INTO goals VALUES('G-70','p1','Cycle goal','intent','1. a\n2. b','active',2,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-70", "G-70", None, "Three tries", "s", "a", "coder", "done", "coder-t-70", "https://github.com/o/r/pull/70", 3,
                  ts(9, 0), ts(12, 0), ))
        for name, a, b in (("coder-70a", (10, 0), (10, 30)), ("coder-70b", (10, 50), (11, 10)), ("coder-70c", (11, 30), (12, 0))):
            x(A_INS, (name, "coder", "G-70", None, "T-70", "done", "m", ts(*a), ts(*b)))
        x(V_INS, ("T-70", "u", "aaaaaaaa11", "reviewer", "fail", "needs work", "reviewer-70", ts(10, 40)))
        x(E_INS, (ts(11, 20, 5), "judge", "task", "T-70", "judge-reject", "bad"))             # same second as the judge fail row
        x(V_INS, ("T-70", "u", "bbbbbbbb22", "judge", "fail", "bad", "judge", ts(11, 20, 5)))
        x(V_INS, ("T-70", "u", "cccccccc33", "qa", "pass", "ok", "qa-70", ts(11, 50)))
        x(V_INS, ("T-70", "u", "cccccccc33", "judge", "merge", "ok", "judge", ts(12, 0)))
        x(E_INS, (ts(12, 0), "judge", "task", "T-70", "merged", "@deadbeef"))


class TestHeadings(DetailCase):
    def test_goal_h1_is_the_title_and_breadcrumb_links_goals(self):
        self.assertEqual(self.h1("/goal/G-1"), "Goal one")
        page = self.page("/goal/G-1")
        nav = re.search(r'<nav aria-label="Breadcrumb".*?</nav>', page, re.S).group(0)
        self.assertIn('href="/goals"', nav)
        self.assertIn("G-1", nav)

    def test_task_h1_is_the_title_and_breadcrumb_links_tasks(self):
        self.assertEqual(self.h1("/task/T-1"), "[SC1] one")
        nav = re.search(r'<nav aria-label="Breadcrumb".*?</nav>', self.page("/task/T-1"), re.S).group(0)
        self.assertIn('href="/tasks"', nav)

    def test_h1_follows_each_request_not_the_last_one(self):
        self.assertEqual(self.h1("/goal/G-1"), "Goal one")
        self.assertEqual(self.h1("/goal/G-2"), "Goal two")
        self.assertEqual(self.h1("/goal/G-404"), "Goal")


class TestNotFound(DetailCase):
    def test_unknown_goal_and_task_are_calm(self):
        goal, task = self.page("/goal/G-999"), self.page("/task/T-9999")
        self.assertIn("No goal G-999", goal)
        self.assertIn('href="/goals"', goal)
        self.assertIn("No task T-9999", task)
        self.assertIn('href="/tasks"', task)
        for page in (goal, task):
            self.assertNotIn("Traceback", page)


class TestAttempts(DetailCase):
    def test_three_attempts_latest_open_and_duplicate_pair_merged(self):
        self.seed_three_attempts()
        page = self.page("/task/T-70")
        blocks = re.findall(r'<details class="disc attempt"[^>]*>', page)
        self.assertEqual(len(blocks), 3)
        self.assertEqual([b.endswith(" open>") for b in blocks], [False, False, True])
        lines = re.findall(r'<li class="timeline-item" data-actor="([^"]*)" data-kind="([^"]*)" data-ts="([^"]*)"', page)
        for a, b in zip(lines, lines[1:]):
            self.assertNotEqual(a, b)
        at = ts(11, 20, 5)
        self.assertEqual(sum(1 for _a, k, t in lines if t == at), 1)        # judge-reject event + judge fail verdict: one line
        self.assertIn("Attempt 3", page)

    def test_verdict_summary_one_line_kinds_only(self):
        self.seed_three_attempts()
        page = self.page("/task/T-70")
        self.assertIn("Reviewer failed", page)
        self.assertIn("QA passed", page)
        self.assertIn("Judge rejected once, then merged", page)
        self.assertNotIn("needs work", page)

    def test_tiles_total_rejections_attempts(self):
        self.seed_three_attempts()
        page = self.page("/task/T-70")
        for label in ("Total time", "Rejections", "Attempts"):
            self.assertIn(label, page)

    def test_task_without_events_has_an_empty_cycle(self):
        self.con.execute(T_INS, ("T-71", "G-1", None, "fresh", "s", "a", "coder", "todo", None, None, 0, ts(9, 0), ts(9, 0)))
        page = self.page("/task/T-71")
        self.assertIn("No events yet.", page)
        self.assertIn("No verdicts yet.", page)


class TestPrivateText(DetailCase):
    def test_no_private_text_on_goals_goal_or_task(self):
        x = self.con.execute
        x("INSERT INTO messages(sender,recipient,kind,subject,body,goal_id,task_id,created,read_at) VALUES"
          "('lead-a','owner','escalation','SUBJECT-VISIBLE','BODY-SECRET','G-1',NULL,?,NULL)", (ts(9, 0),))
        x("INSERT INTO plans(goal_id,version,body,author,created) VALUES('G-1',2,'PLAN-SECRET','pm',?)", (ts(9, 0),))
        x("UPDATE tasks SET spec='SPEC-SECRET', acceptance='ACC-SECRET' WHERE id='T-1'")
        x(E_INS, (ts(9, 5), "lead", "task", "T-1", "note", "NOTE-SECRET"))
        x(V_INS, ("T-1", "u", "dddddddd44", "qa", "pass", "VERDICT-SECRET", "qa-1", ts(9, 6)))
        pages = {p: self.page(p) for p in ("/goals", "/goal/G-1", "/task/T-1")}
        for p, page in pages.items():
            for secret in ("BODY-SECRET", "PLAN-SECRET", "SPEC-SECRET", "ACC-SECRET", "NOTE-SECRET", "VERDICT-SECRET"):
                self.assertNotIn(secret, page, f"{secret} on {p}")
        self.assertIn("SUBJECT-VISIBLE", pages["/goals"])

    def test_privacy_rules_stay_in_the_view_docstrings(self):
        self.assertIn("Never renders plan bodies", (ROOT / "web/views/goals.py").read_text())
        self.assertIn("Never renders specs, acceptance text, notes or verdict summaries", (ROOT / "web/views/tasks.py").read_text())


class TestCriteria(DetailCase):
    def test_criteria_counts_only_for_tagged_goals(self):
        x = self.con.execute
        x("INSERT INTO goals VALUES('G-80','p1','Tagged goal',NULL,'1. one\n2. two','active',3,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-80", "G-80", None, "[SC1] done part", "s", "a", "coder", "done", None, None, 1, ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-81", "G-80", None, "[SC2] open part", "s", "a", "coder", "todo", None, None, 0, ts(8, 0), ts(9, 0)))
        x("INSERT INTO goals VALUES('G-81','p1','Untagged goal',NULL,'1. one','active',3,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-82", "G-81", None, "plain task", "s", "a", "coder", "done", None, None, 1, ts(8, 0), ts(9, 0)))
        page = self.page("/goals")
        rows = {m.group(1): m.group(0) for m in re.finditer(r'<li class="goalrow">.*?<span class="mono lrow-id">(G-\d+)</span>.*?</li>', page, re.S)}
        self.assertIn("criteria 1/2", rows["G-80"])
        self.assertNotIn("criteria", rows["G-81"])


    def test_list_and_detail_agree_for_a_done_but_unmerged_task(self):
        x = self.con.execute
        x("INSERT INTO goals VALUES('G-82','p1','Unmerged goal',NULL,'1. one\n2. two','active',3,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-83", "G-82", None, "[SC1] done, never merged", "s", "a", "coder", "done", None, None, 1, ts(8, 0), ts(9, 0)))
        x(T_INS, ("T-84", "G-82", None, "[SC2] open", "s", "a", "coder", "todo", None, None, 0, ts(8, 0), ts(9, 0)))
        self.assertIn("criteria 1/2", self.page("/goals"))
        self.assertIn("1/2 criteria", self.page("/goal/G-82"))


class TestGoalsPage(DetailCase):
    def order(self, path):
        return re.findall(r'<span class="mono lrow-id">(G-\d+)</span>', re.split(r'data-key="goals-done"', self.page(path))[0])

    def test_sort_whitelist(self):
        x = self.con.execute
        x("INSERT INTO goals VALUES('G-90','p1','Zero','i',NULL,'active',0,'owner',NULL,?,?)", (ts(8, 0), ts(9, 0)))
        default = self.order("/goals")
        self.assertEqual(self.order("/goals?sort=nonsense"), default)
        self.assertEqual(self.order("/goals?sort=priority")[0], "G-90")
        self.assertEqual(self.order("/goals?sort=progress")[0], "G-1")      # the only goal with done work among the open ones

    def test_header_chips_and_sections(self):
        page = self.page("/goals")
        for word in ("Active", "Blocked", "Proposed", "Done", "Needs you", "Open goals"):
            self.assertIn(word, page)
        self.assertIn('data-key="goals-done"', page)

    def test_goal_detail_sections(self):
        page = self.page("/goal/G-1")
        for word in ("What&#x27;s blocking", "Success criteria", "Tasks", "Intent", "Teams", "v1"):
            self.assertIn(word, page)
        self.assertNotIn("What&#x27;s blocking", self.page("/goal/G-2"))


class TestContract(DetailCase):
    def test_api_goals_keeps_its_keys(self):
        status, _h, body = self.get("/api/goals.json")
        self.assertEqual(status, 200)
        self.assertTrue({"goals", "owner_pending", "internal_blocked"} <= set(json.loads(body)))

    def test_only_known_classes_and_no_inline_style(self):
        self.seed_three_attempts()
        for p in ("/goals", "/goal/G-1", "/task/T-1", "/task/T-70", "/goal/G-404"):
            page = self.page(p)
            used = {c for m in re.findall(r'class="([^"]*)"', page) for c in m.split()}
            self.assertEqual(used - wh.CLASSES, set(), p)
            self.assertNotIn("style=", page)

    def test_css_section_uses_tokens_only(self):
        css = (ROOT / "web/static/app.css").read_text()
        self.assertEqual(css.count("/* == B5 detail == */"), 1)
        sec = css.split("/* == B5 detail == */")[1]
        self.assertIsNone(re.search(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(", sec))


class TestSpeed(DetailCase):
    def test_median_render_under_200ms(self):
        self.seed_three_attempts()
        for p in ("/goals", "/goal/G-1", "/task/T-70"):
            runs = []
            for _ in range(5):
                t = time.perf_counter()
                self.page(p)
                runs.append(time.perf_counter() - t)
            self.assertLess(statistics.median(runs), 0.2, p)


if __name__ == "__main__":
    unittest.main()
