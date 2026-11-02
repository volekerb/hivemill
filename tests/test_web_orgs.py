"""Two orgs side by side in the web UI (G-028): the ?org= filter, org chips, overview lanes, org columns on goals /
tasks / agents / teams, the resources mix and tokens by org. Every view also works on a DB without the org columns."""
import json
import re
import sys
import unittest
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))
sys.path.insert(0, str(ROOT / "tests"))

from test_web_queries import NOW, ts  # noqa: E402
from test_web_views_agents import Base  # noqa: E402
from test_web_views_tokens import TokensCase  # noqa: E402
from web import components as wc, html as wh, queries  # noqa: E402
from web.views import agents, changelog, goals, home, quality, resources, tasks, throughput, tokens  # noqa: E402

CLASS_RE = re.compile(r'class="([^"]+)"')


def add_parked_at(w):
    from web.db import has_column
    if not has_column(w, "teams", "parked_at"):
        w.execute("ALTER TABLE teams ADD COLUMN parked_at TEXT")


def add_org_columns(w):
    """The CLI side's migration: goals.org (NOT NULL DEFAULT 'claude') and agents.org (NULL on legacy rows)."""
    w.execute("ALTER TABLE goals ADD COLUMN org TEXT NOT NULL DEFAULT 'claude'")
    w.execute("ALTER TABLE agents ADD COLUMN org TEXT")


def seed_cursor_org(w):
    """G-3 and G-4 move to the cursor org, which gets a boss, a director, a team with a live lead and one live worker."""
    add_org_columns(w)
    w.execute("UPDATE goals SET org='cursor', status='active' WHERE id='G-4'")
    w.execute("UPDATE goals SET org='cursor' WHERE id='G-3'")
    w.execute("INSERT INTO teams VALUES('t-c','G-4','scope C','cursor-lead-g-4-a',NULL,'active',?)", (ts(8, 0),))
    w.execute("INSERT INTO tasks(id,goal_id,team_id,title,role,status,created,updated) VALUES('T-9','G-4','t-c','cursor work','coder','in_progress',?,?)",
              (ts(8, 0), ts(9, 0)))
    for name, role, goal, team, task, model in [
            ("cursor-boss", "boss", None, None, None, "grok-4.7-high"),
            ("cursor-director", "director", None, None, None, "grok-4.7-high"),
            ("cursor-lead-g-4-a", "lead", "G-4", "t-c", None, "grok-4.7-high"),
            ("cursor-coder-t-9", "coder", "G-4", "t-c", "T-9", "composer-2.5")]:
        w.execute("INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,started,org) VALUES(?,?,?,?,?,'live',?,?,'cursor')",
                  (name, role, goal, team, task, model, ts(10, 0)))
    w.execute("INSERT INTO agents(name,role,status,started) VALUES('boss','boss','live',?)", (ts(7, 0),))   # legacy claude boss


def classes(html):
    return {c for m in CLASS_RE.findall(html) for c in m.split()}


class TestOrgParam(unittest.TestCase):
    def test_whitelist(self):
        self.assertEqual(queries.org_param({"org": "claude"}), "claude")
        self.assertEqual(queries.org_param({"org": "cursor"}), "cursor")
        for bad in ({"org": "Cursor"}, {"org": "evil"}, {"org": ""}, {"org": "cursor "}, {"org": None}, {"org": ["cursor"]},
                    {}, None, "org=cursor"):
            self.assertIsNone(queries.org_param(bad), bad)

    def test_agent_org_order(self):
        self.assertEqual(queries.agent_org("cursor", "claude-coder-t-1", "claude"), "cursor")   # the column wins
        self.assertEqual(queries.agent_org(None, "cursor-coder-t-1", None), "cursor")           # then the prefix
        self.assertEqual(queries.agent_org(None, "coder-t-1", "cursor"), "cursor")             # then an explicit runtime
        self.assertEqual(queries.agent_org(None, "lead-g-4-a", None, "cursor"), "cursor")      # then the goal's org
        self.assertEqual(queries.agent_org("bogus", "boss", None), "claude")                    # else the default


