"""G-019 B3: shared list components (web/components.py) and the rebuilt /tasks, /agents and /changelog pages."""
import datetime as dt
import json
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_server import ServerCase  # noqa: E402
from web import components as wc  # noqa: E402
from web import html as wh  # noqa: E402
from web.html import Safe  # noqa: E402

NOW = dt.datetime(2026, 10, 24, 12, 0).astimezone()
XSS = "<script>x</script>"
# data() keys and task-row keys on origin/main before B3; B3 may only add keys
KEYS = {"tasks": {"filter", "goals", "statuses", "tasks"},
        "agents": {"counts", "blocked", "roles", "goals", "filters", "matches", "matches_total", "teams", "live", "queued",
                   "ended", "ended_total", "proc_scan"},
        "changelog": {"goal", "goals", "groups", "total"}}
TASK_ROW_KEYS = {"id", "goal_id", "title", "status", "waits", "pr_url", "ci", "rejections", "attempts"}


def text_of(html):
    return re.sub(r"<[^>]+>", " ", html)


class TestComponents(unittest.TestCase):
    def test_c4_data_table_groups_sorting_and_collapsed_disclosure(self):
        cols = [{"key": "id", "label": "ID"}, {"key": "title", "label": "Task", "sortable": True},
                {"key": "updated", "label": "Updated", "sortable": True}]
        groups = [{"key": "blocked", "label": "Blocked", "count": 1, "rows": [["T-1", XSS, "2 h"]]},
                  {"key": "done", "label": "Done", "count": 2, "rows": [["T-2", "b", "1 d"], ["T-3", "c", "2 d"]], "collapsed": True},
                  {"key": "todo", "label": "To do", "count": 0, "rows": []}]
        out = str(wc.data_table(cols, groups, sort=("updated", "desc"), sort_href=lambda k: f"/x?sort={k}", key="t"))
        self.assertEqual(out.count("<table"), 2)   # open groups share one table, the collapsed group has its own
        self.assertRegex(out, r'<th scope="col" aria-sort="descending"><a href="/x\?sort=updated"')
        self.assertEqual(out.count("aria-sort"), 1)
        self.assertRegex(out, r'<th colspan="3" scope="colgroup">Blocked <span class="grp-n">1</span>')
        self.assertNotIn("To do", out)   # empty groups are not drawn
        self.assertIn('<details class="disc" data-key="t-done">', out)
        self.assertIn("&lt;script&gt;x&lt;/script&gt;", out)
        self.assertNotIn("<script>", out)
        self.assertIn("Nothing here", text_of(str(wc.data_table(cols, [], empty="Nothing here"))))

    def test_c4_list_row_omits_empty_metadata_and_escapes(self):
        out = str(wc.list_row("in_review", "T-9", XSS, url="/task/T-9", meta=["#226", "", None, "2 h"]))
        self.assertEqual(out.count('<span class="lrow-meta">'), 1)
        self.assertEqual(re.findall(r"<span>([^<]*)</span>", out.split("lrow-meta")[1]), ["#226", "2 h"])
        self.assertIn('<a href="/task/T-9">&lt;script&gt;x&lt;/script&gt;</a>', out)
        self.assertNotIn("<script>", out)

    def test_c5_row_status_uses_the_label_map_not_the_raw_enum(self):
        quiet = str(wc.row_status("in_progress"))
        self.assertIn('<span class="sr-only">In progress</span>', quiet)
        self.assertNotIn("in_progress", quiet)
        self.assertIn('aria-hidden="true"', quiet)
        loud = str(wc.row_status("live", label=True))
        self.assertIn("Running", loud)
        self.assertNotIn(">live<", loud)

    def test_c6_chips_current_zero_and_segmented(self):
        out = str(wc.chips("Status", [("All", 3, "/t", False), ("To do", 3, "/t?status=todo", True), ("Done", 0, "/t?status=done", False)]))
        self.assertEqual(out.count('aria-current="true"'), 1)
        self.assertRegex(out, r'<a href="/t\?status=todo" class="chip" aria-current="true"><svg')
        self.assertRegex(out, r'class="chip chip--zero"[^>]*><span>Done</span> <span class="chip-n">0</span>')   # zero stays visible
        seg = str(wc.segmented("Range", [("7 days", "/r?d=7", False), ("14 days", "/r?d=14", True)]))
        self.assertEqual(seg.count('aria-current="true"'), 1)
        self.assertIn('aria-label="Range"', seg)

    def test_c13_feed_row_pr_number_short_sha_and_relative_time(self):
        out = str(wc.feed_row("Fix <b>", "/task/T-1", goal="G-1", pr_url="https://github.com/o/r/pull/226", sha="abcdef1234567",
                              when="3 h ago", when_title="2026-10-24T09:00"))
        self.assertIn(">#226<", out)
        self.assertIn(">abcdef12<", out)
        self.assertIn('title="2026-10-24T09:00" class="feed-when">3 h ago<', out)
        self.assertIn("Fix &lt;b&gt;", out)
        bare = str(wc.feed_row("t", "/task/T-1"))
        self.assertNotIn("feed-sha", bare)
        self.assertNotIn("#", text_of(bare))

    def test_c14_empty_state_with_and_without_action(self):
        self.assertEqual(str(wc.empty_state("Nothing needs you")), '<p class="empty emptybox">Nothing needs you</p>')
        out = str(wc.empty_state("No tasks match", ("/tasks", "Clear filters")))
        self.assertIn('<a href="/tasks">Clear filters</a>', out)

    def test_c15_disclosure_keeps_a_stable_key_and_open_state(self):
        out = str(wc.disclosure("Done", Safe("<p>x</p>"), key="tasks-done", count=28))
        self.assertIn('data-key="tasks-done"', out)
        self.assertRegex(out, r"<summary><span>Done</span><span class=\"disc-n\">28</span></summary>")
        self.assertNotIn(" open", out)
        self.assertIn(" open", str(wc.disclosure("Done", key="k", open=True)))

    def test_c17_agent_row_hides_role_equal_to_name_and_shows_last_event(self):
        a = {"name": "lead-a", "role": "lead", "status": "live", "task_id": "T-2", "task_title": "Build <it>",
             "last_event": "2026-10-24T11:56:00" }
        cells = wc.agent_row(a, NOW)
        self.assertEqual(len(cells), len(wc.AGENT_COLS))
        self.assertIn("agent-role", str(cells[1]))
        self.assertIn("rt-claude", str(cells[1]))  # default runtime chip when unset
        self.assertIn("Build &lt;it&gt;", str(cells[2]))
        self.assertEqual(cells[3], "4 m")
        boss = wc.agent_row({"name": "boss", "role": "boss", "status": "live"}, NOW)
        self.assertNotIn("agent-role", str(boss[1]))
        self.assertIn("Blocked", str(wc.agent_row(dict(a, blocked=True), NOW)[0]))
        cur = wc.agent_row({"name": "pm-a", "role": "pm", "status": "live", "runtime": "cursor"}, NOW)
        self.assertIn("rt-cursor", str(cur[1]))
        self.assertNotIn("rt-claude", str(cur[1]))

    def test_href_and_ago_helpers(self):
        self.assertEqual(wc.href("/tasks", {"status": "done", "goal": None, "sort": ""}), "/tasks?status=done")
        self.assertEqual(wc.href("/tasks", {}), "/tasks")
        self.assertEqual(wc.ago("2026-10-24T09:00:00", NOW), "3 h")
        self.assertEqual(wc.ago("junk", NOW), "")


