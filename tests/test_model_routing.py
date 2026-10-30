"""G-011 T-0246: Haiku for the cleaner only, `factory config model_cleaner` rollback, one resolver for spawn and relaunch."""
import json
import tempfile
import unittest
from pathlib import Path

from harness import REPO, FactoryTestCase
from web.prices import cost
from web.tokens import TokenIndex, rollup

OPUS, SONNET, HAIKU = "claude-opus-5-5", "claude-sonnet-5-5", "claude-haiku-5-5"
EXPECTED = {"coder": SONNET, "qa": SONNET, "scout": SONNET, "cleaner": HAIKU, "reviewer": OPUS, "judge": OPUS}


def spawn_args(role):
    base = ["spawn", role, "--name", f"zz-test-{role}"]
    if role in ("coder", "qa", "reviewer"):
        base += ["--goal", "G-001", "--task", "T-0001"]
    return base


class ModelRoutingTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team(lead="zz-test-lead")
        self.env.seed_task("T-0001", role="coder", status="todo", pr_url=None, team="g-001-a")
        for k in ("max_live_agents", "max_total_claude"):
            self.env.run("config", k, "99")
        self.env.seed_agent("zz-test-director", "director")

    def starts(self):
        return {c["argv"][2]: c["argv"] for c in self.env.calls("herdr") if c["argv"][:2] == ["agent", "start"]}

    def model_of(self, role):
        argv = self.starts()[f"zz-test-{role}"]
        return argv[argv.index("--model") + 1]

    def director(self, *args):
        return self.env.run(*args, agent="zz-test-director")

    def stored(self):
        return self.env.sql("SELECT value FROM config WHERE key='model_cleaner'")[0][0]

    def test_cleaner_on_haiku_others_unchanged(self):
        for role in EXPECTED:
            with self.subTest(role=role):
                self.ok(*spawn_args(role))
        for role, model in EXPECTED.items():
            self.assertEqual(self.model_of(role), model, role)

    def test_override_allowed_then_cleared(self):
        self.ok("config", "model_cleaner", SONNET, agent="zz-test-director")
        self.ok(*spawn_args("cleaner"))
        self.assertEqual(self.model_of("cleaner"), SONNET)
        self.assertEqual(self.env.sql("SELECT model FROM agents WHERE name='zz-test-cleaner'")[0][0], SONNET)
        self.ok("config", "model_cleaner", "", agent="zz-test-director")
        self.assertEqual(self.stored(), "")

    def test_override_refused_value_unchanged(self):
        for bad in ("claude-fable-5-1", "foo", "claude-opus-4"):
            with self.subTest(bad=bad):
                p = self.refused(2, "config", "model_cleaner", bad, agent="zz-test-director")
                self.assertIn("model_cleaner", p.stderr)
                self.assertEqual(self.stored(), "")

    def test_override_refused_keeps_previous_value(self):
        self.ok("config", "model_cleaner", OPUS, agent="zz-test-director")
        self.refused(2, "config", "model_cleaner", "claude-fable-5-1", agent="zz-test-director")
        self.assertEqual(self.stored(), OPUS)

    def test_override_director_only_and_listed(self):
        self.env.seed_agent("zz-test-coder", "coder", goal="G-001", team="g-001-a", task="T-0001")
        p = self.env.run("config", "model_cleaner", SONNET, agent="zz-test-coder")
        self.assertNotEqual(p.returncode, 0)
        self.assertEqual(self.stored(), "")
        p = self.ok("config")
        self.assertIn("model_cleaner = ", p.stdout)

    def test_no_effort_for_haiku_but_opus_keeps_it(self):
        self.ok("config", "opus_effort", "high", agent="zz-test-director")
        self.ok(*spawn_args("cleaner"))
        self.ok(*spawn_args("reviewer"))
        self.assertNotIn("--effort", self.starts()["zz-test-cleaner"])
        self.assertIn("--effort", self.starts()["zz-test-reviewer"])

    def relaunch_plan(self):
        self.env.sql("UPDATE agents SET model=NULL, status='live', pane_id='px2', session_id='sess-c' WHERE name='zz-test-cleaner'")
        self.env.set_herdr(
            agents=[{"name": "zz-test-cleaner", "agent": "claude", "pane_id": "px2", "agent_status": "idle",
                     "agent_session": {"value": "sess-c"}}],
            workspaces=[{"workspace_id": "w1", "label": "x"}],
            tabs=[{"tab_id": "t1", "workspace_id": "w1", "label": "hq", "pane_count": 1}],
            panes=[{"pane_id": "px2", "tab_id": "t1", "workspace_id": "w1"}])
        p = self.ok("relaunch", "zz-test-cleaner", "--dry-run", agent="zz-test-director")
        return p.stdout

    def test_relaunch_plan_matches_spawn_with_and_without_override(self):
        for override, want in (("", HAIKU), (SONNET, SONNET)):
            with self.subTest(override=override):
                self.ok("config", "model_cleaner", override, agent="zz-test-director")
                self.env.sql("DELETE FROM agents WHERE name='zz-test-cleaner'")
                self.ok(*spawn_args("cleaner"))
                spawned = self.model_of("cleaner")
                self.assertEqual(spawned, want)
                self.assertIn(f"--model {spawned} ", self.relaunch_plan())


class WebCostTests(unittest.TestCase):
    def test_haiku_transcript_priced_with_haiku_rates(self):
        row = {"type": "assistant", "uuid": "u-1", "requestId": "r1", "sessionId": "s1", "timestamp": "2026-10-20T10:00:00Z",
               "cwd": "/tmp/x", "message": {"id": "m1", "model": HAIKU, "usage": {
                   "input_tokens": 1000, "output_tokens": 200, "cache_read_input_tokens": 5000,
                   "cache_creation_input_tokens": 0, "cache_creation": {}}}}
        with tempfile.TemporaryDirectory() as d:
            f = Path(d) / "p" / "s1.jsonl"
            f.parent.mkdir()
            f.write_text(json.dumps(row) + "\n")
            idx = TokenIndex(Path(d))
            idx.refresh()
            roll = rollup(idx.snapshot(), {})
        self.assertEqual(set(roll["by_model"]), {HAIKU})
        want = (1000 * 0.10 + 200 * 0.50 + 5000 * 0.01) / 1e6
        self.assertAlmostEqual(roll["by_model"][HAIKU]["cost"], want, places=9)
        self.assertFalse(roll["by_model"][HAIKU]["cost_unknown"])


if __name__ == "__main__":
    unittest.main()