class TestOrgChip(unittest.TestCase):
    def test_chip_per_org_and_unknown_is_default(self):
        self.assertEqual(str(wc.org_badge("cursor")), '<span class="org org-cursor" title="Cursor org">Cursor</span>')
        self.assertIn("org-claude", str(wc.org_badge("claude")))
        self.assertIn("org-claude", str(wc.org_badge("<script>")))
        self.assertNotIn("<script>", str(wc.org_badge("<script>")))

    def test_runtime_chip_only_when_it_differs_from_the_org(self):
        self.assertNotIn("rt-", str(wc.org_badge("cursor", "cursor")))
        self.assertNotIn("rt-", str(wc.org_badge("claude", "claude")))
        both = str(wc.org_badge("cursor", "claude"))        # e.g. the cursor org's evaluator runs on Claude
        self.assertIn("org-cursor", both)
        self.assertIn("rt rt-claude", both)

    def test_org_classes_are_whitelisted_and_styled(self):
        css = (ROOT / "web" / "static" / "app.css").read_text()
        for c in ("org", "org-claude", "org-cursor", "org-dot-claude", "org-dot-cursor", "orgseg", "lane", "lane--claude",
                  "lane--cursor"):
            self.assertIn(c, wh.CLASSES)
            self.assertRegex(css, r"\." + re.escape(c) + r"(?![\w-])")


class TestShellFilter(unittest.TestCase):
    SHELL = {"needs": 0, "agents_attention": 0, "health": None, "now": NOW}

    def test_filter_marks_the_current_org_and_nav_keeps_it(self):
        html = wh.page("Agents", "<p>x</p>", "/agents", shell=self.SHELL, org="cursor", path="/agents", query={"org": "cursor"})
        nav = re.search(r'<nav class="seg seg-links orgseg" aria-label="Org">(.*?)</nav>', html).group(1)
        self.assertEqual(re.findall(r'href="([^"]+)"', nav), ["/agents", "/agents?org=claude", "/agents?org=cursor"])
        self.assertRegex(nav, r'href="/agents\?org=cursor" aria-current="true"')
        self.assertIn('href="/goals?org=cursor"', html)       # sidebar / tabs keep the filter
        self.assertIn('href="/tokens?org=cursor"', html)

    def test_unknown_org_is_all_and_never_echoed(self):
        html = wh.page("Goals", "<p>x</p>", "/goals", shell=self.SHELL, org="evil", path="/goals",
                       query={"org": "evil", "sort": "<b>"})
        nav = re.search(r'<nav class="seg seg-links orgseg" aria-label="Org">(.*?)</nav>', html).group(1)
        self.assertRegex(nav, r'<a href="/goals" aria-current="true">All</a>')
        self.assertNotIn("evil", html)
        self.assertNotIn("<b>", html)
        self.assertNotIn("?org=", re.sub(r'<nav class="seg seg-links orgseg".*?</nav>', "", html, flags=re.S))

    def test_no_filter_without_a_path_or_shell(self):
        self.assertNotIn("orgseg", wh.page("Error", "<p>x</p>", "", refresh=False))


class TestLegacyDb(Base):
    """A DB from before goals.org / agents.org: everything is in the claude org, and nothing breaks."""

    def test_goals_have_default_org_and_render_a_chip(self):
        d, html = self.page(goals)
        self.assertEqual({g["org"] for g in d["goals"]}, {"claude"})
        self.assertEqual(d["orgs"], {"claude": 4, "cursor": 0})
        self.assertIn('class="org org-claude"', html)
        self.assertNotIn("org-cursor\"", html.replace('class="org-cursor"', ""))   # only the facet chip, no cursor goal

    def test_home_has_no_lanes_with_one_org_but_a_lane_when_picked(self):
        d, html = self.page(home)
        self.assertEqual(d["lanes"], [])
        self.assertNotIn('class="lanes"', html)
        d, html = self.page(home, query={"org": "claude"})
        self.assertEqual([ln["org"] for ln in d["lanes"]], ["claude"])
        self.assertIn('class="lane lane--claude"', html)

    def test_cursor_filter_is_empty_not_an_error(self):
        for view in (home, goals, tasks, agents, resources):
            d, html = self.page(view, query={"org": "cursor"})
            json.dumps(d)
        d, _ = self.page(goals, query={"org": "cursor"})
        self.assertEqual(d["goals"], [])
        d, _ = self.page(tasks, query={"org": "cursor"})
        self.assertEqual(d["tasks"], [])


