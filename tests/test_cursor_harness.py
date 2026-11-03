"""Tests for scout/cursor_harness.py policy generation and parse_pilot_roles (not in test_cursor_pilot.py)."""
import importlib
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

from harness import REPO

HOOKS = REPO / "roles" / "hooks"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


class ParsePilotRolesTests(unittest.TestCase):
    def test_coder_and_qa_allowed(self):
        h = load_harness()
        self.assertEqual(h.parse_pilot_roles("coder"), ["coder"])
        self.assertEqual(h.parse_pilot_roles("qa"), ["qa"])

    def test_evaluator_rejected(self):
        h = load_harness()
        with self.assertRaises(ValueError):
            h.parse_pilot_roles("evaluator")

    def test_unknown_role_rejected(self):
        h = load_harness()
        with self.assertRaises(ValueError):
            h.parse_pilot_roles("not-a-role")

    def test_pilot_roles_equals_cursor_roles_without_evaluator(self):
        h = load_harness()
        self.assertEqual(h.PILOT_ROLES, h.CURSOR_ROLES)
        self.assertNotIn("evaluator", h.PILOT_ROLES)


class HooksJsonShapeTests(unittest.TestCase):
    def test_materialized_hooks_fail_closed_and_pretooluse_matcher(self):
        h = load_harness()
        settings = {"permissions": {"deny": ["Bash(sudo:*)"], "allow": []}}
        with tempfile.TemporaryDirectory(prefix="zz-cursor-hooks-") as tmp:
            h.materialize_cursor_policy(
                tmp,
                "coder",
                hooks_dir=HOOKS,
                claude_settings=settings,
                system_prompt="# coder\n",
            )
            hooks = json.loads((Path(tmp) / ".cursor" / "hooks.json").read_text())
            for event, entries in hooks["hooks"].items():
                for entry in entries:
                    self.assertTrue(
                        entry.get("failClosed"),
                        f"{event} hook entry must set failClosed true: {entry!r}",
                    )
            matcher = hooks["hooks"]["preToolUse"][0].get("matcher")
            self.assertEqual(matcher, "Shell|Write|Delete|Read|Grep|StrReplace|EditNotebook")


if __name__ == "__main__":
    unittest.main()