class PageCase(ServerCase):
    def text(self, path):
        st, _, body = self.get(path)
        self.assertEqual(st, 200, path)
        return body.decode()

    def seed(self, n_todo=3, n_prog=2, n_blocked=1, live=2, queued=1):
        x = self.con.execute
        x("DELETE FROM verdicts"); x("DELETE FROM events"); x("DELETE FROM tasks"); x("DELETE FROM agents")
        x("INSERT OR REPLACE INTO config VALUES('max_live_agents','6')")
        i = 0
        for st, n in (("todo", n_todo), ("in_progress", n_prog), ("blocked", n_blocked)):
            for _ in range(n):
                i += 1
                x("INSERT INTO tasks(id,goal_id,team_id,title,role,status,attempts,created,updated) VALUES(?,?,?,?,?,?,?,?,?)",
                  (f"T-{i}", "G-1", "t-a", f"task {i}", "coder", st, 0, "2026-10-24T08:00:00", f"2026-10-24T09:{i:02d}:00"))
        for n, st in [(f"live{k}", "live") for k in range(live)] + [(f"que{k}", "queued") for k in range(queued)]:
            x("INSERT INTO agents(name,role,goal_id,team_id,status,started,queued_at) VALUES(?,?,?,?,?,?,?)",
              (n, "coder", "G-1", "t-a", st, "2026-10-24T10:00:00", "2026-10-24T10:30:00"))