class TestTwoOrgs(Base):
    def setUp(self):
        super().setUp()
        seed_cursor_org(self.w)

    def test_home_shows_both_lanes(self):
        d, html = self.page(home)
        lanes = {ln["org"]: ln for ln in d["lanes"]}
        self.assertEqual(list(lanes), ["claude", "cursor"])
        cur = lanes["cursor"]
        self.assertEqual((cur["boss"]["name"], cur["boss"]["status"]), ("cursor-boss", "live"))
        self.assertEqual(cur["director"]["name"], "cursor-director")
        self.assertEqual((cur["teams_active"], cur["leads_awake"], cur["leads_parked"], cur["leads_waking"],
                          cur["workers_live"], cur["goals_active"]), (1, 1, 0, 0, 1, 1))
        cl = lanes["claude"]
        self.assertEqual(cl["boss"]["name"], "boss")              # the legacy unprefixed boss is the claude org's
        self.assertIsNone(cl["director"])
        self.assertEqual(cl["health"]["tone"], "warning")         # active goal, no director
        self.assertEqual((cl["teams_active"], cl["leads_awake"], cl["leads_parked"], cl["leads_waking"],
                          cl["workers_live"]), (1, 1, 0, 0, 1))   # t-a (lead-a live), coder-2
        self.assertIn('class="lane lane--claude"', html)
        self.assertIn('class="lane lane--cursor"', html)
        self.assertIn('href="/agents?org=cursor"', html)
        self.assertLess(set(), classes(html))
        self.assertEqual(classes(html) - wh.CLASSES, set())

    def test_overview_lane_parked_lead_still_counts_active_team(self):
        add_parked_at(self.w)
        self.w.execute("UPDATE teams SET parked_at=? WHERE id='t-c'", (ts(9, 0),))
        self.w.execute("UPDATE agents SET status='exited', ended=? WHERE name='cursor-lead-g-4-a'", (ts(9, 30),))
        d, html = self.page(home)
        cur = next(ln for ln in d["lanes"] if ln["org"] == "cursor")
        self.assertEqual(cur["teams_active"], 1)
        self.assertIn("1 teams · 0 lead awake · 1 parked · 0 waking · 1 workers", html)
        self.assertNotIn("0 active teams", html)

    def test_overview_lane_three_lead_states_at_once(self):
        add_parked_at(self.w)
        self.w.execute(
            "INSERT INTO teams(id, goal_id, scope, lead_agent, tab_id, status, created) "
            "VALUES('t-c2','G-4','scope C2','cursor-lead-g-4-b',NULL,'active',?)", (ts(8, 0),))
        self.w.execute(
            "INSERT INTO teams(id, goal_id, scope, lead_agent, tab_id, status, created) "
            "VALUES('t-c3','G-4','scope C3','cursor-lead-g-4-c',NULL,'active',?)", (ts(8, 0),))
        self.w.execute("UPDATE teams SET parked_at=? WHERE id='t-c2'", (ts(9, 0),))
        self.w.execute(
            "INSERT INTO agents(name,role,goal_id,team_id,status,model,started,ended,org) "
            "VALUES('cursor-lead-g-4-b','lead','G-4','t-c2','exited','grok-4.7-high',?,?, 'cursor')",
            (ts(8, 0), ts(9, 0)))
        self.w.execute(
            "INSERT INTO agents(name,role,goal_id,team_id,status,model,started,queued_at,org) "
            "VALUES('cursor-lead-g-4-c','lead','G-4','t-c3','queued','grok-4.7-high',?,?, 'cursor')",
            (ts(8, 0), ts(9, 0)))
        d, _ = self.page(home)
        cur = next(ln for ln in d["lanes"] if ln["org"] == "cursor")
        self.assertEqual((cur["leads_awake"], cur["leads_parked"], cur["leads_waking"]), (1, 1, 1))

    def test_overview_workers_counted_on_org_lane_only(self):
        d, _ = self.page(home)
        lanes = {ln["org"]: ln for ln in d["lanes"]}
        self.assertEqual(lanes["cursor"]["workers_live"], 1)
        self.assertEqual(lanes["claude"]["workers_live"], 1)
        self.w.execute(
            "INSERT INTO agents(name,role,goal_id,team_id,task_id,status,model,started,org) "
            "VALUES('claude-only-coder','coder','G-1','t-a','T-2','live','sonnet',?,'claude')",
            (ts(11, 0),))
        d, _ = self.page(home)
        lanes = {ln["org"]: ln for ln in d["lanes"]}
        self.assertEqual(lanes["claude"]["workers_live"], 2)
        self.assertEqual(lanes["cursor"]["workers_live"], 1)

    def test_home_with_org_shows_only_that_lane_and_its_goals(self):
        d, html = self.page(home, query={"org": "cursor"})
        self.assertEqual([ln["org"] for ln in d["lanes"]], ["cursor"])
        self.assertEqual({g["id"] for g in d["goals"]}, {"G-3", "G-4"})
        self.assertEqual({r["name"] for r in d["agents"]["rows"]} - {"cursor-boss", "cursor-director", "cursor-lead-g-4-a",
                                                                     "cursor-coder-t-9"}, set())
        self.assertTrue(all(i["org"] == "cursor" for i in d["needs_you"]))
        self.assertNotIn("lane--claude", html)
        self.assertIn("Showing the Cursor org only", html)
        self.assertEqual(d["system"]["agents"]["live"], 7)        # capacity stays shared: every live agent counts

    def test_agents_org_chips_filter_and_groups(self):
        d, html = self.page(agents)
        self.assertEqual(d["live_by_org"]["cursor"], 4)
        self.assertIn('<span class="org org-cursor" title="Cursor org">Cursor</span> <span>t-c · G-4</span>', html)
        self.assertNotIn('class="rt ', html)                       # org implies runtime: no second chip
        self.assertIn('<span class="facet-label">Org</span>', html)
        d, html = self.page(agents, query={"org": "cursor"})
        self.assertEqual({a["org"] for a in d["live"]}, {"cursor"})
        self.assertEqual([t["id"] for t in d["teams"]], ["t-c"])
        self.assertIn("Cursor org: 4 running", html)
        self.assertIn('href="/agents?status=live&amp;org=cursor"', html.replace("%2C", ","))
        d, _ = self.page(agents, query={"org": "evil"})
        self.assertIsNone(d["org"])

    def test_goals_org_chip_and_filter(self):
        d, html = self.page(goals)
        self.assertEqual({g["id"]: g["org"] for g in d["goals"]}["G-4"], "cursor")
        self.assertEqual(d["orgs"], {"claude": 2, "cursor": 2})
        row = re.search(r'<li class="goalrow">.*?G-4.*?</li>', html, re.S).group(0)
        self.assertIn("org-cursor", row)
        d, html = self.page(goals, query={"org": "cursor", "sort": "priority"})
        self.assertEqual({g["id"] for g in d["goals"]}, {"G-3", "G-4"})
        self.assertIn('href="/goals?sort=progress&amp;org=cursor"', html)
        dd = goals.detail.data(self.ctx(), id="G-4")
        self.assertEqual(dd["org"], "cursor")
        self.assertIn("org-cursor", str(goals.detail.render(self.ctx(), dd)))

    def test_tasks_org_from_goal_and_filter(self):
        d, html = self.page(tasks)
        self.assertEqual({t["id"]: t["org"] for t in d["tasks"]}["T-9"], "cursor")
        self.assertEqual({t["id"]: t["org"] for t in d["tasks"]}["T-1"], "claude")
        d, html = self.page(tasks, query={"org": "cursor"})
        self.assertEqual([t["id"] for t in d["tasks"]], ["T-9"])
        self.assertEqual(d["goals"], ["G-3", "G-4"])
        self.assertIn("org=cursor", html)
        d, _ = self.page(tasks, query={"org": "cursor", "goal": "G-1"})   # a goal of the other org is not a filter
        self.assertIsNone(d["filter"]["goal"])

    def test_teams_take_their_goals_org(self):
        con = self.ctx().db
        self.assertEqual({t["id"]: t["org"] for t in queries.teams(con)}, {"t-a": "claude", "t-b": "claude", "t-c": "cursor"})

    def test_resources_mix_per_org(self):
        d, html = self.page(resources)
        mix = {m["org"]: m for m in d["gate"]["orgs"]}
        self.assertEqual((mix["cursor"]["live"], mix["cursor"]["workers"], mix["cursor"]["leads"], mix["cursor"]["hq"]), (4, 1, 1, 2))
        self.assertEqual(mix["cursor"]["runtimes"], {"cursor": 4})
        self.assertEqual(mix["claude"]["live"], 3)
        self.assertIn("agents live 7/6 (Claude 3 · Cursor 4)", d["gate"]["checks"][0])
        self.assertIn("Agents by org", html)
        _, html = self.page(resources, query={"org": "cursor"})
        box = html.split("Agents by org")[1].split("</section>")[0]
        self.assertIn("org-cursor", box)
        self.assertNotIn("org-claude", box)

    def test_agents_org_column_wins_over_name_and_legacy_goal_fallback(self):
        self.w.execute("INSERT INTO agents(name,role,goal_id,status,started) VALUES('lead-g-4-b','lead','G-4','live',?)", (ts(10, 0),))
        rows = {a["name"]: a for a in queries.agents(self.ctx().db)}
        self.assertEqual(rows["lead-g-4-b"]["org"], "cursor")      # unprefixed, no org column value: its goal's org
        self.assertEqual(rows["lead-g-4-b"]["runtime"], "cursor")
        self.assertEqual(rows["lead-a"]["org"], "claude")


