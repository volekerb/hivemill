"""Runtime display helpers: Claude vs Cursor chips (docs/runtimes.md)."""
import unittest

from web.components import runtime_badge
from web.queries import agent_runtime_of


class AgentRuntimeOfTests(unittest.TestCase):
    def test_default_claude(self):
        self.assertEqual(agent_runtime_of(None), "claude")
        self.assertEqual(agent_runtime_of(""), "claude")
        self.assertEqual(agent_runtime_of("{}"), "claude")
        self.assertEqual(agent_runtime_of('{"runtime":null}'), "claude")

    def test_cursor(self):
        self.assertEqual(agent_runtime_of('{"runtime":"cursor"}'), "cursor")

    def test_junk_and_unknown(self):
        self.assertEqual(agent_runtime_of("{bad"), "claude")
        self.assertEqual(agent_runtime_of('{"runtime":"codex"}'), "claude")


class RuntimeBadgeTests(unittest.TestCase):
    def test_claude_chip(self):
        h = str(runtime_badge(None))
        self.assertIn("rt-claude", h)
        self.assertIn("claude", h)

    def test_cursor_chip(self):
        h = str(runtime_badge("cursor"))
        self.assertIn("rt-cursor", h)
        self.assertNotIn("rt-claude", h)

    def test_unknown_falls_back(self):
        h = str(runtime_badge("nope"))
        self.assertIn("rt-claude", h)


if __name__ == "__main__":
    unittest.main()
