"""T-0834: Cursor spawn disables agent commit/PR attribution in materialized cli.json."""
import importlib
import json
import os
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, str(Path(__file__).resolve().parent.parent))

HOOKS = Path(__file__).resolve().parent.parent / "roles" / "hooks"


def load_harness():
    return importlib.import_module("scout.cursor_harness")


class CursorAttributionPolicyTests(unittest.TestCase):
    def test_materialized_cli_json_disables_agent_attribution(self):
        h = load_harness()
        settings = {"permissions": {"deny": ["Bash(sudo:*)"], "allow": []}}
        with tempfile.TemporaryDirectory(prefix="zz-cursor-attr-") as tmp:
            info = h.materialize_cursor_policy(
                tmp,
                "coder",
                hooks_dir=HOOKS,
                claude_settings=settings,
                system_prompt="# coder\n",
            )
            cli = json.loads(Path(info["cli"]).read_text())
            attr = cli["attribution"]
            self.assertIs(attr["attributeCommitsToAgent"], False)
            self.assertIs(attr["attributePRsToAgent"], False)


if __name__ == "__main__":
    unittest.main()
