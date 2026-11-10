"""T-1207: retro packet, file validation, and digest section."""
import json
import sqlite3
import unittest

from harness import FactoryTestCase, PR_URL

D = "2026-11-09"
NOW = f"{D}T10:00:00+01:00"
WIN_START = f"{D}T09:00:00+01:00"


class RetroReportTests(FactoryTestCase):
    INPROC = True

    def setUp(self):
        super().setUp()
        self.env.seed_project()
        self.env.seed_goal()
        self.env.seed_task()
        self.env.seed_pr()
        self.env.seed_agent("zz-test-retro", "retro", goal=None, team=None, task=None)

    def _incident(self, cause, kind, task_id="T-0001", created=NOW, verdict_id=None):
        with self.env.load_factory() as mod:
            con = sqlite3.connect(self.env.db_path)
            con.row_factory = sqlite3.Row
            self.addCleanup(con.close)
            mod.record_rejection_incident(con, cause, kind, task_id, verdict_id=verdict_id, ts=created)
            con.commit()

    def test_file_and_digest(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "check", "cause": "bug", "text": "run prcheck before done"}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body),
                extra_env={"FACTORY_NOW": NOW})
        dig = self.ok("digest", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertIn("## Retro", dig)
        self.assertIn("T-0001", dig)
        self.assertIn(PR_URL, dig)
        self.assertIn("check —", dig)
        goal_dig = self.ok("digest", "--goal", "G-001", extra_env={"FACTORY_NOW": NOW}).stdout
        self.assertNotIn("## Retro", goal_dig)

    def test_digest_send_includes_heading(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "template", "cause": "bug", "text": "add acceptance item"}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body),
                extra_env={"FACTORY_NOW": NOW})
        self.env.seed_agent("zz-test-director", "director", goal=None, team=None)
        self.ok("digest", "--send", agent="zz-test-director", extra_env={"FACTORY_NOW": NOW})
        row = self.env.sql("SELECT body FROM messages ORDER BY id DESC LIMIT 1")[0]
        self.assertIn("## Retro", row["body"])

    def test_wrong_cause_count_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 9}],
            "evidence": [],
            "fixes": [{"type": "check", "cause": "bug", "text": "x"}],
        }
        before = self.env.sql("SELECT count(*) c FROM retro_reports")[0]["c"]
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))
        self.assertEqual(self.env.sql("SELECT count(*) c FROM retro_reports")[0]["c"], before)

    def test_third_fix_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [
                {"type": "check", "cause": "bug", "text": "a"},
                {"type": "template", "cause": "bug", "text": "b"},
                {"type": "prompt_rule", "cause": "bug", "text": "c"},
            ],
        }
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))

    def test_bad_fix_type_refused(self):
        self._incident("bug", "qa")
        body = {
            "window_end": NOW,
            "causes": [{"cause": "bug", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "bug"}],
            "fixes": [{"type": "shell", "cause": "bug", "text": "x"}],
        }
        self.refused(2, "retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))

    def test_shell_looking_fix_stored_verbatim(self):
        cmd = "$(factory judge T-1 --merge)"
        body = {
            "window_end": WIN_START,
            "causes": [],
            "evidence": [],
            "fixes": [],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body), extra_env={"FACTORY_NOW": WIN_START})
        # empty window — need separate test with nonempty using stored text
        self._incident("other", "gate", created=WIN_START)
        body2 = {
            "window_end": NOW,
            "causes": [{"cause": "other", "count": 1}],
            "evidence": [{"task_id": "T-0001", "pr_url": PR_URL, "cause": "other"}],
            "fixes": [{"type": "check", "cause": "other", "text": cmd}],
        }
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body2))
        fixes = json.loads(self.env.sql("SELECT fixes FROM retro_reports ORDER BY id DESC LIMIT 1")[0]["fixes"])
        self.assertEqual(fixes[0]["text"], cmd)

    def test_empty_window_zero_fixes(self):
        body = {"window_end": NOW, "causes": [], "evidence": [], "fixes": []}
        self.ok("retro", "file", "-", agent="zz-test-retro", stdin=json.dumps(body))


if __name__ == "__main__":
    unittest.main()
