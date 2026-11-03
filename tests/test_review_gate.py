"""T-0857: the multi-angle-review gate (one helper) and the reviewer kickoff line it drives. gh is mocked at the
process boundary (subprocess.run); the spawn-never-fails case also runs the real CLI with the gh test stub."""
import json
import sqlite3
import subprocess
import sys
import unittest
from pathlib import Path
from unittest import mock

from harness import FactoryTestCase

sys.path.insert(0, str(Path(__file__).resolve().parent.parent / "bin"))
import factory_cli  # noqa: E402

PR = "https://github.com/zz-test/repo/pull/7"
LINE = "Gated PR: run the multi-angle-review workflow (reviewer skill multi-angle-review) before your verdict."


def fake_gh(files, additions, deletions, fail=None):
    def run(argv, **kw):
        if fail:
            return subprocess.CompletedProcess(argv, 1, "", fail)
        if argv[:3] == ["gh", "pr", "diff"]:
            return subprocess.CompletedProcess(argv, 0, "\n".join(files) + "\n", "")
        if argv[:3] == ["gh", "pr", "view"]:
            return subprocess.CompletedProcess(argv, 0, json.dumps({"additions": additions, "deletions": deletions}), "")
        raise AssertionError(argv)
    return run


class GateFunctionTests(unittest.TestCase):
    def test_gated_paths(self):
        for f in ("roles/hooks/worker-guard.py", "roles/settings/coder.json", "ci/affected.py", ".github/workflows/ci.yml"):
            with self.subTest(f=f):
                self.assertTrue(factory_cli.review_gate(["README.md", f], 3))

    def test_ungated_paths_and_lookalikes(self):
        for f in ("bin/factory_cli.py", "roles/reviewer.md", "docs/ci/notes.md", "roles/hooks-doc.md", "tests/test_x.py"):
            with self.subTest(f=f):
                self.assertFalse(factory_cli.review_gate([f], 10))

    def test_size_boundary(self):
        self.assertFalse(factory_cli.review_gate(["bin/factory_cli.py"], 500))
        self.assertTrue(factory_cli.review_gate(["bin/factory_cli.py"], 501))

    def test_no_files_small(self):
        self.assertFalse(factory_cli.review_gate([], 0))


class GatedPromptTests(unittest.TestCase):
    def setUp(self):
        self.con = sqlite3.connect(":memory:")
        self.con.row_factory = sqlite3.Row
        self.addCleanup(self.con.close)
        self.con.execute("CREATE TABLE tasks(id TEXT, pr_url TEXT)")
        self.con.execute("INSERT INTO tasks VALUES('T-1', ?)", (PR,))
        self.con.execute("INSERT INTO tasks VALUES('T-2', NULL)")

    def prompt(self, gh, role="reviewer", task="T-1", extra=None):
        with mock.patch.object(factory_cli.subprocess, "run", gh), mock.patch.object(factory_cli, "event") as ev:
            out = factory_cli.gated_reviewer_prompt(self.con, role, task, extra)
        return out, ev

    def kick(self, extra):
        return factory_cli.kickoff_text("reviewer", "zz-r", "G-1", "g-1-a", "T-1", extra)

    def test_gated_by_path_adds_exact_line(self):
        out, _ = self.prompt(fake_gh(["roles/hooks/x.py"], 5, 1))
        self.assertEqual(out, LINE)
        self.assertTrue(self.kick(out).endswith(" " + LINE))

    def test_gated_by_size_appends_to_existing_extra(self):
        out, _ = self.prompt(fake_gh(["a.py"], 400, 101), extra="look at X")
        self.assertEqual(out, "look at X " + LINE)

    def test_ungated_kickoff_is_byte_identical(self):
        out, _ = self.prompt(fake_gh(["a.py"], 250, 250), extra=None)
        self.assertIsNone(out)
        self.assertEqual(self.kick(out), factory_cli.kickoff_text("reviewer", "zz-r", "G-1", "g-1-a", "T-1", None))
        self.assertNotIn("Gated PR", self.kick(out))

    def test_other_roles_and_no_pr_never_call_gh(self):
        boom = mock.Mock(side_effect=AssertionError("gh called"))
        self.assertEqual(self.prompt(boom, role="coder", extra="e")[0], "e")
        self.assertEqual(self.prompt(boom, task="T-2", extra="e")[0], "e")
        self.assertEqual(self.prompt(boom, task=None, extra="e")[0], "e")

    def test_gh_failure_adds_no_line_logs_reason(self):
        out, ev = self.prompt(fake_gh([], 0, 0, fail="gh down"), extra="e")
        self.assertEqual(out, "e")
        ev.assert_called_once()
        self.assertIn("gh down", ev.call_args[0][4])
        self.assertEqual(ev.call_args[0][3], "gate-skipped")

    def test_gh_missing_adds_no_line(self):
        out, ev = self.prompt(mock.Mock(side_effect=FileNotFoundError("gh")), extra="e")
        self.assertEqual(out, "e")
        ev.assert_called_once()

    def test_garbled_stats_adds_no_line(self):
        def run(argv, **kw):
            return subprocess.CompletedProcess(argv, 0, "x\n" if argv[2] == "diff" else "not json", "")
        out, ev = self.prompt(run, extra="e")
        self.assertEqual(out, "e")
        ev.assert_called_once()


class SpawnStillSucceedsTests(FactoryTestCase):
    def test_reviewer_spawn_with_failing_gh(self):
        e = self.env
        e.seed_goal()
        e.seed_task()
        p = e.run("spawn", "reviewer", "--goal", "G-001", "--task", "T-0001", "--name", "zz-test-rv")
        self.assertEqual(p.returncode, 0, p.stdout + p.stderr)
        self.assertIn("multi-angle-review gate skipped", p.stderr)
        kick = e.sql("SELECT body FROM messages WHERE subject LIKE 'kickoff: reviewer%'")
        self.assertTrue(kick)
        self.assertNotIn("Gated PR", kick[0]["body"])
        self.assertTrue(e.sql("SELECT 1 FROM events WHERE action='gate-skipped' AND entity_id='T-0001'"))


if __name__ == "__main__":
    unittest.main()