class TestTokensByOrg(TokensCase):
    def setUp(self):
        super().setUp()
        add_org_columns(self.w)
        # s2 (opus 500k in) is lead-a's session: make it a cursor-org agent that ran on the Claude runtime
        self.w.execute("""UPDATE agents SET org='cursor', spawn_args='{"runtime":"claude"}' WHERE name='lead-a'""")
        self.w.execute("INSERT INTO agents(name,role,goal_id,status,model,started,org) "
                       "VALUES('cursor-coder-t-9','coder','G-1','live','composer-2.5',?,'cursor')", (ts(10, 0),))

    def test_by_org_breakdown(self):
        d, html = self.page(days="all", by="org")
        rows = {e["key"]: e for e in d["by_org"][0]}
        self.assertEqual(set(rows), {"claude", "cursor"})
        self.assertEqual(rows["cursor"]["input"], 500_000)
        self.assertEqual(rows["cursor"]["cursor_sessions"], 1)
        self.assertEqual(rows["cursor"]["cursor_hours"], 2.0)
        self.assertEqual(rows["claude"]["input"] + rows["cursor"]["input"], d["totals"]["input"])
        self.assertIn('aria-current="true">By org</a>', html)
        self.assertIn("Cursor agent-hours", html)
        self.assertIn("not reported by Cursor CLI", html)

    def test_cursor_activity_has_hours_per_model_and_no_cost(self):
        d, html = self.page(days="7")
        self.assertEqual(d["cursor"], [{"org": "cursor", "model": "composer-2.5", "sessions": 1, "live": 1, "hours": 2.0}])
        box = html.split("<h2>Cursor runtime")[1].split("</section>")[0]
        self.assertIn("composer-2.5", box)
        self.assertIn("not reported", box)
        self.assertNotIn("$", box)

    def test_org_filter_on_tokens(self):
        d, html = self.page(days="all", org="cursor")
        self.assertEqual(d["totals"]["input"], 500_000)            # only lead-a's session
        self.assertNotIn("Claude usage", html.split("<h2>Cursor runtime")[0])
        self.assertIn('href="/tokens?days=all&amp;by=org&amp;org=cursor"', html)
        d, _ = self.page(days="all", org="claude")
        self.assertNotIn("composer-2.5", json.dumps(d["cursor"]))
        self.assertEqual(d["totals"]["input"] + 500_000, self.page(days="all")[0]["totals"]["input"])

    def test_old_db_and_bad_org(self):
        d, html = self.page(days="all", org="<x>", by="org")
        self.assertIsNone(d["org"])
        self.assertNotIn("<x>", html)