class TestPages(PageCase):
    def test_tasks_markers_table_aria_sort_group_headers_and_chips(self):
        t = self.text("/tasks")
        self.assertIn('<table class="dt">', t)
        self.assertEqual(t.count("aria-sort="), 1)
        self.assertRegex(t, r'aria-sort="descending"><a href="/tasks\?sort=updated&amp;dir=asc"[^>]*>Updated')
        self.assertRegex(t, r'<tr class="grp"><th colspan="8" scope="colgroup">In review <span class="grp-n">1</span>')
        self.assertRegex(t, r'class="chip"[^>]*><span>In progress</span> <span class="chip-n">1<')
        self.assertIn('data-key="tasks-done"', t)   # Done is collapsed

    def test_agents_and_changelog_markers(self):
        a = self.text("/agents")
        self.assertIn('<table class="dt">', a)
        self.assertRegex(a, r'class="chip"[^>]*><span>Running</span> <span class="chip-n">2<')
        self.assertRegex(a, r'<tr class="grp"><th colspan="5" scope="colgroup">t-a · G-1 <span class="grp-n">2<')
        c = self.text("/changelog")
        self.assertIn('class="seg seg-links"', c)
        self.assertRegex(c, r'<li class="feed-row">')
        self.assertRegex(c, r'<h2>(Today|Yesterday|[A-Z][a-z]{2} \d\d) ')
        self.assertIn(">#1<", c)   # PR number, not the word PR
        by_goal = self.text("/changelog?group=goal")
        self.assertRegex(by_goal, r"<h2>G-1 - Goal one ")
        self.assertRegex(by_goal, r'aria-current="true">By goal<')

    def test_agents_use_labels_not_raw_enums(self):
        for n, st in (("x-live", "live"), ("x-q", "queued"), ("x-exit", "exited"), ("x-stop", "stopped")):
            self.con.execute("INSERT INTO agents(name,role,goal_id,team_id,status,started,ended,queued_at) VALUES(?,?,?,?,?,?,?,?)",
                             (n, "coder", "G-1", "t-a", st, "2026-10-24T10:00:00", "2026-10-24T10:30:00", "2026-10-24T10:00:00"))
        for path in ("/agents", "/agents?status=exited,stopped"):
            t = self.text(path)
            for raw in (">live<", ">queued<", ">exited<", ">stopped<", ">in_progress<", ">in_review<"):
                self.assertNotIn(raw, t, path)
            for word in ("Running", "Queued", "Ended"):
                self.assertIn(word, t, path)

    def test_old_urls_keep_working_with_the_chip_current(self):
        t = self.text("/tasks?status=in_review")
        self.assertRegex(t, r'aria-current="true"[^>]*>(<svg.*?</svg>)?<span>In review</span>')
        a = self.text("/agents?status=queued")
        self.assertRegex(a, r'aria-current="true"[^>]*>(<svg.*?</svg>)?<span>Queued</span>')
        self.assertEqual(self.get("/changelog?goal=G-1")[0], 200)
        self.assertIn("Filtered: goal=G-1", self.text("/tasks?goal=G-1"))

    def test_json_keys_are_only_added(self):
        for page, keys in KEYS.items():
            d = json.loads(self.get(f"/api/{page}.json")[2])
            self.assertLessEqual(keys, set(d), page)
        rows = json.loads(self.get("/api/tasks.json")[2])["tasks"]
        self.assertLessEqual(TASK_ROW_KEYS, set(rows[0]))

    def test_sort_is_a_whitelist_and_junk_falls_back_to_the_default(self):
        default = re.findall(r"<td class=\"c-id mono\">(T-\d+)", self.text("/tasks"))
        self.assertTrue(default)
        for q in ("sort=title;DROP%20TABLE%20tasks&dir=asc", "sort=nonexistent&dir=x", "sort=title&dir=up", "sort=updated"):
            t = self.text("/tasks?" + q)
            self.assertIn('aria-sort="descending"><a href="/tasks?sort=updated&amp;dir=asc"', t, q)
            self.assertEqual(re.findall(r"<td class=\"c-id mono\">(T-\d+)", t), default, q)
        self.assertEqual(self.con.execute("SELECT count(*) FROM tasks").fetchone()[0], 6)   # the table is still there
        t = self.text("/tasks?sort=title&dir=asc")
        self.assertIn('aria-sort="ascending"><a href="/tasks?sort=title&amp;dir=desc"', t)
        titles = re.findall(r'<td class="c-title"><span><a href="/task/T-\d+">([^<]*)</a>', t)
        self.assertGreater(len(titles), 1)
        for group in re.split(r'<tr class="grp">', t)[1:]:   # sorted within each status group
            g = re.findall(r'<td class="c-title"><span><a href="/task/T-\d+">([^<]*)</a>', group.split("</tbody>")[0])
            self.assertEqual(g, sorted(g, key=str.lower))

    def test_titles_and_agent_names_are_escaped(self):
        self.con.execute("UPDATE tasks SET title=? WHERE id='T-2'", (XSS,))
        self.con.execute("UPDATE agents SET name=? WHERE name='queued-x'", ('a"&<>b',))
        for path in ("/tasks", "/agents", "/changelog"):
            t = self.text(path)
            self.assertNotIn("<script>x", t, path)
        self.assertIn("&lt;script&gt;x", self.text("/tasks"))
        a = self.text("/agents")
        self.assertIn("a&quot;&amp;&lt;&gt;b", a)
        self.assertNotIn('a"&<>b', a)

    def test_every_details_has_a_unique_data_key(self):
        for path in ("/tasks", "/agents", "/changelog", "/tasks?status=done", "/agents?status=queued"):
            t = self.text(path)
            ds = re.findall(r"<details\b[^>]*>", t)
            self.assertTrue(all("data-key=" in d for d in ds), (path, ds))
            keys = [re.search(r'data-key="([^"]+)"', d).group(1) for d in ds]
            self.assertEqual(len(keys), len(set(keys)), path)

    def test_filtering_to_done_opens_the_done_group(self):
        t = self.text("/tasks?status=done")
        self.assertNotIn('data-key="tasks-done"', t)
        self.assertRegex(t, r'<th colspan="8" scope="colgroup">Done <span class="grp-n">2<')


