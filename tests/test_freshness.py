"""Tick freshness check (G-011 SC2): live agents vs the current role files, settings, plugins and CLI. See docs/freshness.md."""
import json
import os
import shutil
import unittest

from harness import REPO, FactoryTestCase, git

OLD, NEW = 1_700_000_000, 1_800_000_000  # file mtimes; agents start at the harness default 2026-01-01 (1_767_225_600)


class FreshBase(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        self.roles = e.home_dir / "roles"
        self.roles.unlink()
        shutil.copytree(REPO / "roles", self.roles, symlinks=True)  # a private copy: tests edit it
        e.seed_goal("G-001")
        e.seed_goal("G-002")
        e.seed_team("g-001-a", "G-001", lead="zz-test-lead")
        e.set_herdr(agents=[])
        self.ok("tick")  # renders the gitignored legacy settings files, so they exist before the mtimes are pinned
        self.pin_old()
        self.panes = []
        self.agent("zz-test-director", "director")

    def pin_old(self):
        for d, _dirs, files in os.walk(self.roles):
            for f in files:
                p = os.path.join(d, f)
                if not os.path.islink(p):
                    os.utime(p, (OLD, OLD))

    def touch(self, rel):
        os.utime(self.roles / rel, (NEW, NEW))

    def prompt(self, role):
        return (self.roles / "_common.md").read_text() + "\n\n" + (self.roles / f"{role}.md").read_text()

    def agent(self, name, role, goal=None, team=None, prompt=True):
        self.env.seed_agent(name, role, goal=goal, team=team, pane_id=f"p-{name}")
        if prompt:
            (self.env.home_dir / "run" / f"{name}.prompt.md").write_text(self.prompt(role))
        self.panes.append({"name": name, "pane_id": f"p-{name}", "agent_status": "working", "agent_session": {"value": f"s-{name}"}})
        self.env.set_herdr(agents=self.panes)

    def stale_events(self, name=None):
        rows = self.env.sql("SELECT entity_id, detail FROM events WHERE action='stale-agent' ORDER BY id")
        return [json.loads(r["detail"]) for r in rows if name in (None, r["entity_id"])]

    def fresh_events(self):
        return [json.loads(r["detail"]) for r in self.env.sql("SELECT detail FROM events WHERE action='fresh-agent' ORDER BY id")]

    def msgs(self):
        return [dict(r) for r in self.env.sql("SELECT recipient, subject, body FROM messages WHERE subject LIKE 'stale agent%' ORDER BY id")]

    def reasons(self, name):
        (e,) = self.stale_events(name)
        return e["reasons"]


class StaleDetectionTests(FreshBase):
    def test_stale_lead_prompt_flagged_in_one_tick_with_one_director_message(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        (self.roles / "lead.md").write_text((self.roles / "lead.md").read_text() + "\nNew rule.\n")  # the prompt file holds the old text
        self.ok("tick")
        self.assertEqual(self.stale_events(), [{"name": "zz-test-lead", "role": "lead", "reasons": ["prompt"]}])
        (m,) = self.msgs()
        self.assertEqual((m["recipient"], m["subject"]), ("zz-test-director", "stale agent: zz-test-lead"))
        self.assertIn("factory relaunch zz-test-lead --when-idle", m["body"])

    def test_current_agent_gets_no_event_and_no_message(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.agent("zz-test-coder", "coder", goal="G-001")
        self.ok("tick")
        self.assertEqual((self.stale_events(), self.fresh_events(), self.msgs()), ([], [], []))

    def test_settings_change_after_start_is_reason_settings(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.touch("settings/lead.json.in")
        self.ok("tick")
        self.assertEqual(self.reasons("zz-test-lead"), ["settings"])

    def test_rendered_settings_file_change_is_reason_settings(self):
        self.agent("zz-test-qa", "qa", goal="G-001")
        self.touch("settings/qa.json")
        self.ok("tick")
        self.assertEqual(self.reasons("zz-test-qa"), ["settings"])

    def test_role_plugin_change_is_reason_plugins_for_that_role_only(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.agent("zz-test-pm", "pm", goal="G-001")
        f = next(p for p in (self.roles / "plugins" / "lead").rglob("*") if p.is_file())
        os.utime(f, (NEW, NEW))
        self.ok("tick")
        self.assertEqual([e["name"] for e in self.stale_events()], ["zz-test-lead"])
        self.assertEqual(self.reasons("zz-test-lead"), ["plugins"])

    def test_goal_plugin_change_flags_only_coders_of_that_goal(self):
        self.agent("zz-test-c1", "coder", goal="G-001")
        self.agent("zz-test-c2", "coder", goal="G-002")
        gdir = self.roles / "plugins" / "goals" / "G-001"
        (gdir / "skill").mkdir(parents=True)
        (gdir / "skill" / "SKILL.md").write_text("x\n")
        os.utime(gdir / "skill" / "SKILL.md", (NEW, NEW))
        self.ok("tick")
        self.assertEqual([e["name"] for e in self.stale_events()], ["zz-test-c1"])
        self.assertEqual(self.reasons("zz-test-c1"), ["plugins"])

    def test_hook_change_is_not_a_reason(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        hook = next(p for p in (self.roles / "hooks").iterdir() if p.is_file())
        os.utime(hook, (NEW, NEW))
        self.ok("tick")
        self.assertEqual(self.stale_events(), [])

    def test_evaluator_and_non_live_agents_are_skipped(self):
        self.agent("zz-test-eval", "evaluator", goal="G-001", prompt=False)
        self.agent("zz-test-gone", "lead", goal="G-001", team="g-001-a")
        self.env.sql("UPDATE agents SET status='exited' WHERE name='zz-test-gone'")
        (self.roles / "lead.md").write_text("changed\n")
        self.touch("settings/evaluator.json.in")
        self.ok("tick")
        self.assertEqual(self.stale_events(), [])


class DeliveryTests(FreshBase):
    def setUp(self):
        super().setUp()
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")

    def test_same_reason_set_is_not_repeated_and_a_change_is(self):
        (self.roles / "lead.md").write_text("changed\n")
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.stale_events()), 1)
        self.assertEqual(len(self.msgs()), 1)
        self.touch("settings/lead.json.in")
        self.ok("tick")
        self.assertEqual([e["reasons"] for e in self.stale_events()], [["prompt"], ["prompt", "settings"]])
        self.assertEqual(len(self.msgs()), 2)

    def test_cleared_agent_gets_one_fresh_event(self):
        (self.roles / "lead.md").write_text("changed\n")
        self.ok("tick")
        (self.env.home_dir / "run" / "zz-test-lead.prompt.md").write_text(self.prompt("lead"))  # relaunched with the current text
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(self.fresh_events(), [{"name": "zz-test-lead"}])
        self.assertEqual(len(self.msgs()), 1)

    def test_persistent_agent_with_relaunch_requested_gets_the_event_but_no_message(self):
        self.env.sql("UPDATE agents SET relaunch_requested_at='2026-01-02T00:00:00+00:00' WHERE name='zz-test-lead'")
        (self.roles / "lead.md").write_text("changed\n")
        self.ok("tick")
        self.assertEqual(len(self.stale_events()), 1)
        self.assertEqual(self.msgs(), [])

    def test_worker_gets_the_event_but_no_message(self):
        self.agent("zz-test-qa", "qa", goal="G-001")
        self.touch("settings/qa.json.in")
        self.ok("tick")
        self.assertEqual([e["name"] for e in self.stale_events()], ["zz-test-qa"])
        self.assertEqual(self.msgs(), [])

    def test_stale_director_is_reported_to_the_boss(self):
        (self.roles / "director.md").write_text("changed\n")
        self.ok("tick")
        (m,) = self.msgs()
        self.assertEqual(m["recipient"], "boss")
        self.assertIn("factory relaunch zz-test-director --when-idle", m["body"])


class CheckoutTests(FreshBase):
    def setUp(self):
        super().setUp()
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.agent("zz-test-qa", "qa", goal="G-001")
        home = self.env.home_dir
        self.origin = self.env.home / "origin.git"
        git("init", "-q", "--bare", "-b", "main", str(self.origin), cwd=self.env.home)
        git("remote", "add", "origin", str(self.origin), cwd=home)
        git("commit", "-q", "--allow-empty", "-m", "base", cwd=home)
        git("push", "-q", "-u", "origin", "main", cwd=home)
        self.minimal_path = {"PATH": f"{self.env.stub_bin}{os.pathsep}/usr/bin{os.pathsep}/bin"}  # no ~/.local/bin, like the systemd unit

    def checkout_reasons(self):
        return {e["name"]: e["reasons"] for e in self.stale_events()}

    def test_checkout_and_launcher_matching_flags_nobody_even_with_a_minimal_path(self):
        self.ok("tick", extra_env=self.minimal_path)
        self.assertEqual(self.stale_events(), [])

    def test_head_behind_origin_main_marks_every_live_agent(self):
        git("commit", "-q", "--allow-empty", "-m", "local only", cwd=self.env.home_dir)  # HEAD != origin/main
        self.ok("tick", extra_env=self.minimal_path)
        self.assertEqual(self.checkout_reasons(), {n: ["checkout"] for n in ("zz-test-director", "zz-test-lead", "zz-test-qa")})

    def test_launcher_not_resolving_to_the_home_checkout_marks_every_live_agent(self):
        launcher = self.env.user_home / ".local" / "bin" / "factory"
        launcher.unlink()
        launcher.symlink_to(self.env.home / "elsewhere" / "factory")
        self.ok("tick", extra_env=self.minimal_path)
        self.assertEqual(self.checkout_reasons(), {n: ["checkout"] for n in ("zz-test-director", "zz-test-lead", "zz-test-qa")})

    def test_path_lookup_is_not_used(self):
        # a factory on PATH that resolves elsewhere changes nothing: only ~/.local/bin/factory counts
        other = self.env.home / "otherbin"
        other.mkdir()
        (other / "factory").symlink_to(self.env.home / "elsewhere")
        self.ok("tick", extra_env={"PATH": f"{other}{os.pathsep}{self.env.env()['PATH']}"})
        self.assertEqual(self.stale_events(), [])

    def test_a_synced_export_does_not_touch_unchanged_role_files(self):
        before = {str(p): p.stat().st_mtime_ns for p in self.roles.rglob("*") if p.is_file()}
        self.ok("tick")
        self.ok("tick")
        after = {str(p): p.stat().st_mtime_ns for p in self.roles.rglob("*") if p.is_file()}
        self.assertEqual(before, after)


class RobustnessAndDigestTests(FreshBase):
    def test_unreadable_prompt_file_is_reason_unreadable_and_the_others_are_still_checked(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a", prompt=False)
        (self.env.home_dir / "run" / "zz-test-lead.prompt.md").mkdir()  # reading it raises IsADirectoryError
        self.agent("zz-test-qa", "qa", goal="G-001")
        self.touch("settings/qa.json.in")
        exported = self.env.home_dir / "exports" / "INDEX.md"
        exported.unlink()  # only this tick's export can bring it back
        p = self.ok("tick")
        self.assertEqual({e["name"]: e["reasons"] for e in self.stale_events()},
                         {"zz-test-lead": ["unreadable"], "zz-test-qa": ["settings"]}, p.stderr)
        self.assertEqual(self.env.sql("SELECT 1 FROM events WHERE action='freshness-failed'"), [])
        self.assertTrue(exported.is_file())

    def failing_check(self, text):
        """Make every dedupe-state write of the check fail with `text`: a failure that is not about one agent's files, repeating each tick."""
        self.env.sql("DROP TRIGGER IF EXISTS fresh_boom")
        self.env.sql(f"CREATE TRIGGER fresh_boom BEFORE INSERT ON throttle WHEN NEW.key LIKE 'fresh:%' "
                     f"BEGIN SELECT RAISE(ABORT, '{text}'); END")

    def failure_events(self, action):
        return [r["detail"] for r in self.env.sql("SELECT detail FROM events WHERE action=? ORDER BY id", (action,))]

    def test_same_failure_on_three_ticks_is_one_event_and_clearing_is_one_event(self):
        self.agent("zz-test-qa", "qa", goal="G-001")
        self.touch("settings/qa.json.in")
        self.failing_check("boom one")
        for _ in range(3):
            self.ok("tick")
        self.assertIn("boom one", self.failure_events("freshness-failed")[0])
        self.assertEqual(len(self.failure_events("freshness-failed")), 1)
        self.failing_check("boom two")  # a new error text is a new event
        self.ok("tick")
        self.assertEqual(len(self.failure_events("freshness-failed")), 2)
        self.env.sql("DROP TRIGGER fresh_boom")
        self.ok("tick")
        self.ok("tick")
        self.assertEqual(len(self.failure_events("freshness-recovered")), 1)
        self.assertEqual(len(self.failure_events("freshness-failed")), 2)

    def test_digest_without_a_goal_block_still_has_the_freshness_line(self):
        self.env.sql("DELETE FROM teams")
        self.env.sql("DELETE FROM goals")
        self.ok("tick")
        out = self.ok("digest").stdout
        self.assertIn("_no active goals_", out)
        self.assertIn("\nFreshness: all current\n", out)

    def test_digest_line_all_current_then_names_the_stale_agent(self):
        self.agent("zz-test-lead", "lead", goal="G-001", team="g-001-a")
        self.ok("tick")
        out = self.ok("digest").stdout
        self.assertIn("\nFreshness: all current\n", out)
        self.assertLess(out.index("Drift: "), out.index("Freshness: "))
        (self.roles / "lead.md").write_text("changed\n")
        self.touch("settings/lead.json.in")
        self.ok("tick")
        self.assertIn("\nFreshness: zz-test-lead (prompt, settings)\n", self.ok("digest").stdout)


if __name__ == "__main__":
    unittest.main()
