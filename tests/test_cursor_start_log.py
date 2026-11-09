"""T-1206: refuse cursor researcher; log cursor herdr-kind start-uncertain; lost alert cites the log."""
import datetime as dt
import json
import os
import stat
import unittest
from pathlib import Path

from harness import FactoryTestCase
from test_sandbox_spawn import prep

PLANT_ENV = "zz-plant-env-secret-5c1e9d"
PLANT_TOKEN = "sk-ant-" + "PLANT1206FAKE-TOKEN-SHAPE"


def ago(seconds):
    return (dt.datetime.now().astimezone() - dt.timedelta(seconds=seconds)).isoformat(timespec="seconds")


class CursorResearcherRefuseTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        prep(self.env)
        self.state = Path(self.env.user_home) / "xdg-state"
        self.xdg = {"XDG_STATE_HOME": str(self.state)}
        self.env.seed_task("T-0001", role="researcher", status="todo", team="g-001-a")
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")

    def test_cursor_runtime_researcher_refused(self):
        before = len(self.env.calls("herdr"))
        p = self.env.run(
            "spawn", "researcher", "--name", "cursor-researcher-t-0001", "--goal", "G-001",
            "--task", "T-0001", extra_env=self.xdg,
        )
        self.assertNotEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("docs/research-team.md", p.stderr)
        self.assertEqual(
            [c for c in self.env.calls("herdr")[before:] if c["argv"][:2] == ["agent", "start"]],
            [],
        )
        self.assertEqual(self.env.sql("SELECT count(*) AS n FROM agents WHERE role='researcher'")[0]["n"], 0)

    def test_claude_researcher_unchanged(self):
        self.env.sql("UPDATE goals SET org='claude' WHERE id='G-001'")
        p = self.env.run(
            "spawn", "researcher", "--name", "zz-test-researcher", "--goal", "G-001", "--task", "T-0001",
            extra_env=self.xdg,
        )
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        want = self.state / "factory" / "research" / "t-0001"
        self.assertEqual(self.env.sql("SELECT cwd FROM agents WHERE name LIKE '%zz-test-researcher'")[0]["cwd"], str(want))


class CursorStartLogTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.seed_team("g-001-a")
        self.ok("config", "agent_runtimes", "pm=cursor")
        fake = self.env.stub_bin / "agent"
        fake.write_text("#!/bin/sh\necho ok\n")
        fake.chmod(0o755)
        self.name = "zz-cursor-pm-startlog"
        self.log = self.env.home_dir / "logs" / f"{self.name}-cursor-start.log"

    def spawn_pm(self, extra_env=None):
        return self.env.run(
            "spawn", "pm", "--name", self.name, "--goal", "G-001", "--team", "g-001-a",
            extra_env=extra_env or {},
        )

    def test_start_uncertain_writes_log_without_secrets(self):
        self.env.set_herdr(start_fail=f"agent_not_ready {PLANT_TOKEN} GH_TOKEN={PLANT_ENV}")
        env = {**self.env.env(), "GH_TOKEN": PLANT_ENV}
        p = self.spawn_pm(extra_env=env)
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertTrue(self.log.is_file())
        self.assertEqual(stat.S_IMODE(self.log.stat().st_mode), 0o600)
        text = self.log.read_text()
        self.assertIn("herdr agent start", text)
        self.assertIn("herdr pane process-info", text)
        self.assertNotIn(PLANT_TOKEN, text)
        self.assertNotIn(PLANT_ENV, text)
        self.assertIn("[redacted-token]", text)
        self.assertIn("[redacted-env]", text)

    def test_successful_start_writes_no_log(self):
        self.env.set_herdr(start_fail=False)
        p = self.spawn_pm()
        self.assertEqual(p.returncode, 0, p.stderr)
        self.assertFalse(self.log.exists())


class CursorLostLogAlertTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        self.env.seed_goal()
        self.env.sql("UPDATE goals SET org='cursor' WHERE id='G-001'")
        self.env.seed_team("g-001-a")
        self.env.seed_agent("cursor-director", "director", status="live")
        self.env.sql("UPDATE agents SET org='cursor' WHERE name='cursor-director'")
        self.env.seed_agent("cursor-pm-tick", "pm", goal="G-001", team="g-001-a", status="live", pane_id="p-cur")
        sa = json.dumps({"runtime": "cursor", "launch": "herdr-kind", "herdr_kind": "cursor"})
        self.env.sql(
            "UPDATE agents SET spawn_args=?, started=?, org='cursor' WHERE name='cursor-pm-tick'",
            (sa, ago(200)),
        )
        self.env.set_herdr(agents=[])
        self.log = self.env.home_dir / "logs" / "cursor-pm-tick-cursor-start.log"

    def vanished_msg(self):
        rows = self.env.sql("SELECT body FROM messages WHERE subject LIKE '%vanished%' ORDER BY id")
        return rows[0]["body"] if rows else ""

    def test_lost_alert_names_log_when_present(self):
        self.log.parent.mkdir(parents=True, exist_ok=True)
        self.log.write_text("diag\n")
        self.log.chmod(0o600)
        self.ok("tick")
        self.assertEqual(self.env.sql("SELECT status FROM agents WHERE name='cursor-pm-tick'")[0]["status"], "lost")
        body = self.vanished_msg()
        self.assertIn(str(self.log), body)

    def test_lost_alert_unchanged_without_log(self):
        self.ok("tick")
        body = self.vanished_msg()
        self.assertIn("Decide: respawn or reassign.", body)
        self.assertNotIn("Start diagnostics:", body)
        self.assertNotIn("cursor-start.log", body)


if __name__ == "__main__":
    unittest.main()