def seed_insight_org_merges(w):
    """One merged task on a claude goal (T-1/G-1) and one on a cursor goal (T-c/G-4)."""
    add_org_columns(w)
    w.execute("UPDATE goals SET org='cursor' WHERE id='G-4'")
    w.execute("INSERT INTO teams VALUES('t-c','G-4','scope C','cursor-lead',NULL,'active',?)", (ts(8, 0),))
    w.execute("INSERT INTO tasks(id,goal_id,team_id,title,role,status,created,updated) VALUES('T-c','G-4','t-c','cursor merge','coder','done',?,?)",
              (ts(8, 0), ts(9, 0)))
    w.execute("INSERT INTO events(ts,actor,entity,entity_id,action,detail) VALUES(?,?,?,?,?,?)",
              (ts(11, 40), "judge", "task", "T-c", "merged", "@cafe0001"))


class TestInsightOrgFilter(Base):
    def setUp(self):
        super().setUp()
        seed_insight_org_merges(self.w)

    def test_throughput_org_filter_and_links(self):
        base, _ = self.page(throughput)
        self.assertEqual(base["merges"], 3)   # T-1, T-5 (claude goals) and T-c (cursor)
        cur, _ = self.page(throughput, query={"org": "cursor"})
        self.assertEqual(cur["merges"], 1)
        cl, _ = self.page(throughput, query={"org": "claude"})
        self.assertEqual(cl["merges"], 2)
        evil, html_e = self.page(throughput, query={"org": "evil"})
        self.assertEqual(evil["merges"], 3)
        self.assertNotIn("evil", html_e)
        self.assertIn("org=cursor", self.page(throughput, query={"org": "cursor", "days": "7"})[1])

    def test_quality_merged_count_by_org(self):
        cur, _ = self.page(quality, query={"org": "cursor"})
        self.assertEqual(cur["overall"]["merged"], 1)

    def test_quality_org_filter_keeps_team_with_only_unassigned_merges(self):
        """Team on a goal with no direct tasks still gets team_id-NULL merges after ?org= filter."""
        self.w.execute("INSERT INTO teams VALUES('t-g2','G-2','scope G2','lead-g2',NULL,'active',?)", (ts(8, 0),))
        all_d, _ = self.page(quality)
        self.assertEqual({t["team_id"]: t["merged"] for t in all_d["teams"]}["t-g2"], 1)
        cl, _ = self.page(quality, query={"org": "claude"})
        by_team = {t["team_id"]: t for t in cl["teams"]}
        self.assertIn("t-g2", by_team)
        self.assertEqual(by_team["t-g2"]["merged"], 1)
        self.assertEqual(cl["overall"]["merged"], 2)

    def test_changelog_org_and_chip_href(self):
        cur, html = self.page(changelog, query={"org": "cursor"})
        gids = {g["goal_id"] for g in cur["goals"]}
        self.assertIn("G-4", gids)
        self.assertNotIn("G-1", gids)
        self.assertIn("org=cursor", html)


class TestInsightOrgFilterLegacy(Base):
    """Legacy DB (no goals.org): claude filter matches all merges; cursor filter is empty."""

    def test_legacy_db_throughput_org(self):
        all_d, _ = self.page(throughput)
        n_all = all_d["merges"]
        cur, _ = self.page(throughput, query={"org": "cursor"})
        self.assertEqual(cur["merges"], 0)
        cl, _ = self.page(throughput, query={"org": "claude"})
        self.assertEqual(cl["merges"], n_all)


if __name__ == "__main__":
    unittest.main()
