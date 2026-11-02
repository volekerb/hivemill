"""T-0767: the tick/export writes run/guard-export.json for the Go worker-guard."""
import json
import os
import stat
import unittest
from datetime import datetime, timedelta, timezone

from harness import FactoryTestCase


class GuardExportTests(FactoryTestCase):
    def setUp(self):
        super().setUp()
        e = self.env
        e.seed_project()
        e.seed_goal(project="zz-test-proj", title="zz-test guard export goal")
        e.seed_task("T-0001", status="todo", pr_url=None)
        e.sql("UPDATE tasks SET worktree='/zz-test/wt/T-0001' WHERE id='T-0001'")
        e.seed_agent("zz-test-coder-t-0001", "coder", goal="G-001")
        e.seed_agent("zz-test-boss", "boss")  # no goal: not in the map
        self.ok("config", "public_text_repos", "zz-test/pub")
        self.ok("config", "scrub_terms_file", "/zz-test/terms.txt")
        self.ok("config", "test_sandbox", "1")
        self.path = e.home_dir / "run" / "guard-export.json"

    def load(self):
        return json.loads(self.path.read_text())

    def test_export_writes_config_agent_goals_and_worktrees(self):
        self.ok("export", "--no-push")
        doc = self.load()
        self.assertEqual(doc["version"], 1)
        self.assertEqual(doc["config"]["public_text_repos"], "zz-test/pub")
        self.assertEqual(doc["config"]["scrub_terms_file"], "/zz-test/terms.txt")
        self.assertEqual(doc["config"]["test_sandbox"], "1")
        self.assertEqual(doc["config"]["frozen_repos"], "")
        self.assertEqual(doc["agent_goals"], {"zz-test-coder-t-0001": "G-001"})
        self.assertEqual(doc["task_worktrees"], {"T-0001": "/zz-test/wt/T-0001"})

    def test_mode_0600_and_generated_at_is_recent_rfc3339(self):
        self.ok("export", "--no-push")
        self.assertEqual(stat.S_IMODE(os.stat(self.path).st_mode), 0o600)
        at = datetime.fromisoformat(self.load()["generated_at"])
        self.assertIsNotNone(at.tzinfo)
        self.assertLess(abs(datetime.now(timezone.utc) - at), timedelta(minutes=2))

    def test_rewrite_reflects_a_changed_row_and_leaves_no_temp_file(self):
        self.ok("export", "--no-push")
        self.ok("config", "test_sandbox", "0")
        self.ok("export", "--no-push")
        self.assertEqual(self.load()["config"]["test_sandbox"], "0")
        self.assertEqual([p.name for p in self.path.parent.glob("guard-export.json*")], ["guard-export.json"])

    def test_not_in_the_committed_state_dir(self):
        self.ok("export", "--no-push")
        self.assertFalse((self.env.home_dir / "state" / "guard-export.json").exists())


if __name__ == "__main__":
    unittest.main()
