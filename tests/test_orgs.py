"""G-028: two orgs (claude, cursor) side by side — goal ownership, org-prefixed names, per-org runtime and models,
recipient routing inside an org, and the cross-org chain-of-command rule (docs/runtimes.md "Two orgs")."""
import json
import unittest

from harness import FactoryTestCase

from scout import orgs


class OrgRegistryTests(unittest.TestCase):
    def test_models_per_org(self):
        self.assertEqual(orgs.model("claude", "director"), "claude-opus-5-5")
        self.assertEqual(orgs.model("claude", "coder"), "claude-sonnet-5-5")
        self.assertEqual(orgs.model("cursor", "director"), "grok-4.7-high")
        self.assertEqual(orgs.model("cursor", "coder"), "composer-2.5")
        for role in orgs.JUDGEMENT_ROLES + orgs.BUILD_ROLES:
            self.assertNotIn("fable", orgs.model("cursor", role))
            self.assertNotIn("fable", orgs.model("claude", role))
        # the cursor org uses no Anthropic model except the skilleval evaluator (always Claude)
        self.assertEqual([r for r in orgs.ORGS["cursor"]["models"] if orgs.model("cursor", r).startswith("claude-")],
                         ["evaluator"])

    def test_names(self):
        self.assertEqual(orgs.name("cursor", "coder-t-0001"), "cursor-coder-t-0001")
        self.assertEqual(orgs.name("cursor", "cursor-coder-t-0001"), "cursor-coder-t-0001")
        self.assertEqual(orgs.prefix_of("claude-boss"), "claude")
        self.assertIsNone(orgs.prefix_of("boss"))
        self.assertEqual(orgs.strip_prefix("cursor-lead-g-001-a"), "lead-g-001-a")
        with self.assertRaises(ValueError):
            orgs.check("codex")

    def test_of_agent(self):
        self.assertEqual(orgs.of_agent({"name": "x", "org": "cursor"}), "cursor")
        self.assertEqual(orgs.of_agent({"name": "cursor-boss"}), "cursor")
        self.assertEqual(orgs.of_agent({"name": "lead-g-1", "spawn_args": json.dumps({"runtime": "cursor"})}), "cursor")
        self.assertEqual(orgs.of_agent({"name": "boss"}), "claude")


class GoalOrgTests(FactoryTestCase):
    def test_goal_new_org_list_and_move(self):
        gid = self.ok("goal", "new", "--title", "zz-test cursor goal", "--org", "cursor").stdout.strip()
        self.assertEqual(self.env.sql("SELECT org FROM goals WHERE id=?", (gid,))[0]["org"], "cursor")
        out = self.ok("goal", "list", "--org", "cursor").stdout
        self.assertIn(gid, out)
        self.assertNotIn(gid, self.ok("goal", "list", "--org", "claude").stdout)
        self.assertIn("org=cursor", self.ok("goal", "show", gid).stdout)
        self.ok("goal", "org", gid, "claude")
        self.assertEqual(self.env.sql("SELECT org FROM goals WHERE id=?", (gid,))[0]["org"], "claude")
        self.refused(2, "goal", "org", gid, "codex")

    def test_goal_new_defaults_to_claude(self):
        gid = self.ok("goal", "new", "--title", "zz-test default goal").stdout.strip()
        self.assertEqual(self.env.sql("SELECT org FROM goals WHERE id=?", (gid,))[0]["org"], "claude")

    def test_unknown_org_refused(self):
        self.refused(2, "goal", "new", "--title", "zz-test bad", "--org", "codex")


class SpawnOrgTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-002'")
        e.seed_team("g-002-a", goal="G-002", lead="cursor-lead-g-002-a")
        e.seed_agent("director", "director")
        fake = e.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho CURSOR_OK\n")
        fake.chmod(0o755)

    def test_cursor_goal_spawn_prefix_runtime_models(self):
        p = self.ok("spawn", "pm", "--goal", "G-002", "--team", "g-002-a", "--name", "cursor-pm-g-002-a", agent="director")
        self.assertIn("SPAWNED cursor-pm-g-002-a", p.stdout)
        row = self.env.sql("SELECT org, model, spawn_args FROM agents WHERE name='cursor-pm-g-002-a'")[0]
        self.assertEqual(row["org"], "cursor")
        sa = json.loads(row["spawn_args"])
        self.assertEqual(sa["runtime"], "cursor")
        self.assertEqual(sa["org"], "cursor")
        starts = [c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        argv = starts[-1]
        self.assertEqual(argv[argv.index("--kind") + 1], "cursor")
        self.assertEqual(argv[argv.index("--model") + 1], "grok-4.7-high")
        prompt = (self.env.home_dir / "run" / "cursor-pm-g-002-a.prompt.md").read_text()
        self.assertIn("**Cursor org**", prompt)
        self.assertIn("`cursor-boss`", prompt)

    def test_claude_goal_spawn_unchanged(self):
        p = self.ok("spawn", "pm", "--goal", "G-001", "--name", "pm-g-001-a", agent="director")
        self.assertIn("SPAWNED pm-g-001-a", p.stdout)
        row = self.env.sql("SELECT org, spawn_args FROM agents WHERE name='pm-g-001-a'")[0]
        self.assertEqual(row["org"], "claude")
        self.assertEqual(json.loads(row["spawn_args"]).get("runtime"), "claude")

    def test_org_prefix_default_on(self):
        self.ok("config", "org_prefix_default", "1")
        p = self.ok("spawn", "pm", "--goal", "G-001", "--name", "claude-pm-g-001-a", agent="director")
        self.assertIn("SPAWNED claude-pm-g-001-a", p.stdout)

    def test_resume_dropped_when_runtime_changes(self):
        """A legacy cursor lead respawned for a claude-org goal starts fresh (a cursor session id never resumes on claude)."""
        e = self.env
        e.seed_team("g-001-a", goal="G-001", lead="lead-g-001-a")
        e.seed_agent("lead-g-001-a", "lead", goal="G-001", team="g-001-a", status="stopped")
        e.sql("UPDATE agents SET session_id='cursor-sess-1', spawn_args=? WHERE name='lead-g-001-a'",
              (json.dumps({"runtime": "cursor"}),))
        self.ok("config", "org_prefix_default", "1")
        self.ok("up", "--no-boss")
        row = e.sql("SELECT spawn_args, session_id FROM agents WHERE name='claude-lead-g-001-a'")[0]
        self.assertEqual(json.loads(row["spawn_args"]).get("runtime"), "claude")
        self.assertNotEqual(row["session_id"], "cursor-sess-1")
        self.assertEqual(e.sql("SELECT lead_agent FROM teams WHERE id='g-001-a'")[0]["lead_agent"], "claude-lead-g-001-a")
        starts = [c["argv"] for c in e.calls("herdr") if c["argv"][:2] == ["agent", "start"]]
        self.assertFalse(any("cursor-sess-1" in a for a in starts))

    def test_spawn_org_must_match_goal(self):
        self.refused(2, "spawn", "pm", "--goal", "G-001", "--org", "cursor", agent="director")


class RoutingTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.sql("UPDATE goals SET org='cursor' WHERE id='G-002'")
        e.seed_team("g-001-a", goal="G-001", lead="lead-g-001-a")
        e.seed_team("g-002-a", goal="G-002", lead="cursor-lead-g-002-a")
        for name, role, goal, team in (("boss", "boss", None, None), ("director", "director", None, None),
                                       ("cursor-boss", "boss", None, None), ("cursor-director", "director", None, None),
                                       ("lead-g-001-a", "lead", "G-001", "g-001-a"),
                                       ("cursor-lead-g-002-a", "lead", "G-002", "g-002-a")):
            e.seed_agent(name, role, goal=goal, team=team)
        e.sql("UPDATE agents SET org='cursor' WHERE name LIKE 'cursor-%'")
        e.sql("UPDATE agents SET org='claude' WHERE org IS NULL")

    def last_rcpt(self):
        return self.env.sql("SELECT recipient FROM messages ORDER BY id DESC LIMIT 1")[0]["recipient"]

    def test_bare_roles_resolve_inside_sender_org(self):
        self.ok("msg", "director", "zz-test s", "b", agent="cursor-lead-g-002-a")
        self.assertEqual(self.last_rcpt(), "cursor-director")
        self.ok("msg", "director", "zz-test s", "b", agent="lead-g-001-a")
        self.assertEqual(self.last_rcpt(), "director")
        self.ok("msg", "owner", "zz-test s", "b", agent="cursor-director")
        self.assertEqual(self.last_rcpt(), "cursor-boss")

    def test_goal_routes_to_goal_org(self):
        self.ok("msg", "boss", "zz-test s", "b", "--goal", "G-002", agent="director")
        self.assertEqual(self.last_rcpt(), "cursor-boss")

    def test_directors_talk_across_orgs(self):
        self.ok("msg", "cursor-director", "zz-test capacity", "b", agent="director")
        self.assertEqual(self.last_rcpt(), "cursor-director")
        self.ok("msg", "director", "zz-test ack", "b", agent="cursor-director")
        self.assertEqual(self.last_rcpt(), "cursor-director")  # bare role = own org
        self.ok("msg", "director", "zz-test fyi", "b", agent="cursor-boss")
        self.assertEqual(self.last_rcpt(), "cursor-director")

    def test_lead_may_not_cross_orgs(self):
        p = self.refused(3, "msg", "lead-g-001-a", "zz-test s", "b", agent="cursor-lead-g-002-a")
        self.assertIn("only the boss and director talk across orgs", p.stderr)
        p = self.refused(3, "msg", "cursor-director", "zz-test s", "b", agent="lead-g-001-a")
        self.assertIn("across orgs", p.stderr)


class LeadBareHqRoutingTests(RoutingTests):
    """T-1075: a lead or worker's bare director/boss/owner is its OWN org's, whatever --goal says."""

    def test_claude_lead_bare_hq_with_cursor_goal_stays_in_own_org(self):
        for to, want in (("director", "director"), ("boss", "boss"), ("owner", "boss")):
            self.ok("msg", to, "zz-test s", "b", "--goal", "G-002", agent="lead-g-001-a")
            self.assertEqual(self.last_rcpt(), want)

    def test_cursor_lead_bare_director_with_claude_goal_stays_in_cursor(self):
        self.ok("msg", "director", "zz-test s", "b", "--goal", "G-001", agent="cursor-lead-g-002-a")
        self.assertEqual(self.last_rcpt(), "cursor-director")

    def test_worker_bare_director_with_other_org_goal_stays_in_own_org(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a")
        self.env.sql("UPDATE agents SET org='claude' WHERE name='zz-test-coder'")
        self.ok("msg", "director", "zz-test s", "b", "--goal", "G-002", agent="zz-test-coder")
        self.assertEqual(self.last_rcpt(), "director")

    def test_explicit_other_org_agent_still_refused(self):
        p = self.refused(3, "msg", "cursor-director", "zz-test s", "b", "--goal", "G-002", agent="lead-g-001-a")
        self.assertIn("across orgs", p.stderr)
        p = self.refused(3, "msg", "cursor-lead-g-002-a", "zz-test s", "b", "--goal", "G-001", agent="lead-g-001-a")
        self.assertIn("across orgs", p.stderr)

    def test_director_with_cursor_goal_still_resolves_to_cursor_director(self):
        self.ok("msg", "director", "zz-test s", "b", "--goal", "G-002", agent="director")
        self.assertEqual(self.last_rcpt(), "cursor-director")

    def test_lead_report_to_director_with_cursor_goal_stays_in_own_org(self):
        self.ok("report", "zz-test h", "b", "--goal", "G-002", agent="lead-g-001-a")
        self.assertEqual(self.last_rcpt(), "director")


class OrgPrefixDefaultRoutingTests(RoutingTests):
    """Bare boss/director under org_prefix_default=1: legacy unprefixed HQ rows still receive mail."""

    def setUp(self):
        super().setUp()
        self.ok("config", "org_prefix_default", "1")

    def test_default_org_bare_hq_reaches_legacy_row(self):
        e = self.env
        e.sql("DELETE FROM agents WHERE name IN ('claude-director', 'claude-boss')")
        self.ok("msg", "director", "zz-test s", "b", agent="lead-g-001-a")
        self.assertEqual(self.last_rcpt(), "director")
        self.ok("msg", "boss", "zz-test s", "b", agent="director")
        self.assertEqual(self.last_rcpt(), "boss")

    def test_live_prefixed_hq_wins_over_legacy(self):
        e = self.env
        e.seed_agent("claude-director", "director")
        e.seed_agent("claude-boss", "boss")
        self.ok("msg", "director", "zz-test s", "b", agent="lead-g-001-a")
        self.assertEqual(self.last_rcpt(), "claude-director")
        self.assertNotEqual(self.last_rcpt(), "cursor-director")

    def test_stopped_prefixed_row_falls_back_to_legacy(self):
        e = self.env
        e.seed_agent("claude-director", "director", status="stopped")
        self.ok("msg", "director", "zz-test s", "b", agent="lead-g-001-a")
        self.assertEqual(self.last_rcpt(), "director")

    def test_cursor_hq_unchanged_under_org_prefix_default(self):
        self.ok("msg", "director", "zz-test s", "b", agent="cursor-lead-g-002-a")
        self.assertEqual(self.last_rcpt(), "cursor-director")
        self.ok("msg", "cursor-director", "zz-test s", "b", agent="director")
        self.assertEqual(self.last_rcpt(), "cursor-director")
        self.ok("msg", "cursor-boss", "zz-test s", "b", agent="director")
        self.assertEqual(self.last_rcpt(), "cursor-boss")


if __name__ == "__main__":
    unittest.main()
