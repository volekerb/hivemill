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


class PromptV2Tests(unittest.TestCase):
    """T-1623: the Cursor prompt chain v2 (docs/cursor-prompt.md)."""

    def setUp(self):
        self.h = load_harness()

    def test_check_prompt_version(self):
        self.assertEqual(self.h.check_prompt_version("v2"), "v2")
        self.assertEqual(self.h.check_prompt_version(" v1 "), "v1")
        self.assertEqual(self.h.check_prompt_version(""), "v1")
        with self.assertRaises(ValueError):
            self.h.check_prompt_version("on")

    def test_role_prompt_v2_reads_only_the_cursor_file(self):
        with tempfile.TemporaryDirectory(prefix="zz-cursor-roles-") as tmp:
            roles = Path(tmp)
            (roles / "cursor").mkdir()
            (roles / "cursor" / "coder.md").write_text("# short coder\n")
            (roles / "cursor" / "evaluator.md").write_text("# not a cursor role\n")
            self.assertEqual(self.h.role_prompt_v2(roles, "coder"), "# short coder\n")
            self.assertIsNone(self.h.role_prompt_v2(roles, "qa"))
            self.assertIsNone(self.h.role_prompt_v2(roles, "evaluator"))
            self.assertIsNone(self.h.role_prompt_v2(roles, "../coder"))

    def test_kickoff_v2_coder_names_every_command_with_its_ids(self):
        k = self.h.kickoff_v2("coder", "cursor-coder-t-0007", "G-001", "g-001-a", "T-0007", "extra words")
        for part in (
            "You are `cursor-coder-t-0007`, the factory coder for T-0007.",
            "Do not end your turn until `factory done` has succeeded",
            "no human reads this pane and there is no user",
            '"Briefly inform the user" and "Factory watchdog: your turn ended" prompts are orders to act',
            "`env -u CURSOR_AGENT git commit`",
            "refused for a Co-authored-by line",
            "`factory task show T-0007`",
            "`git push origin HEAD:factory/t-0007` alone in its own shell call",
            "`factory pr create --title 'T-0007: <what the change does>' --body -`",
            "`factory prcheck`",
            "`factory task update T-0007 --status in_review --pr <PR url>`",
            '`factory done "<PR url; what changed; test evidence>"`',
            'Stuck: `factory done "BLOCKED:',
            "Goal context: `factory goal show G-001`.",
            "Your team is g-001-a.",
        ):
            self.assertIn(part, k)
        self.assertTrue(k.endswith(" extra words"))
        self.assertNotIn("\n", k)
        self.assertNotIn("$(", k)
        order = [k.index(x) for x in ("git push", "factory pr create", "factory prcheck", "--status in_review", "(7) `factory done")]
        self.assertEqual(order, sorted(order))

    def test_kickoff_v2_other_worker_and_hq(self):
        k = self.h.kickoff_v2("qa", "cursor-qa-t-0007", None, None, "T-0007", None)
        self.assertIn("the factory qa.", k)
        self.assertIn("Do not end your turn until `factory done` has succeeded", k)
        self.assertNotIn("factory pr create", k)
        self.assertNotIn("Goal context", k)
        for role in ("boss", "director", "lead", "evaluator"):
            self.assertIsNone(self.h.kickoff_v2(role, "x", "G-001", None, None, None))

    def test_cursor_coder_prompt_file_rules(self):
        text = (REPO / "roles" / "cursor" / "coder.md").read_text()
        old = (REPO / "roles" / "_common.md").read_text() + (REPO / "roles" / "coder.md").read_text()
        self.assertLess(len(text), len(old) // 2)
        for must in (
            "## Loop contract",
            "No human reads this pane. There is no user.",
            '"Briefly inform the user about the task result"',
            'begins "Factory watchdog: your turn ended"',
            "an offer",
            "`env -u CURSOR_AGENT git commit -m",
            "`env -u CURSOR_AGENT git merge origin/main`",
            "A CI check that is red on your head is yours",
            "`Co-authored-by: Cursor`",
            "git commit-tree HEAD^{tree} -p origin/main",
            "`git reset --soft <the printed sha>`",
            "the only allowed end of your turn",
            "`git push origin HEAD:factory/<task-id>`",
            "factory pr create --title 'T-xxxx: <what the change does>' --body - <<'EOF'",
            "Never only the id.",
            "## Acceptance walk",
            "`factory prcheck`",
            "`factory task update <T> --status in_review --pr <PR url>`",
            '`factory done "BLOCKED:',
            "Merge, review your own PR, or push to main",
            "dollar-paren sequence",
            "Machine-level changes",
            "any GitHub repo other than this project's own",
        ):
            self.assertIn(must, text)
        for claude_only in ("skill `", "subagent", "Sonnet", "Bash call", "ci-wait", "--title T "):
            self.assertNotIn(claude_only, text)
        steps = [text.index(x) for x in ("git push origin HEAD", "factory pr create", "`factory prcheck`",
                                         "--status in_review", '10. `factory done')]
        self.assertEqual(steps, sorted(steps))


if __name__ == "__main__":
    unittest.main()