class TestCounts(PageCase):
    def test_same_number_in_chip_group_header_and_summary(self):
        self.seed()
        t = self.text("/tasks")
        want = dict(self.con.execute("SELECT status, count(*) FROM tasks GROUP BY status"))
        self.assertEqual(want, {"todo": 3, "in_progress": 2, "blocked": 1})
        for st, label in (("todo", "To do"), ("in_progress", "In progress"), ("blocked", "Blocked")):
            n = want[st]
            self.assertIn(f"<span>{label}</span> <span class=\"chip-n\">{n}</span>", t)
            self.assertRegex(t, rf'scope="colgroup">{label} <span class="grp-n">{n}</span>')
        self.assertIn(">6 tasks<", t)
        self.assertIn('<span>All</span> <span class="chip-n">6</span>', t)
        for label in ("In review", "Done", "Cancelled"):   # zero-count chips stay visible
            self.assertRegex(t, rf'chip chip--zero"[^>]*><span>{label}</span> <span class="chip-n">0<')
        a = self.text("/agents")
        self.assertIn("2 running · cap 6 · 1 queued", a)
        self.assertIn('<span>Running</span> <span class="chip-n">2</span>', a)
        self.assertIn('<span>Queued</span> <span class="chip-n">1</span>', a)
        self.assertRegex(a, r'<h2>Running <span class="sect-n">2</span>')
        self.assertRegex(a, r'<h2>Queued <span class="sect-n">1</span>')
        self.assertRegex(a, r'scope="colgroup">t-a · G-1 <span class="grp-n">2<')

    def test_ended_chip_and_disclosure_show_the_same_number(self):
        from web.views import agents
        row = {"name": "new1", "role": "coder", "status": "exited", "task_id": None, "ended": "2026-10-24T11:00:00", "summary": ""}
        d = {"counts": {"exited": 2, "stopped": 1}, "blocked": 0, "filters": {}, "roles": {}, "goals": {},
             "ended": [row], "ended_total": 1}   # 3 ended overall, 1 inside the 24 h window
        chip = str(agents._chips(d)[0])
        self.assertIn('<span>Ended</span> <span class="chip-n">3</span>', chip)
        box = str(agents._ended(d, NOW))
        self.assertIn('<span>Ended</span><span class="disc-n">3</span>', box)
        self.assertIn("Showing the 1 that ended in the last 24 h.", box)

    def test_goal_filter_makes_every_count_follow_the_goal(self):
        self.seed()
        self.con.execute("UPDATE tasks SET goal_id='G-2' WHERE id IN ('T-1','T-2')")
        t = self.text("/tasks?goal=G-1")
        self.assertIn('<span>To do</span> <span class="chip-n">1</span>', t)
        self.assertIn(">4 tasks<", t)


class TestStatic(unittest.TestCase):
    def test_theme_js_keys_details_by_data_key_and_stays_small(self):
        js = (ROOT / "web" / "static" / "theme.js").read_text()
        self.assertIn("data-key", js)
        self.assertNotIn('querySelectorAll("details")[', js)
        self.assertLessEqual(len(js.splitlines()), 60)

    def test_b3_css_section_uses_tokens_only_and_no_fixed_row_heights(self):
        css = (ROOT / "web" / "static" / "app.css").read_text()
        self.assertEqual(css.count("/* == B3 lists == */"), 1)
        sec = css.split("/* == B3 lists == */")[1]
        self.assertIsNone(re.search(r"#[0-9a-fA-F]{3,8}\b|rgba?\(|hsla?\(", sec))
        self.assertIsNone(re.search(r"(?<![\w-])(min-|max-)?height:\s*[0-9]+px", sec))

    def test_views_never_interpolate_into_order_by(self):
        for f in ("views/tasks.py", "views/agents.py", "views/changelog.py", "components.py"):
            src = (ROOT / "web" / f).read_text()
            self.assertIsNone(re.search(r"(?i)order by[^\"\n]*\{", src), f)


if __name__ == "__main__":
    unittest.main()
